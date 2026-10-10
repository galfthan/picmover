package server

import (
	"cmp"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"picmover/internal/catalog"
)

const (
	maxZoom      = 22
	tileSize     = 256  // web-mercator world size at zoom 0, pixels
	cellPx       = 64   // cluster cell size, pixels
	maxCells     = 4096 // cells over a request's box; larger boxes get coarser cells
	maxMercLat   = 85.05112878
	geoItemsMax  = 1000
	geoItemsDflt = 200
)

// bbox is a validated bounding box: west may be > east (crossing the
// antimeridian); lon in [-180, 180], lat in [-90, 90].
type bbox struct{ west, south, east, north float64 }

// parseBBox parses "w,s,e,n" as Leaflet's getBounds().toBBoxString()
// sends it. Longitudes outside [-180, 180] (a map panned across the
// antimeridian) are wrapped; a box 360 degrees wide or wider is the whole
// world.
func parseBBox(s string) (bbox, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return bbox{}, fmt.Errorf("invalid bbox %q: want west,south,east,north", s)
	}
	var v [4]float64
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return bbox{}, fmt.Errorf("invalid bbox %q: want four numbers", s)
		}
		v[i] = f
	}
	b := bbox{west: v[0], south: v[1], east: v[2], north: v[3]}
	if b.south > b.north || b.south < -90 || b.north > 90 {
		return bbox{}, fmt.Errorf("invalid bbox %q: latitudes must satisfy -90 <= south <= north <= 90", s)
	}
	if b.west > b.east && b.west <= 180 && b.east >= -180 {
		return b, nil // already an antimeridian-crossing box
	}
	if b.east < b.west {
		return bbox{}, fmt.Errorf("invalid bbox %q", s)
	}
	if b.east-b.west >= 360 {
		b.west, b.east = -180, 180
		return b, nil
	}
	b.west, b.east = wrapLon(b.west), wrapLon(b.east)
	switch {
	case b.west == -180 && b.east == -180:
		// A zero-width box on the antimeridian: as a crossing box
		// [180, -180] it matches points stored at either 180 or -180.
		b.west = 180
	case b.east == -180 && b.west > -180:
		b.east = 180 // a box ending exactly on the antimeridian
	}
	return b, nil
}

// wrapLon maps a longitude into [-180, 180).
func wrapLon(lon float64) float64 {
	l := math.Mod(lon+180, 360)
	if l < 0 {
		l += 360
	}
	return l - 180
}

// geoQuery parses the filters and the bbox of a geo request.
func geoQuery(q url.Values) (catalog.Filter, bbox, error) {
	f, err := parseFilter(q)
	if err != nil {
		return f, bbox{}, err
	}
	raw := q.Get("bbox")
	if raw == "" {
		return f, bbox{}, fmt.Errorf("bbox is required")
	}
	b, err := parseBBox(raw)
	return f, b, err
}

type clusterJSON struct {
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
	Count int     `json:"count"`
	ID    int64   `json:"id,omitempty"`   // single point only
	Hash  string  `json:"hash,omitempty"` // single point only
}

type clustersJSON struct {
	Clusters []clusterJSON `json:"clusters"`
}

func (s *Server) handleGeo(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, b, err := geoQuery(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	zoom, err := strconv.Atoi(q.Get("zoom"))
	if err != nil || zoom < 0 || zoom > maxZoom {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("zoom is required: an integer 0-%d", maxZoom))
		return
	}
	pts, err := s.db.GeoPoints(r.Context(), f, b.west, b.south, b.east, b.north, 0)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, clustersJSON{Clusters: cluster(pts, b, zoom)})
}

// mercator projects lat/lon to web-mercator pixel coordinates in a world
// of the given size.
func mercator(lat, lon, world float64) (x, y float64) {
	lat = max(-maxMercLat, min(maxMercLat, lat))
	x = (lon + 180) / 360 * world
	phi := lat * math.Pi / 180
	y = (1 - math.Log(math.Tan(phi)+1/math.Cos(phi))/math.Pi) / 2 * world
	return x, y
}

// cellSize returns the cluster cell size in pixels for box b at zoom:
// cellPx, doubled until the box spans at most maxCells cells, so the
// output stays bounded whatever box and zoom a client sends.
func cellSize(b bbox, world float64) float64 {
	width := b.east - b.west
	if width < 0 {
		width += 360
	}
	wpx := width / 360 * world
	_, ytop := mercator(b.north, 0, world)
	_, ybot := mercator(b.south, 0, world)
	hpx := ybot - ytop
	size := float64(cellPx)
	for (wpx/size+1)*(hpx/size+1) > maxCells {
		size *= 2
	}
	return size
}

type cellKey struct{ x, y int64 }

type cellAcc struct {
	lat, lon float64 // sums
	n        int
	first    catalog.GeoPoint
}

// cluster buckets the points into grid cells of the web-mercator
// projection at zoom (one pass, one map entry per occupied cell) and
// returns one cluster per cell with the mean position, most populated
// first. A cell with one point carries its id and hash. Cell edges align
// with the antimeridian (the world width is a multiple of the cell size),
// so no cell spans it and plain means are correct.
func cluster(pts []catalog.GeoPoint, b bbox, zoom int) []clusterJSON {
	world := tileSize * math.Exp2(float64(zoom))
	size := cellSize(b, world)
	cells := make(map[cellKey]*cellAcc)
	for _, p := range pts {
		x, y := mercator(p.Lat, p.Lon, world)
		k := cellKey{int64(math.Floor(x / size)), int64(math.Floor(y / size))}
		c := cells[k]
		if c == nil {
			c = &cellAcc{first: p}
			cells[k] = c
		}
		c.lat += p.Lat
		c.lon += p.Lon
		c.n++
	}
	out := make([]clusterJSON, 0, len(cells))
	for _, c := range cells {
		cl := clusterJSON{Lat: c.lat / float64(c.n), Lon: c.lon / float64(c.n), Count: c.n}
		if c.n == 1 {
			cl.ID, cl.Hash = c.first.ID, hexHash(c.first.Hash)
		}
		out = append(out, cl)
	}
	slices.SortFunc(out, func(a, b clusterJSON) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Lat, b.Lat), cmp.Compare(a.Lon, b.Lon), cmp.Compare(a.ID, b.ID))
	})
	return out
}

type geoItemsJSON struct {
	Items []itemJSON `json:"items"`
	Total int64      `json:"total"`
}

func (s *Server) handleGeoItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, b, err := geoQuery(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit := geoItemsDflt
	if v := q.Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil || limit < 1 || limit > geoItemsMax {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid limit %q: want 1-%d", v, geoItemsMax))
			return
		}
	}
	items, total, err := s.db.GeoItems(r.Context(), f, b.west, b.south, b.east, b.north, limit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, geoItemsJSON{Items: itemList(items), Total: total})
}
