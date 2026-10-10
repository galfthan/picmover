// Package server is picmover's local HTTP server: a JSON API over the
// catalog and the thumbnail cache, import and thumbnail jobs with live
// progress (server-sent events), and the embedded single-page app.
//
// The package exposes an http.Handler (Server.Handler) rather than a
// listener, so the same API can be served by "picmover serve" or by an
// embedding shell. It never writes to stdout; diagnostics go to the
// Options.Logger.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"
	_ "time/tzdata" // IANA zones for job time zones on Windows

	"picmover/internal/catalog"
	"picmover/internal/importer"
	"picmover/internal/library"
	"picmover/internal/thumbs"
	"picmover/web"
)

// Options configure a Server.
type Options struct {
	Library *library.Library
	Catalog *catalog.DB   // read-write: jobs write through it
	Store   *thumbs.Store // read-only store renditions are served from
	Version string        // reported by /api/library
	FFmpeg  string        // ffmpeg path for video thumbnails; "" = not found
	// Addr is the host:port the server listens on. Host and Origin
	// headers must name it, or localhost / 127.0.0.1 / [::1] with its port.
	Addr string
	// CORSOrigins are extra origins (e.g. "http://localhost:5173") allowed
	// to call the API cross-origin; they get Access-Control-Allow-Origin.
	CORSOrigins []string
	// Static overrides the embedded frontend (web.Dist); for tests. An
	// FS without index.html counts as "not built".
	Static fs.FS
	Logger *slog.Logger // nil: discard

	// jobOptions, when set, adjusts the importer options of every job
	// (tests use it to slow down or observe a job).
	jobOptions func(*importer.Options)
}

// Server serves the API, the jobs and the frontend of one library.
type Server struct {
	lib     *library.Library
	db      *catalog.DB
	store   *thumbs.Store
	version string
	ffmpeg  string
	log     *slog.Logger
	static  fs.FS // nil: the frontend is not built

	guard   *guard
	jobs    *jobManager
	handler http.Handler

	// bodyTimeout bounds reading a request body (a client trickling a
	// POST body would otherwise hold a goroutine); tests shorten it.
	bodyTimeout time.Duration
}

// New returns a server for opts. Library, Catalog, Store and Addr are
// required. It raises the catalog's idle connection limit.
func New(opts Options) (*Server, error) {
	if opts.Library == nil || opts.Catalog == nil || opts.Store == nil {
		return nil, errors.New("server: Library, Catalog and Store are required")
	}
	g, err := newGuard(opts.Addr, opts.CORSOrigins)
	if err != nil {
		return nil, err
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Server{
		lib:     opts.Library,
		db:      opts.Catalog,
		store:   opts.Store,
		version: opts.Version,
		ffmpeg:  opts.FFmpeg,
		log:     log,
		static:  opts.Static,
		guard:   g,

		bodyTimeout: bodyReadTimeout,
	}
	switch {
	case s.static == nil:
		if dist, ok := web.Dist(); ok {
			s.static = dist
		}
	default:
		if _, err := fs.Stat(s.static, "index.html"); err != nil {
			s.static = nil // an override without index.html: not built
		}
	}
	// Keep enough idle connections for the concurrent reads of a page
	// load and a parallel geo scan (database/sql keeps 2 by default and
	// would reopen, re-running the connection pragmas, on every request).
	s.db.SetMaxIdleConns(maxIdleConns)
	s.jobs = newJobManager(s.lib, s.db, s.ffmpeg, log, opts.jobOptions)
	s.handler = s.middleware(s.routes())
	return s, nil
}

// Handler returns the HTTP handler: every request passes the Host,
// Origin and mutation checks before reaching a route.
func (s *Server) Handler() http.Handler { return s.handler }

// Shutdown cancels the running job and waits for it to finish, bounded by
// ctx and by 30 s, and ends every event stream. New jobs are refused
// afterwards. Close the catalog only after Shutdown returns.
func (s *Server) Shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, jobShutdownTimeout)
	defer cancel()
	return s.jobs.shutdown(ctx)
}

// maxIdleConns is the catalog's idle connection pool size while serving.
const maxIdleConns = 8

// jobShutdownTimeout bounds how long Shutdown waits for a cancelled job.
const jobShutdownTimeout = 30 * time.Second

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/library", s.handleLibrary)
	mux.HandleFunc("GET /api/timeline", s.handleTimeline)
	mux.HandleFunc("GET /api/media", s.handleMediaList)
	mux.HandleFunc("GET /api/media/{id}", s.handleMediaDetail)
	mux.HandleFunc("GET /api/thumb/{hash}", s.handleThumb)
	mux.HandleFunc("GET /api/preview/{hash}", s.handlePreview)
	mux.HandleFunc("GET /api/file/{id}", s.handleFile)
	mux.HandleFunc("GET /api/geo", s.handleGeo)
	mux.HandleFunc("GET /api/geo/items", s.handleGeoItems)
	mux.HandleFunc("GET /api/fs", s.handleFS)
	mux.HandleFunc("GET /api/jobs", s.handleJobList)
	mux.HandleFunc("POST /api/jobs", s.handleJobStart)
	mux.HandleFunc("GET /api/jobs/{id}", s.handleJobGet)
	mux.HandleFunc("GET /api/jobs/{id}/events", s.handleJobEvents)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.handleJobCancel)
	mux.HandleFunc("/", s.handleStatic)
	return mux
}

// statusWriter records the status code for the request log. Unwrap lets
// http.ResponseController reach the underlying writer (Flush for SSE).
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// maxBodyBytes caps every request body.
const maxBodyBytes = 64 << 10

// bodyReadTimeout is the read deadline set on requests with a body.
const bodyReadTimeout = 30 * time.Second

// middleware applies the security rules (security.go), the body cap and
// the request log around h.
func (s *Server) middleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w := &statusWriter{ResponseWriter: rw}
		defer func() {
			s.log.Debug("request", "method", r.Method, "path", r.URL.Path, "status", w.status,
				"duration", time.Since(start).Round(time.Microsecond))
		}()
		hdr := w.Header()
		hdr.Set("X-Content-Type-Options", "nosniff")
		hdr.Set("Referrer-Policy", "no-referrer")
		// No foreign page may embed any response (img, video, script):
		// keeps them out of cross-site processes and their probes.
		hdr.Set("Cross-Origin-Resource-Policy", "same-origin")
		if !s.guard.hostOK(r.Host) {
			hdr.Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusMisdirectedRequest)
			fmt.Fprintf(w, "misdirected request: host %q is not served here\n", r.Host)
			return
		}
		origin := r.Header.Get("Origin")
		cors := false
		if origin != "" {
			switch {
			case s.guard.corsOK(origin):
				cors = true
			case s.guard.originOK(origin):
			default:
				s.reject(w, r, http.StatusForbidden, "origin not allowed")
				return
			}
		}
		if !fetchSiteOK(r, cors) {
			s.reject(w, r, http.StatusForbidden, "cross-site request refused")
			return
		}
		if len(s.guard.cors) > 0 {
			hdr.Add("Vary", "Origin")
		}
		if cors {
			hdr.Set("Access-Control-Allow-Origin", origin)
		}
		if r.Method == http.MethodOptions {
			if cors {
				hdr.Set("Access-Control-Allow-Methods", "GET, POST")
				hdr.Set("Access-Control-Allow-Headers", "Content-Type, X-Picmover")
				hdr.Set("Access-Control-Max-Age", "600")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if msg := checkMutation(r); msg != "" {
				s.reject(w, r, http.StatusForbidden, msg)
				return
			}
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && s.bodyTimeout > 0 {
			// The server resets the deadline before the next request on
			// the connection. Unsupported writers (tests) ignore it.
			_ = http.NewResponseController(rw).SetReadDeadline(time.Now().Add(s.bodyTimeout))
		}
		h.ServeHTTP(w, r)
	})
}

// reject answers a request refused by the security checks: JSON under
// /api/, plain text elsewhere. Refusals log at Debug (shown with
// serve --verbose), so a hostile page cannot flood the terminal.
func (s *Server) reject(w http.ResponseWriter, r *http.Request, code int, msg string) {
	s.log.Debug("request refused", "method", r.Method, "path", r.URL.Path, "status", code, "reason", msg,
		"origin", r.Header.Get("Origin"))
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, code, msg)
		return
	}
	http.Error(w, msg, code)
}

// writeJSON writes v as the JSON response with status code. API JSON is
// never cached.
func writeJSON(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		writeError(w, http.StatusInternalServerError, internalErrorMsg)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	w.Write(append(b, '\n'))
}

type errorBody struct {
	Error string `json:"error"`
}

// writeError writes {"error": msg} with status code.
func writeError(w http.ResponseWriter, code int, msg string) {
	b, _ := json.Marshal(errorBody{Error: msg})
	h := w.Header()
	h.Del("Content-Disposition")
	h.Del("ETag")
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	w.Write(append(b, '\n'))
}

// internalErrorMsg is the body of every 500: the detail (paths, OS and
// SQL errors) goes to the log only.
const internalErrorMsg = "internal error"

// internalError logs err and answers a generic 500. When the client has
// gone away (the request context is done, which is usually why err
// happened) nothing is written and the error is logged at Debug only.
func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		s.log.Debug("request abandoned", "method", r.Method, "path", r.URL.Path, "err", err)
		return
	}
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, internalErrorMsg)
}
