package cmd

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"picmover/internal/library"
	"picmover/internal/userdata"
)

func openUserDB(t *testing.T, dir string) *userdata.DB {
	t.Helper()
	u, err := userdata.Open(context.Background(), filepath.Join(dir, userdata.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { u.Close() })
	return u
}

func removeUserDB(t *testing.T, dir string) {
	t.Helper()
	for _, s := range []string{"", "-wal", "-shm"} {
		os.Remove(filepath.Join(dir, userdata.FileName+s))
	}
}

// import and rebuild record the run in user.db and print the record id;
// 'imports' lists, renames and deletes records.
func TestImportRecordsAndImportsCommand(t *testing.T) {
	ctx := context.Background()
	src, dir := t.TempDir(), filepath.Join(t.TempDir(), "lib")
	writeJPEG(t, filepath.Join(src, "phone", "a.jpg"), 1)
	writeJPEG(t, filepath.Join(src, "phone", "b.jpg"), 2)
	writeJPEG(t, filepath.Join(src, "phone", "dup.jpg"), 1)
	phone := filepath.Join(src, "phone")

	out, err := run(t, "import", "--quiet", "--no-thumbs", phone+string(filepath.Separator), dir)
	if err != nil || !strings.Contains(out, "Import record: #1\n") {
		t.Fatalf("import: %v\n%s", err, out)
	}
	u := openUserDB(t, dir)
	rec, err := u.GetImport(ctx, 1)
	if err != nil || rec.Mode != "copy" || rec.Outcome != "done" || rec.Source != phone || rec.SourceName != "phone" ||
		rec.Imported != 2 || rec.Duplicates != 1 || rec.Bytes == 0 || rec.CatalogSession != 1 || rec.FinishedAt.IsZero() {
		t.Fatalf("record %+v %v", rec, err)
	}
	if hs, _ := u.ImportHashes(ctx, 1); len(hs) != 2 {
		t.Fatalf("items %v", hs)
	}
	u.Close()

	// A rebuild records every row as existing, mode rebuild.
	out, err = run(t, "rebuild", "--quiet", "--no-thumbs", dir)
	if err != nil || !strings.Contains(out, "Import record: #2\n") {
		t.Fatalf("rebuild: %v\n%s", err, out)
	}
	u = openUserDB(t, dir)
	rec, err = u.GetImport(ctx, 2)
	if err != nil || rec.Mode != "rebuild" || rec.Outcome != "done" || rec.Existing != 2 || rec.SourceName != "lib" {
		t.Fatalf("rebuild record %+v %v", rec, err)
	}
	l, _ := u.ListImports(ctx)
	if len(l) != 2 || l[0].ID != 2 || l[0].KeepsItems() || l[1].Items != 2 || l[1].Cover == 0 {
		t.Fatalf("list %+v", l)
	}
	if hs, _ := u.ImportHashes(ctx, 2); len(hs) != 0 {
		t.Fatalf("rebuild items %v", hs)
	}
	u.Close()

	out, err = run(t, "imports", dir)
	if err != nil {
		t.Fatalf("imports: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 || lines[0] != "Library: "+dir ||
		!regexp.MustCompile(`^ID\s+STARTED\s+MODE\s+OUTCOME\s+SOURCE\s+IMPORTED\s+EXISTING\s+DUPLICATES\s+ERRORS\s+BYTES\s+NAME$`).MatchString(lines[1]) ||
		!regexp.MustCompile(`^2\s+\d{4}-\d\d-\d\d \d\d:\d\d\s+rebuild\s+done\s+lib\s+0\s+2\s+0\s+0\s+0 B\s*$`).MatchString(lines[2]) ||
		!regexp.MustCompile(`^1\s+\S+ \S+\s+copy\s+done\s+phone\s+2\s+0\s+1\s+0\s+\S+ \S+\s*$`).MatchString(lines[3]) {
		t.Fatalf("imports output:\n%s", out)
	}

	out, err = run(t, "imports", dir, "--rename", "1", "  Holiday  ")
	if err != nil || out != "Library: "+dir+"\nImport record #1 renamed to \"Holiday\"\n" {
		t.Fatalf("rename: %v\n%s", err, out)
	}
	if out, _ := run(t, "imports", dir); !regexp.MustCompile(`(?m)^1\s.*Holiday$`).MatchString(out) {
		t.Fatalf("renamed list:\n%s", out)
	}
	if out, err = run(t, "imports", dir, "--rename", "1", ""); err != nil || !strings.Contains(out, "name cleared") {
		t.Fatalf("clear: %v\n%s", err, out)
	}
	out, err = run(t, "imports", dir, "--delete", "2")
	if err != nil || !strings.HasPrefix(out, "Library: "+dir+"\n") ||
		!strings.Contains(out, "Deleted import record #2; its files were left as they are.") {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if n := len(mediaSet(t, dir)); n != 2 {
		t.Fatalf("catalog rows after deleting a record: %d", n)
	}
	for _, args := range [][]string{
		{"imports", dir, "--delete", "2"},
		{"imports", dir, "--rename", "9", "x"},
		{"imports", dir, "--delete", "0"},
		{"imports", dir, "--rename", "1"},
		{"imports", dir, "--rename", "1", "x", "--delete", "1"},
		{"imports", dir, "--rename", "1", strings.Repeat("x", 201)},
	} {
		if out, err := run(t, args...); err == nil {
			t.Errorf("%v accepted:\n%s", args, out)
		}
	}
}

// The first command of this version that opens a library copies its
// catalog's import sessions into the history: import uses the live
// catalog, rebuild the old one before replacing it.
func TestImportHistoryBackfill(t *testing.T) {
	ctx := context.Background()
	src, dir := t.TempDir(), filepath.Join(t.TempDir(), "lib")
	writeJPEG(t, filepath.Join(src, "one", "a.jpg"), 1)
	writeJPEG(t, filepath.Join(src, "two", "b.jpg"), 2)
	if out, err := run(t, "import", "--quiet", "--no-thumbs", filepath.Join(src, "one"), dir); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	removeUserDB(t, dir) // as an earlier version left it: sessions, no history

	out, err := run(t, "import", "--quiet", "--no-thumbs", filepath.Join(src, "two"), dir)
	if err != nil || !strings.Contains(out, "Import record: #2\n") {
		t.Fatalf("second import: %v\n%s", err, out)
	}
	u := openUserDB(t, dir)
	l, _ := u.ListImports(ctx)
	if len(l) != 2 || l[1].SourceName != "one" || l[1].CatalogSession != 1 || l[1].Host != "" || l[1].Items != 1 ||
		l[0].SourceName != "two" || l[0].Items != 1 {
		t.Fatalf("list %+v", l)
	}
	u.Close()

	// Rebuild: the old catalog's sessions (1 and 2) are backfilled first.
	removeUserDB(t, dir)
	out, err = run(t, "rebuild", "--quiet", "--no-thumbs", dir)
	if err != nil || !strings.Contains(out, "Import record: #3\n") {
		t.Fatalf("rebuild: %v\n%s", err, out)
	}
	u = openUserDB(t, dir)
	l, _ = u.ListImports(ctx)
	if len(l) != 3 || l[0].Mode != "rebuild" || l[0].KeepsItems() || l[1].SourceName != "two" || l[2].SourceName != "one" {
		t.Fatalf("list after rebuild %+v", l)
	}
	baks, _ := filepath.Glob(filepath.Join(dir, library.DBName+".bak-*"))
	if len(baks) == 0 {
		t.Fatal("no catalog backup")
	}
}

// A record that cannot be written never fails the import; the summary
// says so.
func TestImportRecordFailureDoesNotFailImport(t *testing.T) {
	src, dir := t.TempDir(), filepath.Join(t.TempDir(), "lib")
	writeJPEG(t, filepath.Join(src, "a.jpg"), 1)
	if out, err := run(t, "import", "--quiet", "--no-thumbs", src, dir); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	raw, err := sql.Open("sqlite", filepath.Join(dir, userdata.FileName))
	if err != nil {
		t.Fatal(err)
	}
	raw.Exec("PRAGMA user_version = 4")
	raw.Close()
	writeJPEG(t, filepath.Join(src, "b.jpg"), 2)
	out, err := run(t, "import", "--quiet", "--no-thumbs", src, dir)
	if err != nil || !strings.Contains(out, "Imported:          1\n") ||
		!strings.Contains(out, "Import record: could not be written (import record: open user.db:") ||
		!strings.Contains(out, "schema version 4") {
		t.Fatalf("import with a newer user.db: %v\n%s", err, out)
	}
	logs, _ := filepath.Glob(filepath.Join(dir, library.MetaDir, "logs", "import_*.log"))
	found := false
	for _, p := range logs {
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), "level=ERROR msg=\"import record could not be written\"") {
			found = true
		}
	}
	if !found {
		t.Fatalf("not logged at error level in %v", logs)
	}
}

// downgradeUserDB turns the library's user.db into a version-2 file (no
// import history), as step 5 left it.
func downgradeUserDB(t *testing.T, dir string) {
	t.Helper()
	raw, err := sql.Open("sqlite", filepath.Join(dir, userdata.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, q := range []string{"DROP TABLE import_items", "DROP TABLE imports",
		"DELETE FROM meta WHERE key = 'imports_backfilled'", "PRAGMA user_version = 2"} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

// The listing reads user.db only: an older user.db is neither upgraded
// nor backfilled, and a catalog of another format does not matter.
func TestImportsListReadOnly(t *testing.T) {
	src, dir := t.TempDir(), filepath.Join(t.TempDir(), "lib")
	writeJPEG(t, filepath.Join(src, "a.jpg"), 1)
	if out, err := run(t, "import", "--quiet", "--no-thumbs", src, dir); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	// A catalog that needs a rebuild.
	raw, err := sql.Open("sqlite", filepath.Join(dir, library.DBName))
	if err != nil {
		t.Fatal(err)
	}
	raw.Exec("PRAGMA user_version = 2")
	raw.Close()
	out, err := run(t, "imports", dir)
	if err != nil || !strings.HasPrefix(out, "Library: "+dir+"\n") || !regexp.MustCompile(`(?m)^1\s.*copy\s+done`).MatchString(out) {
		t.Fatalf("imports with an old catalog: %v\n%s", err, out)
	}
	downgradeUserDB(t, dir)
	out, err = run(t, "imports", dir)
	if err != nil || !strings.Contains(out, "No import records (user.db has schema version 2;") {
		t.Fatalf("imports on a version-2 user.db: %v\n%s", err, out)
	}
	if baks, _ := filepath.Glob(filepath.Join(dir, userdata.FileName+".bak-*")); len(baks) != 0 {
		t.Fatalf("the listing migrated user.db: %v", baks)
	}
	removeUserDB(t, dir)
	if out, err := run(t, "imports", dir); err != nil || !strings.Contains(out, "No import records.") {
		t.Fatalf("imports without user.db: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, userdata.FileName)); err == nil {
		t.Fatal("the listing created user.db")
	}
	if out, err := run(t, "imports", t.TempDir()); err == nil {
		t.Fatalf("imports of a folder that is no library:\n%s", out)
	}
}

// A record left running by a process that died is closed (failed, with
// the rows of its catalog session) by the next import, and by the
// listing.
func TestCrashedImportClosed(t *testing.T) {
	ctx := context.Background()
	src, dir := t.TempDir(), filepath.Join(t.TempDir(), "lib")
	writeJPEG(t, filepath.Join(src, "one", "a.jpg"), 1)
	writeJPEG(t, filepath.Join(src, "two", "b.jpg"), 2)
	writeJPEG(t, filepath.Join(src, "three", "c.jpg"), 3)
	if out, err := run(t, "import", "--quiet", "--no-thumbs", filepath.Join(src, "one"), dir); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	// Make record 1 look like a crashed run's: running, no items.
	crash := func(id int64) {
		u := openUserDB(t, dir)
		raw, _ := sql.Open("sqlite", filepath.Join(dir, userdata.FileName))
		for _, q := range []string{"DELETE FROM import_items WHERE import_id = ?",
			"UPDATE imports SET outcome = 'running', finished_at = NULL, items = 0, cover = NULL WHERE id = ?"} {
			if _, err := raw.Exec(q, id); err != nil {
				t.Fatal(err)
			}
		}
		raw.Close()
		u.Close()
	}
	crash(1)
	if out, err := run(t, "import", "--quiet", "--no-thumbs", filepath.Join(src, "two"), dir); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	u := openUserDB(t, dir)
	rec, _ := u.GetImport(ctx, 1)
	hs, _ := u.ImportHashes(ctx, 1)
	if rec.Outcome != userdata.ImportFailed || rec.FinishedAt.IsZero() || rec.Items != 1 || len(hs) != 1 || rec.Cover != hs[0] {
		t.Fatalf("crashed record after the next import %+v %v", rec, hs)
	}
	if rec, _ := u.GetImport(ctx, 2); rec.Outcome != userdata.ImportDone || rec.Items != 1 {
		t.Fatalf("new record %+v", rec)
	}
	u.Close()

	crash(2)
	out, err := run(t, "imports", dir)
	if err != nil || !regexp.MustCompile(`(?m)^2\s.*copy\s+failed\s+two\s+1\s`).MatchString(out) {
		t.Fatalf("listing a crashed record: %v\n%s", err, out)
	}
	u = openUserDB(t, dir)
	if hs, _ := u.ImportHashes(ctx, 2); len(hs) != 1 {
		t.Fatalf("crashed record items %v", hs)
	}
	u.Close()

	// While another process holds the library lock, the listing leaves a
	// running record alone (that process may own it).
	crash(2)
	lib, err := library.Attach(dir)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lib.Lock(ctx, "serve")
	if err != nil {
		t.Fatal(err)
	}
	out, err = run(t, "imports", dir)
	unlock()
	if err != nil || !regexp.MustCompile(`(?m)^2\s.*copy\s+running\s+two`).MatchString(out) {
		t.Fatalf("listing while the library is in use: %v\n%s", err, out)
	}
	// A rebuild closes it with the rows of the old catalog.
	if out, err := run(t, "rebuild", "--quiet", "--no-thumbs", dir); err != nil {
		t.Fatalf("rebuild: %v\n%s", err, out)
	}
	u = openUserDB(t, dir)
	if rec, _ := u.GetImport(ctx, 2); rec.Outcome != userdata.ImportFailed || rec.Items != 1 {
		t.Fatalf("crashed record after a rebuild %+v", rec)
	}
}

// rebuild says when it upgraded user.db, and warns when the old
// catalog's sessions cannot be read (the backfill is tried again later).
func TestRebuildMigrationNoticeAndBackfillWarning(t *testing.T) {
	src, dir := t.TempDir(), filepath.Join(t.TempDir(), "lib")
	writeJPEG(t, filepath.Join(src, "a.jpg"), 1)
	if out, err := run(t, "import", "--quiet", "--no-thumbs", src, dir); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	downgradeUserDB(t, dir)
	raw, err := sql.Open("sqlite", filepath.Join(dir, library.DBName))
	if err != nil {
		t.Fatal(err)
	}
	raw.Exec("PRAGMA user_version = 1") // a catalog of another format: no sessions to read
	raw.Close()
	out, err := run(t, "rebuild", "--quiet", "--no-thumbs", dir)
	if err != nil || !strings.Contains(out, "user.db upgraded from schema version 2 to 3 (the old file is kept as user.db.bak-") ||
		!strings.Contains(out, "Warning: the old catalog's import sessions could not be copied into the import history:") {
		t.Fatalf("rebuild: %v\n%s", err, out)
	}
	u := openUserDB(t, dir)
	if ok, _ := u.ImportsBackfilled(context.Background()); ok {
		t.Fatal("marked as backfilled after a failure")
	}
}
