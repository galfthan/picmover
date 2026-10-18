package catalog

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := LoadZone(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// at is a wall clock in zone loc.
func at(loc *time.Location, y int, m time.Month, d, h, mi int) time.Time {
	return time.Date(y, m, d, h, mi, 0, 0, loc)
}

// row describes a row to insert for the home time tests.
type row struct {
	name        string
	takenAt     time.Time
	offsetKnown bool
	source      string // taken_at_source
	fileType    string
	make, model string
	camType     string
	session     int64
}

func photo(name string, t time.Time, session int64) row {
	return row{name: name, takenAt: t, offsetKnown: true, source: "exif", fileType: FileTypeImage,
		make: "samsung", model: "SM-S921B", camType: "phone", session: session}
}

// samsungVideo is a video of an Android phone that only knows its UTC
// instant, converted to the import's zone as the extractor does.
func samsungVideo(name string, instant time.Time, importZone *time.Location, session int64) row {
	return row{name: name, takenAt: instant.In(importZone), offsetKnown: true, source: "video_utc",
		fileType: FileTypeVideo, make: "Android", model: "Android 14", camType: "phone", session: session}
}

func insertRows(t *testing.T, d *DB, rows ...row) map[string]int64 {
	t.Helper()
	ids := map[string]int64{}
	for i, r := range rows {
		ft := r.fileType
		if ft == "" {
			ft = FileTypeImage
		}
		m := &Media{Hash: uint64(1000 + i), Size: 1, RelPath: ft + "/" + r.name, FileType: ft, Ext: "x",
			TakenAt: r.takenAt, OffsetKnown: r.offsetKnown, TakenAtSource: r.source,
			CameraMake: r.make, CameraModel: r.model, CameraType: r.camType, ImportSessionID: r.session}
		if !insert(t, d, m) {
			t.Fatalf("%s not inserted", r.name)
		}
		ids[r.name] = m.ID
	}
	return ids
}

func get(t *testing.T, d *DB, id int64) Media {
	t.Helper()
	m, err := d.GetMedia(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// local formats the local capture time with its offset, home the home
// time.
func local(m Media) string {
	if !m.OffsetKnown {
		return m.TakenAt.Format("2006-01-02 15:04") + " ?"
	}
	return m.TakenAt.Format("2006-01-02 15:04 -07:00")
}
func home(m Media) string { return m.TakenAtHome.Format("2006-01-02 15:04") }

// A trip in Central Europe from a home in Helsinki: the photos carry
// their offset, the videos only a UTC instant (converted to the home zone
// on import). Home times put both on Helsinki's clock with the real
// summer/winter time of the date; the videos get the photos' local
// offset back.
func TestHomeTimeTrip(t *testing.T) {
	hel, paris := mustZone(t, "Europe/Helsinki"), mustZone(t, "Europe/Paris")
	for _, c := range []struct {
		name                  string
		y                     int
		mo                    time.Month
		d                     int
		wantLocal, wantVLocal string
		wantHome, wantVHome   string
	}{
		// Summer: CEST +02:00, EEST +03:00.
		{"summer", 2024, 7, 14, "2024-07-14 14:30 +02:00", "2024-07-14 14:40 +02:00", "2024-07-14 15:30", "2024-07-14 15:40"},
		// Winter: CET +01:00, EET +02:00.
		{"winter", 2024, 1, 14, "2024-01-14 14:30 +01:00", "2024-01-14 14:40 +01:00", "2024-01-14 15:30", "2024-01-14 15:40"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			d := newDB(t)
			if err := d.SetHomeZone(ctx, "Europe/Helsinki"); err != nil {
				t.Fatal(err)
			}
			p := at(paris, c.y, c.mo, c.d, 14, 30)
			v := at(paris, c.y, c.mo, c.d, 14, 40)
			later := at(paris, c.y, c.mo, c.d, 15, 0)
			ids := insertRows(t, d,
				photo("p1.jpg", p, 1),
				samsungVideo("v1.mp4", v, hel, 1),
				photo("p2.jpg", later, 1))
			// Before the pass the video sits an hour late on the local
			// clock (Helsinki's), but already in place on the home clock.
			if got := get(t, d, ids["v1.mp4"]); home(got) != c.wantVHome || got.OffsetSource != OffsetSourceHome {
				t.Fatalf("video before inference: home %s source %q", home(got), got.OffsetSource)
			}
			n, err := d.InferVideoOffsets(ctx, 1)
			if err != nil || n != 1 {
				t.Fatalf("InferVideoOffsets = %d, %v", n, err)
			}
			ph, vd := get(t, d, ids["p1.jpg"]), get(t, d, ids["v1.mp4"])
			if local(ph) != c.wantLocal || home(ph) != c.wantHome || ph.OffsetSource != OffsetSourceExif {
				t.Errorf("photo: local %s home %s source %q", local(ph), home(ph), ph.OffsetSource)
			}
			if local(vd) != c.wantVLocal || home(vd) != c.wantVHome || vd.OffsetSource != OffsetSourceNearby || vd.TakenAtSource != "video_utc" {
				t.Errorf("video: local %s home %s source %q/%q", local(vd), home(vd), vd.OffsetSource, vd.TakenAtSource)
			}
			// The timeline interleaves them in capture order.
			ms, err := d.List(ctx, ListOptions{OrderBy: "taken_at"})
			if err != nil {
				t.Fatal(err)
			}
			if got, want := idsOf(ms), []int64{ids["p1.jpg"], ids["v1.mp4"], ids["p2.jpg"]}; !reflect.DeepEqual(got, want) {
				t.Errorf("order %v, want %v", got, want)
			}
			// A second pass changes nothing.
			if n, err := d.InferVideoOffsets(ctx, 0); err != nil || n != 0 {
				t.Errorf("second pass = %d, %v", n, err)
			}
		})
	}
}

// A trip west across midnight and a month end: the timeline groups by
// the home date, and date filters bound home times.
func TestHomeTimeGroupsByHomeDate(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	if err := d.SetHomeZone(ctx, "Europe/Helsinki"); err != nil {
		t.Fatal(err)
	}
	ny := mustZone(t, "America/New_York")
	ids := insertRows(t, d,
		// 20:30 on 31 July in New York is 03:30 on 1 August in Helsinki.
		photo("late.jpg", at(ny, 2024, 7, 31, 20, 30), 1),
		photo("early.jpg", at(ny, 2024, 7, 31, 10, 0), 1))
	late := get(t, d, ids["late.jpg"])
	if local(late) != "2024-07-31 20:30 -04:00" || home(late) != "2024-08-01 03:30" {
		t.Fatalf("late: local %s home %s", local(late), home(late))
	}
	b, err := d.Buckets(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []Bucket{{2024, 8, 1}, {2024, 7, 1}}; !reflect.DeepEqual(b, want) {
		t.Errorf("buckets %v, want %v", b, want)
	}
	items, err := d.BucketItems(ctx, Filter{}, 2024, 8)
	if err != nil || len(items) != 1 || items[0].ID != ids["late.jpg"] {
		t.Errorf("August items %v, %v", idsOf(items), err)
	}
	aug := time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		f    Filter
		want []int64
	}{
		{Filter{From: aug}, []int64{ids["late.jpg"]}},
		{Filter{To: aug}, []int64{ids["early.jpg"]}},
		{Filter{Months: []int{8}}, []int64{ids["late.jpg"]}},
		{Filter{Recur: &Recur{From: "07-31", To: "07-31"}}, []int64{ids["early.jpg"]}},
	} {
		hs, err := d.MatchingHashes(ctx, c.f)
		if err != nil {
			t.Fatal(err)
		}
		var got []int64
		for _, h := range hs {
			for name, id := range ids {
				if get(t, d, id).Hash == h {
					got = append(got, ids[name])
				}
			}
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("filter %+v: %v, want %v", c.f, got, c.want)
		}
	}
}

// Rows without an offset keep their wall clock as home time, whatever
// the home zone; a video with no photo near it is shown in the home zone.
func TestHomeTimeUnknownOffsetAndLoneVideo(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	if err := d.SetHomeZone(ctx, "Europe/Helsinki"); err != nil {
		t.Fatal(err)
	}
	utc := time.UTC
	ids := insertRows(t, d,
		row{name: "old.jpg", takenAt: time.Date(2010, 6, 1, 12, 0, 0, 0, utc), source: "exif"},
		// A photo 7 hours before the video: outside the window.
		photo("far.jpg", at(mustZone(t, "Europe/Paris"), 2024, 7, 14, 7, 0), 1),
		samsungVideo("lone.mp4", time.Date(2024, 7, 14, 12, 0, 0, 0, utc), mustZone(t, "Asia/Tokyo"), 1))
	if _, err := d.InferVideoOffsets(ctx, 1); err != nil {
		t.Fatal(err)
	}
	old := get(t, d, ids["old.jpg"])
	if local(old) != "2010-06-01 12:00 ?" || home(old) != "2010-06-01 12:00" || old.OffsetSource != "" {
		t.Errorf("unknown offset: local %s home %s source %q", local(old), home(old), old.OffsetSource)
	}
	lone := get(t, d, ids["lone.mp4"])
	// Imported in Tokyo's zone, it is re-expressed in the home zone.
	if local(lone) != "2024-07-14 15:00 +03:00" || home(lone) != "2024-07-14 15:00" || lone.OffsetSource != OffsetSourceHome {
		t.Errorf("lone video: local %s home %s source %q", local(lone), home(lone), lone.OffsetSource)
	}
	for _, zone := range []string{"America/New_York", "Asia/Kolkata"} {
		if err := d.SetHomeZone(ctx, zone); err != nil {
			t.Fatal(err)
		}
		if got := get(t, d, ids["old.jpg"]); home(got) != "2010-06-01 12:00" {
			t.Errorf("%s: unknown offset home %s", zone, home(got))
		}
	}
}

// Changing the home zone recomputes every home time and re-expresses the
// rows that are on the home zone; local times read from the files stay.
func TestHomeZoneChangeRecomputes(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	paris := mustZone(t, "Europe/Paris")
	ids := insertRows(t, d,
		photo("p.jpg", at(paris, 2024, 7, 14, 14, 30), 1),
		row{name: "m.jpg", takenAt: time.Date(2024, 7, 14, 9, 0, 0, 0, time.UTC), offsetKnown: true, source: "mtime"})
	// No home zone: home times are the recorded wall clocks.
	if name, loc, err := d.HomeZone(ctx); err != nil || name != "" || loc != nil {
		t.Fatalf("HomeZone = %q %v %v", name, loc, err)
	}
	if p := get(t, d, ids["p.jpg"]); home(p) != "2024-07-14 14:30" {
		t.Fatalf("no zone: home %s", home(p))
	}
	for _, c := range []struct {
		zone, wantP, wantM, wantMLocal string
	}{
		{"Europe/Helsinki", "2024-07-14 15:30", "2024-07-14 12:00", "2024-07-14 12:00 +03:00"},
		{"America/New_York", "2024-07-14 08:30", "2024-07-14 05:00", "2024-07-14 05:00 -04:00"},
	} {
		if err := d.SetHomeZone(ctx, c.zone); err != nil {
			t.Fatal(err)
		}
		p, m := get(t, d, ids["p.jpg"]), get(t, d, ids["m.jpg"])
		if home(p) != c.wantP || local(p) != "2024-07-14 14:30 +02:00" {
			t.Errorf("%s: photo home %s local %s", c.zone, home(p), local(p))
		}
		if home(m) != c.wantM || local(m) != c.wantMLocal || m.OffsetSource != OffsetSourceHome {
			t.Errorf("%s: mtime row home %s local %s source %q", c.zone, home(m), local(m), m.OffsetSource)
		}
	}
	if name, _, _ := d.HomeZone(ctx); name != "America/New_York" {
		t.Errorf("home zone %q", name)
	}
	// EnsureHomeZone never replaces a stored zone.
	if loc, err := d.EnsureHomeZone(ctx, "Europe/Helsinki"); err != nil || loc.String() != "America/New_York" {
		t.Errorf("EnsureHomeZone = %v, %v", loc, err)
	}
	if err := d.SetHomeZone(ctx, "Mars/Olympus"); err == nil {
		t.Error("unknown zone accepted")
	}
}

// The home zone's own summer time applies at each date: a home time
// after the spring change is an hour further from UTC.
func TestHomeTimeDST(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	if err := d.SetHomeZone(ctx, "Europe/Helsinki"); err != nil {
		t.Fatal(err)
	}
	utc := time.UTC
	// Helsinki moves to summer time at 01:00 UTC on 31 March 2024.
	ids := insertRows(t, d,
		photo("before.jpg", time.Date(2024, 3, 31, 0, 30, 0, 0, utc), 1),
		photo("after.jpg", time.Date(2024, 3, 31, 1, 30, 0, 0, utc), 1))
	if b, a := get(t, d, ids["before.jpg"]), get(t, d, ids["after.jpg"]); home(b) != "2024-03-31 02:30" || home(a) != "2024-03-31 04:30" {
		t.Errorf("home times %s, %s", home(b), home(a))
	}
}

func TestInferVideoOffsetMatching(t *testing.T) {
	ctx := context.Background()
	hel, lisbon, paris := mustZone(t, "Europe/Helsinki"), mustZone(t, "Europe/Lisbon"), mustZone(t, "Europe/Paris")
	for _, c := range []struct {
		name  string
		rows  []row
		want  string // the video's local time
		wantS string
	}{
		{"same session phone", []row{
			photo("p", at(lisbon, 2024, 7, 14, 10, 0), 1),
			samsungVideo("v", at(lisbon, 2024, 7, 14, 11, 0), hel, 1)},
			"2024-07-14 11:00 +01:00", OffsetSourceNearby},
		{"android video, other session", []row{
			photo("p", at(lisbon, 2024, 7, 14, 10, 0), 2),
			samsungVideo("v", at(lisbon, 2024, 7, 14, 11, 0), hel, 1)},
			"2024-07-14 13:00 +03:00", OffsetSourceHome},
		{"android video, camera photo", []row{
			{name: "p", takenAt: at(lisbon, 2024, 7, 14, 10, 0), offsetKnown: true, source: "exif", make: "Canon", model: "EOS R5", camType: "camera", session: 1},
			samsungVideo("v", at(lisbon, 2024, 7, 14, 11, 0), hel, 1)},
			"2024-07-14 13:00 +03:00", OffsetSourceHome},
		{"same camera other session", []row{
			{name: "p", takenAt: at(lisbon, 2024, 7, 14, 10, 0), offsetKnown: true, source: "exif", make: "Apple", model: "iPhone 15", session: 2},
			{name: "v", takenAt: at(lisbon, 2024, 7, 14, 11, 0).In(hel), offsetKnown: true, source: "video_utc", fileType: FileTypeVideo, make: "Apple", model: "iPhone 15", session: 1}},
			"2024-07-14 11:00 +01:00", OffsetSourceNearby},
		{"same session preferred over nearer", []row{
			photo("near", at(paris, 2024, 7, 14, 10, 55), 2),
			photo("p", at(lisbon, 2024, 7, 14, 8, 0), 1),
			samsungVideo("v", at(lisbon, 2024, 7, 14, 11, 0), hel, 1)},
			"2024-07-14 11:00 +01:00", OffsetSourceNearby},
		{"nearest wins", []row{
			photo("p1", at(paris, 2024, 7, 14, 9, 0), 1),
			photo("p2", at(lisbon, 2024, 7, 14, 10, 50), 1),
			samsungVideo("v", at(lisbon, 2024, 7, 14, 11, 0), hel, 1)},
			"2024-07-14 11:00 +01:00", OffsetSourceNearby},
		{"window edge", []row{
			photo("p", at(lisbon, 2024, 7, 14, 5, 0), 1),
			samsungVideo("v", at(lisbon, 2024, 7, 14, 11, 0), hel, 1)},
			"2024-07-14 11:00 +01:00", OffsetSourceNearby},
		{"photo without offset is no evidence", []row{
			{name: "p", takenAt: time.Date(2024, 7, 14, 10, 0, 0, 0, time.UTC), source: "exif", make: "samsung", model: "SM-S921B", camType: "phone", session: 1},
			samsungVideo("v", at(lisbon, 2024, 7, 14, 11, 0), hel, 1)},
			"2024-07-14 13:00 +03:00", OffsetSourceHome},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := newDB(t)
			if err := d.SetHomeZone(ctx, "Europe/Helsinki"); err != nil {
				t.Fatal(err)
			}
			ids := insertRows(t, d, c.rows...)
			if _, err := d.InferVideoOffsets(ctx, 1); err != nil {
				t.Fatal(err)
			}
			v := get(t, d, ids["v"])
			if local(v) != c.want || v.OffsetSource != c.wantS || home(v) != v.TakenAt.In(hel).Format("2006-01-02 15:04") {
				t.Errorf("video local %s source %q home %s, want %s %q", local(v), v.OffsetSource, home(v), c.want, c.wantS)
			}
		})
	}
}

// writeV3 creates a version 3 catalog at p as picmover 3 left it (no
// meta table) with the given rows: (taken_at, taken_at_offset,
// taken_at_source, file_type, camera_make, camera_model, camera_type,
// import_session_id).
func writeV3(t *testing.T, p string, rows [][]any) {
	t.Helper()
	raw, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(schemaV3); err != nil {
		t.Fatal(err)
	}
	for i, r := range rows {
		if _, err := raw.Exec(`INSERT INTO media (hash, size, rel_path, file_type, ext, taken_at, taken_at_offset,
			taken_at_source, camera_make, camera_model, camera_type, imported_at, import_session_id)
			VALUES (?, 1, ?, ?, 'x', ?, ?, ?, ?, ?, ?, 1, ?)`,
			i+1, fmt.Sprintf("r%d", i+1), r[3], r[0], r[1], r[2], r[4], r[5], r[6], r[7]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec("PRAGMA user_version = 3"); err != nil {
		t.Fatal(err)
	}
}

// A version 3 catalog is migrated in place on open: offset sources from
// the date sources, the videos' local offsets inferred, home times in
// the system zone, which becomes the home zone.
func TestMigrateV3(t *testing.T) {
	t.Setenv("TZ", "Europe/Helsinki")
	ctx := context.Background()
	wall := func(y int, mo time.Month, d, h, mi int) int64 {
		return time.Date(y, mo, d, h, mi, 0, 0, time.UTC).Unix()
	}
	rows := [][]any{
		// 1: a CEST photo, 14:30 +02:00.
		{wall(2024, 7, 14, 14, 30), 7200, "exif", "image", "samsung", "SM-S921B", "phone", 1},
		// 2: its video, imported in Helsinki: 15:40 +03:00 (14:40 local).
		{wall(2024, 7, 14, 15, 40), 10800, "video_utc", "video", "Android", "Android 14", "phone", 1},
		// 3: a photo without offset.
		{wall(2010, 6, 1, 12, 0), nil, "exif", "image", "", "", "", 1},
		// 4: a video with no photo near it, imported in UTC.
		{wall(2023, 1, 5, 10, 0), 0, "video_utc", "video", "Android", "Android 13", "phone", 2},
		// 5: undated.
		{nil, nil, "none", "image", "", "", "", 2},
	}
	for _, open := range []struct {
		name string
		fn   func(context.Context, string) (*DB, error)
	}{
		{"Open", func(ctx context.Context, p string) (*DB, error) { return Open(ctx, p, nil) }},
		{"OpenExisting", func(ctx context.Context, p string) (*DB, error) { return OpenExisting(ctx, p, nil) }},
	} {
		t.Run(open.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "media.db")
			writeV3(t, p, rows)
			before, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			// A peek reads it as it is, without migrating.
			pk, err := PeekFile(ctx, p)
			if err != nil || pk.Summary.Total != 5 || pk.Summary.Latest.Format(time.DateTime) != "2024-07-14 15:40:00" {
				t.Fatalf("peek v3: %+v, %v", pk.Summary, err)
			}
			if after, _ := os.ReadFile(p); string(after) != string(before) {
				t.Fatal("peek modified the v3 catalog")
			}
			d, err := open.fn(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if v := userVersion(t, d); v != SchemaVersion {
				t.Fatalf("user_version %d", v)
			}
			if name, _, _ := d.HomeZone(ctx); name != "Europe/Helsinki" {
				t.Errorf("home zone %q", name)
			}
			want := []struct{ local, home, src string }{
				{"2024-07-14 14:30 +02:00", "2024-07-14 15:30", OffsetSourceExif},
				{"2024-07-14 14:40 +02:00", "2024-07-14 15:40", OffsetSourceNearby},
				{"2010-06-01 12:00 ?", "2010-06-01 12:00", ""},
				{"2023-01-05 12:00 +02:00", "2023-01-05 12:00", OffsetSourceHome},
			}
			for i, w := range want {
				m := get(t, d, int64(i+1))
				if local(m) != w.local || home(m) != w.home || m.OffsetSource != w.src {
					t.Errorf("row %d: local %s home %s source %q, want %+v", i+1, local(m), home(m), m.OffsetSource, w)
				}
			}
			if m := get(t, d, 5); !m.TakenAt.IsZero() || !m.TakenAtHome.IsZero() {
				t.Errorf("undated row: %v %v", m.TakenAt, m.TakenAtHome)
			}
			b, err := d.Buckets(ctx, Filter{})
			if err != nil {
				t.Fatal(err)
			}
			if want := []Bucket{{2024, 7, 2}, {2023, 1, 1}, {2010, 6, 1}, {0, 0, 1}}; !reflect.DeepEqual(b, want) {
				t.Errorf("buckets %v, want %v", b, want)
			}
			// Reopening does not migrate again.
			d.Close()
			d2 := openTest(t, p)
			if m := get(t, d2, 2); local(m) != "2024-07-14 14:40 +02:00" {
				t.Errorf("after reopen: %s", local(m))
			}
		})
	}
}

// Without a known system zone the migration stores no home zone: home
// times are the recorded wall clocks.
func TestMigrateV3NoSystemZone(t *testing.T) {
	t.Setenv("TZ", "Nowhere/Unknown")
	p := filepath.Join(t.TempDir(), "media.db")
	writeV3(t, p, [][]any{
		{time.Date(2024, 7, 14, 14, 30, 0, 0, time.UTC).Unix(), 7200, "exif", "image", "samsung", "SM-S921B", "phone", 1},
		{time.Date(2024, 7, 14, 15, 40, 0, 0, time.UTC).Unix(), 10800, "video_utc", "video", "Android", "Android 14", "phone", 1},
	})
	d := openTest(t, p)
	if name, loc, _ := d.HomeZone(context.Background()); name != "" || loc != nil {
		t.Errorf("home zone %q", name)
	}
	if m := get(t, d, 1); home(m) != "2024-07-14 14:30" {
		t.Errorf("photo home %s", home(m))
	}
	// The video still borrows the photo's offset, and its home time is
	// its new local time.
	if m := get(t, d, 2); local(m) != "2024-07-14 14:40 +02:00" || home(m) != "2024-07-14 14:40" {
		t.Errorf("video local %s home %s", local(m), home(m))
	}
}

func TestHomeZoneFile(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "media.db")
	d := openTest(t, p)
	if hz, err := HomeZoneFile(ctx, p); err != nil || hz != "" {
		t.Fatalf("HomeZoneFile = %q, %v", hz, err)
	}
	if err := d.SetHomeZone(ctx, "Asia/Tokyo"); err != nil {
		t.Fatal(err)
	}
	if hz, err := HomeZoneFile(ctx, p); err != nil || hz != "Asia/Tokyo" {
		t.Fatalf("HomeZoneFile = %q, %v", hz, err)
	}
	// A version 3 catalog without a meta table has none.
	p3 := filepath.Join(t.TempDir(), "media.db")
	writeV3(t, p3, nil)
	if hz, err := HomeZoneFile(ctx, p3); err != nil || hz != "" {
		t.Fatalf("v3 HomeZoneFile = %q, %v", hz, err)
	}
}

func TestSystemZoneName(t *testing.T) {
	t.Setenv("TZ", "Europe/Helsinki")
	if got := SystemZoneName(); got != "Europe/Helsinki" {
		t.Errorf("TZ=Europe/Helsinki: %q", got)
	}
	t.Setenv("TZ", ":America/New_York")
	if got := SystemZoneName(); got != "America/New_York" {
		t.Errorf("TZ=:America/New_York: %q", got)
	}
	t.Setenv("TZ", "Bogus/Zone")
	if got := SystemZoneName(); got != "" {
		t.Errorf("TZ=Bogus/Zone: %q", got)
	}
	if got := ZoneName(mustZone(t, "Asia/Tokyo")); got != "Asia/Tokyo" {
		t.Errorf("ZoneName = %q", got)
	}
}
