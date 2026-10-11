package userdata

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// v2Schema creates a version-2 user.db (as step 5 wrote it) at p with one
// rating, a library id and a backup target.
func v2Schema(t *testing.T, p string) {
	t.Helper()
	raw, err := sql.Open("sqlite", p+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, q := range []string{schemaV1, schemaV2,
		`INSERT INTO ratings (hash, rating, updated_at) VALUES (-5, 4, 1)`,
		`INSERT INTO meta (key, value) VALUES ('library_id', '0123456789abcdef'), ('created_at', '1')`,
		`INSERT INTO backup_targets (id, label, last_path, last_host, added_at) VALUES ('fedcba9876543210', 'Red', '/mnt/red', 'h', 1)`,
		`PRAGMA user_version = 2`} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

// schemaOf returns the schema SQL of the database, by object name.
func schemaOf(t *testing.T, d *DB) map[string]string {
	t.Helper()
	rows, err := d.db.Query(`SELECT name, sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, s string
		if err := rows.Scan(&n, &s); err != nil {
			t.Fatal(err)
		}
		out[n] = strings.Join(strings.Fields(s), " ")
	}
	return out
}

func TestMigrateV2ToV3(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	v2Schema(t, p)
	if err := CheckFile(ctx, p); err != nil {
		t.Fatalf("CheckFile refuses version 2: %v", err)
	}
	if err := IntegrityCheck(ctx, p); err != nil {
		t.Fatalf("IntegrityCheck refuses version 2: %v", err)
	}
	if s, ok, err := PeekBackupSummary(ctx, p); err != nil || !ok || s.Targets != 1 {
		t.Fatalf("PeekBackupSummary on version 2: %+v %v %v", s, ok, err)
	}
	// Read-only (backup dry run) opens a version-2 file as it is.
	ro, err := OpenReadOnly(ctx, p)
	if err != nil {
		t.Fatalf("OpenReadOnly on version 2: %v", err)
	}
	if id, err := ro.LibraryID(ctx); err != nil || id != "0123456789abcdef" {
		t.Fatalf("read-only id %q %v", id, err)
	}
	ro.Close()

	d, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	from, bak := d.Migrated()
	if from != 2 || filepath.Dir(bak) != dir {
		t.Fatalf("Migrated() = %d %q", from, bak)
	}
	var v int
	d.db.QueryRow("PRAGMA user_version").Scan(&v)
	if v != 3 {
		t.Fatalf("user_version %d", v)
	}
	// Data and identity survive; the import history is there and empty.
	if r, err := d.Rating(ctx, uint64(1<<64-5)); err != nil || r != 4 {
		t.Fatalf("rating %d %v", r, err)
	}
	if id, err := d.LibraryID(ctx); err != nil || id != "0123456789abcdef" {
		t.Fatalf("library id %q %v", id, err)
	}
	if ts, err := d.BackupTargets(ctx); err != nil || len(ts) != 1 {
		t.Fatalf("targets %+v %v", ts, err)
	}
	if l, err := d.ListImports(ctx); err != nil || len(l) != 0 {
		t.Fatalf("imports %+v %v", l, err)
	}
	// The backup is the version-2 file.
	bd, err := sql.Open("sqlite", bak)
	if err != nil {
		t.Fatal(err)
	}
	defer bd.Close()
	var bv int
	if err := bd.QueryRow("PRAGMA user_version").Scan(&bv); err != nil || bv != 2 {
		t.Fatalf("backup version %d %v", bv, err)
	}
	// The migrated schema is the one a fresh database gets.
	fresh := openTest(t)
	if got, want := schemaOf(t, d), schemaOf(t, fresh); !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated schema differs from a fresh one:\n%v\n%v", got, want)
	}
}

func TestMigrateV1ToV3(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	v1Schema(t, p)
	d, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if from, _ := d.Migrated(); from != 1 {
		t.Fatalf("migrated from %d", from)
	}
	// Both steps ran, with one backup copy.
	baks, _ := filepath.Glob(filepath.Join(dir, "user.db.bak-*"))
	if len(baks) != 1 {
		t.Fatalf("backups %v", baks)
	}
	if id, err := d.LibraryID(ctx); err != nil || len(id) != 16 {
		t.Fatalf("library id %q %v", id, err)
	}
	if _, err := d.BeginImport(ctx, ImportRecord{Source: "/x", Mode: ImportModeCopy}); err != nil {
		t.Fatal(err)
	}
	fresh := openTest(t)
	if got, want := schemaOf(t, d), schemaOf(t, fresh); !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated schema differs from a fresh one:\n%v\n%v", got, want)
	}
}

func TestFreshIsV3(t *testing.T) {
	d := openTest(t)
	var v int
	d.db.QueryRow("PRAGMA user_version").Scan(&v)
	if v != 3 || SchemaVersion != 3 {
		t.Fatalf("user_version %d", v)
	}
	if from, _ := d.Migrated(); from != 0 {
		t.Fatal("fresh database migrated")
	}
	if ok, err := d.ImportsBackfilled(context.Background()); err != nil || ok {
		t.Fatalf("backfilled %v %v", ok, err)
	}
}

func TestImportRecords(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }

	// A crash between begin and finish leaves a running record.
	crashed, err := d.BeginImport(ctx, ImportRecord{Source: "/media/phone/DCIM", Mode: ImportModeMove, Host: "box"})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := d.GetImport(ctx, crashed)
	if err != nil || rec.Outcome != ImportRunning || !rec.FinishedAt.IsZero() || rec.SourceName != "DCIM" ||
		rec.Mode != "move" || rec.Host != "box" || !rec.StartedAt.Equal(now) || rec.CatalogSession != 0 {
		t.Fatalf("running record %+v %v", rec, err)
	}

	now = now.Add(time.Hour)
	id, err := d.BeginImport(ctx, ImportRecord{Source: "/home/u/Pictures", Mode: ImportModeCopy, Host: "box"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	items := []ImportItem{{10, ItemImported}, {11, ItemExisting}, {12, ItemRestored}, {10, ItemExisting}, {1 << 63, ItemImported}}
	if err := d.FinishImport(ctx, id, ImportRecord{Outcome: ImportDone, Imported: 3, Existing: 1, Duplicates: 2,
		Skipped: 4, Errors: 1, Bytes: 12345, CatalogSession: 7}, items); err != nil {
		t.Fatal(err)
	}
	rec, err = d.GetImport(ctx, id)
	if err != nil || rec.Outcome != ImportDone || !rec.FinishedAt.Equal(now) || rec.Imported != 3 || rec.Existing != 1 ||
		rec.Duplicates != 2 || rec.Skipped != 4 || rec.Errors != 1 || rec.Bytes != 12345 || rec.CatalogSession != 7 {
		t.Fatalf("finished record %+v %v", rec, err)
	}
	hs, err := d.ImportHashes(ctx, id)
	if err != nil || !reflect.DeepEqual(hs, []uint64{10, 11, 12, 1 << 63}) {
		t.Fatalf("hashes %v %v", hs, err)
	}
	var status string
	d.db.QueryRow("SELECT status FROM import_items WHERE import_id = ? AND hash = 10", id).Scan(&status)
	if status != ItemImported {
		t.Fatalf("a repeated item replaced the first status: %s", status)
	}

	// Validation.
	if err := d.FinishImport(ctx, id, ImportRecord{Outcome: ImportRunning}, nil); err == nil {
		t.Fatal("finish with outcome running accepted")
	}
	if err := d.FinishImport(ctx, id, ImportRecord{Outcome: ImportDone}, []ImportItem{{1, "duplicate"}}); err == nil {
		t.Fatal("item status duplicate accepted")
	}
	if _, err := d.BeginImport(ctx, ImportRecord{Source: "/x", Mode: "teleport"}); err == nil {
		t.Fatal("mode accepted")
	}
	if err := d.FinishImport(ctx, 999, ImportRecord{Outcome: ImportDone}, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("finish unknown: %v", err)
	}

	// The list: newest first, item count and first item.
	l, err := d.ListImports(ctx)
	if err != nil || len(l) != 2 || l[0].ID != id || l[0].Items != 4 || l[0].Cover != 10 ||
		l[1].ID != crashed || l[1].Items != 0 || l[1].Cover != 0 || l[1].Outcome != ImportRunning {
		t.Fatalf("list %+v %v", l, err)
	}

	// ImportsOf.
	refs, err := d.ImportsOf(ctx, 11)
	if err != nil || len(refs) != 1 || refs[0].ID != id {
		t.Fatalf("imports of %+v %v", refs, err)
	}
	if refs, err := d.ImportsOf(ctx, 99); err != nil || len(refs) != 0 {
		t.Fatalf("imports of unknown %+v %v", refs, err)
	}

	// Rename: trimmed, may be cleared, length and character rules.
	if rec, err := d.RenameImport(ctx, id, "  Summer phone  "); err != nil || rec.Name != "Summer phone" {
		t.Fatalf("rename %+v %v", rec, err)
	}
	if _, err := d.RenameImport(ctx, id, strings.Repeat("x", MaxImportName+1)); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("long name: %v", err)
	}
	if _, err := d.RenameImport(ctx, id, "a\nb"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("control character: %v", err)
	}
	if rec, err := d.RenameImport(ctx, id, strings.Repeat("é", MaxImportName)); err != nil || len([]rune(rec.Name)) != MaxImportName {
		t.Fatalf("200 runes: %v", err)
	}
	if rec, err := d.RenameImport(ctx, id, "   "); err != nil || rec.Name != "" {
		t.Fatalf("clear %+v %v", rec, err)
	}
	if _, err := d.RenameImport(ctx, 999, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rename unknown: %v", err)
	}

	// Delete removes the record and its items only.
	if err := d.DeleteImport(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GetImport(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
	var n int
	d.db.QueryRow("SELECT COUNT(*) FROM import_items").Scan(&n)
	if n != 0 {
		t.Fatalf("%d items left", n)
	}
	if err := d.DeleteImport(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestBackfillImports(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	t0 := time.Date(2025, 5, 1, 10, 0, 0, 0, time.UTC)
	sessions := []BackfillSession{
		{CatalogSession: 1, StartedAt: t0, FinishedAt: t0.Add(time.Minute), Source: "/src/a", Mode: "copy",
			Imported: 2, Duplicates: 1, Skipped: 3, Hashes: []uint64{1, 2}},
		{CatalogSession: 2, StartedAt: t0.Add(time.Hour), Source: "/src/empty", Mode: "move"}, // no rows: skipped
		{CatalogSession: 3, StartedAt: t0.Add(2 * time.Hour), Source: "/lib", Mode: "record", Existing: 1,
			Hashes: []uint64{3}}, // never finished
	}
	n, err := d.BackfillImports(ctx, sessions)
	if err != nil || n != 2 {
		t.Fatalf("backfill %d %v", n, err)
	}
	l, err := d.ListImports(ctx)
	if err != nil || len(l) != 2 {
		t.Fatalf("list %+v %v", l, err)
	}
	if r := l[0]; r.CatalogSession != 3 || r.Outcome != ImportCancelled || r.Mode != "record" || r.SourceName != "lib" ||
		r.Host != "" || r.Name != "" || r.Items != 1 || r.Existing != 1 || !r.FinishedAt.IsZero() {
		t.Fatalf("unfinished session %+v", r)
	}
	if r := l[1]; r.CatalogSession != 1 || r.Outcome != ImportDone || !r.StartedAt.Equal(t0) ||
		!r.FinishedAt.Equal(t0.Add(time.Minute)) || r.Imported != 2 || r.Duplicates != 1 || r.Skipped != 3 || r.Items != 2 {
		t.Fatalf("finished session %+v", r)
	}
	hs, _ := d.ImportHashes(ctx, l[1].ID)
	if !reflect.DeepEqual(hs, []uint64{1, 2}) {
		t.Fatalf("hashes %v", hs)
	}
	if ok, err := d.ImportsBackfilled(ctx); err != nil || !ok {
		t.Fatalf("backfilled %v %v", ok, err)
	}
	// Once only.
	if n, err := d.BackfillImports(ctx, sessions); err != nil || n != 0 {
		t.Fatalf("second backfill %d %v", n, err)
	}

	// A database that already has records is only marked.
	d2 := openTest(t)
	if _, err := d2.BeginImport(ctx, ImportRecord{Source: "/x", Mode: ImportModeRebuild}); err != nil {
		t.Fatal(err)
	}
	if n, err := d2.BackfillImports(ctx, sessions); err != nil || n != 0 {
		t.Fatalf("backfill with records %d %v", n, err)
	}
	if ok, _ := d2.ImportsBackfilled(ctx); !ok {
		t.Fatal("not marked")
	}
	if l, _ := d2.ListImports(ctx); len(l) != 1 {
		t.Fatalf("list %+v", l)
	}
}
