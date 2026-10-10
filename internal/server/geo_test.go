package server

import (
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"picmover/internal/catalog"
)

func TestParseBBox(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bbox
		ok   bool
	}{
		{"-10,-20,30,40", bbox{-10, -20, 30, 40}, true},
		{"170,-10,-170,10", bbox{170, -10, -170, 10}, true},  // crosses the antimeridian
		{"170,-10,190,10", bbox{170, -10, -170, 10}, true},   // Leaflet's unwrapped form
		{"-190,-10,-170,10", bbox{170, -10, -170, 10}, true}, // and to the west
		{"-200,-90,200,90", bbox{-180, -90, 180, 90}, true},  // more than the world
		{"0,0,180,10", bbox{0, 0, 180, 10}, true},            // ends on the antimeridian
		{"190,0,200,10", bbox{-170, 0, -160, 10}, true},      // wholly wrapped
		{" 1 , 2 , 3 , 4 ", bbox{1, 2, 3, 4}, true},
		{"1,2,3", bbox{}, false}, {"1,2,3,4,5", bbox{}, false}, {"a,2,3,4", bbox{}, false},
		{"1,20,3,10", bbox{}, false}, {"1,-91,3,10", bbox{}, false}, {"1,2,3,91", bbox{}, false},
		{"1,NaN,3,4", bbox{}, false}, {"1,2,Inf,4", bbox{}, false}, {"200,0,190,10", bbox{}, false},
	} {
		got, err := parseBBox(c.in)
		if (err == nil) != c.ok || (c.ok && got != c.want) {
			t.Errorf("parseBBox(%q) = %v, %v; want %v ok=%v", c.in, got, err, c.want, c.ok)
		}
	}
}

func pt(id int64, lat, lon float64) catalog.GeoPoint {
	return catalog.GeoPoint{ID: id, Hash: uint64(id) << 40, Lat: lat, Lon: lon}
}

func TestCluster(t *testing.T) {
	world := bbox{-180, -90, 180, 90}
	// Two points 10 m apart merge at zoom 10; a far one stays single.
	got := cluster([]catalog.GeoPoint{pt(1, 60.17, 24.94), pt(2, 60.1701, 24.9401), pt(3, -33.87, 151.21)}, world, 10)
	want := []clusterJSON{
		{Lat: (60.17 + 60.1701) / 2, Lon: (24.94 + 24.9401) / 2, Count: 2},
		{Lat: -33.87, Lon: 151.21, Count: 1, ID: 3, Hash: hexHash(3 << 40)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	// At zoom 20 they are ~60 px apart: separate cells.
	got = cluster([]catalog.GeoPoint{pt(1, 60.17, 24.94), pt(2, 60.1701, 24.9401)}, bbox{24.9395, 60.1698, 24.9406, 60.1703}, 20)
	if len(got) != 2 || got[0].Count != 1 || got[1].Count != 1 || got[0].ID == 0 || got[1].ID == 0 {
		t.Fatalf("zoom 20: %+v", got)
	}
	// Points on both sides of the antimeridian never share a cell.
	got = cluster([]catalog.GeoPoint{pt(1, 0, 179.99), pt(2, 0, -179.99)}, bbox{179, -1, -179, 1}, 0)
	if len(got) != 2 {
		t.Fatalf("antimeridian: %+v", got)
	}
	// Mercator: the equator and Greenwich are the world's centre.
	if x, y := mercator(0, 0, 256); x != 128 || math.Abs(y-128) > 1e-9 {
		t.Fatalf("mercator(0,0) = %v,%v", x, y)
	}
	// The poles are clamped, not infinite.
	if _, y := mercator(90, 0, 256); math.IsInf(y, 0) || math.IsNaN(y) {
		t.Fatal("pole not clamped")
	}
	// Output stays bounded for a world box at the deepest zoom.
	var many []catalog.GeoPoint
	for i := range 20000 {
		many = append(many, pt(int64(i+1), float64(i%170)-85, float64(i%359)-179.5))
	}
	if got := cluster(many, world, maxZoom); len(got) > maxCells {
		t.Fatalf("%d clusters for a world box at zoom %d", len(got), maxZoom)
	}
}

func TestGeoEndpoints(t *testing.T) {
	f := newFixture(t)
	w := f.get("/api/geo?bbox=-180,-90,180,90&zoom=3")
	wantStatus(t, w, http.StatusOK)
	got := decode[clustersJSON](t, w).Clusters
	if len(got) != 2 || got[0].Count != 2 || got[0].ID != 0 || got[0].Hash != "" ||
		math.Abs(got[0].Lat-(latA+latB)/2) > 1e-6 || math.Abs(got[0].Lon-(lonA+lonB)/2) > 1e-6 ||
		got[1].Count != 1 || got[1].ID != f.ids["c.jpg"] || got[1].Hash != hexHash(f.hashes["c.jpg"]) ||
		math.Abs(got[1].Lat-latC) > 1e-6 || math.Abs(got[1].Lon-lonC) > 1e-6 {
		t.Fatalf("clusters %s", w.Body)
	}
	if strings.Contains(w.Body.String(), `"id":0`) {
		t.Fatalf("cluster carries id 0: %s", w.Body)
	}
	// Zoomed into Helsinki: the two photos are separate singles.
	w = f.get("/api/geo?bbox=24.93,60.16,24.95,60.18&zoom=18")
	got = decode[clustersJSON](t, w).Clusters
	if len(got) != 2 || got[0].ID == 0 || got[1].ID == 0 {
		t.Fatalf("zoom 18: %s", w.Body)
	}
	// Antimeridian box around Fiji holds nothing; one around Sydney's
	// longitude written across the antimeridian holds Sydney.
	w = f.get("/api/geo?bbox=170,-60,-170,0&zoom=3")
	if got := decode[clustersJSON](t, w).Clusters; len(got) != 0 {
		t.Fatalf("fiji: %s", w.Body)
	}
	w = f.get("/api/geo?bbox=150,-60,-170,0&zoom=3")
	if got := decode[clustersJSON](t, w).Clusters; len(got) != 1 || got[0].ID != f.ids["c.jpg"] {
		t.Fatalf("antimeridian: %s", w.Body)
	}
	// Filters apply.
	w = f.get("/api/geo?bbox=-180,-90,180,90&zoom=3&camera=Z+6")
	if got := decode[clustersJSON](t, w).Clusters; len(got) != 1 || got[0].ID != f.ids["c.jpg"] {
		t.Fatalf("camera filter: %s", w.Body)
	}
	for _, bad := range []string{"/api/geo?zoom=3", "/api/geo?bbox=-180,-90,180,90", "/api/geo?bbox=-180,-90,180,90&zoom=23",
		"/api/geo?bbox=-180,-90,180,90&zoom=-1", "/api/geo?bbox=1,2,3&zoom=1", "/api/geo?bbox=-180,-90,180,90&zoom=1&type=x"} {
		wantError(t, f.get(bad), http.StatusBadRequest, "")
	}

	w = f.get("/api/geo/items?bbox=-180,-90,180,90")
	wantStatus(t, w, http.StatusOK)
	items := decode[geoItemsJSON](t, w)
	if items.Total != 3 || len(items.Items) != 3 || items.Items[0].ID != f.ids["b.jpg"] ||
		items.Items[1].ID != f.ids["a.jpg"] || items.Items[2].ID != f.ids["c.jpg"] || !items.Items[0].GPS {
		t.Fatalf("geo items %s", w.Body)
	}
	w = f.get("/api/geo/items?bbox=-180,-90,180,90&limit=1")
	items = decode[geoItemsJSON](t, w)
	if items.Total != 3 || len(items.Items) != 1 || items.Items[0].ID != f.ids["b.jpg"] {
		t.Fatalf("geo items limit: %s", w.Body)
	}
	w = f.get("/api/geo/items?bbox=0,0,1,1")
	if w.Body.String() != "{\"items\":[],\"total\":0}\n" {
		t.Fatalf("empty geo items %q", w.Body)
	}
	for _, bad := range []string{"/api/geo/items", "/api/geo/items?bbox=-180,-90,180,90&limit=0",
		"/api/geo/items?bbox=-180,-90,180,90&limit=1001", "/api/geo/items?bbox=-180,-90,180,90&limit=x"} {
		wantError(t, f.get(bad), http.StatusBadRequest, "")
	}
}
