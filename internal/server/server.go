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
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata" // IANA zones for job time zones on Windows

	"picmover/internal/catalog"
	"picmover/internal/importer"
	"picmover/internal/library"
	"picmover/internal/thumbs"
	"picmover/internal/userdata"
	"picmover/web"
)

// Options configure a Server.
//
// Single mode serves one library opened by the caller: Library, Catalog
// and Store are required, the caller holds the library lock, and the
// server takes ownership of the handles (Shutdown closes them; closing
// them again is harmless). Workspace mode (Workspace set, the other three
// nil) serves the libraries in the subfolders of a folder, opening and
// locking each on request.
type Options struct {
	Library *library.Library
	Catalog *catalog.DB   // read-write: jobs write through it
	Store   *thumbs.Store // read-only store renditions are served from
	// UserData is the library's user.db; nil opens <library>/user.db.
	UserData *userdata.DB
	// Workspace is the folder of libraries in workspace mode.
	Workspace string
	Version   string // reported by /api/l/{lib}/library
	FFmpeg    string // ffmpeg path for video thumbnails; "" = not found
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

// Server serves the API, the jobs and the frontend of one library or of a
// workspace of libraries.
type Server struct {
	ws      *workspace
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

// New returns a server for opts (see Options for the two modes). Addr is
// required. In single mode it opens <library>/user.db when
// Options.UserData is nil and raises the catalog's idle connection limit.
func New(opts Options) (*Server, error) {
	g, err := newGuard(opts.Addr, opts.CORSOrigins)
	if err != nil {
		return nil, err
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ws := &workspace{log: log, libs: map[string]*libEntry{}, peeks: map[string]peekEntry{}}
	switch {
	case opts.Workspace != "":
		if opts.Library != nil || opts.Catalog != nil || opts.Store != nil {
			return nil, errors.New("server: Workspace excludes Library, Catalog and Store")
		}
		root, err := filepath.Abs(opts.Workspace)
		if err != nil {
			return nil, err
		}
		if fi, err := os.Stat(root); err != nil {
			return nil, fmt.Errorf("server: workspace: %w", err)
		} else if !fi.IsDir() {
			return nil, fmt.Errorf("server: workspace %s is not a directory", root)
		}
		ws.root = root
	case opts.Library == nil || opts.Catalog == nil || opts.Store == nil:
		return nil, errors.New("server: Library, Catalog and Store are required")
	default:
		ws.root, ws.single = opts.Library.Root(), true
		user := opts.UserData
		if user == nil {
			user, err = userdata.Open(context.Background(), filepath.Join(opts.Library.Root(), userdata.FileName))
			if err != nil {
				return nil, err
			}
		}
		// Keep enough idle connections for the concurrent reads of a page
		// load and a parallel geo scan (database/sql keeps 2 by default and
		// would reopen, re-running the connection pragmas, on every request).
		opts.Catalog.SetMaxIdleConns(maxIdleConns)
		id := singleID(ws.root)
		// The lock belongs to the caller (cmd/serve): the entry records it
		// as held, so nothing here takes it again or releases it.
		lockInfo, _ := os.Stat(opts.Library.LockPath())
		ws.libs[id] = &libEntry{id: id, path: ws.root, state: libOpen, lib: opts.Library, db: opts.Catalog,
			store: opts.Store, user: user, unlock: func() {}, external: true, lockInfo: lockInfo}
		backfillImports(context.Background(), opts.Catalog, user, log)
	}
	s := &Server{
		ws:      ws,
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
	s.jobs = newJobManager(ws, s.ffmpeg, log, opts.jobOptions)
	s.handler = s.middleware(s.routes())
	return s, nil
}

// Handler returns the HTTP handler: every request passes the Host,
// Origin and mutation checks before reaching a route.
func (s *Server) Handler() http.Handler { return s.handler }

// Mode is "single" or "workspace".
func (s *Server) Mode() string {
	if s.ws.single {
		return modeSingle
	}
	return modeWorkspace
}

// StopJobs is the first phase of a shutdown: it refuses new jobs,
// cancels the running one and waits for it (bounded by ctx and
// jobShutdownTimeout); when it does not stop, its event streams are
// closed so their connections end. The libraries stay open, so requests
// still in flight (a trash batch) finish against open handles while the
// HTTP server drains them. Close is the last phase.
func (s *Server) StopJobs(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, jobShutdownTimeout)
	defer cancel()
	return s.jobs.shutdown(ctx)
}

// Close is the last phase of a shutdown, after the HTTP server has
// drained: it closes every library (catalog, store, user.db) and releases
// the locks it took. Library routes answer 503 afterwards.
func (s *Server) Close() {
	s.ws.closeAll()
}

// Shutdown is StopJobs followed by Close, for callers that have no HTTP
// server to drain in between. When the job does not stop, the libraries
// are left open (the job still uses them) and the error says so.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.StopJobs(ctx)
	if err == nil {
		s.Close()
	}
	return err
}

// maxIdleConns is the catalog's idle connection pool size while serving.
const maxIdleConns = 8

// jobShutdownTimeout bounds how long Shutdown waits for a cancelled job.
const jobShutdownTimeout = 30 * time.Second

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/libraries", s.handleLibraries)
	mux.HandleFunc("POST /api/libraries", s.handleLibraryCreate)
	mux.HandleFunc("POST /api/libraries/{lib}/open", s.handleLibraryOpen)
	mux.HandleFunc("GET /api/libraries/{lib}/cover", s.handleLibraryCover)
	mux.HandleFunc("POST /api/libraries/{lib}/jobs", s.handleLibraryJobStart)

	scoped := func(pattern string, h func(http.ResponseWriter, *http.Request, *libHandles)) {
		mux.HandleFunc(pattern, s.scoped(h))
	}
	scoped("GET /api/l/{lib}/library", s.handleLibrary)
	scoped("GET /api/l/{lib}/timeline", s.handleTimeline)
	scoped("GET /api/l/{lib}/media", s.handleMediaList)
	scoped("GET /api/l/{lib}/media/{id}", s.handleMediaDetail)
	scoped("GET /api/l/{lib}/thumb/{hash}", s.handleThumb)
	scoped("GET /api/l/{lib}/preview/{hash}", s.handlePreview)
	scoped("GET /api/l/{lib}/file/{id}", s.handleFile)
	scoped("GET /api/l/{lib}/geo", s.handleGeo)
	scoped("GET /api/l/{lib}/geo/items", s.handleGeoItems)
	scoped("GET /api/l/{lib}/jobs", s.handleLibJobList)
	scoped("POST /api/l/{lib}/jobs", s.handleJobStart)

	scoped("POST /api/l/{lib}/ratings", s.handleRatings)
	scoped("GET /api/l/{lib}/tags", s.handleTags)
	scoped("POST /api/l/{lib}/tags", s.handleTagCreate)
	scoped("POST /api/l/{lib}/tags/{tid}", s.handleTagRename)
	scoped("POST /api/l/{lib}/tags/{tid}/delete", s.handleTagDelete)
	scoped("POST /api/l/{lib}/media/tags", s.handleMediaTags)
	scoped("GET /api/l/{lib}/albums", s.handleAlbums)
	scoped("POST /api/l/{lib}/albums", s.handleAlbumCreate)
	scoped("POST /api/l/{lib}/albums/{aid}", s.handleAlbumUpdate)
	scoped("POST /api/l/{lib}/albums/{aid}/delete", s.handleAlbumDelete)
	scoped("POST /api/l/{lib}/albums/{aid}/items", s.handleAlbumItems)
	scoped("GET /api/l/{lib}/imports", s.handleImports)
	scoped("GET /api/l/{lib}/imports/{iid}", s.handleImport)
	scoped("POST /api/l/{lib}/imports/{iid}", s.handleImportRename)
	scoped("POST /api/l/{lib}/imports/{iid}/delete", s.handleImportDelete)
	scoped("POST /api/l/{lib}/trash", s.handleTrash)
	scoped("POST /api/l/{lib}/trash/restore", s.handleRestore)
	scoped("POST /api/l/{lib}/trash/delete", s.handleTrashDelete)
	scoped("GET /api/l/{lib}/similar", s.handleSimilar)
	scoped("POST /api/l/{lib}/similar/ignore", s.handleSimilarIgnore)
	scoped("GET /api/l/{lib}/backups", s.handleBackups)
	scoped("POST /api/l/{lib}/backups/targets", s.handleTargetAdd)
	scoped("POST /api/l/{lib}/backups/targets/{id}", s.handleTargetUpdate)
	scoped("POST /api/l/{lib}/backups/targets/{id}/forget", s.handleTargetForget)
	scoped("GET /api/l/{lib}/backups/targets/{id}/plan", s.handleTargetPlan)
	scoped("GET /api/l/{lib}/backups/targets/{id}/runs", s.handleTargetRuns)

	mux.HandleFunc("GET /api/fs", s.handleFS)
	mux.HandleFunc("GET /api/jobs", s.handleJobList)
	mux.HandleFunc("GET /api/jobs/{id}", s.handleJobGet)
	mux.HandleFunc("GET /api/jobs/{id}/events", s.handleJobEvents)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.handleJobCancel)
	mux.HandleFunc("/", s.handleStatic)
	return mux
}

// scoped wraps a library-scoped handler: the {lib} path value must name
// an open library (acquire), whose handles the handler receives and keeps
// for the request.
func (s *Server) scoped(h func(http.ResponseWriter, *http.Request, *libHandles)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l, ok := s.acquire(w, r, r.PathValue("lib"))
		if !ok {
			return
		}
		defer l.entry.release()
		h(w, r, l)
	}
}

// stateError is the body of 409/503 answers about a library's state.
type stateError struct {
	Error string `json:"error"`
	State string `json:"state"`
}

// acquire resolves the library id of a scoped request: 404 when it is
// malformed or not in the workspace listing, 409 {error, state} when the
// library is not open, 503 while it is being rebuilt.
func (s *Server) acquire(w http.ResponseWriter, r *http.Request, id string) (*libHandles, bool) {
	if s.ws.isClosed() {
		writeError(w, http.StatusServiceUnavailable, errClosed.Error())
		return nil, false
	}
	if !validLibID(id) {
		writeError(w, http.StatusNotFound, errLibNotFound.Error())
		return nil, false
	}
	e := s.ws.entry(id)
	if e == nil {
		l, err := s.ws.find(id)
		if errors.Is(err, errLibNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return nil, false
		}
		if err != nil {
			s.internalError(w, r, err)
			return nil, false
		}
		e = s.ws.entryFor(l)
	}
	h, state, ok := e.acquire()
	switch {
	case ok:
		return h, true
	case state == libRebuilding:
		writeJSON(w, http.StatusServiceUnavailable, stateError{Error: "the library is being rebuilt", State: state})
	default:
		writeJSON(w, http.StatusConflict, stateError{Error: "library is not open", State: state})
	}
	return nil, false
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
