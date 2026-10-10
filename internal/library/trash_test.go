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
	t.Cleanup(func() { rename = RenameNoReplace })
	if err := l.MoveToTrash(rel); err != error(xdev) {
		t.Fatalf("error not returned verbatim: %v", err)
	}
	if b, _ := os.ReadFile(l.Abs(rel)); string(b) != "D" {
		t.Fatal("file touched after a failed rename")
	}
	rename = RenameNoReplace
	if err := l.MoveToTrash(rel); err != nil {
		t.Fatal(err)
	}
	rename = func(string, string) error { return xdev }
	if _, err := l.Restore(rel, nil); err != error(xdev) {
		t.Fatalf("restore error not verbatim: %v", err)
	}
}

func TestRenameNoReplace(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	writeTestFile(t, a, "A")
	writeTestFile(t, b, "B")
	if err := RenameNoReplace(a, b); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("existing target: %v", err)
	}
	for p, want := range map[string]string{a: "A", b: "B"} {
		if got, _ := os.ReadFile(p); string(got) != want {
			t.Fatalf("%s = %q, want %q", p, got, want)
		}
	}
	c := filepath.Join(dir, "c")
	if err := RenameNoReplace(a, c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(a); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("source still there: %v", err)
	}
	if got, _ := os.ReadFile(c); string(got) != "A" {
		t.Fatalf("moved file %q", got)
	}
	if err := RenameNoReplace(filepath.Join(dir, "none"), filepath.Join(dir, "d")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing source: %v", err)
	}
}

// A name taken between Restore's check and its rename (an import picking
// the same free name) is never replaced: the next variant is used.
func TestRestoreRaceNeverReplaces(t *testing.T) {
	l := open(t)
	rel := "image/r.jpg"
	writeTestFile(t, l.Abs(rel), "R")
	if err := l.MoveToTrash(rel); err != nil {
		t.Fatal(err)
	}
	first := true
	rename = func(src, dst string) error {
		if first {
			first = false
			writeTestFile(t, dst, "import") // another writer wins the name
		}
		return RenameNoReplace(src, dst)
	}
	t.Cleanup(func() { rename = RenameNoReplace })
	got, err := l.Restore(rel, nil)
	if err != nil || got != "image/r_1.jpg" {
		t.Fatalf("restore %q %v", got, err)
	}
	if b, _ := os.ReadFile(l.Abs(rel)); string(b) != "import" {
		t.Fatalf("the other file was replaced: %q", b)
	}
	if b, _ := os.ReadFile(l.Abs(got)); string(b) != "R" {
		t.Fatalf("restored %q", b)
	}
}

func TestTrashRefusesRaceOnTrashTarget(t *testing.T) {
	l := open(t)
	rel := "image/s.jpg"
	writeTestFile(t, l.Abs(rel), "S")
	rename = func(src, dst string) error {
		writeTestFile(t, dst, "other")
		return RenameNoReplace(src, dst)
	}
	t.Cleanup(func() { rename = RenameNoReplace })
	if err := l.MoveToTrash(rel); !errors.Is(err, ErrTrashTargetExists) {
		t.Fatalf("got %v", err)
	}
	if b, _ := os.ReadFile(l.TrashPath(rel)); string(b) != "other" {
		t.Fatal("trash file replaced")
	}
	if b, _ := os.ReadFile(l.Abs(rel)); string(b) != "S" {
		t.Fatal("source lost")
	}
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

// A library folder that is a link to a folder outside the library: the
// file server refuses it, and so do trash and restore.
func TestTrashRefusesSymlinkedDirOutside(t *testing.T) {
	l := open(t)
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(outside, "12", "a.jpg"), "A")
	symlinkOrSkip(t, outside, l.Abs("image/2019"))
	if err := l.MoveToTrash("image/2019/12/a.jpg"); !errors.Is(err, ErrNotLibraryPath) {
		t.Fatalf("trash through a link: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "12", "a.jpg")); string(b) != "A" {
		t.Fatal("outside file moved")
	}

	// Restore into a linked folder is refused as well.
	writeTestFile(t, l.TrashPath("image/2019/12/b.jpg"), "B")
	if _, err := l.Restore("image/2019/12/b.jpg", nil); !errors.Is(err, ErrNotLibraryPath) {
		t.Fatalf("restore through a link: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "12", "b.jpg")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("file restored outside the library")
	}
}

// An intermediate link inside the trash never lets a delete or restore
// reach a file outside it.
func TestTrashSideSymlinkRefused(t *testing.T) {
	l := open(t)
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(outside, "06", "scan.png"), "X")
	symlinkOrSkip(t, outside, l.TrashPath("image/2018"))
	if err := l.RemoveTrashFile("image/2018/06/scan.png"); !errors.Is(err, ErrNotLibraryPath) {
		t.Fatalf("delete through a link: %v", err)
	}
	if _, err := l.Restore("image/2018/06/scan.png", nil); !errors.Is(err, ErrNotLibraryPath) {
		t.Fatalf("restore through a link: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "06", "scan.png")); string(b) != "X" {
		t.Fatal("outside file touched")
	}
	// A library file is not trashed into a linked trash folder either.
	writeTestFile(t, l.Abs("image/2018/06/c.png"), "C")
	if err := l.MoveToTrash("image/2018/06/c.png"); !errors.Is(err, ErrNotLibraryPath) {
		t.Fatalf("trash into a link: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "06", "c.png")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("file moved out of the library")
	}
}

// Directories and links at a catalog path are refused, never moved or
// deleted.
func TestTrashRefusesNonRegular(t *testing.T) {
	l := open(t)
	writeTestFile(t, l.Abs("image/dir.jpg/inner.jpg"), "I")
	if err := l.MoveToTrash("image/dir.jpg"); !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("directory trashed: %v", err)
	}
	writeTestFile(t, l.TrashPath("image/tdir.jpg/x.jpg"), "X")
	if err := l.RemoveTrashFile("image/tdir.jpg"); !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("trash directory deleted: %v", err)
	}
	if _, err := l.Restore("image/tdir.jpg", nil); !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("trash directory restored: %v", err)
	}
	if err := l.RemoveTrashFile("image/none.jpg"); err != nil {
		t.Fatalf("missing file: %v", err)
	}
	writeTestFile(t, l.TrashPath("image/gone.jpg"), "G")
	if err := l.RemoveTrashFile("image/gone.jpg"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(l.TrashPath("image/gone.jpg")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("not removed")
	}
	writeTestFile(t, l.Abs("image/target.jpg"), "T")
	symlinkOrSkip(t, l.Abs("image/target.jpg"), l.Abs("image/link.jpg"))
	if err := l.MoveToTrash("image/link.jpg"); !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("link trashed: %v", err)
	}
	symlinkOrSkip(t, l.Abs("image/target.jpg"), l.TrashPath("image/tlink.jpg"))
	if err := l.RemoveTrashFile("image/tlink.jpg"); !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("trash link deleted: %v", err)
	}
	if b, _ := os.ReadFile(l.Abs("image/target.jpg")); string(b) != "T" {
		t.Fatal("link target touched")
	}
}
