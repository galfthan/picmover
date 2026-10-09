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
	if _, err := d.InsertMedia(ctx, nil, &Media{Hash: 1, RelPath: "image/2020/01/a.jpg", FileType: "image", Ext: "jpg"}); err != nil {
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
	matches, _ := filepath.Glob(p + ".v1-*")
	if len(matches) != 0 {
		t.Fatalf("unexpected backup on fresh db: %v", matches)
	}
}

func TestOpenNewerVersionFails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "media.db")
	d := openTest(t, p)
	if _, err := d.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	d.Close()
	_, err := Open(context.Background(), p, nil)
	if err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("expected newer-version error, got %v", err)
	}
}

func TestOpenPathWithSpaces(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my photo library", "sub dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "media.db")
	d := openTest(t, p)
	if _, err := d.InsertMedia(context.Background(), nil, &Media{Hash: 5, RelPath: "x.jpg", FileType: "image", Ext: "jpg"}); err != nil {
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
	got.TakenAt, m.TakenAt = time.Time{}, time.Time{}
	if got != m {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, m)
	}

	// Wall-clock only: zone discarded, wall-clock preserved, UTC placeholder.
	wall := Media{Hash: 2, RelPath: "image/2021/01/b.jpg", FileType: "image", Ext: "jpg",
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
	zero := Media{Hash: 3, RelPath: "video/x.mp4", FileType: "video", Ext: "mp4", OffsetKnown: true,
		ImportedAt: time.Unix(1700000000, 0).UTC()}
	insert(t, d, &zero)
	got, err = d.GetMedia(ctx, zero.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := zero
	want.TakenAtSource = "none"
	want.OffsetKnown = false
	if got != want {
		t.Errorf("zero round trip:\n got %+v\nwant %+v", got, want)
	}

	// Conflict.
	dup := Media{Hash: full.Hash, RelPath: "other.jpg", FileType: "image", Ext: "jpg"}
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
	m := Media{Hash: 1, RelPath: "a.jpg", FileType: "image", Ext: "jpg", CameraModel: "old", Lat: 1, Lon: 2, HasGPS: true}
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
}

func TestRelPathTakenCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	insert(t, d, &Media{Hash: 1, RelPath: "image/2020/01/IMG_001.JPG", FileType: "image", Ext: "jpg"})
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

const v1DDL = `CREATE TABLE media (
    id INTEGER PRIMARY KEY AUTOINCREMENT, hash INTEGER UNIQUE, original_path TEXT,
    new_path TEXT, date_taken DATETIME, file_type TEXT, location TEXT,
    camera_model TEXT, camera_make TEXT, camera_type TEXT, resolution TEXT)`

func TestLegacyMigration(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "Photo Lib")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "media.db")

	raw, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(v1DDL); err != nil {
		t.Fatal(err)
	}
	// Path under the actual root, with different case (case-insensitive strip).
	underRoot := strings.ToUpper(filepath.Join(root, "video", "2021", "05", "c.mp4")[:1]) +
		filepath.Join(root, "video", "2021", "05", "c.mp4")[1:]
	type v1 struct {
		hash                     int64
		orig, newPath            string
		date                     any
		ftype                    string
		loc                      any
		model, make, ctype, reso string
	}
	rows := []v1{
		{-42, `C:\src\a.jpg`, `D:\images\image\2020\01\a.jpg`, "2020-01-02 03:04:05+00:00", "image", "60.1699,24.9384", "iPhone", "Apple", "phone", "4032x3024"},
		{2, "/home/u/b.jpg", "/mnt/d/images/image/2019/07/b.JPG", "2019-07-08 09:10:11.5+00:00", "image", "-33.5,151.25", "", "", "unknown", "unknown"},
		{3, "/src/c.mp4", underRoot, "2021-05-06 18:30:00+03:00", "video", "+60.1699+024.9384+010.000/", "Pixel", "Google", "phone", "1920x1080"},
		{4, "/src/d.cr2", `D:\images\image_raw\2018\02\d.cr2`, "0001-01-01 00:00:00+00:00", "image_raw", nil, "EOS", "Canon", "camera", "6000x4000"},
		{5, "/src/e.jpg", "/elsewhere/e.jpg", time.Date(2017, 3, 4, 5, 6, 7, 0, time.UTC), "image", "", "", "", "", "garbage"},
		{6, "/src/f.mp4", `D:\images\video\2016\01\f.mp4`, "not a date", "video", "+48.85-002.29/", "", "", "", "640x480"},
	}
	for _, r := range rows {
		if _, err := raw.Exec(`INSERT INTO media (hash, original_path, new_path, date_taken, file_type, location,
			camera_model, camera_make, camera_type, resolution) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.hash, r.orig, r.newPath, r.date, r.ftype, r.loc, r.model, r.make, r.ctype, r.reso); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()

	d := openTest(t, p)
	if v := userVersion(t, d); v != SchemaVersion {
		t.Fatalf("user_version = %d", v)
	}
	var n int
	if err := d.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name = 'media_v1'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("media_v1 still present: %d %v", n, err)
	}
	backups, _ := filepath.Glob(filepath.Join(root, "media.db.v1-*.bak"))
	if len(backups) != 1 {
		t.Fatalf("backups = %v", backups)
	}
	bk, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := bk.QueryRow("SELECT COUNT(*) FROM media WHERE new_path IS NOT NULL").Scan(&n); err != nil || n != len(rows) {
		t.Errorf("backup rows = %d %v", n, err)
	}
	bk.Close()

	all, err := d.List(ctx, ListOptions{OrderBy: "id"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(rows) {
		t.Fatalf("migrated %d rows, want %d", len(all), len(rows))
	}
	byHash := map[int64]Media{}
	for _, m := range all {
		byHash[int64(m.Hash)] = m
	}
	type want struct {
		rel, ext, taken, source string
		lat, lon                float64
		gps                     bool
		alt                     float64
		hasAlt                  bool
		w, h                    int
	}
	wants := map[int64]want{
		-42: {"image/2020/01/a.jpg", "jpg", "2020-01-02 03:04:05", "legacy", 60.1699, 24.9384, true, 0, false, 4032, 3024},
		2:   {"image/2019/07/b.JPG", "jpg", "2019-07-08 09:10:11", "legacy", -33.5, 151.25, true, 0, false, 0, 0},
		3:   {"video/2021/05/c.mp4", "mp4", "2021-05-06 18:30:00", "legacy", 60.1699, 24.9384, true, 10, true, 1920, 1080},
		4:   {"image_raw/2018/02/d.cr2", "cr2", "", "none", 0, 0, false, 0, false, 6000, 4000},
		5:   {"/elsewhere/e.jpg", "jpg", "2017-03-04 05:06:07", "legacy", 0, 0, false, 0, false, 0, 0},
		6:   {"video/2016/01/f.mp4", "mp4", "", "none", 48.85, -2.29, true, 0, false, 640, 480},
	}
	for h, w := range wants {
		m, ok := byHash[h]
		if !ok {
			t.Errorf("hash %d missing", h)
			continue
		}
		taken := ""
		if !m.TakenAt.IsZero() {
			taken = m.TakenAt.Format("2006-01-02 15:04:05")
		}
		if m.RelPath != w.rel || m.Ext != w.ext || taken != w.taken || m.TakenAtSource != w.source ||
			m.OffsetKnown != (h == 3) || m.HasGPS != w.gps || m.Lat != w.lat || m.Lon != w.lon ||
			m.HasAltitude != w.hasAlt || m.Altitude != w.alt || m.Width != w.w || m.Height != w.h {
			t.Errorf("hash %d: got %+v\nwant %+v", h, m, w)
		}
		if _, off := m.TakenAt.Zone(); h == 3 && off != 3*3600 {
			t.Errorf("hash 3: offset = %d, want %d", off, 3*3600)
		}
		if m.ImportedAt.IsZero() || m.Size != 0 {
			t.Errorf("hash %d: imported_at=%v size=%d", h, m.ImportedAt, m.Size)
		}
	}
	a := byHash[-42]
	if a.OriginalPath != `C:\src\a.jpg` || a.FileType != "image" || a.CameraMake != "Apple" ||
		a.CameraModel != "iPhone" || a.CameraType != "phone" || uint64(a.Hash) != uint64(0xFFFFFFFFFFFFFFD6) {
		t.Errorf("row -42 fields: %+v", a)
	}
	if rel, found, _ := d.LookupHash(ctx, uint64(0xFFFFFFFFFFFFFFD6)); !found || rel != "image/2020/01/a.jpg" {
		t.Errorf("LookupHash after migration = %q %v", rel, found)
	}

	// Inserting after migration works and reopening is a no-op.
	insert(t, d, &Media{Hash: 100, RelPath: "image/new.jpg", FileType: "image", Ext: "jpg"})
	d.Close()
	d = openTest(t, p)
	if err := d.QueryRow("SELECT COUNT(*) FROM media").Scan(&n); err != nil || n != len(rows)+1 {
		t.Errorf("rows after reopen = %d %v", n, err)
	}
	backups, _ = filepath.Glob(filepath.Join(root, "media.db.v1-*.bak"))
	if len(backups) != 1 {
		t.Errorf("reopen created another backup: %v", backups)
	}
}

func TestLegacyRelPath(t *testing.T) {
	root := "/data/Lib"
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"/data/lib/image/2020/01/a.jpg", "image/2020/01/a.jpg", true},
		{"/data/Lib/odd/a.jpg", "odd/a.jpg", true},
		{"/data/Library/image/2020/01/a.jpg", "image/2020/01/a.jpg", true},
		{`E:\x\video\2020\01\v.mp4`, "video/2020/01/v.mp4", true},
		{`E:\x\other\2020\01\v.mp4`, "E:/x/other/2020/01/v.mp4", false},
		{"a.jpg", "a.jpg", false},
	}
	for _, c := range cases {
		got, ok := legacyRelPath(c.in, root)
		if got != c.want || ok != c.ok {
			t.Errorf("legacyRelPath(%q) = %q %v, want %q %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// writeV1 creates a v1 database at p with n image rows.
func writeV1(t *testing.T, p string, n int) {
	t.Helper()
	raw, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(v1DDL); err != nil {
		t.Fatal(err)
	}
	tx, err := raw.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := tx.Exec(`INSERT INTO media (hash,new_path,original_path,date_taken,file_type) VALUES (?,?,?,?,?)`,
			i+1, fmt.Sprintf("/x/image/2020/01/a%d.jpg", i), "/src/a.jpg", "2020-01-02 03:04:05+03:00", "image"); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
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

// Two openers of the same v1 database: both succeed, the migration runs
// once and exactly one backup remains.
func TestConcurrentLegacyMigration(t *testing.T) {
	const n = 500
	p := filepath.Join(t.TempDir(), "media.db")
	writeV1(t, p, n)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d, err := Open(context.Background(), p, nil)
			errs[i] = err
			if err == nil {
				d.Close()
			}
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Errorf("concurrent Open failed: %v", err)
		}
	}
	d := openTest(t, p)
	if v := userVersion(t, d); v != SchemaVersion {
		t.Errorf("user_version = %d", v)
	}
	var c int
	if err := d.QueryRow("SELECT COUNT(*) FROM media").Scan(&c); err != nil || c != n {
		t.Errorf("rows = %d %v, want %d", c, err, n)
	}
	backups, _ := filepath.Glob(p + ".v1-*.bak")
	if len(backups) != 1 {
		t.Errorf("backups = %v, want exactly one", backups)
	}
	if tmps, _ := filepath.Glob(p + ".v1-*.tmp"); len(tmps) != 0 {
		t.Errorf("temp files left: %v", tmps)
	}
}

// A failed backup leaves no file named like a backup (nor a temp file).
func TestFailedBackupLeavesNoFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "media.db")
	writeV1(t, p, 10)
	sqldb, err := sql.Open("sqlite", p+dsnParams)
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	d := &DB{DB: sqldb, path: p, logger: slog.New(slog.DiscardHandler)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.migrateLegacy(ctx); err == nil {
		t.Fatal("migrateLegacy with a cancelled context succeeded")
	}
	m, _ := filepath.Glob(p + ".v1-*")
	if len(m) != 0 {
		t.Errorf("failed backup left files: %v", m)
	}
}

func TestLegacyMigrationOffsets(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	p := filepath.Join(root, "media.db")
	raw, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(v1DDL); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		hash         int64
		date, ftype  string
		loc          string
		wantKnown    bool
		wantOffset   int
		wantSource   string
		wantWallText string
	}{
		{1, "2021-05-06 18:30:00+00:00", "video", "0.000000,0.000000", true, 0, "legacy_utc", "2021-05-06 18:30:00"},
		{2, "2021-05-06 18:30:00+03:00", "video", "", true, 10800, "legacy", "2021-05-06 18:30:00"},
		{3, "2020-01-02 03:04:05+00:00", "image", "", false, 0, "legacy", "2020-01-02 03:04:05"},
		{4, "2020-01-02 03:04:05+03:00", "image", "", true, 10800, "legacy", "2020-01-02 03:04:05"},
		{5, "2020-01-02 03:04:05+00:00", "image_raw", "", false, 0, "legacy", "2020-01-02 03:04:05"},
	}
	for _, r := range rows {
		if _, err := raw.Exec(`INSERT INTO media (hash, new_path, date_taken, file_type, location) VALUES (?, ?, ?, ?, ?)`,
			r.hash, fmt.Sprintf("/x/%s/2020/01/f%d", r.ftype, r.hash), r.date, r.ftype, r.loc); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()

	d := openTest(t, p)
	for _, r := range rows {
		var id int64
		if err := d.QueryRow("SELECT id FROM media WHERE hash = ?", r.hash).Scan(&id); err != nil {
			t.Fatalf("hash %d: %v", r.hash, err)
		}
		m, err := d.GetMedia(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		_, off := m.TakenAt.Zone()
		if m.OffsetKnown != r.wantKnown || (r.wantKnown && off != r.wantOffset) ||
			m.TakenAtSource != r.wantSource || m.TakenAt.Format("2006-01-02 15:04:05") != r.wantWallText {
			t.Errorf("hash %d: known=%v offset=%d source=%q wall=%s, want %v %d %q %s", r.hash,
				m.OffsetKnown, off, m.TakenAtSource, m.TakenAt.Format("2006-01-02 15:04:05"),
				r.wantKnown, r.wantOffset, r.wantSource, r.wantWallText)
		}
		if r.hash == 1 && m.HasGPS {
			t.Errorf("hash 1: 0,0 location gave HasGPS (%v,%v)", m.Lat, m.Lon)
		}
	}
}

func TestLegacyTimeZoneless(t *testing.T) {
	for _, v := range []any{"2020-01-02 03:04:05", []byte("2020-01-02T03:04:05")} {
		if _, off, source := legacyTakenAt(v, "video"); off != nil || source != "legacy" {
			t.Errorf("legacyTakenAt(%q) offset=%v source=%q, want unknown/legacy", v, off, source)
		}
	}
	if w, off, source := legacyTakenAt("0001-01-01 00:00:00+00:00", "video"); w != nil || off != nil || source != "none" {
		t.Errorf("zero time: %v %v %q", w, off, source)
	}
}

func TestLegacyLocationNullIsland(t *testing.T) {
	for _, s := range []string{"0.000000,0.000000", "+00.0000+000.0000/"} {
		if _, _, _, _, ok := legacyLocation(s); ok {
			t.Errorf("legacyLocation(%q) ok, want no location", s)
		}
	}
	if la, lo, _, _, ok := legacyLocation("0.000000,24.5"); !ok || la != 0 || lo != 24.5 {
		t.Errorf("legacyLocation on equator rejected: %v %v %v", la, lo, ok)
	}
}

// OpenExisting never creates or migrates: a missing file and a v1 database
// are errors, and the v1 file is left untouched.
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
	writeV1(t, p, 2)
	before, _ := os.ReadFile(p)
	if d, err := OpenExisting(ctx, p, nil); !errors.Is(err, ErrNeedsMigration) {
		if d != nil {
			d.Close()
		}
		t.Fatalf("OpenExisting(v1) = %v, want ErrNeedsMigration", err)
	}
	if after, _ := os.ReadFile(p); !bytes.Equal(before, after) {
		t.Error("OpenExisting modified the v1 database")
	}
	if m, _ := filepath.Glob(p + ".v1-*"); len(m) != 0 {
		t.Errorf("OpenExisting wrote a backup: %v", m)
	}
	d, err := Open(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	d, err = OpenExisting(ctx, p, nil)
	if err != nil {
		t.Fatalf("OpenExisting after migration: %v", err)
	}
	d.Close()
}
