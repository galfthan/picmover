package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"picmover/internal/backup"
	"picmover/internal/library"
)

// Tests for the resumability and performance review of step 5, through
// the CLI.

// L6: --dry-run writes nothing: no attach, no set, no instance, no
// user.db creation or migration.
func TestBackupDryRunWritesNothingCLI(t *testing.T) {
	base, lib := backupLibrary(t)
	drive := filepath.Join(base, "drive")
	os.Mkdir(drive, 0o755)
	if _, _, err := backup.InitTarget(drive); err != nil {
		t.Fatal(err)
	}
	// No user.db yet: reported, not created.
	os.Remove(filepath.Join(lib, "user.db"))
	out, err := run(t, "backup", "--dry-run", lib, drive)
	wantExit(t, err, out, 0, "Plan:")
	if !strings.Contains(out, "no user.db yet") {
		t.Fatalf("no note: %s", out)
	}
	if _, err := os.Stat(filepath.Join(lib, "user.db")); err == nil {
		t.Fatal("a dry run created user.db")
	}
	// With a target: nothing in user.db changes.
	out, err = run(t, "targets", "add", lib, drive)
	wantExit(t, err, out, 0, "")
	drive2 := filepath.Join(base, "drive2")
	os.Mkdir(drive2, 0o755)
	backup.InitTarget(drive2)
	snap := func() string {
		var b strings.Builder
		for _, n := range []string{"user.db", "user.db-wal"} {
			c, _ := os.ReadFile(filepath.Join(lib, n))
			b.Write(c)
		}
		return b.String()
	}
	before := snap()
	out, err = run(t, "backup", "--dry-run", lib)
	wantExit(t, err, out, 0, "Plan:")
	out, err = run(t, "backup", "--dry-run", lib, drive2)
	wantExit(t, err, out, 0, "Plan:")
	if snap() != before {
		t.Fatal("a dry run changed user.db")
	}
	if strings.Contains(out, "Attached") {
		t.Fatalf("a dry run attached the target: %s", out)
	}
	if backup.SetExists(&backup.Target{Root: drive}, "photos") {
		t.Fatal("a dry run created the set")
	}
}

// H1: a scrub or a backup while another process holds the set is refused
// naming it (exit 2).
func TestRefusedWhileSetLockedCLI(t *testing.T) {
	base, lib := backupLibrary(t)
	drive := filepath.Join(base, "drive")
	os.Mkdir(drive, 0o755)
	out, err := run(t, "targets", "add", lib, drive)
	wantExit(t, err, out, 0, "")
	out, err = run(t, "backup", "--quiet", lib)
	wantExit(t, err, out, 0, "")
	unlock, err := library.LockFile(filepath.Join(drive, "photos", library.MetaDir, library.LockName), "backup")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	out, err = run(t, "scrub", "--quiet", drive)
	wantExit(t, err, out, 2, "the backup set is in use by another picmover process")
	out, err = run(t, "backup", "--quiet", lib)
	wantExit(t, err, out, 2, "the backup set is in use")
}
