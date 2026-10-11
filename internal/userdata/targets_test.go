package userdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// v1Schema creates a version-1 user.db (as step 4 wrote it) at p with one
// rating.
func v1Schema(t *testing.T, p string) {
	t.Helper()
	raw, err := sql.Open("sqlite", p+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, q := range []string{schemaV1, `INSERT INTO ratings (hash, rating, updated_at) VALUES (-5, 4, 1)`, `PRAGMA user_version = 1`} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrateV1(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	v1Schema(t, p)
	if err := CheckFile(ctx, p); err != nil {
		t.Fatalf("CheckFile refuses version 1: %v", err)
	}
	d, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	from, bak := d.Migrated()
	if from != 1 || !regexp.MustCompile(`user\.db\.bak-\d{8}-\d{6}$`).MatchString(bak) || filepath.Dir(bak) != dir {
		t.Fatalf("Migrated() = %d %q", from, bak)
	}
	var v int
	d.db.QueryRow("PRAGMA user_version").Scan(&v)
	if v != SchemaVersion {
		t.Fatalf("user_version %d", v)
	}
	if r, err := d.Rating(ctx, uint64(1<<64-5)); err != nil || r != 4 {
		t.Fatalf("rating after migration %d %v", r, err)
	}
	id, err := d.LibraryID(ctx)
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(id) {
		t.Fatalf("library id %q %v", id, err)
	}
	if c, err := d.CreatedAt(ctx); err != nil || time.Since(c) > time.Minute {
		t.Fatalf("created_at %v %v", c, err)
	}
	// The backup is the version-1 database with the data.
	bd, err := sql.Open("sqlite", bak)
	if err != nil {
		t.Fatal(err)
	}
	defer bd.Close()
	var bv, n int
	if err := bd.QueryRow("SELECT (SELECT user_version FROM pragma_user_version), (SELECT COUNT(*) FROM ratings)").Scan(&bv, &n); err != nil || bv != 1 || n != 1 {
		t.Fatalf("backup version %d ratings %d %v", bv, n, err)
	}
	d.Close()

	// Reopening keeps the id and makes no second backup.
	d2, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if from, _ := d2.Migrated(); from != 0 {
		t.Fatal("migrated again")
	}
	if id2, _ := d2.LibraryID(ctx); id2 != id {
		t.Fatalf("library id changed %s -> %s", id, id2)
	}
	baks, _ := filepath.Glob(filepath.Join(dir, "user.db.bak-*"))
	if len(baks) != 1 {
		t.Fatalf("backups %v", baks)
	}
}

func TestMigrateV1BackupFails(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	v1Schema(t, p)
	// Every candidate backup name is taken: the migration is refused, the
	// file stays at version 1 and no temporary copy is left.
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.Local)
	base := p + ".bak-" + now.Format(backupStampLayout)
	os.WriteFile(base, []byte("x"), 0o644)
	for i := 2; i <= 100; i++ {
		os.WriteFile(fmt.Sprintf("%s-%d", base, i), []byte("x"), 0o644)
	}
	sqldb, err := sql.Open("sqlite", p+dsnParams)
	if err != nil {
		t.Fatal(err)
	}
	d := &DB{db: sqldb, path: p, now: func() time.Time { return now }}
	err = d.ensureSchema(ctx)
	if err == nil || !strings.Contains(err.Error(), "left untouched") {
		t.Fatalf("migration with no backup name: %v", err)
	}
	var v int
	sqldb.QueryRow("PRAGMA user_version").Scan(&v)
	sqldb.Close()
	if v != 1 {
		t.Fatalf("user_version %d after a refused migration", v)
	}
	if tmps, _ := filepath.Glob(p + ".bak-*.tmp"); len(tmps) != 0 {
		t.Fatalf("temporary copies left: %v", tmps)
	}
}

func TestFreshIsV2WithIdentity(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	if from, bak := d.Migrated(); from != 0 || bak != "" {
		t.Fatal("a fresh database was migrated")
	}
	id, err := d.LibraryID(ctx)
	if err != nil || len(id) != 16 {
		t.Fatalf("id %q %v", id, err)
	}
	// Two libraries get different ids.
	d2 := openTest(t)
	if id2, _ := d2.LibraryID(ctx); id2 == id {
		t.Fatal("same id twice")
	}
}

func TestBackupTargets(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	if ts, err := d.BackupTargets(ctx); err != nil || len(ts) != 0 {
		t.Fatalf("targets %v %v", ts, err)
	}
	if s, err := d.BackupSummary(ctx); err != nil || s.Targets != 0 || !s.LastBackupAt.IsZero() {
		t.Fatalf("summary %+v %v", s, err)
	}
	tg, created, err := d.AddBackupTarget(ctx, BackupTarget{ID: "0123456789abcdef", Label: "  Red WD drive ", LastPath: "/media/x", LastHost: "h1"})
	if err != nil || !created || tg.Label != "Red WD drive" || tg.LastPath != "/media/x" || tg.AddedAt.IsZero() {
		t.Fatalf("add %+v %v %v", tg, created, err)
	}
	if _, _, err := d.AddBackupTarget(ctx, BackupTarget{ID: "1111111111111111", Label: "", LastPath: "/y"}); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("empty label: %v", err)
	}
	// Adding again updates path and host.
	tg, created, err = d.AddBackupTarget(ctx, BackupTarget{ID: "0123456789abcdef", Label: "Red", LastPath: "E:\\", LastHost: "h2"})
	if err != nil || created || tg.Label != "Red" || tg.LastPath != "E:\\" || tg.LastHost != "h2" {
		t.Fatalf("re-add %+v %v %v", tg, created, err)
	}
	set := "photos-2"
	tg, err = d.UpdateBackupTarget(ctx, tg.ID, TargetUpdate{SetName: &set})
	if err != nil || tg.SetName != set || tg.Label != "Red" {
		t.Fatalf("update %+v %v", tg, err)
	}
	bad := "a\nb"
	if _, err := d.UpdateBackupTarget(ctx, tg.ID, TargetUpdate{Label: &bad}); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("bad label: %v", err)
	}
	if _, err := d.UpdateBackupTarget(ctx, "ffffffffffffffff", TargetUpdate{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	at := time.Unix(1_800_000_000, 0)
	if err := d.MarkBackup(ctx, tg.ID, at); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkScrub(ctx, tg.ID, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	tg, _ = d.BackupTarget(ctx, tg.ID)
	if !tg.LastBackupAt.Equal(at) || !tg.LastScrubAt.Equal(at.Add(time.Hour)) {
		t.Fatalf("marks %+v", tg)
	}
	if s, err := d.BackupSummary(ctx); err != nil || s.Targets != 1 || !s.LastBackupAt.Equal(at) || s.LastLabel != "Red" {
		t.Fatalf("summary %+v %v", s, err)
	}
	if err := d.MarkBackup(ctx, "ffffffffffffffff", at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mark unknown: %v", err)
	}
	d.Close()
	s, ok, err := PeekBackupSummary(ctx, d.Path())
	if err != nil || !ok || s.Targets != 1 || s.LastLabel != "Red" {
		t.Fatalf("peek %+v %v %v", s, ok, err)
	}
	if _, ok, err := PeekBackupSummary(ctx, filepath.Join(t.TempDir(), FileName)); ok || err != nil {
		t.Fatalf("peek missing %v %v", ok, err)
	}
	d, err = Open(ctx, d.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.ForgetBackupTarget(ctx, tg.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.ForgetBackupTarget(ctx, tg.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("forget twice: %v", err)
	}
	if _, err := d.BackupTarget(ctx, tg.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get forgotten: %v", err)
	}
}

func TestSnapshot(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	if _, err := d.SetRating(ctx, []uint64{7}, 5); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "snap.db")
	if err := d.Snapshot(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := d.Snapshot(ctx, p); err == nil {
		t.Fatal("snapshot replaced an existing file")
	}
	s, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if r, _ := s.Rating(ctx, 7); r != 5 {
		t.Fatal("snapshot lacks the rating")
	}
	a, _ := d.LibraryID(ctx)
	b, _ := s.LibraryID(ctx)
	if a != b {
		t.Fatal("snapshot has another library id")
	}
}
