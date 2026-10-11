package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"picmover/internal/server"
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

type serveParams struct {
	addr        string
	addrGiven   bool
	noOpen      bool
	verbose     bool
	workspace   bool
	corsOrigins []string
}

// Serve modes chosen by serveMode (server.ServeMode).
const (
	serveSingle    = server.ServeSingle
	serveWorkspace = server.ServeWorkspace
	serveNew       = server.ServeNew
	serveRefused   = server.ServeRefused
)

// serveMode decides how dir is served (see server.ServeMode).
func serveMode(dir string, workspace bool) (string, int, error) {
	return server.ServeMode(dir, workspace)
}

// browserHost is the host:port to put in the URL (server.BrowserHost).
func browserHost(addr string) string { return server.BrowserHost(addr) }

// runServe serves the library or workspace until the command's context
// is cancelled or SIGINT/SIGTERM arrives; a stop is a normal exit (status
// 0). server.Start does the work; the desktop app calls it too.
func runServe(cmd *cobra.Command, dir string, p serveParams) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	level := slog.LevelInfo
	if p.verbose {
		level = slog.LevelDebug
	}
	run, err := server.Start(ctx, server.StartOptions{
		Path: abs, Workspace: p.workspace, Addr: p.addr, AddrGiven: p.addrGiven,
		CORSOrigins: p.corsOrigins, Version: Version, ErrOut: errOut,
		Logger:        slog.New(slog.NewTextHandler(errOut, &slog.HandlerOptions{Level: level})),
		CatalogLogger: stderrLogger(),
	})
	if err != nil {
		var me *server.ModeError
		if errors.As(err, &me) {
			return usageError(cmd, me.Err)
		}
		return err
	}

	url := run.URL()
	fmt.Fprintf(out, "%s at %s\n", run.Banner(), url)
	if !p.noOpen && isTerminal(out) {
		if err := openBrowser(url); err != nil {
			fmt.Fprintf(errOut, "Could not open the browser (%v); open %s yourself.\n", err, url)
		}
	}

	select {
	case <-ctx.Done():
	case <-run.Done():
	}
	stop() // a second Ctrl+C now terminates at once
	fmt.Fprintln(out, "Stopping...")
	// Shutdown in three phases: stop the job (its event streams end),
	// drain the HTTP requests still in flight (a trash batch finishes
	// against open catalogs and under the lock), then close the
	// libraries and release their locks.
	if err := run.Shutdown(context.Background()); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
