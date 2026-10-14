package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/metadata"
)

// StartOptions configure Start: what "picmover serve" resolves from its
// flags, and what the desktop app passes.
type StartOptions struct {
	// Path is the library or workspace folder (made absolute). "" serves
	// no workspace (mode "unset"); the app sets one later through POST
	// /api/app/workspace.
	Path string
	// Workspace serves Path as a workspace even when it has no library
	// yet (serve --workspace); a library given with it is a ModeError.
	Workspace bool
	// Addr is the listen address. When it is busy and AddrGiven is false,
	// a free loopback port is used instead (with a notice on ErrOut).
	Addr      string
	AddrGiven bool
	// CORSOrigins are passed to Options.CORSOrigins.
	CORSOrigins []string
	Version     string
	// ErrOut receives the notices serve prints (busy port, non-loopback
	// warning, missing file locking, a job that did not stop); nil
	// discards them.
	ErrOut io.Writer
	// Logger is the server's logger, also used for the HTTP server's
	// error log; nil discards.
	Logger *slog.Logger
	// CatalogLogger receives the catalog's notices when a library is
	// opened or created at start; nil: Logger.
	CatalogLogger *slog.Logger
	// App enables the desktop app's endpoints (nil for serve).
	App *AppHooks
	// ShareDir and ShareCmd are passed to Options (sharing on the home
	// Wi-Fi); sharing starts only through StartSharing or the Share page.
	ShareDir string
	ShareCmd string

	static fs.FS // tests: Options.Static
}

// AppHooks are the desktop shell's side of the /api/app endpoints. The
// callbacks run on request goroutines (OnQuit on its own goroutine after
// the response is sent).
type AppHooks struct {
	Token    string // per-run secret of POST /api/app/show and /quit
	Version  string
	Platform string // "windows", "linux" or "darwin"
	// Window reports, per request, whether the native window is in use
	// and why (WindowOK, WindowNoRuntime, WindowBrowserFlag,
	// WindowFailed, WindowUnsupported); nil: false, WindowUnsupported.
	Window      func() (ok bool, reason string)
	CloseToTray func() bool
	// OnOpen opens or focuses the window (the browser where there is none).
	OnOpen func()
	// OnQuit quits the app (force: jobs are cancelled). Called after the
	// 204 is sent.
	OnQuit func(force bool) error
	// OnWorkspaceChanged is called after a successful workspace switch with
	// the new root (the "root" of /api/libraries).
	OnWorkspaceChanged func(path string)
}

// The windowReason values of GET /api/app.
const (
	WindowOK          = "ok"           // the native window is used
	WindowNoRuntime   = "no-runtime"   // Windows without the WebView2 runtime
	WindowBrowserFlag = "browser-flag" // --browser
	WindowFailed      = "failed"       // creating the window failed; the browser is used for the rest of the run
	WindowUnsupported = "unsupported"  // no native window on this OS (Linux, macOS)
)

// ModeError is a Start failure caused by the folder and the Workspace
// flag (serve turns it into a usage error under --workspace).
type ModeError struct{ Err error }

func (e *ModeError) Error() string { return e.Err.Error() }
func (e *ModeError) Unwrap() error { return e.Err }

// Serve modes chosen by ServeMode.
const (
	ServeSingle    = "single"    // dir has media.db
	ServeWorkspace = "workspace" // dir holds libraries, or --workspace
	ServeNew       = "new"       // single, created: dir absent or empty
	ServeRefused   = "refused"   // dir holds other files
)

// ServeMode decides how dir is served:
//  1. dir/media.db exists: one library (workspace is then an error);
//  2. a subfolder has a regular media.db, or workspace: a workspace;
//  3. dir is absent, empty or has only .picmover: a new library;
//  4. otherwise refused (Start explains).
//
// libraries is the number of library subfolders found in case 2.
func ServeMode(dir string, workspace bool) (mode string, libraries int, err error) {
	if _, err := os.Lstat(filepath.Join(dir, library.DBName)); err == nil {
		if workspace {
			return "", 0, fmt.Errorf("--workspace: %s is a library (it has %s); serve it without --workspace", dir, library.DBName)
		}
		return ServeSingle, 0, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", 0, err
	}
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if workspace {
			return ServeWorkspace, 0, nil
		}
		return ServeNew, 0, nil
	case err != nil:
		if fi, serr := os.Stat(dir); serr == nil && !fi.IsDir() {
			return "", 0, fmt.Errorf("%s is not a directory", dir)
		}
		return "", 0, err
	}
	others := false
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			if name != library.MetaDir {
				others = true
			}
			continue
		}
		others = true
		if fi, err := os.Lstat(filepath.Join(dir, name, library.DBName)); err == nil && fi.Mode().IsRegular() {
			libraries++
		}
	}
	switch {
	case libraries > 0 || workspace:
		return ServeWorkspace, libraries, nil
	case !others:
		return ServeNew, 0, nil
	}
	return ServeRefused, 0, nil
}

// IsLibraryFolder reports whether dir has a media.db: the app serves such
// a folder as one library and any other folder as a workspace.
func IsLibraryFolder(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, library.DBName))
	return err == nil
}

// RebuildError explains how to recover from a catalog of another format.
type RebuildError struct {
	Root string
	Err  error
}

func (e *RebuildError) Error() string {
	return fmt.Sprintf("%v\nRecreate the catalog from the files with:\n  picmover rebuild %s\n"+
		"(the old catalog is kept as %s.bak-<timestamp>; thumbnails are reused)", e.Err, library.QuoteArg(e.Root), library.DBName)
}

func (e *RebuildError) Unwrap() error { return e.Err }

// CatalogError turns catalog.ErrNeedsRebuild into a message naming
// "picmover rebuild <library>"; other errors are returned unchanged.
func CatalogError(root string, err error) error {
	if errors.Is(err, catalog.ErrNeedsRebuild) {
		return &RebuildError{Root: root, Err: err}
	}
	return err
}

// LockLibrary takes the library lock for the command name. When the file
// system cannot lock files it warns on errOut and goes on without the
// lock; when another process holds it, the error names that process.
func LockLibrary(ctx context.Context, lib *library.Library, name string, errOut io.Writer) (func(), error) {
	unlock, err := lib.Lock(ctx, name)
	if errors.Is(err, library.ErrLockUnsupported) {
		if errOut == nil {
			errOut = os.Stderr
		}
		fmt.Fprintf(errOut, "Warning: %v; going on without the library lock, so do not run another "+
			"picmover command that writes this library at the same time.\n", err)
		return unlock, nil
	}
	return unlock, err
}

// openStartLibrary opens the library at dir and takes the library lock
// (release it with unlock). It creates the library and its catalog only
// when dir is not a library yet and holds nothing else: it is absent,
// empty, or has only a .picmover directory or an empty (0-byte) media.db.
// isNew reports that the catalog was created.
func openStartLibrary(ctx context.Context, dir string, errOut io.Writer, catLog *slog.Logger) (lib *library.Library, db *catalog.DB, unlock func(), isNew bool, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil, nil, false, err
	}
	fi, err := os.Stat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		isNew = true
	case err != nil:
		return nil, nil, nil, false, err
	case !fi.IsDir():
		return nil, nil, nil, false, fmt.Errorf("%s is not a directory", abs)
	default:
		dbi, err := os.Stat(filepath.Join(abs, library.DBName))
		switch {
		case err == nil && dbi.Mode().IsRegular() && dbi.Size() == 0:
			isNew = true
		case err == nil:
		case !errors.Is(err, fs.ErrNotExist):
			return nil, nil, nil, false, err
		default:
			if err := checkCreatable(abs); err != nil {
				return nil, nil, nil, false, err
			}
			isNew = true
		}
	}
	if lib, err = library.Open(abs); err != nil {
		return nil, nil, nil, false, err
	}
	if unlock, err = LockLibrary(ctx, lib, "serve", errOut); err != nil {
		return nil, nil, nil, false, err
	}
	db, err = catalog.Open(ctx, lib.DBPath(), catLog)
	if err != nil {
		unlock()
		if errors.Is(err, catalog.ErrNeedsRebuild) || isNew {
			return nil, nil, nil, false, CatalogError(lib.Root(), err)
		}
		return nil, nil, nil, false, fmt.Errorf("the catalog %s is unreadable: %w\n"+
			"If it is damaged, 'picmover rebuild %s' recreates it from the files "+
			"(the damaged one is kept as %s.bak-<timestamp>)", lib.DBPath(), err, library.QuoteArg(lib.Root()), library.DBName)
	}
	return lib, db, unlock, isNew, nil
}

// checkCreatable returns nil when the existing directory dir may become
// a new library: it is empty apart from a .picmover directory. Otherwise
// the error explains how to make a library of it.
func checkCreatable(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == library.MetaDir && e.IsDir() {
			continue
		}
		q := library.QuoteArg(dir)
		return fmt.Errorf("%s is not a picmover library (it has no %s) and is not empty; serve creates a "+
			"library only in an absent or empty directory.\n"+
			"To record the files already there in place, run:\n  picmover rebuild %s\n"+
			"To import photos into it in the <type>/YYYY/MM layout, run:\n  picmover import <source> %s\n"+
			"A folder whose subfolders are libraries is served as a workspace; to start an empty "+
			"workspace here (its subfolders become libraries from the browser), run:\n  picmover serve --workspace %s",
			dir, library.DBName, q, q, q)
	}
	return nil
}

// BrowserHost is the host:port to put in the URL: loopback for a
// wildcard listener.
func BrowserHost(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

const (
	startShutdownGrace = 5 * time.Second // HTTP requests still in flight at shutdown
	readHeaderTimeout  = 10 * time.Second
	idleTimeout        = 120 * time.Second
)

// Running is a server started by Start.
type Running struct {
	srv     *Server
	httpSrv *http.Server
	url     string
	addr    string
	banner  string
	errOut  io.Writer

	done     chan struct{} // closed when Serve returned
	serveErr error         // valid after done

	cleanup  []func() // closes what Start opened, after the server
	shutOnce sync.Once
	shutErr  error
}

// Start resolves opts.Path like "picmover serve" (a library, a workspace,
// a new library, or nothing in unset mode), opens and locks a single
// library, listens and serves until Shutdown. It returns once the server
// accepts connections.
func Start(ctx context.Context, opts StartOptions) (*Running, error) {
	errOut := opts.ErrOut
	if errOut == nil {
		errOut = io.Discard
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	catLog := opts.CatalogLogger
	if catLog == nil {
		catLog = logger
	}
	r := &Running{errOut: errOut, done: make(chan struct{})}
	ok := false
	defer func() {
		if !ok {
			r.runCleanup()
		}
	}()

	srvOpts := Options{App: opts.App, Static: opts.static, ShareDir: opts.ShareDir, ShareCmd: opts.ShareCmd}
	switch {
	case opts.Path == "":
		srvOpts.Unset = true
		r.banner = "Serving no workspace yet"
	default:
		abs, err := filepath.Abs(opts.Path)
		if err != nil {
			return nil, err
		}
		workspaceFlag := opts.Workspace
		if opts.App != nil && !IsLibraryFolder(abs) {
			workspaceFlag = true // the app serves any folder without media.db as a workspace
		}
		mode, nLibs, err := ServeMode(abs, workspaceFlag)
		if err != nil {
			if opts.Workspace {
				return nil, &ModeError{Err: err}
			}
			return nil, err
		}
		if mode == ServeWorkspace {
			if err := os.MkdirAll(abs, 0o755); err != nil {
				return nil, err
			}
			root := abs
			if rr, err := filepath.EvalSymlinks(abs); err == nil {
				root = rr
			}
			srvOpts.Workspace = root
			noun := "libraries"
			if nLibs == 1 {
				noun = "library"
			}
			r.banner = fmt.Sprintf("Serving workspace %s (%d %s)", root, nLibs, noun)
		} else {
			lib, db, unlock, isNew, err := openStartLibrary(ctx, abs, errOut, catLog)
			if err != nil {
				return nil, err
			}
			unlockOnce := sync.OnceFunc(unlock)
			r.cleanup = append(r.cleanup, unlockOnce, func() { db.Close() })
			store, err := openReadStore(lib)
			if err != nil {
				return nil, err
			}
			r.cleanup = append(r.cleanup, func() { store.Close() })
			srvOpts.Library, srvOpts.Catalog, srvOpts.Store, srvOpts.unlock = lib, db, store, unlockOnce
			created := ""
			if isNew {
				created = " (new)"
			}
			r.banner = fmt.Sprintf("Serving %s%s", lib.Root(), created)
		}
	}

	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		if opts.AddrGiven {
			return nil, fmt.Errorf("listen on %s: %w", opts.Addr, err)
		}
		fallback, ferr := net.Listen("tcp", "127.0.0.1:0")
		if ferr != nil {
			return nil, fmt.Errorf("listen on %s: %w", opts.Addr, err)
		}
		fmt.Fprintf(errOut, "Port %s is busy; using %s instead.\n", opts.Addr, fallback.Addr())
		ln = fallback
	}
	actual := ln.Addr().String()
	if !IsLoopbackAddr(opts.Addr) {
		fmt.Fprintf(errOut, "Warning: listening on %s, which is reachable from other machines. "+
			"There is no authentication: anyone who can connect can browse the library and start imports.\n", actual)
	}

	ffmpeg, _ := metadata.FindTool("ffmpeg")
	srvOpts.Version, srvOpts.FFmpeg, srvOpts.Addr = opts.Version, ffmpeg, actual
	srvOpts.CORSOrigins, srvOpts.Logger = opts.CORSOrigins, logger
	srv, err := New(srvOpts)
	if err != nil {
		ln.Close()
		return nil, err
	}
	r.srv = srv
	r.addr = actual
	r.url = "http://" + BrowserHost(actual) + "/"
	r.httpSrv = &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelInfo),
	}
	go func() {
		r.serveErr = r.httpSrv.Serve(ln)
		close(r.done)
	}()
	ok = true
	return r, nil
}

// URL is the server's address for a browser ("http://127.0.0.1:7439/").
func (r *Running) URL() string { return r.url }

// Addr is the address listened on (host:port).
func (r *Running) Addr() string { return r.addr }

// Banner is serve's first line without the URL ("Serving <library>",
// "Serving workspace <dir> (N libraries)").
func (r *Running) Banner() string { return r.banner }

// Done is closed when the server stops serving: after Shutdown, or when
// serving failed (Shutdown then returns the error).
func (r *Running) Done() <-chan struct{} { return r.done }

// JobCount is the number of running jobs (0 or 1).
func (r *Running) JobCount() int { return r.srv.jobs.count() }

// Workspace is the folder served ("" in unset mode).
func (r *Running) Workspace() string { return r.srv.ws.root() }

// Shutdown stops the server in three phases, like Ctrl+C in serve: it
// stops the job (cancelled; waited for until ctx is done, at most 30 s),
// drains the HTTP requests in flight (5 s), then closes the libraries
// and releases their locks. It returns the error that stopped serving,
// if any (not http.ErrServerClosed). Later calls return the same.
func (r *Running) Shutdown(ctx context.Context) error {
	r.shutOnce.Do(func() {
		// The share listener first (StopJobs stops it): phones are cut
		// off before the job stops and the libraries close.
		jobErr := r.srv.StopJobs(ctx)
		if jobErr != nil {
			fmt.Fprintf(r.errOut, "Warning: %v\n", jobErr)
		}
		sctx, cancel := context.WithTimeout(context.Background(), startShutdownGrace)
		defer cancel()
		if err := r.httpSrv.Shutdown(sctx); err != nil {
			r.httpSrv.Close()
		}
		if jobErr == nil {
			r.srv.Close()
		}
		r.runCleanup()
		<-r.done
		if r.serveErr != nil && !errors.Is(r.serveErr, http.ErrServerClosed) {
			r.shutErr = r.serveErr
		}
	})
	return r.shutErr
}

func (r *Running) runCleanup() {
	for i := len(r.cleanup) - 1; i >= 0; i-- {
		r.cleanup[i]()
	}
	r.cleanup = nil
}
