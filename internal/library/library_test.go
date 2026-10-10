package library

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cespare/xxhash/v2"
)

func open(t *testing.T) *Library {
	t.Helper()
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestOpenCreatesInternalDirs(t *testing.T) {
	l := open(t)
	for _, d := range []string{l.TempDir(), l.LogDir()} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("%s not created: %v", d, err)
		}
	}
}

func TestRelAbs(t *testing.T) {
	l := open(t)
	abs := l.Abs("image/2020/01/a.jpg")
	want := filepath.Join(l.Root(), "image", "2020", "01", "a.jpg")
	if abs != want {
		t.Errorf("Abs = %q, want %q", abs, want)
	}
	rel, err := l.Rel(abs)
	if err != nil || rel != "image/2020/01/a.jpg" {
		t.Errorf("Rel = %q, %v", rel, err)
	}
	if !l.Contains(abs) {
		t.Error("Contains(abs) = false")
	}
	outside := filepath.Join(filepath.Dir(l.Root()), "elsewhere", "x.jpg")
	if _, err := l.Rel(outside); err == nil {
		t.Error("Rel(outside) succeeded")
	}
	if l.Contains(outside) || l.Contains(l.Root()) {
		t.Error("Contains wrong for outside/root")
	}
}

func TestIsInternal(t *testing.T) {
	l := open(t)
	cases := map[string]bool{
		"media.db":                          true,
		"media.db-wal":                      true,
		"media.db-shm":                      true,
		"media.db-journal":                  true,
		"media.dbx":                         false,
		"media.db.old":                      false,
		"media.db-foo":                      false,
		"media.db.v1-20240101-120000-2.bak": true,
		"media.db.v1-20240101-120000.bak.12345.tmp": true,
		"media.db.v1-20240101-120000.tmp":           false,
		"import_x.txt":                              false,
		"media.v1-20240101.bak":                     false,
		"media.db.v1-20240101.bak":                  true,
		"media.db.bak-20261010-120000":              true,
		"media.db.bak-20261010-120000-wal":          true,
		"media.db.bak-20261010-120000-shm":          true,
		"media.db.bak-20261010-120000-journal":      true,
		"media.db.bak-20261010-120000-foo":          false,
		"media.db.bak-holiday.jpg":                  false,
		"media.db.bak-20261010120000":               false,
		"media.db.bak-2026101a-120000":              false,
		"media.db.bak-20261010-120000.jpg":          false,
		"image/media.db.bak-20261010-120000":        false,
		"import_2024-01-01.log":                     true,
		".picmover":                                 true,
		".picmover/tmp/x.part":                      true,
		".picmover/lock":                            true,
		".picmover/trash/image/2020/01/a.jpg":       true,
		"user.db":                                   true,
		"user.db-wal":                               true,
		"user.db-shm":                               true,
		"user.db-journal":                           true,
		"user.db.jpg":                               false,
		"image/user.db":                             false,
		"image/2020/01/a.jpg":                       false,
		"image/import_x.log":                        false,
		"USER.DB":                                   true,
		"User.db-WAL":                               true,
		"MEDIA.DB":                                  true,
		".PicMover/trash/a.jpg":                     true,
		"Import_x.LOG":                              true,
	}
	for rel, want := range cases {
		if got := l.IsInternal(l.Abs(rel)); got != want {
			t.Errorf("IsInternal(%q) = %v, want %v", rel, got, want)
		}
	}
	if l.IsInternal(filepath.Join(filepath.Dir(l.Root()), "media.db")) {
		t.Error("file outside library reported internal")
	}
}

func TestLayoutPath(t *testing.T) {
	ts := time.Date(2020, 3, 5, 23, 59, 0, 0, time.FixedZone("x", 2*3600))
	if got := LayoutPath("image", ts, "a.jpg"); got != "image/2020/03/a.jpg" {
		t.Errorf("got %q", got)
	}
}

func TestUniqueName(t *testing.T) {
	taken := map[string]bool{"d/a.jpg": true, "d/a_1.jpg": true}
	f := func(s string) (bool, error) { return taken[s], nil }
	if got, err := UniqueName("d/a.jpg", f); err != nil || got != "d/a_2.jpg" {
		t.Errorf("got %q, %v", got, err)
	}
	if got, err := UniqueName("d/b.jpg", f); err != nil || got != "d/b.jpg" {
		t.Errorf("got %q, %v", got, err)
	}
	if got, err := UniqueName("d/noext", func(string) (bool, error) { return false, nil }); err != nil || got != "d/noext" {
		t.Errorf("got %q, %v", got, err)
	}

	boom := errors.New("boom")
	calls := 0
	_, err := UniqueName("d/a.jpg", func(c string) (bool, error) {
		calls++
		if c == "d/a_3.jpg" {
			return false, boom
		}
		return true, nil
	})
	if !errors.Is(err, boom) || calls != 4 {
		t.Errorf("error from taken: got %v after %d calls, want boom after 4", err, calls)
	}

	calls = 0
	if got, err := UniqueName("d/a.jpg", func(string) (bool, error) { calls++; return true, nil }); err == nil {
		t.Errorf("all taken: got %q, want error", got)
	}
	if calls != maxUniqueAttempts {
		t.Errorf("all taken: %d calls, want %d", calls, maxUniqueAttempts)
	}
}

func TestHashFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, n, err := HashFile(p)
	if err != nil || n != 5 || h != xxhash.Sum64String("hello") {
		t.Errorf("HashFile = %x, %d, %v", h, n, err)
	}
}

func randomFile(t *testing.T, dir string, size int) (string, []byte) {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "src.bin")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p, data
}

func tempIsEmpty(t *testing.T, l *Library) {
	t.Helper()
	entries, err := os.ReadDir(l.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("temp dir not empty: %d entries", len(entries))
	}
}

func TestCopyVerified(t *testing.T) {
	l := open(t)
	src, data := randomFile(t, t.TempDir(), 3<<20+123)
	mtime := time.Date(2019, 7, 1, 10, 0, 0, 0, time.UTC)
	if err := os.Chtimes(src, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	dst := l.Abs("image/2019/07/src.bin")
	n, err := l.CopyVerified(context.Background(), src, dst, xxhash.Sum64(data))
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(data)) {
		t.Errorf("n = %d", n)
	}
	got, err := os.ReadFile(dst)
	if err != nil || !bytes.Equal(got, data) {
		t.Error("destination content differs")
	}
	fi, _ := os.Stat(dst)
	if !fi.ModTime().Equal(mtime) {
		t.Errorf("mtime not preserved: %v", fi.ModTime())
	}
	tempIsEmpty(t, l)

	// Hash mismatch leaves nothing behind.
	dst2 := l.Abs("image/2019/07/src2.bin")
	_, err = l.CopyVerified(context.Background(), src, dst2, xxhash.Sum64(data)+1)
	if !errors.Is(err, ErrHashMismatch) {
		t.Errorf("err = %v, want ErrHashMismatch", err)
	}
	if _, err := os.Stat(dst2); !os.IsNotExist(err) {
		t.Error("dst2 exists after failed copy")
	}
	tempIsEmpty(t, l)

	// Existing destination is never overwritten.
	if _, err := l.CopyVerified(context.Background(), src, dst, xxhash.Sum64(data)); err == nil {
		t.Error("overwrote existing destination")
	}
	tempIsEmpty(t, l)

	// Cancellation aborts and cleans up.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dst3 := l.Abs("image/2019/07/src3.bin")
	if _, err := l.CopyVerified(ctx, src, dst3, xxhash.Sum64(data)); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want Canceled", err)
	}
	if _, err := os.Stat(dst3); !os.IsNotExist(err) {
		t.Error("dst3 exists after cancelled copy")
	}
	tempIsEmpty(t, l)
}

func TestCleanTemp(t *testing.T) {
	l := open(t)
	if err := os.WriteFile(filepath.Join(l.TempDir(), "stale.part"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(l.TempDir(), "zip-1", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := l.CleanTemp(); err != nil {
		t.Fatal(err)
	}
	tempIsEmpty(t, l)
}

func TestCopyVerifiedMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	l := open(t)
	src := filepath.Join(t.TempDir(), "a.jpg")
	data := []byte("hello")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	dst := l.Abs("image/2020/01/a.jpg")
	if _, err := l.CopyVerified(context.Background(), src, dst, xxhash.Sum64(data)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("imported file mode %v, want 0644", fi.Mode().Perm())
	}
}

func TestAttach(t *testing.T) {
	dir := t.TempDir()
	l, err := Attach(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(dir); l.Root() != want {
		t.Errorf("Root = %q, want %q", l.Root(), want)
	}
	if _, err := os.Stat(l.TempDir()); !os.IsNotExist(err) {
		t.Errorf("Attach created %s (err %v)", l.TempDir(), err)
	}
	missing := filepath.Join(dir, "nope")
	if _, err := Attach(missing); err == nil {
		t.Error("Attach of a missing directory succeeded")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Error("Attach created the missing directory")
	}
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Attach(file); err == nil {
		t.Error("Attach of a regular file succeeded")
	}
}

// A library opened through a symlinked path resolves its root, and paths
// spelled through either the link or the target are recognised.
func TestSymlinkedRoot(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	realLib, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	realLib = filepath.Join(realLib, "lib")
	linkLib := filepath.Join(link, "lib")
	// Open first: it creates the directory Attach needs (a map would
	// iterate in random order).
	for _, c := range []struct {
		name   string
		openFn func(string) (*Library, error)
	}{{"Open", Open}, {"Attach", Attach}} {
		name := c.name
		l, err := c.openFn(linkLib)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if l.Root() != realLib {
			t.Errorf("%s: Root = %q, want %q", name, l.Root(), realLib)
		}
		for _, root := range []string{realLib, linkLib} {
			p := filepath.Join(root, "image", "2020", "01", "a.jpg")
			if rel, err := l.Rel(p); err != nil || rel != "image/2020/01/a.jpg" {
				t.Errorf("%s: Rel(%q) = %q, %v", name, p, rel, err)
			}
			if !l.Contains(p) {
				t.Errorf("%s: Contains(%q) = false", name, p)
			}
			if l.IsInternal(p) {
				t.Errorf("%s: IsInternal(%q) = true", name, p)
			}
			for _, in := range []string{
				filepath.Join(root, MetaDir, "previews", "ab", "ab01.jpg"),
				filepath.Join(root, DBName),
				filepath.Join(root, DBName+".bak-20261010-120000"),
			} {
				if !l.IsInternal(in) {
					t.Errorf("%s: IsInternal(%q) = false", name, in)
				}
			}
			if l.Contains(root) {
				t.Errorf("%s: Contains(root %q) = true", name, root)
			}
			if out := filepath.Join(filepath.Dir(root), "other", "a.jpg"); l.Contains(out) || l.IsInternal(out) {
				t.Errorf("%s: %q reported inside", name, out)
			}
		}
	}
}

func TestOpenLog(t *testing.T) {
	l := open(t)
	before := time.Now().Truncate(time.Second)
	f, p, err := l.OpenLog("import")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if filepath.Dir(p) != l.LogDir() {
		t.Fatalf("log %s not in %s", p, l.LogDir())
	}
	base := filepath.Base(p)
	stamp, ok := strings.CutPrefix(base, "import_")
	if !ok || !strings.HasSuffix(stamp, ".log") {
		t.Fatalf("log name %q", base)
	}
	ts, err := time.ParseInLocation("2006-01-02_15-04-05", strings.TrimSuffix(stamp, ".log"), time.Local)
	if err != nil {
		t.Fatalf("log name %q: %v", base, err)
	}
	if ts.Before(before) || ts.After(time.Now()) {
		t.Fatalf("log time %v not now", ts)
	}
	if _, err := f.WriteString("x\n"); err != nil {
		t.Fatal(err)
	}
	if !l.IsInternal(p) {
		t.Fatal("log must be internal")
	}
	if _, _, err := (&Library{root: filepath.Join(t.TempDir(), "missing")}).OpenLog("x"); err == nil {
		t.Fatal("want error without a log directory")
	}
}
