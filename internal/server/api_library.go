package server

import (
	"net/http"

	"picmover/internal/catalog"
)

type libraryJSON struct {
	Root    string       `json:"root"`
	Version string       `json:"version"`
	Summary summaryJSON  `json:"summary"`
	Thumbs  thumbsJSON   `json:"thumbs"`
	Cameras []cameraJSON `json:"cameras"`
	FFmpeg  bool         `json:"ffmpeg"`
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
}

type cameraJSON struct {
	Make  string `json:"make"`
	Model string `json:"model"`
	Count int64  `json:"count"`
}

func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sum, err := s.db.Summary(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	pending, ok, unsupported, failed, err := s.db.ThumbStats(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	cams, err := s.db.Cameras(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := libraryJSON{
		Root:    s.lib.Root(),
		Version: s.version,
		Summary: summaryJSON{
			Total: sum.Total, Images: sum.Images, Raw: sum.Raw, Videos: sum.Videos,
			WithGPS: sum.WithGPS, Deleted: sum.Deleted, Bytes: sum.TotalBytes,
			Earliest: wallClock(sum.Earliest), Latest: wallClock(sum.Latest),
		},
		Thumbs:  thumbsJSON{Pending: pending, OK: ok, Unsupported: unsupported, Error: failed},
		Cameras: cameraList(cams),
		FFmpeg:  s.ffmpeg != "",
	}
	writeJSON(w, http.StatusOK, out)
}

func cameraList(cams []catalog.Camera) []cameraJSON {
	out := make([]cameraJSON, 0, len(cams))
	for _, c := range cams {
		out = append(out, cameraJSON{Make: c.Make, Model: c.Model, Count: c.Count})
	}
	return out
}
