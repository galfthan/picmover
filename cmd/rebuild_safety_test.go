package cmd

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/userdata"
)

// rebuildLib makes a library with two recorded files.
func rebuildLib(t *testing.T) (string, *library.Library) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "lib")
	lib, err := library.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeJPEG(t, lib.Abs("image/2021/03/a.jpg"), 1)
	writeJPEG(t, lib.Abs("image/2021/03/b.jpg"), 2)
	if out, err := run(t, "rebuild", "--quiet", "--no-thumbs", dir); err != nil {
		t.Fatalf("rebuild: %v\n%s", err, out)
	}
	return dir, lib
}

func catalogRows(t *testing.T, lib *library.Library) int {
	t.Helper()
	db, err := catalog.OpenExisting(context.Background(), lib.DBPath(), nil)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	defer db.Close()
	rows, err := db.List(context.Background(), catalog.ListOptions{IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func backups(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, library.DBName+".bak-*"))
	return m
}

// An unreadable user.db stops the rebuild before the backup: the
// library keeps its catalog.
func TestRebuildUnreadableUserDBKeepsCatalog(t *testing.T) {
	dir, lib := rebuildLib(t)
	before := backups(t, dir)
	u, err := userdata.Open(context.Background(), filepath.Join(dir, userdata.FileName))
	if err != nil {
		t.Fatal(err)
	}
	u.Close()
	raw, err := sql.Open("sqlite", filepath.Join(dir, userdata.FileName))
	if err != nil {
		t.Fatal(err)
	}
	raw.Exec("PRAGMA user_version = 4")
	raw.Close()
	out, err := run(t, "rebuild", "--quiet", "--no-thumbs", dir)
	if err == nil || !strings.Contains(err.Error(), "schema version 4") {
		t.Fatalf("rebuild with a newer user.db: %v\n%s", err, out)
	}
	if n := catalogRows(t, lib); n != 2 {
		t.Fatalf("catalog has %d rows", n)
	}
	if after := backups(t, dir); len(after) != len(before) {
		t.Fatalf("a backup was made: %v", after)
	}
}

// A rebuild cancelled before its record stage finished restores the
// previous catalog instead of leaving the partial one live.
func TestRebuildCancelledRestoresBackup(t *testing.T) {
	dir, lib := rebuildLib(t)
	// Without user.db (the import made one for its record): a cancelled
	// context would stop the rebuild at reading it, before the backup.
	for _, s := range []string{"", "-wal", "-shm"} {
		os.Remove(filepath.Join(dir, userdata.FileName+s))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := runCtx(t, ctx, "rebuild", "--quiet", "--no-thumbs", dir)
	if !strings.Contains(out, "previous catalog was restored") {
		t.Fatalf("output: %v\n%s", err, out)
	}
	if n := catalogRows(t, lib); n != 2 {
		t.Fatalf("catalog has %d rows after the rollback", n)
	}
	if b := backups(t, dir); len(b) != 0 {
		t.Fatalf("backups left: %v", b)
	}
}

// A trash file whose content is live gets no row from a rebuild; verify
// reports it as an orphan in the trash.
func TestVerifyReportsTrashOrphans(t *testing.T) {
	dir, lib := rebuildLib(t)
	writeJPEG(t, lib.TrashPath("image/2021/03/copy.jpg"), 1) // same content as a.jpg
	if out, err := run(t, "rebuild", "--quiet", "--no-thumbs", dir); err != nil {
		t.Fatalf("rebuild: %v\n%s", err, out)
	}
	out, err := run(t, "verify", dir)
	if err == nil || !strings.Contains(out, "orphan file in trash: image/2021/03/copy.jpg") {
		t.Fatalf("verify: %v\n%s", err, out)
	}
}

// A cancelled rebuild of a folder without a catalog removes the
// incomplete one: the folder is not turned into a library.
func TestRebuildCancelledFolderRemovesCatalog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plain")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJPEG(t, filepath.Join(dir, "x.jpg"), 3)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := runCtx(t, ctx, "rebuild", "--quiet", "--no-thumbs", dir)
	if !strings.Contains(out, "incomplete catalog was removed") {
		t.Fatalf("output: %v\n%s", err, out)
	}
	if _, err := os.Lstat(filepath.Join(dir, library.DBName)); !os.IsNotExist(err) {
		t.Fatalf("media.db left: %v", err)
	}
}
