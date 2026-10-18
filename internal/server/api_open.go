package server

import (
	"context"
	"net"
	"net/http"

	"picmover/internal/metadata"
)

// remoteKey marks a request that came through a listener for other
// devices (RemoteOnly).
type remoteKey struct{}

// RemoteOnly wraps h for a listener that serves other devices (the LAN
// share): requests through it never count as coming from this computer,
// whatever their address (a proxy on this machine forwarding a phone's
// request has a loopback address). Local-only actions such as opening an
// original in a program on this computer answer 403 there.
func RemoteOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), remoteKey{}, true)))
	})
}

// localRequest reports whether r comes from this computer: a loopback
// remote address, not through a RemoteOnly listener.
func localRequest(r *http.Request) bool {
	if remote, _ := r.Context().Value(remoteKey{}).(bool); remote {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// canOpen reports whether "Open original" is available to r.
func (s *Server) canOpen(r *http.Request) bool {
	return localRequest(r) && s.opener != nil && s.opener.available()
}

// opener opens a file in the user's default program.
type opener interface {
	available() bool
	open(path string) error
}

// handleOpen opens the original of a row in the default program of this
// computer:
//
//	POST /api/l/{lib}/media/{id}/open
//
// Only for requests from this computer (403 otherwise: it would open a
// window on the server's screen, not the requester's). The path comes
// only from the row and passes the same checks as /file.
func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request, l *libHandles) {
	if !localRequest(r) {
		writeError(w, http.StatusForbidden, "opening originals is only possible on the computer running picmover")
		return
	}
	if s.opener == nil || !s.opener.available() {
		writeError(w, http.StatusNotImplemented, "no program to open files with on this computer")
		return
	}
	m, ok := s.mediaRow(w, r, l)
	if !ok {
		return
	}
	// Only media files go to the opener: a row naming anything else (an
	// executable, a script) is refused before the OS sees it.
	if _, media := metadata.Classify(m.RelPath); !media {
		writeError(w, http.StatusBadRequest, "not a photo or video file")
		return
	}
	f, err := s.openOriginal(l.lib, m.RelPath, !m.DeletedAt.IsZero())
	if isMissing(err) {
		writeError(w, http.StatusNotFound, "file not available")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	path := f.Name()
	f.Close()
	if err := s.opener.open(path); err != nil {
		s.log.Warn("open original", "id", m.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "could not open the file")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
