package server

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"picmover/internal/catalog"
)

// The phone's map (docs/design/lan-share.md "Map on the phone"): the
// photos with a position inside the share's scope, clustered on the
// server so that a library of 200 000 photos answers in a bounded number
// of clusters. Both routes need the `location` capability (403 without):
// positions are what that capability grants. They take the phone's
// filter whitelist (shareFilter: the same scope restriction as the
// timeline), plus the box and the zoom or the limit.

const (
	// shareCellPx is the phone's cluster cell: a 48 px thumbnail marker
	// with room around it.
	shareCellPx = 80
	// shareMaxCells bounds a phone's answer: a phone screen holds about
	// a hundred cells; a larger box gets coarser cells, never more.
	shareMaxCells = 1024
	// The bottom sheet's items: at most this many per request.
	shareGeoItemsMax  = 500
	shareGeoItemsDflt = 200
)

// shareClusterJSON is a cluster of the phone's map. Every cluster has a
// representative: its newest photo (hash, for the thumbnail marker). A
// single photo carries its id (to open it); a cluster of several carries
// the bounds of its members as [west, south, east, north] (to zoom to
// them and to ask for their items).
type shareClusterJSON struct {
	Lat     float64     `json:"lat"`
	Lon     float64     `json:"lon"`
	Count   int         `json:"count"`
	Hash    string      `json:"hash"`
	ThumbAt int64       `json:"thumbAt,omitempty"`
	ID      int64       `json:"id,omitempty"`
	Bounds  *[4]float64 `json:"bounds,omitempty"`
}

type shareClustersJSON struct {
	Clusters []shareClusterJSON `json:"clusters"`
}

// shareGeoAllowed answers 403 when the share has no `location`.
func shareGeoAllowed(w http.ResponseWriter, req *shareReq) bool {
	if !req.sh.Capabilities.Location {
		writeError(w, http.StatusForbidden, errNotAllowed)
		return false
	}
	return true
}

// shareGeoQuery is a geo request's scope-restricted filter and box. It
// answers itself when it returns false.
func (s *Server) shareGeoQuery(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles, extra string) (catalog.Filter, bbox, bool) {
	if !shareGeoAllowed(w, req) {
		return catalog.Filter{}, bbox{}, false
	}
	f, ok := s.shareFilter(w, r, req, l, "bbox", extra)
	if !ok {
		return f, bbox{}, false
	}
	raw := r.URL.Query().Get("bbox")
	if raw == "" {
		writeError(w, http.StatusBadRequest, "bbox is required")
		return f, bbox{}, false
	}
	b, err := parseBBox(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return f, bbox{}, false
	}
	return f, b, true
}

// handleShareGeo is GET /api/l/{lib}/geo?bbox=w,s,e,n&zoom=z[&filters]:
// the clusters of the scope's photos with a position inside the box.
func (s *Server) handleShareGeo(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles) {
	f, b, ok := s.shareGeoQuery(w, r, req, l, "zoom")
	if !ok {
		return
	}
	zoom, err := strconv.Atoi(r.URL.Query().Get("zoom"))
	if err != nil || zoom < 0 || zoom > maxZoom {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("zoom is required: an integer 0-%d", maxZoom))
		return
	}
	pts, err := l.db.GeoPoints(r.Context(), f, b.west, b.south, b.east, b.north, 0)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, shareClustersJSON{Clusters: shareCluster(pts, b, zoom)})
}

// shareCluster clusters the points for a phone: shareCellPx cells, at
// most shareMaxCells of them, most populated first.
func shareCluster(pts []catalog.GeoPoint, b bbox, zoom int) []shareClusterJSON {
	cells := geoCells(pts, b, zoom, shareCellPx, shareMaxCells)
	out := make([]shareClusterJSON, 0, len(cells))
	for _, c := range cells {
		cl := shareClusterJSON{Lat: c.lat / float64(c.n), Lon: c.lon / float64(c.n), Count: c.n,
			Hash: hexHash(c.newest.Hash), ThumbAt: unixOrZero(c.newest.ThumbAt)}
		if c.n == 1 {
			cl.ID = c.newest.ID
		} else {
			cl.Bounds = &[4]float64{c.west, c.south, c.east, c.north}
		}
		out = append(out, cl)
	}
	slices.SortFunc(out, func(a, b shareClusterJSON) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Lat, b.Lat), cmp.Compare(a.Lon, b.Lon), cmp.Compare(a.Hash, b.Hash))
	})
	return out
}

// handleShareGeoItems is GET /api/l/{lib}/geo/items?bbox=w,s,e,n[&limit][&filters]:
// up to limit (default 200, at most 500) of the scope's photos with a
// position inside the box, newest first, and how many there are; the
// items are redacted as the media list's.
func (s *Server) handleShareGeoItems(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles) {
	f, b, ok := s.shareGeoQuery(w, r, req, l, "limit")
	if !ok {
		return
	}
	limit := shareGeoItemsDflt
	if v := r.URL.Query().Get("limit"); v != "" {
		var err error
		limit, err = strconv.Atoi(v)
		if err != nil || limit < 1 || limit > shareGeoItemsMax {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid limit %q: want 1-%d", v, shareGeoItemsMax))
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
	shareRedactItems(items, req)
	writeJSON(w, http.StatusOK, geoItemsJSON{Items: items, Total: total})
}
