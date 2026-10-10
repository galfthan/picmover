package catalog

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
)

// bucketHashes returns the hashes matching f via BucketItems over every
// bucket of Buckets, sorted.
func bucketHashes(t *testing.T, d *DB, f Filter) []uint64 {
	t.Helper()
	ctx := context.Background()
	bs, err := d.Buckets(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	var out []uint64
	for _, b := range bs {
		items, err := d.BucketItems(ctx, f, b.Year, b.Month)
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(items)) != b.Count {
			t.Fatalf("bucket %v has %d items", b, len(items))
		}
		for _, m := range items {
			out = append(out, m.Hash)
		}
	}
	slices.Sort(out)
	return out
}

func TestFilterHashes(t *testing.T) {
	d := newDB(t)
	seedQuery(t, d)
	big := make([]uint64, 50000)
	for i := range big {
		big[i] = uint64(1000 + i)
	}
	big[123], big[45678] = 2, 4
	for _, c := range []struct {
		name   string
		hashes []uint64
		want   []uint64
	}{
		{"nil", nil, []uint64{1, 2, 3, 4, 5, 6, 8, 9}},
		{"empty", []uint64{}, nil},
		{"one", []uint64{3}, []uint64{3}},
		{"deleted excluded", []uint64{7, 1}, []uint64{1}},
		{"50k", big, []uint64{2, 4}},
	} {
		start := time.Now()
		got := bucketHashes(t, d, Filter{Hashes: c.hashes})
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
		if el := time.Since(start); el > 2*time.Second {
			t.Errorf("%s took %v", c.name, el)
		}
	}
	// Geo queries take the restriction too.
	pts, err := d.GeoPoints(context.Background(), Filter{Hashes: []uint64{1, 4}}, -180, -90, 180, 90, 0)
	if err != nil || len(pts) != 2 {
		t.Fatalf("geo with hashes: %v %v", pts, err)
	}
}

func TestFilterRecur(t *testing.T) {
	d := newDB(t)
	ctx := context.Background()
	wall := func(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 12, 0, 0, 0, time.UTC) }
	dates := map[uint64]time.Time{
		1: wall(2020, 12, 24), 2: wall(2021, 12, 26), 3: wall(2022, 12, 27), 4: wall(2023, 1, 1),
		5: wall(2023, 1, 2), 6: wall(2023, 1, 3), 7: wall(2024, 2, 29), 8: wall(2019, 6, 15), 9: {},
		10: time.Date(2022, 12, 31, 23, 59, 59, 0, time.UTC),
	}
	for h, at := range dates {
		m := Media{Hash: h, Size: 1, FileType: "image", Ext: "jpg", RelPath: "image/" + string(rune('a'+h)) + ".jpg", TakenAt: at}
		if !insert(t, d, &m) {
			t.Fatal("insert")
		}
	}
	for _, c := range []struct {
		name     string
		from, to string
		f        Filter
		want     []uint64
	}{
		{"christmas", "12-24", "12-26", Filter{}, []uint64{1, 2}},
		{"one day", "06-15", "06-15", Filter{}, []uint64{8}},
		{"wrap", "12-30", "01-02", Filter{}, []uint64{4, 5, 10}},
		{"leap day", "02-29", "02-29", Filter{}, []uint64{7}},
		{"whole year", "01-01", "12-31", Filter{}, []uint64{1, 2, 3, 4, 5, 6, 7, 8, 10}},
		{"with from/to", "12-24", "01-02", Filter{From: wall(2021, 1, 1), To: wall(2023, 1, 2)}, []uint64{2, 3, 4, 10}},
		{"with hashes", "12-24", "01-02", Filter{Hashes: []uint64{1, 9}}, []uint64{1}},
	} {
		f := c.f
		f.Recur = &Recur{From: c.from, To: c.to}
		if got := bucketHashes(t, d, f); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	// Buckets with recur return only months with matches; undated never.
	bs, err := d.Buckets(ctx, Filter{Recur: &Recur{"12-30", "01-02"}})
	if err != nil || !reflect.DeepEqual(bs, []Bucket{{2023, 1, 2}, {2022, 12, 1}}) {
		t.Fatalf("wrap buckets %v %v", bs, err)
	}
	for _, bad := range []Recur{{"13-01", "01-01"}, {"02-30", "03-01"}, {"1-1", "01-01"}, {"01-01", ""}, {"00-10", "01-01"}} {
		if _, err := d.Buckets(ctx, Filter{Recur: &bad}); err == nil {
			t.Errorf("recur %v accepted", bad)
		}
	}
	for s, ok := range map[string]bool{"02-29": true, "04-31": false, "12-31": true, "ab-cd": false, "01-00": false} {
		if ValidMonthDay(s) != ok {
			t.Errorf("ValidMonthDay(%q) != %v", s, ok)
		}
	}
}

func TestTrashRows(t *testing.T) {
	d := newDB(t)
	ids := seedQuery(t, d)
	ctx := context.Background()
	at := time.Unix(1750000000, 0)
	if err := d.SetDeleted(ctx, 1, at); err != nil {
		t.Fatal(err)
	}
	if err := d.SetDeleted(ctx, 999, at); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown hash: %v", err)
	}
	if got := bucketHashes(t, d, Filter{Trashed: true}); !reflect.DeepEqual(got, []uint64{1, 7}) {
		t.Fatalf("trashed listing %v", got)
	}
	if got := bucketHashes(t, d, Filter{Trashed: true, FileType: "image", Hashes: []uint64{1}}); !reflect.DeepEqual(got, []uint64{1}) {
		t.Fatalf("trashed with filters %v", got)
	}
	tr, err := d.ListTrashed(ctx)
	if err != nil || len(tr) != 2 || tr[0].Hash != 1 || !tr[0].DeletedAt.Equal(at) {
		t.Fatalf("list trashed %+v %v", tr, err)
	}
	if err := d.ClearDeleted(ctx, 1, "image/x/1_1.jpg"); err != nil {
		t.Fatal(err)
	}
	m, err := d.GetByHash(ctx, 1)
	if err != nil || !m.DeletedAt.IsZero() || m.RelPath != "image/x/1_1.jpg" || m.ID != ids[1] {
		t.Fatalf("restored row %+v %v", m, err)
	}
	if err := d.ClearDeleted(ctx, 7, ""); err != nil {
		t.Fatal(err)
	}
	if m, _ := d.GetByHash(ctx, 7); m.RelPath != "image/x/7.jpg" || !m.DeletedAt.IsZero() {
		t.Fatalf("restore keeping path %+v", m)
	}
	if err := d.UpdateRelPath(ctx, 7, "image/x/7b.jpg"); err != nil {
		t.Fatal(err)
	}
	byh, err := d.ByHashes(ctx, []uint64{7, 1, 12345})
	if err != nil || len(byh) != 2 || byh[7].RelPath != "image/x/7b.jpg" {
		t.Fatalf("by hashes %+v %v", byh, err)
	}
	if err := d.ReplaceSimilarPairs(ctx, []SimilarPair{{A: 7, B: 1, Distance: 2}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GetByHash(ctx, 7); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("removed row: %v", err)
	}
	if n, _ := d.CountSimilarPairs(ctx); n != 0 {
		t.Fatalf("pairs of a removed row survive: %d", n)
	}
	if err := d.Remove(ctx, 7); err != nil {
		t.Fatalf("remove twice: %v", err)
	}
}

func TestSimilarPairsAndMeta(t *testing.T) {
	d := newDB(t)
	seedQuery(t, d)
	ctx := context.Background()
	big := uint64(1 << 63) // smallest as int64
	if err := d.ReplaceSimilarPairs(ctx, []SimilarPair{{A: 2, B: 1, Distance: 3}, {A: 1, B: 3, Distance: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.ReplaceSimilarPairs(ctx, []SimilarPair{{A: 2, B: 1, Distance: 4}, {A: 7, B: 1, Distance: 0}, {A: 4, B: big, Distance: 5}},
		map[string]string{MetaSimilarAt: "123", MetaSimilarDistance: "6"}); err != nil {
		t.Fatal(err)
	}
	if n, _ := d.CountSimilarPairs(ctx); n != 3 {
		t.Fatalf("stored %d pairs, want 3 (replaced)", n)
	}
	// Live pairs only: 7 is deleted, big has no row.
	got, err := d.SimilarPairs(ctx)
	if err != nil || !reflect.DeepEqual(got, []SimilarPair{{A: 1, B: 2, Distance: 4}}) {
		t.Fatalf("pairs %+v %v", got, err)
	}
	var a, b int64
	d.QueryRow("SELECT hash_a, hash_b FROM similar_pairs WHERE distance = 5").Scan(&a, &b)
	if uint64(a) != big || b != 4 {
		t.Fatalf("pair not normalised as int64: %d %d", a, b)
	}
	if v, ok, err := d.Meta(ctx, MetaSimilarAt); err != nil || !ok || v != "123" {
		t.Fatalf("meta %q %v %v", v, ok, err)
	}
	if err := d.SetMeta(ctx, map[string]string{MetaSimilarAt: "456"}); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := d.Meta(ctx, MetaSimilarAt); v != "456" {
		t.Fatalf("meta overwrite %q", v)
	}
	if _, ok, err := d.Meta(ctx, "nope"); ok || err != nil {
		t.Fatalf("missing meta %v %v", ok, err)
	}
}

func TestDerivedTablesCreatedOnOpen(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "media.db")
	d := openTest(t, p)
	if _, err := d.Exec("DROP TABLE similar_pairs; DROP TABLE meta"); err != nil {
		t.Fatal(err)
	}
	d.Close()
	for _, open := range []func() (*DB, error){
		func() (*DB, error) { return OpenExisting(ctx, p, nil) },
		func() (*DB, error) { return Open(ctx, p, nil) },
	} {
		d, err := open()
		if err != nil {
			t.Fatal(err)
		}
		if userVersion(t, d) != SchemaVersion {
			t.Fatal("version changed")
		}
		if _, _, err := d.Meta(ctx, "x"); err != nil {
			t.Fatalf("meta table missing: %v", err)
		}
		d.Exec("DROP TABLE similar_pairs; DROP TABLE meta")
		d.Close()
	}
}

func TestPeekAndPHashItems(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "media.db")
	d := openTest(t, p)
	seedQuery(t, d)
	for _, h := range []uint64{1, 2, 3, 4, 5} {
		d.Exec("UPDATE media SET thumb_state = 'ok', phash = ? WHERE hash = ?", int64(h*10), int64(h))
	}
	items, err := d.PHashItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 3 is a video; 7 is deleted and has no phash anyway.
	want := []PHashItem{{1, 10}, {2, 20}, {4, 40}, {5, 50}}
	slices.SortFunc(items, func(a, b PHashItem) int { return int(a.Hash) - int(b.Hash) })
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("phash items %v", items)
	}
	c, ok, err := d.Cover(ctx)
	if err != nil || !ok || c != 3 {
		t.Fatalf("cover %d %v %v", c, ok, err)
	}
	if h, ok, _ := d.NewestOf(ctx, []uint64{1, 4, 5}); !ok || h != 1 {
		t.Fatalf("newest of %d %v", h, ok)
	}
	pk, err := PeekFile(ctx, p)
	if err != nil || pk.Summary.Total != 8 || !pk.HasCover || pk.Cover != 3 {
		t.Fatalf("peek %+v %v", pk, err)
	}
	d.Exec("PRAGMA user_version = 2")
	if _, err := PeekFile(ctx, p); !errors.Is(err, ErrNeedsRebuild) {
		t.Fatalf("peek of v2: %v", err)
	}
	if _, err := PeekFile(ctx, filepath.Join(t.TempDir(), "none.db")); err == nil {
		t.Fatal("peek of a missing file")
	}
}
