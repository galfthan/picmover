package library

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func writeTestFile(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTrashRoundTrip(t *testing.T) {
	l := open(t)
	rel := "image/2024/07/a.jpg"
	writeTestFile(t, l.Abs(rel), "A")
	if got, want := l.TrashPath(rel), filepath.Join(l.Root(), ".picmover", "trash", "image", "2024", "07", "a.jpg"); got != want {
		t.Fatalf("TrashPath %q, want %q", got, want)
	}
	if !l.IsInternal(l.TrashPath(rel)) {
		t.Fatal("trash is not internal")
	}
	if err := l.MoveToTrash(rel); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(l.Abs(rel)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("original still there: %v", err)
	}
	if b, err := os.ReadFile(l.TrashPath(rel)); err != nil || string(b) != "A" {
		t.Fatalf("trash file %q %v", b, err)
	}
	got, err := l.Restore(rel, nil)
	if err != nil || got != rel {
		t.Fatalf("restore %q %v", got, err)
	}
	if b, err := os.ReadFile(l.Abs(rel)); err != nil || string(b) != "A" {
		t.Fatalf("restored file %q %v", b, err)
	}
	if _, err := l.Restore(rel, nil); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("restore of a live file: %v", err)
	}
	if err := l.MoveToTrash("image/none.jpg"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("trash of a missing file: %v", err)
	}
}

func TestTrashRefusesOverwrite(t *testing.T) {
	l := open(t)
	rel := "image/b.jpg"
	writeTestFile(t, l.Abs(rel), "new")
	writeTestFile(t, l.TrashPath(rel), "old")
	if err := l.MoveToTrash(rel); !errors.Is(err, ErrTrashTargetExists) {
		t.Fatalf("overwrite not refused: %v", err)
	}
	for p, want := range map[string]string{l.Abs(rel): "new", l.TrashPath(rel): "old"} {
		if b, _ := os.ReadFile(p); string(b) != want {
			t.Fatalf("%s = %q, want %q", p, b, want)
		}
	}
}

func TestRestoreIntoTakenName(t *testing.T) {
	l := open(t)
	rel := "image/c.jpg"
	writeTestFile(t, l.Abs(rel), "C")
	if err := l.MoveToTrash(rel); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, l.Abs(rel), "other") // the path is taken on disk
	// c_1.jpg is free on disk but taken in the catalog.
	taken := func(c string) (bool, error) { return c == "image/c_1.jpg", nil }
	got, err := l.Restore(rel, taken)
	if err != nil || got != "image/c_2.jpg" {
		t.Fatalf("restore %q %v", got, err)
	}
	if b, _ := os.ReadFile(l.Abs(got)); string(b) != "C" {
		t.Fatal("wrong content restored")
	}
	if b, _ := os.ReadFile(l.Abs(rel)); string(b) != "other" {
		t.Fatal("taken file overwritten")
	}
}

func TestTrashCrossDeviceVerbatim(t *testing.T) {
	l := open(t)
	rel := "image/d.jpg"
	writeTestFile(t, l.Abs(rel), "D")
	xdev := &os.LinkError{Op: "rename", Old: "a", New: "b", Err: syscall.EXDEV}
	rename = func(string, string) error { return xdev }
	t.Cleanup(func() { rename = os.Rename })
	if err := l.MoveToTrash(rel); err != error(xdev) {
		t.Fatalf("error not returned verbatim: %v", err)
	}
	if b, _ := os.ReadFile(l.Abs(rel)); string(b) != "D" {
		t.Fatal("file touched after a failed rename")
	}
	rename = os.Rename
	if err := l.MoveToTrash(rel); err != nil {
		t.Fatal(err)
	}
	rename = func(string, string) error { return xdev }
	if _, err := l.Restore(rel, nil); err != error(xdev) {
		t.Fatalf("restore error not verbatim: %v", err)
	}
}
