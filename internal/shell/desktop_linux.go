//go:build linux

package shell

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed assets/icon256.png
var icon256PNG []byte

// desktopMarker marks a desktop entry picmover-app wrote; only such a
// file is ever rewritten.
const desktopMarker = "X-Picmover=1"

// Hooks for tests.
var (
	desktopExecutable = executablePath
	desktopLookPath   = exec.LookPath
	desktopRun        = func(ctx context.Context, name string, args ...string) error {
		return exec.CommandContext(ctx, name, args...).Run()
	}
)

// executablePath is the running executable, symlinks resolved.
func executablePath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

// xdgDataHome is $XDG_DATA_HOME, or ~/.local/share when it is unset or
// not absolute.
func xdgDataHome() (string, error) {
	if d := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}

// installDesktopEntry makes sure the user-level launcher and icons exist
// (see ensureDesktopEntry). Failures are logged, never returned: the app
// works without them.
func installDesktopEntry(log *slog.Logger) {
	dataHome, err := xdgDataHome()
	if err != nil {
		log.Warn("desktop entry: no data directory", "err", err)
		return
	}
	exe, err := desktopExecutable()
	if err != nil {
		log.Warn("desktop entry: cannot find the executable", "err", err)
		return
	}
	ensureDesktopEntry(log, dataHome, exe)
}

// desktopEntry is the content of picmover.desktop for the executable exe.
func desktopEntry(exe string) string {
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=picmover\n" +
		"Comment=Photo and video library\n" +
		"Exec=" + desktopExecQuote(exe) + "\n" +
		"Icon=picmover\n" +
		"Terminal=false\n" +
		"Categories=Graphics;Photography;\n" +
		"StartupWMClass=picmover\n" +
		desktopMarker + "\n"
}

// desktopExecQuote quotes a path for an Exec key: the Exec quoting rules
// (backslash before ", `, $ and \), then the string escaping of every
// value (\ doubled), and % doubled for the field codes.
func desktopExecQuote(p string) string {
	var b strings.Builder
	for _, r := range p {
		switch r {
		case '"', '`', '$', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	s := strings.ReplaceAll(b.String(), `\`, `\\`)
	return `"` + strings.ReplaceAll(s, "%", "%%") + `"`
}

// hasDesktopMarker reports whether a desktop entry carries the marker line.
func hasDesktopMarker(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == desktopMarker {
			return true
		}
	}
	return false
}

// ensureDesktopEntry writes <dataHome>/applications/picmover.desktop and
// the icons under <dataHome>/icons/hicolor so GNOME and KDE show
// picmover's icon for the app-mode window (StartupWMClass=picmover, the
// --class of the browser) and list picmover among the applications. A
// missing entry is created; one with the marker is rewritten only when
// its content differs (the executable moved); one without the marker is
// someone else's and left alone, icons included. It reports whether it
// wrote anything.
func ensureDesktopEntry(log *slog.Logger, dataHome, exe string) bool {
	if strings.ContainsAny(exe, "\n\r") {
		log.Warn("desktop entry: the executable path has a line break; not writing one", "exe", exe)
		return false
	}
	appsDir := filepath.Join(dataHome, "applications")
	entryPath := filepath.Join(appsDir, "picmover.desktop")
	want := []byte(desktopEntry(exe))
	old, err := os.ReadFile(entryPath)
	switch {
	case err == nil && !hasDesktopMarker(old):
		log.Info("desktop entry: leaving a picmover.desktop without the "+desktopMarker+" marker alone", "path", entryPath)
		return false
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		log.Warn("desktop entry: cannot read it", "path", entryPath, "err", err)
		return false
	}

	wrote := false
	icons := []struct {
		size string
		data []byte
	}{{"256x256", icon256PNG}, {"32x32", trayPNG}}
	for _, ic := range icons {
		p := filepath.Join(dataHome, "icons", "hicolor", ic.size, "apps", "picmover.png")
		w, err := writeIfChanged(p, ic.data)
		if err != nil {
			log.Warn("desktop entry: cannot write the icon", "path", p, "err", err)
		}
		wrote = wrote || w
	}
	entryWritten, err := writeIfChanged(entryPath, want)
	if err != nil {
		log.Warn("desktop entry: cannot write it", "path", entryPath, "err", err)
	}
	if entryWritten {
		log.Info("desktop entry written", "path", entryPath, "exe", exe)
		if p, err := desktopLookPath("update-desktop-database"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := desktopRun(ctx, p, appsDir); err != nil {
				log.Info("desktop entry: update-desktop-database failed (ignored)", "err", err)
			}
			cancel()
		}
	}
	return wrote || entryWritten
}

// writeIfChanged writes data to p atomically unless p already holds
// exactly data; it reports whether it wrote.
func writeIfChanged(p string, data []byte) (bool, error) {
	if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, data) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return false, err
	}
	if err := writeFileAtomic(p, data, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", p, err)
	}
	return true, nil
}
