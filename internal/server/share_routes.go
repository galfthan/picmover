package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"picmover/internal/share"
)

// The share listener's routes (docs/design/lan-share.md, "Share listener:
// routes"). The share mux is built only from shareRouteTable, plus a
// final "/" that answers 404 for everything else: no admin handler is
// reachable from it. A test compares the table with a golden list.

// Kinds of share routes.
const (
	shareRoutePublic  = "public"  // no device needed (session answers 401 itself)
	shareRouteDevice  = "device"  // a paired device of an active share
	shareRouteLibrary = "library" // device, then the {lib} key through the scope
	shareRouteStatic  = "static"  // the phone app's files
)

// shareRoute is one route of the share mux.
type shareRoute struct {
	pattern string
	kind    string
	handler http.HandlerFunc
}

// shareLibHandler is a library route behind shareScoped.
type shareLibHandler func(http.ResponseWriter, *http.Request, *shareReq, *libHandles)

// shareRouteTable is every route of the share listener.
func (s *Server) shareRouteTable() []shareRoute {
	lib := func(pattern string, h shareLibHandler) shareRoute {
		return shareRoute{pattern, shareRouteLibrary, s.shareScoped(h, false)}
	}
	// media routes (renditions and originals) take their own, larger
	// per-device budget: a grid asks for a month of thumbnails at once.
	media := func(pattern string, h shareLibHandler) shareRoute {
		return shareRoute{pattern, shareRouteLibrary, s.shareScoped(h, true)}
	}
	// A static route serves one file, or (dir) a file named by the
	// {file...} wildcard under that folder of the frontend.
	static := func(pattern, name string) shareRoute {
		return shareRoute{pattern, shareRouteStatic, s.handleShareStatic(name)}
	}
	staticDir := func(pattern, dir string) shareRoute {
		return shareRoute{pattern, shareRouteStatic, s.handleShareStaticDir(dir)}
	}
	return []shareRoute{
		{"GET /api/session", shareRoutePublic, s.handleShareSession},
		{"POST /api/pair", shareRoutePublic, s.handleSharePair},
		{"GET /api/libraries", shareRouteDevice, s.shareDevice(s.handleShareLibraries)},
		lib("GET /api/l/{lib}/library", s.handleShareLibrary),
		lib("GET /api/l/{lib}/timeline", s.handleShareTimeline),
		lib("GET /api/l/{lib}/media", s.handleShareMediaList),
		lib("GET /api/l/{lib}/facets", s.handleShareFacets),
		lib("GET /api/l/{lib}/media/{id}", s.handleShareMediaDetail),
		media("GET /api/l/{lib}/thumb/{hash}", s.shareRendition(s.handleThumb)),
		media("GET /api/l/{lib}/preview/{hash}", s.shareRendition(s.handlePreview)),
		media("GET /api/l/{lib}/large/{hash}", s.shareRendition(s.handleLarge)),
		media("GET /api/l/{lib}/file/{id}", s.handleShareFile),
		lib("GET /api/l/{lib}/albums", s.handleShareAlbums),
		lib("POST /api/l/{lib}/ratings", s.handleShareRating),
		static("GET /{$}", shareAppFile),
		static("GET /share.html", shareAppFile),
		staticDir("GET /assets/{file...}", "assets"),
		static("GET /favicon.png", "favicon.png"),
		staticDir("GET /icons/{file...}", "icons"),
		static("GET /manifest.webmanifest", "manifest.webmanifest"),
	}
}

// shareMux builds the share mux from the table.
func (s *Server) shareMux() *http.ServeMux {
	mux := http.NewServeMux()
	for _, rt := range s.shareRouteTable() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	// Everything else, including every admin path and an allowed path
	// with another method: the same 404.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { shareNotFound(w) })
	return mux
}

// shareNotFound is the one 404 of the share listener: nothing outside a
// share's scope is told apart from what does not exist.
func shareNotFound(w http.ResponseWriter) { writeError(w, http.StatusNotFound, "not found") }

// shareBodyBytes caps a phone's request body.
const shareBodyBytes = 4 << 10

// shareCSP is the Content-Security-Policy of the phone app's HTML (no
// map tiles, no forms).
const shareCSP = "default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; connect-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// hostOK reports whether a Host header names the share listener: the
// bound IP, <hostname>.local when the mDNS name is on, or loopback, each
// with the listener's port.
func (h *shareHost) hostOK(hostport string) bool {
	if h == nil {
		return false
	}
	host, port, err := net.SplitHostPort(hostport)
	if err != nil || port != h.port {
		return false
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
	switch host {
	case "localhost", "127.0.0.1", "::1", h.ip:
		return true
	}
	return h.local != "" && host == h.local
}

// originOK reports whether an Origin is https://<accepted host>.
func (h *shareHost) originOK(origin string) bool {
	rest, ok := strings.CutPrefix(origin, "https://")
	if !ok || strings.ContainsAny(rest, "/?#@") {
		return false
	}
	return h.hostOK(rest)
}

// remoteHost is the IP of a request's peer ("" when unknown).
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// middleware is the share listener's request checks: security headers,
// Host (421), the auth-failure lockout (429), Origin (403, no CORS ever),
// Sec-Fetch-Site (403), the mutation headers (403), the 4 KiB body cap
// and the body deadline, and the request log (the path only: never the
// query or a cookie).
func (sh *sharing) middleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w := &statusWriter{ResponseWriter: rw}
		defer func() {
			sh.log.Debug("share request", "method", r.Method, "path", r.URL.Path, "status", w.status,
				"duration", time.Since(start).Round(time.Microsecond))
		}()
		hdr := w.Header()
		hdr.Set("X-Content-Type-Options", "nosniff")
		hdr.Set("Referrer-Policy", "no-referrer")
		hdr.Set("Cross-Origin-Resource-Policy", "same-origin")
		hdr.Set("Cross-Origin-Opener-Policy", "same-origin")
		hdr.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		host := sh.host.Load()
		if !host.hostOK(r.Host) {
			hdr.Set("Content-Type", "text/plain; charset=utf-8")
			hdr.Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusMisdirectedRequest)
			fmt.Fprintf(w, "misdirected request: host %q is not served here\n", r.Host)
			return
		}
		// The lockout covers the API, except POST /api/pair (it has its own
		// limits): the static files load even for a locked-out address, so
		// a phone that tripped it still gets the page that explains.
		if lockable(r) && sh.authFail.Locked(remoteHost(r)) {
			sh.reject(w, r, http.StatusTooManyRequests, "too many failed requests; try again in a few minutes")
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !host.originOK(o) {
			sh.reject(w, r, http.StatusForbidden, "origin not allowed")
			return
		}
		if !fetchSiteOK(r, false) {
			sh.reject(w, r, http.StatusForbidden, "cross-site request refused")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if msg := checkMutation(r); msg != "" {
				sh.reject(w, r, http.StatusForbidden, msg)
				return
			}
			_ = http.NewResponseController(rw).SetReadDeadline(time.Now().Add(bodyReadTimeout))
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, shareBodyBytes)
		}
		h.ServeHTTP(w, r)
	})
}

// lockable reports whether a request is subject to the auth-failure
// lockout: every /api/ path but /api/pair.
func lockable(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/pair"
}

// reject answers a refused request (JSON under /api/, text elsewhere),
// logged at Debug.
func (sh *sharing) reject(w http.ResponseWriter, r *http.Request, code int, msg string) {
	sh.log.Debug("share request refused", "method", r.Method, "path", r.URL.Path, "status", code, "reason", msg)
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, code, msg)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, msg, code)
}

// shareReq is an authenticated phone request: the device, a copy of its
// share as it is now, and the share's scope.
type shareReq struct {
	dev    share.Device
	sh     share.Share
	auth   scopeAuth
	cookie string // the cookie value, re-sent when renewed
}

// Phone-facing messages of the share states.
var shareStateMsg = map[string]string{
	share.StatePaused:  "This share is paused.",
	share.StateExpired: "This share has ended.",
	stateGone:          "This album is no longer shared.",
	stateUnavailable:   "This share is not available on the computer right now.",
}

// Share states the server adds to share.Share.State.
const (
	stateGone        = "gone"        // the album or library shared is no longer there
	stateUnavailable = "unavailable" // another workspace is served, or an unknown kind
)

// notPairedJSON is the 401 of the share API.
type notPairedJSON struct {
	Error  string `json:"error"`
	Paired bool   `json:"paired"`
}

// errNotPaired is the 401 message.
const errNotPaired = "This phone is not paired with picmover. Show a pairing code on the computer."

// writeShareState answers 403 {error, state} for a share that is not
// active.
func writeShareState(w http.ResponseWriter, state string) {
	writeJSON(w, http.StatusForbidden, stateError{Error: shareStateMsg[state], State: state})
}

// baseState is a share's state from its own fields and the served
// workspace, without opening anything: paused, expired, unavailable
// (unknown kind, another workspace root), else active. Album identity
// (gone) needs the library (scopeAuth.bind, Server.shareState).
func (s *Server) baseState(sh *share.Share, now time.Time) string {
	switch st := sh.State(now); st {
	case share.StatePaused, share.StateExpired:
		return st
	case share.StateUnknown:
		return stateUnavailable
	}
	root := s.ws.root()
	if root == "" || !sameDir(sh.Scope.Workspace, root) {
		return stateUnavailable
	}
	return share.StateActive
}

// verifyDevice resolves the device cookie of r: the device and its share,
// or false (no, malformed, unknown or revoked cookie). suspect is set for
// a malformed cookie or a wrong secret of an existing device: only those
// count against the address's lockout (no cookie, or the cookie of a
// revoked phone, is what an unpaired or removed phone sends).
func (s *Server) verifyDevice(r *http.Request) (v share.Verified, cookie string, ok, suspect bool) {
	m := s.share.mgr
	if m == nil {
		return share.Verified{}, "", false, false
	}
	c, err := r.Cookie(share.CookieName)
	if err != nil {
		return share.Verified{}, "", false, false
	}
	v, ok, suspect = m.VerifyCookie(c.Value)
	return v, c.Value, ok, suspect
}

// shareDeviceAuth authenticates a phone request (step 1 of "Per-request
// authorization"): 401 {error, paired: false} without a valid device (a
// malformed cookie or a wrong secret counts against the remote IP's
// lockout), 429 for a runaway device (media routes have their own,
// larger budget), 403 {error, state} when its share is not active. It
// records the device's activity and renews its cookie when due.
func (s *Server) shareDeviceAuth(w http.ResponseWriter, r *http.Request, media bool) (*shareReq, bool) {
	sh := s.share
	ip := remoteHost(r)
	v, cookie, ok, suspect := s.verifyDevice(r)
	if !ok {
		if suspect {
			sh.authFail.Fail(ip)
		}
		writeJSON(w, http.StatusUnauthorized, notPairedJSON{Error: errNotPaired})
		return nil, false
	}
	limit := sh.perDevice
	if media {
		limit = sh.perDeviceMedia
	}
	if !limit.Allow(v.Device.ID) {
		writeError(w, http.StatusTooManyRequests, "too many requests")
		return nil, false
	}
	renew, first := sh.mgr.Seen(v.Device.ID, ip)
	if renew {
		w.Header().Add("Set-Cookie", share.CookieFor(cookie))
	}
	if first {
		sh.log.Info(fmt.Sprintf("Phone %q of share %q is here today", v.Device.Name, v.Share.Name), "address", ip)
	}
	if st := s.baseState(&v.Share, sh.now()); st != share.StateActive {
		writeShareState(w, st)
		return nil, false
	}
	auth := scopeFor(&v.Share)
	if auth == nil {
		writeShareState(w, stateUnavailable)
		return nil, false
	}
	return &shareReq{dev: v.Device, sh: v.Share, auth: auth, cookie: cookie}, true
}

// shareDevice wraps a route that needs a device of an active share.
func (s *Server) shareDevice(h func(http.ResponseWriter, *http.Request, *shareReq)) http.HandlerFunc {
	return s.shareDeviceBudget(h, false)
}

// shareDeviceBudget is shareDevice with the media routes' budget when
// media is set.
func (s *Server) shareDeviceBudget(h func(http.ResponseWriter, *http.Request, *shareReq), media bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := s.shareDeviceAuth(w, r, media)
		if !ok {
			return
		}
		h(w, r, req)
	}
}

// shareScoped wraps a library route: device → share (shareDeviceAuth),
// the {lib} key through the scope, the library acquired (opened on
// demand) and bound to the scope's stable keys. media selects the media
// routes' per-device budget.
func (s *Server) shareScoped(h shareLibHandler, media bool) http.HandlerFunc {
	return s.shareDeviceBudget(func(w http.ResponseWriter, r *http.Request, req *shareReq) {
		ref, ok := req.auth.library(r.PathValue("lib"))
		if !ok {
			shareNotFound(w)
			return
		}
		l, ok := s.shareLibrary(w, r, req, ref)
		if !ok {
			return
		}
		defer l.entry.release()
		h(w, r, req, l)
	}, media)
}

// errShareUnavailable is the 503 message of a library a phone cannot use
// now (busy, rebuilding, unreadable): no holder pids or paths.
const errShareUnavailable = "This library is not available right now."

// shareLibrary acquires the library ref names for req, opening it on
// demand, and binds it to the scope: 404 when it is not in the served
// workspace or not the one shared (403 gone for an album share), 503 when
// it cannot be used now.
func (s *Server) shareLibrary(w http.ResponseWriter, r *http.Request, req *shareReq, ref libRef) (*libHandles, bool) {
	l, err := s.shareAcquire(r.Context(), ref.id)
	switch {
	case errors.Is(err, errLibNotFound):
		s.shareScopeError(w, r, req, errScopeMissing)
		return nil, false
	case err != nil:
		s.log.Debug("share: library not available", "id", ref.id, "err", err)
		writeError(w, http.StatusServiceUnavailable, errShareUnavailable)
		return nil, false
	}
	// A workspace switch between the share check and the acquire.
	if root := s.ws.root(); root == "" || !sameDir(req.sh.Scope.Workspace, root) || l.entry.retired.Load() {
		l.entry.release()
		writeShareState(w, stateUnavailable)
		return nil, false
	}
	if err := req.auth.bind(r.Context(), l); err != nil {
		l.entry.release()
		s.shareScopeError(w, r, req, err)
		return nil, false
	}
	return l, true
}

// shareScopeError answers a scope refusal: errScopeGone is 403 gone;
// errScopeMissing is 404, except in a share whose whole scope is that one
// library (an album share), where it is gone too; anything else is 500.
func (s *Server) shareScopeError(w http.ResponseWriter, r *http.Request, req *shareReq, err error) {
	switch {
	case errors.Is(err, errScopeGone), errors.Is(err, errScopeMissing) && !req.auth.wholeLibrary():
		writeShareState(w, stateGone)
	case errors.Is(err, errScopeMissing):
		shareNotFound(w)
	default:
		s.internalError(w, r, err)
	}
}

// errNotAvailable: the library is listed but cannot be acquired now.
var errNotAvailable = errors.New("library not available")

// shareAcquire acquires library id for a phone: a closed (or busy)
// library is opened on demand with the same open and lock as the admin
// "Open", and closed again once phones leave it alone
// (closePhoneLibraries). errLibNotFound when it is not a library of the served
// workspace; errNotAvailable or the open error otherwise.
func (s *Server) shareAcquire(ctx context.Context, id string) (*libHandles, error) {
	if s.ws.isClosed() || !validLibID(id) {
		return nil, errLibNotFound
	}
	e := s.ws.entry(id)
	if e == nil {
		li, err := s.ws.find(id)
		if err != nil {
			return nil, err
		}
		if !li.library {
			return nil, errLibNotFound
		}
		e = s.ws.entryFor(li)
	}
	h, state, ok := e.acquire()
	if !ok && (state == libClosed || state == libBusy) {
		if err := s.ws.openForPhone(ctx, e); err != nil {
			return nil, err
		}
		h, _, ok = e.acquire()
	}
	if !ok {
		return nil, errNotAvailable
	}
	e.notePhone(time.Now())
	return h, nil
}
