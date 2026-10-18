package catalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The upgrade keeps a copy of the version 3 catalog, under the name a
// rebuild gives its backups.
func TestMigrateV3Backup(t *testing.T) {
	t.Setenv("TZ", "Europe/Helsinki")
	ctx := context.Background()
	dir := t.TempDir()
	p := filepath.Join(dir, "media.db")
	writeV3(t, p, [][]any{{time.Date(2024, 7, 14, 14, 30, 0, 0, time.UTC).Unix(), 7200, "exif", "image", "", "", "", 1}})
	d := openTest(t, p)
	baks, _ := filepath.Glob(p + ".bak-*")
	if len(baks) != 1 {
		t.Fatalf("backups %v", baks)
	}
	if left, _ := filepath.Glob(p + ".bak-*.tmp"); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
	pk, err := PeekFile(ctx, baks[0])
	if err != nil || pk.Summary.Total != 1 {
		t.Fatalf("backup peek %+v %v", pk, err)
	}
	var v int
	if err := d.QueryRow("SELECT user_version FROM pragma_user_version").Scan(&v); err != nil || v != SchemaVersion {
		t.Fatalf("version %d %v", v, err)
	}
	// Reopening an upgraded catalog makes no further backup.
	d.Close()
	openTest(t, p)
	if baks, _ := filepath.Glob(p + ".bak-*"); len(baks) != 1 {
		t.Fatalf("backups after reopen %v", baks)
	}
}

// A reader without the lock never upgrades: the version 3 file is refused
// untouched.
func TestOpenExistingNoUpgrade(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "media.db")
	writeV3(t, p, nil)
	before, _ := os.ReadFile(p)
	if _, err := OpenExistingNoUpgrade(ctx, p, nil); !errors.Is(err, ErrNeedsUpgrade) {
		t.Fatalf("err %v", err)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("file changed")
	}
	if baks, _ := filepath.Glob(p + ".bak-*"); len(baks) != 0 {
		t.Fatalf("backup written: %v", baks)
	}
	// A current catalog opens normally.
	p4 := filepath.Join(t.TempDir(), "media.db")
	openTest(t, p4).Close()
	d, err := OpenExistingNoUpgrade(ctx, p4, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
}

// Rows written without the version 4 columns (an older picmover writing
// into an upgraded catalog) get their home time on the next write open.
func TestHealHomeOnOpen(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "media.db")
	d := openTest(t, p)
	if err := d.SetHomeZone(ctx, "Europe/Helsinki"); err != nil {
		t.Fatal(err)
	}
	wall := func(h int) int64 { return time.Date(2024, 7, 14, h, 30, 0, 0, time.UTC).Unix() }
	for _, r := range []struct {
		hash   int
		wall   int64
		off    any
		source string
	}{
		{1, wall(14), 7200, "exif"},       // CEST photo
		{2, wall(21), 32400, "mtime"},     // an instant in Tokyo's zone
		{3, wall(9), nil, "exif"},         // no offset
		{4, wall(10), 10800, "video_utc"}, // already in the home zone
	} {
		if _, err := d.Exec(`INSERT INTO media (hash, size, rel_path, file_type, ext, taken_at, taken_at_offset,
			taken_at_source, imported_at) VALUES (?, 1, ?, 'image', 'jpg', ?, ?, ?, 1)`,
			r.hash, filepath.Join("x", string(rune('a'+r.hash))), r.wall, r.off, r.source); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Exec(`INSERT INTO media (hash, size, rel_path, file_type, ext, taken_at_source, imported_at)
		VALUES (9, 1, 'undated', 'image', 'jpg', 'none', 1)`); err != nil {
		t.Fatal(err)
	}
	d.Close()
	for _, open := range []func(context.Context, string) (*DB, error){
		func(ctx context.Context, p string) (*DB, error) { return OpenExisting(ctx, p, nil) },
	} {
		d2, err := open(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		defer d2.Close()
		want := []struct{ local, home, src string }{
			{"2024-07-14 14:30 +02:00", "2024-07-14 15:30", OffsetSourceExif},
			{"2024-07-14 15:30 +03:00", "2024-07-14 15:30", OffsetSourceHome},
			{"2024-07-14 09:30 ?", "2024-07-14 09:30", ""},
			{"2024-07-14 10:30 +03:00", "2024-07-14 10:30", OffsetSourceHome},
		}
		for i, w := range want {
			m := get(t, d2, int64(i+1))
			if local(m) != w.local || home(m) != w.home || m.OffsetSource != w.src {
				t.Errorf("row %d: local %s home %s source %q, want %+v", i+1, local(m), home(m), m.OffsetSource, w)
			}
		}
		if m := get(t, d2, 5); !m.TakenAtHome.IsZero() {
			t.Errorf("undated row got a home time %v", m.TakenAtHome)
		}
	}
}

// A row written with offset source 'home' is stored in the home zone,
// whatever zone its time came in.
func TestWriteHomeSourceInHomeZone(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	if err := d.SetHomeZone(ctx, "Europe/Helsinki"); err != nil {
		t.Fatal(err)
	}
	tokyo := mustZone(t, "Asia/Tokyo")
	ids := insertRows(t, d,
		row{name: "m.jpg", takenAt: time.Date(2024, 7, 14, 21, 30, 0, 0, tokyo), offsetKnown: true, source: "mtime"},
		photo("p.jpg", time.Date(2024, 7, 14, 21, 30, 0, 0, tokyo), 1))
	if m := get(t, d, ids["m.jpg"]); local(m) != "2024-07-14 15:30 +03:00" || home(m) != "2024-07-14 15:30" {
		t.Errorf("mtime row: local %s home %s", local(m), home(m))
	}
	// An EXIF offset is the local time and stays.
	if m := get(t, d, ids["p.jpg"]); local(m) != "2024-07-14 21:30 +09:00" || home(m) != "2024-07-14 15:30" {
		t.Errorf("photo: local %s home %s", local(m), home(m))
	}
	// UpdateMetadata does the same.
	m := get(t, d, ids["m.jpg"])
	m.TakenAt = time.Date(2024, 7, 14, 21, 45, 0, 0, tokyo)
	if err := d.UpdateMetadata(ctx, nil, m.ID, &m); err != nil {
		t.Fatal(err)
	}
	if m := get(t, d, ids["m.jpg"]); local(m) != "2024-07-14 15:45 +03:00" {
		t.Errorf("updated: local %s", local(m))
	}
}

type busyErr struct{}

func (busyErr) Error() string { return "database is locked" }
func (busyErr) Code() int     { return 5 }

func TestRetryBusy(t *testing.T) {
	n := 0
	err := retryBusy(context.Background(), time.Minute, func() error {
		n++
		if n < 3 {
			return busyErr{}
		}
		return nil
	})
	if err != nil || n != 3 {
		t.Fatalf("err %v after %d", err, n)
	}
	n = 0
	err = retryBusy(context.Background(), 0, func() error { n++; return busyErr{} })
	if !isBusy(err) || n != 1 {
		t.Fatalf("bounded: %v after %d", err, n)
	}
	other := errors.New("other")
	if err := retryBusy(context.Background(), time.Minute, func() error { return other }); err != other {
		t.Fatalf("other error: %v", err)
	}
}
