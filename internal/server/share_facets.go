package server

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"picmover/internal/catalog"
	"picmover/internal/userdata"
)

// The phone's filter sheet asks GET /api/l/{lib}/facets for what it can
// offer (docs/design/lan-share.md "Filters on the phone"). Every list is
// computed inside the share's scope, through the same restrict as the
// timeline: an album share sees only values that occur among the album's
// live members, a library share only its library's live photos. Nothing
// is listed that a list request could not find.

// Facet list bounds: the sheet is for picking, not for browsing.
const (
	maxFacetCameras = 100
	maxFacetTags    = 300
	maxFacetAlbums  = 300
)

// shareFacetsJSON is the answer of GET /api/l/{lib}/facets.
type shareFacetsJSON struct {
	Total   int64           `json:"total"`
	Types   shareTypesJSON  `json:"types"`
	Years   []shareYearJSON `json:"years"`
	Undated int64           `json:"undated"`
	Cameras []cameraJSON    `json:"cameras"`
	// NoCamera counts the photos without a camera model (the `nocamera` filter).
	NoCamera int64 `json:"nocamera"`
	// GPS needs location.
	GPS *int64 `json:"gps,omitempty"`
	// Tags and Albums are sent where the scope covers whole libraries.
	Tags   *[]shareFacetJSON `json:"tags,omitempty"`
	Albums *[]shareFacetJSON `json:"albums,omitempty"`
}

type shareTypesJSON struct {
	Image int64 `json:"image"`
	Raw   int64 `json:"image_raw"`
	Video int64 `json:"video"`
}

type shareYearJSON struct {
	Year  int   `json:"year"`
	Count int64 `json:"count"`
}

// shareFacetJSON is a tag or an album with its number of live photos.
type shareFacetJSON struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// handleShareFacets is GET /api/l/{lib}/facets: the filter sheet's
// choices within the scope. It takes no parameters (400 for any).
func (s *Server) handleShareFacets(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles) {
	for k := range r.URL.Query() {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid filter %q", k))
		return
	}
	var f catalog.Filter
	if !s.shareScope(w, r, req, l, &f, userFilter{}) {
		return
	}
	ctx := r.Context()
	fc, err := l.db.Facets(ctx, f, maxFacetCameras)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := shareFacetsJSON{Total: fc.Total, Types: shareTypesJSON{Image: fc.Images, Raw: fc.Raw, Video: fc.Videos},
		Years: make([]shareYearJSON, 0, len(fc.Years)), Undated: fc.Undated, Cameras: cameraList(fc.Cameras), NoCamera: fc.NoCamera}
	for _, y := range fc.Years {
		out.Years = append(out.Years, shareYearJSON{Year: y.Year, Count: y.Count})
	}
	if req.sh.Capabilities.Location {
		gps := fc.GPS
		out.GPS = &gps
	}
	if req.auth.wholeLibrary() {
		tags, albums, err := liveGroups(ctx, l)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		out.Tags, out.Albums = &tags, &albums
	}
	writeJSON(w, http.StatusOK, out)
}

// liveGroups lists l's tags and albums that have live photos, with their
// live counts, sorted by name and bounded (the most used are kept). It
// reads every membership once and checks the hashes with one LiveHashes
// call, whatever the number of tags and albums.
func liveGroups(ctx context.Context, l *libHandles) (tags, albums []shareFacetJSON, err error) {
	tm, err := l.user.TagMembers(ctx)
	if err != nil {
		return nil, nil, err
	}
	am, err := l.user.AlbumMembers(ctx)
	if err != nil {
		return nil, nil, err
	}
	seen := map[uint64]bool{}
	var hashes []uint64
	for _, ms := range [][]userdata.Member{tm, am} {
		for _, m := range ms {
			if !seen[m.Hash] {
				seen[m.Hash] = true
				hashes = append(hashes, m.Hash)
			}
		}
	}
	live, err := l.db.LiveHashes(ctx, hashes)
	if err != nil {
		return nil, nil, err
	}
	count := func(ms []userdata.Member) map[int64]int64 {
		out := map[int64]int64{}
		for _, m := range ms {
			if live[m.Hash] {
				out[m.ID]++
			}
		}
		return out
	}
	tagCounts, albumCounts := count(tm), count(am)

	ts, err := l.user.Tags(ctx)
	if err != nil {
		return nil, nil, err
	}
	tags = []shareFacetJSON{}
	for _, t := range ts {
		if n := tagCounts[t.ID]; n > 0 {
			tags = append(tags, shareFacetJSON{ID: t.ID, Name: t.Name, Count: n})
		}
	}
	as, err := l.user.Albums(ctx)
	if err != nil {
		return nil, nil, err
	}
	albums = []shareFacetJSON{}
	for _, a := range as {
		if n := albumCounts[a.ID]; n > 0 {
			albums = append(albums, shareFacetJSON{ID: a.ID, Name: a.Name, Count: n})
		}
	}
	return boundFacets(tags, maxFacetTags), boundFacets(albums, maxFacetAlbums), nil
}

// boundFacets keeps the max most used of list and sorts them by name
// (case-insensitively, then by id).
func boundFacets(list []shareFacetJSON, max int) []shareFacetJSON {
	if len(list) > max {
		slices.SortStableFunc(list, func(a, b shareFacetJSON) int { return cmp.Compare(b.Count, a.Count) })
		list = list[:max]
	}
	slices.SortStableFunc(list, func(a, b shareFacetJSON) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.ID, b.ID))
	})
	return list
}
