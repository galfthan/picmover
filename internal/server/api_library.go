package server

import (
	"context"
	"net/http"
	"strconv"

	"picmover/internal/catalog"
)

type libraryJSON struct {
	ID      string       `json:"id"`
	Root    string       `json:"root"`
	Version string       `json:"version"`
	Summary summaryJSON  `json:"summary"`
	Thumbs  thumbsJSON   `json:"thumbs"`
	Cameras []cameraJSON `json:"cameras"`
	FFmpeg  bool         `json:"ffmpeg"`
	UserDB  userDBJSON   `json:"userdb"`
	Similar similarJSON  `json:"similar"`
}

type userDBJSON struct {
	Ratings      int64 `json:"ratings"`
	Tags         int64 `json:"tags"`
	Albums       int64 `json:"albums"`
	Trashed      int64 `json:"trashed"`
	IgnoredPairs int64 `json:"ignoredPairs"`
}

// similarJSON describes the last near-duplicate run; every field is null
// before the first run.
type similarJSON struct {
	At       *int64 `json:"at"` // Unix seconds
	Distance *int   `json:"distance"`
	Items    *int   `json:"items"`
	Pairs    *int64 `json:"pairs"`
}

type summaryJSON struct {
	Total    int64   `json:"total"`
	Images   int64   `json:"images"`
	Raw      int64   `json:"raw"`
	Videos   int64   `json:"videos"`
	WithGPS  int64   `json:"withGps"`
	Deleted  int64   `json:"deleted"`
	Bytes    int64   `json:"bytes"`
	Earliest *string `json:"earliest"` // wall clock; null when no dated media
	Latest   *string `json:"latest"`
}

type thumbsJSON struct {
	Pending     int64 `json:"pending"`
	OK          int64 `json:"ok"`
	Unsupported int64 `json:"unsupported"`
	Error       int64 `json:"error"`
	// LargeMissing is the number of rows with a thumbnail and a preview
	// but no large thumbnail (the thumbs job fills them from the
	// previews); cached for up to largeMissingTTL.
	LargeMissing int64 `json:"largeMissing"`
}

type cameraJSON struct {
	Make  string `json:"make"`
	Model string `json:"model"`
	Count int64  `json:"count"`
}

func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request, l *libHandles) {
	ctx := r.Context()
	sum, err := l.db.Summary(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	pending, ok, unsupported, failed, err := l.db.ThumbStats(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	largeMissing, err := s.largeMissing(ctx, l)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	cams, err := l.db.Cameras(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	counts, err := l.user.Counts(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	sim, err := similarInfo(ctx, l.db)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := libraryJSON{
		ID:      l.id,
		Root:    l.lib.Root(),
		Version: s.version,
		Summary: summaryJSON{
			Total: sum.Total, Images: sum.Images, Raw: sum.Raw, Videos: sum.Videos,
			WithGPS: sum.WithGPS, Deleted: sum.Deleted, Bytes: sum.TotalBytes,
			Earliest: wallClock(sum.Earliest), Latest: wallClock(sum.Latest),
		},
		Thumbs: thumbsJSON{Pending: pending, OK: ok, Unsupported: unsupported, Error: failed,
			LargeMissing: largeMissing},
		Cameras: cameraList(cams),
		FFmpeg:  s.ffmpeg != "",
		UserDB: userDBJSON{Ratings: counts.Ratings, Tags: counts.Tags, Albums: counts.Albums,
			Trashed: counts.Trashed, IgnoredPairs: counts.IgnoredPairs},
		Similar: sim,
	}
	writeJSON(w, http.StatusOK, out)
}

// similarInfo reads the meta keys of the last near-duplicate run and the
// number of stored pairs (all null before the first run).
func similarInfo(ctx context.Context, db *catalog.DB) (similarJSON, error) {
	var out similarJSON
	at, ok, err := db.Meta(ctx, catalog.MetaSimilarAt)
	if err != nil || !ok {
		return out, err
	}
	if sec, err := strconv.ParseInt(at, 10, 64); err == nil {
		out.At = &sec
	}
	for _, k := range []struct {
		key string
		dst **int
	}{{catalog.MetaSimilarDistance, &out.Distance}, {catalog.MetaSimilarItems, &out.Items}} {
		v, ok, err := db.Meta(ctx, k.key)
		if err != nil {
			return out, err
		}
		if n, err := strconv.Atoi(v); ok && err == nil {
			*k.dst = &n
		}
	}
	n, err := db.CountSimilarPairs(ctx)
	if err != nil {
		return out, err
	}
	out.Pairs = &n
	return out, nil
}

func cameraList(cams []catalog.Camera) []cameraJSON {
	out := make([]cameraJSON, 0, len(cams))
	for _, c := range cams {
		out = append(out, cameraJSON{Make: c.Make, Model: c.Model, Count: c.Count})
	}
	return out
}
