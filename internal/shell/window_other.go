//go:build !windows

package shell

import (
	"fmt"
	"log/slog"
	"os"

	"picmover/internal/server"
)

// newNativeWindow: there is no native window outside Windows (Linux uses
// a browser in app mode, macOS the default browser).
func newNativeWindow(windowConfig) (w nativeWindow, reason, detail string) {
	return nil, server.WindowUnsupported, ""
}

// confirmQuit has no dialog outside Windows: quitting cancels the job
// like Ctrl+C in serve.
func confirmQuit(w nativeWindow, jobs int) bool { return true }

// watchSessionEnd: Linux and macOS end the session with SIGTERM, which
// Run already handles.
func watchSessionEnd(log *slog.Logger, onEnd func()) {}

// ShowText prints the --help or --version text on stdout (Windows
// attaches to the parent console or shows a dialog).
func ShowText(title, text string) { fmt.Fprint(os.Stdout, text) }
