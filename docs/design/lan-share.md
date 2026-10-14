# Sharing on the home Wi-Fi (phone viewing)

Status: design for review, written before any implementation. Nothing
here is built yet. When it is, "As implemented" sections go at the end,
backend first and then frontend, as in the other design documents.
Anything marked **unverified** is a browser or OS behaviour that a spike
on real devices has to confirm before the related code is written (see
"Phase 0" and "Open questions").

## Goal

The user turns on sharing on the computer and creates one or more
**shares**. A share is a named, scoped grant: "my phone" sees several
libraries, "Summer guests" see one album. Scanning a share's QR code
pairs a phone with that share. The phone can then browse what the share
covers over the home Wi-Fi: the timeline grid, albums (in a library
share), a lightbox with swipe, videos, and, when the share allows it,
downloads of originals and star ratings. Nothing else. The admin UI with
its imports, moves, trash, backups and folder browser stays reachable
only from the computer itself.

Linux is the main platform. Windows and macOS are covered where they
differ (firewall prompts, config paths, the tray). The phone browsers to
support are iOS Safari, Android Chrome, and the Sailfish Browser on
Sailfish OS 5.1 (Gecko-based, see "Browser behaviour").

## Decisions (from the request and the review)

| Question | Decision |
|---|---|
| Login | QR-code pairing. The computer shows a QR code; scanning it signs that phone in, and it stays signed in. Paired phones are listed and can be revoked. No passwords. |
| Encryption | HTTPS with a self-signed certificate, with a one-time warning on the phone |
| What is shared | Only what the user explicitly shares, as **shares** (see "Shares"): a set of libraries, or a single album. More scope kinds can be added later. |
| What a phone may do | View (grid, albums read-only, lightbox), and per share: play videos, download originals, set star ratings. Nothing else. |
| Ratings from a phone | The library's normal rating (`user.db` `ratings`), the same one the admin UI sets. Each phone rating is logged with the device and share name. |
| Approval on the computer | Not by default. Each share has a setting "Ask on this computer before a phone is paired". |
| Certificate install on the phone | Opt-in, offered only after Phase 0 shows the phone enforces the CA's name constraints |
| After a restart | Sharing is off, unless the desktop app's "Keep sharing on" is set. `serve` shares only with `--share`. |
| Tags, camera filters, map | Not on phones in this version |

Decisions this document makes (justified below):

| Question | Decision |
|---|---|
| Architecture | A **second listener with its own handler and its own `ServeMux`**, built from an explicit allowlist table. The admin mux is never reachable from it. |
| Authorization | Every request resolves device → share → scope. Each scope kind implements one small interface (restrict a query, decide whether a row is visible), so new kinds need no change to the routes. |
| Admin listener | Unchanged: loopback only. Sharing refuses to start when the admin `--addr` is not loopback. |
| Settings, shares, devices, certificate | Per-user config dir (`os.UserConfigDir()/picmover/share/`), never inside the workspace |
| Library identity | Workspace root + library folder id + the `user.db` `LibraryID`; all three must match |
| Album identity | Library identity + album id + the album's `created_at` (album ids can be reused by SQLite after a deletion) |
| Device | Belongs to exactly one share. A browser holds one device cookie; pairing it with another share replaces the old device after asking on the phone. |
| Device credential | 256-bit random secret in a `__Host-` cookie (`HttpOnly; Secure; SameSite=Strict`), stored server-side only as SHA-256 |
| Pairing token | Per share. 256-bit, single use, 2-minute TTL, in memory only, carried in the URL **fragment**. Typed fallback: an 8-character one-time code. |
| Capabilities | Per share: `video`, `download`, `rate`, `location`. Defaults: all on for a library share; for an album share video on, the rest off. |
| Expiry and pause | Per share: an optional expiry (album shares default to 30 days), pause and resume, and "Revoke all phones" |
| TLS | A local CA (per user, name-constrained) signs a leaf certificate for the current LAN addresses and `<host>.local`. ECDSA P-256, TLS 1.2+. No HSTS. |
| Bind address | The chosen LAN IPv4 address (by default the one of the default route), not `0.0.0.0`. Connections are filtered to that subnet (and loopback). Sharing pauses when the network changes. |
| Phone UI | A separate Vite entry (`share.html`) with its own small shell, reusing Grid, Tile, Lightbox and RatingStars in a read-only mode. An album share opens straight into its album. |
| CLI | `serve --share` serves the existing shares; a new `picmover share` command lists, creates, changes and deletes shares. |

## Non-goals

- Access from outside the home network (internet, VPN, relays). Remote
  access is a separate project with its own threat model. Guests must be
  on the home Wi-Fi to use an album share.
- User accounts. A share is the unit of permission; a person with two
  phones pairs both with the same share.
- Editing anything other than ratings from a phone: tags, albums,
  trash, imports, moves, backups, jobs, settings, and the map.
- A native phone app, push notifications, offline sync, uploading from
  the phone (a later "import from phone" would need its own design).
- A publicly trusted certificate (ACME needs a public name; Q3 below
  covers the local-CA option).
- Changing firewall rules automatically. picmover prints and shows the
  command; the user runs it.

## Current state (what this builds on)

Findings from reading the code at `origin/master` (befcdb6):

- **Binding.** `cmd/serve.go` defaults `--addr` to `127.0.0.1:7439`;
  `server.Start` (`internal/server/start.go`) listens on it, falls back
  to `127.0.0.1:0` when the port is busy and `--addr` was not given, and
  prints a warning for a non-loopback address ("There is no
  authentication: anyone who can connect can browse the library and
  start imports"). The desktop app (`internal/shell`) calls the same
  `server.Start` on `127.0.0.1:<port>` (7440 by default, saved in
  `app.json`).
- **Request checks** (`internal/server/security.go`, `middleware` in
  `server.go`), applied to every request:
  - Host must be the listening host or `localhost`/`127.0.0.1`/`[::1]`
    with the right port, else 421. With a wildcard listener any IP
    literal is accepted.
  - Origin must be `http://<accepted host>` or a `--cors-origin`.
  - `Sec-Fetch-Site: cross-site` is refused, except top-level
    navigations to non-API paths.
  - POST needs `Content-Type: application/json` and `X-Picmover: 1`.
  - Every response gets CORP `same-origin`, `nosniff` and
    `Referrer-Policy: no-referrer`.

  There is no authentication at all. Safety rests on loopback plus these
  CSRF and DNS-rebinding checks.
- **Routes** (`Server.routes`): one mux holding everything. Library
  routes are `/api/l/{lib}/...`, wrapped by `scoped` (resolve and acquire
  the library) and, for user-data edits, by `notMoving` (409 while a move
  runs on the library). The mux also holds the global `/api/fs`,
  `/api/jobs`, SSE streams and `/api/app/*`, plus the static fallback
  `/`. **Filtering this mux for a LAN client would be a deny-list in
  practice**: every route added later would be exposed unless someone
  remembered to block it. Hence the separate mux.
- **Media endpoints:**
  - `thumb/{hash}`, `preview/{hash}` and `large/{hash}` serve by hash
    straight from the thumbs store, **without checking that the hash is a
    live row**. A trashed item's renditions stay fetchable by hash.
  - `media/{id}` and `file/{id}` use `mediaRow`, which returns **trashed
    rows too**. `file` then serves the original from the trash path.
  - `file` uses `http.ServeContent`, so Range requests work (needed for
    video). `Content-Disposition` carries the base name, and
    `?download=1` makes it an attachment.
  - The detail JSON includes `relPath`, `originalPath` (the absolute
    source path on the computer at import time), imports, session ids,
    `thumbError` and `phash`.

  The share handlers must therefore add a live-row check and a
  redacted detail.
- **Ratings:** `POST /api/l/{lib}/ratings {hashes ≤ 2000, rating 0..5}`
  calls `userdata.SetRating`, which **does not check that the hashes
  exist** in the catalog. It is wrapped in `notMoving`.
- **Libraries and locking:** a workspace library is opened on demand
  (`POST /api/libraries/{lib}/open`), taking the OS lock
  `<lib>/.picmover/lock` for the server's lifetime. A rebuilding library
  answers 503 and a closed one 409 `{state}`. The library id is the
  folder name. `user.db` carries a stable `LibraryID`
  (`userdata.Identity`).
- **Frontend:**
  - It uses a hash router (`lib/router.ts`, `lib/routes.ts`) and
    library pages under `#/l/<id>/...`. `api.ts` builds every library URL
    from `libBase(id) = /api/l/<id>`.
  - It already has a viewport meta tag, a web manifest and icons.
  - The Lightbox has one-finger swipe (`touchstart`/`touchend`) and
    `<video controls preload="metadata">`, but no `playsinline`.
  - The Scrubber uses pointer events with `touch-action: none`.
  - The tile date, rating and selection check appear on `:hover` only.
  - Two `max-width` media queries exist (App, Lightbox).
  - The grid picks thumb/large/preview by tile size × DPR
    (`lib/rendition.ts`).
  - Vite builds one entry (`index.html`) with `target: 'es2022'`.

## Architecture

```
                      ┌──────────────────────── picmover process ────────────────────────┐
 computer browser ──▶ │ admin listener 127.0.0.1:7439 (HTTP)                             │
                      │   middleware (Host/Origin/Sec-Fetch/X-Picmover) → admin mux      │
                      │     … every existing route …                                      │
                      │     /api/share/*  (manage sharing: loopback-only, see below)      │
                      │                                                                   │
 phone (Wi-Fi) ─────▶ │ share listener 192.168.1.20:7443 (HTTPS)                          │
                      │   conn filter (subnet/loopback) → TLS → shareMiddleware           │
                      │   (Host/Origin/Sec-Fetch, headers, rate limit, device auth)       │
                      │     → share mux: ONLY the routes in shareRouteTable               │
                      │                                                                   │
                      │ shared state: workspace registry, libEntry locks, jobs            │
                      └───────────────────────────────────────────────────────────────────┘
```

- The share listener is a second `net.Listener` and a second
  `http.Server` owned by `Running` (start.go), with its own
  `ReadHeaderTimeout`/`IdleTimeout`, `TLSConfig` and `ErrorLog`. It is
  started and stopped at runtime (the toggle) without touching the admin
  listener. Go enables HTTP/2 automatically on a TLS server, which lets
  the many small thumbnail requests share one connection.
- **`shareRouteTable`** (`internal/server/share_routes.go`) is a plain
  Go table `[]shareRoute{pattern, handler, kind}`. The share mux is
  built only from it. It has no `/` catch-all that reaches admin code: the
  static fallback is a share-specific handler that serves a fixed
  allowlist of files. The table and a golden list in the test must
  match, so adding a share route means editing the test, which a
  reviewer sees. A second test sends every admin route pattern to the
  share handler with a valid device cookie and expects 404.
- The share handlers reuse the server's internals (workspace registry,
  `libEntry.acquire`, catalog queries, `openOriginal`, renditions). They
  never call admin handlers that produce JSON with fields the phone must
  not see. Where the admin handler's output is already safe (timeline,
  media list items, renditions, files), the share route wraps the same
  function after its own checks. Where it is not (library info, media
  detail), a redacted share handler builds its own JSON.
- **`internal/share`** (new package, no dependency on `internal/server`)
  holds the security-critical parts, which are testable on their own:
  - `config.go`: `share.json` load/save, atomic writes, and the
    one-sharing-process lock.
  - `shares.go`: shares (name, scope, capabilities, expiry, pause) and
    their devices; `Verify(cookie) (Device, *Share, bool)` in constant
    time; revoke, revoke all, rename, last seen.
  - `scope.go`: the scope kinds as data (`libraries`, `album`), their
    validation, and the stable keys. The query side lives in
    `internal/server/share_scope.go`, because it needs the catalog.
  - `pairing.go`: per-share tokens and codes, TTL, single use, attempt
    limits.
  - `ratelimit.go`: token buckets per remote IP and global.
  - `tlscert.go`: local CA, leaf, SANs, fingerprints, renewal.
  - `netif.go`: LAN address detection, the change watcher, the remote
    address filter.
  - `qr.go`: QR as SVG and as terminal text.

### Layout

```
internal/share/                       config, shares and devices, scope data, pairing, rate limits, TLS, network, QR (above)
internal/server/share.go              share lifecycle in Running (start/stop/pause), admin /api/share/* handlers
internal/server/share_routes.go       shareRouteTable, the share mux, shareMiddleware, device → share resolution
internal/server/share_scope.go        scopeAuth: the per-kind query restriction and row visibility
internal/server/share_api.go          redacted handlers: session, pair, libraries, library, detail, albums, rating
internal/server/share_static.go       static allowlist for the share listener (share.html, /assets/*, icons, manifest)
cmd/serve.go                          --share, --share-port, --pair SHARE
cmd/share.go                          picmover share list|create|set|delete|revoke
internal/shell/                       tray item "Share on Wi-Fi", keep-on start
web/share.html                        second Vite entry
web/src/share/main.ts, ShareApp.svelte, Pair.svelte, ShareLibraries.svelte, shareapi.ts
web/src/pages/Share.svelte            admin page: global switch, the shares, each with QR, devices, capabilities, expiry
web/src/components/ShareDialog.svelte create or edit a share ("Share libraries…", "Share album…")
web/src/components/SharePill.svelte   "Sharing" indicator in the admin top bar
```

## Shares

A **share** is a first-class object: a name, a scope, capabilities, an
optional expiry, a paused flag, an approval setting, and its own paired
devices. Every device belongs to exactly one share. Pairing QR codes and
codes are issued per share.

```json
{
  "id": "s_8c1f04a9d2e37b65",
  "name": "Summer guests",
  "scope": {
    "kind": "album",
    "workspace": "/media",
    "library": {"id": "peter", "libraryId": "6f1c…"},
    "album": {"id": 12, "createdAt": 1781092800}
  },
  "capabilities": {"video": true, "download": false, "rate": false, "location": false},
  "expiresAt": "2026-11-13T19:00:00+02:00",
  "paused": false,
  "approve": false,
  "createdAt": "2026-10-14T19:00:00+03:00",
  "devices": [ {"id": "d_…", "name": "Mia's phone", "secretSha256": "…", "pairedAt": "…",
                "lastSeen": "…", "lastAddress": "…", "browser": "…", "cookieIssuedAt": "…"} ]
}
```

A library share has `"scope": {"kind": "libraries", "workspace":
"/media", "libraries": [{"id": "peter", "libraryId": "…"}, {"id":
"penny", "libraryId": "…"}]}`.

### Scope kinds

| Kind | Covers | Stable key |
|---|---|---|
| `libraries` | Every live photo of the listed libraries, and their albums (read only) | Per library: workspace root + folder id + `LibraryID` |
| `album` | The live photos that are members of one album, while they are members | Library key + album id + the album's `created_at` (Unix seconds) |

**Why the album key includes `created_at`:** `albums.id` in `user.db` is
`INTEGER PRIMARY KEY` without `AUTOINCREMENT`. SQLite may hand a deleted
album's id to the next new album when the deleted one had the highest
id. Without the creation time, a share of a deleted album could silently
start showing a different album. `userdata.Album` already carries
`CreatedAt`.

### What happens when things change

| Event | Library share | Album share |
|---|---|---|
| Album renamed | Nothing; phones see the new name | Nothing. The share keeps its own name; the phone shows the share name as title and the album name below it. |
| Album emptied | Nothing | The share works and shows an empty album ("Nothing here yet") |
| Photo removed from the album, trashed, or moved to another library | That photo is gone from the share if it left the library, or is trashed; album membership does not matter | That photo is 404 at once on every route |
| Album deleted, or id reused (a `created_at` mismatch) | The album leaves the album list | The share becomes **broken**: phones get 403 `{state: "gone"}` "This album is no longer shared". The admin page marks the share "Album deleted" and offers Delete. Nothing is deleted automatically. |
| Library folder renamed or removed, or `LibraryID` mismatch | That library leaves the share (the others keep working); the admin page says "Library missing" | Broken, as above, with "Library missing" |
| A library unshared by the user | The scope is edited; that library is 404 at once | (Not applicable; changing an album share's album is not offered. Create a new share.) |
| Workspace switch (app) | Shares of other workspace roots are **inactive** (phones get 403 `{state: "unavailable"}`) until that root is served again | Same |
| Library rebuilt | `user.db`, and so ratings, albums and `LibraryID`, survive a rebuild, so shares keep working (503 during the rebuild) | Same |

Shares are never repaired or rescoped automatically. Showing more than
the user granted is the failure that must not happen, so every
uncertainty ends in "show nothing".

### Capabilities

| Capability | Allows | Library share default | Album share default |
|---|---|---|---|
| `video` | Playing videos: `file/{id}` for video rows, inline | on | on |
| `download` | Originals: `file/{id}` for image rows (1:1 view) and `?download=1` for any row | on | **off** |
| `rate` | `POST ratings`, **and** seeing the library's ratings (item `rating`, detail `rating`, the `rating` filter) | on | **off** |
| `location` | `lat`/`lon` in the detail and `gps` in items | on | **off** |

- Without `download`, images are shown only as the 1600 px preview.
- `video` without `download` is a deterrent, not a guarantee: a streamed
  video can be saved by someone determined. The share dialog says so
  next to the checkboxes.
- Ratings are visible only with `rate`. The owner's stars ("1 star:
  blurry") are private judgements, and a guest does not need them.
- **Guest ratings:** off by default for album shares. Ratings are the
  library's single rating (the decision for Q1), so a guest's stars
  would overwrite the owner's own. When the owner turns `rate` on for an
  album share, the dialog says "Guests' stars replace yours". Every
  phone rating is logged at Info with the share name, device name, hash,
  library, and old and new rating, so a bad change can be found and
  undone by hand.

### Expiry, pause, revoke all

- **Expiry:** optional `expiresAt`. The share dialog offers 1 day,
  7 days, 30 days, 90 days, a date, or never. The default is 30 days for
  album shares and never for library shares.
  - After expiry the share is **expired**: phones get 403 `{state:
    "expired"}` "This share has ended", and pairing is refused.
  - The admin page keeps the share with "Expired" and an "Extend" button.
    Expired shares are deleted automatically 30 days after expiry.
- **Pause:** a share-level switch. While paused, phones get 403
  `{state: "paused"}` "This share is paused", and no pairing is possible.
  Devices stay paired, and resuming restores access with no re-pairing.
  This is separate from the global sharing switch (the listener) and from
  the network-change pause.
- **Revoke all:** removes every device of the share. The share stays, so
  new pairings are possible.
- **Delete:** removes the share and its devices.
- Limits: at most 50 shares and 20 devices per share.

## Share listener: routes (the allowlist)

All on the share listener only. Every route except `GET /api/session`,
`POST /api/pair` and the static files needs a valid device cookie.

`{lib}` is a **share-local key**, not necessarily the folder id:

- In a library share it is the library's folder id. The owner's own
  phones see the names they know.
- In an album share it is always the literal `album`. Guests never learn
  library folder names, and never see album ids in URLs.

| Method | Path | Result | Library share | Album share |
|---|---|---|---|---|
| GET | `/api/session` | `{device: {id, name}, computer, share: shareJSON}`, or 401 `{error, paired: false}`, or 403 `{error, state}` | ✓ | ✓ |
| POST | `/api/pair` | `{token}` or `{code}` plus `{name, replace?}` → 200 `{device, share}` and `Set-Cookie` | ✓ | ✓ |
| GET | `/api/libraries` | `{libraries: [shareLib]}` | ✓ | 404 |
| GET | `/api/l/{lib}/library` | redacted summary `{id, name, summary: {total, images, raw, videos, earliest, latest}}` | ✓ | 404 (the library's size is not the guest's business) |
| GET | `/api/l/{lib}/timeline` | as admin, scope-restricted | ✓ | ✓ (album forced) |
| GET | `/api/l/{lib}/media` | as admin item JSON, scope-restricted, redacted by capability | ✓ | ✓ |
| GET | `/api/l/{lib}/media/{id}` | redacted detail | visible rows | visible rows |
| GET | `/api/l/{lib}/thumb/{hash}` | as admin | visible rows | visible rows |
| GET | `/api/l/{lib}/preview/{hash}` | as admin | visible rows | visible rows |
| GET | `/api/l/{lib}/large/{hash}` | as admin | visible rows | visible rows |
| GET | `/api/l/{lib}/file/{id}` | as admin, Range, `?download=1`; `Cache-Control: private, no-store` | visible rows, per `video`/`download` | same |
| GET | `/api/l/{lib}/albums` | `{albums: [{id, name, count, cover}]}` | ✓ | 404 |
| POST | `/api/l/{lib}/ratings` | `{hashes: [h], rating: 0..5}` → `{updated}` | needs `rate`; one visible hash; `notMoving` → 409 | same |
| GET | `/`, `/share.html`, `/assets/*`, `/favicon.png`, `/icons/*`, `/manifest.webmanifest` | the share SPA | no auth | no auth |

The route table is the same for every scope kind. A "404" in the table
above is the scope's answer, not a missing route. The scope interface
(next section) decides it.

`shareJSON` is `{name, kind, capabilities, expiresAt}`, plus
`libraries: [shareLib]` for a library share, or `album: {name, count,
cover}` for an album share. `shareLib` is `{id, name, cover, total}`:
no path, no state.

Redacted detail: `{id, hash, type, takenAt, offset, w, h, dur, size,
ext, fileName, cameraMake, cameraModel, lens, displayable, thumbAt}`.

- With `location`, it adds `lat`/`lon`.
- With `rate`, it adds `rating`.
- In a library share it adds `albums: [{id, name}]`. An album share
  sends no `albums`, so a guest learns no other album names.
- Left out always: `relPath`, `originalPath`, `imports`, `sessionId`,
  `importedAt`, `thumbState`, `thumbError`, `phash`, `deletedAt`,
  `tags`, `takenAtSource`. `fileName` is the base name only.
- `displayable` is false without `download`, so the lightbox offers no
  1:1 view.
- List items lose `rating` without `rate`, and `gps` without `location`.

Filters accepted (a whitelist; anything else is 400): `type`, `from`,
`to`, `recur`, `months`. In addition:

- `rating` needs `rate`;
- `album` exists only in a library share, and must be an album of
  `{lib}`;
- an album share refuses `album` (the scope sets it).

Always refused: `trashed`, `import`, `session`, `tag`, `camera`, `gps`,
and unknown keys.

There are **no** routes for `/api/fs`, `/api/jobs*`, SSE, `/api/app*`,
library open or create, tags, albums (write), imports, trash, transfer,
similar, backups, geo, cover, or `/api/share/*`. All of those are 404 on
the share listener, because the share mux has no pattern for them.

### Per-request authorization (`shareScoped`)

Every library route runs these steps in this order. Unless stated, each
refusal is the same 404 `{"error":"not found"}`, so nothing can probe
which libraries, albums or photos exist outside the scope:

1. **Device → share:** cookie → `shares.Verify` (look up the device by
   id, then `subtle.ConstantTimeCompare` of SHA-256(secret)), which
   returns the device and its share.
   - Missing, bad or revoked → 401 `{error, paired: false}`.
   - A share that is paused, expired, broken, or not in the served
     workspace → 403 `{error, state: "paused"|"expired"|"gone"|"unavailable"}`.
     The phone may know this much about its own share.
2. **Scope → library:** `auth := scopeFor(share)` (a `scopeAuth`, below).
   `auth.library({lib})` maps the share-local key to a library id: the
   key itself for a listed library, the album's library for `album`, and
   anything else is 404. Then:
   - `validLibID`, and the root must be the served workspace
     (`ws.root()`);
   - its `user.db` `LibraryID`, read when acquired, must equal the
     stored one.
3. **Acquire:** `ws.entry`/`entryFor` + `acquire`. A closed library is
   **opened on demand** (`ws.open`: the same path and lock as the admin
   open). Busy, error or retired → 503 `{error: "This library is not
   available right now."}`, and the same for rebuilding. The phone never
   sees holder pids or paths.
4. **Scope check for the route:** library-level routes (`library`,
   `albums`, and an `album` filter) need `auth.wholeLibrary()`, else
   404.
5. **Row checks:**
   - `{id}` routes: `GetMedia`;
   - `{hash}` routes and ratings: a new `catalog.LiveByHash(ctx, hash)
     (Media, bool, error)` (`hash` is `UNIQUE`, so this is one index
     probe).

   Then the row must be live (`DeletedAt.IsZero()`) **and**
   `auth.visible(ctx, l, row)` must be true. For an album scope that is
   a new `userdata.AlbumHas(ctx, albumID, hash)` (one probe of the
   `album_items` primary key), plus the album identity check.
6. **Capability:**
   - `file/{id}` needs `video` for a video row, and `download` for an
     image row or for `?download=1`;
   - `ratings` needs `rate`.

   A refusal is 403 `{error: "not allowed in this share"}`; the UI never
   offers the action anyway.
7. **Query restriction** (timeline and media list):
   `auth.restrict(ctx, l, &filter, &userFilter)` adds the scope to the
   parsed filter (for an album scope, `userFilter.album = albumID`,
   which the existing `resolveFilter` turns into a hash set) after the
   whitelist check. So a list can only ever return visible rows.
8. **Handler,** then the response redaction by capability.

```go
// scopeAuth is one share's scope, resolved for a request. New scope kinds
// (several albums, a date range, a tag) implement it; routes never switch
// on the kind.
type scopeAuth interface {
    // library maps a share-local {lib} key to a served library, or false.
    library(key string) (libRef, bool)
    // libraries lists the libraries for /api/libraries (nil: route is 404).
    libraries() []libRef
    // wholeLibrary reports whether library-level routes (library summary,
    // albums list, album filter) are allowed.
    wholeLibrary() bool
    // restrict narrows a parsed list query to the scope.
    restrict(ctx context.Context, l *libHandles, f *catalog.Filter, uf *userFilter) error
    // visible reports whether a live row of l is in the scope now.
    visible(ctx context.Context, l *libHandles, m catalog.Media) (bool, error)
}
```

`restrict` and `visible` must agree: the tests check, per scope kind,
that every row a list returns is `visible`, and that no `visible` row is
missing from the unfiltered list. Future kinds:

- several albums: `restrict` uses the union of the album hash sets, and
  `visible` checks any of them;
- a date range: `restrict` sets `From`/`To`, and `visible` compares
  `TakenAt`.

Neither touches a route.

Hash enumeration: hashes are 64-bit content hashes. A device learns the
visible hashes by listing its scope, which is allowed. Steps 5 and 7
make guessing a hash (2^64) or a media id (sequential, so guessable)
useless: anything outside the scope is the same 404 as a nonexistent
row. A guest in an album share cannot list albums, library summaries or
other libraries, and cannot pass an `album` filter, so other albums and
their sizes stay hidden. Path traversal: no share route takes a path.
Files come only from catalog rows through `openOriginal` (shape check,
containment, `EvalSymlinks`, `Lstat`, `SameFile`), unchanged.

### Share middleware (before routing)

- **Connection filter** (a wrapping `net.Listener`): the remote IP
  must be loopback or inside the bound interface's subnet; anything else
  is closed at `Accept`, before the TLS handshake. Concurrent connections
  are capped at 64.
- **Host:** must be `<bound IP>:<port>`, `<hostname>.local:<port>` (when
  the mDNS name is enabled), or loopback with the port. Otherwise 421.
  This keeps the DNS-rebinding defence.
- **Origin:** when present, it must be `https://<accepted host>`.
  Otherwise 403. No CORS, ever (`--cors-origin` does not apply here).
- **Sec-Fetch-Site:** the same rule as the admin listener (`fetchSiteOK`
  with `corsOrigin=false`).
- **POST:** `Content-Type: application/json` and `X-Picmover: 1`, as
  on the admin listener. The body cap is 4 KiB (not 64), with the same
  30 s body deadline.
- **Rate limits** (`share/ratelimit.go`, token buckets, in memory):
  - `/api/pair`: 5 per minute per IP and 20 per minute in total.
  - Requests that fail authentication: 30 per minute per IP. Once that
    is exceeded, 429 for every request from that IP for 5 minutes.
  - Authenticated requests: 100 per second burst and 30 per second
    sustained per device. That is generous for scrolling thumbnails, and
    exists only to stop a runaway client.
- **Headers on every response:**
  - `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`,
    CORP `same-origin`, `Cross-Origin-Opener-Policy: same-origin`.
  - `Permissions-Policy: camera=(), microphone=(), geolocation=()`.
  - `Cache-Control: no-store` on JSON, 401/403/404, and originals.
  - Renditions keep `private, max-age=604800` with their versioned ETag.
    Re-downloading thumbnails on every visit would hurt scrolling, and
    they are in the phone's private cache only.
  - The HTML gets a CSP of `default-src 'self'; img-src 'self' data:
    blob:; media-src 'self' blob:; connect-src 'self'; style-src 'self'
    'unsafe-inline'; script-src 'self'; frame-ancestors 'none';
    base-uri 'none'; form-action 'none'`. There are no OSM hosts (no
    map).
  - **No HSTS.** With a self-signed certificate, HSTS would turn the
    click-through warning into a hard failure in every browser that
    honours it on `.local` names, and could lock the phone out of the
    name for a year.
- **Logging:** `serve --verbose` logs requests at Debug as today, with
  the path only and never the query or the cookie. Refusals log at
  Debug. Pairing, revocation, the first request of a device per day, and
  every rating change ("rating 3 → 4 on 9f3a… in peter by device 'Anna's
  iPhone' (share 'My phone')") log at Info, as do share changes (created,
  scope or capabilities edited, paused, expired, deleted).

## Turning sharing on and off

### Admin API (admin listener only)

Global sharing (the listener):

| Method | Path | Body → Result |
|---|---|---|
| GET | `/api/share` | `{state: "off"\|"on"\|"paused"\|"error", reason?, url?, mdnsUrl?, port, address, addresses: [{ip, iface, subnet}], fingerprint: {leaf, ca}, keepOn, autoOff: {mode, until?}, managedElsewhere?: {pid, cmd}, firewallHint, shares: [share]}` |
| POST | `/api/share` | `{enabled?, address?, port?, keepOn?, autoOff?: "never"\|"1h"\|"4h"\|"quit", mdns?: bool}` → the GET body |
| POST | `/api/share/cert/renew` | `{}` → the GET body (new leaf; the CA is kept) |

Shares:

| Method | Path | Body → Result |
|---|---|---|
| POST | `/api/share/shares` | `{name, scope: {kind: "libraries", libraries: [id]} \| {kind: "album", library: id, album: aid}, capabilities?, expiresIn?: seconds \| expiresAt?, approve?}` → 201 share. The server fills in the workspace root, the `LibraryID`s and the album's `createdAt`; omitted capabilities take the kind's defaults. 400 for an unknown library or album, or an empty library list. |
| POST | `/api/share/shares/{sid}` | `{name?, libraries?: [id] (libraries kind only), capabilities?, expiresAt?: time \| null, paused?, approve?}` → share. The kind and an album share's album cannot change (400). |
| POST | `/api/share/shares/{sid}/delete` | `{}` → 204 (devices go with it) |
| POST | `/api/share/shares/{sid}/revoke-all` | `{}` → share |
| POST | `/api/share/shares/{sid}/pairings` | `{}` → `{url, code, qrSvg, expiresAt}`. 409 when sharing is off, or the share is paused, expired or broken. |
| POST | `/api/share/shares/{sid}/pairings/cancel` | `{}` → 204 |
| GET | `/api/share/shares/{sid}/requests` | pairing requests waiting for approval (`approve` on): `[{id, name, browser, address, at}]` |
| POST | `/api/share/shares/{sid}/requests/{rid}` | `{approve: bool}` → 204 |
| POST | `/api/share/devices/{did}` | `{name}` → device |
| POST | `/api/share/devices/{did}/revoke` | `{}` → 204 |

`share` (admin view) is `{id, name, kind, scope: {libraries: [{id,
name, missing?}]} | {library: {id, name, missing?}, album: {id, name,
count, missing?}}, capabilities, expiresAt, state: "active" | "paused" |
"expired" | "gone" | "unavailable", approve, createdAt, devices:
[device]}`. `device` is `{id, name, pairedAt, lastSeen, lastAddress,
browser}`. `browser` is a coarse label from the User-Agent at pairing
time ("Safari on iPhone", "Chrome on Android", "Sailfish Browser"). It is
for recognition only, never for decisions.

These routes manage the LAN exposure, so they get one extra rule beyond
the existing admin checks. They answer 403 unless the admin listener is
loopback (`IsLoopbackAddr`) **and** the request's `RemoteAddr` is
loopback. With `serve --addr 0.0.0.0:7439` (the existing warned mode),
sharing cannot be managed or started. `serve --share` with a non-loopback
`--addr` is a usage error: "--share needs the default loopback --addr;
the shared view has its own listener".

The admin page polls `GET /api/share` every 2 s while it is open, which
shows "Paired: Anna's iPhone" (and approval requests) as soon as a scan
completes. A share event stream is not worth it for this.

### One sharing process

The app and `serve` can run at the same time (different ports) and read
the same per-user `share.json`. The process that turns sharing on takes
an exclusive lock on `<config>/picmover/share/share.lock` (the same
`flock`/`LockFileEx` helpers as the library lock) and keeps it while
sharing is on or paused. Another process's share page then shows
"Sharing is managed by another picmover process (pid N, picmover-app)"
with every control disabled (`managedElsewhere`). Writes to `share.json`
take the lock briefly. The process that is not sharing never writes the
file while another process holds the lock. The same applies to the
`picmover share` command (below).

### Entry points

- **Admin UI:**
  - A "Share" item in the top bar (next to the job pill) opens
    `#/share`. While sharing is on, the item is a green pill: "Sharing ·
    3 shares · 4 phones". It is visible on every page, in serve mode and
    in app mode.
  - The **Share page** shows the global switch, the address and URL, the
    fingerprint and the firewall hint at the top. Below them it lists the
    shares as cards, each with:
    - name, kind and scope ("Libraries: peter, penny" / "Album: Summer
      2026 in peter, 48 photos");
    - state and expiry ("expires in 12 days");
    - capability chips;
    - buttons: **Pair a phone** (the QR dialog), **Edit**, **Pause**,
      **Revoke all**, **Delete**;
    - the device list with Rename and Revoke, and pending approval
      requests with Approve and Deny.
  - Buttons "Share libraries…" and "Share album…" open `ShareDialog`:
    - name;
    - scope: library checkboxes, or a library and album picker;
    - capability checkboxes pre-set to the kind's defaults, with the
      warnings ("Guests' stars replace yours"; "Videos can be saved
      even without Download");
    - expiry;
    - "Ask on this computer before a phone is paired".

    Saving offers "Pair a phone now".
  - On an **album page** (`#/l/<id>/albums/<aid>`), a "Share…" button
    opens `ShareDialog` for that album. When shares of the album already
    exist, the button reads "Shared (2)" and links to them on the Share
    page.
  - When creating a share while sharing is off, the dialog says
    "Sharing on Wi-Fi is off" and offers to turn it on.
- **Desktop app tray** (Windows, Linux): a checkbox item "Share on
  Wi-Fi". When the tooltip and the label show the address, it reads
  "Share on Wi-Fi (192.168.1.20)".
  - Turning it on starts the listener for the existing shares.
  - When there are no shares, it opens the window on `#/share` instead.
  - Turning it off stops sharing at once.

  macOS has no tray (stub), so the UI is the entry point there.
- **CLI, serving:**
  `picmover serve <dir> --share [--share-port 7443] [--pair SHARE]`.
  - `--share` turns the listener on for this run with the existing
    shares of `<dir>`'s workspace root. When there are none it fails
    with "no shares for this folder; create one with 'picmover share
    create' or on the Share page".
  - `--pair SHARE` (a name or id) prints a QR code in the terminal
    (Unicode half blocks) and the one-time code for that share at start,
    valid for 2 minutes. Pressing Enter prints a new one.
  - Without `--share`, `serve` never starts sharing by itself, even when
    the app's "Keep sharing on" is set. That setting belongs to the app.
  - On start it prints `Sharing on Wi-Fi at https://192.168.1.20:7443;
    certificate SHA-256 AB:CD:…`, one line per active share ("My phone:
    libraries peter, penny; 2 phones"), and a line per pairing,
    revocation, pause and expiry.
- **CLI, managing** (recommended, new command `cmd/share.go`). It works
  on `share.json` and needs no running server:

  ```
  picmover share list [--json]
  picmover share create NAME (--library ID... | --album LIB:NAME | --album-id LIB:AID)
                        [--allow video,download,rate,location] [--deny ...]
                        [--expires 30d|never|YYYY-MM-DD] [--approve]
                        [--workspace DIR]
  picmover share set NAME [--allow ...] [--deny ...] [--expires ...] [--pause|--resume] [--rename NEW]
  picmover share revoke NAME [--device DEVICE | --all]
  picmover share delete NAME
  ```

  - `create` resolves the libraries and the album like the server does.
    It reads `user.db` read-only for the `LibraryID` and the album's
    `created_at`, takes no library lock, and is refused when the album
    name is ambiguous (the candidates' ids are listed).
  - The workspace is `--workspace` or the current directory.
  - Every command except `list` is refused with "sharing is managed by
    picmover pid N; use its Share page" while another process holds
    `share.lock`. A live server never has its file changed underneath
    it.
  - Pairing needs the running server, so it is `serve --share --pair` or
    the UI. The command does not pair.

### Restart, auto-off, network changes

- **Persistent:** the shares (scope, capabilities, expiry, paused,
  approve) with their devices, the port, the chosen interface, the CA
  and the leaf, and `keepOn`. **Not persistent:** pairing tokens and
  codes, pending approval requests, rate-limit state, and the global
  on/off state unless `keepOn`. Expiry is checked on every request and
  once a minute, so a share also ends while picmover keeps running.
- **After a restart:** off by default. With `keepOn` (an app setting,
  default false), the app starts sharing once the server is up, as long
  as the network check below passes.
- **Auto-off:** `never`, `1h`, `4h`, or `quit` (the default: on until
  picmover quits). A timer turns sharing off, and the pill shows the
  time left.
- **Network change:** `netif` checks the bound address every 15 s.
  - "Home network" is recorded when sharing is turned on as the
    interface subnet plus the default gateway's IP.
  - If the address changes **within the same home network** (a new DHCP
    lease on the same subnet and gateway), the listener rebinds to the
    new address, the leaf is reissued for it, and the state stays `on`.
    The UI and the log announce the new URL. Phones must re-pair, because
    cookies are per host; see "TLS".
  - If the subnet or gateway changes (the laptop is on another network),
    or the address disappears, sharing goes to **`paused`**: the
    listener closes, and the pill turns amber with "Sharing paused:
    network changed". The user resumes it on the Share page, which
    records the new network as home.

  This is what keeps a laptop that leaves the house from offering the
  share on a café Wi-Fi, where the address range is also private.

## Persistence

`os.UserConfigDir()/picmover/share/` holds the files below. That is
`~/.config/picmover/share` on Linux,
`%APPDATA%\picmover\share` on Windows and
`~/Library/Application Support/picmover/share` on macOS.

```
share.json      settings and devices (0600)
share.lock      the one-sharing-process lock
ca.pem, ca.key  local CA certificate and key (key 0600)
leaf.pem, leaf.key  current server certificate and key (key 0600)
```

The directory is created 0700 and every key file 0600. Keys are written
atomically (temp + rename, the shell's `writeFileAtomic` with its
Windows retry). On Windows the per-user `%APPDATA%` ACL is the
protection. DPAPI encryption of the keys is a possible later hardening,
not in this design. On start, a key file readable by group or others on
Unix is refused with a message that names the path, like ssh does.

```json
{
  "version": 1,
  "port": 7443,
  "interface": "wlp3s0",
  "keepOn": false,
  "autoOff": "quit",
  "mdns": false,
  "home": {"subnet": "192.168.1.0/24", "gateway": "192.168.1.1"},
  "shares": [
    {"id": "s_…", "name": "My phone",
     "scope": {"kind": "libraries", "workspace": "/media",
               "libraries": [{"id": "peter", "libraryId": "6f1c…"}, {"id": "penny", "libraryId": "a90e…"}]},
     "capabilities": {"video": true, "download": true, "rate": true, "location": true},
     "expiresAt": null, "paused": false, "approve": false, "createdAt": "…",
     "devices": [
       {"id": "d_3f9a1c2e7b5d4a10", "name": "Anna's iPhone", "secretSha256": "…64 hex…",
        "pairedAt": "2026-10-14T19:02:11+03:00", "lastSeen": "…", "lastAddress": "192.168.1.34",
        "browser": "Safari on iPhone", "cookieIssuedAt": "…"}]},
    {"id": "s_…", "name": "Summer guests", "scope": {"kind": "album", "…": "see Shares"}, "…": "…"}
  ]
}
```

Unknown scope kinds (a newer picmover's file read by an older one) load
as **inactive**: they are shown as "Unknown kind; update picmover" and
never authorize anything. `version` changes only for incompatible
changes. A new scope kind is a new `kind` value, not a new version.

Why the user config and not the workspace:

- The secrets (CA key, device hashes) must not travel with an external
  photo drive.
- Another computer serving the same drive must not accept this
  computer's phones.
- The workspace folder can be readable by other accounts.
- Sharing is a decision about this machine and network, not about the
  photos.

Scopes are keyed by workspace root (links resolved, as `/api/libraries`
reports it). After an app workspace switch, only the shares of the new
root are active. The others answer `unavailable` until their root is
served again. A single-library `serve <lib>` uses the library's root as
the workspace key, with its `singleID` as the id.

`lastSeen`/`lastAddress` are updated in memory on every request and
written at most every 5 minutes and at shutdown, so browsing does not
rewrite the file constantly.

## Pairing protocol

### Issue (computer)

1. The user clicks "Pair a phone" on a share's card. The admin UI posts
   `/api/share/shares/{sid}/pairings`. The pairing is bound to that
   share: whoever redeems it becomes a device of that share and of no
   other.
2. The server creates:
   - a **token**: 32 random bytes (`crypto/rand`), base64url, 43
     characters;
   - a **code**: 8 characters from a 32-letter alphabet with no
     look-alikes (`23456789ABCDEFGHJKMNPQRSTUVWXYZ`, 40 bits), shown as
     `K7QM-3XRD`.

   Both are kept **in memory only** as SHA-256 hashes, with
   `expiresAt = now + 2 min`, `used = false`, and a counter of wrong
   code attempts, and the share id. At most 3 pairings are open per
   share and 10 in total; issuing one more drops the oldest.
3. The answer carries `url =
   https://192.168.1.20:7443/#/pair/<token>` (the token in the
   **fragment**, which browsers never send to the server, so it stays
   out of request lines, logs and Referer), the code, and an SVG QR of
   the url.
4. The page shows the QR, the code under it, a 2-minute countdown, the
   URL in text, and the certificate fingerprint ("The phone will warn
   that the connection is not private. That is expected for the first
   visit. The certificate's SHA-256 fingerprint starts with AB:CD:EF").
   When the time runs out the QR is blurred out with "Show a new code".
   Closing the dialog cancels the pairing.

### Redeem (phone)

5. The camera app opens the URL in the phone's default browser. The
   certificate warning appears the first time (see "TLS"). After the
   user accepts it, the browser loads `/` → `share.html`.
   **Unverified:** whether every target browser keeps the fragment
   across its "proceed anyway" interstitial. If it does not, the page has
   no token and shows the code form; the user types the 8 characters.
6. The share app sees `#/pair/<token>`, at once runs
   `history.replaceState` to `#/pair` (the token leaves the address bar
   and history), and shows "Name this phone" (pre-filled from the
   browser label, e.g. "iPhone") with a Pair button.
7. `POST /api/pair {token, name}` (or `{code, name}`), with the share
   middleware's Origin/X-Picmover/JSON rules and rate limits. The server:
   - Hashes the token, finds an open pairing, and checks `!used &&
     now < expiresAt`. Otherwise it answers 410 `{error: "This pairing
     code has expired or was already used. Show a new one on the
     computer."}`, or 400 for a malformed body.
   - For a code: a wrong code adds one attempt to **every** open pairing.
     After 5 wrong attempts in total, all open pairings are cancelled
     (the computer shows "Too many wrong codes; show a new one"). With 40
     bits and at most 5 tries per 2 minutes, guessing is hopeless.
   - Checks the pairing's share: it must still exist and be active (not
     paused, expired, broken or unavailable), else 410 with that
     reason.
   - Marks it used (an atomic swap under a mutex, so two concurrent
     redeems cannot both win).
   - **Already paired:** when the request carries a valid device cookie
     (of any share) and `replace` is not true, the server answers 409
     `{error, current: {share: name}}` without using up the token. The
     phone asks "This phone is already paired with 'My phone'. Pair it
     with 'Summer guests' instead?", and Yes resends with `replace:
     true`.

     The old device is then revoked in the same save as the new one is
     created. One cookie per browser keeps resolution simple: the cookie
     names exactly one device and so one share. Holding two shares in one
     browser would need per-share cookie names and a share in every URL;
     that is not worth it, since the owner's own share covers what an
     album share shows.
   - **Approval** (the share's `approve` setting): the redeem becomes a
     pending request (name, browser, address) that the admin page shows
     with Approve and Deny. The phone's request waits (long poll, up to
     60 s; then 410 "Not approved in time"). Deny gives 403. Approve
     continues below. The token counts as used from the moment the
     request is made, so a second scan cannot queue a second request.
   - Creates the device:
     - `id = "d_" + 8 random bytes hex`;
     - `secret` = 32 random bytes;
     - `secretSha256` stored;
     - `name` trimmed to 1–60 characters, control characters removed,
       "Phone" when empty;
     - `browser` from the User-Agent;
     - `pairedAt`.

     It saves `share.json`, with at most 20 devices per share ("Revoke a
     phone first").
   - Answers 200 `{device: {id, name}, share: shareJSON}` with
     `Set-Cookie: __Host-picmover=<id>.<base64url secret>; Path=/; Secure; HttpOnly; SameSite=Strict; Max-Age=34560000`
     (400 days, Chrome's cap).
8. The phone goes to `#/`: the libraries for a library share, or
   straight into the album for an album share. The computer's Share page
   (polling) shows "Paired with My phone: Anna's iPhone, just now" with
   **Rename** and **Revoke** buttons right there, and the QR is gone.

### Staying signed in

- The cookie is the only credential. `__Host-` forces `Secure`,
  `Path=/` and no `Domain`, so it is bound to exactly this host and
  port. Gecko, WebKit and Blink all implement cookie prefixes.
- **Renewal:** on an authenticated request, when `cookieIssuedAt` is
  older than 7 days, the response re-sends the same cookie with a fresh
  `Max-Age` and updates `cookieIssuedAt`. The cookie then never reaches
  the 400-day cap on a phone in use.
- **Expiry:** a device unused for 400 days is removed at the next start
  (its cookie has expired anyway).
- **Loss:** "Clear history and website data", a private tab, or an iOS
  home-screen web app (which has its own cookie jar; **unverified** for
  current iOS) mean the phone is not paired there. The share app's
  "Not paired" screen says to show a new code on the computer, and offers
  the code form, which also works inside a home-screen app where the
  camera cannot open a URL.

### Revocation

"Revoke" removes the device record and saves. The very next request
with that cookie is 401, because every request checks the in-memory
registry. Connections stay open but carry no session state. The phone
shows "This phone was removed from picmover on the computer". Renaming
changes the label only.

Removing access in bulk works the same way, on the next request:

- **Revoke all** on a share removes its devices.
- **Delete** a share removes it and its devices.
- **Pause** and **expiry** keep the devices but refuse them (403 with
  the state).
- Editing a scope (a library removed from a library share) or a
  capability takes effect on the next request too, because the share is
  read from the in-memory registry on every request. Nothing about the
  scope is cached in the cookie or on the phone.

### Threats to pairing

- **Someone photographs the QR or reads the code:** it is valid for 2
  minutes, works once, and works only on the home Wi-Fi with sharing on.
  If an intruder redeems it first:
  - the user's own scan fails with "already used";
  - the computer shows the unexpected device's name, browser label and
    address in the paired list, with Revoke next to it.

  The share's "Ask on this computer before a phone is paired" setting
  (off by default, as decided) closes that window completely. The
  intruder's device would also land only in the share whose QR they saw:
  for a guest album share, that one album.
- **Replay of a pairing request:** the token is single use, so a replay
  gets 410. A captured device cookie is equivalent to the phone itself.
  It is protected by TLS, `HttpOnly` (no script access) and
  `SameSite=Strict` (never sent by other sites' requests), and limited
  by revocation and the "last seen / address" display.
- **Brute force:** tokens and secrets are 256-bit, so rate limits only
  limit log noise and CPU. Codes are covered above.
- **The QR or URL leaking through the phone:** the fragment is removed
  from the address bar at once. The token is dead after use or after 2
  minutes, so screenshots and history entries hold nothing useful.

## TLS

### Certificates

- **Local CA:** generated once per user (`share/ca.pem`, `ca.key`).
  - ECDSA P-256, self-signed, `CN = picmover local CA (<hostname>)`.
  - `BasicConstraints CA:TRUE, pathLen 0`; `KeyUsage certSign`.
  - Valid 10 years.
  - **Name constraints (critical):** permitted DNS `local` and
    `home.arpa`, permitted IP ranges 10/8, 172.16/12, 192.168/16,
    169.254/16 and 127/8.

    This limits the damage if the CA key is ever stolen from a phone
    that trusts it: it cannot vouch for any public site. Support for
    constraints on user-installed roots is **unverified** on iOS and on
    Sailfish (Gecko's NSS supports name constraints in general).
- **Leaf:** ECDSA P-256, signed by the CA.
  - `EKU serverAuth`, `KeyUsage digitalSignature`.
  - SANs: every private IPv4 address of the eligible interfaces, plus
    `127.0.0.1`, plus `<hostname>.local` when the hostname is a valid DNS
    label.
  - Valid **397 days**. Apple rejects TLS leaves valid longer than 825
    days even under user-installed roots, and 397 days stays inside every
    browser's current rule.
  - Reissued automatically:
    - 30 days before expiry;
    - when the bound address is not among its SANs (a DHCP change);
    - when the user clicks "Renew certificate".

  The leaf's key is new at every reissue.
- **Why a CA and not one self-signed leaf:**
  - **Without** installing anything on the phone, the experience is the
    same: one warning per new leaf.
  - **With** the optional install (Q3), reissued leaves (a new IP, a
    yearly renewal) are trusted with no new warning, and the iOS media
    stack problem below goes away.

  A single self-signed leaf would have to carry `CA:TRUE` to be
  installable on iOS anyway.
- `tls.Config`: `MinVersion: tls.VersionTLS12`, Go's default cipher
  suites (Gecko ESR91, iOS 15+ and current Chrome all do TLS 1.3 with
  P-256), and `GetCertificate` returning the current leaf (an atomic
  pointer), so a reissue needs no restart.
- **Fingerprints:** SHA-256 of the leaf and of the CA, shown on the
  Share page in groups (`AB:CD:EF:…`) and printed by `serve --share`.

### What the fingerprint in the QR can and cannot do

A web page cannot see or pin the certificate of its own connection, and
the browser decides about the warning before any picmover code runs on
the phone. Anything the page could check (a fingerprint in the QR
fragment) would be checked by code that an attacker in the middle
served. So the QR does **not** carry a fingerprint, and the design does
not claim pinning. What does help:

- The computer shows the fingerprint. Every target browser can show the
  certificate's SHA-256 from its warning page or its padlock or
  "not secure" menu, so a careful user can compare the first few groups
  (the Share page says how, per browser).
- The optional CA install (Q3) turns that one-time comparison into real
  trust from then on.

### Browser behaviour (all marked items must be confirmed in Phase 0)

| | iOS Safari (iOS 17/18) | Android Chrome (current) | Sailfish Browser (Sailfish OS 5.1) |
|---|---|---|---|
| Engine | WebKit | Blink | Gecko **ESR 91** (stock browser, embedlite). Jolla targets ESR 115 for 5.2, not 5.1. Community builds of newer Gecko exist but are not the target. |
| First visit to a self-signed site | "This Connection Is Not Private" → Show Details → "visit this website" → confirm | "Your connection is not private" → Advanced → "Proceed to … (unsafe)" | A security warning page; whether it offers a lasting "accept the risk" exception like desktop Firefox is **unverified** |
| How long the exception lasts | **Unverified:** believed to last until Safari's website data is cleared | Remembered for about a week, then the warning returns (**unverified** exact period) | **Unverified** (desktop Firefox stores permanent exceptions; embedlite may not) |
| Address bar | "Not Secure" | Red "Not secure" | **Unverified** |
| `Secure` and `__Host-` cookies with an accepted exception | Expected to work (the scheme is https) | Works | Expected to work (Gecko 91 supports `SameSite`, `Secure` and prefixes); **unverified** in embedlite |
| HTTP cache for responses over an excepted certificate | **Unverified** | Chrome does not cache them, so thumbnails are re-fetched on every visit (**unverified** for current versions) | **Unverified** |
| `<video>` over an excepted certificate | **Known risk:** media loads go through a separate media process that may not honour Safari's exception, so playback can fail while images work. Also check that cookies are sent on media requests. | Expected to work (same network stack) | **Unverified.** Playback goes through GStreamer on the device, and codecs depend on the device and its hardware decoders. H.264/AAC MP4 is the likely common format, but HEVC (`.mov` from iPhones) and VP9 are uncertain. |
| Service worker / PWA install | Not on a certificate-error origin | Not allowed on a certificate-error origin | Not relevant (embedlite) |
| `.local` names (mDNS) | Resolves natively | **Unverified** (Android support for `.local` in Chrome has been inconsistent across versions) | **Unverified** (Sailfish has Avahi packages, but whether the browser's resolver uses mDNS is unknown) |

Consequences built into the design:

- **Video fallback:** the lightbox always offers **Download** next to
  the player, and shows "This video cannot be played in this browser"
  on the `<video>` `error` event. Download navigations go through the
  browser's main stack.
- **If the iOS spike confirms the video problem:**
  - First remedy: the optional CA install (Q3).
  - Second remedy (Q5): a per-item media capability URL. The file route
    accepts `?t=<token>` on live rows, where the token is an HMAC of
    (device id, media id, expiry ≤ 10 min) under a per-run key. The
    `<video>` then needs no cookie. That does not fix the certificate
    side, only the cookie side.
- **Host changes and cookies:** cookies are per host. When the IP
  changes, the phone is unpaired at the new address. The design reduces
  this with:
  - the same-network rebind (the old address simply stops working);
  - the recommendation, on the Share page and in the README, to give the
    computer a fixed address (a DHCP reservation in the router);
  - the optional mDNS URL `https://<hostname>.local:7443/` (Q9), which
    survives IP changes for phones that resolve `.local`.

  picmover does not run its own mDNS responder: Avahi on Linux, Bonjour
  on macOS and the Windows 10+ mDNS responder publish `<hostname>.local`
  already, which is **unverified** on Windows for incoming name queries.
  The Share page shows the `.local` URL only when `mdns` is on.

## Address, port, firewall

### Which address

`netif.Candidates()` lists every interface that is up, not loopback, not
point-to-point, and has an RFC 1918 IPv4 address. Interfaces whose names
mark them as virtual (`docker*`, `br-*`, `veth*`, `virbr*`, `vmnet*`,
`vboxnet*`, `tun*`, `tap*`, `wg*`, `tailscale*`, `zt*`, `utun*`, and
Windows "vEthernet") are listed separately as "other networks" and never
chosen by default.

The default is the interface of the default route. It is found
portably: dial UDP to `192.0.2.1:9`, which sends no packet, and read the
local address. The gateway comes from `/proc/net/route` on Linux,
`GetIpForwardTable2` on Windows and `route -n get default` on macOS.
Without a gateway, the "home" check uses the subnet alone.

The Share page offers the candidates in a dropdown when there is more
than one. IPv6 is not supported in this version: the URL is IPv4, and
the listener binds one IPv4 address.

### Port

The default is 7443, saved once used, and configurable on the Share page
and with `--share-port`. Phones bookmark the URL and the cookie is bound
to host and port, so a busy port is **not** replaced by a random one:
sharing goes to `error` with "Port 7443 is in use by another program;
choose another port on the Share page".

### Showing the URL

The Share page shows `https://192.168.1.20:7443/` (with a copy button),
the QR for pairing, and the `.local` URL when enabled. `serve --share`
prints it. The tray tooltip includes the address.

### Firewall

picmover never changes firewall rules. The Share page shows "If the
phone cannot connect" with the exact commands for the current subnet and
port.

- **Linux, ufw** (inactive by default on Ubuntu desktop):
  `sudo ufw allow from 192.168.1.0/24 to any port 7443 proto tcp`.
  Check with `sudo ufw status`.
- **Linux, firewalld** (Fedora, openSUSE): put the Wi-Fi connection in
  the home zone and open the port there, with
  `sudo firewall-cmd --zone=home --add-port=7443/tcp --permanent && sudo firewall-cmd --reload`.
  The Wi-Fi connection's zone is shown by `firewall-cmd
  --get-active-zones`.
- **Windows:** the first time the executable listens on a non-loopback
  address, Windows Defender Firewall asks whether to allow it on
  "Private networks" and "Public networks". The page says to allow
  **Private** only, and to set the Wi-Fi network's profile to Private
  (Settings → Network → Wi-Fi → the network → Network profile type).
  The rule is tied to the executable's path, so moving the exe asks
  again. A "Public" profile blocks the phone even when Private was
  allowed.
- **macOS:** the Application Firewall is off by default. When it is on,
  it asks "Do you want the application picmover to accept incoming
  network connections?". For an unsigned or ad-hoc-signed binary the
  question can come back after every rebuild or update (**unverified**
  for current macOS).

picmover cannot test its own reachability from outside: a connection to
its own LAN address stays on the host and does not pass the firewall
the same way. The page instead shows, per paired device, when it was
last seen. Once sharing is on, a "Waiting for the phone…" state during
pairing that lasts over 30 s shows the firewall hint.

## Phone UI (`share.html`)

### How the app knows it is in share mode

By its entry point. Vite builds two inputs, `index.html` (admin, as
today) and `share.html`, which mounts `ShareApp.svelte` from
`src/share/main.ts`. The share listener serves `share.html` for `/`, and
never `index.html`. The admin listener does not serve `share.html`
specially. This needs no runtime detection, and the share bundle does
not pull in the admin pages: Vite splits the shared components into
common chunks. `shareapi.ts` implements the `LibApi` subset the reused
components need (timeline, bucket, detail, thumb/preview/large/file
URLs, albums, setRating) against the same URL shapes, so `Grid`, `Tile`
and `Lightbox` work unchanged where they are reused.

### Routes (hash router, `src/share/routes.ts`)

The app reads `GET /api/session` at start. The share's `kind` and
`capabilities` decide the routes and the controls.

| Route | Library share | Album share |
|---|---|---|
| `#/pair/<token>`, `#/pair` | pairing (name field, Pair; the code form when there is no token; the "replace" question) | same |
| `#/` | the share's libraries (cards with cover and count); with one library it redirects there | **the album grid** (`/api/l/album/timeline`) |
| `#/l/<id>/` | grid (timeline, newest first) | → `#/` |
| `#/l/<id>/albums` | album list (cards) | → `#/` |
| `#/l/<id>/albums/<aid>` | grid of the album | → `#/` |
| `#/l/<id>/view/<mid>` | lightbox over the grid it came from | `#/view/<mid>` instead |
| anything else | `#/` | `#/` |

Error screens:

- On 401, every route shows "Not paired" (show a code on the computer,
  or type the code).
- On 403 with a state: "This share is paused", "This share has ended",
  or "This album is no longer shared". No other content is shown.
- On 503: "This library is not available right now (the computer is
  busy with it). Try again later."

### What is shown

- **Top bar** (compact, 48 px). The title is the **share name**
  ("Summer guests"); in a library share, the library name follows it.
  - In a library share: a Photos/Albums switch, a filter button (a
    bottom sheet with Photos/Videos/All, rating ≥ 1–5 when `rate` is on,
    and a year range), and a menu (Libraries, "Paired as Anna's iPhone",
    Help).
  - In an album share: no library or album navigation at all. The album
    name and photo count sit under the title, there is a filter button
    with Photos/Videos/All and the year range, and the menu has "Paired
    as …", the expiry ("Available until 13 Nov") and Help.
- **Grid:** the timeline with month headers and the scrubber.
  - Column count by width: 3 in portrait under 500 CSS px, 4–5 in
    landscape or on tablets. Pinch-to-change-columns is later.
  - Tiles show the video badge and duration, and the rating stars (only
    with `rate`), always visible: there is no hover on touch.
  - No selection and no checkboxes. A tap opens the lightbox.
- **Lightbox:**
  - Swipe left and right (exists), swipe down to close, tap to toggle
    the chrome.
  - Double-tap toggles fit and 1:1 on the original when displayable
    (only with `download`), else on the preview. Native pinch-zoom is
    allowed inside the lightbox only (`touch-action: pinch-zoom` on the
    image).
  - Bottom bar, each control only with its capability:
    - **RatingStars** (`rate`; 5 stars, 44 px targets, tap the current
      rating to clear);
    - **Download** (`download`; `file?download=1`);
    - **Info**, always: a bottom sheet with the redacted detail (date,
      camera, lens, size, dimensions, albums in a library share, and
      GPS as text with `location`).
  - Video (`video`) uses `<video controls playsinline preload="metadata">`
    with the preview as poster, and Download beside it when allowed.
    Without `video`, video tiles show the preview with a "Video not
    available in this share" note. Without `video` and `download`, a
    video can still be seen as its poster.
- **Hidden** (absent from the share bundle, not merely hidden): Import,
  Imports, Library stats, Map, Trash, Similar, Backups, Libraries
  management, workspace setup, the selection bar, tags, the album editor,
  move, the job pill, toasts about jobs, and the folder picker.
- **Rating feedback:** optimistic, rolled back on error.
  - A 409 (a move runs on the library) shows "Ratings are paused while
    photos are being moved on the computer".
  - A 404 shows "This photo is no longer in the library" (in an album
    share, "no longer in this album").
  - A 403 (the capability was turned off meanwhile) re-reads the session
    and hides the stars.

### Responsive and touch work in shared components

- `Tile`: an always-visible variant of the date and rating for coarse
  pointers (`@media (hover: none)`) and a `readonly` prop with no check
  box.
- `Lightbox`: a `mode="share"` prop that leaves out tags, album editing,
  trash, move and the admin info fields. Add swipe-down,
  `playsinline`, safe-area insets (`env(safe-area-inset-*)`), and a
  bottom-sheet info panel under 640 px.
- `Grid`: a `columns` override for share mode; no Ctrl+wheel. The
  rendition choice is capped (next section).
- `RatingStars`: 44 px hit targets under `(pointer: coarse)`.
- Use `100vh` with a JS fallback rather than `dvh`, since Gecko 91 has
  no `dvh`/`svh` units. Avoid `:has()`, container queries, CSS nesting
  and `color-mix()`, which Gecko 91 lacks, in any component the share
  bundle uses. A Stylelint rule is not worth adding; a review checklist
  item and the Sailfish test cover it.

### Sailfish Browser (Gecko ESR 91) specifics

- **Build target:** the share entry needs a lower target than the
  admin's `es2022`:
  - `build.target: ['es2020', 'firefox91', 'safari15', 'chrome100']`
    for the whole build. One build is simpler, and the admin UI loses
    nothing measurable.
  - esbuild then lowers class static blocks (Gecko 93+) and other
    post-ES2021 syntax. Private fields and methods (`#x`, Gecko 90) and
    top-level await (Gecko 89) are native.
  - ES modules (`<script type="module">`, dynamic `import()`) are
    supported since Gecko 60.
- **Runtime APIs are not lowered** by esbuild. Missing in Gecko 91:
  `Object.hasOwn` (92), `structuredClone` (94),
  `Array.prototype.findLast`/`findLastIndex` (104),
  `Array.prototype.toSorted`/`toReversed`/`with` (115),
  `String.prototype.replaceAll` exists (77). Before building the share
  entry:
  - grep `web/src` and the built share chunks (which include Svelte 5's
    runtime) for these;
  - add a tiny explicit polyfill module imported first by
    `share/main.ts` for any that Svelte's runtime needs;
  - add a build-time check script (`scripts/check-share-apis.mjs`) that
    fails the build when a listed API appears in a share chunk without
    the polyfill.

  Whether Svelte 5's runtime runs on Gecko 91 at all is **unverified**
  and is the first Phase 0 item for Sailfish.
- **Touch:** Gecko 91 supports Touch Events and Pointer Events
  (`touch-action` included), so the existing swipe code and the
  Scrubber's pointer capture should work. Sailfish's own edge-swipe
  gestures (from the screen edges, for the OS) take precedence, so
  lightbox swipes must start at least 24 px from the left and right
  edges to be reliable. That is a recommendation, not a measured value.
- **Rendering cost:** Sailfish devices are often older and slower
  phones. The share grid caps renditions at `thumb` (below), keeps
  overscan to one screen, and avoids blur filters (the lightbox
  blur-up uses a scaled thumbnail without `filter: blur` under
  `prefers-reduced-motion` or in share mode).

## Concurrency with the admin UI and jobs

- **Locks:** a phone request on a closed shared library opens it with the
  same `ws.open` as the admin "Open", so it takes the library lock. While
  the server runs, a CLI `import` on that library is then refused, as it
  would be after an admin open. The Share page says this: "Shared
  libraries are opened (and locked) when a phone views them". A library
  locked by another process is 503 to the phone.
- **Rebuild:** 503 to the phone while it runs, then normal.
- **Moves** (`library-move.md`): the share rating route is wrapped in
  `notMoving` like the admin one → 409 while a move runs on that
  library. Reads continue. Photos moved away become 404 on the phone
  (no longer live rows); the grid drops them at its next timeline load.
- **Trash from the admin UI:** a trashed photo is 404 for every share
  route (the live-row check), so a phone that still shows its tile gets
  a broken image. The share grid reloads the timeline when the tab
  becomes visible again (`visibilitychange`), and on any 404 from the
  lightbox.
- **Ratings in both directions:**
  - A phone rating is a normal `SetRating`. An admin grid that is open
    shows the old value until it reloads its bucket. This version adds
    no live sync; Q10 covers a small "changes" stream.
  - Two writers of one rating are last-write-wins, as between two admin
    tabs today.
- **Jobs:** phones cannot start, list or watch jobs. Imports add rows the
  phone sees after its next timeline load. Thumbnail jobs and the
  `large` deriver are shared, and the existing slot limit bounds the
  deriver.
- **Album edits from the admin UI:** an album share reads membership
  on every request (`AlbumHas` for rows, the album hash set for lists),
  so a photo removed from the album is 404 at once, and an added one
  appears at the next timeline load. A move between libraries takes a
  photo's album membership along to the target library
  (`library-move.md`), so it leaves an album share of the source
  library, which is correct: the share is of that library's album.
  Deleting the album breaks the share (see "Shares").
- **Workspace switch** (app): the share mux reads `ws.root()` per
  request, so shares of the old root become `unavailable` at their next
  request, and shares of the new root become active. Entries retired by
  the switch answer 503.
- **Shutdown:** `Running.Shutdown` stops the share `http.Server` first
  (5 s drain), then the admin path as today. Pairing state is dropped,
  and `share.json` is flushed (last seen).

### Performance

- **Grid renditions on phones:** a 3-column portrait grid on a 390 px
  wide phone with DPR 3 needs about 390 device px per tile. The current
  `renditionFor` would pick `large` (640 px) for every tile. That
  derives large thumbnails on demand on the computer for every month
  scrolled through, which is CPU-heavy over 100k photos. Share mode
  passes a cap to `renditionFor`: `thumb` (256 px, at most about 1.5×
  upscale) for grid tiles, `large` only for 1–2 column layouts, and
  `preview` (1600 px) in the lightbox.
- **Caching:** rendition URLs are versioned (`?v=thumbAt`) and cacheable
  `private`. If Chrome does not cache over an excepted certificate,
  HTTP/2 multiplexing keeps re-fetching thumbnails cheap on a LAN (a
  256 px JPEG is about 15–25 KB).
- **Video:** `http.ServeContent` answers `Range` with 206, which iOS
  requires (it probes with `bytes=0-1`). Originals stream from disk, and
  there is no transcoding in this version. A file the browser cannot
  play gets the Download fallback.
- **JSON:** the media list is the admin shape (about 600 KB for a
  5,000-item month), which is fine on Wi-Fi. Gzip stays a non-goal.

## Threat model

Assets:

1. The photos and videos **within each share's scope**: the libraries
   of a library share, or the members of an album share's album.
2. Everything **outside** a share's scope:
   - other libraries;
   - in an album share, the rest of that library, including its other
     albums, their names and its size;
   - the trash and import sources;
   - the file system (through `/api/fs`);
   - the admin powers (import with move, trash, empty trash, move
     between libraries, rebuild, backups).
3. The integrity of `user.db` (ratings).
4. The availability of the computer and the library locks.
5. Location data (GPS), and the owner's ratings as private judgements.

Adversaries and what stops them:

| Adversary | Can | Stopped by |
|---|---|---|
| A guest or IoT device on the home Wi-Fi, not paired | Connect to the share port | Device auth on every API route (401); rate limits; static files reveal only public code; the admin listener is loopback-only |
| The same, with a share's QR or code in view | Pair with **that** share within 2 minutes, before the user | Single use, short TTL, the device shown at once with Revoke; the share's approval setting closes it fully; a guest QR grants only that guest share's scope |
| A guest paired with an album share (semi-trusted) | View that album, within the share's capabilities; probe ids and hashes; try other routes | Scope checks on every route (row membership, forced album filter, library-level routes 404, no `album` filter); capabilities enforced on the server; ids and hashes outside the scope are the same 404 as nonexistent; folder names and album ids are hidden behind the `album` key; expiry and pause |
| A guest who forwards their link or a screenshot of the URL | Let someone else in | The URL is just the address; access needs the device cookie, which is `HttpOnly` and per browser. A new phone needs a new QR from the owner. |
| A guest who keeps the phone after the visit | Keep viewing | Album share expiry (30 days by default), Revoke, Pause |
| The owner's paired phone (lost or stolen) | View the share's libraries, download originals, change ratings | Intended for the owner. Revoke, Revoke all, "last seen"; ratings logged with device and share; no access to anything in asset 2 |
| A malicious web page in the phone's browser | Make requests to the share origin | `SameSite=Strict` cookie, Origin and Sec-Fetch checks, `X-Picmover` on POST, no CORS, CORP same-origin, `frame-ancestors 'none'` |
| A malicious page in the computer's browser (DNS rebinding, CSRF) | Reach the admin API or the share management routes | Unchanged admin checks; `/api/share/*` additionally require a loopback listener and peer |
| An attacker on the LAN doing ARP spoofing (MITM) at first contact | Present its own certificate and relay | Accepted risk for a home network: the user clicks through a warning either way. Mitigations: the fingerprint comparison, the optional CA install (Q3), and the LAN-only listener. Documented honestly on the Share page. |
| Other networks (the laptop on café Wi-Fi) | Reach the share port | Bound to one interface; connection filter; pause on network change; auto-off; device auth still required |
| A local malicious process on the computer | Read `share.json`, keys and the libraries | Out of scope: it can read the photos directly. Files are 0600 so that other local *accounts* cannot. |
| A future developer adding an admin route, or a scope kind | Expose it to phones, or let a list return rows that `visible` refuses | The separate allowlisted mux plus the golden-list test; the route × scope test matrix and the `restrict`/`visible` agreement test |

## Security and safety rules

1. The share listener serves **only** `shareRouteTable`. It has no
   catch-all into admin code, and a test fails when the table changes
   without the golden list.
2. The admin listener stays loopback-only. Sharing cannot start, and
   `/api/share/*` answers 403, unless the admin address and the request's
   peer are loopback.
3. Every share API request except `session` and `pair` needs a valid,
   unrevoked device, and the device's **one** share must be active (not
   paused, expired, broken or of another workspace). Verification
   compares SHA-256 hashes in constant time. Secrets are never stored or
   logged in clear.
4. Every library route resolves device → share → scope and checks:
   - the library against the scope and its `LibraryID`;
   - for album scopes, the album against its id and `created_at`;
   - that the row or hash is **live** and `visible` in the scope;
   - lists are restricted by the same scope.

   Everything outside the scope (other photos, albums, libraries, the
   trash, imports, jobs, settings, the file system) answers like a
   nonexistent thing. Routes never branch on the scope kind; only
   `scopeAuth` does.
5. Capabilities are enforced on the server (file route, ratings,
   response fields), not only hidden in the UI.
6. The only write is a single-hash rating (0–5) on a visible row, only
   with `rate`, refused during a move, and logged with device and
   share.
7. Pairing tokens and codes belong to one share, are single use, expire
   in 2 minutes, live in memory only, travel in the URL fragment, and are
   rate-limited. A device credential is only ever created by a
   successful redeem, for that share only.
8. Shares are never widened automatically. A missing album or library,
   a reused album id, or an unknown scope kind means "show nothing", not
   "show the closest thing".
9. TLS 1.2+, a leaf from a name-constrained local CA, keys 0600 in a
   0700 directory, and no HSTS.
10. Bind one LAN address. Accept only connections from that subnet or
    loopback. Pause when the network changes. picmover never touches
    the firewall.
11. Responses to phones contain no absolute paths, no import sources, no
    holder pids, and no raw errors (500 bodies stay generic). Album
    shares also hide folder names and album ids.
12. Sharing is visible whenever it is on: the admin pill (shares and
    phones), the tray label, and `serve` output lines for pair, revoke,
    pause and expiry.

## Tests

### Go, `internal/share`

- **Config:** round trip, defaults, a damaged file (logged, kept as
  `share.json.damaged`, defaults with no shares and sharing off: fail
  closed, phones re-pair), modes 0600/0700, refusing a group-readable
  key, the one-sharing-process lock (two lockers, second refused with
  the holder), and an unknown scope kind loading as inactive.
- **Shares and devices:**
  - create with the kind's default capabilities; the limits (50 shares,
    20 devices per share);
  - verify returns the device's share, and rejects a wrong secret, a
    wrong id and a revoked device;
  - revoke, revoke all, delete (devices gone), pause and resume (devices
    kept);
  - expiry at `expiresAt` and deletion 30 days later (fake clock);
  - `share.json` never contains a secret (a grep in the test);
  - device expiry after 400 days unused.
- **Pairing:**
  - a token redeems once; the second redeem is 410; expired at TTL+1 s
    (fake clock); concurrent redeems give exactly one winner;
  - the redeem creates the device in the token's share only;
  - redeeming for a paused, expired or broken share is 410;
  - codes are case- and dash-insensitive; 5 wrong codes cancel all open
    pairings; at most 3 open per share and 10 in total;
  - a redeem with an existing cookie is 409 without using the token,
    and `replace` revokes the old device;
  - the approval flow (approve, deny, timeout);
  - tokens are gone after "restart" (a new registry).
- **Rate limits:** buckets per IP and global, and the 5-minute lockout.
- **TLS:**
  - the CA has `CA:TRUE`, pathLen 0 and the critical name constraints;
  - the leaf has the right SANs (IPs, `.local`, `127.0.0.1`), EKU,
    validity 397 days, and verifies against the CA with
    `x509.Verify`;
  - a leaf for a public name or IP fails verification under the CA;
  - reissue on a SAN miss and near expiry, with the CA unchanged;
  - fingerprints are stable.
- **netif:** candidate filtering over fake interface lists (virtual
  names excluded, private ranges only); the subnet check of the
  connection filter; home-network comparison (same subnet with a new IP
  → rebind, another gateway → pause).
- **QR:** the SVG decodes back to the URL (a test-only decoder, or a
  golden file).

### Go, `internal/server`

The tests use the existing fixtures: a workspace with two real
libraries imported with `testimg`, several albums, and some trashed
rows, through `httptest` and a real TLS listener where the connection
matters.

- **Allowlist:** `shareRouteTable` patterns equal the golden list. For
  every admin route pattern (taken from a table that `routes()` is
  refactored to use), a request with a valid device cookie to the share
  handler gets 404. Unknown paths are 404, and static files outside the
  allowlist are 404.
- **Authentication:** every share API route without a cookie is 401;
  with a revoked cookie 401; with a malformed cookie 401. `session` and
  `pair` work without a cookie. The `Set-Cookie` attributes are checked
  exactly. A paused, expired, broken (album deleted) or unavailable
  (other workspace) share is 403 with its state on every route.
- **Route × scope matrix** (one table-driven test). For each scope kind
  (`libraries` with two libraries, `album`) and each route (`libraries`,
  `library`, `timeline`, `media`, `media/{id}`, `thumb`, `preview`,
  `large`, `file`, `file?download=1`, `albums`, `ratings`), and for each
  subject:
  - a row in scope;
  - a live row of the same library outside the scope (another album's
    member, not in this album);
  - a row of an unshared library;
  - a trashed row (once a member of the album);
  - a row just removed from the album (the removal done in the test
    between two requests);
  - a nonexistent id and hash;

  the expected status is in the table (200, or the same 404 body). For
  every list response, every returned row is checked against `visible`,
  and the album share's lists are compared with `AlbumHashes ∩ live`.
- **Album identity:** delete the shared album and create a new one that
  gets the same id → the share is `gone` and the new album's photos are
  never served. Rename and empty the album: the share keeps working.
  Remove the library folder, or swap in a copy with another `LibraryID`
  → `gone` (album share) or that library 404 (library share).
- **Share-local keys:** in an album share `{lib}` must be `album`; the
  folder id and any other value are 404; no response body contains the
  folder name or the album id. An `album` filter in an album share is
  400; in a library share, an album of another library is 404.
- **Capabilities** (a matrix over `video`, `download`, `rate`,
  `location` on and off):
  - `file` for a video row needs `video`;
  - `file` for an image row and `?download=1` need `download`;
  - `ratings` needs `rate`;
  - `rating` fields and the `rating` filter appear only with `rate`;
  - `lat`/`lon`/`gps` appear only with `location`;
  - `displayable` is false without `download`.

  A capability turned off takes effect on the next request.
- **Ratings:** one hash only; a non-visible hash is 404; during a move
  → 409; the log line names the device and the share.
- **Redaction:** the detail JSON keys are exactly the allowed set per
  share kind and capabilities (the test fails on any extra key), and no
  JSON body contains the library root or `originalPath`.
- **Headers:** CSP, no HSTS, no-store on JSON and originals, CORP and
  COOP. Range on file → 206.
- **Middleware:** wrong Host → 421; foreign Origin → 403; POST without
  `X-Picmover` → 403; `Sec-Fetch-Site: cross-site` → 403; the 4 KiB body
  cap; 429 after the limits.
- **Connection filter:** a remote address outside the subnet is closed
  (a wrapped listener with an injected remote address).
- **Management:** `/api/share/*` returns 403 with a non-loopback admin
  address or peer. `serve --share --addr 0.0.0.0:…` is a usage error.
  Pairing returns 409 while sharing is off or the share is not active.
  Create validates the library and album and fills in the identity.
  Edits cannot change the kind or the album.
- **Lifecycle:** start, stop and restart the share listener while the
  admin one serves; a rebind on a same-network address change (fake
  netif); pause on a network change; auto-off timer (fake clock);
  `keepOn` honoured by the app path and ignored by `serve` without
  `--share`; shutdown order.
- **Concurrency:** rating during a move → 409; rebuild → 503; a closed
  library opened on demand and locked; a library locked by another
  process → 503; a workspace switch makes shares unavailable and back.

### `cmd`

- `serve`: flags (`--share`, `--share-port`, `--pair SHARE`), the
  refusal without shares, messages, and the printed URL, fingerprint
  and share lines.
- `share`: `list`, `create` (libraries, album by name, an ambiguous
  album name refused with the ids, album by id, capability flags,
  expiry), `set`, `revoke`, `delete`, and the refusal while another
  process holds `share.lock`.

### Web (vitest, jsdom, fake server)

- The share fake (`testing/fakes.ts` gains `fakeShareServer(kind,
  capabilities)`):
  - pairing with a token (the fragment is removed with `replaceState`),
    with a code, expired (410), used, rate-limited (429), already paired
    (409 → the replace question → resent with `replace`), waiting for
    approval;
  - the "Not paired" screen on 401 from any request, and the
    paused/ended/no-longer-shared screens on 403;
  - library share: one library → redirect; libraries list; album list
    and album grid;
  - album share: `#/` is the album grid, the title is the share name,
    and the library and album routes redirect to `#/`. No request ever
    goes to `/api/libraries`, `/library` or `/albums`.
- Lightbox in share mode, for each capability on and off: stars,
  Download, the 1:1 view, video or the "not available" note, and GPS in
  the info sheet. No tag, album-edit, trash or move controls are ever
  rendered. The rating sends one hash; 409 shows the move message; 403
  re-reads the session; the video has `playsinline`; a video error shows
  the fallback.
- The rendition cap in share mode (pure function tests in
  `rendition.test.ts`).
- Admin Share page and `ShareDialog`:
  - the global switch;
  - creating both kinds with the default capabilities and warnings, and
    from the album page with the "Shared (n)" button;
  - per share: the pairing dialog with its countdown and expiry,
    devices with rename and revoke, Revoke all, Pause, Extend, Delete,
    and approval requests;
  - "Album deleted" and "Library missing" states;
  - `managedElsewhere` with disabled controls;
  - the pill with share and phone counts on every page.
- Build check: `scripts/check-share-apis.mjs` runs in `npm run build`
  and fails on unpolyfilled post-Gecko-91 APIs in share chunks.

### End to end (not in `make check`)

`make e2e-share`, a developer target that needs Playwright browsers.
This adds the first browser-driven tests to the repo (Q11).

- It starts `picmover serve <fixture> --share` on loopback, after
  `picmover share create` has made a library share and an album share
  (view only). It issues pairings through the admin API and drives the
  share app with Playwright device profiles "iPhone 13" (WebKit),
  "Pixel 7" (Chromium) and a Firefox profile at 360×640 with touch
  enabled. The Firefox profile is a stand-in for Gecko, not ESR 91.
  It runs with `ignoreHTTPSErrors`.
- Library share flow: pair → libraries → grid → lightbox swipe → rate →
  download → album → video plays (Chromium) → revoke on the admin side
  → 401 screen.
- Album share flow: pair → straight into the album; no stars, no
  Download; remove a photo from the album on the admin side → it is gone
  from the phone after a reload, and its old lightbox URL shows "no
  longer in this album"; pause → "paused" screen; expire (admin sets a
  past date) → "ended" screen.
- Screenshot checks at phone width: no horizontal scroll, touch targets
  at least 44 px.

### Manual device checklist (the release gate; also Phase 0)

On a real **iPhone (Safari)**, **Android phone (Chrome)** and **Sailfish
OS 5.1 phone (Sailfish Browser)**:

1. Firewall hint followed. Scan the QR (on Sailfish, open the URL or
   type the code, since there may be no QR scanner app). Read the
   certificate warning text, accept it, and check that the fingerprint
   matches the Share page.
2. Pairing keeps or loses the fragment through the interstitial (record
   which; the code form must work in either case).
3. Grid scroll over a large library is smooth. Thumbnails are cached or
   not across visits (record which).
4. Lightbox swipe, swipe-down, double-tap, and pinch. On Sailfish, check
   that the OS edge gestures do not steal the swipe.
5. Rate a photo; the admin grid shows it after a reload.
6. Videos: H.264 MP4, iPhone HEVC `.mov`, and an Android MP4. Each one
   either plays, or shows the fallback and downloads.
7. Download an original (a JPEG and a RAW).
8. Close the browser, reboot the phone, and come back after 8 days: is
   the phone still paired, and has the certificate warning come back?
9. Revoke from the computer: the phone shows the removed message at the
   next tap.
10. Change the computer's IP (renew DHCP or set a reservation): the
    phone at the old URL fails; the new URL needs re-pairing; with mDNS
    on, the `.local` URL keeps working (where it resolves).
11. Laptop on another network: sharing pauses.
12. Sailfish only: the app loads at all (Svelte 5 on Gecko 91), and no
    console errors come from missing APIs.
13. Album share on one phone: it opens straight into the album, shows
    the share name, offers no stars, Download or 1:1 view, and plays
    videos. Pair the same phone with the library share: the replace
    question appears, and afterwards the phone is in the library share
    only.

## Phases

0. **Spike on real devices** (half a day, throwaway code). A Go
   program serves a self-signed HTTPS page with an image, a video (H.264
   MP4 and HEVC MOV), a `__Host-` cookie set via fetch, a fragment
   redirect test, and the production build of a tiny Svelte 5 page with
   the planned build target. Run checklist items 1–8 and 12 on all three
   phones. The results settle the **unverified** cells of the browser
   table and the Phase-0-dependent decisions (Q3, Q5, Q9), and they are
   recorded in this document before Phase 1.
1. **Backend:**
   - `internal/share`: config, shares and devices, scope data, pairing
     with approval, TLS, netif, rate limits;
   - in the server: the share listener in `Running`, the allowlisted
     mux, `scopeAuth` with both kinds, the redacted handlers and
     capability checks, `LiveByHash` and `AlbumHas`, and the admin
     `/api/share/*` routes;
   - on the command line: `serve --share` and `picmover share`.

   Go tests, including the route × scope and capability matrices.
2. **Admin UI:** the Share page with share cards, `ShareDialog`, "Share…"
   on the album page, and the pill. Then the tray item and `keepOn` in
   the app.
3. **Phone UI:** the share entry, the shell for both kinds, pairing
   (replace, approval), grid/lightbox share modes driven by
   capabilities, the Gecko 91 build target and polyfills. Web tests.
4. **E2E target and the manual checklist on devices. README section.**

## Phase 0 probe

`tools/shareprobe` is the Phase 0 spike: a separate `main` package in
the module, never linked into picmover, that serves a self-checking test
page to the phones. It uses the design's TLS setup (the name-constrained
P-256 CA and a 397-day leaf for the LAN address, `127.0.0.1` and
`<hostname>.local`), the planned response headers and CSP, no HSTS, and
the real `__Host-picmover` cookie. Its only `go.mod` change is
`rsc.io/qr` v0.2.0 (Q12's recommendation; pure Go, no dependencies),
which the product would use for its QR codes anyway.

Run it on the computer (ffmpeg on `PATH` makes the test clips):

```
go build -tags nodynamic ./tools/shareprobe
./shareprobe -dir ~/.cache/picmover-shareprobe   # keep the dir for the later-visit items
```

It prints the URLs, both SHA-256 fingerprints, the firewall commands
(ufw and firewalld, to run by hand and undo afterwards), per-phone steps,
and a QR code for `https://<ip>:7443/pair#<token>` and the `.local`
variant (also written to `pair-qr.png`). A phone without a scanner types
the short `https://<ip>:7443/pair#<code>` instead, which tests the
fragment the same way. Flags: `-ip` (another interface), `-port`,
`-clips DIR` (add real iPhone or Android recordings), `-v` (every page
load and media request), `-qr-invert`.

The page runs the checklist by itself, shows PASS/FAIL per item, and
posts every snapshot (with the user agent) to the probe, which appends
it to `<dir>/results.jsonl` and prints a one-line summary. Each media
request (cookie present, Range header, status) goes to `<dir>/media.log`.
Devices are kept in `<dir>/devices.json`, so the later-visit items work
across probe restarts.

| Item | Measures | Settles |
|---|---|---|
| 1 | The pairing token in the fragment survives the certificate interstitial; the POST sets the cookie, which comes back and is invisible to script | Redeem step 5 (code form needed as the main path or only as fallback) |
| 2 | The cookie on a fetch, a scripted top-level navigation, a reload, and (manual) after closing and reopening the browser, with the hours since pairing | "Staying signed in"; the `SameSite=Strict` choice for navigations |
| 3 | The cookie on `<img>`, `<video>` (and its Range requests) and a download, counted by the server | Q5 (whether the `?t=` media URL is needed) |
| 4 | H.264/AAC MP4, HEVC MP4 and MOV (`hvc1`), VP9/Opus WebM, AV1 WebM and MP4, each `<video playsinline>` with Range; `canPlayType`, events, errors; `<a download>` of an original | The `<video>` row of the browser table; the iOS media-process risk; the Download fallback |
| 5 | A Svelte 5 component built with the repo's toolchain for `es2020, firefox91, safari15, chrome100` mounts and updates under the planned CSP; feature detection for `Object.hasOwn`, `structuredClone`, `findLast`, `toSorted`, `dvh`, `:has()`, container queries, nesting, `color-mix()`, observers, pointer and touch events, fetch `keepalive`, `Intl` | Q13 and the Sailfish build target and polyfill list |
| 6 | The `.local` URL loads (a background fetch, and a link that reports in from the other host) | Q9 |
| 7 | Whether the certificate warning comes back on a later visit (manual yes/no, kept with the hours since pairing) | "How long the exception lasts" |
| 8 | Touch starts and completed swipes within 24 px of each screen edge vs the middle | The Sailfish edge-gesture margin |
| 9 | A thumbnail served `private, max-age=3600` with an ETag: is a second fetch, and a later visit's `<img>`, served from the HTTP cache (the server counts 200s and 304s) | "HTTP cache over an excepted certificate"; rendition caching |
| 10 | After installing the CA (`/ca.crt`, DER, `application/x-x509-ca-cert`; `/ca.pem` for Sailfish): the page loads without a warning, and port 7444, whose leaf also names `constraint-test.example.com`, still warns | Q3 (offer the install only where constraints are enforced) |

Run on a desktop (headless Chromium with `--ignore-certificate-errors`,
and with the leaf trusted through `--ignore-certificate-errors-spki-list`),
every automatic item passed except item 9, which failed with the
ignore flag and passed with the trusted leaf. That matches the
expectation that Chrome does not cache over a certificate error. OpenSSL
(curl) refuses the port 7444 leaf under the CA. The Svelte bundle is
committed (`tools/shareprobe/static/svelte/probe.js`); rebuild it with
`(cd web && npm ci) && node tools/shareprobe/svelte/build.mjs`, which
also lists any post-Gecko-91 APIs in the output (none for Svelte
5.57.1).

## Settled in review

Decisions by the user on the first version of this document (the Q
numbers are the ones used there and in references above):

- **Q1, ratings:** a phone rating is the library's normal rating. Each
  phone rating is logged with the device and the share. Per-person
  ratings may come later as a design of their own.
- **Q2, approval:** no approval step by default. Each share offers "Ask
  on this computer before a phone is paired" (the approval flow in
  "Pairing protocol").
- **Q3, certificate install:** an opt-in "Install the certificate on
  this phone" step, built only after Phase 0 shows that the phone
  enforces the CA's name constraints, and not offered on a platform that
  does not.
- **Q4, restart:** sharing is off after a restart unless the desktop
  app's "Keep sharing on" is set. `serve` shares only with `--share`.
- **Q5, iOS video:** decided by Phase 0. The certificate install is the
  fix, and Download is the fallback. The `?t=` media URL is built only if
  cookies turn out to be the problem.
- **Q7 and Q8, tags, camera filters, map:** not on phones in this
  version. GPS is a per-share capability (`location`), on for library
  shares and off for album shares.
- **Multiple shares** (a structural change, designed in now):
  - A share is a first-class object: a name, a scope, capabilities, an
    expiry, a paused flag, an approval setting and its own devices.
  - Scope kinds are `libraries` and `album`, behind a `scopeAuth`
    interface, so more kinds can be added without touching routes.
  - Every route is checked against the scope. An album share opens
    straight into its album and is titled with the share name.
  - The admin UI lists the shares, and "Share album…" is also on the
    album page. The CLI gets `picmover share`.
- **Q6, per-device library sets:** replaced by multiple shares. A phone
  sees what its share covers; different phones get different shares.
- **Q15, one cookie per browser:** a browser is paired with one share at
  a time. Pairing it with another share replaces the first, after asking
  on the phone. Per-share cookies would need the share in every URL and a
  share picker; a second browser or a private tab covers the owner
  testing a guest share.
- **Q16, ratings for guests:** ratings are visible only with `rate`.
  Stars are the owner's private judgement. A separate `seeRatings`
  capability stays a possible one-field addition if ever wanted.
- **Q17, default expiry:** album shares expire after 30 days by default
  (changeable in the dialog, including "never"); library shares never
  expire by default.
- **Q18, video without download:** accepted that a streamed video can be
  saved by a determined guest. The share dialog says so next to the
  checkboxes; turning off `video` is the only real guarantee. No
  transcoding in this version.
- **Q19, more scope kinds:** added when needed, each as a new `kind` plus
  a `scopeAuth` implementation, its admin dialog, and its column in the
  route × scope test matrix. "Several albums" is the likely first.
- **Q20, a busy library:** a library locked by another process (or
  rebuilding) gives the phone 503 "This library is not available right
  now". The admin share card shows "Library busy" so the owner knows why.

## Open questions (with recommendations)

Q1–Q8 and Q15–Q20 are settled (above). Still open:

9. **Q9. Offer the `.local` URL?**
   - *Recommendation:* show it as an option after Phase 0 records which
     phones resolve it: iOS likely, Android and Sailfish unverified.
   - Default to the IP URL, and recommend a DHCP reservation.
10. **Q10. Live update of the admin grid when a phone rates?**
    - *Recommendation:* not in this version; a reload shows it.
    - If wanted, add a small SSE stream of `Change` events per library
      on the admin listener. The admin UI already has `emitChange`, so
      that is the place to start.
11. **Q11. Add Playwright for the e2e target?** It would be the repo's
    first browser-driven test, and `server.md` lists it as a non-goal.
    - *Recommendation:* yes, as a separate dev-only target outside
      `make check`, pinned exactly in a separate `e2e/package.json`, so
      the normal toolchain and CI stay as they are.
12. **Q12. QR encoder dependency.**
    - *Recommendation:* `rsc.io/qr` (pure Go, BSD, no dependencies, small
      and stable), pinned, used for the SVG and the terminal output.
    - The alternative is a hand-written byte-mode encoder (about 400
      lines) to keep `go.mod` unchanged.
13. **Q13. Sailfish Browser version.**
    - Sailfish OS 5.1's stock browser is believed to be on Gecko ESR 91,
      with ESR 115 planned for 5.2. This comes from community and Jolla
      forum posts, not from official release notes.
    - *Recommendation:* target ESR 91 (the build target and polyfills
      above), confirm the version on the device in Phase 0 from the
      browser's user-agent, and drop the polyfills when 5.2 is the
      minimum.
14. **Q14. Albums across libraries.** Albums are per library today. If
    cross-library albums ever come, the album scope's key and `visible`
    must be revisited, so that membership never lets a photo of an
    unshared library through.

## As implemented (frontend)

Built against this document's API, in parallel with the backend, so
every answer is parsed leniently (missing fields defaulted, malformed
entries dropped) and tested against the fakes in
`web/src/testing/fakes.ts` (`fakeServer().sharing` for the admin API,
`fakeShareServer(kind, opts)` for the share listener).

### What is built

- **Admin UI** (`#/share`, `pages/Share.svelte`, `ShareDialog`,
  `PairDialog`, `ShareCapabilities`, `SharePill`, `lib/sharing.svelte.ts`,
  `lib/sharetext.ts`): the global switch, network address choice, port,
  auto-off, "Keep sharing on" (app only), the `.local` name as an extra,
  the leaf and CA fingerprints with "Renew certificate", the firewall
  hint; share cards with scope, state ("Album deleted", "Library
  missing", "Library busy"), expiry, capability toggles with the guest
  rating and video saving warnings, Pair a phone (the server's QR as an
  `<img>` of `qrSvg`, the code, the countdown, the firewall hint after
  30 s, the paired phone with Rename and Revoke), Edit, Pause/Resume,
  Extend 30 days, Revoke all, Delete, devices (last seen, address,
  browser, Rename, Revoke) and approval requests (Approve, Deny);
  "Share libraries…" and "Share album…"; `managedElsewhere` disables
  every control. The album page has "Share…", or "Shared (n)" linking to
  `#/share?album=<lib>:<aid>`, which marks that album's shares. The top
  bar shows "Sharing · N shares · M phones" (amber when paused, red when
  failed) on every page. The Share page polls every 2 s, the pill every
  10 s while on and 30 s while off; a 404 from `GET /api/share` (an
  older server) hides it all.
- **Phone app** (`web/share.html`, `web/src/share/`): `main.ts` (imports
  `polyfills.ts` first, sets `--app-h` from `innerHeight` instead of
  `dvh`), `ShareApp.svelte` (shell, top bar, menu, filter sheet, the
  replacing screens), `Pair.svelte`, `ShareLibraries.svelte`,
  `ShareAlbums.svelte`, `ShareGrid.svelte`, `ShareLightbox.svelte`,
  `routes.ts`, `gestures.ts`, `session.svelte.ts`, `shareapi.ts`,
  `share.css`. Routes as in "Routes" above. Reused from the admin app:
  `Tile` (with a `viewer` prop, no check box without `onselect`),
  `Scrubber`, `RatingStars` (44 px targets under a coarse pointer), the
  hash router, `timeline.ts`, `rendition.ts` (`renditionFor(…, cap)`,
  `shareGridCap`) and `format.ts`.
- **Build:** Vite builds `index.html` and `share.html` into `web/dist`
  with `target: ['es2020', 'firefox91', 'safari15', 'chrome100']` for
  both. `npm run build` runs `scripts/check-share-apis.mjs`, which
  follows `dist/share.html` into every chunk it loads and fails on the
  runtime APIs and CSS in `scripts/share-compat.mjs` (`Object.hasOwn`,
  `structuredClone`, `findLast`, `toSorted`/`toReversed`/`toSpliced`,
  `with`, `fetch` `keepalive`, `<dialog>`, `randomUUID`,
  `AbortSignal.timeout`, …; `dvh`/`svh`/`lvh`, `:has()`, container
  queries, nesting, `color-mix()`, `@layer`) unless `polyfills.ts`
  lists them in `POLYFILLED`, and on admin API URLs (`/api/jobs`,
  `/api/fs`, `/api/share`, `/api/app`) in a share chunk.
  `touch-action` is reported as tolerated: every use has a touch-event
  fallback. Today the share bundle needs no polyfill. At the time of
  writing the phone loads `share.html` plus three files: the share
  chunk (about 49 KiB, 18 KiB gzipped), the common chunk with the Svelte
  runtime and the shared components (about 63 KiB, 24 KiB gzipped) and
  two stylesheets (about 18 KiB together); the admin entry is about
  318 KiB plus Leaflet.

### Shapes relied on

Phone app (share listener):

- `GET /api/session` → 200 `{device: {id, name}, computer, share}`;
  `computer` is a string (an object with `name` is also read). 401 →
  the "Not paired" screen (or "This phone was removed…" when the page
  had a session). 403 `{state}` → the state's screen; `state` is one of
  `paused`, `expired`, `gone`, `unavailable` (anything else gets a
  generic "not available" screen).
- `shareJSON` (`share` above): `{name, kind, capabilities: {video,
  download, rate, location}, expiresAt}`, plus `libraries: [{id, name,
  cover, total}]` (library share) or `album: {name, count, cover}`
  (album share). `kind` missing is inferred from `album`. A capability
  is granted only by an explicit `true`. `cover` is an item hash (16 hex
  digits; anything else is ignored), shown as
  `/api/l/{lib}/thumb/{cover}`; `total` falls back to `count`.
- `POST /api/pair` with `X-Picmover: 1` and JSON `{token, name}` or
  `{code, name}`, plus `replace: true` on the second try → 200 `{device,
  share}` (read like the session) and the cookie. Errors: 409
  `{error, current: {share}}` → the replace question; 410 `{error}` →
  the server's message, and the token form falls back to the code; 403
  → "The computer did not approve this phone" (also falls back to the
  code); 429 → "Too many tries"; any other `{error}` is shown as is. A
  request still open after 1.5 s shows "Waiting for the computer…" (the
  approval long poll). The code is sent upper-case without dashes or
  spaces ("K7QM3XRD").
- `GET /api/libraries` → `{libraries: [shareLib]}` (a bare array is also
  read). Never requested in an album share.
- `GET /api/l/{lib}/timeline?…` → `{buckets: [{year, month, count}]}`.
- `GET /api/l/{lib}/media?…&year=Y&month=M` → `{items: [item]}`, item
  as the admin list item: `id`, `hash`, `type`, `takenAt`, `w`, `h`,
  `dur`, `thumb`, `thumbAt`, `rating`, `gps`. Items without a positive
  `id` or a hash are dropped; `rating` is used only with `rate`, `gps`
  only with `location`.
- `GET /api/l/{lib}/media/{id}` → the redacted detail: `id, hash, type,
  takenAt, offset, w, h, dur, size, ext, fileName, cameraMake,
  cameraModel, lens, displayable, thumbAt`, plus `lat`/`lon` (shown only
  with `location`), `rating` (only with `rate`) and `albums: [{id,
  name}]` (library share; linked to `#/l/{lib}/albums/{id}`).
  `displayable` counts only with `download`. `fileName` is cut to its
  base name. 404 → "This photo is no longer in the library" ("… in this
  album" in an album share) and the grid reloads.
- `GET /api/l/{lib}/thumb|preview|large/{hash}?v={thumbAt}`; the grid
  asks for `thumb` (`large` only at 1–2 columns), the lightbox for
  `preview`. `GET /api/l/{lib}/file/{id}` for the player and the 1:1
  original, `?download=1` for Download.
- `GET /api/l/{lib}/albums` → `{albums: [{id, name, count, cover}]}`.
  Never requested in an album share.
- `POST /api/l/{lib}/ratings {hashes: [hash], rating}` → `{updated}`.
  409 → "Ratings are paused while photos are being moved on the
  computer"; 404 → the "no longer in" text; 403 without `state` →
  "Ratings are no longer allowed in this share" and the session is read
  again (the stars go once `rate` is off).
- 503 on a list → "This library is not available right now (the
  computer is busy with it). Try again later." with Try again.
- Query parameters sent: `type` (`image` or `video`), `from`, `to`
  (YYYY-01-01, `to` exclusive), `rating` (only with `rate`), `album`
  (library share only), `year`, `month`. `recur` and `months` are never
  sent. The `{lib}` key is a library id of the session's `libraries`,
  or `album` in an album share.
- Every 401, and every 403 with a `state`, from any request replaces the
  page with the matching screen.

Admin UI (admin listener):

- `GET /api/share` → `{state, reason, url, mdnsUrl, mdns?, port,
  address, addresses: [{ip, iface, subnet}], fingerprint: {leaf, ca},
  keepOn, autoOff: {mode, until} (a bare mode string is also read),
  managedElsewhere: {pid, cmd} | null, firewallHint, shares: [share]}`.
  `firewallHint` may be a string, a list of lines or an object of
  `{label: command}`. `mdns` missing is inferred from `mdnsUrl`. 404 →
  sharing is not offered.
- `POST /api/share {enabled?, address?, port?, keepOn?, autoOff?,
  mdns?}` and `POST /api/share/cert/renew {}` → the GET body.
- Admin `share`: `{id, name, kind, scope: {libraries: [{id, name,
  missing?}]} | {library: {id, name, missing?}, album: {id, name, count,
  missing?}}, capabilities, expiresAt, state, approve, createdAt,
  devices: [{id, name, pairedAt, lastSeen, lastAddress, browser}]}`.
  The scope's parts are also read from the top level; library entries
  may be plain id strings; an answer wrapped as `{share: …}` is
  unwrapped. Missing capabilities take the kind's defaults (admin side
  only).
- `POST /api/share/shares {name, scope: {kind: "libraries", libraries}
  | {kind: "album", library, album}, capabilities, approve, expiresIn
  (seconds) | expiresAt (RFC 3339 or null)}` → share.
- `POST /api/share/shares/{sid} {name?, libraries?, capabilities?,
  expiresAt?, paused?, approve?}` → share (Extend sends `expiresAt` 30
  days ahead; Pause/Resume send `paused`).
- `POST …/{sid}/delete {}`, `…/{sid}/revoke-all {}` (→ share),
  `…/{sid}/pairings {}` → `{url, code, qrSvg, expiresAt}` (409 → the
  dialog shows the server's message), `…/{sid}/pairings/cancel {}` when
  the dialog closes before a phone paired.
- `GET …/{sid}/requests` → `[{id, name, browser, address, at}]` (or
  `{requests: […]}`; numeric ids are read too), polled only for shares
  with `approve` while the Share page or the pairing dialog is open;
  `POST …/{sid}/requests/{rid} {approve}` (404 or 410 means the phone
  gave up, and the request is dropped).
- `POST /api/share/devices/{did} {name}` → device (`{device: …}` is
  unwrapped); `POST /api/share/devices/{did}/revoke {}`.
- Share, device and request ids must match `[A-Za-z0-9_-]{1,80}` to be
  put in a URL.

### Deviations

- `Grid` and `Lightbox` are not reused in a `mode="share"`: the admin
  ones import the selection, jobs, moves, tags, albums, toasts and view
  memory, which would all land in the phone's bundle. `ShareGrid` and
  `ShareLightbox` are small read-only components built on the reused
  `Tile`, `Scrubber`, `RatingStars` and layout code.
- The lightbox route keeps the grid it was opened from in its path
  (`#/l/<id>/albums/<aid>/view/<mid>` besides `#/l/<id>/view/<mid>` and
  `#/view/<mid>`), so a reload reopens it over the same grid. Opening
  from a tile pushes a history entry, so the phone's Back closes it.
- The filter sheet's "Photos" is `type=image` (RAW files are under
  All); the year range is two year fields.
- Swipes count only when they start 32 px or more from the left and
  right edges (the document suggested 24 px; Phase 0 showed the system
  takes edge swipes on Android and Sailfish). Without `touch-action` on
  Gecko 91 the lightbox's touch listeners are non-passive and cancel
  panning while one finger drags; two fingers are left to the browser.
  A swipe on a video that starts in its bottom 72 px is left to the
  player's controls.
- Svelte's `inert` and CSS `touch-action` are used but not relied on
  (Gecko 91 ignores both).
- The share chunk check is `scripts/check-share-apis.mjs` with the rules
  in `scripts/share-compat.mjs` (shared with `src/share/compat.test.ts`);
  it also refuses admin API URLs in share chunks.

### Left to do

- Serving `share.html` and `/assets/*` on the share listener (backend).
- The opt-in certificate install (Q3) and the `?t=` media URL (Q5), if
  Phase 0 calls for them.
- Approval requests show only while the Share page or a pairing dialog
  is open; there is no prompt elsewhere in the admin UI.
- Pinch to change the grid's columns; the tray item and `keepOn` wiring
  in the desktop app; `make e2e-share`; the manual device checklist (iOS
  is untested).
