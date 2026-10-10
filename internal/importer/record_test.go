package importer

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"picmover/internal/catalog"
	"picmover/internal/thumbs"
)

// RecordInPlace records every media file of the library where it is, in
// any folder, and never copies, moves or deletes anything.
func TestRecordInPlace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "lib")
	e := newEnvAt(t, root, root)
	writeFile(t, e.lib.Abs("image/2020/03/a.jpg"), jpegAt(date(2020, 3, 5), 1))
	writeFile(t, e.lib.Abs("image/2019/01/wrongfolder.jpg"), jpegAt(date(2020, 3, 5), 2))
	writeFile(t, e.lib.Abs("old/2012/x.jpg"), jpegAt(date(2012, 5, 6), 3))
	writeFile(t, e.lib.Abs("old/2012/copy-of-x.jpg"), jpegAt(date(2012, 5, 6), 3))
	writeFile(t, e.lib.Abs("media.db.bak-holiday.jpg"), jpegAt(date(2013, 1, 1), 4))
	writeZip(t, e.lib.Abs("archive.zip"), map[string][]byte{"z.jpg": jpegAt(date(2014, 1, 1), 5)})
	before := allFiles(t, root)

	o := e.opts()
	o.RecordInPlace = true
	o.Move = true // ignored
	o.NoThumbs = true
	st, c := e.mustRun(o)
	if st.Existing != 4 || st.Imported != 0 || st.Duplicates != 1 || st.Errors != 0 || st.NonMedia != 1 || st.BytesCopied != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if after := allFiles(t, root); !slices.Equal(after, before) {
		t.Errorf("files changed:\nbefore %v\nafter  %v", before, after)
	}
	rows := e.rows()
	for _, rel := range []string{"image/2020/03/a.jpg", "image/2019/01/wrongfolder.jpg", "media.db.bak-holiday.jpg"} {
		if _, ok := rows[rel]; !ok {
			t.Errorf("%s not recorded in place; rows %v", rel, keys(rows))
		}
	}
	_, x := rows["old/2012/x.jpg"]
	_, cx := rows["old/2012/copy-of-x.jpg"]
	if x == cx {
		t.Errorf("want exactly one of the identical files recorded; rows %v", keys(rows))
	}
	if d := c.byStatus(StatusDuplicate); len(d) != 1 || !strings.Contains(filepath.ToSlash(d[0].Source), "/old/2012/") {
		t.Errorf("duplicates = %+v", d)
	}

	// A second pass records nothing new and reports everything as duplicates.
	st, _ = e.mustRun(o)
	if st.Duplicates != 5 || st.Existing != 0 {
		t.Errorf("second pass stats = %+v", st)
	}
	if after := allFiles(t, root); !slices.Equal(after, before) {
		t.Errorf("files changed by the second pass")
	}
}

// A file whose path differs only in case from a recorded one is an error
// result, never a copy.
func TestRecordInPlaceDuplicatePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "lib")
	e := newEnvAt(t, root, root)
	writeFile(t, e.lib.Abs("image/2020/03/a.jpg"), jpegAt(date(2020, 3, 5), 1))
	// Recorded under another case, for a file that is not on disk.
	if _, err := e.db.InsertMedia(context.Background(), nil, &catalog.Media{Hash: 42, Size: 1,
		RelPath: "IMAGE/2020/03/A.JPG", FileType: "image", Ext: "jpg"}); err != nil {
		t.Fatal(err)
	}
	before := allFiles(t, root)
	o := e.opts()
	o.RecordInPlace = true
	o.NoThumbs = true
	st, c, err := e.run(o)
	if err != nil || st.Errors != 1 || st.Existing != 0 || st.Imported != 0 || st.BytesCopied != 0 {
		t.Fatalf("Run = %+v, %v", st, err)
	}
	if r := c.byStatus(StatusError); len(r) != 1 || !strings.Contains(r[0].Message, "duplicate path") {
		t.Errorf("errors = %+v", r)
	}
	if after := allFiles(t, root); !slices.Equal(after, before) {
		t.Errorf("files changed: %v", after)
	}
}

func TestRecordInPlaceRequiresLibraryRoot(t *testing.T) {
	e := newEnv(t)
	writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	o := e.opts()
	o.RecordInPlace = true
	if _, _, err := e.run(o); err == nil || !strings.Contains(err.Error(), "not the library root") {
		t.Fatalf("Run with an outside source = %v", err)
	}
	if rows := e.rows(); len(rows) != 0 {
		t.Errorf("rows = %v", keys(rows))
	}
	// A subdirectory of the library is not the root either.
	sub := e.lib.Abs("image")
	writeFile(t, filepath.Join(sub, "2020", "03", "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	o.Source = sub
	if _, _, err := e.run(o); err == nil {
		t.Fatal("Run with a library subdirectory as source succeeded")
	}
}

// A replacing Put that fails (here cancelled between its context check and
// its insert) keeps the previous preview; the row still matches it and a
// plain thumbnail pass has nothing to repair.
func TestFailedReplacePutKeepsPreview(t *testing.T) {
	e := newEnv(t)
	writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	st, _ := e.mustRun(e.opts())
	if st.ThumbsDone != 1 {
		t.Fatalf("setup %+v", st)
	}
	var m catalog.Media
	for _, r := range e.rows() {
		m = r
	}
	store := openStore(t, e.lib)
	prev, err := os.ReadFile(store.PreviewPath(m.Hash))
	if err != nil {
		t.Fatal(err)
	}
	c := &lateCancel{Context: context.Background(), done: make(chan struct{})}
	close(c.done)
	if err := store.Put(c, m.Hash, []byte{1}, 1, 1, []byte{2}); err == nil {
		t.Fatal("Put with a late cancellation succeeded")
	}
	if got, err := os.ReadFile(store.PreviewPath(m.Hash)); err != nil || !bytes.Equal(got, prev) {
		t.Fatalf("previous preview lost (err %v)", err)
	}
	if _, w, h, ok, err := store.Thumb(context.Background(), m.Hash); !ok || err != nil || w == 1 || h == 1 {
		t.Errorf("thumb row replaced: %dx%d ok=%v err=%v", w, h, ok, err)
	}
	st, err = New(e.lib, e.db, e.opts()).RunThumbs(context.Background(), 0, false)
	if err != nil || st.ThumbsDone+st.ThumbsFailed+st.ThumbsUnsupported != 0 {
		t.Errorf("RunThumbs = %+v, %v", st, err)
	}
	for _, r := range e.rows() {
		assertThumbOK(t, store, r)
		if r.PHash != m.PHash {
			t.Errorf("phash changed %x -> %x", m.PHash, r.PHash)
		}
	}
}

// lateCancel is not cancelled at its first Err call and is cancelled at
// every later one: Ctrl+C arriving between Put's ctx check and its INSERT.
type lateCancel struct {
	context.Context
	calls int
	done  chan struct{}
}

func (c *lateCancel) Done() <-chan struct{} { return c.done }
func (c *lateCancel) Err() error {
	c.calls++
	if c.calls == 1 {
		return nil
	}
	return context.Canceled
}

// Stale rows are regenerated (force) by a plain pass; error rows are
// retried by every plain pass.
func TestRunThumbsStaleAndErrorRows(t *testing.T) {
	e := newEnv(t)
	writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	writeFile(t, filepath.Join(e.src, "bad.jpg"), []byte("not a jpeg"))
	st, _ := e.mustRun(e.opts())
	if st.ThumbsDone != 1 || st.ThumbsFailed != 1 {
		t.Fatalf("setup %+v", st)
	}
	store := openStore(t, e.lib)
	var good catalog.Media
	for rel, m := range e.rows() {
		if strings.HasSuffix(rel, "a.jpg") {
			good = m
		}
	}
	fi, err := os.Stat(store.PreviewPath(good.Hash))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.db.MarkThumbStale(context.Background(), nil, good.ID); err != nil {
		t.Fatal(err)
	}
	st, err = New(e.lib, e.db, Options{}).RunThumbs(context.Background(), 0, false)
	if err != nil || st.ThumbsDone != 1 || st.ThumbsFailed != 1 {
		t.Fatalf("RunThumbs = %+v, %v", st, err)
	}
	fi2, err := os.Stat(store.PreviewPath(good.Hash))
	if err != nil || os.SameFile(fi, fi2) {
		t.Errorf("stale preview not regenerated (%v)", err)
	}
	for _, m := range e.rows() {
		if m.ID == good.ID {
			assertThumbOK(t, store, m)
		} else if m.ThumbState != catalog.ThumbStateError {
			t.Errorf("bad.jpg state %q", m.ThumbState)
		}
	}
	// The import's own stage only selects its session's rows.
	writeFile(t, filepath.Join(e.src, "c.jpg"), jpegAt(date(2020, 3, 6), 3))
	st, _, err = e.run(e.opts())
	if err != nil || st.Imported != 1 || st.ThumbsDone != 1 || st.ThumbsFailed != 0 {
		t.Errorf("second import = %+v, %v", st, err)
	}
}

// The writer drops every result that arrives after cancellation, whatever
// its state, and counts nothing it did not commit.
func TestThumbWriterDropsResultsAfterCancel(t *testing.T) {
	e := newEnv(t)
	writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	o := e.opts()
	o.NoThumbs = true
	e.mustRun(o)
	var m catalog.Media
	for _, r := range e.rows() {
		m = r
	}
	im := New(e.lib, e.db, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan thumbDone, 3)
	for _, state := range []string{thumbs.StateOK, thumbs.StateUnsupported, thumbs.StateError} {
		done <- thumbDone{job: thumbJob{id: m.ID, rel: m.RelPath}, res: thumbs.Result{State: state, PHash: 1}}
	}
	close(done)
	if err := im.thumbWriter(ctx, done, func() {}); err != nil {
		t.Fatal(err)
	}
	if st := im.Stats(); st.ThumbsDone+st.ThumbsUnsupported+st.ThumbsFailed != 0 {
		t.Errorf("counted dropped results: %+v", st)
	}
	if r := e.rows()[m.RelPath]; r.ThumbState != catalog.ThumbStatePending {
		t.Errorf("state = %q, want pending", r.ThumbState)
	}
}

// A batch that cannot be committed is retried row by row: one bad row does
// not lose the others, and only committed rows are counted. When every row
// fails the error is fatal.
func TestCommitThumbsRetriesRows(t *testing.T) {
	e := newEnv(t)
	for i := range 3 {
		writeFile(t, filepath.Join(e.src, string(rune('a'+i))+".jpg"), jpegAt(date(2020, 3, 5), byte(i)))
	}
	o := e.opts()
	o.NoThumbs = true
	e.mustRun(o)
	rows := e.rows()
	bad := rows["image/2020/03/b.jpg"]
	if _, err := e.db.Exec(`CREATE TRIGGER boom BEFORE UPDATE OF thumb_state ON media WHEN NEW.id = ` +
		strconv.FormatInt(bad.ID, 10) + ` BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	var batch []thumbDone
	for _, rel := range []string{"image/2020/03/a.jpg", "image/2020/03/b.jpg", "image/2020/03/c.jpg"} {
		m := rows[rel]
		batch = append(batch, thumbDone{job: thumbJob{id: m.ID, rel: rel}, res: thumbs.Result{State: thumbs.StateOK, PHash: 7}})
	}
	im := New(e.lib, e.db, Options{})
	if err := im.commitThumbs(batch); err != nil {
		t.Fatalf("commitThumbs = %v", err)
	}
	if st := im.Stats(); st.ThumbsDone != 2 {
		t.Errorf("ThumbsDone = %d, want 2", st.ThumbsDone)
	}
	for rel, m := range e.rows() {
		want := catalog.ThumbStateOK
		if rel == bad.RelPath {
			want = catalog.ThumbStatePending
		}
		if m.ThumbState != want {
			t.Errorf("%s: state %q, want %q", rel, m.ThumbState, want)
		}
	}
	// Every row fails: fatal, nothing counted.
	im = New(e.lib, e.db, Options{})
	if err := im.commitThumbs(batch[1:2]); err == nil {
		t.Error("commitThumbs of a batch whose every row fails succeeded")
	}
	if st := im.Stats(); st.ThumbsDone != 0 {
		t.Errorf("counted a failed row: %+v", st)
	}
}

// A thumbnail stage that cannot run (incompatible cache) is a
// ThumbStageError; the copy stage's files are recorded.
func TestRunThumbStageErrorKeepsImport(t *testing.T) {
	e := newEnv(t)
	s, err := thumbs.OpenStore(e.lib)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite", filepath.Join(e.lib.Root(), ".picmover", thumbs.DBName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	st, _, err := e.run(e.opts())
	var se *ThumbStageError
	if !errors.As(err, &se) || !errors.Is(err, thumbs.ErrFormat) {
		t.Fatalf("Run err = %v, want a ThumbStageError wrapping ErrFormat", err)
	}
	if st.Imported != 1 {
		t.Errorf("stats = %+v", st)
	}
	if rows := e.rows(); len(rows) != 1 {
		t.Errorf("rows = %v", keys(rows))
	}
}

func writeZip(t *testing.T, p string, entries map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
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
	writeFile(t, p, buf.Bytes())
}

func allFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".picmover" {
			return filepath.SkipDir
		}
		if !d.IsDir() && !strings.HasPrefix(d.Name(), "media.db-") && d.Name() != "media.db" {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func keys(m map[string]catalog.Media) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// A damaged cached preview is not trusted: after a rebuild of the catalog
// the row is never recorded "ok" without a perceptual hash.
func TestCorruptCachedPreviewRegenerated(t *testing.T) {
	root := filepath.Join(t.TempDir(), "lib")
	e := newEnvAt(t, root, root)
	writeFile(t, e.lib.Abs("image/2020/03/a.jpg"), jpegAt(date(2020, 3, 5), 1))
	e.mustRun(e.opts())
	var h uint64
	for _, r := range e.rows() {
		h = r.Hash
	}
	store := openStore(t, e.lib)
	if err := os.WriteFile(store.PreviewPath(h), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
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
	o := e.opts()
	o.RecordInPlace = true
	st, _ := e.mustRun(o)
	if st.Existing != 1 || st.ThumbsDone != 1 {
		t.Fatalf("rebuild stats %+v", st)
	}
	for _, r := range e.rows() {
		assertThumbOK(t, store, r)
	}
}
