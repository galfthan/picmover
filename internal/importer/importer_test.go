package importer

import (
	"archive/zip"
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/metadata"
	"picmover/internal/testimg"
	"picmover/internal/thumbs"
)

type env struct {
	t   *testing.T
	src string
	lib *library.Library
	db  *catalog.DB
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base := t.TempDir()
	return newEnvAt(t, filepath.Join(base, "src"), filepath.Join(base, "lib"))
}

func newEnvAt(t *testing.T, src, libDir string) *env {
	t.Helper()
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	lib, err := library.Open(libDir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := catalog.Open(context.Background(), lib.DBPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &env{t: t, src: src, lib: lib, db: db}
}

func writeFile(t *testing.T, p string, data []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func jpegAt(t time.Time, seed byte) []byte {
	return testimg.JPEG(testimg.Options{DateTime: t, Seed: seed})
}

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, time.UTC) }

type collector struct {
	mu      sync.Mutex
	results []Result
}

func (c *collector) add(r Result) {
	c.mu.Lock()
	c.results = append(c.results, r)
	c.mu.Unlock()
}

func (c *collector) byStatus(s Status) []Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Result
	for _, r := range c.results {
		if r.Status == s {
			out = append(out, r)
		}
	}
	return out
}

func (e *env) opts() Options {
	return Options{Source: e.src, Workers: 4, Extractor: metadata.Default{DefaultZone: time.UTC}}
}

func (e *env) run(o Options) (Stats, *collector, error) {
	e.t.Helper()
	c := &collector{}
	prev := o.OnResult
	o.OnResult = func(r Result) {
		c.add(r)
		if prev != nil {
			prev(r)
		}
	}
	st, err := New(e.lib, e.db, o).Run(context.Background())
	return st, c, err
}

func (e *env) mustRun(o Options) (Stats, *collector) {
	e.t.Helper()
	st, c, err := e.run(o)
	if err != nil {
		e.t.Fatalf("Run: %v", err)
	}
	for _, r := range c.byStatus(StatusError) {
		e.t.Errorf("unexpected error result: %+v", r)
	}
	return st, c
}

func (e *env) rows() map[string]catalog.Media {
	e.t.Helper()
	ms, err := e.db.List(context.Background(), catalog.ListOptions{})
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]catalog.Media{}
	for _, m := range ms {
		out[m.RelPath] = m
	}
	return out
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func (e *env) assertFiles(rels ...string) {
	e.t.Helper()
	for _, r := range rels {
		if !exists(e.lib.Abs(r)) {
			e.t.Errorf("missing library file %s", r)
		}
	}
}

// libraryMedia lists the files under the media directories of the library.
func (e *env) libraryMedia() []string {
	e.t.Helper()
	var out []string
	for _, top := range []string{"image", "image_raw", "video"} {
		root := filepath.Join(e.lib.Root(), top)
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := e.lib.Rel(p)
				out = append(out, rel)
			}
			return nil
		})
	}
	slices.Sort(out)
	return out
}

func (e *env) assertTempEmpty() {
	e.t.Helper()
	ents, err := os.ReadDir(e.lib.TempDir())
	if err != nil {
		e.t.Fatal(err)
	}
	for _, d := range ents {
		e.t.Errorf("temp dir not empty: %s", d.Name())
	}
}

func TestBasicCopyImport(t *testing.T) {
	e := newEnv(t)
	a := writeFile(t, filepath.Join(e.src, "a.jpg"), testimg.JPEG(testimg.Options{
		DateTime: time.Date(2020, 3, 5, 10, 11, 12, 0, time.UTC), Make: "Apple", Model: "iPhone 12",
		GPS: true, Lat: 60.1699, Lon: 24.9384, Seed: 1,
	}))
	b := writeFile(t, filepath.Join(e.src, "b.png"), testimg.PNG(16, 16, 2))
	mt := time.Date(2019, 12, 31, 23, 30, 0, 0, time.UTC)
	if err := os.Chtimes(b, mt, mt); err != nil {
		t.Fatal(err)
	}
	c := writeFile(t, filepath.Join(e.src, "sub", "c.jpg"), jpegAt(date(2021, 7, 1), 3))
	writeFile(t, filepath.Join(e.src, "notes.txt"), []byte("hello"))
	writeFile(t, filepath.Join(e.src, ".DS_Store"), []byte("junk"))

	var progressCalls int
	o := e.opts()
	o.OnProgress = func(Stats) { progressCalls++ }
	st, _ := e.mustRun(o)
	if st.Imported != 3 || st.NonMedia != 1 || st.Scanned != 4 || st.Errors != 0 || st.Duplicates != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if st.BytesCopied == 0 {
		t.Errorf("BytesCopied = 0")
	}
	if progressCalls == 0 {
		t.Errorf("OnProgress never called")
	}
	e.assertFiles("image/2020/03/a.jpg", "image/2019/12/b.png", "image/2021/07/c.jpg")
	for _, p := range []string{a, b, c} {
		if !exists(p) {
			t.Errorf("source %s removed in copy mode", p)
		}
	}
	e.assertTempEmpty()

	rows := e.rows()
	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
	ra := rows["image/2020/03/a.jpg"]
	if ra.CameraType != "phone" || ra.CameraMake != "Apple" || !ra.HasGPS ||
		math.Abs(ra.Lat-60.1699) > 1e-3 || math.Abs(ra.Lon-24.9384) > 1e-3 ||
		ra.TakenAtSource != "exif" || ra.Ext != "jpg" || ra.FileType != "image" || ra.OriginalPath != a {
		t.Errorf("row a = %+v", ra)
	}
	if fi, _ := os.Stat(a); ra.Size != fi.Size() {
		t.Errorf("size = %d, want %d", ra.Size, fi.Size())
	}
	rb := rows["image/2019/12/b.png"]
	if rb.TakenAtSource != "mtime" || rb.Ext != "png" || rb.Width != 16 || rb.HasGPS {
		t.Errorf("row b = %+v", rb)
	}
	if ra.ImportSessionID == 0 || rb.ImportSessionID != ra.ImportSessionID {
		t.Errorf("session ids %d %d", ra.ImportSessionID, rb.ImportSessionID)
	}

	var n, imported, nonMedia int64
	var mode string
	if err := e.db.QueryRow("SELECT COUNT(*), MAX(imported), MAX(non_media), MAX(mode) FROM import_sessions WHERE finished_at IS NOT NULL").
		Scan(&n, &imported, &nonMedia, &mode); err != nil {
		t.Fatal(err)
	}
	if n != 1 || imported != 3 || nonMedia != 1 || mode != "copy" {
		t.Errorf("session: n=%d imported=%d non_media=%d mode=%s", n, imported, nonMedia, mode)
	}

	// Second run: everything is a duplicate pointing at the existing rows.
	st, col := e.mustRun(e.opts())
	if st.Duplicates != 3 || st.Imported != 0 || st.Existing != 0 {
		t.Fatalf("rerun stats = %+v", st)
	}
	want := map[string]string{a: "image/2020/03/a.jpg", b: "image/2019/12/b.png", c: "image/2021/07/c.jpg"}
	for _, r := range col.byStatus(StatusDuplicate) {
		if want[r.Source] != r.Dest {
			t.Errorf("duplicate %s: Dest = %q, want %q", r.Source, r.Dest, want[r.Source])
		}
	}
	if len(e.libraryMedia()) != 3 {
		t.Errorf("library files = %v", e.libraryMedia())
	}
}

func TestSameNameDifferentContent(t *testing.T) {
	e := newEnv(t)
	writeFile(t, filepath.Join(e.src, "x", "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	writeFile(t, filepath.Join(e.src, "y", "a.jpg"), jpegAt(date(2020, 3, 20), 2))
	st, _ := e.mustRun(e.opts())
	if st.Imported != 2 {
		t.Fatalf("stats = %+v", st)
	}
	e.assertFiles("image/2020/03/a.jpg", "image/2020/03/a_1.jpg")
	rows := e.rows()
	if _, ok := rows["image/2020/03/a.jpg"]; !ok {
		t.Errorf("a.jpg not in catalog: %v", rows)
	}
	if _, ok := rows["image/2020/03/a_1.jpg"]; !ok {
		t.Errorf("a_1.jpg not in catalog: %v", rows)
	}
}

func TestSameContentTwoNames(t *testing.T) {
	for _, workers := range []int{1, 4} {
		t.Run(strings.Repeat("w", workers), func(t *testing.T) {
			e := newEnv(t)
			data := jpegAt(date(2020, 3, 5), 1)
			writeFile(t, filepath.Join(e.src, "one.jpg"), data)
			writeFile(t, filepath.Join(e.src, "two.jpg"), data)
			o := e.opts()
			o.Workers = workers
			st, _ := e.mustRun(o)
			if st.Imported != 1 || st.Duplicates != 1 {
				t.Fatalf("stats = %+v", st)
			}
			if got := e.libraryMedia(); len(got) != 1 || len(e.rows()) != 1 {
				t.Errorf("library = %v, rows = %d", got, len(e.rows()))
			}
		})
	}
}

func TestMinDimension(t *testing.T) {
	e := newEnv(t)
	writeFile(t, filepath.Join(e.src, "small.jpg"), jpegAt(date(2020, 3, 5), 1))
	writeFile(t, filepath.Join(e.src, "big.jpg"), testimg.JPEG(testimg.Options{Width: 400, Height: 400, DateTime: date(2020, 3, 5), Seed: 2}))
	o := e.opts()
	o.MinDimension = 100
	st, col := e.mustRun(o)
	if st.SkippedSmall != 1 || st.Imported != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if s := col.byStatus(StatusSkippedSmall); len(s) != 1 || filepath.Base(s[0].Source) != "small.jpg" {
		t.Errorf("skipped = %+v", s)
	}
	e.assertFiles("image/2020/03/big.jpg")
}

func TestMove(t *testing.T) {
	for _, force := range []bool{false, true} {
		name := "rename"
		if force {
			name = "forcecopy"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			a := writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
			b := writeFile(t, filepath.Join(e.src, "d", "b.jpg"), jpegAt(date(2021, 4, 5), 2))
			o := e.opts()
			o.Move, o.ForceCopy = true, force
			st, _ := e.mustRun(o)
			if st.Imported != 2 {
				t.Fatalf("stats = %+v", st)
			}
			if exists(a) || exists(b) {
				t.Errorf("sources still present")
			}
			e.assertFiles("image/2020/03/a.jpg", "image/2021/04/b.jpg")
			if len(e.rows()) != 2 {
				t.Errorf("rows = %v", e.rows())
			}
			if force && st.BytesCopied == 0 {
				t.Errorf("ForceCopy did not copy")
			}
			if !force && st.BytesCopied != 0 {
				t.Errorf("rename copied %d bytes", st.BytesCopied)
			}
			e.assertTempEmpty()
		})
	}
}

func TestZip(t *testing.T) {
	e := newEnv(t)
	zipPath := filepath.Join(e.src, "photos.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, data := range map[string][]byte{
		"a.jpg":            jpegAt(date(2020, 3, 5), 1),
		"notes.txt":        []byte("hi"),
		"__MACOSX/._a.jpg": []byte("junk"),
		"dir/":             nil,
	} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: date(2022, 1, 1)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	o := e.opts()
	o.Move = true
	st, _ := e.mustRun(o)
	if st.Imported != 1 || st.NonMedia != 1 || st.Scanned != 2 {
		t.Fatalf("stats = %+v", st)
	}
	rows := e.rows()
	m, ok := rows["image/2020/03/a.jpg"]
	if !ok || m.OriginalPath != zipPath+"!/a.jpg" {
		t.Fatalf("rows = %+v", rows)
	}
	if !exists(zipPath) {
		t.Errorf("zip removed with Move")
	}
	e.assertTempEmpty()
}

func TestLibraryOntoItself(t *testing.T) {
	root := filepath.Join(t.TempDir(), "lib")
	e := newEnvAt(t, root, root)
	inPlace := writeFile(t, e.lib.Abs("image/2020/03/a.jpg"), jpegAt(date(2020, 3, 5), 1))
	old := writeFile(t, e.lib.Abs("old/2012/x.jpg"), jpegAt(date(2012, 5, 6), 2))
	// A v1 backup name is internal (IsInternal matches exact names only).
	// media.db-journal would be internal too, but SQLite treats a non-empty
	// journal next to the catalog as a hot journal and the test would hang.
	bak := writeFile(t, e.lib.Abs("media.db.v1-20130101-000000.bak"), jpegAt(date(2013, 1, 1), 3))
	logImg := writeFile(t, filepath.Join(e.lib.LogDir(), "stale.jpg"), jpegAt(date(2014, 1, 1), 4))
	stale := writeFile(t, filepath.Join(e.lib.TempDir(), "stale.part"), []byte("partial"))

	o := e.opts()
	o.Move = true
	st, _ := e.mustRun(o)
	if st.Existing != 1 || st.Imported != 1 || st.Scanned != 2 || st.NonMedia != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if !exists(inPlace) {
		t.Errorf("in-place file removed")
	}
	if exists(old) {
		t.Errorf("old-layout file not moved")
	}
	e.assertFiles("image/2012/05/x.jpg")
	for _, p := range []string{bak, logImg} {
		if !exists(p) {
			t.Errorf("internal file %s touched", p)
		}
	}
	if exists(stale) {
		t.Errorf("stale temp file not cleaned")
	}
	rows := e.rows()
	if len(rows) != 2 {
		t.Errorf("rows = %v", rows)
	}
	if got := e.libraryMedia(); !slices.Equal(got, []string{"image/2012/05/x.jpg", "image/2020/03/a.jpg"}) {
		t.Errorf("library = %v", got)
	}

	// Running again changes nothing.
	st, _ = e.mustRun(o)
	if st.Duplicates != 2 || st.Imported != 0 || st.Existing != 0 {
		t.Errorf("rerun stats = %+v", st)
	}
}

func TestIdenticalAtDestination(t *testing.T) {
	for _, move := range []bool{false, true} {
		t.Run(map[bool]string{false: "copy", true: "move"}[move], func(t *testing.T) {
			e := newEnv(t)
			data := jpegAt(date(2020, 3, 5), 1)
			src := writeFile(t, filepath.Join(e.src, "a.jpg"), data)
			writeFile(t, e.lib.Abs("image/2020/03/a.jpg"), data)
			o := e.opts()
			o.Move = move
			st, _ := e.mustRun(o)
			if st.Existing != 1 || st.Imported != 0 || st.BytesCopied != 0 {
				t.Fatalf("stats = %+v", st)
			}
			if _, ok := e.rows()["image/2020/03/a.jpg"]; !ok {
				t.Errorf("row missing")
			}
			if exists(src) == move {
				t.Errorf("source exists=%v with move=%v", exists(src), move)
			}
			if got := e.libraryMedia(); len(got) != 1 {
				t.Errorf("library = %v", got)
			}
		})
	}
}

func TestCancellation(t *testing.T) {
	e := newEnv(t)
	for i := range 40 {
		writeFile(t, filepath.Join(e.src, "f"+string(rune('a'+i/10))+string(rune('0'+i%10))+".jpg"),
			jpegAt(date(2020, 3, 5), byte(i)))
	}
	// Sorted last: the scanner reports it (the first result) while workers are
	// still busy with the jpegs, so cancellation hits a pipeline in motion.
	writeFile(t, filepath.Join(e.src, "zzz.txt"), []byte("x"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := e.opts()
	o.OnResult = func(Result) { cancel() }
	st, err := New(e.lib, e.db, o).Run(ctx)
	t.Logf("stats after cancel: %+v", st)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v", err)
	}
	rows := e.rows()
	for rel := range rows {
		if !exists(e.lib.Abs(rel)) {
			t.Errorf("catalog row %s has no file", rel)
		}
	}
	files := e.libraryMedia()
	if int64(len(files)) != st.Imported+st.Existing || len(files) != len(rows) {
		t.Errorf("files on disk %d, rows %d, stats %+v", len(files), len(rows), st)
	}
	if done := st.Imported + st.Existing + st.Duplicates + st.SkippedSmall + st.NonMedia + st.Errors; done < 1 || done > st.Scanned || st.Scanned > 41 {
		t.Errorf("inconsistent stats %+v", st)
	}
	if st.Errors != 0 {
		t.Errorf("cancellation produced errors: %+v", st)
	}
	e.assertTempEmpty()
}

func TestUnreadableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permissions not enforced")
	}
	e := newEnv(t)
	writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	locked := filepath.Join(e.src, "locked")
	writeFile(t, filepath.Join(locked, "b.jpg"), jpegAt(date(2020, 3, 5), 2))
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	st, col, err := e.run(e.opts())
	if err != nil {
		t.Fatal(err)
	}
	errs := col.byStatus(StatusError)
	if len(errs) != 1 || errs[0].Source != locked || st.Errors != 1 {
		t.Errorf("errors = %+v", errs)
	}
	if st.Imported != 1 {
		t.Errorf("stats = %+v", st)
	}
}

func TestMissingSource(t *testing.T) {
	e := newEnv(t)
	o := e.opts()
	o.Source = filepath.Join(e.src, "nope")
	if _, err := New(e.lib, e.db, o).Run(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestSingleFileSource(t *testing.T) {
	e := newEnv(t)
	a := writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	o := e.opts()
	o.Source = a
	st, _ := e.mustRun(o)
	if st.Imported != 1 || st.Scanned != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

// pendingCopy makes a library file as a worker would and returns its pending item.
func pendingCopy(t *testing.T, e *env, rel string, data []byte) *pending {
	t.Helper()
	src := writeFile(t, filepath.Join(e.src, filepath.Base(rel)), data)
	writeFile(t, e.lib.Abs(rel), data)
	h, size, err := library.HashFile(src)
	if err != nil {
		t.Fatal(err)
	}
	return &pending{
		media:  catalog.Media{Hash: h, Size: size, RelPath: rel, FileType: "image", Ext: "jpg"},
		status: StatusImported, src: src, origin: src, hash: h, copied: true, deleteSrc: true,
	}
}

func TestCommitLostHashRaceUndoesCopy(t *testing.T) {
	e := newEnv(t)
	data := jpegAt(date(2020, 3, 5), 1)
	p := pendingCopy(t, e, "image/2020/03/a_1.jpg", data)
	if _, err := e.db.InsertMedia(context.Background(), nil, &catalog.Media{Hash: p.hash, Size: p.media.Size, RelPath: "image/2020/03/a.jpg", FileType: "image", Ext: "jpg"}); err != nil {
		t.Fatal(err)
	}
	c := &collector{}
	im := New(e.lib, e.db, Options{OnResult: c.add})
	if err := im.commit([]*pending{p}); err != nil {
		t.Fatal(err)
	}
	if exists(e.lib.Abs(p.media.RelPath)) {
		t.Errorf("copy not undone")
	}
	if !exists(p.src) {
		t.Errorf("source deleted after lost race")
	}
	if d := c.byStatus(StatusDuplicate); len(d) != 1 || d[0].Dest != "image/2020/03/a.jpg" {
		t.Errorf("results = %+v", c.results)
	}
}

func TestCommitFailureUndoesBatch(t *testing.T) {
	e := newEnv(t)
	p := pendingCopy(t, e, "image/2020/03/a.jpg", jpegAt(date(2020, 3, 5), 1))
	e.db.Close()
	c := &collector{}
	im := New(e.lib, e.db, Options{OnResult: c.add})
	pend := make(chan *pending, 1)
	pend <- p
	close(pend)
	if err := im.writer(pend); err == nil {
		t.Fatal("expected writer error")
	}
	if exists(e.lib.Abs(p.media.RelPath)) {
		t.Errorf("copy not undone")
	}
	if !exists(p.src) {
		t.Errorf("source deleted although not recorded")
	}
	if len(c.byStatus(StatusError)) != 1 {
		t.Errorf("results = %+v", c.results)
	}
}

func TestCommitLostHashRaceUndoesMove(t *testing.T) {
	e := newEnv(t)
	p := pendingCopy(t, e, "image/2020/03/a_1.jpg", jpegAt(date(2020, 3, 5), 1))
	os.Remove(p.src) // as after a rename
	p.copied, p.moved, p.deleteSrc = false, true, false
	if _, err := e.db.InsertMedia(context.Background(), nil, &catalog.Media{Hash: p.hash, Size: p.media.Size, RelPath: "image/2020/03/a.jpg", FileType: "image", Ext: "jpg"}); err != nil {
		t.Fatal(err)
	}
	im := New(e.lib, e.db, Options{})
	if err := im.commit([]*pending{p}); err != nil {
		t.Fatal(err)
	}
	if exists(e.lib.Abs(p.media.RelPath)) || !exists(p.src) {
		t.Errorf("move not undone")
	}
	if st := im.Stats(); st.Duplicates != 1 {
		t.Errorf("stats = %+v", st)
	}
}

// runBounded runs an import and fails the test if it does not return
// promptly, so a regression to an unbounded name search fails instead of
// hanging the suite.
func (e *env) runBounded(o Options) (Stats, *collector, error) {
	e.t.Helper()
	type out struct {
		st  Stats
		c   *collector
		err error
	}
	done := make(chan out, 1)
	go func() {
		st, c, err := e.run(o)
		done <- out{st, c, err}
	}()
	select {
	case r := <-done:
		return r.st, r.c, r.err
	case <-time.After(10 * time.Second):
		e.t.Fatal("Run did not return: destination search does not terminate")
		return Stats{}, nil, nil
	}
}

// relPathCounts counts catalog rows per rel path (rows() would collapse
// duplicates).
func (e *env) relPathCounts() map[string]int {
	e.t.Helper()
	ms, err := e.db.List(context.Background(), catalog.ListOptions{})
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]int{}
	for _, m := range ms {
		out[m.RelPath]++
	}
	return out
}

func (e *env) assertUniqueRelPaths() {
	e.t.Helper()
	for rel, n := range e.relPathCounts() {
		if n > 1 {
			e.t.Errorf("%d catalog rows share rel_path %s", n, rel)
		}
	}
}

// A file where the layout directory should be makes the destination
// directory unreadable (ENOTDIR): one error result, no endless name search.
func TestNoHangWhenLayoutDirIsAFile(t *testing.T) {
	e := newEnv(t)
	writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	writeFile(t, filepath.Join(e.lib.Root(), "image", "2020", "03"), []byte("not a dir"))
	st, c, err := e.runBounded(e.opts())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Errors != 1 || st.Imported != 0 || len(c.byStatus(StatusError)) != 1 {
		t.Errorf("stats = %+v, results = %+v", st, c.results)
	}
	if len(e.rows()) != 0 {
		t.Errorf("rows = %v", e.rows())
	}
}

// A 255-byte name that collides: every _N variant is too long, which is an
// error result rather than an endless search.
func TestNoHangWhenNameTooLong(t *testing.T) {
	e := newEnv(t)
	name := strings.Repeat("x", 251) + ".jpg" // 255 bytes
	src := writeFile(t, filepath.Join(e.src, name), jpegAt(date(2020, 3, 5), 1))
	writeFile(t, filepath.Join(e.lib.Root(), "image", "2020", "03", name), []byte("other content"))
	o := e.opts()
	o.Move = true
	st, c, err := e.runBounded(o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Errors != 1 || st.Imported != 0 || len(c.byStatus(StatusError)) != 1 {
		t.Errorf("stats = %+v, results = %+v", st, c.results)
	}
	if !exists(src) {
		t.Errorf("source removed after failed import")
	}
}

// A case variant already on disk (not in the catalog) is a collision, for
// ASCII and non-ASCII names alike, because the name would clash on
// case-insensitive file systems.
func TestCaseVariantOnDiskIsCollision(t *testing.T) {
	e := newEnv(t)
	writeFile(t, e.lib.Abs("image/2020/03/A.jpg"), []byte("orphan"))
	writeFile(t, e.lib.Abs("image/2020/03/Ä.jpg"), []byte("orphan"))
	writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	writeFile(t, filepath.Join(e.src, "ä.jpg"), jpegAt(date(2020, 3, 5), 2))
	st, _ := e.mustRun(e.opts())
	if st.Imported != 2 {
		t.Fatalf("stats = %+v", st)
	}
	want := []string{"image/2020/03/A.jpg", "image/2020/03/a_1.jpg", "image/2020/03/Ä.jpg", "image/2020/03/ä_1.jpg"}
	slices.Sort(want)
	if got := e.libraryMedia(); !slices.Equal(got, want) {
		t.Errorf("library = %v, want %v", got, want)
	}
}

// A non-ASCII case variant that is only in the catalog (its file is gone)
// is not detected: SQLite NOCASE folds ASCII letters only. Known limitation.
func TestNonASCIICaseVariantCatalogOnly(t *testing.T) {
	t.Skip("known limitation: SQLite NOCASE folds ASCII only, so Ä.jpg in the catalog does not block ä.jpg when no file is on disk")
}

// The file at a catalogued path was edited in place; importing a copy of the
// edited file must not reuse that path as "identical" and add a second row
// with the same rel_path.
func TestIdenticalFileNeverReusesCataloguedPath(t *testing.T) {
	e := newEnv(t)
	writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	e.mustRun(e.opts())
	edited := jpegAt(date(2020, 3, 5), 2)
	if err := os.WriteFile(e.lib.Abs("image/2020/03/a.jpg"), edited, 0o644); err != nil {
		t.Fatal(err)
	}
	src2 := t.TempDir()
	writeFile(t, filepath.Join(src2, "a.jpg"), edited)
	o := e.opts()
	o.Source = src2
	o.Move = true
	st, _ := e.mustRun(o)
	if st.Imported != 1 || st.Existing != 0 {
		t.Errorf("stats = %+v", st)
	}
	e.assertUniqueRelPaths()
	if _, ok := e.rows()["image/2020/03/a_1.jpg"]; !ok {
		t.Errorf("rows = %v, want a_1.jpg", e.relPathCounts())
	}
}

// A library file whose catalogued path is recorded with another hash (edited
// in place) is not "in place" when the library is imported onto itself.
func TestInPlaceNeverReusesCataloguedPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "lib")
	e := newEnvAt(t, root, root)
	writeFile(t, e.lib.Abs("image/2020/03/a.jpg"), jpegAt(date(2020, 3, 5), 1))
	e.mustRun(e.opts())
	if err := os.WriteFile(e.lib.Abs("image/2020/03/a.jpg"), jpegAt(date(2020, 3, 5), 2), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ := e.mustRun(e.opts())
	if st.Existing != 0 || st.Imported != 1 {
		t.Errorf("stats = %+v", st)
	}
	e.assertUniqueRelPaths()
	if _, ok := e.rows()["image/2020/03/a_1.jpg"]; !ok {
		t.Errorf("rows = %v, want a_1.jpg", e.relPathCounts())
	}
}

// A symlink at the destination pointing at the source is not "in place":
// the catalog must never record a symlink into the source tree.
func TestInPlaceIgnoresSymlink(t *testing.T) {
	e := newEnv(t)
	src := writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	dst := e.lib.Abs("image/2020/03/a.jpg")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, dst); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	o := e.opts()
	o.Move = true
	st, _ := e.mustRun(o)
	if st.Imported != 1 || st.Existing != 0 {
		t.Errorf("stats = %+v", st)
	}
	rows := e.rows()
	if _, ok := rows["image/2020/03/a.jpg"]; ok || len(rows) != 1 {
		t.Fatalf("rows = %v, want only a_1.jpg", rows)
	}
	fi, err := os.Lstat(e.lib.Abs("image/2020/03/a_1.jpg"))
	if err != nil || !fi.Mode().IsRegular() {
		t.Errorf("a_1.jpg: %v, %v; want a regular file", fi, err)
	}
}

// A source given as a symlink to a directory is walked.
func TestSymlinkedSourceDirIsWalked(t *testing.T) {
	e := newEnv(t)
	writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	link := filepath.Join(t.TempDir(), "srclink")
	if err := os.Symlink(e.src, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	o := e.opts()
	o.Source = link
	st, _ := e.mustRun(o)
	if st.Imported != 1 {
		t.Errorf("stats = %+v, want 1 imported", st)
	}
	e.assertFiles("image/2020/03/a.jpg")
}

// --- Thumbnail stage ---

func openStore(t *testing.T, lib *library.Library) *thumbs.Store {
	t.Helper()
	s, err := thumbs.OpenStore(lib)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// assertThumbOK checks the catalog row, the thumbs.db row and the preview
// file of one imported file.
func assertThumbOK(t *testing.T, store *thumbs.Store, m catalog.Media) {
	t.Helper()
	if m.ThumbState != catalog.ThumbStateOK || m.ThumbError != "" || m.PHash == 0 || m.ThumbAt.IsZero() {
		t.Errorf("%s: state %q error %q phash %x at %v", m.RelPath, m.ThumbState, m.ThumbError, m.PHash, m.ThumbAt)
	}
	thumb, preview, err := store.Has(context.Background(), m.Hash)
	if err != nil || !thumb || !preview {
		t.Errorf("%s: cache thumb=%v preview=%v err=%v", m.RelPath, thumb, preview, err)
	}
	if fi, err := os.Stat(store.PreviewPath(m.Hash)); err != nil || fi.Size() == 0 {
		t.Errorf("%s: preview file: %v", m.RelPath, err)
	}
}

func TestImportGeneratesThumbs(t *testing.T) {
	e := newEnv(t)
	writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	writeFile(t, filepath.Join(e.src, "b.png"), testimg.PNG(40, 30, 2))
	writeFile(t, filepath.Join(e.src, "c.webp"), testimg.WebP(48, 32, 3))
	var last Stats
	o := e.opts()
	o.OnProgress = func(s Stats) { last = s }
	st, _ := e.mustRun(o)
	if st.Imported != 3 || st.ThumbsDone != 3 || st.ThumbsUnsupported != 0 || st.ThumbsFailed != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if last.ThumbsDone != 3 {
		t.Errorf("last progress = %+v", last)
	}
	store := openStore(t, e.lib)
	rows := e.rows()
	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
	for _, m := range rows {
		assertThumbOK(t, store, m)
	}
	if n, _, err := store.Stats(context.Background()); err != nil || n != 3 {
		t.Errorf("store count = %d %v", n, err)
	}
	// A rerun imports nothing and has no thumbnails to make.
	st, _ = e.mustRun(e.opts())
	if st.ThumbsDone+st.ThumbsUnsupported+st.ThumbsFailed != 0 {
		t.Errorf("rerun stats = %+v", st)
	}
}

func TestNoThumbsThenRunThumbs(t *testing.T) {
	e := newEnv(t)
	for i := range 3 {
		writeFile(t, filepath.Join(e.src, "f"+string(rune('a'+i))+".jpg"), jpegAt(date(2020, 3, 5), byte(i)))
	}
	writeFile(t, filepath.Join(e.src, "garbage.jpg"), []byte("this is not a JPEG at all"))
	o := e.opts()
	o.NoThumbs = true
	st, c, err := e.run(o)
	if err != nil || st.Imported != 4 || st.ThumbsDone+st.ThumbsFailed != 0 {
		t.Fatalf("Run = %+v, %v (results %+v)", st, err, c.results)
	}
	if p, ok, _, _, err := e.db.ThumbStats(context.Background()); err != nil || p != 4 || ok != 0 {
		t.Fatalf("ThumbStats after --no-thumbs: pending %d ok %d %v", p, ok, err)
	}
	if _, err := os.Stat(filepath.Join(e.lib.Root(), library.MetaDir, thumbs.DBName)); err == nil {
		t.Errorf("thumbs.db created with NoThumbs")
	}

	st, err = New(e.lib, e.db, Options{ThumbWorkers: 2}).RunThumbs(context.Background(), 0, false)
	if err != nil || st.ThumbsDone != 3 || st.ThumbsFailed != 1 || st.ThumbsUnsupported != 0 || st.Imported != 0 {
		t.Fatalf("RunThumbs = %+v, %v", st, err)
	}
	store := openStore(t, e.lib)
	for rel, m := range e.rows() {
		if strings.HasSuffix(rel, "garbage.jpg") {
			if m.ThumbState != catalog.ThumbStateError || m.ThumbError == "" || m.PHash != 0 {
				t.Errorf("garbage: state %q error %q phash %x", m.ThumbState, m.ThumbError, m.PHash)
			}
			continue
		}
		assertThumbOK(t, store, m)
	}
	// Nothing pending: a second pass only retries the failed row; force
	// redoes every row.
	if st, err := New(e.lib, e.db, Options{}).RunThumbs(context.Background(), 0, false); err != nil ||
		st.ThumbsDone+st.ThumbsUnsupported != 0 || st.ThumbsFailed != 1 {
		t.Errorf("second RunThumbs = %+v, %v", st, err)
	}
	if st, err := New(e.lib, e.db, Options{}).RunThumbs(context.Background(), 0, true); err != nil ||
		st.ThumbsDone != 3 || st.ThumbsFailed != 1 {
		t.Errorf("forced RunThumbs = %+v, %v", st, err)
	}
	// ThumbLimit bounds the rows processed.
	if st, err := New(e.lib, e.db, Options{ThumbLimit: 2}).RunThumbs(context.Background(), 0, true); err != nil ||
		st.ThumbsDone+st.ThumbsFailed != 2 {
		t.Errorf("limited RunThumbs = %+v, %v", st, err)
	}
}

func TestThumbsCancellationLeavesPending(t *testing.T) {
	e := newEnv(t)
	for i := range 30 {
		writeFile(t, filepath.Join(e.src, "f"+string(rune('a'+i/10))+string(rune('0'+i%10))+".jpg"),
			testimg.JPEG(testimg.Options{Width: 400, Height: 300, DateTime: date(2020, 3, 5), Seed: byte(i)}))
	}
	// With the 500 ms interval, 30 small thumbnails are often all
	// committed by the first flush, after which a cancel comes too late.
	thumbBatchInterval = 5 * time.Millisecond
	t.Cleanup(func() { thumbBatchInterval = batchInterval })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := e.opts()
	o.ThumbWorkers = 1
	o.ProgressInterval = time.Millisecond
	o.OnProgress = func(s Stats) {
		if s.ThumbsDone > 0 {
			cancel()
		}
	}
	st, err := New(e.lib, e.db, o).Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v (stats %+v)", err, st)
	}
	if st.Imported != 30 || st.ThumbsFailed != 0 || st.ThumbsUnsupported != 0 {
		t.Fatalf("stats = %+v", st)
	}
	pending, ok, unsupported, failed, err := e.db.ThumbStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("after cancel: stats %+v; pending %d ok %d", st, pending, ok)
	if pending == 0 || ok == 0 || unsupported+failed != 0 || pending+ok != 30 || ok != st.ThumbsDone {
		t.Fatalf("ThumbStats = pending %d ok %d unsupported %d failed %d; stats %+v", pending, ok, unsupported, failed, st)
	}
	e.assertTempEmpty()

	// A later pass finishes the rest.
	st, err = New(e.lib, e.db, Options{}).RunThumbs(context.Background(), 0, false)
	if err != nil || st.ThumbsDone != pending {
		t.Fatalf("RunThumbs = %+v, %v; want %d done", st, err, pending)
	}
	if p, ok, _, _, _ := e.db.ThumbStats(context.Background()); p != 0 || ok != 30 {
		t.Errorf("after RunThumbs: pending %d ok %d", p, ok)
	}
}

// Rebuilding a catalog (import of the library onto itself into a new
// media.db) reuses the cache and reproduces the perceptual hashes.
func TestRebuildReusesThumbCache(t *testing.T) {
	root := filepath.Join(t.TempDir(), "lib")
	e := newEnvAt(t, root, root)
	writeFile(t, e.lib.Abs("image/2020/03/a.jpg"), jpegAt(date(2020, 3, 5), 1))
	writeFile(t, e.lib.Abs("image/2021/07/b.png"), testimg.PNG(40, 30, 2))
	if err := os.Chtimes(e.lib.Abs("image/2021/07/b.png"), date(2021, 7, 1), date(2021, 7, 1)); err != nil {
		t.Fatal(err)
	}
	st, _ := e.mustRun(e.opts())
	if st.Existing != 2 || st.ThumbsDone != 2 {
		t.Fatalf("first run = %+v", st)
	}
	before := e.rows()
	store := openStore(t, e.lib)
	mtimes := map[string]time.Time{}
	for rel, m := range before {
		fi, err := os.Stat(store.PreviewPath(m.Hash))
		if err != nil {
			t.Fatal(err)
		}
		mtimes[rel] = fi.ModTime()
	}

	e.db.Close()
	for _, s := range []string{"", "-wal", "-shm"} {
		os.Remove(e.lib.DBPath() + s)
	}
	db, err := catalog.Open(context.Background(), e.lib.DBPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e.db = db
	time.Sleep(10 * time.Millisecond)
	st, _ = e.mustRun(e.opts())
	if st.Existing != 2 || st.ThumbsDone != 2 {
		t.Fatalf("rebuild = %+v", st)
	}
	after := e.rows()
	for rel, m := range before {
		a, ok := after[rel]
		if !ok {
			t.Errorf("%s missing after rebuild", rel)
			continue
		}
		if a.Hash != m.Hash || a.PHash != m.PHash || a.Width != m.Width || a.Height != m.Height || a.ThumbState != catalog.ThumbStateOK {
			t.Errorf("%s: before %+v after %+v", rel, m, a)
		}
		if fi, err := os.Stat(store.PreviewPath(m.Hash)); err != nil || !fi.ModTime().Equal(mtimes[rel]) {
			t.Errorf("%s: preview rewritten (%v)", rel, err)
		}
	}
}
