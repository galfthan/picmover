package cmd

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/testimg"
)

// Rebuild through a library path that contains a symlink records the same
// rows: the cache previews under .picmover are never imported as photos.
func TestRebuildSymlinkedLibraryImportsNoPreviews(t *testing.T) {
	src, base := t.TempDir(), t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	dir := filepath.Join(link, "lib")
	writeJPEG(t, filepath.Join(src, "a.jpg"), 1)
	writeJPEG(t, filepath.Join(src, "b.jpg"), 2)
	if out, err := run(t, "import", "--quiet", src, dir); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	before := mediaSet(t, dir)
	if len(previewMTimes(t, dir)) != 2 {
		t.Fatal("setup: previews not generated")
	}
	out, err := run(t, "rebuild", "--quiet", dir)
	if err != nil || !strings.Contains(out, "Already in place:  2\n") || !strings.Contains(out, "Imported:          0\n") {
		t.Fatalf("rebuild: %v\n%s", err, out)
	}
	after := mediaSet(t, dir)
	if len(after) != len(before) {
		t.Errorf("rows before %v, after %v", before, after)
	}
	for rel := range after {
		if _, ok := before[rel]; !ok {
			t.Errorf("rebuild recorded %s", rel)
		}
	}
}

// A file whose computed layout folder differs from where it is (here the
// rebuild uses another --timezone) is recorded where it is: one file on
// disk, one row.
func TestRebuildRecordsInPlace(t *testing.T) {
	src, dir := t.TempDir(), filepath.Join(t.TempDir(), "lib")
	p := filepath.Join(src, "noexif.png")
	writeBytes(t, p, testimg.PNG(40, 30, 7))
	mt := time.Date(2020, 1, 31, 23, 30, 0, 0, time.UTC)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "import", "--quiet", "--timezone", "Pacific/Auckland", src, dir); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	before := mediaSet(t, dir)
	if _, ok := before["image/2020/02/noexif.png"]; !ok || len(before) != 1 {
		t.Fatalf("setup rows = %v", before)
	}
	// Plus a file outside the layout and an identical copy of it.
	writeJPEG(t, filepath.Join(dir, "old", "x.jpg"), 9)
	writeJPEG(t, filepath.Join(dir, "old", "copy-of-x.jpg"), 9)

	out, err := run(t, "rebuild", "--quiet", "--timezone", "UTC", dir)
	if err != nil || !strings.Contains(out, "Duplicates:        1\n") || !strings.Contains(out, "Copied:            0 B\n") {
		t.Fatalf("rebuild: %v\n%s", err, out)
	}
	var files []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == library.MetaDir {
			return filepath.SkipDir
		}
		if !d.IsDir() && !strings.HasPrefix(d.Name(), library.DBName) {
			rel, _ := filepath.Rel(dir, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if len(files) != 3 {
		t.Errorf("files on disk after rebuild: %v", files)
	}
	after := mediaSet(t, dir)
	if len(after) != 2 {
		t.Errorf("rows after rebuild: %v", after)
	}
	if _, ok := after["image/2020/02/noexif.png"]; !ok {
		t.Errorf("file not recorded in place: %v", after)
	}
	if _, ok := after["old/x.jpg"]; !ok {
		if _, ok := after["old/copy-of-x.jpg"]; !ok {
			t.Errorf("file outside the layout not recorded: %v", after)
		}
	}
}

// Without media.db, rebuild records the files into a new catalog (no backup
// is made) and names the newest existing backup so a mistaken rebuild is
// noticed. --no-thumbs leaves the rows pending.
func TestRebuildMissingCatalog(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"media.db.bak-20240101-000000", "media.db.bak-20250101-000000",
		"media.db.bak-20250101-000000-wal", "media.db.bak-holiday.jpg"} {
		writeBytes(t, filepath.Join(dir, n), []byte("x"))
	}
	writeBytes(t, filepath.Join(dir, "image", "2020", "02", "noexif.png"), testimg.PNG(64, 64, 1))
	out, err := run(t, "rebuild", "--no-thumbs", "--quiet", dir)
	if err != nil {
		t.Fatalf("rebuild without media.db: %v\n%s", err, out)
	}
	for _, want := range []string{"No media.db in " + dir, "2 catalog backup(s) exist, the newest media.db.bak-20250101-000000;",
		"delete the new media.db (and its -wal/-shm files) afterwards and rename the backup"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Old catalog saved as") {
		t.Errorf("a backup was reported without a catalog:\n%s", out)
	}
	for _, n := range []string{"media.db.bak-20240101-000000", "media.db.bak-20250101-000000",
		"media.db.bak-20250101-000000-wal", "media.db.bak-holiday.jpg"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("backup touched: %v", err)
		}
	}
	// media.db.bak-holiday.jpg is not a backup name, so it is media.
	after := mediaSet(t, dir)
	if len(after) != 2 {
		t.Fatalf("rows after rebuild: %v", after)
	}
	for _, rel := range []string{"image/2020/02/noexif.png", "media.db.bak-holiday.jpg"} {
		if _, ok := after[rel]; !ok {
			t.Errorf("%s not recorded in place: %v", rel, after)
		}
	}
	// --no-thumbs: no thumbnail cache, or an empty one.
	if _, err := os.Stat(filepath.Join(dir, library.MetaDir, "thumbs.db")); err == nil {
		if n := thumbRows(t, dir); n != 0 {
			t.Errorf("thumbnails generated despite --no-thumbs: %d", n)
		}
	}
	// A second rebuild now finds the catalog and backs it up.
	out, err = run(t, "rebuild", "--no-thumbs", "--quiet", dir)
	if err != nil || !strings.Contains(out, "Old catalog saved as ") {
		t.Fatalf("second rebuild: %v\n%s", err, out)
	}
}

func TestBackupCatalogJournal(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "media.db")
	for _, s := range library.CatalogFileSuffixes {
		writeBytes(t, db+s, []byte("x"+s))
	}
	now := time.Date(2026, 10, 10, 9, 8, 7, 0, time.UTC)
	got, err := backupCatalog(db, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range library.CatalogFileSuffixes {
		if b, err := os.ReadFile(got + s); err != nil || string(b) != "x"+s {
			t.Errorf("backup%s: %q %v", s, b, err)
		}
		if _, err := os.Lstat(db + s); err == nil {
			t.Errorf("media.db%s still exists", s)
		}
		if !library.IsCatalogBackup(filepath.Base(got + s)) {
			t.Errorf("%s not recognised as a backup", got+s)
		}
	}
}

// corruptThumbsVersion gives the library's thumbs.db another format version.
func corruptThumbsVersion(t *testing.T, dir string) {
	t.Helper()
	raw, err := sql.Open("sqlite", filepath.Join(dir, library.MetaDir, "thumbs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
}

// A thumbnail stage that cannot run does not fail a successful import: the
// summary is printed, the cache error is a warning, the exit status is 2.
// db reports the cache as unavailable instead of failing.
func TestThumbStageFailureIsNotImportFailure(t *testing.T) {
	src, dir := t.TempDir(), filepath.Join(t.TempDir(), "lib")
	writeJPEG(t, filepath.Join(src, "a.jpg"), 1)
	if out, err := run(t, "import", "--quiet", src, dir); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	corruptThumbsVersion(t, dir)
	writeJPEG(t, filepath.Join(src, "b.jpg"), 2)
	out, err := run(t, "import", "--quiet", src, dir)
	if exitCode(err) != 2 || !strings.Contains(out, "Imported:          1\n") ||
		!strings.Contains(out, "Warning: thumbnails were not generated") || !strings.Contains(err.Error(), "picmover thumbs") {
		t.Fatalf("import with a broken cache: code %d (%v)\n%s", exitCode(err), err, out)
	}
	if rows := mediaSet(t, dir); len(rows) != 2 {
		t.Errorf("rows = %v", rows)
	}
	out, err = run(t, "db", dir)
	if err != nil || !strings.Contains(out, "Thumbnail cache:      unavailable (") ||
		!strings.Contains(out, "1 ok, 1 pending") {
		t.Fatalf("db with a broken cache: %v\n%s", err, out)
	}
}

// db never writes the cache: it works while the cache is locked by a
// writer and leaves no -wal/-shm of its own.
func TestDBOpensCacheReadOnly(t *testing.T) {
	src, dir := t.TempDir(), filepath.Join(t.TempDir(), "lib")
	writeJPEG(t, filepath.Join(src, "a.jpg"), 1)
	if out, err := run(t, "import", "--quiet", src, dir); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	raw, err := sql.Open("sqlite", filepath.Join(dir, library.MetaDir, "thumbs.db")+"?_pragma=busy_timeout(100)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	conn, err := raw.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	out, err := run(t, "db", dir)
	if err != nil || !strings.Contains(out, "Thumbnail cache:      1 entries, ") {
		t.Fatalf("db while the cache is being written: %v\n%s", err, out)
	}
}

// Changing a row's orientation marks its thumbnail stale; thumbs then
// regenerates it.
func TestUpdateMetadataOrientationMarksThumbStale(t *testing.T) {
	src, dir := t.TempDir(), filepath.Join(t.TempDir(), "lib")
	writeBytes(t, filepath.Join(src, "a.jpg"), testimg.JPEG(testimg.Options{Width: 32, Height: 16,
		DateTime: time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC), Orientation: 6}))
	if out, err := run(t, "import", "--quiet", src, dir); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	rel := "image/2021/03/a.jpg"
	db, err := catalog.OpenExisting(context.Background(), filepath.Join(dir, library.DBName), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE media SET orientation = 1"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if m := getRow(t, dir, rel); m.ThumbState != catalog.ThumbStateOK {
		t.Fatalf("setup state %q", m.ThumbState)
	}
	out, err := run(t, "update-metadata", dir)
	if err != nil || !strings.Contains(out, "Orientation: 1 -> 6") {
		t.Fatalf("update-metadata: %v\n%s", err, out)
	}
	if m := getRow(t, dir, rel); m.ThumbState != catalog.ThumbStateStale {
		t.Fatalf("state after orientation change = %q, want stale", m.ThumbState)
	}
	if out, err := run(t, "db", dir); err != nil || !strings.Contains(out, "0 ok, 1 pending") {
		t.Fatalf("db: %v\n%s", err, out)
	}
	previews := previewMTimes(t, dir)
	time.Sleep(20 * time.Millisecond)
	out, err = run(t, "thumbs", "--quiet", dir)
	if err != nil || !strings.Contains(out, "Done:              1\n") || !strings.Contains(out, "Still pending:     0\n") {
		t.Fatalf("thumbs: %v\n%s", err, out)
	}
	if m := getRow(t, dir, rel); m.ThumbState != catalog.ThumbStateOK {
		t.Errorf("state after thumbs = %q", m.ThumbState)
	}
	for p, mt := range previewMTimes(t, dir) {
		if previews[p].Equal(mt) {
			t.Errorf("stale preview %s not regenerated", p)
		}
	}
}

// A mistyped path must not become a library.
func TestRebuildRequiresDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "typo")
	if out, err := run(t, "rebuild", "--no-thumbs", "--quiet", dir); err == nil {
		t.Fatalf("rebuild of a missing directory succeeded:\n%s", out)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("rebuild created %s: %v", dir, err)
	}
}
