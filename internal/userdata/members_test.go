package userdata

import (
	"context"
	"slices"
	"testing"
)

func TestMembers(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	beach, _, err := d.CreateTag(ctx, "beach")
	if err != nil {
		t.Fatal(err)
	}
	dog, _, err := d.CreateTag(ctx, "dog")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.CreateTag(ctx, "unused"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.TagMedia(ctx, []uint64{1, 2}, []int64{beach.ID}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.TagMedia(ctx, []uint64{2}, []int64{dog.ID}, nil, nil); err != nil {
		t.Fatal(err)
	}
	a, err := d.CreateAlbum(ctx, "Summer", []uint64{3, 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateAlbum(ctx, "Empty", nil); err != nil {
		t.Fatal(err)
	}
	sortm := func(ms []Member) []Member {
		slices.SortFunc(ms, func(x, y Member) int {
			if x.ID != y.ID {
				return int(x.ID - y.ID)
			}
			return int(x.Hash) - int(y.Hash)
		})
		return ms
	}
	tags, err := d.TagMembers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Member{{beach.ID, 1}, {beach.ID, 2}, {dog.ID, 2}}; !slices.Equal(sortm(tags), want) {
		t.Fatalf("tag members %v, want %v", tags, want)
	}
	albums, err := d.AlbumMembers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Member{{a.ID, 1}, {a.ID, 3}}; !slices.Equal(sortm(albums), want) {
		t.Fatalf("album members %v, want %v", albums, want)
	}
}
