package catalog

import (
	"context"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// seedQuery inserts the rows the browsing-query tests use and returns
// their ids by hash.
func seedQuery(t *testing.T, d *DB) map[uint64]int64 {
	t.Helper()
	wall := func(y int, m time.Month, day, h, mi, s int) time.Time {
		return time.Date(y, m, day, h, mi, s, 0, time.UTC)
	}
	plus3 := time.FixedZone("", 3*3600)
	rows := []Media{
		{Hash: 1, FileType: "image", TakenAt: wall(2024, 7, 14, 13, 5, 22), HasGPS: true, Lat: 60, Lon: 25,
			CameraMake: "Canon", CameraModel: "EOS R5", ImportSessionID: 1},
		// The stored wall clock decides the month, whatever the offset.
		{Hash: 2, FileType: "image", TakenAt: time.Date(2024, 7, 31, 23, 59, 59, 0, plus3), OffsetKnown: true,
			HasGPS: true, Lat: 60.001, Lon: 25.001, CameraMake: "Canon", CameraModel: "EOS R5", ImportSessionID: 1},
		{Hash: 3, FileType: "video", TakenAt: wall(2024, 8, 1, 0, 0, 0), DurationMS: 1500, ImportSessionID: 1},
		{Hash: 4, FileType: "image_raw", TakenAt: wall(2023, 1, 5, 10, 0, 0), HasGPS: true, Lat: -33.9, Lon: 151.2,
			CameraMake: "NIKON", CameraModel: "Z 6", ImportSessionID: 2},
		{Hash: 5, FileType: "image"},
		{Hash: 6, FileType: "image"},
		{Hash: 7, FileType: "image", TakenAt: wall(2024, 7, 2, 0, 0, 0), CameraModel: "EOS R5", CameraMake: "Canon",
			DeletedAt: time.Unix(1700000000, 0)},
		{Hash: 8, FileType: "image", TakenAt: wall(1965, 3, 1, 8, 0, 0), HasGPS: true, Lat: 0.5, Lon: 179.5},
		{Hash: 9, FileType: "image", TakenAt: wall(2022, 2, 2, 2, 2, 2), HasGPS: true, Lat: 0.5, Lon: -179.5,
			CameraMake: "Apple"},
	}
	ids := map[uint64]int64{}
	for i := range rows {
		m := &rows[i]
		m.Size = 100
		m.RelPath = fmt.Sprintf("%s/x/%d.jpg", m.FileType, m.Hash)
		m.Ext = ".jpg"
		if !insert(t, d, m) {
			t.Fatalf("row %d not inserted", m.Hash)
		}
		ids[m.Hash] = m.ID
	}
	return ids
}

func TestBuckets(t *testing.T) {
	d := newDB(t)
	seedQuery(t, d)
	ctx := context.Background()
	for _, c := range []struct {
		name string
		f    Filter
		want []Bucket
	}{
		{"all", Filter{}, []Bucket{{2024, 8, 1}, {2024, 7, 2}, {2023, 1, 1}, {2022, 2, 1}, {1965, 3, 1}, {0, 0, 2}}},
		{"deleted", Filter{IncludeDeleted: true}, []Bucket{{2024, 8, 1}, {2024, 7, 3}, {2023, 1, 1}, {2022, 2, 1}, {1965, 3, 1}, {0, 0, 2}}},
		{"video", Filter{FileType: "video"}, []Bucket{{2024, 8, 1}}},
		{"raw", Filter{FileType: "image_raw"}, []Bucket{{2023, 1, 1}}},
		{"gps", Filter{HasGPS: true}, []Bucket{{2024, 7, 2}, {2023, 1, 1}, {2022, 2, 1}, {1965, 3, 1}}},
		{"camera", Filter{CameraModel: "EOS R5"}, []Bucket{{2024, 7, 2}}},
		{"session", Filter{SessionID: 2}, []Bucket{{2023, 1, 1}}},
		{"range", Filter{From: time.Date(2024, 7, 15, 0, 0, 0, 0, time.UTC), To: time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC)},
			[]Bucket{{2024, 7, 1}}},
		// From/To compare wall clocks: a zone on the bound is ignored.
		{"range zone", Filter{From: time.Date(2024, 8, 1, 0, 0, 0, 0, time.FixedZone("", -5*3600))}, []Bucket{{2024, 8, 1}}},
		{"none", Filter{CameraModel: "nope"}, nil},
	} {
		got, err := d.Buckets(ctx, c.f)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if _, err := d.Buckets(ctx, Filter{FileType: "audio"}); err == nil {
		t.Error("unknown file type accepted")
	}
}

func idsOf(ms []Media) []int64 {
	var out []int64
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func TestBucketItems(t *testing.T) {
	d := newDB(t)
	ids := seedQuery(t, d)
	ctx := context.Background()
	for _, c := range []struct {
		name        string
		f           Filter
		year, month int
		want        []uint64
	}{
		{"july", Filter{}, 2024, 7, []uint64{2, 1}},
		{"july deleted", Filter{IncludeDeleted: true}, 2024, 7, []uint64{2, 1, 7}},
		{"august boundary", Filter{}, 2024, 8, []uint64{3}},
		{"raw in july", Filter{FileType: "image_raw"}, 2024, 7, nil},
		{"undated", Filter{}, 0, 0, []uint64{6, 5}},
		{"undated gps", Filter{HasGPS: true}, 0, 0, nil},
		{"pre-1970", Filter{}, 1965, 3, []uint64{8}},
		{"from inside", Filter{From: time.Date(2024, 7, 20, 0, 0, 0, 0, time.UTC)}, 2024, 7, []uint64{2}},
		{"empty month", Filter{}, 2024, 6, nil},
	} {
		got, err := d.BucketItems(ctx, c.f, c.year, c.month)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var want []int64
		for _, h := range c.want {
			want = append(want, ids[h])
		}
		if !reflect.DeepEqual(idsOf(got), want) {
			t.Errorf("%s: got ids %v, want %v", c.name, idsOf(got), want)
		}
	}
	got, err := d.BucketItems(ctx, Filter{}, 2024, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].OffsetKnown || got[0].TakenAt.Format("2006-01-02T15:04:05") != "2024-07-31T23:59:59" {
		t.Errorf("row decoded as %v (offset known %v)", got[0].TakenAt, got[0].OffsetKnown)
	}
	for _, bad := range [][2]int{{2024, 0}, {2024, 13}, {0, 5}, {-1, 1}, {10000, 1}} {
		if _, err := d.BucketItems(ctx, Filter{}, bad[0], bad[1]); err == nil {
			t.Errorf("bucket %v accepted", bad)
		}
	}
}

// TestBucketItemsUsesTakenAtIndex checks the query plan: the month range is
// answered from media_taken_at even when a filter matches another index.
func TestBucketItemsUsesTakenAtIndex(t *testing.T) {
	d := newDB(t)
	for _, f := range []Filter{{}, {FileType: "image"}, {SessionID: 3}, {HasGPS: true, CameraModel: "x"}} {
		for _, ym := range [][2]int{{2024, 7}, {0, 0}} {
			q, args, err := bucketItemsQuery(f, ym[0], ym[1])
			if err != nil {
				t.Fatal(err)
			}
			rows, err := d.Query("EXPLAIN QUERY PLAN "+q, args...)
			if err != nil {
				t.Fatal(err)
			}
			var plan []string
			for rows.Next() {
				var id, parent, notused int
				var detail string
				if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
					t.Fatal(err)
				}
				plan = append(plan, detail)
			}
			rows.Close()
			p := strings.Join(plan, "; ")
			if !strings.Contains(p, "USING INDEX media_taken_at (taken_at") {
				t.Errorf("filter %+v bucket %v: plan %q does not use the taken_at range", f, ym, p)
			}
		}
	}
}

func TestCameras(t *testing.T) {
	d := newDB(t)
	seedQuery(t, d)
	got, err := d.Cameras(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Camera{{"Canon", "EOS R5", 2}, {"NIKON", "Z 6", 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestGeoPointsAndItems(t *testing.T) {
	d := newDB(t)
	ids := seedQuery(t, d)
	ctx := context.Background()
	pointIDs := func(ps []GeoPoint) []int64 {
		var out []int64
		for _, p := range ps {
			out = append(out, p.ID)
		}
		return out
	}
	want := func(hs ...uint64) []int64 {
		var out []int64
		for _, h := range hs {
			out = append(out, ids[h])
		}
		return out
	}
	for _, c := range []struct {
		name                     string
		f                        Filter
		west, south, east, north float64
		limit                    int
		want                     []int64
	}{
		{"world", Filter{}, -180, -90, 180, 90, 0, want(2, 1, 4, 9, 8)},
		{"world limit", Filter{}, -180, -90, 180, 90, 2, want(2, 1)},
		{"helsinki", Filter{}, 24, 59, 26, 61, 0, want(2, 1)},
		{"antimeridian", Filter{}, 179, -1, -179, 1, 0, want(9, 8)},
		{"antimeridian excludes", Filter{}, 179.6, -1, -179.6, 1, 0, nil},
		{"zero width", Filter{}, 25, 59, 25, 61, 0, want(1)},
		{"raw", Filter{FileType: "image_raw"}, -180, -90, 180, 90, 0, want(4)},
		{"camera", Filter{CameraModel: "EOS R5", From: time.Date(2024, 7, 20, 0, 0, 0, 0, time.UTC)}, -180, -90, 180, 90, 0, want(2)},
	} {
		got, err := d.GeoPoints(ctx, c.f, c.west, c.south, c.east, c.north, c.limit)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		gotIDs, wantIDs := pointIDs(got), c.want
		if c.limit == 0 {
			// Unordered without a limit.
			slices.Sort(gotIDs)
			wantIDs = slices.Sorted(slices.Values(c.want))
		}
		if !reflect.DeepEqual(gotIDs, wantIDs) {
			t.Errorf("%s: got %v, want %v", c.name, gotIDs, wantIDs)
		}
		items, total, err := d.GeoItems(ctx, c.f, c.west, c.south, c.east, c.north, c.limit)
		if err != nil {
			t.Fatalf("%s items: %v", c.name, err)
		}
		if !reflect.DeepEqual(idsOf(items), c.want) {
			t.Errorf("%s items: got %v, want %v", c.name, idsOf(items), c.want)
		}
		full, _ := d.GeoPoints(ctx, c.f, c.west, c.south, c.east, c.north, 0)
		if total != int64(len(full)) {
			t.Errorf("%s: total %d, want %d", c.name, total, len(full))
		}
	}
	// The parallel table scan returns the same rows.
	old := geoMinPartRows
	geoMinPartRows = 2
	defer func() { geoMinPartRows = old }()
	all, err := d.GeoPoints(ctx, Filter{}, -180, -90, 180, 90, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := pointIDs(all)
	slices.Sort(got)
	if want := slices.Sorted(slices.Values(want(1, 2, 4, 8, 9))); !reflect.DeepEqual(got, want) {
		t.Fatalf("parallel scan: %v, want %v", got, want)
	}
	geoMinPartRows = old

	got2, _ := d.GeoPoints(ctx, Filter{}, 24, 59, 26, 61, 1)
	if len(got2) != 1 || got2[0].Hash != 2 || got2[0].Lat != 60.001 || got2[0].Lon != 25.001 ||
		got2[0].TakenAt.Format(time.DateTime) != "2024-07-31 23:59:59" {
		t.Errorf("point decoded as %+v", got2)
	}
	if _, err := d.GeoPoints(ctx, Filter{}, 0, 10, 1, 5, 0); err == nil {
		t.Error("south > north accepted")
	}
}

// genCatalog fills d with n rows for the benchmarks: dates over 20 years,
// one month (2020-06) holding 5000 rows, every row with coordinates.
func genCatalog(b *testing.B, d *DB, n int) {
	b.Helper()
	ctx := context.Background()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	r := rand.New(rand.NewPCG(1, 2))
	start := time.Date(2006, 1, 1, 0, 0, 0, 0, time.UTC)
	span := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Sub(start)
	june := time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)
	types := []string{"image", "image", "image", "image_raw", "video"}
	for i := range n {
		var taken time.Time
		switch {
		case i < 5000:
			taken = june.Add(time.Duration(r.Int64N(int64(29 * 24 * time.Hour))))
		case i%50 == 0:
			// undated
		default:
			taken = start.Add(time.Duration(r.Int64N(int64(span))))
			if taken.Year() == 2020 && taken.Month() == 6 {
				taken = taken.AddDate(0, 1, 0)
			}
		}
		m := &Media{Hash: uint64(i + 1), Size: 1000, RelPath: fmt.Sprintf("x/%d.jpg", i), FileType: types[i%len(types)],
			Ext: ".jpg", TakenAt: taken, Width: 4000, Height: 3000, HasGPS: true,
			Lat: r.Float64()*140 - 70, Lon: r.Float64()*360 - 180, CameraMake: "Canon",
			CameraModel: fmt.Sprintf("Model %d", i%7), ThumbState: "ok"}
		if _, err := d.InsertMedia(ctx, tx, m); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
}

func benchDB(b *testing.B) *DB {
	b.Helper()
	d, err := Open(context.Background(), b.TempDir()+"/media.db", nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { d.Close() })
	genCatalog(b, d, 100_000)
	return d
}

// The benchmarks run only with -bench; each builds a 100k-row catalog.
// go test -tags nodynamic -run '^$' -bench . ./internal/catalog/
func BenchmarkBuckets(b *testing.B) {
	d := benchDB(b)
	ctx := context.Background()
	for b.Loop() {
		if _, err := d.Buckets(ctx, Filter{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBucketsFiltered(b *testing.B) {
	d := benchDB(b)
	ctx := context.Background()
	for b.Loop() {
		if _, err := d.Buckets(ctx, Filter{FileType: "image", CameraModel: "Model 3"}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBucketItems5k(b *testing.B) {
	d := benchDB(b)
	ctx := context.Background()
	for b.Loop() {
		items, err := d.BucketItems(ctx, Filter{}, 2020, 6)
		if err != nil || len(items) < 5000 {
			b.Fatal(len(items), err)
		}
	}
}

func BenchmarkGeoPointsWorld(b *testing.B) {
	d := benchDB(b)
	ctx := context.Background()
	for b.Loop() {
		ps, err := d.GeoPoints(ctx, Filter{}, -180, -90, 180, 90, 0)
		if err != nil || len(ps) != 100_000 {
			b.Fatal(len(ps), err)
		}
	}
}

// taken_at outside years 1-9999 (reachable through a corrupt file mtime)
// is undated in both Buckets and BucketItems, and never fails Buckets.
func TestBucketsOutOfRangeYears(t *testing.T) {
	d := newDB(t)
	ctx := context.Background()
	unix := func(y int, m time.Month, day, h, mi, s int) int64 {
		return time.Date(y, m, day, h, mi, s, 0, time.UTC).Unix()
	}
	if minDatedTakenAt != unix(1, 1, 1, 0, 0, 0) || maxDatedTakenAt != unix(9999, 12, 31, 23, 59, 59) {
		t.Fatalf("bounds %d %d", minDatedTakenAt, maxDatedTakenAt)
	}
	values := map[uint64]int64{
		1: unix(0, 6, 15, 12, 0, 0),   // year 0
		2: unix(-1, 6, 15, 12, 0, 0),  // year -1
		3: unix(10000, 1, 1, 0, 0, 0), // year 10000
		4: maxDatedTakenAt,            // 9999-12-31 23:59:59: dated
		5: minDatedTakenAt,            // 0001-01-01 00:00:00: dated
		6: minDatedTakenAt - 1,        // just before year 1
		7: maxDatedTakenAt + 1,        // just after year 9999
		8: unix(2024, 7, 1, 0, 0, 0),  // an ordinary date
	}
	ids := map[int64]uint64{}
	for h, v := range values {
		m := &Media{Hash: h, FileType: "image", Size: 1, RelPath: fmt.Sprintf("image/x/%d.jpg", h), Ext: ".jpg",
			TakenAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
		if !insert(t, d, m) {
			t.Fatal("not inserted")
		}
		if _, err := d.Exec("UPDATE media SET taken_at = ? WHERE id = ?", v, m.ID); err != nil {
			t.Fatal(err)
		}
		ids[m.ID] = h
	}
	got, err := d.Buckets(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	want := []Bucket{{9999, 12, 1}, {2024, 7, 1}, {1, 1, 1}, {0, 0, 5}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buckets %v, want %v", got, want)
	}
	hashes := func(year, month int) []uint64 {
		items, err := d.BucketItems(ctx, Filter{}, year, month)
		if err != nil {
			t.Fatal(err)
		}
		var hs []uint64
		for _, m := range items {
			hs = append(hs, ids[m.ID])
		}
		slices.Sort(hs)
		return hs
	}
	for _, b := range got {
		if n := len(hashes(b.Year, b.Month)); int64(n) != b.Count {
			t.Errorf("bucket %v has %d items", b, n)
		}
	}
	if h := hashes(0, 0); !reflect.DeepEqual(h, []uint64{1, 2, 3, 6, 7}) {
		t.Errorf("undated items %v", h)
	}
	if h := hashes(9999, 12); !reflect.DeepEqual(h, []uint64{4}) {
		t.Errorf("9999-12 items %v", h)
	}
	if h := hashes(1, 1); !reflect.DeepEqual(h, []uint64{5}) {
		t.Errorf("0001-01 items %v", h)
	}
}
