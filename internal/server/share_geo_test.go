package server

import (
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
)

// Positions of the map tests (a.jpg keeps latA, lonA from its EXIF).
var shareGeoPositions = map[string][2]float64{
	"peter/b.jpg": {60.1702, 24.9390}, // Helsinki, next to a
	"peter/v.mp4": {60.1700, 24.9400}, // Helsinki, the newest there
	"peter/t.jpg": {60.1700, 24.9400}, // trashed, a member of Summer
	"peter/c.jpg": {48.8566, 2.3522},  // Paris, in "Other"
	"peter/d.jpg": {-33.87, 151.21},   // Sydney, in no album
	"penny/e.jpg": {40.71, -74.0},
	"other/f.jpg": {35.0, 139.0}, // never shared
}

// geoFixture is the share fixture with positions on most rows.
func newGeoFixture(t *testing.T) *shareFixture {
	t.Helper()
	f := newShareFixture(t)
	byLib := map[string][]string{}
	for n := range shareGeoPositions {
		lib, _, _ := strings.Cut(n, "/")
		byLib[lib] = append(byLib[lib], n)
	}
	for lib, names := range byLib {
		l, err := library.Open(filepath.Join(f.root, lib))
		if err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", l.DBPath())
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			p := shareGeoPositions[n]
			if _, err := db.Exec("UPDATE media SET lat = ?, lon = ? WHERE id = ?", p[0], p[1], f.ids[n]); err != nil {
				t.Fatal(err)
			}
		}
		db.Close()
	}
	return f
}

// geoClusters reads a phone's clusters.
func (f *shareFixture) geoClusters(cookie, lib, query string) []shareClusterJSON {
	f.t.Helper()
	w := f.phone("GET", "/api/l/"+lib+"/geo?"+query, "", cookie)
	wantStatus(f.t, w, http.StatusOK)
	return decode[shareClustersJSON](f.t, w).Clusters
}

// geoHashes is every photo of a box (through geo/items), sorted.
func (f *shareFixture) geoHashes(cookie, lib, query string) ([]string, int64) {
	f.t.Helper()
	w := f.phone("GET", "/api/l/"+lib+"/geo/items?bbox=-180,-90,180,90"+query, "", cookie)
	wantStatus(f.t, w, http.StatusOK)
	r := decode[geoItemsJSON](f.t, w)
	var out []string
	for _, it := range r.Items {
		out = append(out, it.Hash)
	}
	sort.Strings(out)
	return out, r.Total
}

func clusterTotal(cs []shareClusterJSON) int {
	n := 0
	for _, c := range cs {
		n += c.Count
	}
	return n
}

func TestShareGeoLibraryShare(t *testing.T) {
	f := newGeoFixture(t)
	c := f.pair(f.libraryShare().ID, "Anna")
	h := func(n string) string { return hexHash(f.hashes[n]) }

	// The world: every live photo of peter with a position, never the
	// trashed one.
	world := f.geoClusters(c, "peter", "bbox=-180,-90,180,90&zoom=0")
	if n := clusterTotal(world); n != 5 {
		t.Fatalf("world clusters hold %d photos, want 5: %+v", n, world)
	}
	for _, cl := range world {
		if cl.Hash == h("peter/t.jpg") {
			t.Fatalf("trashed photo on the map: %+v", cl)
		}
	}
	// Helsinki at zoom 10: one cluster of a, b, v; its marker is the
	// newest (v), its bounds hold the three.
	hel := f.geoClusters(c, "peter", "bbox=24.5,60,25.5,60.4&zoom=10")
	if len(hel) != 1 || hel[0].Count != 3 || hel[0].Hash != h("peter/v.mp4") || hel[0].ID != 0 || hel[0].Bounds == nil {
		t.Fatalf("Helsinki %+v", hel)
	}
	b := *hel[0].Bounds
	if b[0] > 24.9384 || b[2] < 24.9400 || b[1] > 60.1699 || b[3] < 60.1702 || b[0] > b[2] || b[1] > b[3] {
		t.Fatalf("bounds %v", b)
	}
	// Zoomed in fully: singles with their ids.
	singles := f.geoClusters(c, "peter", "bbox=24.937,60.169,24.941,60.171&zoom=22")
	if len(singles) != 3 {
		t.Fatalf("zoom 22: %+v", singles)
	}
	for _, cl := range singles {
		if cl.Count != 1 || cl.ID == 0 || cl.Bounds != nil || cl.Hash == "" {
			t.Fatalf("single %+v", cl)
		}
	}
	// The cluster's bounds as a box: exactly its photos, newest first.
	w := f.phone("GET", fmt.Sprintf("/api/l/peter/geo/items?bbox=%v,%v,%v,%v", b[0], b[1], b[2], b[3]), "", c)
	wantStatus(t, w, http.StatusOK)
	items := decode[geoItemsJSON](t, w)
	if items.Total != 3 || len(items.Items) != 3 || items.Items[0].Hash != h("peter/v.mp4") ||
		items.Items[1].Hash != h("peter/b.jpg") || items.Items[2].Hash != h("peter/a.jpg") ||
		!items.Items[2].GPS || items.Items[2].Rating != 4 {
		t.Fatalf("cluster items %s", w.Body)
	}

	// The phone's filters apply, as on the timeline.
	for _, c2 := range []struct {
		q    string
		want []string
	}{
		{"", []string{"peter/a.jpg", "peter/b.jpg", "peter/c.jpg", "peter/d.jpg", "peter/v.mp4"}},
		{"&type=video", []string{"peter/v.mp4"}},
		{"&rating=4", []string{"peter/a.jpg"}},
		{"&camera=EOS+R5", []string{"peter/a.jpg", "peter/b.jpg", "peter/c.jpg", "peter/d.jpg"}},
		{fmt.Sprintf("&album=%d", f.albums["peter/Summer"]), []string{"peter/a.jpg", "peter/b.jpg", "peter/v.mp4"}},
		{fmt.Sprintf("&album=%d", f.albums["peter/Other"]), []string{"peter/c.jpg"}},
		{"&from=2024-07-12&to=2024-08-01", []string{"peter/c.jpg", "peter/d.jpg", "peter/v.mp4"}},
		{"&months=8", nil},
		{"&gps=1", []string{"peter/a.jpg", "peter/b.jpg", "peter/c.jpg", "peter/d.jpg", "peter/v.mp4"}},
		{"&minpx=60&maxpx=70", []string{"peter/a.jpg", "peter/b.jpg", "peter/c.jpg", "peter/d.jpg"}},
	} {
		got, total := f.geoHashes(c, "peter", c2.q)
		if want := f.hexes(c2.want...); !slices.Equal(got, want) || total != int64(len(want)) {
			t.Fatalf("filter %q: %v (%d), want %v", c2.q, got, total, want)
		}
		if n := clusterTotal(f.geoClusters(c, "peter", "bbox=-180,-90,180,90&zoom=3"+c2.q)); n != len(c2.want) {
			t.Fatalf("filter %q: clusters hold %d, want %d", c2.q, n, len(c2.want))
		}
	}
	// penny shows its own photo; an unshared library and an album of
	// another library are 404.
	if got, _ := f.geoHashes(c, "penny", ""); !slices.Equal(got, f.hexes("penny/e.jpg")) {
		t.Fatalf("penny %v", got)
	}
	wantStatus(t, f.phone("GET", "/api/l/other/geo?bbox=-180,-90,180,90&zoom=0", "", c), http.StatusNotFound)
	wantStatus(t, f.phone("GET", "/api/l/other/geo/items?bbox=-180,-90,180,90", "", c), http.StatusNotFound)
	wantStatus(t, f.phone("GET", fmt.Sprintf("/api/l/penny/geo?bbox=-180,-90,180,90&zoom=0&album=%d",
		f.albums["peter/Summer"]), "", c), http.StatusNotFound)

	// The whitelist: anything else is 400, so are a missing or bad box,
	// zoom or limit.
	for _, q := range []string{"zoom=1", "bbox=-180,-90,180,90", "bbox=1,2,3&zoom=1", "bbox=-180,-90,180,90&zoom=23",
		"bbox=-180,-90,180,90&zoom=x", "bbox=-180,-90,180,90&zoom=1&trashed=1", "bbox=-180,-90,180,90&zoom=1&import=1",
		"bbox=-180,-90,180,90&zoom=1&session=1", "bbox=-180,-90,180,90&zoom=1&foo=1", "bbox=-180,-90,180,90&zoom=1&year=2024",
		"bbox=-180,-90,180,90&zoom=1&limit=5", "bbox=-180,-90,180,90&zoom=1&type=raw"} {
		wantStatus(t, f.phone("GET", "/api/l/peter/geo?"+q, "", c), http.StatusBadRequest)
	}
	for _, q := range []string{"", "bbox=1,2", "bbox=-180,-90,180,90&limit=0", "bbox=-180,-90,180,90&limit=501",
		"bbox=-180,-90,180,90&limit=x", "bbox=-180,-90,180,90&zoom=3", "bbox=-180,-90,180,90&trashed=1"} {
		wantStatus(t, f.phone("GET", "/api/l/peter/geo/items?"+q, "", c), http.StatusBadRequest)
	}
	// No path or folder of the computer in any answer.
	for _, p := range []string{"/api/l/peter/geo?bbox=-180,-90,180,90&zoom=0", "/api/l/peter/geo/items?bbox=-180,-90,180,90"} {
		if body := f.phone("GET", p, "", c).Body.String(); strings.Contains(body, f.root) ||
			strings.Contains(body, "relPath") || strings.Contains(body, "originalPath") || strings.Contains(body, ".jpg") {
			t.Fatalf("%s leaks: %s", p, body)
		}
	}
}

func TestShareGeoAlbumShare(t *testing.T) {
	f := newGeoFixture(t)
	h := func(n string) string { return hexHash(f.hashes[n]) }
	summer := f.albums["peter/Summer"]
	sh := f.createShare(fmt.Sprintf(`{"name": "Guests", "scope": {"kind": "album", "library": "peter", "album": %d}}`, summer))
	c := f.pair(sh.ID, "Mia")
	// The album share default has no location: no map.
	for _, p := range []string{"/api/l/album/geo?bbox=-180,-90,180,90&zoom=0", "/api/l/album/geo/items?bbox=-180,-90,180,90"} {
		wantError(t, f.phone("GET", p, "", c), http.StatusForbidden, errNotAllowed)
	}
	f.update(sh.ID, `{"capabilities": {"video": true, "download": false, "rate": false, "location": true}}`)

	// Only the album's live members: a, b, v (t is trashed; c, d are not members).
	world := f.geoClusters(c, "album", "bbox=-180,-90,180,90&zoom=0")
	if len(world) != 1 || world[0].Count != 3 || world[0].Hash != h("peter/v.mp4") {
		t.Fatalf("album world %+v", world)
	}
	got, total := f.geoHashes(c, "album", "")
	if want := f.hexes("peter/a.jpg", "peter/b.jpg", "peter/v.mp4"); !slices.Equal(got, want) || total != 3 {
		t.Fatalf("album items %v %d", got, total)
	}
	// Redacted as the album's list: no rating without rate, gps with location.
	w := f.phone("GET", "/api/l/album/geo/items?bbox=-180,-90,180,90", "", c)
	if body := w.Body.String(); strings.Contains(body, `"rating"`) || !strings.Contains(body, `"gps":true`) ||
		strings.Contains(body, "peter") || strings.Contains(body, `"albums"`) {
		t.Fatalf("album items leak: %s", body)
	}
	// The scope's keys and filters: the folder name is 404; album and tag are 400.
	wantStatus(t, f.phone("GET", "/api/l/peter/geo?bbox=-180,-90,180,90&zoom=0", "", c), http.StatusNotFound)
	wantStatus(t, f.phone("GET", fmt.Sprintf("/api/l/album/geo?bbox=-180,-90,180,90&zoom=0&album=%d", f.albums["peter/Other"]), "", c),
		http.StatusBadRequest)
	wantStatus(t, f.phone("GET", "/api/l/album/geo/items?bbox=-180,-90,180,90&tag=1", "", c), http.StatusBadRequest)
	wantStatus(t, f.phone("GET", "/api/l/album/geo?bbox=-180,-90,180,90&zoom=0&rating=1", "", c), http.StatusBadRequest)
	// Boxes around photos outside the album find nothing.
	for _, box := range []string{"2,48,3,49", "151,-34,152,-33", "-75,40,-73,41"} {
		if cs := f.geoClusters(c, "album", "bbox="+box+"&zoom=8"); len(cs) != 0 {
			t.Fatalf("box %s: %+v", box, cs)
		}
	}
	// A photo taken out of the album leaves the map at once.
	wantStatus(t, f.admin(http.MethodPost, fmt.Sprintf("/api/l/peter/albums/%d/items", summer),
		fmt.Sprintf(`{"remove": [%q]}`, h("peter/b.jpg"))), http.StatusOK)
	if got, _ := f.geoHashes(c, "album", ""); !slices.Equal(got, f.hexes("peter/a.jpg", "peter/v.mp4")) {
		t.Fatalf("after removal %v", got)
	}
	// Location turned off: 403 at the next request.
	f.update(sh.ID, `{"capabilities": {"video": true, "download": false, "rate": false, "location": false}}`)
	wantError(t, f.phone("GET", "/api/l/album/geo?bbox=-180,-90,180,90&zoom=0", "", c), http.StatusForbidden, errNotAllowed)
}

func TestShareClusterBounded(t *testing.T) {
	world := bbox{-180, -90, 180, 90}
	var many []catalog.GeoPoint
	for i := range 20000 {
		many = append(many, catalog.GeoPoint{ID: int64(i + 1), Hash: uint64(i+1) << 20,
			Lat: float64(i%170) - 85, Lon: float64(i%359) - 179.5})
	}
	for _, z := range []int{0, 5, 12, maxZoom} {
		got := shareCluster(many, world, z)
		if len(got) > shareMaxCells {
			t.Fatalf("zoom %d: %d clusters", z, len(got))
		}
		if n := clusterTotal(got); n != len(many) {
			t.Fatalf("zoom %d: clusters hold %d of %d", z, n, len(many))
		}
	}
	// The newest photo represents a cluster; undated is the oldest; ties
	// go to the higher id. Bounds hold every member.
	day := func(d int) time.Time { return time.Date(2024, 7, d, 0, 0, 0, 0, time.UTC) }
	pts := []catalog.GeoPoint{
		{ID: 1, Hash: 1, Lat: 60.17, Lon: 24.94, TakenAt: day(3)},
		{ID: 2, Hash: 2, Lat: 60.171, Lon: 24.941, TakenAt: day(5), ThumbAt: time.Unix(1700000000, 0)},
		{ID: 3, Hash: 3, Lat: 60.169, Lon: 24.939},
		{ID: 4, Hash: 4, Lat: 60.1705, Lon: 24.9405, TakenAt: day(5)},
	}
	got := shareCluster(pts, world, 8)
	if len(got) != 1 || got[0].Count != 4 || got[0].Hash != hexHash(4) || got[0].ThumbAt != 0 || got[0].ID != 0 ||
		*got[0].Bounds != [4]float64{24.939, 60.169, 24.941, 60.171} {
		t.Fatalf("cluster %+v", got)
	}
	got = shareCluster(pts[1:2], world, 8)
	if len(got) != 1 || got[0].ID != 2 || got[0].ThumbAt != 1700000000 || got[0].Bounds != nil ||
		math.Abs(got[0].Lat-60.171) > 1e-12 {
		t.Fatalf("single %+v", got)
	}
	// A cluster's bounds sent back as a box keep their exact edges.
	if b, err := parseBBox("24.9384,60.1699,24.94,60.1702"); err != nil || b != (bbox{24.9384, 60.1699, 24.94, 60.1702}) {
		t.Fatalf("bounds as a box: %+v %v", b, err)
	}
}

// The phone's page may load images from exactly one outside host: the
// map tiles.
func TestShareCSPTileHost(t *testing.T) {
	var img string
	for _, d := range strings.Split(shareCSP, ";") {
		d = strings.TrimSpace(d)
		if v, ok := strings.CutPrefix(d, "img-src "); ok {
			img = v
		} else if strings.Contains(d, "http") {
			t.Fatalf("an outside host in %q", d)
		}
	}
	if img != "'self' data: blob: https://tile.openstreetmap.org" {
		t.Fatalf("img-src %q", img)
	}
}
