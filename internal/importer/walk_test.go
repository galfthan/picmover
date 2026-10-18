package importer

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

// fakeEntry is a DirEntry whose Info returns mtime, or err when set.
type fakeEntry struct {
	name  string
	dir   bool
	mtime time.Time
	err   error
}

func (f fakeEntry) Name() string { return f.name }
func (f fakeEntry) IsDir() bool  { return f.dir }
func (f fakeEntry) Type() fs.FileMode {
	if f.dir {
		return fs.ModeDir
	}
	return 0
}
func (f fakeEntry) Info() (fs.FileInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return fakeInfo{f}, nil
}

type fakeInfo struct{ f fakeEntry }

func (i fakeInfo) Name() string       { return i.f.name }
func (i fakeInfo) Size() int64        { return 0 }
func (i fakeInfo) Mode() fs.FileMode  { return i.f.Type() }
func (i fakeInfo) ModTime() time.Time { return i.f.mtime }
func (i fakeInfo) IsDir() bool        { return i.f.dir }
func (i fakeInfo) Sys() any           { return nil }

func names(ents []fs.DirEntry) []string {
	out := make([]string, len(ents))
	for i, e := range ents {
		out[i] = e.Name()
	}
	return out
}

func TestSortNewestFirst(t *testing.T) {
	t0 := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	noInfo := errors.New("no info")
	ents := []fs.DirEntry{
		fakeEntry{name: "IMG_20240101_000000.jpg", mtime: t0},
		fakeEntry{name: "IMG_20240102_000000.jpg", mtime: t0}, // same mtime: the later name first
		fakeEntry{name: "Camera", dir: true, mtime: t0.Add(time.Hour)},
		fakeEntry{name: "a_gone.jpg", err: noInfo},
		fakeEntry{name: "b_old.jpg", mtime: t0.Add(-time.Hour)},
		fakeEntry{name: "z_gone.jpg", err: noInfo},
		fakeEntry{name: "newest.jpg", mtime: t0.Add(2 * time.Hour)},
	}
	sortNewestFirst(context.Background(), ents)
	want := []string{"newest.jpg", "Camera", "IMG_20240102_000000.jpg", "IMG_20240101_000000.jpg", "b_old.jpg",
		"z_gone.jpg", "a_gone.jpg"}
	if got := names(ents); !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}

	// Cancelled: nothing is stat'ed, every entry is kept, in name order (last first).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sortNewestFirst(ctx, ents)
	want = []string{"z_gone.jpg", "newest.jpg", "b_old.jpg", "a_gone.jpg", "IMG_20240102_000000.jpg",
		"IMG_20240101_000000.jpg", "Camera"}
	if got := names(ents); !slices.Equal(got, want) {
		t.Errorf("cancelled order = %v, want %v", got, want)
	}
}

func touch(t *testing.T, p string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// visitOrder walks root with walkNewestFirst and returns the paths
// visited, relative to root.
func visitOrder(t *testing.T, root string, readDir func(string) ([]fs.DirEntry, error)) []string {
	t.Helper()
	var got []string
	err := walkNewestFirst(context.Background(), root, readDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Fatalf("walk %s: %v", p, err)
		}
		rel, _ := filepath.Rel(root, p)
		got = append(got, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Within a directory files and folders come newest first: mtime
// descending, then name descending.
func TestWalkNewestFirstOrder(t *testing.T) {
	root := t.TempDir()
	t0 := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	for i, n := range []string{"IMG_20240101_000000.jpg", "IMG_20240301_000000.jpg", "IMG_20240201_000000.jpg"} {
		writeFile(t, filepath.Join(root, n), []byte{byte(i)})
	}
	touch(t, filepath.Join(root, "IMG_20240101_000000.jpg"), t0.Add(1*time.Hour))
	touch(t, filepath.Join(root, "IMG_20240201_000000.jpg"), t0.Add(2*time.Hour))
	touch(t, filepath.Join(root, "IMG_20240301_000000.jpg"), t0.Add(3*time.Hour))
	writeFile(t, filepath.Join(root, "old", "a.jpg"), []byte{1})
	writeFile(t, filepath.Join(root, "old", "b.jpg"), []byte{2})
	touch(t, filepath.Join(root, "old", "a.jpg"), t0) // equal mtimes: name descending
	touch(t, filepath.Join(root, "old", "b.jpg"), t0)
	touch(t, filepath.Join(root, "old"), t0.Add(90*time.Minute)) // between the first two files
	writeFile(t, filepath.Join(root, "new", "x.jpg"), []byte{3})
	touch(t, filepath.Join(root, "new", "x.jpg"), t0)
	touch(t, filepath.Join(root, "new"), t0.Add(4*time.Hour)) // newest entry of the root

	got := visitOrder(t, root, os.ReadDir)
	want := []string{".", "new", "new/x.jpg", "IMG_20240301_000000.jpg", "IMG_20240201_000000.jpg",
		"old", "old/b.jpg", "old/a.jpg", "IMG_20240101_000000.jpg"}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v\nwant    %v", got, want)
	}
}

// An entry whose Info fails is still walked, after the dated ones.
func TestWalkNewestFirstInfoError(t *testing.T) {
	root := t.TempDir()
	t0 := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	for _, n := range []string{"a.jpg", "b.jpg", "c.jpg", "d.jpg"} {
		writeFile(t, filepath.Join(root, n), []byte(n))
	}
	writeFile(t, filepath.Join(root, "sub", "e.jpg"), []byte("e"))
	touch(t, filepath.Join(root, "a.jpg"), t0.Add(3*time.Hour))
	touch(t, filepath.Join(root, "b.jpg"), t0.Add(1*time.Hour))
	touch(t, filepath.Join(root, "sub"), t0.Add(2*time.Hour))
	failing := map[string]bool{"c.jpg": true, "d.jpg": true, "sub": true}
	readDir := func(p string) ([]fs.DirEntry, error) {
		ents, err := os.ReadDir(p)
		for i, e := range ents {
			if failing[e.Name()] {
				ents[i] = fakeEntry{name: e.Name(), dir: e.IsDir(), err: fs.ErrNotExist}
			}
		}
		return ents, err
	}
	got := visitOrder(t, root, readDir)
	want := []string{".", "a.jpg", "b.jpg", "sub", "sub/e.jpg", "d.jpg", "c.jpg"}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v\nwant    %v", got, want)
	}
}

type walkCall struct {
	path string
	err  bool
}

// walkCalls records every call of a scan-like walk function: it skips
// directories named skip and, like scan, keeps going past errors (SkipDir
// for an unreadable directory below the root).
func walkCalls(root, skip string, calls *[]walkCall) fs.WalkDirFunc {
	return func(p string, d fs.DirEntry, err error) error {
		*calls = append(*calls, walkCall{p, err != nil})
		if err != nil {
			if d != nil && d.IsDir() && p != root {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() && p != root && d.Name() == skip {
			return filepath.SkipDir
		}
		return nil
	}
}

// The walk makes the same calls as filepath.WalkDir, in another order:
// the same files, the same skipped directories, the same errors.
func TestWalkNewestFirstSameAsWalkDir(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a.jpg", "b.mp4", "notes.txt", "._a.jpg", "x.zip",
		"DCIM/Camera/IMG_1.jpg", "DCIM/Camera/IMG_2.jpg", "DCIM/.thumbnails/t.jpg",
		"skipme/inner.jpg", "deep/er/still/f.jpg", "locked/hidden.jpg"} {
		writeFile(t, filepath.Join(root, n), []byte(n))
	}
	if err := os.Symlink(filepath.Join(root, "a.jpg"), filepath.Join(root, "link.jpg")); err != nil {
		t.Logf("no symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "deep"), filepath.Join(root, "dirlink")); err != nil {
		t.Logf("no symlink: %v", err)
	}
	locked := filepath.Join(root, "locked")
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o755) })
	}

	var want, got []walkCall
	if err := filepath.WalkDir(root, walkCalls(root, "skipme", &want)); err != nil {
		t.Fatal(err)
	}
	if err := walkNewestFirst(context.Background(), root, os.ReadDir, walkCalls(root, "skipme", &got)); err != nil {
		t.Fatal(err)
	}
	cmpCall := func(a, b walkCall) int {
		if a.path != b.path {
			if a.path < b.path {
				return -1
			}
			return 1
		}
		if a.err == b.err {
			return 0
		}
		if !a.err {
			return -1
		}
		return 1
	}
	slices.SortFunc(want, cmpCall)
	slices.SortFunc(got, cmpCall)
	if !slices.Equal(got, want) {
		t.Errorf("calls differ\n got  %v\n want %v", got, want)
	}
	if slices.Contains(got, walkCall{filepath.Join(root, "skipme", "inner.jpg"), false}) {
		t.Error("a skipped directory was walked")
	}
}

// scan dispatches the newest files first and still reports an
// unreadable folder, importing everything else.
func TestScanNewestFirst(t *testing.T) {
	e := newEnv(t)
	t0 := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	files := []string{"IMG_20240101_120000.jpg", "IMG_20240102_120000.jpg", "IMG_20240103_120000.jpg"}
	for i, n := range files {
		p := writeFile(t, filepath.Join(e.src, n), jpegAt(date(2024, 1, i+1), byte(i)))
		touch(t, p, t0.Add(time.Duration(i)*time.Hour))
	}
	writeFile(t, filepath.Join(e.src, ".DS_Store"), []byte("junk"))
	writeFile(t, filepath.Join(e.src, "readme.txt"), []byte("text"))
	lockedOK := runtime.GOOS != "windows" && os.Geteuid() != 0
	locked := filepath.Join(e.src, "locked")
	if lockedOK {
		writeFile(t, filepath.Join(locked, "x.jpg"), jpegAt(date(2024, 2, 1), 9))
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o755) })
	}

	col := &collector{}
	o := e.opts()
	o.OnResult = col.add
	im := New(e.lib, e.db, o)
	info, err := os.Stat(e.src)
	if err != nil {
		t.Fatal(err)
	}
	jobs := make(chan job, 100)
	im.scan(context.Background(), e.src, info, jobs, false)
	close(jobs)
	var got []string
	for j := range jobs {
		got = append(got, filepath.Base(j.path))
	}
	want := []string{"IMG_20240103_120000.jpg", "IMG_20240102_120000.jpg", "IMG_20240101_120000.jpg"}
	if !slices.Equal(got, want) {
		t.Errorf("jobs = %v, want %v", got, want)
	}
	if n := im.scanned.Load(); n != 4 { // three photos and readme.txt
		t.Errorf("scanned = %d, want 4", n)
	}
	if nm := col.byStatus(StatusNonMedia); len(nm) != 1 {
		t.Errorf("non-media = %+v", nm)
	}
	errs := col.byStatus(StatusError)
	if lockedOK && (len(errs) != 1 || errs[0].Source != locked) {
		t.Errorf("errors = %+v, want one for %s", errs, locked)
	}
}
