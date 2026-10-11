package shell

import (
	"fmt"
	"os"
)

// newNativeWindow: there is no native window outside Windows (Linux uses
// a browser in app mode, macOS the default browser).
func newNativeWindow(windowConfig) (w nativeWindow, ok bool, why string) { return nil, false, "" }

// confirmQuit has no dialog outside Windows: quitting cancels the job
// like Ctrl+C in serve.
func confirmQuit(owner uintptr, jobs int) bool { return true }

// ShowText prints the --help or --version text on stdout (Windows
// attaches to the parent console or shows a dialog).
func ShowText(title, text string) { fmt.Fprint(os.Stdout, text) }
