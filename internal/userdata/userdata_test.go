package userdata

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	d, err := Open(context.Background(), filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestOpenSchemaAndVersion(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), FileName)
	d, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	var v int
	if err := d.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != SchemaVersion {
		t.Fatalf("user_version %d %v", v, err)
	}
	var fk int
	if err := d.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys %d %v", fk, err)
	}
	if _, err := d.SetRating(ctx, []uint64{1}, 3); err != nil {
		t.Fatal(err)
	}
	d.Close()

	// Reopen keeps the data.
	d, err = Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := d.Rating(ctx, 1); err != nil || r != 3 {
		t.Fatalf("rating after reopen %d %v", r, err)
	}
	if _, err := d.db.Exec("PRAGMA user_version = 3"); err != nil {
		t.Fatal(err)
	}
	d.Close()
	if _, err := Open(ctx, p); err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "version 3") {
		t.Fatalf("version 3 not refused with the file name: %v", err)
	}

	// A version-0 database with foreign tables is refused too.
	q := filepath.Join(t.TempDir(), FileName)
	raw, err := sql.Open("sqlite", q)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("CREATE TABLE x (y)"); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	if _, err := Open(ctx, q); err == nil || !strings.Contains(err.Error(), q) {
		t.Fatalf("foreign database not refused: %v", err)
	}
	if _, err := Open(ctx, "a?b"); err == nil {
		t.Fatal("path with ? accepted")
	}
}

func TestRatings(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	big := uint64(0xfedcba9876543210) // negative as int64
	n, err := d.SetRating(ctx, []uint64{1, 2, big, 2}, 4)
	if err != nil || n != 3 {
		t.Fatalf("set: %d %v", n, err)
	}
	n, err = d.SetRating(ctx, []uint64{1, 2}, 4) // unchanged
	if err != nil || n != 0 {
		t.Fatalf("unchanged set: %d %v", n, err)
	}
	if n, err = d.SetRating(ctx, []uint64{2}, 5); err != nil || n != 1 {
		t.Fatalf("change: %d %v", n, err)
	}
	got, err := d.Ratings(ctx, []uint64{1, 2, 3, big})
	if err != nil || !reflect.DeepEqual(got, map[uint64]int{1: 4, 2: 5, big: 4}) {
		t.Fatalf("ratings %v %v", got, err)
	}
	hs, err := d.HashesWithRating(ctx, 5)
	if err != nil || !reflect.DeepEqual(hs, []uint64{2}) {
		t.Fatalf("with rating 5: %v %v", hs, err)
	}
	if n, err = d.SetRating(ctx, []uint64{1, big}, 0); err != nil || n != 2 {
		t.Fatalf("clear: %d %v", n, err)
	}
	if r, _ := d.Rating(ctx, 1); r != 0 {
		t.Fatalf("cleared rating %d", r)
	}
	for _, bad := range []int{-1, 6} {
		if _, err := d.SetRating(ctx, []uint64{1}, bad); !errors.Is(err, ErrInvalidRating) {
			t.Fatalf("rating %d: %v", bad, err)
		}
	}
	if _, err := d.HashesWithRating(ctx, 0); !errors.Is(err, ErrInvalidRating) {
		t.Fatal("min 0 accepted")
	}
}

func TestTags(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	beach, created, err := d.CreateTag(ctx, "  Beach ")
	if err != nil || !created || beach.Name != "Beach" {
		t.Fatalf("create %+v %v %v", beach, created, err)
	}
	again, created, err := d.CreateTag(ctx, "BEACH")
	if err != nil || created || again.ID != beach.ID || again.Name != "Beach" {
		t.Fatalf("NOCASE duplicate: %+v %v %v", again, created, err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", 65), "a\nb", "a\x00b"} {
		if _, _, err := d.CreateTag(ctx, bad); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("name %q: %v", bad, err)
		}
	}
	if _, _, err := d.CreateTag(ctx, strings.Repeat("ä", 64)); err != nil {
		t.Fatalf("64 runes refused: %v", err)
	}
	cat, _, _ := d.CreateTag(ctx, "cat")

	added, err := d.TagMedia(ctx, []uint64{1, 2, 3}, []int64{beach.ID}, nil, []string{"Dog", "beach"})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 2 || added[0].ID != beach.ID || added[1].Name != "Dog" || added[0].Count != 3 {
		t.Fatalf("added %+v", added)
	}
	dog := added[1]
	if _, err := d.TagMedia(ctx, []uint64{2}, []int64{cat.ID}, []int64{beach.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.TagMedia(ctx, []uint64{1}, []int64{999}, nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown tag: %v", err)
	}
	tags, err := d.Tags(ctx)
	want := []Tag{{ID: beach.ID, Name: "Beach", Count: 2}, {ID: cat.ID, Name: "cat", Count: 1}, {ID: dog.ID, Name: "Dog", Count: 3}}
	if err != nil || !reflect.DeepEqual(tags[:3], want) {
		t.Fatalf("tags %+v %v", tags, err)
	}
	of, err := d.TagsOf(ctx, 2)
	if err != nil || len(of) != 2 || of[0].Name != "cat" || of[1].Name != "Dog" {
		t.Fatalf("tags of 2: %+v %v", of, err)
	}
	hs, _ := d.HashesWithTag(ctx, beach.ID)
	slices.Sort(hs)
	if !reflect.DeepEqual(hs, []uint64{1, 3}) {
		t.Fatalf("with beach %v", hs)
	}
	hs, _ = d.HashesWithTags(ctx, []int64{dog.ID, cat.ID, cat.ID})
	if !reflect.DeepEqual(hs, []uint64{2}) {
		t.Fatalf("with dog AND cat %v", hs)
	}

	// Rename: conflict, case change, unknown.
	if _, err := d.RenameTag(ctx, cat.ID, "DOG"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("rename conflict: %v", err)
	}
	if r, err := d.RenameTag(ctx, cat.ID, "Cat"); err != nil || r.Name != "Cat" || r.Count != 1 {
		t.Fatalf("case rename: %+v %v", r, err)
	}
	if _, err := d.RenameTag(ctx, 999, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rename unknown: %v", err)
	}

	// Delete cascades to memberships.
	removed, err := d.DeleteTag(ctx, dog.ID)
	if err != nil || removed != 3 {
		t.Fatalf("delete: %d %v", removed, err)
	}
	var n int
	d.db.QueryRow("SELECT COUNT(*) FROM media_tags WHERE tag_id = ?", dog.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("%d memberships survived the delete", n)
	}
	if _, err := d.DeleteTag(ctx, dog.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestAlbums(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	clock := time.Unix(1000, 0)
	d.now = func() time.Time { return clock }

	a, err := d.CreateAlbum(ctx, " Summer ", []uint64{10, 20, 10})
	if err != nil || a.Name != "Summer" || a.Count != 2 || a.Cover != 20 || a.CoverSet {
		t.Fatalf("create %+v %v", a, err)
	}
	if _, err := d.CreateAlbum(ctx, strings.Repeat("x", 129), nil); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("long name: %v", err)
	}
	clock = clock.Add(time.Minute)
	b, _ := d.CreateAlbum(ctx, "Summer", nil) // names need not be unique
	if b.Cover != 0 || b.Count != 0 {
		t.Fatalf("empty album %+v", b)
	}

	// Positions are max+1 in the order added; members keep theirs.
	clock = clock.Add(time.Minute)
	n, err := d.AlbumAdd(ctx, a.ID, []uint64{30, 10, 40})
	if err != nil || n != 4 {
		t.Fatalf("add %d %v", n, err)
	}
	hs, _ := d.AlbumHashes(ctx, a.ID)
	if !reflect.DeepEqual(hs, []uint64{10, 20, 30, 40}) {
		t.Fatalf("order %v", hs)
	}
	var pos []int64
	rows, _ := d.db.Query("SELECT position FROM album_items WHERE album_id = ? ORDER BY position", a.ID)
	for rows.Next() {
		var p int64
		rows.Scan(&p)
		pos = append(pos, p)
	}
	rows.Close()
	if !reflect.DeepEqual(pos, []int64{1, 2, 3, 4}) {
		t.Fatalf("positions %v", pos)
	}

	// Cover: fallback to the last added, explicit, explicit removed.
	got, _ := d.Album(ctx, a.ID)
	if got.Cover != 40 || got.CoverSet || !got.UpdatedAt.Equal(clock) {
		t.Fatalf("fallback cover %+v", got)
	}
	cover := uint64(20)
	got, err = d.UpdateAlbum(ctx, a.ID, AlbumUpdate{Cover: &cover})
	if err != nil || got.Cover != 20 || !got.CoverSet {
		t.Fatalf("explicit cover %+v %v", got, err)
	}
	if n, err := d.AlbumRemove(ctx, a.ID, []uint64{20, 40}); err != nil || n != 2 {
		t.Fatalf("remove %d %v", n, err)
	}
	got, _ = d.Album(ctx, a.ID)
	if got.Cover != 30 || got.CoverSet {
		t.Fatalf("cover after removing it %+v", got)
	}
	name := "Winter"
	got, err = d.UpdateAlbum(ctx, a.ID, AlbumUpdate{Name: &name, ClearCover: true})
	if err != nil || got.Name != "Winter" {
		t.Fatalf("rename %+v %v", got, err)
	}

	// List by updated_at desc: a was updated last.
	list, err := d.Albums(ctx)
	if err != nil || len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Fatalf("albums %+v %v", list, err)
	}
	refs, _ := d.AlbumsOf(ctx, 10)
	if !reflect.DeepEqual(refs, []AlbumRef{{a.ID, "Winter"}}) {
		t.Fatalf("albums of 10 %+v", refs)
	}

	if err := d.DeleteAlbum(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Album(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted album: %v", err)
	}
	var left int
	d.db.QueryRow("SELECT COUNT(*) FROM album_items").Scan(&left)
	if left != 0 {
		t.Fatalf("%d items survived the album", left)
	}
	for _, err := range []error{
		d.DeleteAlbum(ctx, a.ID),
		func() error { _, err := d.AlbumAdd(ctx, a.ID, []uint64{1}); return err }(),
		func() error { _, err := d.UpdateAlbum(ctx, a.ID, AlbumUpdate{}); return err }(),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown album: %v", err)
		}
	}
}

func TestTrashAndIgnored(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	at := time.Unix(1700000000, 0)
	big := uint64(1 << 63)
	if err := d.Trash(ctx, []uint64{1, big}, at); err != nil {
		t.Fatal(err)
	}
	if err := d.Trash(ctx, []uint64{1}, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := d.TrashedAt(ctx, []uint64{1, 2, big})
	if err != nil || len(got) != 2 || !got[1].Equal(at.Add(time.Hour)) || !got[big].Equal(at) {
		t.Fatalf("trashed at %v %v", got, err)
	}
	all, _ := d.TrashedAt(ctx, nil)
	if len(all) != 2 {
		t.Fatalf("all trashed %v", all)
	}
	// Ratings and tags are untouched by the trash.
	d.SetRating(ctx, []uint64{1}, 2)
	if err := d.Untrash(ctx, []uint64{1}); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.TrashedAt(ctx, nil); len(got) != 1 {
		t.Fatalf("after untrash %v", got)
	}
	if r, _ := d.Rating(ctx, 1); r != 2 {
		t.Fatal("rating lost")
	}

	// Ignored pairs are normalised a < b as int64.
	n, err := d.Ignore(ctx, []Pair{{5, 3}, {3, 5}, {big, 1}, {7, 7}})
	if err != nil || n != 2 {
		t.Fatalf("ignore %d %v", n, err)
	}
	ign, err := d.Ignored(ctx)
	want := map[Pair]struct{}{{3, 5}: {}, {big, 1}: {}}
	if err != nil || !reflect.DeepEqual(ign, want) {
		t.Fatalf("ignored %v %v", ign, err)
	}
	if NewPair(1, big) != (Pair{big, 1}) {
		t.Fatal("NewPair does not order as int64")
	}
	c, err := d.Counts(ctx)
	if err != nil || c != (Counts{Ratings: 1, Trashed: 1, IgnoredPairs: 2}) {
		t.Fatalf("counts %+v %v", c, err)
	}
}
