package server

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"picmover/internal/catalog"
	"picmover/internal/similar"
)

// Item 13: album count and cover consider only live members.
func TestAlbumCountCoverExcludeTrashed(t *testing.T) {
	f := newFixture(t)
	a, b := hexHash(f.hashes["a.jpg"]), hexHash(f.hashes["b.jpg"])
	w := f.post(lp+"albums", `{"name":"One","hashes":["`+a+`"]}`)
	wantStatus(t, w, http.StatusCreated)
	one := decode[albumJSON](t, w)
	w = f.post(lp+"albums", `{"name":"Two","hashes":["`+a+`","`+b+`"]}`)
	two := decode[albumJSON](t, w)
	wantStatus(t, f.post(lp+"albums/"+itoa64(two.ID), `{"cover":"`+a+`"}`), http.StatusOK)
	f.post(lp+"trash", `{"hashes":["`+a+`"]}`)

	albums := decode[albumsJSON](t, f.get(lp+"albums")).Albums
	byID := map[int64]albumJSON{}
	for _, al := range albums {
		byID[al.ID] = al
	}
	if al := byID[one.ID]; al.Count != 0 || al.Cover != nil {
		t.Fatalf("album with its only item trashed %+v", al)
	}
	if al := byID[two.ID]; al.Count != 1 || al.Cover == nil || *al.Cover != b {
		t.Fatalf("album with a trashed explicit cover %+v", al)
	}
	w = f.post(lp+"albums/"+itoa64(two.ID)+"/items", `{"add":["`+hexHash(f.hashes["c.jpg"])+`"]}`)
	if c := decode[countJSON](t, w); c.Count != 2 {
		t.Fatalf("items count %+v", c)
	}
}

// Item 14: {"group": hash} ignores the stored pairs of the current group;
// a 300-item group disappears with one call.
func TestSimilarIgnoreGroup(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	items := make([]catalog.PHashItem, 300)
	for i := range items {
		h := uint64(0x5000 + i)
		f.insertRow(fmt.Sprintf("s%d", i), catalog.Media{Hash: h, FileType: "image",
			RelPath: fmt.Sprintf("image/2020/01/s%d.jpg", i)})
		items[i] = catalog.PHashItem{Hash: h, PHash: 0xfedcba9876543210}
	}
	pairs, _, err := similar.Find(items, similar.DefaultDistance, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.ReplaceSimilarPairs(ctx, pairs, map[string]string{catalog.MetaSimilarDistance: "6"}); err != nil {
		t.Fatal(err)
	}
	g := decode[similarGroupsJSON](t, f.get(lp+"similar"))
	if g.Total != 1 || len(g.Groups[0].Items) != 300 {
		t.Fatalf("groups total %d", g.Total)
	}
	wantError(t, f.post(lp+"similar/ignore", `{"group":"`+hexHash(f.hashes["a.jpg"])+`"}`), http.StatusNotFound, "no group")
	wantError(t, f.post(lp+"similar/ignore", `{"group":"`+hexHash(0x5000)+`","hashes":["1","2"]}`), http.StatusBadRequest, "either")

	// The hashes form stores only existing pairs among the hashes.
	w := f.post(lp+"similar/ignore", `{"hashes":["`+hexHash(0x5000)+`","`+hexHash(0x5001)+`","`+hexHash(f.hashes["a.jpg"])+`"]}`)
	if n := decode[ignoredJSON](t, w).Ignored; n != 1 {
		t.Fatalf("hashes form ignored %d", n)
	}
	w = f.post(lp+"similar/ignore", `{"group":"`+hexHash(0x5000+150)+`"}`)
	wantStatus(t, w, http.StatusOK)
	if n := decode[ignoredJSON](t, w).Ignored; n != int64(len(pairs)-1) {
		t.Fatalf("group ignored %d pairs, want %d", n, len(pairs)-1)
	}
	if g := decode[similarGroupsJSON](t, f.get(lp+"similar")); g.Total != 0 {
		t.Fatalf("groups after ignoring the group: %d", g.Total)
	}
}
