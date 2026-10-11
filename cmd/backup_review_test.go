package cmd

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"picmover/internal/backup"
	"picmover/internal/library"
)

// Tests for the step 5 file-safety review, through the CLI.

// copyTree copies the regular files under src to dst (a copied library
// folder).
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		if !d.Type().IsRegular() || d.Name() == library.LockName {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// M1: a copied library folder (same library id) is refused on the
// original's set, naming the original's folder and host, until
// --take-over; then the original is refused in turn.
func TestBackupCopiedLibraryCLI(t *testing.T) {
	base, lib := backupLibrary(t)
	drive := filepath.Join(base, "drive")
	os.Mkdir(drive, 0o755)
	out, err := run(t, "targets", "add", lib, drive)
	wantExit(t, err, out, 0, "")
	out, err = run(t, "backup", "--quiet", lib)
	wantExit(t, err, out, 0, "Copied:")
	laptop := filepath.Join(base, "w2", "laptop")
	copyTree(t, lib, laptop)
	os.WriteFile(filepath.Join(laptop, "laptop-only.txt"), []byte("laptop"), 0o644)
	host, _ := os.Hostname()
	root, _ := filepath.EvalSymlinks(lib)
	out, err = run(t, "backup", "--quiet", laptop)
	wantExit(t, err, out, 2, "last written by the library at "+root+" on "+host)
	if !strings.Contains(out+errString(err), "--take-over") {
		t.Fatalf("no --take-over hint: %v", err)
	}
	if _, err := os.Stat(filepath.Join(drive, "photos", "laptop-only.txt")); err == nil {
		t.Fatal("the copy wrote into the original's set")
	}
	out, err = run(t, "prune", "--yes", laptop, drive)
	wantExit(t, err, out, 2, "last written by the library at")
	out, err = run(t, "backup", "--quiet", "--take-over", laptop)
	wantExit(t, err, out, 0, "Copied:")
	lroot, _ := filepath.EvalSymlinks(laptop)
	out, err = run(t, "backup", "--quiet", lib)
	wantExit(t, err, out, 2, "last written by the library at "+lroot)
}

// H1: a folder named like the set that is not a set is never taken over.
func TestBackupExistingFolderCLI(t *testing.T) {
	base, lib := backupLibrary(t)
	drive := filepath.Join(base, "drive")
	os.Mkdir(drive, 0o755)
	out, err := run(t, "targets", "add", lib, drive)
	wantExit(t, err, out, 0, "")
	// Another library copied onto the drive under the same name.
	other := filepath.Join(drive, "photos")
	os.MkdirAll(filepath.Join(other, "image"), 0o755)
	os.WriteFile(filepath.Join(other, "image", "tiny.jpg"), []byte("B"), 0o644)
	os.WriteFile(filepath.Join(other, library.DBName), []byte("B's catalog"), 0o644)
	out, err = run(t, "backup", "--quiet", lib)
	wantExit(t, err, out, 2, "is a picmover library, not a backup set")
	os.Remove(filepath.Join(other, library.DBName))
	out, err = run(t, "backup", "--quiet", lib)
	wantExit(t, err, out, 2, "not a picmover backup set")
	if b, _ := os.ReadFile(filepath.Join(other, "image", "tiny.jpg")); string(b) != "B" {
		t.Fatal("B's file touched")
	}
	if _, err := os.Stat(filepath.Join(other, library.MetaDir)); err == nil {
		t.Fatal("a .picmover was created in the refused folder")
	}
	// targets add with that set name refuses too.
	drive2 := filepath.Join(base, "drive2")
	os.Mkdir(drive2, 0o755)
	if _, _, err := backup.InitTarget(drive2); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(drive2, "photos"), 0o755)
	os.WriteFile(filepath.Join(drive2, "photos", "x.txt"), []byte("x"), 0o644)
	out, err = run(t, "targets", "add", lib, drive2)
	wantExit(t, err, out, 2, "not a picmover backup set")
}

// H2: a remembered set name that another library claimed later is
// refused by backup and prune.
func TestBackupStoredSetNameOtherLibraryCLI(t *testing.T) {
	base, lib := backupLibrary(t)
	drive := filepath.Join(base, "drive")
	os.Mkdir(drive, 0o755)
	out, err := run(t, "targets", "add", "--set", "shared", lib, drive)
	wantExit(t, err, out, 0, "")
	tg, _ := backup.OpenTarget(drive)
	s, err := backup.OpenSet(context.Background(), tg, "shared", true)
	if err != nil {
		t.Fatal(err)
	}
	s.Claim(context.Background(), backup.Identity{LibraryID: "aaaaaaaaaaaaaaaa", Instance: "bbbbbbbbbbbbbbbb"}, false, false)
	s.Close()
	diary := filepath.Join(drive, "shared", "diary.txt")
	os.WriteFile(diary, []byte("penny"), 0o644)
	out, err = run(t, "backup", "--quiet", lib)
	wantExit(t, err, out, 2, "the folder shared on this target holds another library's backup")
	out, err = run(t, "prune", "--yes", lib, drive)
	wantExit(t, err, out, 2, "another library's backup")
	if _, err := os.Stat(diary); err != nil {
		t.Fatal("removed another library's file")
	}
}

// M2: an unreadable folder: backup exits 1 and names it; prune --yes
// refuses without --force.
func TestBackupUnreadableFolderCLI(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("permissions are not enforced here")
	}
	base, lib := backupLibrary(t)
	drive := filepath.Join(base, "drive")
	os.Mkdir(drive, 0o755)
	notes := filepath.Join(lib, "image", "2018", "notes.xmp")
	os.MkdirAll(filepath.Dir(notes), 0o755)
	os.WriteFile(notes, []byte("sidecar"), 0o644)
	out, err := run(t, "targets", "add", lib, drive)
	wantExit(t, err, out, 0, "")
	out, err = run(t, "backup", "--quiet", lib)
	wantExit(t, err, out, 0, "")
	os.Chmod(filepath.Dir(notes), 0)
	defer os.Chmod(filepath.Dir(notes), 0o755)
	out, err = run(t, "backup", "--quiet", lib)
	wantExit(t, err, out, 1, "image/2018: permission denied")
	out, err = run(t, "prune", "--yes", lib, drive)
	wantExit(t, err, out, 2, "--force")
	if _, err := os.Stat(filepath.Join(drive, "photos", "image", "2018", "notes.xmp")); err != nil {
		t.Fatal("pruned the backup of a file that still exists")
	}
	out, err = run(t, "backup", "--quiet", "--prune", "--yes", lib)
	wantExit(t, err, out, 2, "--force")
	out, err = run(t, "prune", "--yes", "--force", lib, drive)
	wantExit(t, err, out, 0, "Removed 1 files")
}

// L2: --keep 0d keeps no earlier versions.
func TestPruneKeepZeroCLI(t *testing.T) {
	base, lib := backupLibrary(t)
	drive := filepath.Join(base, "drive")
	os.Mkdir(drive, 0o755)
	out, err := run(t, "targets", "add", lib, drive)
	wantExit(t, err, out, 0, "")
	for i := range 2 {
		// A changed file: its earlier content goes to previous/.
		os.WriteFile(filepath.Join(lib, "notes.txt"), []byte(fmt.Sprintf("notes %d", i)), 0o644)
		out, err = run(t, "backup", "--quiet", lib)
		wantExit(t, err, out, 0, "")
	}
	out, err = run(t, "prune", "--yes", lib, drive)
	wantExit(t, err, out, 0, "Nothing to prune")
	out, err = run(t, "prune", "--yes", "--keep", "0d", lib, drive)
	wantExit(t, err, out, 0, "Removed 1 files")
	if prev, _ := filepath.Glob(filepath.Join(drive, "photos", ".picmover", "previous", "*")); len(prev) != 0 {
		t.Fatalf("previous/ kept %v", prev)
	}
}

// M4: a user-data restore whose snapshot cannot be read leaves the
// library's user.db in place.
func TestRestoreUserDataFailureCLI(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("permissions are not enforced here")
	}
	base, lib := backupLibrary(t)
	drive := filepath.Join(base, "drive")
	os.Mkdir(drive, 0o755)
	out, err := run(t, "targets", "add", lib, drive)
	wantExit(t, err, out, 0, "")
	out, err = run(t, "backup", "--quiet", lib)
	wantExit(t, err, out, 0, "")
	snap := filepath.Join(drive, "photos", "user.db")
	os.Chmod(snap, 0)
	defer os.Chmod(snap, 0o644)
	out, err = run(t, "restore", "--mode", "user-data", "--force", drive, "photos", lib)
	wantExit(t, err, out, 2, "")
	if _, err := os.Stat(filepath.Join(lib, "user.db")); err != nil {
		t.Fatal("the library lost its user.db")
	}
	if baks, _ := filepath.Glob(filepath.Join(lib, "user.db.bak-*")); len(baks) != 0 {
		t.Fatalf("user.db moved aside: %v", baks)
	}
}
