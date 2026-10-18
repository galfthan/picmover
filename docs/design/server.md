# Step 3: local server and web UI

Status: contract for implementation. Steps 1 and 2 (`core-restructure.md`,
`thumbnails.md`) are merged; this step adds the first user interface.

## Goals

- `picmover serve <library>` starts a local HTTP server, opens the browser,
  and serves a single-page app for **browsing** (date-ordered grid, lightbox,
  map) and **importing** (wizard with live progress). Management (ratings,
  tags, albums, trash) is step 4 and is out of scope here, but nothing in
  this step may make it harder.
- Fast with 100k+ files: the grid must scroll anywhere instantly, requests
  must stay small, and thumbnails must be served straight from the cache.
- One binary, no external runtime: the built frontend is embedded with
  `embed`. Windows native is the primary runtime; Linux and macOS are equal
  targets. CGO stays off, `-tags nodynamic` stays on.
- Browser first, Wails shell later: the frontend talks to `/api/...` with
  absolute paths and uses hash routing, so the same build runs under Wails'
  asset server or any local HTTP origin. The server package exposes an
  `http.Handler`, not just a listener.
- Safe to run on a machine that also browses the internet: the API can read
  any directory and start imports (which can move files), so it must be
  reachable only from this machine and only by its own page (see Security).

## Non-goals

- Authentication, multi-user, remote access (LAN use is possible with
  `--addr` but not supported beyond a warning).
- Editing, deleting or rating media (step 4). Search and AI (step 6).
- Playwright or browser-driven tests; a built-in HTTPS; gzip (localhost).

## Toolchain

- Node 24 LTS (installed on the dev box under `~/.local/node`, symlinked
  into `~/.local/bin`). The Makefile prepends `$(HOME)/.local/node/bin` to
  `PATH` when that directory exists.
- Svelte 5 (runes), TypeScript strict, Vite 7, `svelte-check`, `vitest` for
  pure functions. Runtime dependencies: `leaflet` only. No UI framework, no
  router library (a hash router is ~40 lines), no state library (runes).
- Every library version is pinned exactly in `web/package.json`;
  `web/package-lock.json` is committed.

## Layout

```
web/                      Svelte project
  package.json, package-lock.json, vite.config.ts, tsconfig.json, svelte.config.js
  index.html
  src/
    main.ts, App.svelte, app.css
    lib/api.ts            typed client for every endpoint
    lib/router.ts         hash router
    lib/timeline.ts       bucket layout math (pure, unit-tested)
    lib/format.ts         wall-clock date/size/duration formatting (pure, unit-tested)
    lib/jobs.svelte.ts    job state shared by pages (SSE subscriber)
    pages/Grid.svelte, Map.svelte, Import.svelte, Library.svelte
    components/Tile.svelte, Lightbox.svelte, Filters.svelte, FolderPicker.svelte,
               JobProgress.svelte, Scrubber.svelte, ...
  dist/.gitkeep           build output; everything else in dist/ is ignored
  embed.go                package web: `//go:embed all:dist`, `func Dist() (fs.FS, bool)`
internal/server/          HTTP API, jobs, static serving (no cobra, no stdout)
  server.go               New, Options, Handler, routes, middleware
  security.go             loopback/Host/Origin checks, headers
  api_library.go          /api/library
  api_media.go            buckets, items, item details
  api_files.go            thumb, preview, original
  api_geo.go              clusters, bbox items
  api_fs.go               directory browser
  api_jobs.go             jobs + SSE
  jobs.go                 job manager (importer runs, events, ring buffer)
  browser.go              open-in-browser per OS
  *_test.go
internal/catalog/query.go Buckets, BucketItems, Cameras, GeoPoints, Filter
cmd/serve.go              `picmover serve`
```

`web.Dist()` returns the `dist` sub-FS and `true` when `dist/index.html`
exists; otherwise `false`, and the server answers `/` with a plain-text
page saying the frontend is not built (`make web`). `go build` must never
require Node.

## Catalog additions (`internal/catalog/query.go`)

All read-only, all honouring `deleted_at IS NULL` unless `IncludeDeleted`.

```go
// Filter narrows the browsing queries. Zero values mean "no constraint".
type Filter struct {
    FileType       string    // "" | image | image_raw | video
    HasGPS         bool
    CameraModel    string    // exact match on camera_model
    SessionID      int64     // import_session_id
    From, To       time.Time // wall-clock bounds on taken_at (To exclusive); zero = open
    IncludeDeleted bool
}

// Bucket is one calendar month of dated rows, or the undated rows
// (Year == 0 && Month == 0), which sort last.
type Bucket struct {
    Year, Month int
    Count       int64
}

// Buckets returns the months matching f, newest first, undated last.
// Implemented with one GROUP BY over strftime('%Y-%m', taken_at, 'unixepoch');
// taken_at is wall-clock-as-UTC so no zone conversion is applied.
func (d *DB) Buckets(ctx context.Context, f Filter) ([]Bucket, error)

// BucketItems returns the rows of one bucket ordered taken_at DESC, id DESC
// (undated: id DESC). The month range is a half-open [first, next-first)
// pair of Unix wall-clock seconds so the taken_at index is used.
func (d *DB) BucketItems(ctx context.Context, f Filter, year, month int) ([]Media, error)

type Camera struct {
    Make, Model string
    Count       int64
}

// Cameras lists distinct (camera_make, camera_model) with counts, most
// frequent first, empty model excluded.
func (d *DB) Cameras(ctx context.Context) ([]Camera, error)

type GeoPoint struct {
    ID      int64
    Hash    uint64
    Lat, Lon float64
    TakenAt time.Time
}

// GeoPoints returns every row with coordinates matching f inside the box
// (west, south, east, north; west > east crosses the antimeridian). Limit 0
// = no limit.
func (d *DB) GeoPoints(ctx context.Context, f Filter, west, south, east, north float64, limit int) ([]GeoPoint, error)
```

`Summary` and `ThumbStats` already exist and are reused. No schema change.

Add a `media_taken_id` composite index? No: `media_taken_at` already serves
the month range + `ORDER BY taken_at DESC, id DESC` is a sort of at most one
month. Keep the schema at v3.

## HTTP API

JSON (`application/json; charset=utf-8`), errors as
`{"error":"message"}` with the status code. Everything under `/api/`. All
other paths serve the SPA: files from `web.Dist()` with their content
types, and `index.html` for any path that is not a file (hash routing
does not need this, but keep it so deep links work later).

Hashes are 16 lower-case hex digits in JSON and in URLs (JavaScript has no
uint64). Times are wall-clock ISO strings without a zone,
`"2024-07-14T13:05:22"`, with the offset given separately. IDs are numbers.

### Media item JSON

Compact item (lists):

```json
{"id":123,"hash":"9f3a...","type":"image","takenAt":"2024-07-14T13:05:22",
 "w":4000,"h":3000,"dur":0,"thumb":"ok","gps":true}
```

`takenAt` is `null` when unknown. `thumb` is the thumb_state. `dur` is
milliseconds, omitted when 0. `gps` omitted when false. `thumbAt` (integer
Unix seconds of the last thumbnail run, omitted when never) versions the
rendition URLs: `/api/thumb/<hash>?v=<thumbAt>`, `/api/preview/<hash>?v=<thumbAt>`.

Detail (`/api/media/{id}`) adds: `relPath`, `ext`, `size`, `offset`
(seconds east of UTC, `null` when unknown), `takenAtSource`,
`orientation`, `lat`, `lon`, `altitude` (absent when unknown),
`cameraMake`, `cameraModel`, `cameraType`, `lens`, `originalPath`,
`importedAt` (RFC 3339 with zone, it is a real instant), `sessionId`,
`rating`, `phash` (hex or `null`), `thumbState`, `thumbError`,
`displayable` (bool: the browser can show the original, see below).

### Endpoints

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/library` | `{root, version, summary:{total, images, raw, videos, withGps, deleted, bytes, earliest, latest}, thumbs:{pending, ok, unsupported, error}, cameras:[{make, model, count}], ffmpeg: bool}` |
| GET | `/api/timeline?type=&gps=1&camera=&session=&from=&to=` | `{buckets:[{year, month, count}, ...]}` undated last as `{year:0, month:0}` |
| GET | `/api/media?year=&month=&...filters` | `{items:[...]}` of one bucket (required year+month; `0`,`0` = undated) |
| GET | `/api/media/{id}` | detail object, 404 when missing or deleted |
| GET | `/api/thumb/{hash}` | 256 px JPEG from thumbs.db; 404 `{"error":"no thumbnail"}` when absent |
| GET | `/api/preview/{hash}` | 1600 px JPEG from previews/; 404 when absent |
| GET | `/api/file/{id}` | the original, `http.ServeContent` (Range), `Content-Disposition: inline; filename="<base>"` (and `?download=1` → attachment) |
| GET | `/api/geo?bbox=w,s,e,n&zoom=z&...filters` | `{clusters:[{lat, lon, count, id?, hash?}]}` (see Map) |
| GET | `/api/geo/items?bbox=w,s,e,n&limit=200&...filters` | `{items:[...], total}` newest first |
| GET | `/api/fs?path=` | directory browser (see Import) |
| GET | `/api/jobs` | `{jobs:[job, ...]}` newest first, this process only |
| POST | `/api/jobs` | start a job; body below; 201 with the job object; 409 when one is running |
| GET | `/api/jobs/{id}` | job state |
| GET | `/api/jobs/{id}/events` | SSE stream |
| POST | `/api/jobs/{id}/cancel` | 202 with the job object; idempotent |

Filters accepted by timeline, media, geo and geo/items: `type`
(`image|image_raw|video`), `gps=1`, `camera=<model>`, `session=<id>`,
`from`/`to` (`YYYY-MM-DD`, wall-clock, `to` exclusive). Unknown or
malformed values → 400.

Rendition caching: `/api/thumb` and `/api/preview` take an optional
`v` (the row's `thumbAt`; 1–20 digits, anything else counts as absent)
and send `ETag: "<hash>-<thumbs.FormatVersion>-<v or 0>"`,
`Cache-Control: private, max-age=604800`, and answer `If-None-Match`
with 304. A regenerated rendition (rotation fix, `thumbs --rebuild`) has
a new `thumbAt`, hence a new URL and ETag. A 404 for a pending
thumbnail is `Cache-Control: no-store`. `/api/file` sends `ETag` from hash
and size, `Cache-Control: private, max-age=3600`, and the content type from
the extension (`image/jpeg`, `image/png`, `image/gif`, `image/webp`,
`image/avif`, `image/heic`, `image/bmp`, `image/tiff`, `video/mp4`,
`video/quicktime`, `video/webm`, `video/x-matroska`, ...; RAW and anything
else `application/octet-stream`). `displayable` in the detail object is
true for jpeg/png/gif/webp/avif/bmp and for mp4/webm/mov; false for heic,
tiff and RAW (the lightbox then shows the preview only and offers
download).

File paths come only from catalog rows (`lib.Abs(rel_path)`), never from the
request. The `rel_path` must have a library path's shape (non-empty
components, no `.`/`..`, no `\`, `:` or NUL; the first component is not
`.picmover` and does not start with `media.db`, case-insensitively after
trimming trailing dots and spaces), satisfy `lib.Contains` and
`!lib.IsInternal` both lexically and after `filepath.EvalSymlinks`
(symlinked or junction directories must resolve inside the library), and
`Lstat` must say the last component is a regular file (a symlinked row is
refused). Every refusal and malformed path is 404.

### Map clustering (`/api/geo`)

Server-side grid clustering: project every `GeoPoint` in the box with
web-mercator at `zoom`, bucket into 64 px cells, and return one cluster per
cell with the mean position and count; a cell with one point carries its
`id` and `hash` so the client can show the thumbnail and open the lightbox.
Clients request on every `moveend`; a request without `bbox` or `zoom` is a
400. `zoom` 0–22. Bounded output: at most ~1000 clusters for any zoom (64 px
cells over a viewport). The scan is at most the number of points with GPS;
fine for 100k.

### Directory browser (`/api/fs`)

`GET /api/fs?path=` with an empty `path` returns the roots:
`{path:"", dirs:[{name:"C:\\", path:"C:\\"}, ...]}` (Windows: every logical
drive via `windows.GetLogicalDrives`; Unix: `/`, the home directory and
`/media/<user>`, `/mnt`, `/Volumes` when they exist). With a path:
`{path:<cleaned absolute>, parent:<or "">, dirs:[{name, path}], mediaFiles:<count>, otherFiles:<count>}`
where counts come from one non-recursive `ReadDir` + `metadata.Classify`
(zip counts as media). Hidden entries (dot-prefixed, and on Windows the
hidden attribute) are omitted. A relative, non-existent or unreadable path
→ 400/404 with the OS message. UNC and device paths (`\\server\share`,
`//server/share`, `\\?\`, `\\.\`, `\??\`) and paths containing NUL → 400
on every OS, checked lexically before any filesystem call
(`server.ValidSourcePath`, also for an import job's `source`). Symlinked directories are listed but not
followed by the counter. Never lists files, only directories and counts.

### Jobs

```json
POST /api/jobs
{"kind":"import","source":"D:\\DCIM","move":false,"minDimension":0,
 "timezone":"Europe/Helsinki","noThumbs":false}
{"kind":"thumbs","rebuild":false,"limit":0}
```

Job object:

```json
{"id":3,"kind":"import","state":"running","source":"D:\\DCIM","move":false,
 "startedAt":"2026-10-10T12:00:00+03:00","finishedAt":null,
 "stats":{"scanned":120,"imported":80,"existing":3,"duplicates":10,"skippedSmall":2,
          "nonMedia":5,"errors":0,"bytesCopied":123456789,
          "rate":12897485,"thumbsDone":40,"thumbsUnsupported":0,"thumbsFailed":0},
 "sessionId":17,"logPath":"/lib/.picmover/logs/import_2026-10-10_12-00-00.log",
 "error":null,"exitCode":0}
```

`state` ∈ `queued|running|done|failed|cancelled|disconnected` (disconnected: an import whose source went away, exit code 2, `error` the one status message; `stats.sourceDisconnected` is set). `exitCode` mirrors the
CLI (0, 2 for per-file or thumbnail failures, 130 cancelled, 1 otherwise)
so the UI can word the summary the same way. `sessionId` is the catalog
import session once known (needed for "show what this import added").

Rules: one job runs at a time per process (409 otherwise). A job runs the
same code path as the CLI: `library.Open` for imports (creating a library
is allowed through the UI? Yes, indirectly: `serve <dir>` creates the
library when the directory is absent or empty, so a first import can be
started from the browser; imports go into the served library only), `importer.New(...).Run` /
`RunThumbs`, a per-job log file under `<library>/.picmover/logs/` using the
same naming as the CLI. Move `openLog` into `library` as
`(*Library).OpenLog(prefix string) (*os.File, string, error)` and make cmd
use it. `timezone` is an IANA name resolved with `time.LoadLocation`
(400 when unknown); empty = UTC like the CLI default. The import source must
be an absolute path to an existing directory or file; it may not be the
library's `.picmover` directory.

Events (SSE, `text/event-stream`, `Cache-Control: no-store`, a retry hint
of 2000 ms, a `: ping` comment every 15 s):

```
event: state     data: {job object}          (on every state change; first message on connect)
event: progress  data: {"stats":{...}}       (every 250 ms while running, when changed)
event: result    data: {"status":"imported","source":"...","dest":"...","message":"...","seq":42}
event: done      data: {job object}          (final, then the stream closes)
```

`result` events are sent for every importer result except `non_media`
(those are counted only). The manager keeps the last 500 results per job
in a ring; a client connecting late first receives `state`, then the ring,
then live events. Slow subscribers are dropped (closed) rather than blocking
the importer: the per-subscriber channel has a buffer of 1024 and a full
buffer closes the stream (the client reconnects and gets the ring). A
`progress` event is coalesced: only the latest stats are sent.

`stats.rate` is the import transfer rate in bytes per second, computed in
the importer (`importer.Stats.Rate`, the growth of `bytesCopied` over about
the last two seconds of progress ticks); it is 0 while nothing is moving
and in the final stats of `done`. The web UI shows it after the copied
size as `(12.3 MiB/s)` only while it is positive.

Cancel: `POST /api/jobs/{id}/cancel` cancels the job context; the importer
already handles that safely (batch commit or undo). Shutting the server
down cancels the running job and waits for it (bounded by 30 s) before
closing the catalog.

Completed jobs stay in memory (last 20) so the summary page survives a
reload while the process runs.

After a job ends the frontend refetches `/api/library` and the timeline;
tiles whose thumbnail 404ed retry once on `done`.

## Security

The server is a local tool with write power, so:

1. **Loopback only by default.** `--addr` defaults to `127.0.0.1:7439`. If
   that port is busy and `--addr` was not given, pick a free port
   (`127.0.0.1:0`) and print it. A non-loopback `--addr` prints a warning.
2. **Host header check** (DNS rebinding): the `Host` must be the listening
   address or `localhost[:port]`/`127.0.0.1[:port]`/`[::1][:port]` with the
   right port; otherwise 421 with a plain-text body. Applies to every
   request including static files.
3. **Origin check**: when an `Origin` header is present it must be
   `http://<accepted host>` (any of the forms above) or one of
   `--cors-origin`; otherwise 403. When it matches a `--cors-origin` value
   the response carries `Access-Control-Allow-Origin` for that origin
   (plus `Vary: Origin`, allowed methods/headers for preflight). Without
   `--cors-origin` no CORS header is ever sent. `--cors-origin` values are
   normalised like browsers send them (lower case, no default port:
   `http://localhost:80` → `http://localhost`).
   **Sec-Fetch-Site** (requests without an Origin: img/video loads,
   navigations): `cross-site` → 403; `same-site` → 403 unless the Origin
   is a `--cors-origin`; `same-origin`, `none` and a missing header pass.
   Exception: a top-level navigation (`Sec-Fetch-Mode: navigate`, and
   `Sec-Fetch-Dest: document` when sent) to a path outside `/api/` passes
   whatever the site (links to the app from other sites keep working;
   the shell does nothing by itself). `/api/` paths are never exempt.
   Every response carries `Cross-Origin-Resource-Policy: same-origin`.
4. **Mutating requests** (every POST) require `Content-Type:
   application/json` and the header `X-Picmover: 1` (forces a CORS preflight
   for any cross-origin caller, which fails without 3). 403 otherwise.
5. **Response headers** on everything: `X-Content-Type-Options: nosniff`,
   `Referrer-Policy: no-referrer`, `Cache-Control: no-store` on API JSON.
   The HTML page gets a CSP: `default-src 'self'; img-src 'self' data: blob:
   https://tile.openstreetmap.org https://*.tile.openstreetmap.org;
   media-src 'self'; connect-src 'self';
   style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors
   'none'; base-uri 'none'`. (`unsafe-inline` for styles is needed by
   Leaflet's positioning; scripts stay strict.)
6. **Path handling**: `/api/fs` is the only endpoint that takes a path and
   it lists directories only; `/api/file` resolves by id. Nothing writes
   outside the library and the import source (`--move`). Request paths
   (`/api/fs`, an import `source`) must be absolute, without NUL, and not
   UNC or device paths (400, lexical check before any filesystem call).
   `/api/file` rules: see "HTTP API".
7. **No request body over 64 KiB** (`http.MaxBytesReader`), a 30 s read
   deadline on request bodies, `ReadHeaderTimeout` 10 s, no `WriteTimeout`
   (SSE and large files), `IdleTimeout` 120 s.
8. **Errors and logs**: 500 bodies are `{"error":"internal error"}`, the
   detail is logged; a request whose client has gone away gets nothing and
   logs at Debug. Refused requests (421/403) log at Debug only (`serve
   --verbose`), so a hostile page cannot flood the terminal.

Tests must cover 2, 3 and 4 explicitly (wrong Host → 421, foreign Origin →
403, POST without the header → 403, preflight without `--cors-origin` → no
CORS headers).

## `picmover serve`

```
picmover serve <library> [--addr 127.0.0.1:7439] [--no-open] [--cors-origin URL]...
```

- Opens the library like `import` does (`library.Open` + `catalog.Open`,
  creating both when absent and printing `(new)`); the catalog is opened
  read-write (jobs need it), the thumb store read-only for serving
  (`thumbs.OpenStoreReadOnly`); jobs open their own writer store as today
  (`RunThumbs` does). The read-only store sees committed WAL writes.
- Prints `Serving <root> at http://127.0.0.1:7439` and opens the browser
  (`xdg-open` / `open` / `rundll32 url.dll,FileProtocolHandler` on Windows,
  via `exec.Command` with the URL as a single argument, never a shell) unless
  `--no-open` or stdout is not a terminal.
- Ctrl+C: cancel the running job, `Shutdown` with a 5 s grace, close store
  and catalog, exit 0 (130 is for cancelled *work*, and a shutdown is the
  normal way to stop a server).
- Logging: server log lines (requests at debug, errors at info) go to
  stderr via `slog`; `--verbose` enables request logging. Jobs log to their
  own file as before.
- `version` in `/api/library` comes from `var Version = "dev"` in package
  `cmd`, overridable with `-ldflags -X picmover/cmd.Version=...`; the
  Makefile passes the `git describe` output when available.

Dev loop: `picmover serve <lib> --no-open --cors-origin http://localhost:5173`
and `npm run dev` in `web/` (Vite proxies `/api` to `127.0.0.1:7439`, so the
cors flag is only needed when bypassing the proxy; document both).

## Frontend

### Shell

- Top bar: library name (root base name), nav (Photos, Map, Import,
  Library), a job pill ("Importing 1,234 files…" with a progress fraction
  when known, click → Import page) fed by `jobs.svelte.ts`, which keeps one
  SSE connection open while a job runs.
- Hash routes: `#/` grid, `#/map`, `#/import`, `#/library`, `#/view/<id>`
  (lightbox over the grid; back closes it).
- Dark theme by default (photo tool), light via `prefers-color-scheme`
  honoured through CSS variables; no theme toggle yet.
- Keyboard: `/` focuses filters, `Esc` closes lightbox, arrows navigate.

### Grid (`#/`)

Immich-style virtual timeline:

1. Load `/api/timeline` for the current filters → buckets.
2. Layout (pure, in `timeline.ts`): tile size `s` (default 160 CSS px,
   `Ctrl`+wheel or a slider 100–320 px), gap 4 px, columns =
   `floor((width + gap) / (s + gap))`. Each bucket contributes a header row
   (month name + year + count, sticky while in view) and
   `ceil(count / columns)` tile rows. Total height is known up front, so
   the scrollbar is right from the start and the user can jump anywhere.
3. Render only buckets intersecting the viewport ± one screen; fetch
   `/api/media?year&month` for a bucket the first time it is needed and keep
   it (memory is not a concern at 100k items; a simple map). Within a
   rendered bucket render only the visible rows (plus overscan) as
   absolutely positioned tiles.
4. Tile: square, `object-fit: cover`, `<img loading="lazy"
   decoding="async" src="/api/thumb/<hash>">`, a placeholder background
   while loading or when 404 (pending), a small badge for video (duration)
   and RAW, hover shows the date. Click opens the lightbox; the URL becomes
   `#/view/<id>`.
5. Scrubber on the right edge: one label per year (derived from buckets),
   drag/click jumps to that year; shows the month under the pointer.
6. Filters bar: type, "has location", camera (from `/api/library`), year
   range (from/to), and "this import" when arriving from a finished job
   (`session`). Changing a filter reloads the timeline and resets scroll.
7. Empty states: no library content (points at Import), no match.

### Lightbox (`#/view/<id>`)

- Shows `/api/preview/<hash>`; while it loads shows the thumbnail scaled
  (blur-up). When `displayable` is true, a "1:1" button swaps to
  `/api/file/<id>` (and double-click toggles it). Videos use
  `<video controls preload="metadata" src="/api/file/<id>">` with the
  preview as `poster`. For undisplayable originals, a Download button
  (`?download=1`).
- Prev/next within the loaded timeline order (the grid's flattened item
  list; crossing into an unloaded bucket loads it first). Preload the
  neighbours' previews.
- Info panel (toggle with `i`): date (formatted from the wall-clock string
  with `timeZone: 'UTC'`, plus the offset when known, plus the source
  (`exif`, `mtime`, …) in small text), camera make/model, lens, dimensions
  and megapixels, duration, size, type/ext, path (relPath, with a copy
  button), GPS as decimal degrees with a "Show on map" link
  (`#/map?at=<lat>,<lon>&z=15`), import date and session.
- Keyboard: `←/→`, `Esc`, `i`, `f` (fit/1:1), space (video play/pause).

### Map (`#/map`)

- Leaflet 1.9.x with OSM raster tiles and the required attribution.
  Marker clusters from `/api/geo` on every `moveend` (debounced 150 ms) as
  `L.divIcon`s: count bubbles for clusters, a 40 px thumbnail
  (`/api/thumb/<hash>`) for singles. Click: cluster → zoom in 2 levels at
  the cluster; single → lightbox. No marker image assets (Leaflet's default
  icon images do not survive bundling; `divIcon` only).
- Side panel lists `/api/geo/items` for the current view (thumbnails,
  newest first, "200 of 5,432"), click → lightbox. Filters bar shared with
  the grid.
- Remembers the last view in `localStorage`; `?at=lat,lon&z=` from the
  lightbox link overrides it.
- Offline: tiles fail, markers still show; a note says tiles need internet.

### Import (`#/import`)

Wizard with four states:

1. **Source**: a `FolderPicker` (uses `/api/fs`: breadcrumb, folder list,
   media/other counts per folder, text field to paste a path, roots
   button). Only directories can be chosen (a single file is CLI-only).
2. **Options**: copy or move (move explained: source files are deleted
   after each committed batch), minimum dimension, time zone (select with
   IANA zones from `Intl.supportedValuesOf('timeZone')`, default from the
   browser, explained as "used for files without zone information"), skip
   thumbnails. "Start import".
3. **Running**: counters (scanned, imported, existing, duplicates, skipped
   small, errors, bytes), elapsed time, a scrolling list of the last 200
   results with status colours (errors pinned at the top with their
   message), a Cancel button with confirmation built into the page (no
   `confirm()`), and the thumbnail stage counters when it starts.
4. **Done / failed / cancelled**: summary in the CLI's words, exit-code
   based wording ("12 files could not be imported; see <log>"), the log
   path with a copy button, buttons "Show imported" (grid with
   `session=<id>`) and "Import another".

The page restores the current job on load (from `/api/jobs`), so a refresh
mid-import lands in state 3.

### Library (`#/library`)

Summary numbers, date range, bytes, cameras table (model, count), thumbnail
state counts with "Generate missing thumbnails" (thumbs job) and
"Regenerate all" (rebuild=true, with built-in confirmation), job history
of this process, root path, version, ffmpeg availability.

### Client code rules

- `api.ts` is the only place that knows URLs; every call sets
  `X-Picmover: 1` on POST and sends JSON. Errors throw `ApiError{status,
  message}`; pages show them inline.
- Times: never `new Date(takenAt)` without forcing UTC rendering; `format.ts`
  owns this and has tests for a wall-clock string, an offset, and the
  undated case.
- No `alert/confirm/prompt`.
- `svelte-check` clean, `tsc --noEmit` clean, `vitest` green, `npm run build`
  warnings-free. ESLint is not required.

## Makefile and ignores

```
export PATH := $(HOME)/.local/node/bin:$(PATH)   # only if the dir exists
web:            cd web && npm ci && npm run build
web-check:      cd web && npm ci && npm run check && npm test
build:          web + go build (as today, plus -X picmover/cmd.Version)
build-all:      web + the four cross builds
check:          vet + test + web-check
```

`.gitignore` adds `web/node_modules/`, `web/dist/*`, `!web/dist/.gitkeep`.
`web/dist/.gitkeep` is committed so `//go:embed all:dist` always matches.

## Tests (Go)

`internal/server` tests build a real library in `t.TempDir()` with
`testimg` fixtures imported through `importer` (so thumbs exist), then
exercise the handler with `httptest`:

- `/api/library`, `/api/timeline` and `/api/media` agree with direct
  catalog queries; undated bucket last; each filter; bad filter → 400.
- `/api/media/{id}`: detail fields incl. `displayable`, 404 for deleted.
- `/api/thumb`, `/api/preview`: bytes equal the store's, ETag/304, 404 for
  pending with `no-store`, malformed hash → 400.
- `/api/file`: Range request, content type, `download=1`, symlinked row →
  404, row outside the library → 404.
- `/api/geo` clustering: two points in one cell merge, far apart don't;
  singles carry id/hash; antimeridian box.
- `/api/fs`: roots non-empty, hidden omitted, counts, relative path → 400,
  missing → 404.
- Jobs: start an import of a fixture directory, follow SSE until `done`,
  stats match the job object and the catalog; a second POST while running →
  409; cancel ends with state cancelled and exit code 130; thumbs job after
  `--no-thumbs` import leaves no pending rows; late subscriber gets the
  ring.
- Security: Host/Origin/header tests listed above; CORS only with
  `--cors-origin`.
- Static: with `web.Dist()` false `/` answers the not-built text; with a
  fake dist FS (test-only `Options.Static fs.FS`) `/` and `/#/map` serve
  index.html and `/assets/x.js` its file with `application/javascript`.
- `cmd/serve`: flag validation, port fallback when busy, `--no-open`.

Go tests must not need Node or a built frontend.

## Performance targets (100k files, dev box)

- `/api/timeline` < 150 ms cold, `/api/media` for a 5k bucket < 100 ms
  and < 600 KB, `/api/thumb` < 3 ms server time, `/api/geo` < 60 ms with
  100k GPS points. Record measurements in the "As implemented" section;
  `go test -bench` for Buckets/BucketItems/GeoPoints against a generated
  100k-row catalog (benchmark only, not run by default).
- Grid: scroll at 60 fps with 100k items; no layout thrash (tiles are
  absolutely positioned; measurements only on resize).

## Open questions settled

- Why month buckets and not keyset paging? The grid needs random access
  (drag the scrollbar to 2016) and a correct scrollbar height before
  loading everything. Counts per month give both; one request per visible
  month is small.
- Why server-side clustering? 100k points as JSON is ~3 MB per map move;
  a grid cluster pass is ~ms in Go and returns ≤ 1k items.
- Why not a lock between the UI job and a CLI import in another process?
  Same answer as step 2: not yet; documented limitation.

## As implemented

Go backend (catalog queries, `internal/server`, `picmover serve`, Makefile,
ignores, README). The Svelte app under `web/` is built separately against
this contract; the notes below that matter to it are marked **(frontend)**.

### API shapes, exactly

- **(frontend)** `POST /api/jobs` (201), `POST /api/jobs/{id}/cancel`
  (202, the job as it is at that moment) and `GET /api/jobs/{id}` (200)
  all answer the bare job object; `GET /api/jobs` answers
  `{"jobs": [...]}`. Errors everywhere are
  `{"error": "..."}`.
- **(frontend)** Job object: the fields of the contract, with `exitCode`
  `0` until the job has finished (read `state` first), `sessionId` and
  `finishedAt` `null` until known, `error` `null` or the CLI's message
  (also set for exit code 2, e.g. `"3 files could not be imported; see
  <log>"`; `null` when cancelled). Additional fields, omitted when zero or
  empty: `minDimension`, `timezone`, `noThumbs` (imports), `rebuild`,
  `limit` (thumbs jobs); `source` is omitted for thumbs jobs; `move` is
  always present.
- **(frontend)** Event stream: `retry: 2000`, then `state`, the ring
  (`result` events, oldest first), then live `state`/`result`/`progress`
  and finally `done`, after which the server closes the stream. A stream
  for a job that has already finished sends `state`, the ring and `done`
  and closes. EventSource reconnects after any close, so the client must
  call `close()` on `done`. A stream dropped for falling behind (1024
  buffered events) closes without `done`; reconnecting yields state + ring.
  `result.message` is the importer's message, with the error appended
  (`"catalog write failed: <err>"`) for error results.
- **(frontend)** Media detail: `sessionId` is `null` when the row has no
  import session; `lat`/`lon`/`altitude` are absent when unknown; `offset`
  is `null` when unknown (`OffsetKnown` false, e.g. a zoneless EXIF date).
  An `mtime`-dated row (like `video_utc`) has a known offset: the importer
  turns the instant into wall-clock in its zone (`--timezone`, else the
  local zone), so it reports that zone's offset, `0` when it was UTC; `importedAt` is RFC 3339 in the server's local
  zone. `displayable` also covers `m4v` (served as `video/mp4`).
- The HTML page's CSP `img-src` also allows `https://tile.openstreetmap.org`
  (OSM's current tile host) next to `https://*.tile.openstreetmap.org`.
- **(frontend)** `/api/fs` roots: `name` equals `path` (`"/"`,
  `"/home/user"`, `"C:\\"`). Windows drives are listed from
  `GetLogicalDrives` without being stat'ed (a disconnected network drive
  would stall the request). An unreadable directory is 400 with the OS
  message; a symlink to a file counts like a file.
- **(frontend)** `/api/geo/items`: `limit` defaults to 200, 1–1000 (else
  400). `/api/geo`: clusters are sorted by count, largest first; `bbox`
  longitudes outside ±180 (Leaflet's unwrapped bounds when panned across
  the antimeridian) are wrapped, and a box 360° or wider is the world.
- **(frontend)** Vite dev proxy: the server's Host check requires
  `changeOrigin: true` on the `/api` proxy, and browsers send `Origin:
  http://localhost:5173` on POSTs even through the proxy, so the dev loop
  is `serve --no-open --cors-origin http://localhost:5173` as written above
  (the README says so).
- Unknown `GET /api/...` → 404 JSON; another method on an API path → 405
  JSON. Security refusals: 421 plain text (Host); 403 JSON under `/api/`,
  plain text elsewhere (Origin, mutation headers). `Origin: null` is
  refused. A Host without a port is only accepted when listening on port
  80. With a wildcard `--addr` (`0.0.0.0`), any IP-literal Host with the
  right port is accepted (DNS rebinding needs a name).
- Not-built frontend: `/` and every non-API path answer 200
  `text/plain` "the web frontend is not built ... make web".
- Static files: content types from a fixed table (the OS tables are not
  used; on Windows they come from the registry), `.js` is
  `application/javascript; charset=utf-8`; `/assets/*` gets
  `Cache-Control: public, max-age=31536000, immutable` and a missing asset
  is 404 (never index.html); other files and index.html `no-cache`; the CSP
  is set on HTML responses only. Non-GET/HEAD → 405.

### Deviations and additions

- **Timezone default**: an import job with an empty `timezone` uses the
  system zone (`time.Local`), which *is* the CLI's default
  (`--timezone` "default the system zone"); the contract's "empty = UTC
  like the CLI default" was a slip. The frontend sends the browser's zone
  anyway.
- `server.Options` has `Addr` (the listening `host:port`, required: the
  Host and Origin checks need the port; `serve` passes the actual address
  after a port fallback) and an unexported `jobOptions` hook (tests use it
  to hold a job mid-run). `Static` without an `index.html` counts as "not
  built" (so tests do not depend on whether `web/dist` was built).
  `New` raises the catalog's `SetMaxIdleConns` to 8 (database/sql keeps 2,
  so the concurrent requests of a page load and the parallel geo scan would
  reopen connections). `(*Server).Shutdown(ctx)` cancels the running job
  and waits (≤ 30 s); new jobs are then refused with 503. On a timeout the
  job's streams are closed.
- `importer.(*Importer).SessionID()` was added (atomic; the session id was
  private and the job object needs it while the job runs).
- Catalog additions beyond the contract: `GeoItems(ctx, f, w, s, e, n,
  limit) ([]Media, total int64, error)` for `/api/geo/items` (GeoPoint
  lacks the item fields), `ValidFileType` and `FileType*` constants.
  `GeoPoints` with `limit > 0` is newest first (taken_at DESC, id DESC,
  undated last); with `limit 0` (the cluster pass) it is unordered
  (sorting 100k rows doubled the cost).
- Query plans: `BucketItems` names `INDEXED BY media_taken_at` so a filter
  matching another index (file_type, session) never takes over (a test
  checks the plan). For geo queries SQLite always picks the partial
  `media_latlon` index (no ANALYZE statistics), whose random row lookups
  cost ~4× a table scan when the box holds much of the catalog; an
  index-only `COUNT` capped at a quarter of the rows decides between the
  index and `NOT INDEXED`. A `limit 0` table scan is split into 4 id ranges
  read concurrently (WAL readers; the pure-Go SQLite is CPU-bound): 85 ms
  → 38 ms for the 100k world view. Still "one scan" of the rows.
- `Cameras` breaks count ties by model, then make, case-insensitively.
- Cluster cells: 64 px, doubled until the request's box spans at most
  4096 cells, so any `bbox`/`zoom` combination has bounded output.
- `/api/file` content types also cover avi, mpg, 3gp, wmv, asf, flv,
  mts/m2ts/ts and heif; the opened file is checked against the `Lstat`
  (`os.SameFile`), so a symlink swapped in between is refused too.
- `serve` creates an empty `thumbs.db` (via `thumbs.OpenStore`) when the
  library has none (imported with `--no-thumbs` and never thumbnailed),
  then opens it read-only. The root command gained `--version`.
- Makefile: `install` builds `web` first too. `.gitignore`: the existing
  `dist/` pattern was anchored to `/dist/`; unanchored it also matched
  `web/dist/`, and git cannot re-include `web/dist/.gitkeep` inside an
  excluded directory.
- Unrelated fix: `internal/library` `TestSymlinkedRoot` iterated a map of
  `Open`/`Attach` and failed whenever `Attach` came first (the directory
  did not exist yet); it now runs them in order.

### Tests

`internal/catalog/query_test.go` (buckets incl. pre-1970, wall-clock month
boundaries, each filter, undated last; bucket items order and range; the
`media_taken_at` plan; cameras; geo boxes incl. the antimeridian, limits,
the parallel scan), `internal/server/*_test.go` (a real library imported
through the importer with `testimg` fixtures, exercised through the
handler and, for event streams, a real listener: every endpoint's body,
headers and status codes; the security rules; static serving; jobs:
import followed over SSE with stats checked against the job object and
the catalog, 409, cancel → `cancelled`/130, late subscriber mid-run and
after the end, thumbs job after `noThumbs`, validation, shutdown cancels
and waits, shutdown timeout closes streams; unit tests for slow-subscriber
dropping, progress coalescing, the 500-result ring, job retention and the
exit-code mapping), `cmd/serve_test.go` (flag validation, port fallback,
explicit busy `--addr` is an error, `--no-open`/terminal browser opening,
`--cors-origin`, `--verbose` request logging), `library` `OpenLog`.

### Performance (100k rows, Ryzen 7 7800X3D, Linux, warm cache)

`go test -tags nodynamic -run '^$' -bench . ./internal/catalog/ ./internal/server/`

| Measurement | Result | Target |
|---|---|---|
| `Buckets` (catalog) | 81 ms | |
| `/api/timeline` (handler, incl. JSON) | 79 ms, 8.7 KB | < 150 ms |
| `/api/timeline?type=image&camera=…` | 24 ms | |
| `BucketItems` 5k bucket (catalog) | 16 ms | |
| `/api/media` 5k bucket | 21 ms, 650 KB | < 100 ms, < 600 KB |
| `/api/thumb` | 0.02 ms | < 3 ms |
| `GeoPoints` world, 100k points (catalog) | 33 ms | |
| `/api/geo` world, zoom 2, 100k GPS points | 38 ms | < 60 ms |
| `/api/geo` Europe, zoom 5 | 7.6 ms | |
| `/api/geo/items` world, limit 200 | 61 ms | |

The 5k-bucket response is 650 KB because the benchmark's rows all have
GPS (`"gps":true`, 11 bytes each) and 6-digit ids; with typical data
(half with GPS) it is ~600 KB. The item shape is the contract's; gzip is a
non-goal. `/api/geo/items` for a world box counts and sorts all 100k rows
(no index serves `taken_at` order within a box); zoomed-in boxes use the
lat/lon index and are a few ms.

### Review fixes (security)

From the adversarial review of the server (security and file serving):

- **Sec-Fetch-Site and CORP** (`security.go` `fetchSiteOK`, middleware):
  cross-site requests without an Origin (`<img src=…/api/fs?path=\\host\x>`,
  navigations) reached every GET; on Windows `/api/fs` would stat a UNC
  path and leak NTLM credentials. Now `cross-site` → 403, `same-site` →
  403 unless the Origin is a `--cors-origin` (the Vite dev server on
  another localhost port), others pass. Checked after Host and Origin.
  `Cross-Origin-Resource-Policy: same-origin` on every response, 421 and
  403 included. A cross-site top-level navigation (`Sec-Fetch-Mode:
  navigate`, `Sec-Fetch-Dest: document` when sent) to a non-`/api/` path
  is allowed, so links to the app from other sites work; a cross-site
  navigation to `/api/fs` etc. stays 403, and an iframe load is refused. A dev page on
  `localhost:5173` calling `127.0.0.1:7439` directly is cross-site
  (different hosts) and refused; use the Vite proxy as documented.
- **Network and device paths**: `server.ValidSourcePath(p) error` (absolute,
  no NUL, not `\\…`/`//…` in any mix of separators, not `\??\`) runs in
  `/api/fs` before any filesystem call → 400 `"network (UNC) and device
  paths are not supported"` / `"path contains a NUL byte"` / `"path must be
  absolute"`. Intended for the import job's `source` as well (api_jobs).
- **Rendition versions**: items and the detail gain `thumbAt`; `/api/thumb`
  and `/api/preview` take `?v=` and the ETag is
  `"<hash>-<FormatVersion>-<v or 0>"` (see "HTTP API").
- **`/api/file` resolution** (`openOriginal`): `rel_path` shape check
  (`validRelPath`) first, lexical `Contains`/`!IsInternal`, then
  `filepath.EvalSymlinks` and the same checks on the resolved path, then
  `Lstat` of the unresolved last component (regular, not a link) and the
  file opened at the resolved path must be that same file
  (`os.SameFile`). A link to a directory inside the library is still
  served; one leading outside or into `.picmover` is 404. On Windows the
  resolution also expands 8.3 names (`PICMOV~1`) and fixes case, so the
  internal check sees the real names. Where `EvalSymlinks` fails on
  Windows for a reason other than a missing file (volumes it cannot
  resolve, as in `library.Open`), the lexical checks alone apply, as
  before; on Unix such a failure is 404.
- **404 instead of 500**: not-exist, refused, `ENOTDIR`, `EINVAL`,
  `ENAMETOOLONG`, `ELOOP` (and on Windows `ERROR_INVALID_NAME`,
  `ERROR_DIRECTORY`) are 404 for `/api/file` and `/api/preview`; other
  errors are a generic 500. Static-file read errors are a generic 500 too.
- **Logging**: `internalError` logs at Error (was Info) and writes the
  generic body; when the request context is done it writes nothing and
  logs at Debug. Refusals log at Debug, which `serve` shows only with
  `--verbose` (it sets the handler level to Debug); no new option.
- **Body deadline**: requests other than GET/HEAD get a 30 s read
  deadline (`http.ResponseController.SetReadDeadline`); a trickled
  `POST /api/jobs` body then fails to decode (400). The server resets the
  deadline before the next request on the connection.
- **Static**: paths with a dot segment (`/.gitkeep`, `/.well-known/…`) are
  404; a missing `/assets`, `/assets/…` or any missing path whose last
  segment has an extension is 404 instead of `index.html`.
- **Windows `isHidden`** reads `FILE_ATTRIBUTE_HIDDEN` from the directory
  entry's `Win32FileAttributeData` (FindNextFile already returned it) and
  calls `GetFileAttributes` only when that is unavailable.
- Tests: `review_security_test.go` (Sec-Fetch-Site matrix over API, media
  and static paths, mutations and preflight; CORP on 200/403/404/421;
  `ValidSourcePath`; `/api/fs` UNC/device/NUL → 400; origin default
  ports; refusal log levels; generic 500 and abandoned requests; body
  read deadline over a real connection), `review_files_test.go`
  (`?v=` ETags and 200 for an old tag; `thumbAt` in item and detail;
  `validRelPath`; malformed/internal-spelled rows incl. NUL → 404 without
  the path in the body; symlinked directories outside, into `.picmover`
  and inside the library; static dot files and extension 404s).

### Review fixes (jobs, locking, CLI)

From the adversarial review of concurrency, jobs, data correctness and the
CLI:

- **Library lock** (`internal/library/lock.go`):
  `(*Library).Lock(ctx, name string) (unlock func(), err error)` takes an
  exclusive, non-blocking OS lock on the held-open file
  `<root>/.picmover/lock` (`flock(LOCK_EX|LOCK_NB)` on Linux, macOS and the
  BSDs; `LockFileEx(EXCLUSIVE|FAIL_IMMEDIATELY)` on one byte at offset
  2^40 on Windows, so the holder text at the start stays readable despite
  mandatory byte-range locks). The kernel releases it when the process
  ends, so there are no stale locks. The holder truncates the file and
  writes `pid=<pid> cmd=<name> since=<RFC3339>`; a refused caller reads it
  into `*library.ErrLocked`, whose message is `library is in use by another
  picmover process (pid N, <cmd>, since <local time>)`. Two `Lock` calls
  in one process conflict too (separate open files). When the file system
  cannot lock (`EOPNOTSUPP`/`ENOTSUP`/`ENOLCK`/`ENOSYS`, Windows
  `ERROR_NOT_SUPPORTED`/`ERROR_INVALID_FUNCTION`), `Lock` returns a no-op
  unlock and an error wrapping `library.ErrLockUnsupported`; the CLI
  (`cmd.lockLibrary`) prints a warning and goes on without the lock.
  `name` is an addition to the requested `Lock(ctx)` signature: it fills
  `cmd=`. Held by `serve` (taken after the catalog checks, for the whole
  run), `import`, `rebuild` (from before the backup rename to the end of
  its import), `thumbs`, `update-metadata` and `verify --rehash/--fill-size`
  (taken after the catalog is opened, so a refused v2 catalog still leaves
  the folder untouched). `db` and plain `verify` take no lock. The lock
  file is under `.picmover`, so `IsInternal` covers it and the importer
  skips it. Jobs inside `serve` need no lock of their own: the server's
  lock covers them, and one job runs at a time.
- **`serve` creates only in an absent or empty directory**
  (`openServeLibrary`): an existing directory with no `media.db` must be
  empty apart from a `.picmover` directory; an existing 0-byte `media.db`
  is initialised and reported `(new)`. Otherwise it fails with "… is not a
  picmover library (it has no media.db) and is not empty …" naming
  `picmover rebuild <dir>` and `picmover import <source> <dir>`, and
  creates nothing. A catalog that exists but cannot be opened (not
  `ErrNeedsRebuild`) fails with "the catalog <path> is unreadable: <err>"
  plus the rebuild hint. A `thumbs.db` of another format fails `serve`
  with the CLI's hint ("delete it (and the previews directory) to
  regenerate the cache"), now also given by `OpenStoreReadOnly`.
- **`rebuild` needs an existing directory** (`library.Attach`), so a
  mistyped path no longer creates a library. The backups note now reads
  "… they are left alone. To use one instead, delete the new media.db (and
  its -wal/-shm files) afterwards and rename the backup (and its
  -wal/-shm/-journal files, if any) back to media.db."
- **Error results survive the ring** (`jobs.go`): besides the 500-result
  ring, error results go to a 200-entry error ring. Replay (`ringLocked`)
  sends the errors older than the general ring's oldest seq, then the
  general ring: seq order, every result once.
- **SSE subscriber cap**: at most 32 streams per running job
  (`maxSubscribers`); the next `GET /api/jobs/{id}/events` is 503 JSON
  `{"error":"too many event streams for this job (at most 32)"}`, checked
  before the stream headers. A finished job's replay is not limited.
- **Import `source`** is checked with `ValidSourcePath` (UNC, device
  paths, NUL, relative → 400 `source: …`) before any `os.Stat`.
- **Thumbnail-stage message** quotes the root with `library.QuoteArg`
  (moved from `cmd.quoteArg`, which now delegates), so the suggested
  `picmover thumbs "<root>"` can be pasted.
- **Unique log names**: `library.OpenLog` creates with `O_EXCL` and tries
  `<prefix>_<time>.log`, then `-2.log`, `-3.log`, … (up to 1000), so two
  jobs (or CLI runs) in one second never share a log.
- **Out-of-range dates** (`catalog/query.go`): `taken_at` outside
  0001-01-01 00:00:00 … 9999-12-31 23:59:59 (`minDatedTakenAt`
  -62135596800, `maxDatedTakenAt` 253402300799) is undated in both
  `Buckets` (a `CASE … BETWEEN` around `strftime`) and `BucketItems(0, 0)`
  (`taken_at IS NULL OR < min OR > max`; the plan still uses
  `media_taken_at`). Year 0, negative years and year 10000 land in the
  undated bucket and `/api/media?year=0&month=0` returns them; `Buckets`
  no longer fails on them.
- **`/api/geo/items` consistency**: `GeoItems` reads items and `total` in
  one statement (`COUNT(*) OVER ()` evaluated before the `LIMIT`). A
  zero-width bbox (`west == east`) matches points at exactly that
  longitude; on the antimeridian (`±180`) it becomes the crossing box
  `[180, -180]`, matching points stored at either 180 or -180.
- **Preview rename retry** (`thumbs/store.go` `renameRetry`): every rename
  in `Put` (to `.old`, into place, and the restore) is tried up to 5 times
  50 ms apart (not for a missing source), for Windows sharing violations
  while the preview is being served.
- Tests: `library/lock_test.go` (exclusive in one process with holder
  info and message, idempotent unlock, relock, file cleared, MetaDir
  created, internal; unique log names; `QuoteArg`); `cmd/serve_test.go`
  (`TestServeCreatesOnlyInEmptyDir`, `TestServeCorruptCatalog`,
  `TestServeLocksLibrary`: `rebuild`, `import`, `thumbs` and a second
  `serve` refuse while serving and `db` works; no backup is made);
  `cmd/rebuild_test.go` (`TestRebuildRequiresDirectory`, the new note);
  `catalog/query_test.go` (`TestBucketsOutOfRangeYears`, zero-width geo
  box); `server/server_jobs_review_test.go` (5 early errors among 600
  results replayed in seq order, error-ring cap, subscriber cap and 503,
  UNC/device/NUL sources, quoted root, zero-width bbox parsing);
  `thumbs/store_rename_test.go` (retry helper with an injected rename).
