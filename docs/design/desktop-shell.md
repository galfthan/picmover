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
