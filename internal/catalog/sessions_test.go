package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestSessionsWithHashes(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	if s, err := d.Sessions(ctx); err != nil || len(s) != 0 {
		t.Fatalf("no sessions: %v %v", s, err)
	}
	s1, _ := d.BeginSession(ctx, "/a", "copy")
	s2, _ := d.BeginSession(ctx, "/b", "move") // no rows, never finished
	s3, _ := d.BeginSession(ctx, "/lib", "record")
	if err := d.FinishSession(ctx, s1, SessionStats{Imported: 2, Duplicates: 1, SkippedSmall: 1, NonMedia: 2, Errors: 1}); err != nil {
		t.Fatal(err)
	}
	rows := []Media{
		{Hash: 1, Size: 1, RelPath: "1.jpg", FileType: "image", Ext: "jpg", ImportSessionID: s1},
		{Hash: 2, Size: 1, RelPath: "2.jpg", FileType: "image", Ext: "jpg", ImportSessionID: s3},
		{Hash: 3, Size: 1, RelPath: "3.jpg", FileType: "image", Ext: "jpg", ImportSessionID: s1, DeletedAt: time.Unix(1, 0)},
		{Hash: 4, Size: 1, RelPath: "4.jpg", FileType: "image", Ext: "jpg"},
	}
	for i := range rows {
		insert(t, d, &rows[i])
	}
	got, err := d.Sessions(ctx)
	if err != nil || len(got) != 3 {
		t.Fatalf("sessions %+v %v", got, err)
	}
	a, b, c := got[0], got[1], got[2]
	if a.ID != s1 || a.Source != "/a" || a.Mode != "copy" || a.FinishedAt.IsZero() || a.StartedAt.IsZero() ||
		a.Imported != 2 || a.Duplicates != 1 || a.SkippedSmall != 1 || a.NonMedia != 2 || a.Errors != 1 ||
		fmt.Sprint(a.Hashes) != "[1 3]" {
		t.Errorf("session 1 %+v", a)
	}
	if b.ID != s2 || b.Mode != "move" || !b.FinishedAt.IsZero() || len(b.Hashes) != 0 {
		t.Errorf("session 2 %+v", b)
	}
	if c.ID != s3 || fmt.Sprint(c.Hashes) != "[2]" {
		t.Errorf("session 3 %+v", c)
	}

	// SessionsFile reads the same while the catalog is open (data in its
	// WAL) and writes nothing: the -wal file is left as it is.
	before, err := os.Stat(d.path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	fromFile, err := SessionsFile(ctx, d.path)
	if err != nil || !reflect.DeepEqual(fromFile, got) {
		t.Fatalf("SessionsFile %+v %v", fromFile, err)
	}
	if after, err := os.Stat(d.path + "-wal"); err != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("wal changed: %v", err)
	}
	// Another schema version is refused.
	if _, err := d.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	if _, err := SessionsFile(ctx, d.path); !errors.Is(err, ErrNeedsRebuild) {
		t.Fatalf("SessionsFile on version 99: %v", err)
	}
}

func TestSessionNewestAndHashes(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	s1, _ := d.BeginSession(ctx, "/a", "copy")
	rows := []Media{
		{Hash: 1, Size: 1, RelPath: "1.jpg", FileType: "image", Ext: "jpg", ImportSessionID: s1, TakenAt: time.Unix(100, 0)},
		{Hash: 2, Size: 1, RelPath: "2.jpg", FileType: "image", Ext: "jpg", ImportSessionID: s1, TakenAt: time.Unix(300, 0)},
		{Hash: 3, Size: 1, RelPath: "3.jpg", FileType: "image", Ext: "jpg", ImportSessionID: s1, TakenAt: time.Unix(900, 0), DeletedAt: time.Unix(1, 0)},
		{Hash: 4, Size: 1, RelPath: "4.jpg", FileType: "image", Ext: "jpg", ImportSessionID: s1}, // undated
		{Hash: 5, Size: 1, RelPath: "5.jpg", FileType: "image", Ext: "jpg", TakenAt: time.Unix(999, 0)},
	}
	for i := range rows {
		insert(t, d, &rows[i])
	}
	got, err := d.Sessions(ctx)
	if err != nil || len(got) != 1 || got[0].Newest != 2 || fmt.Sprint(got[0].Hashes) != "[1 2 3 4]" {
		t.Fatalf("sessions %+v %v", got, err)
	}
	hs, newest, err := d.SessionHashes(ctx, s1)
	if err != nil || fmt.Sprint(hs) != "[1 2 3 4]" || newest != 2 {
		t.Fatalf("session hashes %v %d %v", hs, newest, err)
	}
	if want, ok, _ := d.NewestOf(ctx, hs); !ok || want != newest {
		t.Fatalf("NewestOf %d, sessions %d", want, newest)
	}
	if hs, newest, err := d.SessionHashes(ctx, 99); err != nil || len(hs) != 0 || newest != 0 {
		t.Fatalf("unknown session %v %d %v", hs, newest, err)
	}
	if hs, _, err := SessionHashesFile(ctx, d.path, s1); err != nil || fmt.Sprint(hs) != "[1 2 3 4]" {
		t.Fatalf("SessionHashesFile %v %v", hs, err)
	}
	n, cover, ok, err := d.LiveSummary(ctx, []uint64{1, 2, 3, 4, 77})
	if err != nil || n != 3 || !ok || cover != 2 {
		t.Fatalf("live summary %d %d %v %v", n, cover, ok, err)
	}
	if n, _, ok, err := d.LiveSummary(ctx, []uint64{3}); err != nil || n != 0 || ok {
		t.Fatalf("live summary of a trashed row %d %v %v", n, ok, err)
	}
	// Only undated rows: the newest is the last one.
	if _, cover, ok, _ := d.LiveSummary(ctx, []uint64{4}); !ok || cover != 4 {
		t.Fatalf("undated cover %d", cover)
	}
}

// v2CatalogDDL is a schema-version-2 catalog as step 1 wrote it, with
// import_sessions missing a counter column (a tolerant reader reads it
// as 0).
const v2CatalogDDL = `
CREATE TABLE media (
    id INTEGER PRIMARY KEY, hash INTEGER NOT NULL UNIQUE, size INTEGER, rel_path TEXT NOT NULL,
    file_type TEXT NOT NULL, ext TEXT NOT NULL, taken_at INTEGER, taken_at_offset INTEGER,
    taken_at_source TEXT NOT NULL DEFAULT 'none', imported_at INTEGER NOT NULL, import_session_id INTEGER,
    rating INTEGER NOT NULL DEFAULT 0, deleted_at INTEGER);
CREATE TABLE import_sessions (
    id INTEGER PRIMARY KEY, started_at INTEGER NOT NULL, finished_at INTEGER, source TEXT NOT NULL,
    mode TEXT NOT NULL, imported INTEGER NOT NULL DEFAULT 0, existing INTEGER NOT NULL DEFAULT 0,
    duplicates INTEGER NOT NULL DEFAULT 0, skipped_small INTEGER NOT NULL DEFAULT 0,
    errors INTEGER NOT NULL DEFAULT 0);
INSERT INTO import_sessions (id, started_at, finished_at, source, mode, imported, errors) VALUES
    (1, 1000, 1100, '/old/a', 'copy', 2, 1), (2, 2000, NULL, '/old/lib', 'record', 0, 0);
INSERT INTO media (hash, rel_path, file_type, ext, taken_at, imported_at, import_session_id) VALUES
    (11, 'a.jpg', 'image', 'jpg', 50, 1, 1), (12, 'b.jpg', 'image', 'jpg', 40, 1, 1), (13, 'c.jpg', 'image', 'jpg', 1, 1, 2);
PRAGMA user_version = 2;
`

func TestSessionsFileV2(t *testing.T) {
	ctx := context.Background()
	p := t.TempDir() + "/media.db"
	raw, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(v2CatalogDDL); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	got, err := SessionsFile(ctx, p)
	if err != nil || len(got) != 2 {
		t.Fatalf("sessions of a v2 catalog %+v %v", got, err)
	}
	if a := got[0]; a.ID != 1 || a.Source != "/old/a" || a.Imported != 2 || a.Errors != 1 || a.NonMedia != 0 ||
		fmt.Sprint(a.Hashes) != "[11 12]" || a.Newest != 11 || a.FinishedAt.IsZero() {
		t.Fatalf("v2 session %+v", a)
	}
	if b := got[1]; b.Mode != "record" || !b.FinishedAt.IsZero() || fmt.Sprint(b.Hashes) != "[13]" {
		t.Fatalf("v2 session %+v", b)
	}
	// Version 1 (another format) is still refused.
	raw, _ = sql.Open("sqlite", p)
	raw.Exec("PRAGMA user_version = 1")
	raw.Close()
	if _, err := SessionsFile(ctx, p); !errors.Is(err, ErrNeedsRebuild) {
		t.Fatalf("SessionsFile on version 1: %v", err)
	}
}
