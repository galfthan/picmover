package server

import "net/http"

// Video conversions for the phone app (docs/design/video-playback.md,
// "Phones"): the admin /stream and /stream/{id}/status behind the share's
// own checks. The share mux has no route to POST .../open (opening an
// original happens on the computer's screen) and its handler is wrapped
// in RemoteOnly, so no phone request ever counts as local.

// shareVideoRow is the live, visible video row named by {id} of a share
// with `video`: 404 for anything else in or out of scope that is not a
// video, 403 without the capability.
func (s *Server) shareVideoRow(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles) bool {
	m, ok := s.shareRow(w, r, req, l)
	if !ok {
		return false
	}
	if m.FileType != "video" {
		shareNotFound(w)
		return false
	}
	if !req.sh.Capabilities.Video {
		writeError(w, http.StatusForbidden, errNotAllowed)
		return false
	}
	return true
}

// handleShareStream is GET /api/l/{lib}/stream/{id}: the admin handler
// after the share checks.
func (s *Server) handleShareStream(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles) {
	if s.shareVideoRow(w, r, req, l) {
		s.handleStream(w, r, l)
	}
}

// handleShareStreamStatus is GET /api/l/{lib}/stream/{id}/status.
func (s *Server) handleShareStreamStatus(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles) {
	if s.shareVideoRow(w, r, req, l) {
		s.handleStreamStatus(w, r, l)
	}
}
