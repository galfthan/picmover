# Desktop shell (step 6)

Status: contract for branch `feat/desktop-shell` (from master a8e6ada, after PR #8).
"As implemented" sections go at the end, backend then frontend.

## Goal

A second executable, `picmover-app`, that makes picmover a desktop
application without changing the server or the UI: it starts the embedded
server on the loopback interface, shows the UI in a native window on
Windows, keeps a notification-area icon so imports and backups continue
with no window open, and runs one instance per user. The CLI executable
`picmover` stays as it is. Decisions already made: two executables, a
WebView2 window on Windows, Linux limited to the browser plus a tray,
macOS to the browser only.

## Layout

```
cmd/picmover-app/main.go     the app executable (package main)
internal/shell/              config, single instance, server lifecycle, browser launch, tray, window
  shell.go                   Run(ctx, Options) error: the whole lifecycle
  config.go                  app.json load/save
  instance.go + _windows/_unix  single instance (named mutex / flock) and the instance file
  browser.go                 default browser and Chrome-style app mode
  tray.go + tray_darwin.go   fyne.io/systray on windows/linux, stub on darwin
  window_windows.go          WebView2 window (jchv/go-webview2), window_other.go stub
  log.go                     file log in the config dir
internal/server              gains Start/Running (extracted from cmd/serve.go) and the app endpoints
web/                         setup page when no workspace is set, app-mode affordances
winres/                      go-winres config, icon source, committed .syso for windows/amd64
```

Dependencies (pin exactly; all pure Go): `github.com/jchv/go-webview2`
(pseudo-version of 2026-02-05 or newer, Windows files only, build tag
`windows`), `fyne.io/systray` v1.12.2 (build tag `windows || linux`; it is
pure Go there; its macOS backend needs cgo, so darwin gets a stub),
`golang.org/x/sys` (already present) for the mutex and registry. No
`pkg/browser`: three `exec` lines do it. Every build keeps
`CGO_ENABLED=0`; `GOOS=darwin` must still compile.

## Server integration

`cmd/serve.go` keeps its flags and messages but delegates to a new
`server.Start(ctx, server.StartOptions) (*server.Running, error)` with
`Running.URL()`, `Running.Shutdown(ctx)` and `Running.Done()`. StartOptions
carries what `serve` resolves today (workspace or library path, mode
detection, address, ffmpeg, logger, open-browser flag off). The shell calls
the same function, so behaviour is identical to `picmover serve <dir>`.

Workspace not set: `Start` accepts an empty path and serves in mode
`"unset"`: `/api/libraries` answers `{mode:"unset", libraries:[], folders:[]}`,
every `/api/l/...` route answers 409 `{error, state:"unset"}`, the folder
browser (`/api/fs`) works. `POST /api/app/workspace {path}` (app endpoints
below) validates the path (absolute, existing directory, `ValidSourcePath`
rules, not inside a library's `.picmover`), closes every open library,
switches the workspace manager to it and answers the new `/api/libraries`
body. The shell is told through `StartOptions.OnWorkspaceChanged(path)` and
saves it.

### App endpoints (served only when `StartOptions.App != nil`)

```
GET  /api/app                 → {app: true, version, workspace, closeToTray, platform: "windows"|"linux"|"darwin", window: true|false}
POST /api/app/workspace       {path} → the /api/libraries body; 400 on an invalid path, 409 while a job runs
POST /api/app/open            {}     → opens or focuses the window (or the browser where there is none); 204
POST /api/app/show            {token} → same as open; for the second instance; 403 on a bad token
POST /api/app/quit            {token, force?} → 409 {error, jobs:N} while jobs run unless force; else shuts down; 204
```

POST rules of step 3 apply (JSON content type, `X-Picmover: 1`, 64 KiB
cap). The token is a random 32-byte hex string generated per run and
written to the instance file; `open` and `workspace` need no token (they
come from the UI on the same origin, which the Origin/Sec-Fetch checks
already protect). Without `App` these routes are 404 and the UI hides the
app-only controls (`GET /api/app` → 404).

## Lifecycle (`shell.Run`)

1. Load `app.json` from `os.UserConfigDir()/picmover/` (create the dir):
   `{workspace, port, closeToTray: true, window: {width, height}, lastOpened}`.
   `port` is remembered after the first run so bookmarks keep working; if
   it is taken, pick a free one and save it.
2. Single instance: Windows named mutex `Local\picmover-app-<sha1 of the
   config dir>`; Linux and macOS `flock` on `$XDG_RUNTIME_DIR/picmover-app.lock`
   (fallback: the config dir). If taken: read the instance file
   `instance.json` `{pid, port, token}`, POST `/api/app/show` with the token,
   exit 0; if that fails too, exit 1 with a log line (and on Linux a
   message on stderr).
3. Start the log (`picmover-app.log`, 5 MiB, keep 2) and the server with
   the saved workspace (or unset). Write `instance.json`.
4. Tray (`systray.Run` on the main goroutine, which fyne pins to the main
   thread): icon (embedded ICO on Windows, PNG on Linux), tooltip
   "picmover", menu: **Open**, **Open in browser**, separator, a disabled
   item showing the workspace folder name (full path in the tooltip),
   **Change workspace…** (opens the window on the setup page), separator,
   **Quit** (label "Quit (N jobs running)" while jobs run; quitting then
   asks through the window when one is open, else cancels the jobs the way
   Ctrl+C does in `serve` and waits up to 30 s). On Linux without a tray
   host (`systray` fails to register) the app keeps running and says so in
   the log; `--no-tray` skips it.
5. Window: on Windows, if the WebView2 runtime is present (registry check:
   `pv` under `HKLM\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
   or the HKCU twin, non-empty and not `0.0.0.0`), open the UI in a
   WebView2 window: its own goroutine with `runtime.LockOSThread`, user
   data folder `os.UserCacheDir()/picmover/WebView2`, title "picmover",
   the embedded icon, saved size (default 1280×800, centred), `Run()` until
   the user closes it. Closing the window leaves the server and the tray
   running when `closeToTray` is true (the default), else quits. "Open"
   creates a new window when none is open and focuses the existing one
   otherwise (`SetForegroundWindow` from inside the process, after
   `AllowSetForegroundWindow` where needed; at worst the taskbar button
   flashes). Without the runtime: the default browser, plus a log line and
   a one-time notice in the UI linking to the WebView2 download page.
   On Linux: Chrome, Chromium or Edge in `--app=<url>` mode when one is on
   PATH (`google-chrome`, `google-chrome-stable`, `chromium`,
   `chromium-browser`, `microsoft-edge`, `microsoft-edge-stable`), with
   `--user-data-dir=<cache dir>/picmover/app-profile` so the window is a
   separate process; otherwise `xdg-open`. On macOS: `open`. "Open in
   browser" always uses the default browser.
6. Startup opens the window (or browser) unless `--hidden` is given (for
   a future auto-start). First run with no workspace opens the window on
   the setup page.
7. Quit: stop the window, shut the server down (`Running.Shutdown` with a
   30 s deadline, which cancels jobs like Ctrl+C), remove `instance.json`,
   release the lock, exit 0.

Flags of `picmover-app`: `--workspace <dir>` (overrides and saves),
`--hidden`, `--no-tray`, `--browser` (never use the native window),
`--port`, `--version`, `--help`. On Windows the executable is a GUI
program (`-H windowsgui`), so it has no console of its own: `--version`
and `--help` attach to the parent console when there is one
(`AttachConsole(ATTACH_PARENT_PROCESS)`) and print there, and otherwise
show a small message box. Everything else goes to the log file.

## Windows build

- `winres/winres.json` (go-winres v0.3.3 format): application icon (a
  simple icon generated by a Go program in `winres/gen/`, a rounded square
  with a sun-over-mountains glyph; keep the PNG source in the repo),
  version info from `git describe` (`--product-version=git-tag`),
  manifest with `requestedExecutionLevel asInvoker`, DPI awareness
  `PerMonitorV2`. The generated `rsrc_windows_amd64.syso` is committed so
  builds need only the Go toolchain; `make winres` regenerates it with
  `go run github.com/tc-hib/go-winres@v0.3.3 make`.
- Makefile: `build-app` producing `dist/picmover-app.exe`
  (`-ldflags "-H windowsgui -s -w -X …Version"`), `dist/picmover-app`
  (linux), `dist/picmover-app-darwin-arm64`; `build-all` includes them;
  `vet` covers `GOOS=windows`, `linux` and `darwin` for the new packages.
- The window icon uses the same resource (`IconId`).

## Web

- `GET /api/app` decides app mode at startup. In app mode: the header
  shows the workspace folder name with a "Change" link; the Libraries
  page shows the setup card when the mode is `unset` ("Choose the folder
  that holds your libraries", the existing FolderPicker, a Use this folder
  button → `POST /api/app/workspace`, then the libraries list); the
  WebView2-missing notice when `window` is false on Windows (one line,
  dismissable, stored in localStorage); no "open in browser" button in the
  UI itself (the tray has it).
- Keyboard: Ctrl+W or the window's close button closes the window; the UI
  does nothing special.
- Every page keeps showing which library it operates on.
- Tests: api parsing of `/api/app`, the setup card flow against the fake
  server (choose → libraries appear), the unset-mode 409 handling (no
  error toast storm), the notice.

## Tests (Go)

- `internal/shell`: config round trip and defaults; port reuse and
  fallback; single instance on Linux (two processes: the second posts
  show and exits 0; a stale instance file with a free lock is ignored);
  browser command selection per OS and the app-mode binary search (PATH
  stubbed); the quit sequence with and without running jobs (fake
  server); log rotation.
- `internal/server`: `Start`/`Running`, unset mode responses, the
  workspace switch (validation, 409 during a job, libraries closed and
  reopened, `OnWorkspaceChanged` called), the app endpoints' token checks
  and 404 without `App`, `serve` behaviour unchanged (existing tests).
- Windows-only code is vetted with `GOOS=windows` and reviewed; it cannot
  run here. The contract ends with a manual checklist for the user:
  first run and setup page; close to tray and reopen; second launch
  focuses; "Open in browser"; Quit with an import running; missing
  runtime fallback (rename the runtime key in a VM is optional); Linux:
  tray on KDE/GNOME with AppIndicator, app-mode window with Chrome,
  fallback to Firefox.

## Not in scope

Installer, auto-start on login, code signing, a native window on Linux or
macOS, native file dialogs, notifications, auto-update, a `picmover app`
subcommand in the CLI binary.

## As implemented (backend)

### Server (`internal/server`)

- `start.go`: `server.Start(ctx, StartOptions) (*Running, error)` holds
  what `runServe` did: `ServeMode` (moved from cmd, exported; cmd keeps
  `serveMode` as a thin wrapper for its tests), the single-library open
  and lock (`openStartLibrary`, `checkCreatable`, `LockLibrary`,
  `CatalogError`/`RebuildError`, moved from cmd with identical messages),
  the read-only thumbnail store, ffmpeg detection, the listener with the
  busy-port fallback (`AddrGiven`), the non-loopback warning, the HTTP
  server (same timeouts and error log). `StartOptions`: `Path` ("" =
  unset), `Workspace` (the `--workspace` flag), `Addr`, `AddrGiven`,
  `CORSOrigins`, `Version`, `ErrOut` (the notices serve prints),
  `Logger`, `CatalogLogger` (serve: stderr without timestamps), `App`.
  A `--workspace` error is a `*ModeError` (serve turns it into the usage
  error). `Running`: `URL()`, `Addr()`, `Banner()` (serve prints
  `Banner() + " at " + URL()`), `Done()` (closed when serving ends),
  `JobCount()`, `Workspace()`, `Shutdown(ctx)` (idempotent: StopJobs with
  ctx, then HTTP drain 5 s, then Close, then the store, catalog and lock
  Start opened; returns the serve error, not `ErrServerClosed`).
  `cmd/serve.go` now only parses flags, calls `Start`, prints the banner,
  opens the browser and waits for Ctrl+C; all `cmd/serve*_test.go` pass
  unchanged. One internal difference: the single library's lock is
  handed to the server (released by `Server.Close`, then again, as a
  no-op, by `Running`), instead of being released by a `defer` in cmd.
- With `App` set, a folder without `media.db` is always served as a
  workspace (as if `--workspace`), one with `media.db` as a library; the
  app never creates a library at start. The shell checks that a saved
  workspace still exists before passing it (an unplugged drive is not
  re-created by `MkdirAll`).
- Unset mode (`Options.Unset`): `workspace.conf` is now an
  `atomic.Pointer[wsConf]{root, single}` (root "" = unset) read through
  `ws.root()`, `ws.single()`, `ws.unset()`, so a switch is race-free.
- Job manager: `count()` (0 or 1) and `pause()`/`resume()` (a switch
  refuses to start while a job runs and blocks job starts meanwhile; a
  job start during a switch answers 409 "the workspace is being changed").
- Workspace switch: `ws.replace(conf)` retires every registered library
  (handles closed and lock released now, or when the last request using
  them ends) and starts with an empty registry and peek cache; a single
  library is then opened and locked by the server (`openSingle`) and its
  lock released by a later switch or `Close`. When that open fails the
  previous folder is served again (best effort) and the answer is 409.

### API (as the frontend relies on it)

All POSTs follow step 3: `Content-Type: application/json`,
`X-Picmover: 1`, 64 KiB cap, Origin/Sec-Fetch checks. Without `App`
every `/api/app` route answers 404 `{"error":"no such endpoint"}` (GET and
POST).

```
GET  /api/app
  200 {"app":true,"version":"…","workspace":"<root of /api/libraries, \"\" while unset>",
       "closeToTray":true,"platform":"windows"|"linux"|"darwin","window":false,
       "windowReason":"ok"|"no-runtime"|"browser-flag"|"failed"|"unsupported"}
       (window and windowReason are computed per request: "failed" once the
       window could not be created and the browser is used instead)
POST /api/app/open       {}                → 204, empty body (opens/focuses the window or the browser)
POST /api/app/show       {"token":"…"}     → 204 | 403 {"error":"invalid token"}
POST /api/app/quit       {"token":"…","force":false}
                                           → 204 (sent first; the app then quits)
                                           | 403 {"error":"invalid token"}
                                           | 409 {"error":"a job is running","jobs":1}   (unless force)
POST /api/app/workspace  {"path":"…"}      → 200 the /api/libraries body for the new folder
                                           | 400 {"error":"<a sentence for the user>"}
                                           | 409 {"error":"a job is running","jobs":1}
                                           | 409 {"error":"The library is in use by another picmover process (pid …, import, since …)."}
                                           | 409 {"error":"The library is still in use in this app (a request on it has not finished); try again in a moment."}
                                           | 409 {"error":"The library cannot be opened: …"}
                                           | 503 {"error":"the server is shutting down"}
```

The 400 texts: "Choose a folder.", "Use an absolute path (a full path
such as D:\Photos)." (`/home/you/Pictures` elsewhere), "Network (UNC) and
device paths cannot be used; choose a folder on a local or mapped
drive.", "The path contains an invalid character.", "The folder does not
exist.", "The folder cannot be read (permission denied).", "The folder
cannot be read.", "That is a file, not a folder.", "That folder is inside
a library's .picmover directory; choose the folder that holds your
libraries." (any path component `.picmover`, case-insensitive, lexical
and resolved), "That folder is inside the library <parent>; choose the
library itself or the folder that holds your libraries." (a parent
directory has `media.db`). A folder with `media.db` is accepted and served as one
library (mode `single`); the frontend's fake refuses it with 400, the
real server does not. The new root is the path with symbolic links
resolved, which is also what `/api/app` reports and `app.json` stores.

Unset mode: `GET /api/libraries` → `{"root":"","mode":"unset","libraries":[],"folders":[]}`;
every `/api/l/{lib}/…` route and `POST /api/libraries`,
`POST /api/libraries/{lib}/open`, `GET /api/libraries/{lib}/cover`,
`POST /api/libraries/{lib}/jobs` → 409 `{"error":"no workspace","state":"unset"}`;
`/api/fs`, `/api/jobs` and the app routes work.

The token is 32 random bytes as 64 hex digits, compared with
`subtle.ConstantTimeCompare`. The second instance sends `POST
http://127.0.0.1:<port>/api/app/show` with exactly `Content-Type:
application/json` and `X-Picmover: 1`, no `Origin` and no `Sec-Fetch-*`
(Go's client sends neither), and Host `127.0.0.1:<port>`: the
security middleware passes a mutation without Origin when the content
type and header are right (the same path `curl` takes).

### Shell (`internal/shell`)

- `shell.Run(ctx, Options)` (Options: `Workspace`, `Hidden`, `NoTray`,
  `Browser`, `Port`, `Version`, and `ConfigDir`/`CacheDir`/`Stderr` for
  tests). Sequence: config dir, log, single-instance lock (hand-off and
  return nil when taken), `app.json`, `--workspace` (must exist; saved),
  native window check, tray check, `server.Start` (if the saved
  workspace cannot be served, for example a library locked by another
  picmover, it logs that and starts unset, keeping the saved path),
  `app.json` saved (port, workspace, lastOpened), `instance.json`
  written, window/browser opened unless `--hidden`, tray loop on the
  calling (main) goroutine or a wait. SIGINT/SIGTERM and an unexpected
  end of serving quit too.
- `app.json` in `os.UserConfigDir()/picmover` (`%APPDATA%\picmover`,
  `~/.config/picmover`, `~/Library/Application Support/picmover`),
  written atomically (temp file + rename):

  ```json
  {"workspace": "", "port": 0, "closeToTray": true,
   "window": {"width": 1280, "height": 800}, "lastOpened": ""}
  ```

  Absent fields keep these defaults; a damaged file is logged and the
  defaults used (it is rewritten at start). `port` 0 means 7440 (not
  serve's 7439, so both can run); a busy remembered port falls back to a
  free one, which is saved; `--port` is strict (busy = exit 1) and saved.
  `window` is the outer size in physical pixels (sizes below 480×360 or
  above 16384 reset to the default).
- `instance.json` (same dir, mode 0600): `{"pid":N,"port":N,"token":"<64 hex>"}`,
  written after the server listens, removed at quit. A stale file (the
  lock is free) is simply overwritten.
- Single instance: Windows named mutex
  `Local\picmover-app-<sha1 hex of the lower-cased config dir>`; Linux and
  macOS `flock` on `$XDG_RUNTIME_DIR/picmover-app-<first 12 hex>.lock`, or
  `<config dir>/picmover-app.lock` without `XDG_RUNTIME_DIR` (deviation:
  the hash in the name, so test and real config dirs never collide). The
  second instance waits up to 5 s for `instance.json`, calls
  `AllowSetForegroundWindow(pid)` on Windows, posts `show`, exits 0; if
  that fails, exit 1 with the reason in the log and on stderr.
- Log: `picmover-app.log` in the config dir, slog text, 5 MiB, `.1` and
  `.2` kept. The server's request log and notices, systray's and
  WebView2's standard-library `log` output all go there.
- Browser: `rundll32 url.dll,FileProtocolHandler`, `open`, `xdg-open`;
  Linux app mode: the first of `google-chrome`, `google-chrome-stable`,
  `chromium`, `chromium-browser`, `microsoft-edge`,
  `microsoft-edge-stable` on PATH with `--app=<url>
  --user-data-dir=<cache>/picmover/app-profile`, else `xdg-open`. "Open"
  in the tray uses app mode (each click opens another app window: Chrome
  cannot focus an existing one from outside); "Open in browser" the
  default browser.
- Tray (`fyne.io/systray`, `windows || linux`): icon (ICO / PNG embedded
  from `internal/shell/assets`), tooltip "picmover", Open, Open in
  browser, separator, disabled "Workspace: <name>" (full path as tooltip;
  "No workspace chosen" while unset; updated on a switch), Change
  workspace… (window on `<url>#/?setup=1`), separator, Quit (relabelled
  "Quit (1 job running)" on a 5 s ticker). On Linux a left click on the
  icon opens the window. Linux: systray v1.12.2 calls onReady before it
  touches D-Bus and only logs registration failures (and its shutdown
  would crash without a session bus), so the shell first connects to the
  session bus itself: no bus → no tray (logged), the app runs on; bus but
  no `org.kde.StatusNotifierWatcher` → logged, the tray runs anyway and
  appears when a host starts. macOS: stub, logged.
- Quit (`app.quit`, once): close the window, `Running.Shutdown` with a
  30 s deadline (cancels a job like Ctrl+C), remove `instance.json`, end
  the tray loop; `Run` then releases the lock and returns. Tray Quit with
  a job running: on Windows with the window visible a Yes/No message box
  owned by the window asks first ("asks through the window"); elsewhere
  it quits at once. If the tray loop does not return within 5 s after
  `systray.Quit` (it never started on Windows), the process exits 0.
- Window (Windows, `jchv/go-webview2` pseudo-version 20260205): only when
  the registry shows the runtime (`pv` under
  `HKLM\SOFTWARE\WOW6432Node\…\{F3017226-…}`, then HKCU, then both
  without `WOW6432Node`; not empty, not `0.0.0.0`) and `--browser` is not
  given. One window per process, created on its own goroutine with
  `runtime.LockOSThread` and `CoInitializeEx(COINIT_APARTMENTTHREADED)`
  (deviation: needed because go-webview2's `edge` package initialises
  COM only on the main thread in its `init`, and that thread runs the
  tray). Title "picmover", icon resource 1, saved size, centred, user
  data `os.UserCacheDir()/picmover/WebView2`, dev tools and context menus
  off. The window procedure is subclassed: a user close (button, Alt+F4,
  Ctrl+W through a bound script) hides the window instead of destroying
  it (the page and its event streams keep running; deviation: the
  contract says the window is closed; hiding avoids recreating WebView2
  and leaking its browser processes, since go-webview2 has no controller
  Close), saves the normal size, and quits when `closeToTray` is false or
  there is no tray. Open shows, restores and raises it
  (`BringWindowToTop`, `SetForegroundWindow`, else `FlashWindow`); with a
  fragment (Change workspace…) it navigates. Quit destroys it.
- `--version`/`--help` on Windows: `AttachConsole(ATTACH_PARENT_PROCESS)`
  and write to `CONOUT$` (the prompt has already returned, so the text
  appears after it), else `MessageBoxW`. Usage errors exit 1 (not 2: the
  contract allows 0 and 1).

### Executable, resources, Makefile

- `cmd/picmover-app/main.go`: standard `flag` (`--workspace`, `--hidden`,
  `--no-tray`, `--browser`, `--port`, `--version`, `--help`/`-h`),
  version from `-X main.version=…`. Exit 0 normally and after a hand-off,
  1 on failure.
- `winres/gen` (`make icons`) draws the icon (rounded blue square, amber
  sun, white and pale-blue mountains) at 16, 32, 48 and 256 px into
  `winres/icon*.png`, and `internal/shell/assets/tray.png` (32 px) and
  `tray.ico` (16/32/48, PNG-compressed). `winres/winres.json`: icon group
  `#1`, manifest (`as invoker`, `per monitor v2`, `long-path-aware`,
  common controls v6, minimum OS win10), version info with empty version
  strings. `make winres` runs `go run
  github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json --out
  cmd/picmover-app/rsrc --arch amd64 --product-version=$(VERSION)
  --file-version=$(VERSION)`; the resulting
  `cmd/picmover-app/rsrc_windows_amd64.syso` is committed, so the version
  in the file properties is the one of the last `make winres` (regenerate
  it on a clean tagged tree before a release; a non-numeric version such
  as a commit hash gives fixed version 0.0.0.0 with the string shown as
  is). go-winres is not in go.mod.
- Makefile: `build-app` (after `web`) → `dist/picmover-app.exe`
  (`-H windowsgui`), `dist/picmover-app` (linux amd64),
  `dist/picmover-app-darwin-arm64`; `build-all` includes them; `vet` runs
  for linux, windows and darwin; `winres`, `icons`. Sizes at this commit
  (frontend not embedded): 22.7 MB (exe), 25.0 MB (linux).
- Dependencies: `fyne.io/systray v1.12.2`, `github.com/jchv/go-webview2
  v0.0.0-20260205173254-56598839c808` (+ `go-winloader`, indirect),
  `github.com/godbus/dbus/v5 v5.1.0` (systray's, used directly for the
  tray-host check). `GOOS=darwin CGO_ENABLED=0 go build ./...` passes:
  systray is imported only from `windows || linux` files.

### Tests (Go)

- `internal/server/app_test.go`: `TestStartSingleShutdown` (Start on a
  new library, lock held while serving and released by Shutdown, Done,
  idempotent Shutdown, every app route 404 without App, busy given
  address), `TestStartModeError`, `TestAppUnsetMode` (listing, 409
  `unset` on library and workspace routes, `/api/fs`, `/api/app` body),
  `TestAppTokenEndpoints` (open 204 empty, show 403/204, missing
  X-Picmover and foreign Origin refused, no-Origin client accepted, quit
  409 with jobs / 204 with force / 204 idle, OnQuit called),
  `TestAppWorkspaceSwitch` (the 400 sentences, 409 during a job,
  workspace → single library → workspace with locks released and taken,
  `/api/app` workspace = listing root, OnWorkspaceChanged),
  `TestAppWorkspaceSwitchBusyLibrary` (409, previous workspace kept; a
  folder of photos is served as a workspace in app mode, nothing
  created). All existing server and `cmd/serve*` tests pass unchanged.
- `internal/shell`: config defaults, partial and damaged files and round
  trip; log rotation; browser commands per OS and the app-mode search
  (PATH stubbed); instance lock; the show hand-off against a real server
  (no file, bad token, good token, file mode 0600); the quit sequence
  with and without a running job (fake server: one Shutdown with the
  30 s deadline, instance file removed) and window close with and
  without `closeToTray`; `TestRunBusySavedLibrary`; and
  `TestRunSingleInstance`, a real two-process test on Linux (stale
  instance file replaced, `--hidden` opens nothing, `/api/app`, the test
  binary started again as a second instance posts show and exits 0, the
  first opens the browser (stubbed), quit over HTTP, config saved,
  instance file removed, lock free); tray labels.
- Windows-only code (window, registry, mutex, console, message boxes) is
  vetted and built with `GOOS=windows` only; it has not run.

## Manual checklist

Windows (a recent Windows 10/11 with Edge, so the WebView2 runtime is
present):

1. `make build-app` and copy `dist/picmover-app.exe`. Delete
   `%APPDATA%\picmover` first for a first run. Double-click: one window
   "picmover" with the sun-and-mountains icon (title bar, taskbar, Alt+Tab,
   file properties show the version), centred, 1280×800; the tray icon
   appears. The setup card asks for a folder; choose your libraries'
   folder; the libraries are listed; `app.json` holds it.
2. Close the window (X, Alt+F4, Ctrl+W): the tray icon stays; the tray's
   Open brings the same page back (state kept). Resize, close, quit,
   start: the size is restored.
3. Start the exe again (Start menu, Explorer): no second tray icon; the
   existing window comes to the front (or its taskbar button flashes).
   Also after it was minimised and after it was closed to the tray.
4. Tray: Open in browser opens the default browser on the same URL;
   Change workspace… shows the setup card in the window; the workspace
   item shows the new folder name after a change.
5. Start an import, then tray Quit: the label says "Quit (1 job
   running)" (within 5 s); with the window open a Yes/No box asks; Yes
   stops the import safely (its log says cancelled) and the app exits
   within 30 s; `instance.json` is gone. Repeat with the window closed:
   no question, same result.
6. `picmover-app.exe --version` and `--help` in cmd and PowerShell print
   there; from the Run dialog (Win+R) they show a message box.
7. Task Manager: after several close/open cycles there is still one set
   of `msedgewebview2.exe` processes; after Quit none remain.
8. Optional, in a VM: rename the WebView2 `pv` registry value (or start
   with `--browser`): the default browser opens instead and the UI shows
   the WebView2 notice once (with `--browser` it shows too: `window` is
   false).
9. Check `%APPDATA%\picmover\picmover-app.log` for errors; high-DPI
   monitor: the window is crisp (per-monitor v2) when moved between
   monitors.

Linux:

1. `dist/picmover-app` on KDE: tray icon with the menu; left click opens
   the window. With Chrome or Chromium installed the UI opens as an app
   window (own profile under `~/.cache/picmover/app-profile`); without
   them (Firefox only) in the default browser.
2. GNOME with the AppIndicator extension: the icon appears; without it
   the log says no tray host and the app keeps running (start it again
   to open the UI; quit with Ctrl+C or `kill`).
3. Start it again: the running one opens another app window and the
   second process exits 0. Tray Quit with an import running cancels it
   and exits.
4. `--no-tray`, `--hidden`, `--port 7500`, `--workspace <dir>`: behave as
   described; `~/.config/picmover/app.json` and `picmover-app.log`.

## As implemented (frontend)

What was built (all under `web/src/`):

- `lib/api.ts`: `AppInfo` and `parseAppInfo` (tolerant: unknown platform
  reads as `linux`, a missing `window` as `false`, a missing `closeToTray`
  as `true`); `getApp()` (null on 404, other errors throw);
  `setAppWorkspace(path)` (POST `/api/app/workspace`, returns the parsed
  `/api/libraries` body; 400/409 surface as `ApiError` with `status` and
  `body`); `appOpen()` (POST `/api/app/open {}`, not used by the UI: the
  tray opens the window). `Workspace.mode` gains `'unset'`; the listing is
  parsed by one `parseWorkspace` for both GET `/api/libraries` and the
  workspace POST.
- `lib/app.svelte.ts`: the app store. `App.svelte` calls `app.refresh()`
  once at mount; `app.info` (null: `picmover serve`), `app.isApp`,
  `app.workspace` (the listing's root once a workspace is listed, else
  `info.workspace`), `app.workspaceName` (last path segment; POSIX, drive
  and UNC; roots stay whole), `app.setWorkspace(path)` (posts, hands the
  answer to `libraries.apply`, refreshes `/api/app`; returns the inline
  message or null), the WebView2 notice flag and its dismissal
  (localStorage `picmover.webview2notice` = `true` via `lib/storage.ts`).
- `lib/libraries.svelte.ts`: `mode` may be `'unset'`; `apply(ws)` takes a
  listing from the workspace POST and drops the old workspace's per-library
  notes (opening, failures, rebuilding, reopen guard) and any listing
  request in flight. A library-scoped 409 `{state:"unset"}` reloads the
  listing once (shared with any request in flight, and not at all when the
  store already says `unset`); no toast. An open request answered 409
  `{state:"unset"}` reloads the listing too.
- `lib/routes.ts`: `redirectFor` sends every route except the Libraries
  page to `#/` in mode `unset`; `#/?setup=1` (`SETUP_HREF`) stays on the
  Libraries page in every mode (single mode included, so Change works when
  the saved workspace turned out to be a single library).
- `components/WorkspaceSetup.svelte`: the setup card ("Choose the folder
  that holds your libraries", one line of explanation, the current
  workspace with a copy button when there is one, the existing
  `FolderPicker` whose choose button reads "Use this folder"). Errors stay
  inline under the picker (`role="alert"`): the server's 400 message as
  sent; any 409 as "An import or backup is running; wait for it to
  finish." Without app mode the button is disabled and the card says to
  restart `picmover serve <folder>`. `FolderPicker` gained an optional
  `disabled` prop for its choose button.
- `pages/Libraries.svelte`: the WebView2 notice (app mode, `platform
  "windows"` and `window false`, until dismissed) above the heading; the
  setup card when the mode is `unset` (then it is the whole page: no list,
  no "No libraries yet", no New library) or when the route has `?setup=1`
  (then above the list, with Cancel). Success and Cancel replace the route
  with `#/`.
- `App.svelte`: loads `/api/app`; in app mode the top bar shows
  "Workspace <name>" (full path as tooltip, "none" while unset) with a
  Change link to `#/?setup=1`, next to the library chip on library pages
  and next to the brand elsewhere (the brand then leaves out the root it
  shows under `serve`, which the chip replaces). The library chip and the
  Libraries page keep showing the library and workspace paths. The
  Libraries page also renders in mode `unset`, and in single mode with
  `?setup=1`. The shell keeps the last library's API client while the
  route leaves a library, so a page request that finishes after the
  redirect home does not read a null client (a race the unset redirect
  made visible).

Deviations and assumptions the backend must honour:

- The setup page is `#/?setup=1`. The tray's **Change workspace…** should
  open the window (or browser) on `<url>/#/?setup=1`; first run with no
  workspace needs no fragment (`#/` shows the card in mode `unset`).
- `/api/libraries` in mode `unset` may send `root: ""` or leave it out;
  `libraries`/`folders` may be `[]` or `null`.
- The 409 of `POST /api/app/workspace` is shown with a fixed text, whatever
  its `error`; `jobs` is not read. A 400 shows `error` verbatim, so it
  should be a sentence a user can act on.
- `/api/libraries/<id>/open` (and the other workspace routes) in mode
  `unset` may answer 409 `{state:"unset"}` or 404; both reload the listing.
- `GET /api/app` → `workspace` should be `""` while unset. After a switch
  the UI takes the root from the POST answer, then re-reads `/api/app`.
- `POST /api/app/open` returning 204 with no body is handled (an empty body
  parses as `{}`).

Tests added: `AppMode.test.ts` (8 tests: `serve` shows no workspace name
and no card; unset under `serve` shows the disabled card; unset in app mode
sends a library route home, shows the card, posts the chosen folder and
lists the libraries; 400 and 409 inline without toasts; header name,
tooltip and Change with Cancel and a switch that closes the old library;
the WebView2 notice and its remembered dismissal; hidden with the window
and on Linux/macOS; a library view whose requests start answering 409
`unset` goes home with at most two listing requests and no toast),
`lib/app.test.ts` (folder names), and new cases in `lib/api.test.ts`
(`/api/app` parsing and 404, the workspace POST and its errors, `open`,
the unset listing) and `lib/routes.test.ts` (unset redirects, `?setup=1`).
The fake server (`testing/fakes.ts`) gained `fakeServer('unset')`,
`server.app.info` (null → the app routes answer 404; `appInfo()` builds
one), `server.ws` (mode and root), `GET /api/app`, `POST
/api/app/workspace` (400 for an empty, relative or library path, 409
`{error:"a job is running", jobs:N}` while a job runs, else the new
listing with the libraries under the new root and open libraries closed),
`POST /api/app/open` → 204, and 409 `{error:"no workspace", state:"unset"}`
for every `/api/libraries/...` and `/api/l/...` route while unset.

## Review fixes (backend)

Findings of the review of a8e6ada..a85d4af handled on the Go side (2
and 9 are the frontend's).

- **1 (switch against an own lock), 3 (retired entries), 4 (unset
  window)**: `switchWorkspace` now prepares the new configuration first
  and publishes it in one step (`ws.publish`, under `ws.mu`: registry,
  peek cache and `conf` swapped together, `errClosed` after a shutdown
  began); requests see the old configuration or the new one, never an
  unset gap. Only then are the old registry's entries retired
  (`retireAll`): it waits up to 5 s (`retireWait`) for the requests still
  using them, then retires them anyway (their handles close when the
  last request ends, as before). When the target is a library this
  process already serves (the single library chosen again, a workspace
  library chosen as the single one, or, when the target is a workspace,
  any open library directly inside it) the entry is handed over
  (`libEntry.handOver`): its lock, catalog, store and user.db move into
  the new entry, the old entry is left retired and empty, requests still
  holding its handles keep using them, and nothing is closed or locked
  again (handed-over entries are not waited for). A switch that fails
  publishes nothing, so the previous configuration is served untouched;
  the rollback code is gone. `OnWorkspaceChanged` runs only after a
  successful publish. `libEntry.retired` (atomic, set under `e.mu`) is
  refused by `ws.open` and `beginRebuild` (`errRetired`, 409 `{error,
  state:"closed"}` from the open route) and by `jobManager.start` (409);
  the job manager stays paused until the old entries are retired, so the
  check cannot race a switch. A listing taken under the previous
  configuration (`listed.conf`) never registers an entry in the new
  registry (`entryFor`/`freshEntry` return a retired, unregistered one).
  `POST /api/libraries` reads one `cfg()` snapshot, answers 409 `unset`
  for an empty root and creates in that snapshot's root
  (`createLibrary(ctx, c, name)` refuses an empty or single root). An
  `ErrLocked` whose holder is this process (`ownLock`: same pid, same
  host when recorded) is described as "still in use in this app … try
  again in a moment", never as another picmover process (switch answer,
  `publicLibError`, the open route's 409).
- **5 (hidden Quit question)**: `confirmQuit` restores, raises and
  focuses the window before the message box; `app.confirming` (atomic)
  lets one question be up at a time, further Quit clicks are ignored
  while it is.
- **6, 11 and the unbounded wait (window creation)**: `newNativeWindow`
  probes the WebView2 data folder (creates it, writes and removes a
  probe file); a failure means no window (`failed`). `webviewWindow.Open`
  waits at most 20 s (`windowCreateTimeout`); `Embed` false, the probe or
  the timeout mark the window failed for good: the app drops it
  (`app.windowFailed`, logged once) and opens the browser for the rest
  of the run. A window shown blank by a failed `Embed` is destroyed
  (its handle comes from the placement hook); a window that becomes
  ready after the timeout, or after quit began, is destroyed too.
  `/api/app` `window` and the new `windowReason` (`ok`, `no-runtime`,
  `browser-flag`, `failed`, `unsupported` on Linux and macOS) come from
  `AppHooks.Window func() (bool, string)` per request. Not fixed:
  go-webview2 still calls `log.Fatal` in its environment and controller
  callbacks and when it cannot set its settings; the text reaches
  `picmover-app.log` (the standard logger writes there) and the process
  ends without the quit sequence. It is not forked: the probe covers the
  likely cause (an unwritable data folder), the rest (a broken or
  updating runtime, policy blocks) is rare, and a fork would have to
  follow upstream's COM bindings.
- **7 (window off screen)**: `placeWindow` fits the saved size into the
  primary monitor's work area (`SPI_GETWORKAREA`, else the screen size),
  minimum 640×480, centred on the area. go-webview2 can only centre on
  the whole screen, so a thread-local `WH_CBT` hook sets the position and
  size in the window's `CREATESTRUCT` before it appears (the size is
  passed clamped as well, so a missing hook still keeps it on screen).
- **8 (logoff/shutdown)**: `watchSessionEnd` creates a hidden window on
  its own locked thread in every mode (`--browser` included): it answers
  `WM_QUERYENDSESSION` with TRUE and, on `WM_ENDSESSION` with
  `wParam != 0`, runs the quit sequence with a 5 s shutdown deadline and
  waits up to 5 s before returning. Deviation: a hidden top-level window
  (`WS_POPUP`, `WS_EX_TOOLWINDOW`, never shown), not a message-only one:
  message-only windows (`HWND_MESSAGE`) do not receive these broadcasts.
- **10 (workspace inside a library)**: `checkWorkspacePath` walks up the
  cleaned path (not following links) to the volume root and refuses a
  folder whose parent has `media.db`: "That folder is inside the library
  <parent>; choose the library itself or the folder that holds your
  libraries."
- **12 (dirty version in the .syso)**: `make winres` is documented as a
  release step (Makefile comment, here): run on a clean tree, the
  committed `.syso` names the commit it was made on, so the exe's file
  version is one commit behind by construction unless it is run on a
  tag. The `.syso` was regenerated as the last commit of this pass.
- **13 (second instance and the log)**: `Run` takes the single-instance
  lock before it opens or rotates the log; a second instance writes
  nothing to the log file and reports on stderr only (nothing on
  Windows, which has no console).
- **14 (repeated message)**: the busy switch answer is "The library is in
  use by another picmover process (pid …, …)." (from `ErrLocked`).
- **15 (tests)**: `internal/shell`: `TestRunPortFallback` (busy saved
  port falls back and the new one is saved; busy `--port` fails, which
  `main` turns into exit 1; `windowReason` `browser-flag`),
  `TestWindowFailureFallsBack`, `TestTrayQuitOneQuestion`,
  `TestPlaceWindow`, `TestProbeDataDir`, and `TestRunSingleInstance` now
  checks `windowReason` and that the second instance left the log alone.
  `internal/server`: `TestSwitchToHeldSingleLibrary` and
  `TestSwitchToHeldLibraryInWorkspace` (hand-over with a request in
  flight: 200, lock never dropped, the request's catalog still usable,
  no 5 s wait), `TestSwitchFailureKeepsPrevious` (single library kept,
  locked and open, no `OnWorkspaceChanged`),
  `TestSwitchRetiredEntryRefused` (open, rebuild, job start, old listing,
  the open route's 409, create without a root),
  `TestSwitchInsideLibraryRefused`; `TestAppWorkspaceSwitch` now expects
  a workspace library to stay locked when the workspace is chosen again
  (hand-over) and released on a switch elsewhere.
- **16 (left click)**: the tray's left click opens the window on Windows
  and Linux alike (`SetOnTapped`); the right click shows the menu.
  Documented in `tray.go`, here and in the README.
- **Unverified items**: Chrome-style app mode adds `--no-first-run
  --no-default-browser-check`; `writeFileAtomic` retries the rename up to
  3 times, 50 ms apart, on a Windows sharing, lock or access violation
  (`app.json`, `instance.json`). The other unverified items (Ctrl+C
  reaching the app-mode Chrome child, large-thumbnail derivations after a
  switch) are unchanged.
