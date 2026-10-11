package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"picmover/internal/library"
	"picmover/internal/metadata"
	"picmover/internal/testimg"
)

// openCounter is an Importer.openSource hook that counts opens per path.
type openCounter struct {
	mu     sync.Mutex
	opens  map[string]int
	before func(p string) // optional, called before each open
}

func (c *openCounter) open(p string) (*os.File, error) {
	if c.before != nil {
		c.before(p)
	}
	c.mu.Lock()
	if c.opens == nil {
		c.opens = map[string]int{}
	}
	c.opens[p]++
	c.mu.Unlock()
	return os.Open(p)
}

func (c *openCounter) count(p string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opens[p]
}

// recExtractor records the paths it is asked to read and delegates to the
// default extractor, or fails with err.
type recExtractor struct {
	mu    sync.Mutex
	paths []string
	err   error
}

func (x *recExtractor) Extract(ctx context.Context, p string, kind metadata.Kind) (metadata.Info, []string, error) {
	x.mu.Lock()
	x.paths = append(x.paths, p)
	x.mu.Unlock()
	if x.err != nil {
		return metadata.Info{}, nil, x.err
	}
	return metadata.Default{DefaultZone: time.UTC}.Extract(ctx, p, kind)
}

func (x *recExtractor) got() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]string(nil), x.paths...)
}

// runHooked runs an import with hook applied to the importer before Run.
func (e *env) runHooked(ctx context.Context, o Options, hook func(im *Importer)) (Stats, *collector, error) {
	e.t.Helper()
	c := &collector{}
	o.OnResult = c.add
	im := New(e.lib, e.db, o)
	if hook != nil {
		hook(im)
	}
	st, err := im.Run(ctx)
	return st, c, err
}

// assertNoStaged fails when a copy-*.part is left in the temp dir.
func (e *env) assertNoStaged() {
	e.t.Helper()
	ents, err := os.ReadDir(e.lib.TempDir())
	if err != nil {
		e.t.Fatal(err)
	}
	for _, d := range ents {
		if strings.HasPrefix(d.Name(), "copy-") {
			e.t.Errorf("staged temp left: %s", d.Name())
		}
	}
}

func (e *env) assertExtractedFromTemp(x *recExtractor, sources ...string) {
	e.t.Helper()
	for _, p := range x.got() {
		if filepath.Dir(p) != e.lib.TempDir() {
			e.t.Errorf("metadata extracted from %s, not from a staged temp file", p)
		}
		for _, s := range sources {
			if p == s {
				e.t.Errorf("metadata extraction opened the source %s", s)
			}
		}
	}
}

func TestCopyFirstReadsSourceOnce(t *testing.T) {
	e := newEnv(t)
	data := jpegAt(date(2020, 3, 5), 1)
	a := writeFile(t, filepath.Join(e.src, "a.jpg"), data)
	b := writeFile(t, filepath.Join(e.src, "b.jpg"), jpegAt(date(2021, 4, 6), 2))
	// In-flight duplicate: the same content twice in one import.
	b2 := writeFile(t, filepath.Join(e.src, "b2.jpg"), jpegAt(date(2021, 4, 6), 2))

	oc := &openCounter{}
	x := &recExtractor{}
	o := e.opts()
	o.Extractor = x
	st, c, err := e.runHooked(context.Background(), o, func(im *Importer) { im.openSource = oc.open })
	if err != nil {
		t.Fatal(err)
	}
	if st.Imported != 2 || st.Duplicates != 1 || st.Errors != 0 {
		t.Fatalf("stats = %+v, results %+v", st, c.results)
	}
	for _, p := range []string{a, b, b2} {
		if n := oc.count(p); n != 1 {
			t.Errorf("%s opened %d times, want 1", filepath.Base(p), n)
		}
	}
	e.assertExtractedFromTemp(x, a, b, b2)
	if len(x.got()) != 2 {
		t.Errorf("extracted %d files, want 2 (not the duplicate)", len(x.got()))
	}
	rows := e.rows()
	m, ok := rows["image/2020/03/a.jpg"]
	if !ok || m.OriginalPath != a || m.Size != int64(len(data)) {
		t.Fatalf("rows = %+v", rows)
	}
	h, _, _ := library.HashFile(a)
	fi, _ := os.Stat(a)
	if m.Hash != h || m.OriginalMTime.Unix() != fi.ModTime().Unix() {
		t.Errorf("row hash %016x mtime %v; source %016x %v", m.Hash, m.OriginalMTime, h, fi.ModTime())
	}
	if want := int64(len(data)) + sizeOf(t, b); st.BytesCopied != want {
		t.Errorf("BytesCopied = %d, want %d (the duplicate's staged bytes are not counted)", st.BytesCopied, want)
	}
	e.assertTempEmpty()

	// A foreign duplicate of a catalogued file is also read once.
	src2 := filepath.Join(t.TempDir(), "again")
	dup := writeFile(t, filepath.Join(src2, "copy-of-a.jpg"), data)
	oc2 := &openCounter{}
	x2 := &recExtractor{}
	o.Source, o.Extractor = src2, x2
	st, _, err = e.runHooked(context.Background(), o, func(im *Importer) { im.openSource = oc2.open })
	if err != nil {
		t.Fatal(err)
	}
	if st.Duplicates != 1 || st.Imported != 0 || st.BytesCopied != 0 {
		t.Errorf("stats = %+v", st)
	}
	if n := oc2.count(dup); n != 1 {
		t.Errorf("duplicate opened %d times, want 1", n)
	}
	if len(x2.got()) != 0 {
		t.Errorf("metadata extracted for a duplicate: %v", x2.got())
	}
	e.assertTempEmpty()
}

func sizeOf(t *testing.T, p string) int64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func TestSameDeviceMoveDoesNotStage(t *testing.T) {
	e := newEnv(t)
	a := writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	oc := &openCounter{}
	x := &recExtractor{}
	o := e.opts()
	o.Move, o.Extractor = true, x
	st, _, err := e.runHooked(context.Background(), o, func(im *Importer) { im.openSource = oc.open })
	if err != nil {
		t.Fatal(err)
	}
	if st.Imported != 1 || st.BytesCopied != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if n := oc.count(a); n != 1 { // the hash read only; the rename reads nothing
		t.Errorf("source opened %d times, want 1", n)
	}
	if got := x.got(); len(got) != 1 || got[0] != a {
		t.Errorf("extracted %v, want the source itself (renamed, not staged)", got)
	}
	if exists(a) {
		t.Error("source not moved")
	}
	e.assertFiles("image/2020/03/a.jpg")
}

func TestCrossDeviceMoveStagesOnce(t *testing.T) {
	e := newEnv(t)
	data := jpegAt(date(2020, 3, 5), 1)
	a := writeFile(t, filepath.Join(e.src, "a.jpg"), data)
	mt := time.Date(2020, 3, 6, 7, 8, 9, 0, time.UTC)
	if err := os.Chtimes(a, mt, mt); err != nil {
		t.Fatal(err)
	}
	h, size, err := library.HashFile(a)
	if err != nil {
		t.Fatal(err)
	}
	oc := &openCounter{}
	x := &recExtractor{}
	o := e.opts()
	o.Move, o.Extractor = true, x
	st, c, err := e.runHooked(context.Background(), o, func(im *Importer) {
		im.openSource = oc.open
		im.sameDev = func(string, string) bool { return false }
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Imported != 1 || st.BytesCopied != size {
		t.Fatalf("stats = %+v", st)
	}
	if n := oc.count(a); n != 1 {
		t.Errorf("source opened %d times, want 1", n)
	}
	e.assertExtractedFromTemp(x, a)
	if exists(a) {
		t.Error("source not deleted after commit")
	}
	if r := c.byStatus(StatusImported); len(r) != 1 || r[0].Message != "copied; source removed" {
		t.Errorf("results = %+v", c.results)
	}
	m, ok := e.rows()["image/2020/03/a.jpg"]
	if !ok {
		t.Fatalf("rows = %v", e.rows())
	}
	if m.Hash != h || m.Size != size || !m.OriginalMTime.Equal(mt) {
		t.Errorf("row hash %016x size %d mtime %v; source %016x %d %v", m.Hash, m.Size, m.OriginalMTime, h, size, mt)
	}
	fi, err := os.Stat(e.lib.Abs("image/2020/03/a.jpg"))
	if err != nil || !fi.ModTime().Equal(mt) {
		t.Errorf("library file mtime: %v, %v", fi, err)
	}
	e.assertTempEmpty()
}

func TestForceCopyTakesCopyFirst(t *testing.T) {
	e := newEnv(t)
	a := writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	oc := &openCounter{}
	x := &recExtractor{}
	o := e.opts()
	o.Move, o.ForceCopy, o.Extractor = true, true, x
	st, _, err := e.runHooked(context.Background(), o, func(im *Importer) { im.openSource = oc.open })
	if err != nil {
		t.Fatal(err)
	}
	if st.Imported != 1 || st.BytesCopied == 0 {
		t.Fatalf("stats = %+v", st)
	}
	if n := oc.count(a); n != 1 {
		t.Errorf("source opened %d times, want 1", n)
	}
	e.assertExtractedFromTemp(x, a)
	if exists(a) {
		t.Error("source not deleted")
	}
}

// Every way a staged file is abandoned removes its temp file and leaves
// nothing in the library.
func TestCopyFirstCleanup(t *testing.T) {
	small := jpegAt(date(2020, 3, 5), 1)
	cases := []struct {
		name   string
		setup  func(t *testing.T, e *env) // before the run
		opts   func(o *Options)
		hook   func(e *env, cancel context.CancelFunc) func(im *Importer)
		status Status // the result of src/a.jpg; "" when cancelled
	}{
		{name: "duplicate", status: StatusDuplicate, setup: func(t *testing.T, e *env) {
			o := e.opts()
			o.Source = writeFile(t, filepath.Join(t.TempDir(), "x.jpg"), small)
			e.mustRun(o)
			if err := os.Remove(e.lib.Abs("image/2020/03/x.jpg")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "below min dimension", status: StatusSkippedSmall, opts: func(o *Options) { o.MinDimension = 100 }},
		{name: "extraction error", status: StatusError, opts: func(o *Options) {
			o.Extractor = &recExtractor{err: errors.New("broken")}
		}},
		{name: "cancelled during staging", hook: func(e *env, cancel context.CancelFunc) func(im *Importer) {
			oc := &openCounter{before: func(string) { cancel() }}
			return func(im *Importer) { im.openSource = oc.open }
		}},
		{name: "commit fails", status: StatusError, hook: func(e *env, _ context.CancelFunc) func(im *Importer) {
			return func(im *Importer) {
				im.place = func(tmp, dst string) error {
					// The month directory's parent is a file: MkdirAll fails.
					writeFile(e.t, filepath.Join(e.lib.Root(), "image", "2020"), []byte("not a dir"))
					return library.Commit(tmp, dst)
				}
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			writeFile(t, filepath.Join(e.src, "a.jpg"), small)
			if tc.setup != nil {
				tc.setup(t, e)
			}
			o := e.opts()
			if tc.opts != nil {
				tc.opts(&o)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var hook func(*Importer)
			if tc.hook != nil {
				hook = tc.hook(e, cancel)
			}
			st, c, err := e.runHooked(ctx, o, hook)
			if tc.status == "" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Run err = %v", err)
				}
				if len(c.results) != 0 {
					t.Errorf("results after cancel: %+v", c.results)
				}
			} else {
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				if len(c.results) != 1 || c.results[0].Status != tc.status {
					t.Errorf("results = %+v, want one %s", c.results, tc.status)
				}
			}
			if st.BytesCopied != 0 || st.Imported != 0 {
				t.Errorf("stats = %+v", st)
			}
			e.assertNoStaged()
			for _, f := range e.libraryMedia() {
				if f != "image/2020" { // the file planted by "commit fails"
					t.Errorf("library file %s", f)
				}
			}
			if exists(e.lib.Abs("image/2020/03/a.jpg")) {
				t.Error("something at the destination")
			}
		})
	}
}

func TestSourceReadersSerialisesStaging(t *testing.T) {
	for _, readers := range []int{1, 4} {
		e := newEnv(t)
		for i := range 8 {
			writeFile(t, filepath.Join(e.src, string(rune('a'+i))+".jpg"), jpegAt(date(2020, 3, 5), byte(i)))
		}
		var active, peak atomic.Int32
		oc := &openCounter{before: func(string) {
			n := active.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			active.Add(-1)
		}}
		o := e.opts()
		o.Workers, o.SourceReaders = 4, readers
		st, _, err := e.runHooked(context.Background(), o, func(im *Importer) { im.openSource = oc.open })
		if err != nil || st.Imported != 8 {
			t.Fatalf("readers %d: stats %+v, err %v", readers, st, err)
		}
		switch {
		case readers == 1 && peak.Load() != 1:
			t.Errorf("SourceReaders 1: %d concurrent staging reads", peak.Load())
		case readers == 4 && peak.Load() < 2:
			t.Errorf("SourceReaders 4: staging never ran concurrently (peak %d); the test cannot detect serialisation", peak.Load())
		}
	}
}

func TestSourceReadersAuto(t *testing.T) {
	dir := t.TempDir()
	if isFUSE(dir) {
		t.Skip("temp dir is on FUSE")
	}
	for _, c := range []struct{ n, workers, want int }{{0, 4, 4}, {-1, 3, 3}, {1, 4, 1}, {9, 4, 4}} {
		if got := sourceReaders(c.n, c.workers, dir); got != c.want {
			t.Errorf("sourceReaders(%d, %d) = %d, want %d", c.n, c.workers, got, c.want)
		}
	}
	e := newEnv(t)
	writeFile(t, filepath.Join(e.src, "a.jpg"), testimg.PNG(8, 8, 1))
	o := e.opts()
	o.Workers = 3
	var im *Importer
	if _, _, err := e.runHooked(context.Background(), o, func(i *Importer) { im = i }); err != nil {
		t.Fatal(err)
	}
	if im.opts.SourceReaders != 3 || cap(im.readers) != 3 {
		t.Errorf("auto SourceReaders = %d (cap %d), want Workers 3", im.opts.SourceReaders, cap(im.readers))
	}
}
