package importer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/metadata"
	"picmover/internal/testimg"
)

// kindExtractor gives every file a fixed date and, for images, a size, so
// RAW-like and video files need no real decoder or ffprobe.
type kindExtractor struct {
	delay time.Duration // per file, to make the copy stage slow
}

func (x kindExtractor) Extract(ctx context.Context, p string, kind metadata.Kind) (metadata.Info, []string, error) {
	if x.delay > 0 {
		select {
		case <-time.After(x.delay):
		case <-ctx.Done():
			return metadata.Info{}, nil, ctx.Err()
		}
	}
	if kind == metadata.KindImage {
		return metadata.Default{DefaultZone: time.UTC}.Extract(ctx, p, kind)
	}
	return metadata.Info{TakenAt: date(2021, 6, 1), OffsetKnown: true, Source: "exif"}, nil, nil
}

// The imported count is split by kind in the stats, the progress and the
// results.
func TestImportCountsByKind(t *testing.T) {
	e := newEnv(t)
	writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	writeFile(t, filepath.Join(e.src, "b.png"), testimg.PNG(40, 30, 2))
	writeFile(t, filepath.Join(e.src, "c.dng"), []byte("raw one"))
	writeFile(t, filepath.Join(e.src, "d.cr2"), []byte("raw two"))
	writeFile(t, filepath.Join(e.src, "e.mp4"), []byte("video one"))
	writeFile(t, filepath.Join(e.src, "dup.jpg"), jpegAt(date(2020, 3, 5), 1))
	o := e.opts()
	o.Extractor = kindExtractor{}
	o.NoThumbs = true
	var last Stats
	o.OnProgress = func(s Stats) { last = s }
	st, c := e.mustRun(o)
	if st.Imported != 5 || st.ImportedPhotos != 2 || st.ImportedRaw != 2 || st.ImportedVideos != 1 || st.Duplicates != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if last.ImportedPhotos != 2 || last.ImportedRaw != 2 || last.ImportedVideos != 1 {
		t.Errorf("last progress = %+v", last)
	}
	kinds := map[metadata.Kind]int{}
	for _, r := range c.byStatus(StatusImported) {
		kinds[r.Kind]++
	}
	if kinds[metadata.KindImage] != 2 || kinds[metadata.KindRaw] != 2 || kinds[metadata.KindVideo] != 1 {
		t.Errorf("result kinds %v", kinds)
	}
	if d := c.byStatus(StatusDuplicate); len(d) != 1 || d[0].Kind != metadata.KindImage {
		t.Errorf("duplicate result %+v", d)
	}
}

// assertLibraryConsistent checks that every media file in the library
// is a catalog row whose content has the row's hash, every row's file
// exists, and no staged temp file is left.
func (e *env) assertLibraryConsistent() {
	e.t.Helper()
	rows := e.rows()
	onDisk := e.libraryMedia()
	if len(onDisk) != len(rows) {
		e.t.Errorf("library has %d media files, catalog %d rows: %v", len(onDisk), len(rows), onDisk)
	}
	for _, rel := range onDisk {
		m, ok := rows[rel]
		if !ok {
			e.t.Errorf("file %s is not in the catalog", rel)
			continue
		}
		h, _, err := library.HashFile(e.lib.Abs(rel))
		if err != nil || h != m.Hash {
			e.t.Errorf("file %s: hash %x, catalog %x (%v)", rel, h, m.Hash, err)
		}
	}
	e.assertNoStaged()
}

// disconnectSource writes n distinct JPEGs into the source and returns
// their paths.
func disconnectSource(t *testing.T, e *env, n int) []string {
	var paths []string
	for i := range n {
		paths = append(paths, writeFile(t, filepath.Join(e.src, "DCIM", fmt.Sprintf("img%03d.jpg", i)),
			testimg.JPEG(testimg.Options{Width: 64, Height: 48, DateTime: date(2020, 3, 5), Seed: byte(i)})))
	}
	return paths
}

// Unplugging the source mid-run (the source directory goes away while a
// file is being read) stops the import with one status: no error per
// remaining file, no partial file in the library, a consistent catalog,
// and a second run imports the rest and skips what was imported.
func TestSourceDisconnected(t *testing.T) {
	e := newEnv(t)
	const total = 40
	disconnectSource(t, e, total)
	gone := e.src + ".unplugged"
	var opens atomic.Int32
	var once sync.Once
	o := e.opts()
	o.Workers = 2
	var results []Result
	var mu sync.Mutex
	o.OnResult = func(r Result) { mu.Lock(); results = append(results, r); mu.Unlock() }
	im := New(e.lib, e.db, o)
	im.openSource = func(p string) (*os.File, error) {
		if opens.Add(1) == 12 {
			once.Do(func() {
				// The phone is unplugged while this file is read: the
				// mount goes away and the read ends early.
				if err := os.Rename(e.src, gone); err != nil {
					t.Error(err)
				}
			})
			r, w, err := os.Pipe()
			if err != nil {
				return nil, err
			}
			go func() {
				w.Write([]byte("partial JPEG data"))
				w.Close()
			}()
			return r, nil
		}
		return os.Open(p)
	}
	st, err := im.Run(context.Background())
	if !errors.Is(err, ErrSourceDisconnected) {
		t.Fatalf("Run err = %v (stats %+v)", err, st)
	}
	if !st.SourceDisconnected || st.Errors != 0 || st.Imported == 0 || st.Imported >= total {
		t.Fatalf("stats = %+v", st)
	}
	for _, r := range results {
		if r.Status == StatusError {
			t.Errorf("error result after the disconnect: %+v", r)
		}
	}
	if im.RecordStageDone() {
		t.Error("record stage reported done")
	}
	e.assertLibraryConsistent()
	if got := len(e.rows()); int64(got) != st.Imported {
		t.Errorf("rows %d, imported %d", got, st.Imported)
	}
	// The rows recorded before the disconnect got their thumbnails.
	if p, ok, _, _, err := e.db.ThumbStats(context.Background()); err != nil || p != 0 || ok != st.Imported {
		t.Errorf("thumbs pending %d ok %d (%v)", p, ok, err)
	}
	// The source itself is untouched.
	if n := countFiles(t, gone); n != total {
		t.Errorf("source has %d files", n)
	}

	// Reconnected: the rerun imports the rest, skips the rest.
	if err := os.Rename(gone, e.src); err != nil {
		t.Fatal(err)
	}
	st2, _ := e.mustRun(e.opts())
	if st2.Imported+st.Imported != total || st2.Duplicates != st.Imported || st2.Errors != 0 || st2.SourceDisconnected {
		t.Fatalf("rerun stats = %+v (first %+v)", st2, st)
	}
	e.assertLibraryConsistent()
	if len(e.rows()) != total {
		t.Errorf("rows after rerun %d", len(e.rows()))
	}
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// The periodic check finds a source that went away without any read
// failing (the workers are busy elsewhere).
func TestSourceDisconnectedPeriodicCheck(t *testing.T) {
	e := newEnv(t)
	disconnectSource(t, e, 60)
	sourceCheckInterval = 10 * time.Millisecond
	t.Cleanup(func() { sourceCheckInterval = time.Second })
	o := e.opts()
	o.Workers = 1
	o.NoThumbs = true
	o.Extractor = kindExtractor{delay: 20 * time.Millisecond}
	var unplugged atomic.Bool
	o.OnProgress = func(s Stats) {
		if s.Imported >= 1 {
			unplugged.Store(true)
		}
	}
	o.ProgressInterval = 5 * time.Millisecond
	im := New(e.lib, e.db, o)
	im.statRoot = func(p string) (fs.FileInfo, error) {
		if unplugged.Load() {
			return nil, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
		}
		return os.Stat(p)
	}
	st, err := im.Run(context.Background())
	if !errors.Is(err, ErrSourceDisconnected) || !st.SourceDisconnected || st.Errors != 0 || st.Imported >= 60 {
		t.Fatalf("Run = %+v, %v", st, err)
	}
	e.assertLibraryConsistent()
}

// A source error while the source is still there stays an error of
// that file, and the run goes on.
func TestSourceErrorNotDisconnect(t *testing.T) {
	e := newEnv(t)
	paths := disconnectSource(t, e, 5)
	o := e.opts()
	o.NoThumbs = true
	im := New(e.lib, e.db, o)
	im.openSource = func(p string) (*os.File, error) {
		if p == paths[2] {
			return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
		}
		return os.Open(p)
	}
	st, err := im.Run(context.Background())
	if err != nil || st.Errors != 1 || st.Imported != 4 || st.SourceDisconnected {
		t.Fatalf("Run = %+v, %v", st, err)
	}
}

// Thumbnails are made while the copy stage still runs, from the library
// copies; every imported file gets one, and a file whose copy failed
// gets none.
func TestThumbsDuringCopy(t *testing.T) {
	e := newEnv(t)
	const total = 24
	paths := disconnectSource(t, e, total)
	failed := paths[5]
	failedHash, _, err := library.HashFile(failed)
	if err != nil {
		t.Fatal(err)
	}
	o := e.opts()
	o.Workers = 1
	o.ThumbWorkers = 2
	o.Extractor = kindExtractor{delay: 40 * time.Millisecond}
	o.ProgressInterval = 5 * time.Millisecond
	var thumbsDuringCopy atomic.Int64
	o.OnProgress = func(s Stats) {
		if s.Imported+s.Errors < total && s.ThumbsDone > thumbsDuringCopy.Load() {
			thumbsDuringCopy.Store(s.ThumbsDone)
		}
	}
	thumbPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { thumbPollInterval = 250 * time.Millisecond })
	im := New(e.lib, e.db, o)
	var c collector
	im.opts.OnResult = c.add
	place := im.place
	im.place = func(tmp, dst string) error {
		if h, _, _ := library.HashFile(tmp); h == failedHash {
			os.Remove(tmp)
			return errors.New("disk full")
		}
		return place(tmp, dst)
	}
	st, err := im.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Imported != total-1 || st.Errors != 1 || st.ThumbsDone != total-1 || st.ThumbsFailed != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if thumbsDuringCopy.Load() == 0 {
		t.Errorf("no thumbnail was made while copying")
	}
	store := openStore(t, e.lib)
	for _, m := range e.rows() {
		assertThumbOK(t, store, m)
	}
	if thumb, _, preview, err := store.Has(context.Background(), failedHash); err != nil || thumb || preview {
		t.Errorf("failed copy has renditions: thumb %v preview %v %v", thumb, preview, err)
	}
	e.assertLibraryConsistent()
	if p, ok, _, _, _ := e.db.ThumbStats(context.Background()); p != 0 || ok != total-1 {
		t.Errorf("pending %d ok %d", p, ok)
	}
}

// With the thumbnails made during the copy, each source file is still
// read once: the thumbnail workers read the library copies.
func TestThumbsReadLibraryCopy(t *testing.T) {
	e := newEnv(t)
	paths := disconnectSource(t, e, 6)
	o := e.opts()
	oc := &openCounter{}
	st, _, err := e.runHooked(context.Background(), o, func(im *Importer) { im.openSource = oc.open })
	if err != nil || st.ThumbsDone != 6 {
		t.Fatalf("Run = %+v, %v", st, err)
	}
	for _, p := range paths {
		if n := oc.count(p); n != 1 {
			t.Errorf("%s opened %d times", p, n)
		}
	}
	for _, m := range e.rows() {
		if m.ThumbState != catalog.ThumbStateOK {
			t.Errorf("%s: %s", m.RelPath, m.ThumbState)
		}
	}
}

// A cancellation stops the copy and the thumbnails together.
func TestCancelStopsThumbsDuringCopy(t *testing.T) {
	e := newEnv(t)
	disconnectSource(t, e, 60)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := e.opts()
	o.Workers = 1
	o.Extractor = kindExtractor{delay: 20 * time.Millisecond}
	o.ProgressInterval = 5 * time.Millisecond
	o.OnProgress = func(s Stats) {
		if s.Imported >= 1 {
			cancel()
		}
	}
	st, err := New(e.lib, e.db, o).Run(ctx)
	if !errors.Is(err, context.Canceled) || st.Imported >= 60 {
		t.Fatalf("Run = %+v, %v", st, err)
	}
	e.assertLibraryConsistent()
	p, ok, _, _, _ := e.db.ThumbStats(context.Background())
	if p+ok != st.Imported {
		t.Errorf("pending %d ok %d imported %d", p, ok, st.Imported)
	}
}
