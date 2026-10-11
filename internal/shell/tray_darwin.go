//go:build darwin

package shell

import "log/slog"

// trayAvailable: the tray library needs cgo on macOS, which picmover
// does not use, so there is no icon there.
func trayAvailable(log *slog.Logger) bool {
	log.Info("no tray on macOS; quit picmover-app with Ctrl+C in its terminal or POST /api/app/quit")
	return false
}

// trayHooks, runTray and stopTray are never used on macOS.
type trayHooks struct{}

func (a *app) trayHooks() trayHooks { return trayHooks{} }
func runTray(trayHooks)             {}
func stopTray()                     {}
