package shell

import (
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
)

// appModeBrowsers are the Chromium-family browsers whose --app mode gives
// the UI a window of its own on Linux, in order of preference.
var appModeBrowsers = []string{
	"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
	"microsoft-edge", "microsoft-edge-stable",
}

// launcher starts browsers; its fields are hooks for tests.
type launcher struct {
	goos     string
	lookPath func(string) (string, error)
	start    func(name string, args ...string) error
	// profileDir is the --user-data-dir of the app-mode browser, so its
	// window is a process of its own (<cache dir>/picmover/app-profile).
	profileDir string
}

func newLauncher(goos, cacheDir string) launcher {
	return launcher{goos: goos, lookPath: exec.LookPath, start: startCommand,
		profileDir: filepath.Join(cacheDir, "app-profile")}
}

// startCommand starts browser commands (a hook for tests).
var startCommand = startDetached

// startDetached starts a command without waiting for it (reaped in the
// background).
func startDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// defaultCommand is the command that opens u in the default browser.
func (l launcher) defaultCommand(u string) (string, []string) {
	switch l.goos {
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", u}
	case "darwin":
		return "open", []string{u}
	}
	return "xdg-open", []string{u}
}

// appCommand is the command that opens u as an app window (Linux: a
// Chromium-family browser on PATH with --app); ok is false when there is
// none or the OS has no such mode here.
func (l launcher) appCommand(u string) (name string, args []string, ok bool) {
	if l.goos != "linux" {
		return "", nil, false
	}
	for _, b := range appModeBrowsers {
		if p, err := l.lookPath(b); err == nil {
			// --class sets the X11 WM_CLASS and the Wayland app_id, which
			// the desktop entry's StartupWMClass maps to picmover's icon.
			return p, []string{"--app=" + u, "--class=picmover", "--user-data-dir=" + l.profileDir,
				"--no-first-run", "--no-default-browser-check"}, true
		}
	}
	return "", nil, false
}

func checkURL(u string) error {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
		return fmt.Errorf("open browser: not an http URL: %q", u)
	}
	return nil
}

// openDefault opens u in the default browser.
func (l launcher) openDefault(u string) error {
	if err := checkURL(u); err != nil {
		return err
	}
	name, args := l.defaultCommand(u)
	if err := l.start(name, args...); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	return nil
}

// openApp opens u in an app-mode window where there is one (Linux with
// Chrome, Chromium or Edge), else in the default browser.
func (l launcher) openApp(u string) error {
	if err := checkURL(u); err != nil {
		return err
	}
	if name, args, ok := l.appCommand(u); ok {
		if err := l.start(name, args...); err == nil {
			return nil
		}
	}
	return l.openDefault(u)
}
