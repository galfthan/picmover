//go:build windows

package shell

import (
	_ "embed"
	"log/slog"
)

//go:embed assets/tray.ico
var trayICO []byte

// trayIcon is the icon bytes systray takes on Windows (ICO).
func trayIcon() []byte { return trayICO }

// trayAvailable: Windows always has a notification area.
func trayAvailable(*slog.Logger) bool { return true }
