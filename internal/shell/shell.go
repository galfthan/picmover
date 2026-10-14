// Package shell is the desktop app around the server (picmover-app): the
// settings file, one instance per user, the server's lifecycle, the
// native window (WebView2 on Windows) or the browser, and the
// notification-area icon.
package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"picmover/internal/server"
)

// Options are the picmover-app flags.
type Options struct {
	Workspace string // --workspace: overrides the saved one and is saved
	Hidden    bool   // --hidden: do not open the window at start
	NoTray    bool   // --no-tray
	Browser   bool   // --browser: never use the native window
	Port      int    // --port: 0 = the saved port (else DefaultPort)
	// NoDesktopEntry (--no-desktop-entry): do not write the Linux
	// launcher (~/.local/share/applications/picmover.desktop) and icons.
	NoDesktopEntry bool
	Version        string

	// ConfigDir and CacheDir override os.UserConfigDir()/picmover and
	// os.UserCacheDir()/picmover (tests).
	ConfigDir string
	CacheDir  string
	// Stderr receives fatal errors besides the log (nil: os.Stderr on
	// Linux and macOS, nothing on Windows).
	Stderr io.Writer
}

// quitDeadline bounds the wait for a cancelled job at quit.
const quitDeadline = 30 * time.Second

// Paths returns the config and cache directories the app uses.
func Paths() (configDir, cacheDir string, err error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", "", err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = filepath.Join(cfg, "cache")
	}
	return filepath.Join(cfg, "picmover"), filepath.Join(cache, "picmover"), nil
}

// runner is what the app needs of the server (*server.Running; a fake
// in tests).
type runner interface {
	URL() string
	Addr() string
	Banner() string
	Done() <-chan struct{}
	JobCount() int
	Workspace() string
	Shutdown(ctx context.Context) error
	// Sharing on the home Wi-Fi (the tray's "Share on Wi-Fi").
	SharingStatus() server.SharingStatus
	SharesHere() (int, error)
	StartSharing(port int) error
	StopSharing()
}

// app is one running picmover-app.
type app struct {
	opts      Options
	configDir string
	cacheDir  string
	log       *slog.Logger
	browser   launcher
	run       runner
	token     string

	// winMu guards win and winReason: the native window (nil: none) and
	// why it is or is not used (server.WindowOK, ...). A window that
	// fails is dropped for the rest of the run (WindowFailed).
	winMu     sync.Mutex
	win       nativeWindow
	winReason string
	// confirming is set while the tray's Quit question is shown.
	confirming atomic.Bool

	cfgMu sync.Mutex
	cfg   Config

	ready     chan struct{} // closed once run is set
	wsChanged chan struct{} // the tray's workspace item needs a refresh (buffered 1)

	quitOnce sync.Once
	quitting chan struct{} // closed when the quit sequence starts
	done     chan struct{} // closed when the quit sequence has finished
	trayUp   bool          // the tray loop runs on the main goroutine
	trayDone chan struct{} // closed when the tray loop returned
	exit     func(int)     // last resort when the tray loop does not return
}

// Run is the whole app: it returns when the app quits (nil), when a
// second instance handed off to the first one (nil), or on failure.
// Call it on the main goroutine: the tray needs it on Windows.
func Run(ctx context.Context, opts Options) (err error) {
	configDir, cacheDir, perr := Paths()
	if opts.ConfigDir != "" {
		configDir = opts.ConfigDir
	}
	if opts.CacheDir != "" {
		cacheDir = opts.CacheDir
	}
	if configDir == "" {
		return fmt.Errorf("no config directory: %w", perr)
	}
	stderr := opts.Stderr
	if stderr == nil && runtime.GOOS != "windows" {
		stderr = os.Stderr
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "picmover-app: %v\n", err)
		return err
	}

	// The single-instance lock comes before the log: a second instance
	// never opens (or rotates) the first one's log file; it reports on
	// stderr only (nothing on Windows, which has no console).
	release, err := lockInstance(configDir)
	if errors.Is(err, ErrRunning) {
		fmt.Fprintln(stderr, "picmover-app: another instance is running; asking it to show its window")
		hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := handOff(hctx, configDir, 5*time.Second); err != nil {
			err = fmt.Errorf("picmover-app is already running but did not answer: %w", err)
			fmt.Fprintf(stderr, "picmover-app: %v\n", err)
			return err
		}
		return nil
	}
	if err != nil {
		err = fmt.Errorf("single-instance lock: %w", err)
		fmt.Fprintf(stderr, "picmover-app: %v\n", err)
		return err
	}
	defer release()

	logw, err := openLog(logPath(configDir), logMaxBytes, logKeep)
	if err != nil {
		fmt.Fprintf(stderr, "picmover-app: %v\n", err)
		return err
	}
	defer logw.Close()
	logger := slog.New(slog.NewTextHandler(logw, &slog.HandlerOptions{Level: slog.LevelInfo}))
	// Libraries (systray, WebView2) log through the standard logger.
	prevStd := stdlog.Writer()
	stdlog.SetOutput(logw)
	defer stdlog.SetOutput(prevStd)
	defer func() {
		if err != nil {
			logger.Error("picmover-app failed", "err", err)
			fmt.Fprintf(stderr, "picmover-app: %v (log: %s)\n", err, logPath(configDir))
		}
	}()

	a := &app{opts: opts, configDir: configDir, cacheDir: cacheDir, log: logger,
		browser: newLauncher(runtime.GOOS, cacheDir), wsChanged: make(chan struct{}, 1), ready: make(chan struct{}),
		done: make(chan struct{}), quitting: make(chan struct{}), trayDone: make(chan struct{}), exit: os.Exit}
	return a.main(ctx)
}

func (a *app) main(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	a.log.Info("picmover-app starting", "version", a.opts.Version, "pid", os.Getpid(), "os", runtime.GOOS,
		"config", a.configDir)

	cfg, err := LoadConfig(a.configDir)
	if err != nil {
		a.log.Warn("settings unreadable; using the defaults", "err", err)
	}
	if a.opts.Workspace != "" {
		abs, err := filepath.Abs(a.opts.Workspace)
		if err != nil {
			return err
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			return fmt.Errorf("--workspace %s: not an existing folder", abs)
		}
		cfg.Workspace = abs
	}
	workspace := cfg.Workspace
	if workspace != "" {
		if fi, err := os.Stat(workspace); err != nil || !fi.IsDir() {
			a.log.Warn("the saved workspace is not available; starting without one", "workspace", workspace, "err", err)
			workspace = ""
		}
	}
	cfg.LastOpened = time.Now().Format(time.RFC3339)
	a.cfg = cfg

	a.token, err = newToken()
	if err != nil {
		return err
	}
	if a.opts.Browser {
		a.setWindow(nil, server.WindowBrowserFlag)
		a.log.Info("native window disabled (--browser)")
	} else {
		w, reason, detail := newNativeWindow(a.windowConfig())
		a.setWindow(w, reason)
		if w == nil && reason != server.WindowUnsupported {
			a.log.Warn("no native window; using the browser", "reason", reason, "detail", detail)
		}
	}

	switch {
	case a.opts.NoTray:
		a.log.Info("tray disabled (--no-tray)")
	default:
		a.trayUp = trayAvailable(a.log)
	}

	port := a.opts.Port
	given := port != 0
	if !given {
		port = cfg.Port
		if port == 0 {
			port = DefaultPort
		}
	}
	start := func(path string) (*server.Running, error) {
		return server.Start(ctx, server.StartOptions{
			Path:      path,
			Addr:      net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
			AddrGiven: given,
			Version:   a.opts.Version,
			ErrOut:    &logLines{log: a.log},
			Logger:    a.log,
			App:       a.hooks(),
			ShareCmd:  "picmover-app",
			// <config>/picmover/share: share.DefaultDir for the real
			// config dir, and the test's own one in tests.
			ShareDir: filepath.Join(a.configDir, "share"),
		})
	}
	run, err := start(workspace)
	if err != nil && workspace != "" && a.opts.Workspace == "" {
		// The saved library is busy (another picmover holds its lock) or
		// unreadable: start without a workspace so the user can choose.
		a.log.Warn("cannot serve the saved workspace; starting without one", "workspace", workspace, "err", err)
		workspace = ""
		run, err = start("")
	}
	if err != nil {
		return err
	}
	a.run = run
	close(a.ready)
	// Windows logoff or shutdown quits like the tray's Quit (a GUI process
	// gets no console signals). In every mode, --browser included.
	watchSessionEnd(a.log, a.sessionEnd)
	a.log.Info(run.Banner(), "url", run.URL())
	_, ps, _ := net.SplitHostPort(run.Addr())
	actual, _ := strconv.Atoi(ps)
	a.updateConfig(func(c *Config) {
		c.Port = actual
		if workspace != "" {
			c.Workspace = workspace
		}
	})
	if err := writeInstance(a.configDir, Instance{PID: os.Getpid(), Port: actual, Token: a.token}); err != nil {
		a.log.Error("cannot write the instance file; a second launch will not find this one", "err", err)
	}

	a.startDesktopEntry()
	// "Keep sharing on": sharing starts again with the app (never with
	// serve, which shares only with --share).
	if run.KeepSharingOn() {
		go func() {
			if err := run.StartSharing(0); err != nil {
				a.log.Warn("cannot share on Wi-Fi (Keep sharing on)", "err", err)
			}
		}()
	}

	go func() {
		select {
		case <-ctx.Done():
			a.log.Info("signal received; quitting")
		case <-run.Done():
			a.log.Error("the server stopped")
		case <-a.quitting:
			return
		}
		go a.quit()
	}()

	if !a.opts.Hidden {
		a.open("")
	}

	if a.trayUp {
		runTray(a.trayHooks()) // returns after systray.Quit
		close(a.trayDone)
	}
	<-a.done
	a.log.Info("picmover-app stopped")
	return nil
}

// startDesktopEntry writes the Linux launcher and icons in the
// background (unless --no-desktop-entry), so startup does not wait for
// it. The channel is closed when it is done; nil when skipped.
func (a *app) startDesktopEntry() <-chan struct{} {
	if a.opts.NoDesktopEntry {
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		installDesktopEntry(a.log)
	}()
	return done
}

// logLines turns the server's notices (one line each) into log records.
type logLines struct{ log *slog.Logger }

func (l *logLines) Write(p []byte) (int, error) {
	s := string(p)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	l.log.Warn(s)
	return len(p), nil
}

// updateConfig changes the settings and saves them.
func (a *app) updateConfig(f func(*Config)) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	f(&a.cfg)
	if err := SaveConfig(a.configDir, a.cfg); err != nil {
		a.log.Error("cannot save the settings", "err", err)
	}
}

func (a *app) config() Config {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	return a.cfg
}

// hooks are the server's /api/app callbacks.
func (a *app) hooks() *server.AppHooks {
	return &server.AppHooks{
		Token:       a.token,
		Version:     a.opts.Version,
		Platform:    runtime.GOOS,
		Window:      a.windowState,
		CloseToTray: func() bool { return a.config().CloseToTray },
		OnOpen:      func() { <-a.ready; a.open("") },
		OnQuit: func(force bool) error {
			a.log.Info("quit requested over HTTP", "force", force)
			<-a.ready
			a.quit()
			return nil
		},
		OnWorkspaceChanged: func(p string) {
			a.log.Info("workspace changed", "workspace", p)
			a.updateConfig(func(c *Config) { c.Workspace = p })
			select {
			case a.wsChanged <- struct{}{}:
			default:
			}
		},
	}
}

// pageURL is the server URL with an optional fragment ("#/?setup=1").
func (a *app) pageURL(fragment string) string { return a.run.URL() + fragment }

// open opens or focuses the window on the page (fragment "" keeps the
// page of an open window), or opens the browser where there is no
// window.
func (a *app) open(fragment string) {
	u := a.pageURL(fragment)
	if w := a.window(); w != nil {
		nav := ""
		if fragment != "" {
			nav = u
		}
		err := w.Open(u, nav)
		if err == nil {
			return
		}
		a.windowFailed(w, err)
	}
	if err := a.browser.openApp(u); err != nil {
		a.log.Error("cannot open the browser", "url", u, "err", err)
	}
}

// setWindow records the native window (nil: none) and the reason.
func (a *app) setWindow(w nativeWindow, reason string) {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	a.win, a.winReason = w, reason
}

// window is the native window in use, or nil.
func (a *app) window() nativeWindow {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	return a.win
}

// windowState is GET /api/app's window and windowReason.
func (a *app) windowState() (bool, string) {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	if a.winReason == "" {
		return a.win != nil, server.WindowUnsupported
	}
	return a.win != nil, a.winReason
}

// windowFailed drops the native window w after it could not be created:
// the browser is used for the rest of the run. Logged once.
func (a *app) windowFailed(w nativeWindow, err error) {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	if a.win != w {
		return // already dropped
	}
	a.win, a.winReason = nil, server.WindowFailed
	a.log.Error("the native window could not be created; using the browser for the rest of this run", "err", err)
}

// openInBrowser opens the UI in the default browser.
func (a *app) openInBrowser() {
	if err := a.browser.openDefault(a.run.URL()); err != nil {
		a.log.Error("cannot open the browser", "err", err)
	}
}

// changeWorkspace opens the Libraries page with the setup card.
func (a *app) changeWorkspace() { a.open("#/?setup=1") }

// saveSize remembers the window size.
func (a *app) saveSize(size WindowSize) {
	if size.Width > 0 && size.Height > 0 {
		a.updateConfig(func(c *Config) { c.Window = size })
	}
}

// windowClosed is called when the user closed the native window.
func (a *app) windowClosed(size WindowSize) {
	a.saveSize(size)
	if !a.config().CloseToTray || !a.trayUp {
		a.log.Info("window closed; quitting", "closeToTray", a.config().CloseToTray, "tray", a.trayUp)
		go a.quit()
	}
}

// trayQuit is the tray's Quit: with a job running it asks first when the
// window is open (Windows), else cancels the job like Ctrl+C in serve.
func (a *app) trayQuit() {
	if n := a.run.JobCount(); n > 0 {
		if w := a.window(); w != nil && w.IsOpen() {
			// One question at a time: further Quit clicks while it is up
			// are ignored.
			if !a.confirming.CompareAndSwap(false, true) {
				return
			}
			ok := confirmQuitFn(w, n)
			a.confirming.Store(false)
			if !ok {
				return
			}
		}
	}
	a.quit()
}

// confirmQuitFn asks the Quit question (a hook for tests).
var confirmQuitFn = confirmQuit

// sessionEndDeadline bounds the quit when Windows ends the session: the
// system does not wait much longer before it shows the app as blocking.
const sessionEndDeadline = 5 * time.Second

// sessionEnd runs the quit sequence when the user logs off or Windows
// shuts down, and returns when it finished or after sessionEndDeadline.
func (a *app) sessionEnd() {
	a.log.Info("the session is ending; quitting")
	go a.quitWithin(sessionEndDeadline)
	select {
	case <-a.done:
	case <-time.After(sessionEndDeadline):
		a.log.Warn("the quit did not finish before the session ended")
	}
}

// quit is the quit sequence (once): close the window, shut the server
// down (cancelling a job, waiting up to 30 s for it), remove the instance
// file and end the tray loop. Run returns afterwards.
func (a *app) quit() { a.quitWithin(quitDeadline) }

// quitWithin is quit with the shutdown bounded by deadline (the first
// call's deadline wins).
func (a *app) quitWithin(deadline time.Duration) {
	a.quitOnce.Do(func() {
		close(a.quitting)
		a.log.Info("quitting", "jobs", a.run.JobCount())
		if w := a.window(); w != nil {
			w.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), deadline)
		if err := a.run.Shutdown(ctx); err != nil {
			a.log.Error("server stopped with an error", "err", err)
		}
		cancel()
		removeInstance(a.configDir)
		close(a.done)
		if a.trayUp {
			stopTray()
			// The tray loop should return now; if it was never set up
			// (registration failed on Windows) it cannot, so exit.
			go func() {
				select {
				case <-a.trayDone:
				case <-time.After(5 * time.Second):
					a.log.Warn("the tray loop did not end; exiting")
					a.exit(0)
				}
			}()
		}
	})
}

// windowConfig is what the native window needs.
func (a *app) windowConfig() windowConfig {
	return windowConfig{
		dataPath: filepath.Join(a.cacheDir, "WebView2"),
		size:     func() WindowSize { return a.config().Window },
		onClosed: a.windowClosed,
		onSize:   a.saveSize,
		log:      a.log,
	}
}

// windowConfig configures the native window.
type windowConfig struct {
	dataPath string
	size     func() WindowSize
	onClosed func(WindowSize) // the user closed (hid) the window; its last normal size
	onSize   func(WindowSize) // the size at quit
	log      *slog.Logger
}

// nativeWindow is the app's own window (WebView2 on Windows).
type nativeWindow interface {
	// Open shows the window: a new one on url when none is open, else it
	// focuses the open one and, when nav is not "", navigates it there.
	Open(url, nav string) error
	// Close closes the window when it is open and waits (briefly) for it.
	Close()
	IsOpen() bool
	// Handle is the window's HWND (0 when closed), for dialogs.
	Handle() uintptr
}
