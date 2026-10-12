//go:build !linux

package shell

import "log/slog"

// installDesktopEntry is Linux only (a freedesktop.org launcher).
func installDesktopEntry(*slog.Logger) {}
