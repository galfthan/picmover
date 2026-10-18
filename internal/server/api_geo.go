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

// wrapLon maps a longitude into [-180, 180). One already in it is kept
// as it is: the modulo's rounding would move it (24.94 becomes
// 24.939999999999998), and a box made from the bounds of some points
// would then miss the points on its edge.
func wrapLon(lon float64) float64 {
	if lon >= -180 && lon < 180 {
		return lon
	}
	l := math.Mod(lon+180, 360)
	if l < 0 {
		l += 360
	}
	return l - 180
}

// geoQuery parses the filters and the bbox of a geo request.
func geoQuery(q url.Values) (catalog.Filter, userFilter, bbox, error) {
	f, uf, err := parseFilter(q)
	if err != nil {
		return f, uf, bbox{}, err
	}
	raw := q.Get("bbox")
	if raw == "" {
		return f, uf, bbox{}, fmt.Errorf("bbox is required")
	}
	b, err := parseBBox(raw)
	return f, uf, b, err
}

// geoFilter parses and resolves a geo request's filter and box,
// answering 400 or 500 itself when it returns false.
func (s *Server) geoFilter(w http.ResponseWriter, r *http.Request, l *libHandles) (catalog.Filter, bbox, bool) {
	f, uf, b, err := geoQuery(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return f, b, false
	}
	if err := resolveFilter(r.Context(), l, &f, uf); err != nil {
		s.filterError(w, r, err)
		return f, b, false
	}
	return f, b, true
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

func (s *Server) handleGeo(w http.ResponseWriter, r *http.Request, l *libHandles) {
	q := r.URL.Query()
	f, b, ok := s.geoFilter(w, r, l)
	if !ok {
		return
	}
	zoom, err := strconv.Atoi(q.Get("zoom"))
	if err != nil || zoom < 0 || zoom > maxZoom {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("zoom is required: an integer 0-%d", maxZoom))
		return
	}
	pts, err := l.db.GeoPoints(r.Context(), f, b.west, b.south, b.east, b.north, 0)
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
// px, doubled until the box spans at most limit cells, so the output
// stays bounded whatever box and zoom a client sends.
func cellSize(b bbox, world, px float64, limit int) float64 {
	width := b.east - b.west
	if width < 0 {
		width += 360
	}
	wpx := width / 360 * world
	_, ytop := mercator(b.north, 0, world)
	_, ybot := mercator(b.south, 0, world)
	hpx := ybot - ytop
	size := px
	for (wpx/size+1)*(hpx/size+1) > float64(limit) {
		size *= 2
	}
	return size
}

type cellKey struct{ x, y int64 }

// geoCell gathers the points of one cluster cell.
type geoCell struct {
	lat, lon float64 // sums
	n        int
	first    catalog.GeoPoint
	newest   catalog.GeoPoint // latest taken_at, then highest id; undated is oldest
	// The members' bounds. A cell never spans the antimeridian, so plain
	// minima and maxima are right.
	west, south, east, north float64
}

// geoCells buckets the points into grid cells of the web-mercator
// projection at zoom (one pass, one map entry per occupied cell). Cells
// are px pixels wide, coarser while box b would span more than limit
// cells. Cell edges align with the antimeridian (the world width is a
// multiple of the cell size), so no cell spans it and plain means are
// correct.
func geoCells(pts []catalog.GeoPoint, b bbox, zoom int, px float64, limit int) map[cellKey]*geoCell {
	world := tileSize * math.Exp2(float64(zoom))
	size := cellSize(b, world, px, limit)
	cells := make(map[cellKey]*geoCell)
	for _, p := range pts {
		x, y := mercator(p.Lat, p.Lon, world)
		k := cellKey{int64(math.Floor(x / size)), int64(math.Floor(y / size))}
		c := cells[k]
		if c == nil {
			c = &geoCell{first: p, newest: p, west: p.Lon, east: p.Lon, south: p.Lat, north: p.Lat}
			cells[k] = c
		} else {
			if p.TakenAt.After(c.newest.TakenAt) || p.TakenAt.Equal(c.newest.TakenAt) && p.ID > c.newest.ID {
				c.newest = p
			}
			c.west, c.east = min(c.west, p.Lon), max(c.east, p.Lon)
			c.south, c.north = min(c.south, p.Lat), max(c.north, p.Lat)
		}
		c.lat += p.Lat
		c.lon += p.Lon
		c.n++
	}
	return cells
}

// cluster returns one cluster per cell (cellPx pixels at zoom, at most
// maxCells over b) with the mean position, most populated first. A cell
// with one point carries its id and hash.
func cluster(pts []catalog.GeoPoint, b bbox, zoom int) []clusterJSON {
	cells := geoCells(pts, b, zoom, cellPx, maxCells)
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

func (s *Server) handleGeoItems(w http.ResponseWriter, r *http.Request, l *libHandles) {
	q := r.URL.Query()
	f, b, ok := s.geoFilter(w, r, l)
	if !ok {
		return
	}
	limit := geoItemsDflt
	if v := q.Get("limit"); v != "" {
		var err error
		limit, err = strconv.Atoi(v)
		if err != nil || limit < 1 || limit > geoItemsMax {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid limit %q: want 1-%d", v, geoItemsMax))
			return
		}
	}
	rows, total, err := l.db.GeoItems(r.Context(), f, b.west, b.south, b.east, b.north, limit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items, err := itemList(r.Context(), l, rows)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, geoItemsJSON{Items: items, Total: total})
}
