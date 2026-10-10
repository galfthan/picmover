package server

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"picmover/internal/catalog"
	"picmover/internal/similar"
	"picmover/internal/userdata"
)

const (
	similarLimitDflt = 50
	similarLimitMax  = 200
)

// similarItemJSON is an item of a near-duplicate group: the compact item
// plus what the review needs to choose the one to keep.
type similarItemJSON struct {
	itemJSON
	Size    int64  `json:"size"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	RelPath string `json:"relPath"`
}

type similarGroupJSON struct {
	Items       []similarItemJSON `json:"items"`
	MaxDistance int               `json:"maxDistance"`
}

type similarGroupsJSON struct {
	Groups   []similarGroupJSON `json:"groups"`
	Total    int                `json:"total"`
	At       *int64             `json:"at"` // Unix seconds; null before the first run
	Distance *int               `json:"distance"`
}

// queryInt parses an optional non-negative integer parameter within
// [lo, hi], with a default.
func queryInt(r *http.Request, name string, dflt, lo, hi int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return dflt, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("invalid %s %q: want %d-%d", name, v, lo, hi)
	}
	return n, nil
}

// handleSimilar returns the near-duplicate groups of the last similar
// run: union-find over the stored pairs whose rows are both live, minus
// the ignored pairs. Groups are sorted by size (largest first), then max
// distance, then smallest hash; items in a group by pixel count, then
// file size (largest first), then hash. Paginated with offset and limit
// (default 50, at most 200).
func (s *Server) handleSimilar(w http.ResponseWriter, r *http.Request, l *libHandles) {
	offset, err := queryInt(r, "offset", 0, 0, 1<<30)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := queryInt(r, "limit", similarLimitDflt, 1, similarLimitMax)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	info, err := similarInfo(ctx, l.db)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	pairs, err := l.db.SimilarPairs(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	ignored, err := l.user.Ignored(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	groups := similar.Groups(pairs, func(a, b uint64) bool {
		_, ok := ignored[userdata.NewPair(a, b)]
		return ok
	})
	out := similarGroupsJSON{Groups: []similarGroupJSON{}, Total: len(groups), At: info.At, Distance: info.Distance}
	page := groups[min(offset, len(groups)):min(offset+limit, len(groups))]
	var hashes []uint64
	for _, g := range page {
		hashes = append(hashes, g.Hashes...)
	}
	rows, err := l.db.ByHashes(ctx, hashes)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	ratings, err := l.user.Ratings(ctx, hashes)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, g := range page {
		var ms []catalog.Media
		for _, h := range g.Hashes {
			if m, ok := rows[h]; ok {
				ms = append(ms, m)
			}
		}
		slices.SortFunc(ms, func(a, b catalog.Media) int {
			return cmp.Or(cmp.Compare(b.Width*b.Height, a.Width*a.Height), cmp.Compare(b.Size, a.Size), cmp.Compare(a.Hash, b.Hash))
		})
		gj := similarGroupJSON{Items: make([]similarItemJSON, 0, len(ms)), MaxDistance: g.MaxDistance}
		for i := range ms {
			it := newItem(&ms[i])
			it.Rating = ratings[ms[i].Hash]
			gj.Items = append(gj.Items, similarItemJSON{itemJSON: it, Size: ms[i].Size, Width: ms[i].Width,
				Height: ms[i].Height, RelPath: ms[i].RelPath})
		}
		out.Groups = append(out.Groups, gj)
	}
	writeJSON(w, http.StatusOK, out)
}

type ignoredJSON struct {
	Ignored int64 `json:"ignored"`
}

// handleSimilarIgnore stores every pair among the given hashes (at most
// 200) as "not duplicates" and returns the number of new pairs.
func (s *Server) handleSimilarIgnore(w http.ResponseWriter, r *http.Request, l *libHandles) {
	var body hashesBody
	if !decodeBody(w, r, &body) {
		return
	}
	hashes, err := parseHashes("hashes", body.Hashes, maxIgnoreHashes)
	if err == nil && len(hashes) < 2 {
		err = fmt.Errorf("hashes: give at least two")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var pairs []userdata.Pair
	for i := range hashes {
		for j := i + 1; j < len(hashes); j++ {
			pairs = append(pairs, userdata.NewPair(hashes[i], hashes[j]))
		}
	}
	n, err := l.user.Ignore(r.Context(), pairs)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ignoredJSON{Ignored: n})
}
