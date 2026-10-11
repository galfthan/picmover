package server

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFSRoots(t *testing.T) {
	f := newEmptyServer(t)
	w := f.get("/api/fs")
	wantStatus(t, w, http.StatusOK)
	got := decode[fsRootsJSON](t, w)
	if got.Path != "" || len(got.Dirs) == 0 {
		t.Fatalf("roots %s", w.Body)
	}
	for _, d := range got.Dirs {
		if !filepath.IsAbs(d.Path) || d.Name == "" {
			t.Fatalf("root %+v", d)
		}
	}
	if runtime.GOOS != "windows" && got.Dirs[0].Path != "/" {
		t.Fatalf("first root %+v", got.Dirs[0])
	}
}

// The gvfs directory (phone mounts) is a root only when it exists.
func TestFSRootsGvfs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no gvfs on Windows")
	}
	run := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", run)
	gvfs := filepath.Join(run, "gvfs")
	has := func() bool {
		for _, d := range roots() {
			if d.Path == gvfs {
				return true
			}
		}
		return false
	}
	if has() {
		t.Fatal("missing gvfs directory listed")
	}
	if err := os.Mkdir(gvfs, 0o700); err != nil {
		t.Fatal(err)
	}
	if !has() {
		t.Errorf("gvfs directory %s not listed in %+v", gvfs, roots())
	}
}

func TestFSList(t *testing.T) {
	f := newEmptyServer(t)
	dir := t.TempDir()
	for _, d := range []string{"b", "A", ".hidden", "c/deep"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"x.jpg", "y.CR2", "clip.MP4", "archive.ZIP", "notes.txt", "noext", ".hidden.jpg", "c/deep/z.jpg"} {
		writeFile(t, filepath.Join(dir, name), []byte("x"))
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(dir, "c"), filepath.Join(dir, "link")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dir, "x.jpg"), filepath.Join(dir, "linked.jpg")); err != nil {
			t.Fatal(err)
		}
	}
	w := f.get("/api/fs?path=" + url.QueryEscape(dir+string(filepath.Separator)+"."))
	wantStatus(t, w, http.StatusOK)
	got := decode[fsDirJSON](t, w)
	want := fsDirJSON{Path: dir, Parent: filepath.Dir(dir), MediaFiles: 4, OtherFiles: 2, Dirs: []dirJSON{
		{"A", filepath.Join(dir, "A")}, {"b", filepath.Join(dir, "b")}, {"c", filepath.Join(dir, "c")}}}
	if runtime.GOOS != "windows" {
		want.Dirs = append(want.Dirs, dirJSON{"link", filepath.Join(dir, "link")})
		want.MediaFiles++ // linked.jpg
	}
	if got.Path != want.Path || got.Parent != want.Parent || got.MediaFiles != want.MediaFiles ||
		got.OtherFiles != want.OtherFiles || len(got.Dirs) != len(want.Dirs) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	for i := range want.Dirs {
		if got.Dirs[i] != want.Dirs[i] {
			t.Fatalf("dirs %+v, want %+v", got.Dirs, want.Dirs)
		}
	}
	// Empty directory: empty array, not null.
	w = f.get("/api/fs?path=" + url.QueryEscape(filepath.Join(dir, "b")))
	if got := decode[fsDirJSON](t, w); got.Dirs == nil || len(got.Dirs) != 0 || got.MediaFiles != 0 {
		t.Fatalf("empty dir %s", w.Body)
	}
	// A root has no parent.
	root := "/"
	if runtime.GOOS == "windows" {
		root = filepath.VolumeName(dir) + `\`
	}
	if got := decode[fsDirJSON](t, f.get("/api/fs?path="+url.QueryEscape(root))); got.Parent != "" || got.Path != root {
		t.Fatalf("root listing %+v", got)
	}

	wantError(t, f.get("/api/fs?path=relative/dir"), http.StatusBadRequest, "absolute")
	wantError(t, f.get("/api/fs?path="+url.QueryEscape(filepath.Join(dir, "missing"))), http.StatusNotFound, "missing")
	wantError(t, f.get("/api/fs?path="+url.QueryEscape(filepath.Join(dir, "x.jpg"))), http.StatusBadRequest, "not a directory")
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		locked := filepath.Join(dir, "locked")
		if err := os.Mkdir(locked, 0); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(locked, 0o755)
		wantError(t, f.get("/api/fs?path="+url.QueryEscape(locked)), http.StatusBadRequest, "permission denied")
	}
}
