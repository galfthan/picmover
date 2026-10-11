//go:build linux

package shell

import (
	_ "embed"
	"log/slog"

	"github.com/godbus/dbus/v5"
)

//go:embed assets/tray.png
var trayPNG []byte

// trayIcon is the icon bytes systray takes on Linux (PNG).
func trayIcon() []byte { return trayPNG }

// statusNotifierWatcher is the D-Bus name of the tray host (KDE, GNOME
// with the AppIndicator extension, most other desktops).
const statusNotifierWatcher = "org.kde.StatusNotifierWatcher"

// trayAvailable reports whether the tray can run: systray needs a D-Bus
// session bus (without one its shutdown crashes). It only logs when no
// tray host is registered: systray registers its icon when one appears
// later, and the app keeps running without it meanwhile.
func trayAvailable(log *slog.Logger) bool {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		log.Warn("no tray: no D-Bus session bus; the app keeps running without an icon "+
			"(start picmover-app again to open the window, or quit it with POST /api/app/quit)", "err", err)
		return false
	}
	defer conn.Close()
	var has bool
	if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, statusNotifierWatcher).Store(&has); err != nil || !has {
		log.Warn("no tray host (StatusNotifierWatcher) is running; the icon appears when one starts "+
			"(on GNOME, install the AppIndicator extension)", "err", err)
	}
	return true
}
