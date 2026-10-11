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
	Version   string

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
}

// app is one running picmover-app.
type app struct {
	opts      Options
	configDir string
	cacheDir  string
	log       *slog.Logger
	browser   launcher
	win       nativeWindow // nil: no native window
	run       runner
	token     string

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

	release, err := lockInstance(configDir)
	if errors.Is(err, ErrRunning) {
		logger.Info("another instance is running; asking it to show its window")
		hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := handOff(hctx, configDir, 5*time.Second); err != nil {
			return fmt.Errorf("picmover-app is already running but did not answer: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("single-instance lock: %w", err)
	}
	defer release()

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
	if w, ok, why := newNativeWindow(a.windowConfig()); ok && !a.opts.Browser {
		a.win = w
	} else if why != "" && !a.opts.Browser {
		a.log.Warn("no native window; using the browser", "reason", why)
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
	errOut := &logLines{log: a.log}
	run, err := server.Start(ctx, server.StartOptions{
		Path:      workspace,
		Addr:      net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		AddrGiven: given,
		Version:   a.opts.Version,
		ErrOut:    errOut,
		Logger:    a.log,
		App:       a.hooks(),
	})
	if err != nil {
		return err
	}
	a.run = run
	close(a.ready)
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
		Window:      a.win != nil,
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
	if a.win != nil {
		nav := ""
		if fragment != "" {
			nav = u
		}
		if err := a.win.Open(u, nav); err == nil {
			return
		} else {
			a.log.Warn("the window could not be opened; using the browser", "err", err)
		}
	}
	if err := a.browser.openApp(u); err != nil {
		a.log.Error("cannot open the browser", "url", u, "err", err)
	}
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
	if n := a.run.JobCount(); n > 0 && a.win != nil && a.win.IsOpen() {
		if !confirmQuit(a.win.Handle(), n) {
			return
		}
	}
	a.quit()
}

// quit is the quit sequence (once): close the window, shut the server
// down (cancelling a job, waiting up to 30 s for it), remove the instance
// file and end the tray loop. Run returns afterwards.
func (a *app) quit() {
	a.quitOnce.Do(func() {
		close(a.quitting)
		a.log.Info("quitting", "jobs", a.run.JobCount())
		if a.win != nil {
			a.win.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), quitDeadline)
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
