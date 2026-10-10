package cmd

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
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/metadata"
	"picmover/internal/server"
	"picmover/internal/thumbs"
)

// Version is the picmover version, reported by /api/library and
// --version. The Makefile sets it with -ldflags -X picmover/cmd.Version=...
var Version = "dev"

// defaultServeAddr is serve's default --addr; when it is busy and --addr
// was not given, a free loopback port is used instead. A variable for tests.
var defaultServeAddr = "127.0.0.1:7439"

// Test hooks: how serve opens the browser and decides that stdout is a
// terminal.
var (
	openBrowser = server.OpenBrowser
	isTerminal  = func(w io.Writer) bool { _, ok := terminal(w); return ok }
)

const (
	shutdownGrace     = 5 * time.Second
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 120 * time.Second
)

func newServeCmd() *cobra.Command {
	var (
		addr        string
		noOpen      bool
		verbose     bool
		workspace   bool
		corsOrigins []string
	)
	c := &cobra.Command{
		Use:   "serve <library-or-workspace>",
		Short: "Browse and import in a web browser",
		Long: `Start a local web server for the library and open it in the browser: a
date-ordered photo grid with ratings, tags, albums and a trash, a map, an
import wizard with live progress, near-duplicate review, and library
statistics.

The argument is either a library (a folder with media.db) or a workspace:
a folder whose subfolders are libraries (/media/peter, /media/penny, ...).
A folder with at least one such subfolder is served as a workspace; the
browser then starts with the list of libraries, opens any of them, creates
new ones and records plain subfolders in place. --workspace serves a
folder as a workspace even when it has no library yet (it is created when
absent). In a workspace, each library is locked when it is opened.

The server listens on 127.0.0.1:7439 (another free port when that one is
busy) and answers only this machine and its own page. Stop it with Ctrl+C;
a running import is cancelled safely first.

<library> is created when it does not exist yet or is an empty directory,
so the first import can be started from the browser; a folder that already
holds files but no media.db is refused (record it in place with 'picmover
rebuild <dir>' first). While serving, the library is locked: other
picmover commands that write it (import, rebuild, thumbs, update-metadata)
refuse to run until the server stops. Imports
started from the browser write their log to <library>/.picmover/logs/ like
the CLI.`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			addrGiven := cmd.Flags().Changed("addr")
			if !addrGiven {
				addr = defaultServeAddr
			}
			if _, _, err := net.SplitHostPort(addr); err != nil {
				return usageError(cmd, fmt.Errorf("invalid --addr %q: want host:port, e.g. 127.0.0.1:7439", addr))
			}
			for i, o := range corsOrigins {
				n, err := server.NormalizeOrigin(o)
				if err != nil {
					return usageError(cmd, fmt.Errorf("invalid --cors-origin: %w", err))
				}
				corsOrigins[i] = n
			}
			return runServe(cmd, args[0], serveParams{addr: addr, addrGiven: addrGiven, noOpen: noOpen,
				verbose: verbose, corsOrigins: corsOrigins, workspace: workspace})
		},
	}
	f := c.Flags()
	f.StringVar(&addr, "addr", defaultServeAddr, "listen address host:port (non-loopback addresses expose the library to the network)")
	f.BoolVar(&noOpen, "no-open", false, "do not open the browser")
	f.StringArrayVar(&corsOrigins, "cors-origin", nil, "allow this origin to call the API cross-origin (repeatable; for frontend development, e.g. http://localhost:5173)")
	f.BoolVar(&verbose, "verbose", false, "log every request to stderr")
	f.BoolVar(&workspace, "workspace", false, "serve the folder as a workspace of libraries (its subfolders), even when it has none yet")
	return c
}

// openServeLibrary opens the library at dir and takes the library lock
// for the server's lifetime (release it with unlock). It creates the
// library and its catalog only when dir is not a library yet and holds
// nothing else: it is absent, empty, or has only a .picmover directory or
// an empty (0-byte) media.db. That lets the first import be started from
// the browser without turning an existing photo folder (or a mistyped
// path into one) into a library. isNew reports that the catalog was
// created.
func openServeLibrary(ctx context.Context, dir string, errOut io.Writer) (lib *library.Library, db *catalog.DB, unlock func(), isNew bool, err error) {
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
			if err := checkServeCreatable(abs); err != nil {
				return nil, nil, nil, false, err
			}
			isNew = true
		}
	}
	if lib, err = library.Open(abs); err != nil {
		return nil, nil, nil, false, err
	}
	if unlock, err = lockLibrary(ctx, lib, "serve", errOut); err != nil {
		return nil, nil, nil, false, err
	}
	db, err = catalog.Open(ctx, lib.DBPath(), stderrLogger())
	if err != nil {
		unlock()
		if errors.Is(err, catalog.ErrNeedsRebuild) || isNew {
			return nil, nil, nil, false, catalogError(lib.Root(), err)
		}
		return nil, nil, nil, false, fmt.Errorf("the catalog %s is unreadable: %w\n"+
			"If it is damaged, 'picmover rebuild %s' recreates it from the files "+
			"(the damaged one is kept as %s.bak-<timestamp>)", lib.DBPath(), err, quoteArg(lib.Root()), library.DBName)
	}
	return lib, db, unlock, isNew, nil
}

// checkServeCreatable returns nil when the existing directory dir may
// become a new library: it is empty apart from a .picmover directory.
// Otherwise the error explains how to make a library of it.
func checkServeCreatable(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == library.MetaDir && e.IsDir() {
			continue
		}
		q := quoteArg(dir)
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

type serveParams struct {
	addr        string
	addrGiven   bool
	noOpen      bool
	verbose     bool
	workspace   bool
	corsOrigins []string
}

// Serve modes chosen by serveMode.
const (
	serveSingle    = "single"    // dir has media.db
	serveWorkspace = "workspace" // dir holds libraries, or --workspace
	serveNew       = "new"       // single, created: dir absent or empty
	serveRefused   = "refused"   // dir holds other files
)

// serveMode decides how dir is served:
//  1. dir/media.db exists: one library (--workspace is then a usage error);
//  2. a subfolder has a regular media.db, or --workspace: a workspace;
//  3. dir is absent, empty or has only .picmover: a new library;
//  4. otherwise refused (openServeLibrary explains).
//
// libraries is the number of library subfolders found in case 2.
func serveMode(dir string, workspace bool) (mode string, libraries int, err error) {
	if _, err := os.Lstat(filepath.Join(dir, library.DBName)); err == nil {
		if workspace {
			return "", 0, fmt.Errorf("--workspace: %s is a library (it has %s); serve it without --workspace", dir, library.DBName)
		}
		return serveSingle, 0, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", 0, err
	}
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if workspace {
			return serveWorkspace, 0, nil
		}
		return serveNew, 0, nil
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
		return serveWorkspace, libraries, nil
	case !others:
		return serveNew, 0, nil
	}
	return serveRefused, 0, nil
}

// runServe serves the library or workspace until the command's context
// is cancelled or SIGINT/SIGTERM arrives; a stop is a normal exit (status
// 0).
func runServe(cmd *cobra.Command, dir string, p serveParams) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	mode, nLibs, err := serveMode(abs, p.workspace)
	if err != nil {
		if p.workspace {
			return usageError(cmd, err)
		}
		return err
	}
	var (
		srvOpts server.Options
		banner  string
	)
	if mode == serveWorkspace {
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return err
		}
		root := abs
		if r, err := filepath.EvalSymlinks(abs); err == nil {
			root = r
		}
		srvOpts.Workspace = root
		noun := "libraries"
		if nLibs == 1 {
			noun = "library"
		}
		banner = fmt.Sprintf("Serving workspace %s (%d %s)", root, nLibs, noun)
	} else {
		lib, db, unlock, isNew, err := openServeLibrary(ctx, abs, errOut)
		if err != nil {
			return err
		}
		defer unlock()
		defer db.Close()
		store, err := openServeStore(lib)
		if err != nil {
			return err
		}
		defer store.Close()
		srvOpts.Library, srvOpts.Catalog, srvOpts.Store = lib, db, store
		created := ""
		if isNew {
			created = " (new)"
		}
		banner = fmt.Sprintf("Serving %s%s", lib.Root(), created)
	}

	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		if p.addrGiven {
			return fmt.Errorf("listen on %s: %w", p.addr, err)
		}
		fallback, ferr := net.Listen("tcp", "127.0.0.1:0")
		if ferr != nil {
			return fmt.Errorf("listen on %s: %w", p.addr, err)
		}
		fmt.Fprintf(errOut, "Port %s is busy; using %s instead.\n", p.addr, fallback.Addr())
		ln = fallback
	}
	actual := ln.Addr().String()
	if !server.IsLoopbackAddr(p.addr) {
		fmt.Fprintf(errOut, "Warning: listening on %s, which is reachable from other machines. "+
			"There is no authentication: anyone who can connect can browse the library and start imports.\n", actual)
	}

	level := slog.LevelInfo
	if p.verbose {
		level = slog.LevelDebug
	}
	logHandler := slog.NewTextHandler(errOut, &slog.HandlerOptions{Level: level})
	ffmpeg, _ := metadata.FindTool("ffmpeg")
	srvOpts.Version, srvOpts.FFmpeg, srvOpts.Addr = Version, ffmpeg, actual
	srvOpts.CORSOrigins, srvOpts.Logger = p.corsOrigins, slog.New(logHandler)
	srv, err := server.New(srvOpts)
	if err != nil {
		ln.Close()
		return err
	}
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(logHandler, slog.LevelInfo),
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(ln) }()

	url := "http://" + browserHost(actual) + "/"
	fmt.Fprintf(out, "%s at %s\n", banner, url)
	if !p.noOpen && isTerminal(out) {
		if err := openBrowser(url); err != nil {
			fmt.Fprintf(errOut, "Could not open the browser (%v); open %s yourself.\n", err, url)
		}
	}

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-serveErr:
	}
	stop() // a second Ctrl+C now terminates at once
	fmt.Fprintln(out, "Stopping...")
	// Shutdown in three phases: stop the job (its event streams end),
	// drain the HTTP requests still in flight (a trash batch finishes
	// against open catalogs and under the lock), then close the
	// libraries and release their locks.
	jobErr := srv.StopJobs(context.Background())
	if jobErr != nil {
		fmt.Fprintf(errOut, "Warning: %v\n", jobErr)
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := httpSrv.Shutdown(sctx); err != nil {
		httpSrv.Close()
	}
	if jobErr == nil {
		srv.Close()
	}
	if runErr != nil && !errors.Is(runErr, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", runErr)
	}
	return nil
}

// openServeStore opens the thumbnail cache read-only, creating an empty
// one first when the library has none yet (imported with --no-thumbs and
// never thumbnailed): jobs write through their own store.
func openServeStore(lib *library.Library) (*thumbs.Store, error) {
	store, err := thumbs.OpenStoreReadOnly(lib)
	if errors.Is(err, fs.ErrNotExist) {
		w, werr := thumbs.OpenStore(lib)
		if werr != nil {
			return nil, werr
		}
		w.Close()
		store, err = thumbs.OpenStoreReadOnly(lib)
	}
	return store, err
}

// browserHost is the host:port to put in the URL: loopback for a
// wildcard listener.
func browserHost(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
