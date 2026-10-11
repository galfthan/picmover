// Command picmover-app is picmover as a desktop application: it serves
// the UI on the loopback interface, shows it in a native window on
// Windows (WebView2) or in the browser, keeps a notification-area icon,
// and runs once per user. See docs/design/desktop-shell.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"picmover/internal/shell"
)

// version is set by the Makefile: -ldflags -X main.version=...
var version = "dev"

const usage = `picmover-app: picmover as a desktop application.

Usage: picmover-app [flags]

Starts the picmover server on this machine only, opens it in a window
(Windows) or the browser, and keeps an icon in the notification area so
imports and backups go on with the window closed. Starting it again
shows the running one. The first run asks for the folder that holds
your libraries.

Flags:
  --workspace <dir>  serve this folder (a workspace of libraries, or one
                     library) and remember it
  --hidden           do not open the window at start
  --no-tray          no notification-area icon (closing the window quits)
  --browser          use the browser, never the native window
  --port <n>         listen on this loopback port (default: the port of the
                     last run, first 7440)
  --no-desktop-entry Linux: do not create or update the launcher entry
                     (~/.local/share/applications/picmover.desktop)
  --version          print the version
  --help             print this help

Settings: app.json, log: picmover-app.log, both in the user config folder
(%APPDATA%\picmover, ~/.config/picmover, ~/Library/Application Support/picmover).
`

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	fs := flag.NewFlagSet("picmover-app", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var opts shell.Options
	var showVersion, help bool
	fs.StringVar(&opts.Workspace, "workspace", "", "")
	fs.BoolVar(&opts.Hidden, "hidden", false, "")
	fs.BoolVar(&opts.NoTray, "no-tray", false, "")
	fs.BoolVar(&opts.Browser, "browser", false, "")
	fs.IntVar(&opts.Port, "port", 0, "")
	fs.BoolVar(&opts.NoDesktopEntry, "no-desktop-entry", false, "")
	fs.BoolVar(&showVersion, "version", false, "")
	fs.BoolVar(&help, "help", false, "")
	fs.BoolVar(&help, "h", false, "")
	if err := fs.Parse(args); err != nil {
		shell.ShowText("picmover-app", fmt.Sprintf("picmover-app: %v\n\n%s", err, usage))
		return 1
	}
	switch {
	case help:
		shell.ShowText("picmover-app", usage)
		return 0
	case showVersion:
		shell.ShowText("picmover-app", "picmover-app "+version+"\n")
		return 0
	case fs.NArg() > 0:
		shell.ShowText("picmover-app", fmt.Sprintf("picmover-app: unexpected argument %q\n\n%s", strings.Join(fs.Args(), " "), usage))
		return 1
	case opts.Port < 0 || opts.Port > 65535:
		shell.ShowText("picmover-app", fmt.Sprintf("picmover-app: invalid --port %d\n", opts.Port))
		return 1
	}
	opts.Version = version
	if err := shell.Run(context.Background(), opts); err != nil {
		return 1 // the reason is in the log (and on stderr on Linux and macOS)
	}
	return 0
}
