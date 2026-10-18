package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTest(t *testing.T, path string) *DB {
	t.Helper()
	d, err := Open(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func newDB(t *testing.T) *DB {
	t.Helper()
	return openTest(t, filepath.Join(t.TempDir(), "media.db"))
}

func userVersion(t *testing.T, d *DB) int {
	t.Helper()
	var v int
	if err := d.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestOpenFreshAndReopen(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "media.db")
	d, err := Open(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v := userVersion(t, d); v != SchemaVersion {
		t.Fatalf("user_version = %d, want %d", v, SchemaVersion)
	}
	var mode string
	if err := d.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
	if _, err := d.InsertMedia(ctx, nil, &Media{Hash: 1, Size: 10, RelPath: "image/2020/01/a.jpg", FileType: "image", Ext: "jpg"}); err != nil {
		t.Fatal(err)
	}
	d.Close()

	d = openTest(t, p)
	if v := userVersion(t, d); v != SchemaVersion {
		t.Fatalf("reopen user_version = %d", v)
	}
	if _, found, err := d.LookupHash(ctx, 1); err != nil || !found {
		t.Fatalf("row lost on reopen: %v %v", found, err)
	}
}

func TestOpenNewerVersionFails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "media.db")
	d := openTest(t, p)
	if _, err := d.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	d.Close()
	for _, open := range []func(context.Context, string, *slog.Logger) (*DB, error){Open, OpenExisting} {
		_, err := open(context.Background(), p, nil)
		if err == nil || !strings.Contains(err.Error(), "newer") || !errors.Is(err, ErrNeedsRebuild) {
			t.Fatalf("expected newer-version ErrNeedsRebuild, got %v", err)
		}
	}
}

// writeOld creates a database at p with the given user_version and a media
// table, using raw SQL (as an older picmover would have left it).
func writeOld(t *testing.T, p string, version int) {
	t.Helper()
	raw, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	stmts := []string{
		`CREATE TABLE media (id INTEGER PRIMARY KEY AUTOINCREMENT, hash INTEGER UNIQUE,
			original_path TEXT, new_path TEXT, date_taken DATETIME, file_type TEXT)`,
		`INSERT INTO media (hash, new_path, file_type) VALUES (1, '/x/image/2020/01/a.jpg', 'image')`,
	}
	if version == 2 {
		stmts = []string{
			`CREATE TABLE media (id INTEGER PRIMARY KEY, hash INTEGER NOT NULL UNIQUE, size INTEGER,
				rel_path TEXT NOT NULL, file_type TEXT NOT NULL, ext TEXT NOT NULL,
				taken_at_source TEXT NOT NULL DEFAULT 'none', imported_at INTEGER NOT NULL)`,
			`INSERT INTO media (hash, rel_path, file_type, ext, taken_at_source, imported_at)
				VALUES (1, 'a.jpg', 'image', 'jpg', 'legacy', 1)`,
		}
	}
	for _, q := range stmts {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		t.Fatal(err)
	}
}

// Older catalogs (v1 with user_version 0 or 1, v2) are refused with
// ErrNeedsRebuild by both Open and OpenExisting and left byte-for-byte intact.
func TestOpenOldVersionNeedsRebuild(t *testing.T) {
	ctx := context.Background()
	for _, version := range []int{0, 1, 2} {
		p := filepath.Join(t.TempDir(), "media.db")
		writeOld(t, p, version)
		before, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for name, open := range map[string]func(context.Context, string, *slog.Logger) (*DB, error){
			"Open": Open, "OpenExisting": OpenExisting} {
			d, err := open(ctx, p, nil)
			if version == 0 {
				// user_version 0 with a media table is a v1 catalog.
				if err == nil || !errors.Is(err, ErrNeedsRebuild) {
					if d != nil {
						d.Close()
					}
					t.Fatalf("%s(v1, user_version 0) = %v, want ErrNeedsRebuild", name, err)
				}
				continue
			}
			if !errors.Is(err, ErrNeedsRebuild) {
				if d != nil {
					d.Close()
				}
				t.Fatalf("%s(user_version %d) = %v, want ErrNeedsRebuild", name, version, err)
			}
			if !strings.Contains(err.Error(), "picmover rebuild") {
				t.Errorf("%s error does not mention rebuild: %v", name, err)
			}
		}
		after, _ := os.ReadFile(p)
		if !bytes.Equal(before, after) {
			t.Errorf("user_version %d: database modified", version)
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if _, err := os.Stat(p + suffix); err == nil {
				t.Errorf("user_version %d: %s created", version, suffix)
			}
		}
	}
}

// A normal filesystem supports WAL: Open logs no warning.
func TestOpenNoWALWarning(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	d, err := Open(context.Background(), filepath.Join(t.TempDir(), "media.db"), logger)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	if strings.Contains(buf.String(), "WAL") {
		t.Errorf("unexpected WAL warning: %s", buf.String())
	}
}

func TestOpenPathWithSpaces(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my photo library", "sub dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "media.db")
	d := openTest(t, p)
	if _, err := d.InsertMedia(context.Background(), nil, &Media{Hash: 5, Size: 1, RelPath: "x.jpg", FileType: "image", Ext: "jpg"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("db not created at exact path: %v", err)
	}
}

func insert(t *testing.T, d *DB, m *Media) bool {
	t.Helper()
	ctx := context.Background()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := d.InsertMedia(ctx, tx, m)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestInsertLookupGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	zone := time.FixedZone("", 3*3600)
	full := Media{
		Hash:            0xFEDCBA9876543210, // high bit set: exercises the int64 bit cast
		Size:            12345,
		RelPath:         "image/2020/03/a.jpg",
		FileType:        "image",
		Ext:             "jpg",
		TakenAt:         time.Date(2020, 3, 4, 5, 6, 7, 0, zone),
		OffsetKnown:     true,
		TakenAtSource:   "exif",
		Width:           4000,
		Height:          3000,
		Orientation:     6,
		DurationMS:      1500,
		Lat:             60.1699,
		Lon:             -24.9384,
		HasGPS:          true,
		Altitude:        12.5,
		HasAltitude:     true,
		CameraMake:      "Canon",
		CameraModel:     "EOS 5D",
		CameraType:      "camera",
		Lens:            "EF 50mm",
		OriginalPath:    "/src/a.zip!/dir/a.jpg",
		OriginalMTime:   time.Unix(1600000000, 0).UTC(),
		ImportedAt:      time.Unix(1700000000, 0).UTC(),
		ImportSessionID: 7,
		Rating:          4,
		DeletedAt:       time.Unix(1700000500, 0).UTC(),
		PHash:           0x8000000000000001,
		ThumbState:      "ok",
		ThumbError:      "",
		ThumbAt:         time.Unix(1700000600, 0).UTC(),
	}
	m := full
	if !insert(t, d, &m) {
		t.Fatal("insert returned false")
	}
	if m.ID == 0 {
		t.Fatal("ID not set")
	}
	got, err := d.GetMedia(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.TakenAt.Equal(full.TakenAt) || got.TakenAt.Format(time.RFC3339) != "2020-03-04T05:06:07+03:00" {
		t.Errorf("TakenAt = %v", got.TakenAt)
	}
	// No home zone: the home time is the recorded wall clock.
	if got.TakenAtHome.Format(time.DateTime) != "2020-03-04 05:06:07" || got.OffsetSource != OffsetSourceExif {
		t.Errorf("home %v, offset source %q", got.TakenAtHome, got.OffsetSource)
	}
	got.TakenAt, m.TakenAt = time.Time{}, time.Time{}
	got.TakenAtHome, got.OffsetSource = time.Time{}, ""
	if got != m {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, m)
	}

	// Wall-clock only: zone discarded, wall-clock preserved, UTC placeholder.
	wall := Media{Hash: 2, Size: 1, RelPath: "image/2021/01/b.jpg", FileType: "image", Ext: "jpg",
		TakenAt: time.Date(2021, 1, 2, 10, 0, 0, 999, time.FixedZone("", -5*3600))}
	insert(t, d, &wall)
	got, err = d.GetMedia(ctx, wall.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.OffsetKnown || got.TakenAt.Location() != time.UTC ||
		got.TakenAt.Format("2006-01-02 15:04:05") != "2021-01-02 10:00:00" {
		t.Errorf("wall-clock TakenAt = %v known=%v", got.TakenAt, got.OffsetKnown)
	}
	if got.TakenAtSource != "none" {
		t.Errorf("empty source stored as %q", got.TakenAtSource)
	}

	// Zero times, no GPS.
	zero := Media{Hash: 3, Size: 1, RelPath: "video/x.mp4", FileType: "video", Ext: "mp4", OffsetKnown: true,
		ImportedAt: time.Unix(1700000000, 0).UTC()}
	insert(t, d, &zero)
	got, err = d.GetMedia(ctx, zero.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := zero
	want.TakenAtSource = "none"
	want.OffsetKnown = false
	want.ThumbState = "pending"
	if got != want {
		t.Errorf("zero round trip:\n got %+v\nwant %+v", got, want)
	}

	// Conflict.
	dup := Media{Hash: full.Hash, Size: 1, RelPath: "other.jpg", FileType: "image", Ext: "jpg"}
	if insert(t, d, &dup) {
		t.Error("duplicate hash inserted")
	}
	if dup.ID != 0 {
		t.Errorf("dup ID set to %d", dup.ID)
	}

	rel, found, err := d.LookupHash(ctx, full.Hash)
	if err != nil || !found || rel != full.RelPath {
		t.Errorf("LookupHash = %q %v %v", rel, found, err)
	}
	if _, found, err := d.LookupHash(ctx, 999); err != nil || found {
		t.Errorf("LookupHash(missing) = %v %v", found, err)
	}
	if _, err := d.GetMedia(ctx, 12345); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetMedia(missing) err = %v", err)
	}
}

func TestUpdateMetadataAndSize(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	m := Media{Hash: 1, Size: 1, RelPath: "a.jpg", FileType: "image", Ext: "jpg", CameraModel: "old", Lat: 1, Lon: 2, HasGPS: true}
	insert(t, d, &m)
	upd := Media{TakenAt: time.Date(2019, 5, 6, 7, 8, 9, 0, time.UTC), TakenAtSource: "exif",
		Width: 10, Height: 20, CameraModel: "new", Lens: "L"}
	if err := d.UpdateMetadata(ctx, nil, m.ID, &upd); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateSize(ctx, nil, m.ID, 777); err != nil {
		t.Fatal(err)
	}
	got, _ := d.GetMedia(ctx, m.ID)
	if got.CameraModel != "new" || got.Lens != "L" || got.HasGPS || got.Width != 10 || got.Size != 777 ||
		!got.TakenAt.Equal(upd.TakenAt) || got.RelPath != "a.jpg" {
		t.Errorf("after update: %+v", got)
	}
	if err := d.UpdateSize(ctx, nil, 999, 1); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("UpdateSize(missing) = %v", err)
	}
	if err := d.UpdateSize(ctx, nil, m.ID, 0); err == nil {
		t.Error("UpdateSize(0) succeeded")
	}
}

func TestRelPathTakenCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	insert(t, d, &Media{Hash: 1, Size: 1, RelPath: "image/2020/01/IMG_001.JPG", FileType: "image", Ext: "jpg"})
	for _, p := range []string{"image/2020/01/IMG_001.JPG", "image/2020/01/img_001.jpg", "IMAGE/2020/01/Img_001.Jpg"} {
		if ok, err := d.RelPathTaken(ctx, p); err != nil || !ok {
			t.Errorf("RelPathTaken(%q) = %v %v", p, ok, err)
		}
	}
	if ok, err := d.RelPathTaken(ctx, "image/2020/01/img_002.jpg"); err != nil || ok {
		t.Errorf("RelPathTaken(other) = %v %v", ok, err)
	}
}

func seed(t *testing.T, d *DB) {
	t.Helper()
	day := func(n int) time.Time { return time.Date(2020, 1, n, 12, 0, 0, 0, time.UTC) }
	rows := []Media{
		{Hash: 1, RelPath: "image/c.jpg", FileType: "image", Ext: "jpg", TakenAt: day(3), Size: 100, CameraModel: "A", Lat: 1, Lon: 1, HasGPS: true},
		{Hash: 2, RelPath: "image/a.jpg", FileType: "image", Ext: "jpg", TakenAt: day(1), Size: 200, CameraModel: "A"},
		{Hash: 3, RelPath: "video/b.mp4", FileType: "video", Ext: "mp4", TakenAt: day(2), Size: 300, CameraModel: "B"},
		{Hash: 4, RelPath: "image_raw/d.cr2", FileType: "image_raw", Ext: "cr2", Size: 400, CameraModel: ""},
		{Hash: 5, RelPath: "image/e.jpg", FileType: "image", Ext: "jpg", TakenAt: day(9), Size: 1000, CameraModel: "C",
			Lat: 2, Lon: 2, HasGPS: true, DeletedAt: time.Unix(1700000000, 0)},
	}
	for i := range rows {
		insert(t, d, &rows[i])
	}
}

func hashes(ms []Media) []uint64 {
	var out []uint64
	for _, m := range ms {
		out = append(out, m.Hash)
	}
	return out
}

func TestList(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	seed(t, d)
	cases := []struct {
		opts ListOptions
		want string
	}{
		{ListOptions{}, "[1 3 2 4]"}, // taken_at DESC, NULL last
		{ListOptions{OrderBy: "taken_at"}, "[4 2 3 1]"},
		{ListOptions{OrderBy: "taken_at DESC"}, "[1 3 2 4]"},
		{ListOptions{OrderBy: "id"}, "[1 2 3 4]"},
		{ListOptions{OrderBy: "id DESC"}, "[4 3 2 1]"},
		{ListOptions{OrderBy: "rel_path"}, "[2 1 4 3]"},
		{ListOptions{FileType: "image", OrderBy: "id"}, "[1 2]"},
		{ListOptions{FileType: "image", OrderBy: "id", IncludeDeleted: true}, "[1 2 5]"},
		{ListOptions{IncludeDeleted: true, OrderBy: "id"}, "[1 2 3 4 5]"},
		{ListOptions{OrderBy: "id", Limit: 2}, "[1 2]"},
		{ListOptions{OrderBy: "id", Limit: 2, Offset: 1}, "[2 3]"},
		{ListOptions{OrderBy: "id", Offset: 3}, "[4]"},
		{ListOptions{FileType: "nothing"}, "[]"},
	}
	for _, c := range cases {
		got, err := d.List(ctx, c.opts)
		if err != nil {
			t.Fatalf("%+v: %v", c.opts, err)
		}
		if s := fmt.Sprint(hashes(got)); s != c.want {
			t.Errorf("List(%+v) = %s, want %s", c.opts, s, c.want)
		}
	}
	for _, bad := range []string{"hash", "taken_at; DROP TABLE media", "rel_path DESC", "ID"} {
		if _, err := d.List(ctx, ListOptions{OrderBy: bad}); err == nil {
			t.Errorf("OrderBy %q accepted", bad)
		}
	}
}

func TestSummary(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	s, err := d.Summary(ctx)
	if err != nil {
		t.Fatalf("empty summary: %v", err)
	}
	if s != (Summary{}) {
		t.Errorf("empty summary = %+v", s)
	}
	seed(t, d)
	s, err = d.Summary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := Summary{Total: 4, Images: 2, Raw: 1, Videos: 1, WithGPS: 1, Deleted: 1, TotalBytes: 1000,
		Earliest: time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC), Latest: time.Date(2020, 1, 3, 12, 0, 0, 0, time.UTC),
		UniqueModels: 2}
	if s != want {
		t.Errorf("summary =\n %+v\nwant\n %+v", s, want)
	}
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	id, err := d.BeginSession(ctx, "/src/dir", "move")
	if err != nil || id == 0 {
		t.Fatalf("BeginSession = %d %v", id, err)
	}
	id2, err := d.BeginSession(ctx, "/src/other", "copy")
	if err != nil || id2 == id {
		t.Fatalf("second BeginSession = %d %v", id2, err)
	}
	if err := d.FinishSession(ctx, id, SessionStats{Imported: 1, Existing: 2, Duplicates: 3, SkippedSmall: 4, NonMedia: 5, Errors: 6}); err != nil {
		t.Fatal(err)
	}
	var (
		src, mode                    string
		started                      int64
		finished                     sql.NullInt64
		imp, ex, dup, small, non, er int64
	)
	err = d.QueryRow(`SELECT source, mode, started_at, finished_at, imported, existing, duplicates,
		skipped_small, non_media, errors FROM import_sessions WHERE id = ?`, id).
		Scan(&src, &mode, &started, &finished, &imp, &ex, &dup, &small, &non, &er)
	if err != nil {
		t.Fatal(err)
	}
	if src != "/src/dir" || mode != "move" || started == 0 || !finished.Valid ||
		imp != 1 || ex != 2 || dup != 3 || small != 4 || non != 5 || er != 6 {
		t.Errorf("session row: %s %s %d %v %d %d %d %d %d %d", src, mode, started, finished, imp, ex, dup, small, non, er)
	}
	if err := d.QueryRow("SELECT finished_at FROM import_sessions WHERE id = ?", id2).Scan(&finished); err != nil || finished.Valid {
		t.Errorf("unfinished session finished_at = %v %v", finished, err)
	}
	if err := d.FinishSession(ctx, 999, SessionStats{}); err == nil {
		t.Error("FinishSession(missing) succeeded")
	}
}

// A '?' in the path would make the driver open a different file.
func TestOpenRejectsQuestionMark(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "what?x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "media.db")
	d, err := Open(context.Background(), p, nil)
	if err == nil {
		d.Close()
		t.Fatalf("Open(%q) succeeded, want error", p)
	}
	if !strings.Contains(err.Error(), "?") {
		t.Errorf("error does not mention '?': %v", err)
	}
	for _, q := range []string{filepath.Join(parent, "what"), p} {
		if _, err := os.Stat(q); err == nil {
			t.Errorf("Open created %q", q)
		}
	}
}

func TestInsertRejectsZeroSize(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	for _, size := range []int64{0, -1} {
		m := Media{Hash: 1, Size: size, RelPath: "a.jpg", FileType: "image", Ext: "jpg"}
		if ok, err := d.InsertMedia(ctx, nil, &m); err == nil || ok {
			t.Errorf("InsertMedia(size %d) = %v %v, want error", size, ok, err)
		}
	}
	if _, err := d.Exec(`INSERT INTO media (hash, rel_path, file_type, ext, taken_at_source, imported_at)
		VALUES (1, 'a.jpg', 'image', 'jpg', 'none', 1)`); err == nil {
		t.Error("NULL size accepted by the schema")
	}
}

func isConstraint(err error) bool {
	return err != nil && strings.Contains(err.Error(), "constraint failed")
}

func TestRelPathUniqueNoCase(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	insert(t, d, &Media{Hash: 1, Size: 1, RelPath: "image/2020/01/IMG_001.JPG", FileType: "image", Ext: "jpg"})
	m := Media{Hash: 2, Size: 1, RelPath: "image/2020/01/img_001.jpg", FileType: "image", Ext: "jpg"}
	ok, err := d.InsertMedia(ctx, nil, &m)
	if ok || !isConstraint(err) || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("case-variant insert = %v %v, want UNIQUE constraint error", ok, err)
	}
	if ok, err := d.RelPathTaken(ctx, "IMAGE/2020/01/img_001.jpg"); err != nil || !ok {
		t.Errorf("RelPathTaken = %v %v", ok, err)
	}
	var n int
	if err := d.QueryRow("SELECT COUNT(*) FROM media").Scan(&n); err != nil || n != 1 {
		t.Errorf("rows = %d %v", n, err)
	}
}

func TestTakenAtSourceCheck(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	for i, src := range []string{"exif", "video", "video_utc", "mtime", "none", ""} {
		m := Media{Hash: uint64(i + 1), Size: 1, RelPath: fmt.Sprintf("%d.jpg", i), FileType: "image", Ext: "jpg", TakenAtSource: src}
		if _, err := d.InsertMedia(ctx, nil, &m); err != nil {
			t.Errorf("source %q: %v", src, err)
		}
	}
	for _, src := range []string{"legacy", "legacy_utc", "EXIF"} {
		m := Media{Hash: 100, Size: 1, RelPath: "bad.jpg", FileType: "image", Ext: "jpg", TakenAtSource: src}
		if _, err := d.InsertMedia(ctx, nil, &m); !isConstraint(err) {
			t.Errorf("source %q: err = %v, want CHECK constraint error", src, err)
		}
	}
	m := Media{Hash: 101, Size: 1, RelPath: "bad.jpg", FileType: "image", Ext: "jpg", ThumbState: "bogus"}
	if _, err := d.InsertMedia(ctx, nil, &m); !isConstraint(err) {
		t.Errorf("thumb_state bogus: err = %v, want CHECK constraint error", err)
	}
}

func TestThumbs(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	s1, err := d.BeginSession(ctx, "/a", "copy")
	if err != nil {
		t.Fatal(err)
	}
	s2, err := d.BeginSession(ctx, "/b", "copy")
	if err != nil {
		t.Fatal(err)
	}
	rows := []Media{
		{Hash: 1, Size: 1, RelPath: "1.jpg", FileType: "image", Ext: "jpg", ImportSessionID: s1},
		{Hash: 2, Size: 1, RelPath: "2.jpg", FileType: "image", Ext: "jpg", ImportSessionID: s2, Width: 10, Height: 20},
		{Hash: 3, Size: 1, RelPath: "3.jpg", FileType: "image", Ext: "jpg", ImportSessionID: s1},
		{Hash: 4, Size: 1, RelPath: "4.jpg", FileType: "image", Ext: "jpg", ImportSessionID: s1, DeletedAt: time.Unix(1, 0)},
		{Hash: 5, Size: 1, RelPath: "5.jpg", FileType: "image", Ext: "jpg", ThumbState: ThumbStateOK},
		{Hash: 6, Size: 1, RelPath: "6.jpg", FileType: "image", Ext: "jpg", ImportSessionID: s2},
	}
	for i := range rows {
		insert(t, d, &rows[i])
	}
	check := func(limit int, session int64, want string) {
		t.Helper()
		got, err := d.ListThumbPending(ctx, limit, session)
		if err != nil {
			t.Fatal(err)
		}
		if s := fmt.Sprint(hashes(got)); s != want {
			t.Errorf("ListThumbPending(%d, %d) = %s, want %s", limit, session, s, want)
		}
	}
	check(0, 0, "[1 2 3 6]")
	check(2, 0, "[1 2]")
	check(0, s1, "[1 3]")
	check(1, s2, "[2]")
	if p, ok, u, f, err := d.ThumbStats(ctx); err != nil || p != 4 || ok != 1 || u != 0 || f != 0 {
		t.Errorf("ThumbStats = %d %d %d %d %v", p, ok, u, f, err)
	}

	before := time.Now().Add(-time.Second)
	// Row 1 has no dimensions: filled. High-bit phash round-trips.
	if err := d.SetThumb(ctx, nil, rows[0].ID, ThumbStateOK, "", 0xF000000000000001, 640, 480); err != nil {
		t.Fatal(err)
	}
	// Row 2 has dimensions: kept.
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetThumb(ctx, tx, rows[1].ID, ThumbStateOK, "", 42, 640, 480); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Row 3 fails: phash NULL, w/h 0 leave dimensions unset.
	if err := d.SetThumb(ctx, nil, rows[2].ID, ThumbStateError, "decode: bad", 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	// Row 6 unsupported; only one of w/h given: dimensions unset.
	if err := d.SetThumb(ctx, nil, rows[5].ID, ThumbStateUnsupported, "", 0, 100, 0); err != nil {
		t.Fatal(err)
	}

	type want struct {
		state, msg string
		phash      uint64
		w, h       int
		phashNull  bool
	}
	for i, w := range map[int]want{
		0: {ThumbStateOK, "", 0xF000000000000001, 640, 480, false},
		1: {ThumbStateOK, "", 42, 10, 20, false},
		2: {ThumbStateError, "decode: bad", 0, 0, 0, true},
		5: {ThumbStateUnsupported, "", 0, 0, 0, true},
	} {
		got, err := d.GetMedia(ctx, rows[i].ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ThumbState != w.state || got.ThumbError != w.msg || got.PHash != w.phash ||
			got.Width != w.w || got.Height != w.h || got.ThumbAt.Before(before) {
			t.Errorf("row %d after SetThumb: state=%q err=%q phash=%x %dx%d at=%v",
				i, got.ThumbState, got.ThumbError, got.PHash, got.Width, got.Height, got.ThumbAt)
		}
		var ph, wd sql.NullInt64
		if err := d.QueryRow("SELECT phash, width FROM media WHERE id = ?", rows[i].ID).Scan(&ph, &wd); err != nil {
			t.Fatal(err)
		}
		if ph.Valid == w.phashNull {
			t.Errorf("row %d: phash NULL = %v, want %v", i, !ph.Valid, w.phashNull)
		}
		if wd.Valid != (w.w != 0) {
			t.Errorf("row %d: width NULL = %v", i, !wd.Valid)
		}
	}
	// Setting phash back to 0 stores NULL.
	if err := d.SetThumb(ctx, nil, rows[0].ID, ThumbStatePending, "", 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	var ph sql.NullInt64
	if err := d.QueryRow("SELECT phash FROM media WHERE id = ?", rows[0].ID).Scan(&ph); err != nil || ph.Valid {
		t.Errorf("phash after reset = %v %v, want NULL", ph, err)
	}
	if got, _ := d.GetMedia(ctx, rows[0].ID); got.Width != 640 || got.PHash != 0 {
		t.Errorf("after reset: %dx%d phash=%x", got.Width, got.Height, got.PHash)
	}

	check(0, 0, "[1]")
	if p, ok, u, f, err := d.ThumbStats(ctx); err != nil || p != 1 || ok != 2 || u != 1 || f != 1 {
		t.Errorf("ThumbStats = %d %d %d %d %v", p, ok, u, f, err)
	}
	if err := d.SetThumb(ctx, nil, 999, ThumbStateOK, "", 0, 0, 0); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("SetThumb(missing) = %v", err)
	}
	if err := d.SetThumb(ctx, nil, rows[0].ID, "bogus", "", 0, 0, 0); !isConstraint(err) {
		t.Errorf("SetThumb(bogus state) = %v", err)
	}
	empty := newDB(t)
	if p, ok, u, f, err := empty.ThumbStats(ctx); err != nil || p+ok+u+f != 0 {
		t.Errorf("empty ThumbStats = %d %d %d %d %v", p, ok, u, f, err)
	}
}

func TestListForThumbs(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	s1, err := d.BeginSession(ctx, "/a", "copy")
	if err != nil {
		t.Fatal(err)
	}
	rows := []Media{
		{Hash: 1, Size: 1, RelPath: "1.jpg", FileType: "image", Ext: "jpg", ImportSessionID: s1},
		{Hash: 2, Size: 1, RelPath: "2.jpg", FileType: "image", Ext: "jpg", ThumbState: ThumbStateOK},
		{Hash: 3, Size: 1, RelPath: "3.jpg", FileType: "image", Ext: "jpg", ImportSessionID: s1, ThumbState: ThumbStateError},
		{Hash: 4, Size: 1, RelPath: "4.jpg", FileType: "image", Ext: "jpg", DeletedAt: time.Unix(1, 0)},
		{Hash: 5, Size: 1, RelPath: "5.jpg", FileType: "image", Ext: "jpg", ImportSessionID: s1},
		{Hash: 6, Size: 1, RelPath: "6.jpg", FileType: "image", Ext: "jpg", ThumbState: ThumbStateUnsupported},
	}
	for i := range rows {
		insert(t, d, &rows[i])
	}
	check := func(after int64, limit int, session int64, all bool, want string) {
		t.Helper()
		got, err := d.ListForThumbs(ctx, after, limit, session, all)
		if err != nil {
			t.Fatal(err)
		}
		if s := fmt.Sprint(hashes(got)); s != want {
			t.Errorf("ListForThumbs(%d, %d, %d, %v) = %s, want %s", after, limit, session, all, s, want)
		}
	}
	check(0, 0, 0, false, "[1 3 5]")
	check(0, 0, 0, true, "[1 2 3 5 6]")
	check(0, 2, 0, true, "[1 2]")
	check(rows[1].ID, 2, 0, true, "[3 5]")
	check(rows[4].ID, 0, 0, true, "[6]")
	check(0, 0, s1, true, "[1 3 5]")
	check(rows[0].ID, 0, s1, false, "[3 5]")
	// Paging stays correct while earlier rows change state.
	if err := d.SetThumb(ctx, nil, rows[0].ID, ThumbStateOK, "", 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	check(rows[0].ID, 1, 0, false, "[3]")
	check(rows[2].ID, 1, 0, false, "[5]")
	// A stale row is selected again; ThumbStats counts it as pending.
	if err := d.MarkThumbStale(ctx, nil, rows[1].ID); err != nil {
		t.Fatal(err)
	}
	if m, err := d.GetMedia(ctx, rows[1].ID); err != nil || m.ThumbState != ThumbStateStale {
		t.Fatalf("after MarkThumbStale: %q %v", m.ThumbState, err)
	}
	check(0, 0, 0, false, "[2 3 5]")
	if pending, ok, _, failed, err := d.ThumbStats(ctx); err != nil || pending != 2 || ok != 1 || failed != 1 {
		t.Errorf("ThumbStats = pending %d ok %d failed %d, %v", pending, ok, failed, err)
	}
	if err := d.MarkThumbStale(ctx, nil, 9999); err != sql.ErrNoRows {
		t.Errorf("MarkThumbStale(missing) = %v, want sql.ErrNoRows", err)
	}
}

// Many concurrent openers of a new database: the schema is created once,
// every opener succeeds and every insert lands.
func TestConcurrentOpenAndInsert(t *testing.T) {
	const openers, perOpener = 8, 50
	p := filepath.Join(t.TempDir(), "media.db")
	var wg sync.WaitGroup
	errs := make([]error, openers)
	for i := range openers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := context.Background()
			d, err := Open(ctx, p, nil)
			if err != nil {
				errs[i] = err
				return
			}
			defer d.Close()
			for j := range perOpener {
				tx, err := d.BeginTx(ctx, nil)
				if err != nil {
					errs[i] = err
					return
				}
				m := Media{Hash: uint64(i*perOpener + j + 1), Size: 1, FileType: "image", Ext: "jpg",
					RelPath: fmt.Sprintf("image/%d/%d.jpg", i, j)}
				if _, err := d.InsertMedia(ctx, tx, &m); err != nil {
					tx.Rollback()
					errs[i] = err
					return
				}
				if err := tx.Commit(); err != nil {
					errs[i] = err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Errorf("concurrent opener failed: %v", err)
		}
	}
	d := openTest(t, p)
	if v := userVersion(t, d); v != SchemaVersion {
		t.Errorf("user_version = %d", v)
	}
	var c int
	if err := d.QueryRow("SELECT COUNT(*) FROM media").Scan(&c); err != nil || c != openers*perOpener {
		t.Errorf("rows = %d %v, want %d", c, err, openers*perOpener)
	}
}

func TestOpenExisting(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	p := filepath.Join(dir, "media.db")
	if d, err := OpenExisting(ctx, p, nil); err == nil {
		d.Close()
		t.Fatal("OpenExisting of a missing file succeeded")
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("OpenExisting created the database")
	}
	// An empty file is not a catalog and is not initialised.
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if d, err := OpenExisting(ctx, p, nil); err == nil {
		d.Close()
		t.Fatal("OpenExisting of an empty file succeeded")
	}
	if fi, _ := os.Stat(p); fi.Size() != 0 {
		t.Fatal("OpenExisting wrote to an empty file")
	}
	d, err := Open(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	d, err = OpenExisting(ctx, p, nil)
	if err != nil {
		t.Fatalf("OpenExisting after create: %v", err)
	}
	if v := userVersion(t, d); v != SchemaVersion {
		t.Errorf("user_version = %d", v)
	}
	d.Close()
}
