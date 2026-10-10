package cmd

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"picmover/internal/backup"
	"picmover/internal/library"
	"picmover/internal/testimg"
)

func wantExit(t *testing.T, err error, out string, code int, substr string) {
	t.Helper()
	if got := exitCode(err); got != code {
		t.Fatalf("exit %d, want %d (%v)\n%s", got, code, err, out)
	}
	if substr != "" && !strings.Contains(out+errString(err), substr) {
		t.Fatalf("output lacks %q: %v\n%s", substr, err, out)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// backupLibrary imports a few photos into a new library.
func backupLibrary(t *testing.T) (base, lib string) {
	t.Helper()
	base = t.TempDir()
	src := filepath.Join(base, "src")
	for i, d := range []time.Time{time.Date(2024, 7, 1, 12, 0, 0, 0, time.UTC), time.Date(2023, 2, 3, 4, 5, 6, 0, time.UTC)} {
		p := filepath.Join(src, []string{"a.jpg", "b.jpg"}[i])
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, testimg.JPEG(testimg.Options{Width: 40, Height: 30, Seed: byte(i + 1), DateTime: d}), 0o644)
	}
	lib = filepath.Join(base, "photos")
	if out, err := run(t, "import", "--quiet", "--no-thumbs", src, lib); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	os.WriteFile(filepath.Join(lib, "notes.txt"), []byte("notes"), 0o644)
	return base, lib
}

func TestBackupCommands(t *testing.T) {
	base, lib := backupLibrary(t)
	drive := filepath.Join(base, "drive")
	os.Mkdir(drive, 0o755)

	out, err := run(t, "targets", lib)
	wantExit(t, err, out, 0, "No backup targets")
	out, err = run(t, "backup", lib)
	wantExit(t, err, out, 2, "picmover targets add")
	out, err = run(t, "backup", lib, drive)
	wantExit(t, err, out, 2, "is not a backup target")
	out, err = run(t, "backup", "--workers", "x", lib)
	wantExit(t, err, out, 2, "")
	out, err = run(t, "backup", lib, drive, "extra")
	wantExit(t, err, out, 2, "")
	out, err = run(t, "targets", "add", lib, filepath.Join(lib, "image"))
	wantExit(t, err, out, 2, "inside the library")

	out, err = run(t, "targets", "add", "--label", "Red WD", lib, drive)
	wantExit(t, err, out, 0, "Created backup target")
	id := regexp.MustCompile(`[0-9a-f]{16}`).FindString(out)
	if id == "" {
		t.Fatalf("no id in %s", out)
	}
	out, err = run(t, "backup", "--dry-run", lib)
	wantExit(t, err, out, 0, "Plan:")
	if !strings.Contains(out, "New:            3 files") {
		t.Fatalf("dry run %s", out)
	}
	if _, err := os.Stat(filepath.Join(drive, "photos", "notes.txt")); err == nil {
		t.Fatal("dry run copied")
	}
	out, err = run(t, "backup", "--quiet", lib)
	wantExit(t, err, out, 0, "Copied:            3")
	set := filepath.Join(drive, "photos")
	for _, p := range []string{"notes.txt", "user.db", "media.db", ".picmover/backup.db"} {
		if _, err := os.Stat(filepath.Join(set, p)); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if logs, _ := filepath.Glob(filepath.Join(lib, ".picmover", "logs", "backup_*.log")); len(logs) != 1 {
		t.Fatalf("library logs %v", logs)
	}
	if logs, _ := filepath.Glob(filepath.Join(set, ".picmover", "logs", "backup_*.log")); len(logs) != 1 {
		t.Fatalf("set logs %v", logs)
	}
	out, err = run(t, "targets", lib)
	wantExit(t, err, out, 0, id)
	if !strings.Contains(out, "Red WD") || !strings.Contains(out, "yes") {
		t.Fatalf("targets %s", out)
	}

	// The drive mounts elsewhere: --target says where it was expected.
	moved := filepath.Join(base, "mnt-drive")
	os.Rename(drive, moved)
	out, err = run(t, "backup", "--target", id, lib)
	wantExit(t, err, out, 2, "is not at "+drive)
	if !strings.Contains(out+errString(err), "connect it and pass its folder") {
		t.Fatalf("hint missing: %s %v", out, err)
	}
	out, err = run(t, "backup", "--quiet", lib, moved)
	wantExit(t, err, out, 0, "is now at "+moved)
	set = filepath.Join(moved, "photos")

	// Scrub: clean, then damage found (1), repaired from the library.
	out, err = run(t, "scrub", "--quiet", moved)
	wantExit(t, err, out, 0, "Checked:   3 files")
	os.WriteFile(filepath.Join(set, "notes.txt"), []byte("NOTES"), 0o644)
	out, err = run(t, "scrub", "--quiet", "--no-repair", moved)
	wantExit(t, err, out, 1, "Damaged:   1")
	out, err = run(t, "scrub", "--quiet", "--library", lib, moved, "photos")
	wantExit(t, err, out, 1, "Repaired:  1")
	out, err = run(t, "scrub", "--quiet", "--budget", "30m", moved)
	wantExit(t, err, out, 0, "Damaged:   0")
	out, err = run(t, "scrub", "--budget", "soon", moved)
	wantExit(t, err, out, 2, "invalid duration")

	// Prune: list, then remove with --yes.
	os.Remove(filepath.Join(lib, "notes.txt"))
	out, err = run(t, "prune", lib, id)
	wantExit(t, err, out, 0, "run again with --yes")
	if _, err := os.Stat(filepath.Join(set, "notes.txt")); err != nil {
		t.Fatal("prune without --yes removed")
	}
	out, err = run(t, "prune", "--yes", lib, moved)
	wantExit(t, err, out, 0, "Removed 1 files")

	// Restore into a new library: files, user.db, and a rebuilt catalog.
	restored := filepath.Join(base, "restored")
	out, err = run(t, "restore", "--dry-run", moved, "photos", restored)
	wantExit(t, err, out, 0, "Would restore 2 files")
	if _, err := os.Stat(restored); err == nil {
		t.Fatal("dry run created the library")
	}
	out, err = run(t, "restore", "--quiet", moved, "photos", restored)
	wantExit(t, err, out, 0, "Restored:          2")
	for _, p := range []string{library.DBName, "user.db"} {
		if _, err := os.Stat(filepath.Join(restored, p)); err != nil {
			t.Fatalf("restored library lacks %s\n%s", p, out)
		}
	}
	out, err = run(t, "restore", "--mode", "user-data", moved, "photos", restored)
	wantExit(t, err, out, 2, "--force")
	out, err = run(t, "restore", "--mode", "user-data", "--force", moved, "photos", restored)
	wantExit(t, err, out, 0, "user.db:           replaced")

	out, err = run(t, "targets", "forget", lib, id)
	wantExit(t, err, out, 0, "Forgot")
	out, err = run(t, "targets", "forget", lib, id)
	wantExit(t, err, out, 2, "no target")
}

func TestBackupRefusedWhileLocked(t *testing.T) {
	base, lib := backupLibrary(t)
	drive := filepath.Join(base, "drive")
	os.Mkdir(drive, 0o755)
	if out, err := run(t, "targets", "add", lib, drive); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	l, err := library.Attach(lib)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := l.Lock(context.Background(), "serve")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	out, err := run(t, "backup", "--quiet", lib)
	wantExit(t, err, out, 2, "in use by another picmover process")
}

func TestBackupOtherLibrarySet(t *testing.T) {
	base, lib := backupLibrary(t)
	drive := filepath.Join(base, "drive")
	os.Mkdir(drive, 0o755)
	tg, _, err := backup.InitTarget(drive)
	if err != nil {
		t.Fatal(err)
	}
	s, err := backup.OpenSet(context.Background(), tg, "photos", true)
	if err != nil {
		t.Fatal(err)
	}
	s.Claim(context.Background(), backup.Identity{LibraryID: "ffffffffffffffff", Instance: "ffffffffffffffff"}, false, false)
	s.Close()
	out, err := run(t, "targets", "add", lib, drive)
	wantExit(t, err, out, 2, "--set")
	out, err = run(t, "targets", "add", "--set", "photos-2", lib, drive)
	wantExit(t, err, out, 0, filepath.Join(drive, "photos-2"))
	out, err = run(t, "backup", "--quiet", lib)
	wantExit(t, err, out, 0, "Copied:")
	if _, err := os.Stat(filepath.Join(drive, "photos-2", "notes.txt")); err != nil {
		t.Fatal("not backed up into the chosen set")
	}
}
