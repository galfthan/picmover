package library

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMoveFileTo(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(filepath.Join(t.TempDir(), "b"))
	if err != nil {
		t.Fatal(err)
	}
	write := func(l *Library, rel, s string) {
		t.Helper()
		p := l.Abs(rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(a, "image/2024/01/x.jpg", "x")
	write(b, "image/2024/01/taken.jpg", "other")
	if err := a.MoveFileTo("image/2024/01/x.jpg", b, "image/2024/01/taken.jpg"); !errors.Is(err, ErrTargetExists) {
		t.Fatalf("replace: %v", err)
	}
	if got, _ := os.ReadFile(b.Abs("image/2024/01/taken.jpg")); string(got) != "other" {
		t.Fatal("target replaced")
	}
	if err := a.MoveFileTo("image/2024/01/x.jpg", b, ".picmover/x.jpg"); !errors.Is(err, ErrNotLibraryPath) {
		t.Fatalf("internal target: %v", err)
	}
	if err := a.MoveFileTo("image/2024/01/x.jpg", b, "image/2024/02/x.jpg"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.Abs("image/2024/02/x.jpg")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		outside := filepath.Join(t.TempDir(), "outside.jpg")
		os.WriteFile(outside, []byte("o"), 0o644)
		if err := os.Symlink(outside, a.Abs("link.jpg")); err != nil {
			t.Fatal(err)
		}
		if err := a.MoveFileTo("link.jpg", b, "link.jpg"); !errors.Is(err, ErrNotRegularFile) {
			t.Fatalf("link: %v", err)
		}
	}
	// RestoreTo undoes a MoveToTrashAs under another name.
	write(a, "image/2024/03/y.jpg", "y")
	if err := a.MoveToTrashAs("image/2024/03/y.jpg", "image/2024/03/y_1.jpg"); err != nil {
		t.Fatal(err)
	}
	if err := a.RestoreTo("image/2024/03/y_1.jpg", "image/2024/03/y.jpg"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(a.Abs("image/2024/03/y.jpg")); string(got) != "y" {
		t.Fatal("not restored")
	}
}
