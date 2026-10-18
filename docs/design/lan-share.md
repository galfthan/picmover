# Sharing on the home Wi-Fi (phone viewing)

Status: implemented (backend and frontend merged and reconciled), with
the Phase 0 device results recorded ("Phase 0 results"). "As
implemented" at the end holds the authoritative API listing, what
differs from the design above and what is not built yet; "Review
fixes" lists what three reviews found and how it was fixed. Anything still marked
**unverified** or **pending** is a browser or OS behaviour that no device
has confirmed yet.

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
| Filters, map | Phones filter about as richly as the admin grid, read only and inside the share's scope: type, dates (range and every year), rating, camera, location, album, tags (library shares only) and size ("Filters on the phone"; changed from the first version, see Q7 in "Settled in review"). A map mode with the `location` capability ("Map on the phone"; requested by the user after the first version). |

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
  - The Scrubber uses pointer events with `touch-action: none` (later:
    touch events with non-passive listeners on the phone, see "Timeline
    scrolling").
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
| GET | `/api/l/{lib}/facets` | the filter sheet's choices, computed inside the scope ("Filters on the phone") | ✓ (with the library's tags and albums) | ✓ (the album's live members only; no tags, no albums) |
| GET | `/api/l/{lib}/media/{id}` | redacted detail | visible rows | visible rows |
| GET | `/api/l/{lib}/thumb/{hash}` | as admin | visible rows | visible rows |
| GET | `/api/l/{lib}/preview/{hash}` | as admin | visible rows | visible rows |
| GET | `/api/l/{lib}/large/{hash}` | as admin | visible rows | visible rows |
| GET | `/api/l/{lib}/file/{id}` | as admin, Range, `?download=1`; `Cache-Control: private, no-store` | visible rows, per `video`/`download` | same |
| GET | `/api/l/{lib}/albums` | `{albums: [{id, name, count, cover}]}` | ✓ | 404 |
| GET | `/api/l/{lib}/geo` | map clusters inside the scope ("Map on the phone") | needs `location` | needs `location` (the album's live members) |
| GET | `/api/l/{lib}/geo/items` | the photos of a map box, newest first | needs `location` | needs `location` |
| POST | `/api/l/{lib}/ratings` | `{hashes: [h], rating: 0..5}` → `{updated}` | needs `rate`; one visible hash; `notMoving` → 409 | same |
| GET | `/`, `/share.html`, `/assets/*`, `/favicon.png`, `/icons/*`, `/manifest.webmanifest` | the share SPA | no auth | no auth |

The route table is the same for every scope kind. A "404" in the table
above is the scope's answer, not a missing route. The scope interface
(next section) decides it.

`shareJSON` is `{name, kind, capabilities, expiresAt}`, plus
`libraries: [shareLib]` for a library share, or `album: {name, count,
cover}` for an album share. `shareLib` is `{id, name, cover, total}`:
no path, no state.

Redacted detail: `{id, hash, type, takenAt, localTakenAt, offset,
offsetSource, homeOffset, w, h, dur, size, ext, fileName, cameraMake,
cameraModel, lens, displayable, thumbAt}` (`takenAt` is the home time,
docs/design/home-time.md).

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

Filters accepted on `timeline` and `media` (a whitelist; anything else
is 400): `type`, `from`, `to`, `recur`, `months`, `camera`, `minpx`,
`maxpx`. In addition:

- `rating` needs `rate`, and `gps` needs `location` (400 without);
- `album` and `tag` exist only in a library share; an `album` must be an
  album of `{lib}` (404 otherwise), a `tag` that is not one of `{lib}`'s
  simply matches nothing;
- an album share refuses `album` (the scope sets it) and `tag` (tags are
  the owner's labels on the whole library; see Q7).

Always refused: `trashed`, `import`, `session`, and unknown keys.
`facets` takes no parameters at all.

There are **no** routes for `/api/fs`, `/api/jobs*`, SSE, `/api/app*`,
library open or create, tags, albums (write), imports, trash, transfer,
similar, backups, cover, or `/api/share/*` (`geo` and `geo/items` came
later with the phone's map, behind `location`). All of those are 404 on
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
   `albums`) need `auth.wholeLibrary()`, else 404; so do the `album`
   and `tag` filters (400) and the tags and albums of `facets` (left
   out).
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
    base-uri 'none'; form-action 'none'`, plus exactly one outside
    host in `img-src`, `https://tile.openstreetmap.org`, for the map's
    tiles ("Map on the phone").
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

  *Changed in review (S1, R3, R4; see "Review fixes"):* only the user's
  hand (the Share page, the tray, `serve --share`) records the home
  network; "Keep sharing on" at launch listens only on the saved one. No
  LAN address at all is transient: sharing waits (paused, `waiting:
  true`) and resumes by itself when the home network is back. Only
  another network pauses until the user resumes.

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
   Phase 0: Android Chrome keeps the fragment through its "Proceed"
   interstitial (scanned QR), and the Sailfish Browser keeps it on a
   typed fragment URL. iOS is untested. When a browser loses it, the page
   has no token and shows the code form; the user types the 8 characters.
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
   - For a code: ~~a wrong code adds one attempt to every open pairing;
     after 5, all open pairings are cancelled~~ *(changed in review, S2)*
     wrong codes count per remote address: after 5 that address is
     refused pairing for 10 minutes, the open pairings stay, and the
     Share page shows a notice naming the address. With 40 bits, 5 tries
     per address per 10 minutes and a global budget, guessing is
     hopeless, and nobody can cancel someone else's pairing.
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
  `Path=/` and no `Domain`, so it is bound to exactly this host.
  Gecko, WebKit and Blink all implement cookie prefixes. *Correction
  (review):* cookies are not bound to the port; see "Threat model".
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

### Browser behaviour

Android Chrome and the Sailfish Browser were measured in Phase 0 (see
"Phase 0 results"). iOS was not tested: the user has no Apple device,
so iOS support is best effort, built on standards and untested. The iOS
column is what the design expects, not what was seen.

| | iOS Safari (iOS 17/18), **untested** | Android Chrome (current) | Sailfish Browser (Sailfish OS 5.1) |
|---|---|---|---|
| Engine | WebKit | Blink | Gecko ESR 91 (stock browser, embedlite; the UA reports Firefox). Jolla targets ESR 115 for 5.2, not 5.1. |
| First visit to a self-signed site | "This Connection Is Not Private" → Show Details → "visit this website" → confirm | "Your connection is not private" → Advanced → "Proceed to … (unsafe)" | A security warning page with a way to proceed (passed in Phase 0) |
| How long the exception lasts | Believed to last until Safari's website data is cleared | Remembered for about a week, then the warning returns (**pending**: the after-days check has not been run) | **Pending** (the after-days check has not been run) |
| Address bar | "Not Secure" | Red "Not secure" | Not recorded |
| `Secure` and `__Host-` cookies with an accepted exception | Expected to work (the scheme is https) | **Pass**: kept across fetch, navigation, reload and a browser restart | **Pass**: kept across fetch, navigation and reload |
| Cookie on `<img>`, `<video>` (Range) and downloads | Expected to work; the media process is the known risk | **Pass** | **Pass**, the download is saved |
| HTTP cache for responses over an excepted certificate | Unknown | **Not used**: every fetch is a full response | **Used** |
| `<video>` over an excepted certificate | **Known risk:** media loads go through a separate media process that may not honour Safari's exception, so playback can fail while images work | H.264, HEVC (MP4 and MOV), VP9 and AV1 (WebM and MP4) play | H.264, HEVC (MP4 and MOV) and VP9 play; **AV1 fails** (no decoder) |
| Svelte 5 share bundle | Expected to work (Safari 15+) | Mounts | Mounts (see "Sailfish Browser specifics" for missing APIs) |
| Service worker / PWA install | Not on a certificate-error origin | Not allowed on a certificate-error origin | Not relevant (embedlite) |
| `.local` names (mDNS) | Resolves natively | **Pass** (`<host>.local` resolved and loaded) | **Fails**: does not resolve; the IP had to be typed |
| Edge swipes | Unknown | Lost to the system gestures; the middle works | Middle 5/5, edges 0/5 (system gestures) |

Consequences built into the design:

- **The share URL uses the IP address.** The Sailfish Browser cannot
  resolve `.local`, so QR codes, printed URLs and the Share page lead
  with `https://<ip>:<port>/`. The `.local` URL is only an extra (Q9),
  shown when `mdns` is on. The Share page and the README recommend a
  DHCP reservation for the computer, since the cookie is bound to the
  address.
- **No AV1 assumptions.** Nothing picks AV1 or relies on it playing; a
  file the browser cannot play gets the Download fallback.
- **Caching under a certificate exception is browser-dependent.**
  Chrome refetches everything, Gecko caches. Thumbnails stay small
  (`thumb` in the grid), and renditions still carry
  `private, max-age` with a versioned ETag so the browsers that do cache
  benefit.
- **Lightbox swipes start away from the screen edges** on both measured
  phones; the edges belong to the system.

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
and with `--share-port` (which applies to that run only and is never
saved; changed in review, R16). Phones bookmark the URL, so a busy port
is **not** replaced by a random one:
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
  - In a library share: a Photos/Albums switch, a **Filter** button
    with the number of active filters (below), and a menu (Libraries,
    "Paired as Anna's iPhone", Help).
  - In an album share: no library or album navigation at all. The album
    name and photo count sit under the title, there is the same Filter
    button (filtering within the album), and the menu has "Paired as …",
    the expiry ("Available until 13 Nov") and Help.
- **Grid:** the timeline with month headers and the scrubber (see
  "Timeline scrolling" below).
  - Column count by width: 3 in portrait under 500 CSS px, 4–5 in
    landscape or on tablets. Pinch-to-change-columns is later.
  - The scrubber: a 48 px thumb in a 40 px gutter at the right, drawn
    8 px off the screen's edge, the years marked along the track, and a
    large month bubble ("March 2019") while dragging. A press anywhere on
    the track (on a year label too) jumps there and drags; grabbing the
    thumb keeps the view where it is.
  - A tap on a month's header ("March 2019 ▾") opens **Jump to a date**:
    a bottom sheet with the years (and their counts), then the months of
    the year picked (months without photos disabled).
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
    The video sits between the top and bottom bars, never under them,
    so the player's own controls (seek bar, time) stay reachable and a
    tap on them cannot land on a star; a tap on the video is the
    player's and does not toggle the bars.
    Without `video`, video tiles show the preview with a "Video not
    available in this share" note. Without `video` and `download`, a
    video can still be seen as its poster.
  - A video or original that fails to load is asked what it answers
    (one byte, a Range request): 403 says the capability was turned off
    ("Videos are no longer allowed in this share") and re-reads the
    session, no answer is "Cannot reach the computer", 429/5xx "The
    computer is busy" with Try again; only a URL that loads fine is "This
    video cannot be played in this browser". A failed original falls
    back to the preview.
- **Hidden** (absent from the share bundle, not merely hidden): Import,
  Imports, Library stats, the admin Map page (the phone has its own
  map, "Map on the phone"), Trash, Similar, Backups, Libraries
  management, workspace setup, the selection bar, tag editing, the album editor,
  move, the job pill, toasts about jobs, and the folder picker.
- **Rating feedback:** optimistic, rolled back on error.
  - A 409 (a move runs on the library) shows "Ratings are paused while
    photos are being moved on the computer".
  - A 404 shows "This photo is no longer in the library" (in an album
    share, "no longer in this album").
  - A 403 (the capability was turned off meanwhile) re-reads the session
    and hides the stars.
- **Capabilities changed on the computer** take effect without a reload:
  the session is read again when the page comes back into view, every
  3 minutes while it is shown, and after any refusal; the grid reloads
  its items (keeping the scroll) and the lightbox its detail when the
  capabilities differ. Only a refusal (401, 403 `{state}`) replaces the
  page; a background read that fails keeps it.

### Filters on the phone

The phone filters about as richly as the admin grid, read only, within
its share's scope:

| Filter | Query | Library share | Album share |
|---|---|---|---|
| Type: All, Photos, Videos, RAW (RAW only when the scope has some) | `type` | ✓ | ✓ |
| Dates, range: year chips (tap one year, a second for a range, the same again to clear) and From/To month and year pickers | `from`, `to` | ✓ | ✓ |
| Dates, every year: a month grid, the admin's presets (Today, Christmas, New Year, Midsummer, Summer, This month) and a day range | `months`, `recur` | ✓ | ✓ |
| Rating at least 1–5 | `rating` | with `rate` | with `rate` |
| Camera: maker and model, or "Unknown camera" (photos without a model) | `camera` (a model), `nocamera` (`1`) | ✓ | ✓ (the album's cameras only) |
| Has location | `gps` | with `location` | with `location` |
| Album | `album` | ✓ (not on an album page, which is its album) | no |
| Tags (every picked tag must match) | `tag` (repeated) | ✓ | no |
| Size: Any, Small ≤ 1600, Full size ≥ 2000, Large ≥ 4000 | `minpx`, `maxpx` | ✓ | ✓ |

- **Discoverable:** the top bar has a visible **Filter** button (an
  icon and the word) with a badge counting the active filters. The
  active filters sit above the grid as chips, one per filter (one per
  tag), each with a × that removes just that filter; tapping a chip
  opens the sheet; "Clear all" shows with two or more. An empty result
  says "Nothing matches these filters" with Clear filters.
- **The sheet** is a bottom sheet (88 % of the height at most, its own
  scrolling, the header and the Clear all / Apply bar sticky) with one
  section per filter. It edits a draft; Apply puts it in the route.
  Long lists (album, the date pickers) are native `<select>`s,
  which every phone browser shows with its own picker (the Sailfish
  Browser included; `<input type="date">` is not relied on). Every
  target is at least 44 px; nothing needs hover; it fits 360 px.
- **Camera** comes right after Show, so it is on the sheet's first
  screen: "Any camera", the five most used cameras with their counts,
  "Unknown camera" when some photos have no model (videos often, scans,
  edited copies), and with more than five cameras a search box (every
  word must match the maker or the model) and "All n cameras" (a list
  scrolling on its own). A camera reads as maker and model ("Apple
  iPhone 12", "Samsung SM-G991B"), the model alone when it starts with
  the maker ("Canon EOS R6"), without company suffixes ("NIKON D750",
  not "NIKON CORPORATION NIKON D750"). The filter stays the exact model
  (`camera`); `nocamera=1` is the unknown camera, and the two never go
  together (the route keeps the camera).
- **The route:** filters live in the hash query (`#/l/peter/?type=video&tag=3`,
  `#/?months=6,7` in an album share), like the admin's, so Back, reload
  and a link keep them. The lightbox route keeps the query, and the
  lightbox turns pages within the filtered photos (its neighbours come
  from the filtered grid). `share/filters.ts` reads the query and drops
  whatever the share may not use (`rating` without `rate`, `gps`
  without `location`, `album` and `tag` in an album share), so a stale
  link never makes a request the server refuses.
- **Facets:** the choices (types, years, cameras, the photos without a
  camera and the location count with their numbers; a library share's
  tags and albums) come from
  `GET /api/l/{lib}/facets`, read each time the sheet opens, and when
  chips need tag or album names.

**Security.** Every facet list is computed inside the share's scope by
the same `restrict` as the timeline: an album share sees only the types,
years, cameras and (with `location`) the location count of the album's
live members, so the sheet never names a camera or a year that only
photos outside the album have. A library share sees its own library's
values, counted over live photos: tags and albums are listed only when
they have live photos (a tag on trashed photos only, or an empty album,
is left out). Facets are a fixed number of queries per request (three
`GROUP BY`s over the scope; in a library share one read of every tag and
album membership and one `LiveHashes` over their hashes), and the lists
are bounded (100 cameras, 300 tags, 300 albums; the most used are kept).

**Tags in album shares: none.** Tags are the owner's private labels
on the whole library, like ratings ("blurry", "ex", "tax receipts"), and
the detail never sends them. Showing the tags present on the album's
photos would hand each guest the owner's labels for those photos. The
album itself is the owner's selection, and type, dates, camera and size
are enough to find things in it. So an album share has no tag section,
`facets` sends no `tags`, and `tag` is 400. A `tags` capability (like
`rate` for ratings) is a possible later addition if wanted.

### Timeline scrolling

A family library can be 200,000 photos over 15 years. The phone grid
scrolls it like this:

- **The layout is exact before anything loads.** The timeline gives
  every month's count; a month is its 40 px header, `ceil(count /
  columns)` rows of the tile pitch and 8 px of padding
  (`layoutTimeline`). Loading a month never changes its height, so the
  view never moves when photos arrive, and any place can be jumped to
  at once.
- **The scrubber is proportional.** Its track maps linearly onto the
  whole scroll range (`scrubScale`): a point of the track is a fixed
  share of the photos, whatever the months' sizes, and dragging moves
  month by month (180 months on a 600 px track is about 3 px a month).
  Year labels are marks along the track, not targets.
- **Touch:** the thumb is 48 px tall (a 44 px target and more), 26 px
  wide and drawn 8 px off the screen's edge, in a 40 px gutter the grid
  leaves free; a press anywhere on the gutter jumps there and drags,
  and a press on the thumb keeps the finger's offset (no jump). Touch is
  handled with touch events and non-passive listeners that call
  `preventDefault` (the Sailfish Browser has no `touch-action`, and
  would pan the page and cancel a pointer); the mouse and pen use
  pointer events. Android's back gesture is a horizontal swipe, so a
  vertical drag that starts at the edge still reaches the page; Sailfish
  takes swipes that start at the edge itself, which is why the thumb
  sits inside.
- **Keyboard and screen readers:** the scrubber is a vertical
  `role="slider"` whose `aria-valuetext` is the month at the top
  ("March 2019"); Up/Down move a month, Page Up/Down a year, Home/End to
  the ends. The month headers are buttons that open Jump to a date.
- **Only what is near is drawn and loaded.** The grid renders the
  months intersecting the view plus one screen above and below, and of
  those only the visible rows (about 300 DOM nodes, whatever the
  library's size). Months are fetched after the view rests for 60 ms
  (150 ms while the scrubber is dragged), so months that only flash by
  are not requested; a request for a month the view has left is
  cancelled (`AbortController`). Past 12,000 loaded items the months
  farthest from the view are dropped again (not while the lightbox is
  open, which pages through them).
- **Flying past:** while the view moves faster than 6 px/ms (a hard
  fling, a scrubber drag) new tiles are drawn as grey placeholders, so
  no image is fetched or decoded for photos nobody can see; tiles
  already shown stay, and the rest become photos once the view slows
  down.
- **The place is kept** when the layout does change: a month's list is
  shorter or longer than its count (photos came or went), the timeline
  read again on return differs, or the phone turns (other columns). The
  first photo of the row at the top, and how far into that row, stays
  at the top (`anchorAt`/`anchorOffset`).

The admin grid uses the same `Scrubber` (compact: no thumb, the small
hover label) with the same mapping, the year labels as marks and the
same keys.

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

  Phase 0 confirmed that a Svelte 5 bundle built for this target mounts
  and updates on the Sailfish Browser. The feature check there found
  these missing: `Object.hasOwn`, `structuredClone`,
  `Array.prototype.findLast`, `Array.prototype.toSorted`, `dvh` units,
  `:has()`, container queries, CSS nesting, `color-mix()`,
  `touch-action` and fetch `keepalive`. The list above stands, plus
  `touch-action` (see Touch) and `keepalive` (not used by the share
  app).
- **Touch:** Gecko 91 supports Touch Events and Pointer Events, so the
  existing swipe code should work, but the Phase 0 feature check found
  **no `touch-action`** in the Sailfish Browser: the Scrubber and the
  lightbox must not depend on it to suppress scrolling (call
  `preventDefault` on the touch events as well). Sailfish's own
  edge-swipe gestures take every swipe that starts at a screen edge
  (Phase 0: middle 5/5, edges 0/5), so lightbox swipes must start at
  least 24 px from the left and right edges.
- **Rendering cost:** Sailfish devices are often older and slower
  phones. The share grid caps renditions at `thumb` (below), keeps
  overscan to one screen, and avoids blur filters (the lightbox
  blur-up uses a scaled thumbnail without `filter: blur` under
  `prefers-reduced-motion` or in share mode).

## Map on the phone

Added after the first version at the user's request (see "Settled in
review"). The phone gets a map mode beside its grid: the share's photos
with a position, clustered, over map tiles, filtered like the grid.

### When there is a map

- **Only with the `location` capability.** Positions are exactly what
  `location` grants (`lat`/`lon` in the detail, `gps` in items), so the
  map needs nothing new from the owner and is gone the moment `location`
  is turned off: no map button and no map route on the phone, and the
  server answers 403 `{"error": "not allowed in this share"}` on both
  map routes (a route that needs a capability, like `file` and
  `ratings`; the `gps` filter's 400 is the answer for a filter key). The
  phone treats that 403 like any capability refusal: it reads the
  session again, and the map route then sends it to the grid.
- **Library shares and album shares.** The map of a library, of an
  album page in a library share, and of an album share. It never shows
  more than the grid: an album share's map is the album's live members
  that have a position, and nothing else of the library.
- **Location is never asked of the phone.** The map shows where the
  photos are, not where the phone is: no "my location" button, no
  `navigator.geolocation`, and the share listener's
  `Permissions-Policy: geolocation=()` refuses it anyway.

### API (share listener)

Two routes, both behind `shareScoped` like every library route, so the
`{lib}` key, the scope, the library's identity and the per-device
budget apply unchanged:

- `GET /api/l/{lib}/geo?bbox=w,s,e,n&zoom=z[&filters]` → `{"clusters":
  [...]}`. The server reads the positions inside the box with the
  admin map's query (`catalog.GeoPoints`: the `media_latlon` index, or
  a parallel scan for a large box) and clusters them on the
  web-mercator grid of the zoom (the admin's clustering, `geoCells`,
  with phone-sized cells: 80 px, and at most 1024 cells per answer; a
  larger box gets coarser cells, never more). So a 200 000-photo
  library answers a world view with at most 1024 small objects, and a
  phone screen with about a hundred.
- Each cluster: `{lat, lon, count, hash, thumbAt?}` plus `id` when it is
  one photo, or `bounds: [w, s, e, n]` (its members' extent) when it is
  several. `hash` is the cluster's **representative**, its newest photo
  (by taken time, then id; undated counts as oldest), shown as the
  marker's thumbnail through the usual `thumb/{hash}` route (which
  checks the row against the scope).
- `GET /api/l/{lib}/geo/items?bbox=w,s,e,n[&limit=1..500][&filters]` →
  `{"items": [...], "total": n}`: the photos of a box, newest first, as
  the media list's items (the same redaction: `rating` only with `rate`,
  no `deletedAt`). The bottom sheet asks it for a cluster's `bounds`.
- **Filters:** the phone's whitelist (`shareFilter`): `type`, `from`,
  `to`, `recur`, `months`, `camera`, `minpx`, `maxpx`, `gps`; `rating`
  with `rate`; `album` and `tag` in library shares only. Plus `bbox`
  and `zoom` (`geo`) or `limit` (`geo/items`); anything else is 400.
  The scope's `restrict` narrows the query exactly as for the timeline,
  so the map can only ever contain rows the grid could list.
- **No new leak:** positions of photos in the scope only, with
  `location` only; no paths, file names, albums or tags; an album share
  learns nothing about photos outside the album (a box around them is
  empty, like an empty month).
- "Fit to all" needs no route of its own: `geo` at zoom 0 over the
  whole world returns a handful of clusters whose `bounds` (or
  positions, for single photos) together are the extent of every photo.

### Tiles, privacy and the CSP

- **Source:** the admin map's tiles, OpenStreetMap's standard layer,
  from the one host `https://tile.openstreetmap.org/{z}/{x}/{y}.png`
  (OSM asks clients to use that host now rather than the old `a.`/`b.`/
  `c.` subdomains; with HTTP/2 they gain nothing). The share listener's
  CSP gets exactly that origin in `img-src` and nothing else: no
  `connect-src`, no scripts, no styles from outside. A Go test checks
  the CSP, and the phone app has the URL in one constant.
- **What the phone reveals by loading tiles.** The phone fetches the
  tiles itself, so OpenStreetMap's servers see the phone's requests: the
  home's public IP address (the phone is on the home Wi-Fi), the phone's
  browser, and which tiles it asks for, which is roughly the area being
  looked at, and since the map shows where the photos are, roughly
  where the photos were taken. The tile request carries a referrer of
  the share's origin only (`https://192.168.1.20:7443/`, a LAN address;
  `strict-origin-when-cross-origin` on the tile images, as the admin map
  does, because OSM's tile policy wants one); never a path, a filter or
  a photo. The admin map on the computer reveals the same to the same
  server. It is also why the map is tied to `location`: an owner who
  does not want positions (or tile requests) on a share turns
  `location` off.
- **No per-share tile setting in this version.** A setting "Map tiles
  from tile.openstreetmap.org" (or a choice of hosts) would have to
  reach the CSP, the admin dialog and the CLI for one switch whose "off"
  is already available as `location` off. It is listed under "Open
  questions" (Q21) as a possible global switch.
- **Not proxied through picmover.** Proxying would hide nothing from OSM
  (the computer has the same public IP), would make the share listener
  an internet client that phones can drive (an outbound fetch per
  request, the tile policy's caching and identification duties, a new
  failure and abuse path on a listener that today never leaves the
  LAN), and would only help a phone without internet, which a phone on
  the home Wi-Fi rarely is.
- **Without tiles** (no internet, OSM refusing, a blocker): the map
  keeps working on a plain background. Markers, clusters, the sheet,
  the lightbox and "Fit to all" need only the share listener. The
  phone says "No map background: the map tiles need the internet" once
  a tile has failed and none has loaded since.

### Phone UI

- **Entry:** with `location`, the top bar of a grid (a library's
  photos, an album page, an album share) gets a **Map** button; on the
  map it is a **Grid** button. Without `location` neither the button
  nor the route exists.
- **Routes** (filters in the query as on the grid, and the map's
  position as `at=<lat>,<lon>&z=<zoom>`):
  - `#/l/<id>/map`, `#/l/<id>/albums/<aid>/map`, `#/map` (album share),
  - each with `/view/<mid>` for the lightbox over the map.

  The position is replaced (not pushed) while the map moves, so Back
  leaves the map in one step and a reload or a shared link opens the
  same view. Without `at` the map fits all the photos.
- **The map fills the screen** under the top bar (and the filter chips
  when some are on). Leaflet 1.9.4, the admin map's library, does pinch,
  drag, double-tap zoom and the +/− buttons.
- **Markers:** every cluster shows its representative's thumbnail
  (48 px), with the count as a badge when there are several.
  - Tap a **single photo** → the lightbox, turning pages within the
    photos in view (the first 200, newest first).
  - Tap a **cluster** → a bottom sheet with a strip of its photos'
    thumbnails ("12 photos here", up to 200, newest first), **Zoom in here**
    (to the cluster's bounds; not offered at the deepest zoom) and
    Close. Tap a thumbnail → the lightbox, turning pages within the
    sheet's photos.
  - The lightbox is the grid's (stars, Download, Info, video as the
    capabilities allow); closing it returns to the map with the sheet
    still open.
- **Filter** works on the map as on the grid: the same button, sheet
  and chips; applying keeps the map's position. With nothing to show:
  "No photos with a location" (with Clear filters when filters are on).
- **Fit to all**: a button on the map that zooms to every photo
  matching the filters.
- **Sailfish Browser (Gecko 91):** Leaflet 1.9 uses Touch Events where
  the browser has them (Gecko 91 does) and cancels the browser's own
  panning with non-passive listeners on the map, so it does not depend
  on `touch-action`, which the Sailfish Browser lacks (Leaflet's CSS
  sets it, which the build check already tolerates).
- **Bundle:** Leaflet and the map view load only when the map is
  opened (a dynamic import), so the grid, which every phone uses, does
  not grow. The build check follows the map's chunks and stylesheets
  too.

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
  a broken image. The share grid reads the timeline again when the tab
  becomes visible again (`visibilitychange`) and refetches the months
  only when it changed, and reloads on any 404 from the lightbox. A
  thumbnail that fails is retried with a bounded backoff (below, "Review
  fixes").
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
| Another HTTPS service on the computer's address (another port), or a page it serves | Receive the phone's device cookie, or overwrite it | **Accepted, documented (review).** Cookies are not isolated by port: a phone that visits `https://<the computer's IP>:<another port>/` sends `__Host-picmover` there, and that service can set a cookie of the same name. Anything else listening on the computer's LAN address is trusted like picmover itself; a stolen cookie is revoked on the Share page. A clobbered cookie only unpairs the phone. |
| A guest of a share with `download` on and `video` off | Get a video's bytes | **By design (review).** `download` covers every original, videos included: `GET …/file/{id}?download=1` of a video answers with its bytes as an attachment. `video` governs only inline playback (`file` without `?download=1`). A share that must not hand out videos needs both off. (The reverse also holds, as the capability warning says: with `video` on, a played video can be saved.) |
| An unpaired device holding connections or guessing codes | Exhaust the listener, cancel the owner's pairing, use up the pairing budget | *(review)* At most 12 connections per address and 256 in all, a connection that sends nothing is closed after 5 s; wrong codes block only their own address; the global pairing budget is taken only by well-formed codes. |

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

### Phase 0 results

Run on 2026-10-14 against the probe above, on two phones.

**Android Chrome** (one phone):

| Item | Result |
|---|---|
| 1 Pairing | **Pass**: the scanned QR's fragment survived the certificate warning, and the cookie was set |
| 2 Cookie | **Pass** across fetch, navigation, reload and closing and reopening the browser |
| 3 Cookie on media | **Pass** on `<img>`, `<video>` with Range, and a download |
| 4 Video | H.264, HEVC MP4, HEVC MOV, VP9, AV1 (WebM and MP4) all play |
| 5 Svelte 5 | Mounts; no missing features |
| 6 `.local` | **Pass**: `terra.local` resolved and loaded |
| 7 Exception after days | Pending |
| 8 Edge swipes | Lost to the system; the middle works |
| 9 HTTP cache | **Not used** under the certificate exception: every fetch was a full response |
| 10 CA install | Not tested |

**Sailfish Browser**, Sailfish OS 5.1 (the UA reports Firefox; Gecko
ESR 91 as the design assumed):

| Item | Result |
|---|---|
| 1 Pairing | **Pass** with the typed fragment URL. `.local` did not resolve, so the user typed the IP. |
| 2 Cookie | **Pass** across fetch, navigation and reload |
| 3 Cookie on media | **Pass** on `<img>`, `<video>` with Range, and a download (the file was saved) |
| 4 Video | H.264, HEVC MP4, HEVC MOV and VP9 play; **AV1 fails** (no decoder) |
| 5 Svelte 5 | Mounts. Missing: `Object.hasOwn`, `structuredClone`, `Array.prototype.findLast`, `Array.prototype.toSorted`, `dvh` units, `:has()`, container queries, CSS nesting, `color-mix()`, `touch-action`, fetch `keepalive` |
| 6 `.local` | **Fails**: does not resolve |
| 7 Exception after days | Pending |
| 8 Edge swipes | Middle 5/5, edges 0/5 (system gestures) |
| 9 HTTP cache | **Works** |
| 10 CA install | Not tested |

**Not tested:**

- **iOS.** The user has no Apple devices. iOS support is best effort:
  standards-based and untested. The iOS media-process risk (Q5) stays
  open; Download is the fallback.
- **CA install (item 10).** Assumed to work as designed. It stays
  opt-in (Q3) and is not offered until a phone has shown that it
  enforces the name constraints.
- **Certificate exception after days (item 7).** Pending a later visit.

What this changes:

- Share URLs and QR codes use the IP address; `.local` is offered only
  as an extra (Q9). Recommend a DHCP reservation.
- The cookie works on both phones for every request type, so the `?t=`
  media URL (Q5) is not needed there.
- No AV1 assumptions anywhere.
- HTTP caching under a certificate exception depends on the browser:
  keep thumbnails small and send sensible `Cache-Control` and ETags
  anyway.
- `touch-action` is missing on Sailfish; swipes must start away from
  the screen edges on both phones.

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
  cookies turn out to be the problem. Phase 0: cookies reach media and
  Range requests on Android Chrome and Sailfish; iOS is untested, so the
  `?t=` URL is not built.
- **Q7 and Q8, tags, camera filters, map:** first decided as "not on
  phones in this version". **Changed afterwards:** the user wants rich
  filtering on the phone, so phones filter by type, dates (range and
  every year), rating (`rate`), camera, location (`location`), album
  and tags (library shares) and size ("Filters on the phone"). Tags are
  offered in library shares only: an album share gets no tag filter and
  no tag list, because tags are the owner's private labels (like
  ratings) and showing the album photos' tags would hand them to every
  guest; the safe default, with a `tags` capability possible later.
  Every facet list is computed inside the share's scope. The map stayed
  off phones in that version; see "Map on the phone" below. GPS is a per-share capability (`location`), on for library
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
- **Map on the phone (requested by the user after the first
  version):** the phone gets a map mode beside the grid, only with the
  `location` capability, for library and album shares, within the
  share's scope and with the phone's filters; server-side clustering,
  OpenStreetMap tiles allowed by the CSP (one host), no tile proxy, a
  plain background when tiles fail. Designed in "Map on the phone".
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
   - Phase 0: Android Chrome resolves it, the Sailfish Browser does not
     (iOS is untested, likely yes).
   - *Recommendation:* the IP URL is the URL (QR codes, the printed
     line). The `.local` URL is an extra, shown only when `mdns` is on.
     Recommend a DHCP reservation.
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
    - Phase 0: the UA reports Firefox, and the missing APIs match
      ESR 91.
    - *Recommendation:* target ESR 91 (the build target and polyfills
      above), and drop the polyfills when 5.2 is the minimum.
14. **Q21. A switch for the map's tiles?** The phone's map loads
    OpenStreetMap tiles directly ("Map on the phone"). *Recommendation:*
    none until asked for; `location` off removes the map. If wanted, a
    global "Map background from OpenStreetMap" switch on the Share page
    that drops the host from the CSP and gives every phone the plain
    background.
15. **Q14. Albums across libraries.** Albums are per library today. If
    cross-library albums ever come, the album scope's key and `visible`
    must be revisited, so that membership never lets a photo of an
    unshared library through.

## As implemented

The backend (`feat/lan-share-backend`) and the frontend
(`feat/lan-share-frontend`) were built in parallel against this
document, merged into `feat/lan-share` and then reconciled against each
other and an end-to-end run. Everything above holds except where this
section says otherwise. The API listing below is the one authoritative
description of both listeners: field names, which fields can be absent,
and the status codes are exact (read from the handlers), and "Client:"
notes say what the web app does with an answer. The fakes in
`web/src/testing/fakes.ts` (`fakeServer().sharing` for the admin API,
`fakeShareServer(kind, opts)` for the share listener) answer the same
way.

### Code

Backend:

```
internal/share/config.go          share.json (Manager: Open, Hold/Release of share.lock, Managed, Snapshot, Update, Verify(Cookie), Seen, AddDevice, Sweep)
internal/share/extra.go           unknown share.json members kept on rewrite; alive_*.go: processAlive (Managed)
internal/share/shares.go          Share, Device, states, cookie format, CleanName, BrowserLabel
internal/share/scope.go           Scope (unknown kinds kept byte for byte), LibraryKey, AlbumKey, Capabilities and their defaults
internal/share/pairing.go         Pairings: Issue, Find, Use, Cancel, Ask/Wait/Decide (approval), notices
internal/share/ratelimit.go       Limiter (token buckets), Lockout
internal/share/tlscert.go         NewCA, NewLeaf, Certs (Ensure, TLSConfig, Fingerprints), WriteFileAtomic
internal/share/netif.go           Candidates, Choose, Compare and Locate (home network), FilterListener (ConnLimits); gateway_*.go,
                                  wireless_*.go per OS
internal/share/qr.go              QRSVG, QRTerminal (rsc.io/qr)
internal/server/share.go          sharing: the listener's lifecycle (Start, Stop, restart, watch, checkNetwork, auto-off)
internal/server/share_routes.go   shareRouteTable, shareMux, the share middleware, device auth, shareScoped, shareAcquire
internal/server/share_libs.go     libraries opened for phones closed again (closePhoneLibraries); cached user.db peeks
internal/server/share_scope.go    scopeAuth with librariesScope and albumScope
internal/server/share_api.go      the phone's handlers (session, libraries, library, timeline, media, detail, file, albums, ratings); the filter whitelist
internal/server/share_facets.go   GET /api/l/{lib}/facets
internal/server/share_pair.go     POST /api/pair, Server.shareState
internal/server/share_static.go   the phone app's files and the "not built" page
internal/server/share_admin.go    /api/share/* (admin listener)
internal/server/share_public.go   Running.StartSharing/StopSharing/SharingStatus/SharesHere/Pair/KeepSharingOn, ShareWorkspace
internal/catalog/manage.go        LiveByHash; facets.go: Facets (types, location, years, cameras, no camera of a filter)
internal/userdata/albums.go       AlbumHas; peek.go: PeekAlbums (PeekLibraryID now reads through the same open);
                                  tags.go: TagMembers, AlbumMembers (every membership in one query)
cmd/serve.go                      --share, --share-port, --pair
cmd/share.go                      picmover share list|create|set|revoke|delete
internal/shell/tray.go            "Share on Wi-Fi"; shell.go: Keep sharing on at launch
tools/shareprobe                  now uses internal/share for its certificates and QR codes
```

Frontend:

```
web/src/pages/Share.svelte              #/share: the switch, address, port, auto-off, keepOn (app only), .local name, fingerprints, firewall hint, share cards
web/src/components/ShareDialog.svelte   create/edit a share ("Share libraries…", "Share album…")
web/src/components/PairDialog.svelte    the QR (the server's qrSvg as an <img>), code, countdown, the paired phone with Rename/Revoke
web/src/components/ShareCapabilities.svelte, SharePill.svelte
web/src/lib/sharing.svelte.ts           the store: GET /api/share (Share page, 2 s), GET /api/share/status (pill), every change
web/src/lib/sharetext.ts                texts and expiry helpers; lib/api.ts: the admin types and lenient parsers
web/share.html, web/src/share/          the phone app: main.ts (polyfills.ts first, --app-h), ShareApp, Pair, ShareLibraries,
                                        ShareAlbums, ShareGrid, ShareJumpSheet, ShareLightbox, ShareFilterSheet, ShareFilterChips, routes.ts,
                                        filters.ts (the filters in the route, chips, date and camera helpers), gestures.ts,
                                        session.svelte.ts, shareapi.ts (the only module that knows share URLs), share.css
web/scripts/check-share-apis.mjs        run by npm run build: Gecko 91 API/CSS rules (share-compat.mjs) and no admin URLs in share chunks
```

The album page has "Share…", or "Shared (n)" linking to
`#/share?album=<lib>:<aid>`, which marks that album's shares (it reads
`GET /api/share` once when the page opens). Reused from the admin app on
the phone: `Tile` (a `viewer` prop, no check box without `onselect`),
`Scrubber`, `RatingStars` (44 px targets under a coarse pointer), the
hash router, `timeline.ts`, `rendition.ts` (`renditionFor(…, cap)`,
`shareGridCap`) and `format.ts`.

**Build:** Vite builds `index.html` and `share.html` into `web/dist`
with `target: ['es2020', 'firefox91', 'safari15', 'chrome100']` for
both. `npm run build` runs `scripts/check-share-apis.mjs`, which
follows `dist/share.html` into every chunk it loads and fails on
`Object.hasOwn`, `structuredClone`, `findLast`,
`toSorted`/`toReversed`/`toSpliced`, `with`, `fetch` `keepalive`,
`<dialog>`, `randomUUID`, `AbortSignal.timeout`, …; `dvh`/`svh`/`lvh`,
`:has()`, container queries, nesting, `color-mix()`, `@layer` (unless
`polyfills.ts` lists them in `POLYFILLED`), and on admin API URLs
(`/api/jobs`, `/api/fs`, `/api/share`, `/api/app`) in a share chunk.
`touch-action` is tolerated: every use has a touch-event fallback. The
share bundle needs no polyfill today. The phone loads `share.html` plus
four files: the share chunk (about 68 KiB, 24 KiB gzipped, with the
filter sheet), the common chunk with the Svelte runtime and the shared
components (about 71 KiB, 27 KiB gzipped) and two stylesheets (about
22 KiB together); the admin entry is about 322 KiB plus Leaflet.

### Phone API (share listener)

Every response carries `X-Content-Type-Options: nosniff`,
`Referrer-Policy: no-referrer`, `Cross-Origin-Resource-Policy:
same-origin`, `Cross-Origin-Opener-Policy: same-origin` and
`Permissions-Policy: camera=(), microphone=(), geolocation=()`; never
HSTS or CORS. JSON and errors are `Cache-Control: no-store`.

Refusals before routing: wrong Host **421** (plain text); an address
locked out after 30 suspect authentications a minute (a malformed
device cookie, or a wrong secret for a device that exists; *not* a
missing cookie or the cookie of a revoked or unknown device) **429**
`{"error"}` with `Retry-After` (the seconds left) for 5 minutes, on
every `/api/` path except `POST /api/pair` (the static files and
pairing stay reachable; changed in review, R1/S5); a foreign
`Origin` (anything but `https://<accepted host>`), `Sec-Fetch-Site:
cross-site` or `same-site`, or a POST without `Content-Type:
application/json` and `X-Picmover: 1` **403** `{"error"}`; a body over
4 KiB **413**. Anything not in the table (every admin path, an allowed
path with another method, an unknown file) is **404**
`{"error":"not found"}`, the same body as every scope refusal.

Device authentication (every `/api/` route but `POST /api/pair`):

- no, malformed, unknown or revoked cookie: **401**
  `{"error": "This phone is not paired with picmover. Show a pairing code on the computer.", "paired": false}`
- more than 30 requests a second (burst 100) from one device on the
  JSON routes, or 200 a second (burst 500) on `thumb`, `preview`,
  `large` and `file` (their own budget; review R2): **429** `{"error"}`
  with `Retry-After: 1`
- share not active: **403** `{"error", "state"}` with `state` one of
  `"paused"` ("This share is paused."), `"expired"` ("This share has
  ended."), `"gone"` ("This album is no longer shared."),
  `"unavailable"` ("This share is not available on the computer right
  now."; another workspace is served, or a scope kind this picmover does
  not know). `gone` is known once the album share's library is opened,
  so it comes from `session` and the library routes.
- a renewed cookie (older than 7 days) comes back as `Set-Cookie` on any
  authenticated response.

Client: every 401, and every 403 with a `state`, from any request
replaces the page: 401 is "Not paired" with the code form ("This phone
was removed" when the page had a session), a 403 state its screen with
Try again (an unknown state gets a generic "not available" screen). A
403 without `state` is a capability or middleware refusal and never
replaces the page.

`GET /api/session` → 200 (library share):

```json
{"device": {"id": "d_3f9a1c2e7b5d4a10", "name": "Anna's iPhone"},
 "computer": "terra",
 "share": {"name": "My phone", "kind": "libraries",
           "capabilities": {"video": true, "download": true, "rate": true, "location": true},
           "expiresAt": null,
           "libraries": [{"id": "peter", "name": "peter", "cover": "00000000c0ffee00", "total": 5123},
                         {"id": "penny", "name": "penny", "cover": null, "total": 0}]}}
```

Album share: no `libraries`; `album` instead, with the live member
count and a cover hash (load it as `/api/l/album/thumb/<cover>`);
`expiresAt` is RFC 3339 in the computer's zone:

```json
{"device": {"id": "d_…", "name": "Mia's phone"}, "computer": "terra",
 "share": {"name": "Summer guests", "kind": "album",
           "capabilities": {"video": true, "download": false, "rate": false, "location": false},
           "expiresAt": "2026-11-13T19:00:00+02:00",
           "album": {"name": "Summer 2026", "count": 48, "cover": "9f3a0c11d2e4b6a8"}}}
```

An album share's session answers **503** `{"error": "This library is not
available right now."}` when its library is busy, and **403** `gone` when
the album or library is not the one shared. A library share lists only
the libraries that are there with the shared `LibraryID` (a missing one
is left out, never marked).

Client: a capability is granted only by an explicit `true`; `cover` is
used only when it is 16 hex digits, as `/api/l/{lib}/thumb/{cover}`. A
503 session shows "Not available right now" with Try again; a failed
fetch shows "Cannot reach the computer". The `{lib}` key of every
library route is a library id of the session's `libraries`, or `album`
in an album share.

`POST /api/pair` `{"token": "<43 chars>"}` or `{"code": "K7QM-3XRD"}`
(case, dashes and spaces ignored), plus `"name"` and optional
`"replace": true`:

- **200** `{"device": {"id", "name"}, "share": <the session's share object>}`
  and `Set-Cookie: __Host-picmover=<id>.<secret>; Path=/; Secure; HttpOnly; SameSite=Strict; Max-Age=34560000`
- **400** `{"error"}`: malformed JSON, neither token nor code, or a
  token/code of the wrong shape
- **410** `{"error": "This pairing code has expired or was already used. Show a new one on the computer."}`:
  unknown, used, expired or cancelled; also after approval timed out
  (`"Not approved in time. …"`). When the pairing exists but its share
  is not active, **410** `{"error": "This share is paused.", "state": "paused"}`
  (`state` as above; also when the share is paused or ends while the
  request waits for approval).
- **410** `{"error": "Sharing was turned off on the computer. Try again when it is on.", "state": "off"}`:
  the approval wait ended because sharing was turned off, paused (network)
  or picmover quit (review R8; it used to be the 403 of a denial).
- **409** `{"error": "This phone is already paired with \"My phone\".", "current": {"share": "My phone", "sameShare": true}}`:
  the request carried a valid device cookie and no `replace`; the token
  is not used up. `current.sameShare` is `true` when that device belongs
  to the share being paired (absent otherwise), so the phone can say
  "already paired with this share" instead of offering to replace. With
  `replace: true` the old device is removed in the same save.
- **409** `{"error"}` (no `current`): the share already has 20 phones.
  The pairing is **not** used up (review R12): after a phone is revoked
  the same code works.
- **403** `{"error": "The computer did not allow this phone."}` (no `state`): denied on the computer.
- **429** `{"error"}` with `Retry-After`: 5 a minute per address
  (`Retry-After: 15`); an address that sent 5 wrong codes is refused
  for 10 minutes (`"Too many wrong codes from this phone; try again in
  10 minutes."`, `Retry-After` the seconds left); 60 well-formed codes a
  minute in total (malformed values and tokens do not count;
  `Retry-After: 15`). Wrong codes never cancel an open pairing (review
  S2).

With approval on, the request blocks up to 60 s.

Client: the token is taken out of the address bar before anything else;
the code is sent upper-case without dashes ("K7QM3XRD"). While typed,
the code field says at once when a character cannot be in a code
("Codes don't use 0, 1, I, L or O"; both halves of each look-alike pair
are outside the alphabet, so nothing can be mapped) and is set up for
codes (autocapitalize characters, autocorrect and spell check off). A
409 with `current` asks "Pair it with this share instead?" and resends
with `replace: true`; when `current.sameShare` is true (requested of the
backend in the review) it says "This phone is already paired with this
share" with Continue instead. A 409 without `current`, a 410 (with or
without `state`) and any other `{error}` show the server's message, and
the token form falls back to the code after a 410 or 403; a 410 with
`state: "off"` (sharing turned off while the computer was asked) says
"Sharing was turned off on the computer" with Start over. 403 is "The
computer did not approve this phone", 429 "Too many tries". A request
still open after 1.5 s shows "Waiting for the computer…".

`GET /api/libraries` (library share; **404** in an album share):
`{"libraries": [shareLib]}` as in the session. Client: never requested
in an album share.

`GET /api/l/{lib}/library` (library share):

```json
{"id": "peter", "name": "peter",
 "summary": {"total": 5123, "images": 4900, "raw": 100, "videos": 123,
             "earliest": "2003-05-01T10:00:00", "latest": "2026-10-01T18:22:10"}}
```

`GET /api/l/{lib}/timeline` and `GET /api/l/{lib}/media?year&month`:
the admin shapes (`{"buckets": [{year, month, count}]}`, `{"items":
[...]}`). Items keep `thumb` and `thumbAt`; `rating` is absent without
`rate` (and when 0, as on the admin side), `gps` absent without
`location`; `deletedAt` never appears. Query keys allowed: `type`
(`image`, `image_raw`, `video`), `from`, `to` (YYYY-MM-DD, `to`
exclusive), `recur` (`MM-DD` or `MM-DD..MM-DD`), `months` (`1,7,12`),
`rating` (1–5), `album`, `tag` (repeatable, up to 32, all must match),
`camera` (an exact model, at most 200 bytes), `nocamera` (`1`: the
photos without a camera model; **400** for another value, a repeat, or
together with `camera`), `gps` (`1`), `minpx`,
`maxpx` (the longer edge, 1–1000000) (and `year`, `month` on `media`
only); anything else is **400** `{"error": "invalid filter \"<key>\""}`,
and a malformed value **400** naming the key. `rating` without `rate`
and `gps` without `location` are **400** `{"error": "invalid filter
\"rating\": not allowed in this share"}` (`"gps"` likewise); `album` or
`tag` in an album share **400** `{"error": "invalid filter \"album\":
this share is one album"}` (`"tag"` likewise); an `album` that is not
an album of `{lib}` **404**.

Client: sends what the route query holds (`share/filters.ts`), in this
order: `type`, `from`, `to`, `recur`, `months`, `rating` (only with
`rate`), `album` (library share only), `camera`, `gps` (only with
`location`), `tag`… (library share only), `minpx`, `maxpx`, and `year`,
`month` on `media`. The sheet's From/To pickers set whole months
(`from` the 1st, `to` the 1st of the next month), the year chips whole
years. Items without a positive `id` or a hash are dropped. A 503 on a list shows "This library is not available
right now (the computer is busy with it). Try again later." with Try
again.

`GET /api/l/{lib}/facets` (no query parameters; any is **400**): the
filter sheet's choices, computed inside the scope by the same restrict
as the timeline. A library share:

```json
{"total": 5123, "types": {"image": 4900, "image_raw": 100, "video": 123},
 "years": [{"year": 2026, "count": 812}, {"year": 2025, "count": 1490}],
 "undated": 12,
 "cameras": [{"make": "Apple", "model": "iPhone 12", "count": 3011},
             {"make": "Canon", "model": "EOS R5", "count": 1800}],
 "nocamera": 312,
 "gps": 2950,
 "tags": [{"id": 3, "name": "beach", "count": 41}],
 "albums": [{"id": 2, "name": "Summer", "count": 48}]}
```

`types`, `years` (newest first, dated photos), `undated` and `cameras`
(most frequent first, at most 100, photos with a model) and `nocamera`
(photos without a model) count the scope's live photos; `gps` (photos with a position) is sent only with
`location`. `tags` and `albums` are sent only in a library share: the
library's tags and albums with live photos, with their live counts,
sorted by name (case-insensitively), at most 300 each (the most used
kept). An album share's answer, over the album's live members:

```json
{"total": 48, "types": {"image": 45, "image_raw": 0, "video": 3},
 "years": [{"year": 2026, "count": 48}], "undated": 0,
 "cameras": [{"make": "Apple", "model": "iPhone 12", "count": 40}],
 "nocamera": 3}
```

Client: read when the filter sheet opens (and when the route has tags or
an album, for the chips' names); `gps` is kept only with `location`,
malformed entries are dropped. A failed read leaves the sheet usable
with "Could not read the choices".

`GET /api/l/{lib}/media/{id}`: exactly these keys (`dur` and `thumbAt`
are always present, 0 when none; `takenAt`, `localTakenAt`, `offset`,
`offsetSource`, `homeOffset` may be null):

```json
{"id": 12, "hash": "9f3a0c11d2e4b6a8", "type": "image", "takenAt": "2024-07-14T13:05:22",
 "localTakenAt": "2024-07-14T13:05:22", "offset": 10800, "offsetSource": "exif", "homeOffset": 10800,
 "w": 4032, "h": 3024, "dur": 0, "size": 2345678, "ext": ".jpg", "fileName": "IMG_1234.jpg",
 "cameraMake": "Apple", "cameraModel": "iPhone 12", "lens": "", "displayable": true, "thumbAt": 1781092800,
 "lat": 60.1699, "lon": 24.9384, "rating": 4, "albums": [{"id": 3, "name": "Summer"}]}
```

`lat`/`lon` only with `location` (and a position), `rating` (0 when
unrated) only with `rate`, `albums` only in a library share.
`displayable` is false without `download`. `ext` is the catalog's
(".jpg"). Client: a 404 shows "This photo is no longer in the library"
("… in this album" in an album share) and the grid reloads; `albums`
link to `#/l/{lib}/albums/{id}`.

`GET /api/l/{lib}/thumb|preview|large/{hash}`: as the admin, after the
row check (the client adds `?v={thumbAt}`; the grid asks for `thumb`,
`large` only at 1–2 columns, the lightbox for `preview`).
`GET /api/l/{lib}/file/{id}[?download=1]`: inline video needs `video`,
an image or any `?download=1` needs `download` (**403** `{"error": "not
allowed in this share"}`), `Cache-Control: private, no-store`, Range →
206. Client: the player and the 1:1 original use `file`, Download adds
`?download=1`.

`GET /api/l/{lib}/albums` (library share): `{"albums": [{"id", "name",
"count", "cover"}]}` (count and cover over live members; cover null when
none). Client: never requested in an album share.

`POST /api/l/{lib}/ratings` `{"hashes": ["<one hash>"], "rating": 0..5}`
→ **200** `{"updated": 1}`. Checked in this order: not exactly one hash,
a malformed hash or a rating outside 0–5 **400**; not visible **404**;
without `rate` **403**; a move on the library **409** `{"error": "a
move is running on this library; try again when it has finished"}`.
Client: 409 → "Ratings are paused while photos are being moved on the
computer"; 404 → the "no longer in" text; 403 → "Ratings are no longer
allowed in this share" and the session is read again (the stars go once
`rate` is off).

Malformed `{id}` and `{hash}` path values are **404** here (the admin
answers 400): nothing is learned from the difference.

Static: `/` and `/share.html` serve `share.html` with the share CSP and
`Cache-Control: no-cache` (no validators, so a new build is picked up at
once); `/assets/*` (`public, max-age=31536000, immutable`), `/icons/*`,
`/favicon.png`, `/manifest.webmanifest` from the same `web/dist`; never
`index.html`. When the binary has no `share.html`, `/` is a 200 HTML
page "The phone app is not built into this picmover…".

### Admin API (admin listener)

All of `/api/share*` answers **403** `{"error"}` unless the admin
listener and the request's peer are loopback. A change while another
process shares is **409**
`{"error": "sharing is managed by picmover pid 4242 (picmover-app); use its Share page", "managedElsewhere": {"pid": 4242, "cmd": "picmover-app"}}`.
Client: a 404 from `GET /api/share` or `GET /api/share/status` (an
older server) hides sharing everywhere and stops polling;
`managedElsewhere` disables every control on the Share page.

`GET /api/share` (the Share page, polled every 2 s while it or a
pairing dialog is open):

```json
{"state": "on", "url": "https://192.168.1.20:7443/", "mdnsUrl": "https://terra.local:7443/",
 "port": 7443, "address": "192.168.1.20", "phones": 3,
 "addresses": [{"ip": "192.168.1.20", "iface": "wlp3s0", "subnet": "192.168.1.0/24", "wireless": true},
               {"ip": "172.17.0.1", "iface": "docker0", "subnet": "172.17.0.0/16", "virtual": true, "wireless": false}],
 "fingerprint": {"leaf": "AB:CD:…", "ca": "12:34:…"},
 "keepOn": false, "mdns": true,
 "autoOff": {"mode": "1h", "until": "2026-10-14T20:00:00+03:00"},
 "firewallHint": "If the phone cannot connect, allow the port in the firewall (and undo it when done):\nufw: …\nfirewalld: …",
 "shares": [adminShare]}
```

- `state`: `"off"`, `"on"`, `"paused"`, `"error"`. `paused` has a
  `reason`: `"network changed"` (another network: stays paused until
  the user resumes), `"waiting for the home network"` with
  **`"waiting": true`** (no LAN address: resumes by itself when the home
  network is back; review R3/R4), `"turn sharing on once on the Share
  page to record the home network"` ("Keep sharing on" at launch with no
  home network saved), or `"sharing could not listen on <ip>: …"` (also
  `waiting`, retried every 15 s). `waiting` is absent when false.
  `error` has a `reason` (e.g. "Port 7443 is in use
  by another program; choose another port on the Share page", "no Wi-Fi
  or LAN address found; …"). `reason`, `url`, `mdnsUrl` and
  `managedElsewhere` are absent when empty. `address` is the bound
  address while on or paused, else the one that would be chosen ("" when
  none). `fingerprint.leaf` and `.ca` are "" until sharing has been on
  once (the CA is created at the first start, not by this GET); the page
  then says the certificate is made when sharing is first turned on.
- `mdns` is the setting (an addition to the design).
- `addresses[].virtual` (docker, VPN) is absent when false; the address
  menu marks those entries "virtual". `addresses[].wireless` (review
  addition) is always present: `true` for a Wi-Fi interface (Linux:
  `/sys/class/net/<if>/wireless` or `phy80211`; Windows: the adapter
  name contains "Wi-Fi", "WLAN" or "wireless"; macOS: always `false`,
  the name does not tell). The client labels entries "Wi-Fi" or
  "wired" (absent counts as unknown); while a wired address is chosen
  and a non-virtual Wi-Fi one exists, it suggests that one with a
  button (it never switches a saved choice by itself). The page always
  says "Phones must be on the same network as this address."
- `phones` (review addition, R10) counts the paired phones of every
  share: what an address or port change affects (below).
- `firewallHint` is **one plain-text string** (lines separated by
  `\n`): the ufw and firewalld commands for the subnet and port on Linux,
  one paragraph on Windows and macOS. Shown wrapped in a `<pre>`.
- `autoOff.until` only while a 1h/4h timer runs. `"never"` and `"quit"`
  behave the same (no timer).

`GET /api/share/status` (an addition), for the top-bar pill; cheap, no
library is looked at:
`{"state": "on", "url": "https://…/", "shares": 2, "phones": 3, "autoOff": {"mode": "quit"}}`.
`shares` counts the shares that are not paused, expired, of another
workspace or of an unknown kind (an album share whose album is gone
still counts); `phones` their devices; `reason`, `waiting`, `url`,
`autoOff.until` and `managedElsewhere` as above. Client: the pill polls
it every 10 s while sharing is on or paused and every 30 s while it is
off, and right after any change made through the admin UI; it shows
"Sharing · N shares · M phones" (with "52 min left" under an auto-off
timer), amber "Sharing paused: <reason>", red "Sharing failed", or a
plain "Share" link. In a narrow top bar the pill shortens by whole
parts, never mid-word: the timer goes first, then the share count
("Sharing · 4 phones"), then the word ("Sharing · 4"), down to
"Sharing"; the tooltip has all of it. It asks for the width of its
longest text and shows the longest variant that fits the width it gets.

`adminShare`:

```json
{"id": "s_8c1f04a9d2e37b65", "name": "Summer guests", "kind": "album",
 "scope": {"library": {"id": "peter", "name": "peter"},
           "album": {"id": 12, "name": "Summer 2026", "count": 48}},
 "capabilities": {"video": true, "download": false, "rate": false, "location": false},
 "expiresAt": "2026-11-13T19:00:00+02:00", "state": "active", "approve": false,
 "createdAt": "2026-10-14T19:00:00+03:00",
 "devices": [{"id": "d_…", "name": "Mia's phone", "pairedAt": "…", "lastSeen": "…" , "lastAddress": "192.168.1.34",
              "browser": "Chrome on Android"}],
 "pairing": {"expiresAt": "2026-10-14T19:02:00+03:00"}, "requests": 0}
```

- A library share has `"scope": {"libraries": [{"id": "peter", "name": "peter"}, {"id": "penny", "name": "penny", "missing": true}]}`.
  Library names are the folder ids.
- `missing: true` (library not there or another `LibraryID`; for the
  album: deleted or its id reused) and `busy: true` on a library (locked
  by another process or rebuilding: phones get 503) are absent when
  false. Client: "Library busy" badge (also when the admin's own
  library list says busy or rebuilding).
- `album.count` is the member count from `user.db` (trashed members
  included; the phone's count is live members only).
- `state`: `active`, `paused`, `expired`, `gone` (album share with a
  missing library or album), `unavailable` (another workspace, or an
  unknown kind: `kind` is then the unknown value and `scope` is `{}`).
  Client: badges "Paused", "Expired", "Album deleted" / "Library
  missing", "Not in this workspace", and "Needs a newer picmover" for an
  unknown kind (whose scope reads "Unknown kind “x”; update picmover").
- `device.lastSeen` is null before the first request.
- `pairing` is the open pairing's expiry (null when none), `notice`
  (absent, or since review S2 "Too many wrong codes were typed from
  192.168.1.50; that address cannot pair for 10 minutes. The code shown
  still works.") is shown on the card, `requests` counts pending
  approval requests: the page fetches
  `GET …/requests` only for an active share with `approve` whose count is
  not 0.

`POST /api/share` `{enabled?, address?, port?, keepOn?, autoOff?, mdns?}`
→ the GET body. Turning on that fails for a busy port or no address is
**200** with `state: "error"` and the `reason`. `address` must be one of
`addresses[].ip` (it records that interface), `port` 1–65535 (the page's
field accepts 1024–65535), `autoOff` one of the four values (**400**
otherwise). A changed address or port listens again when on, **once**,
also together with `enabled: true` (review R10); a changed `mdns`
applies at once without listening again. A `port` replaces a `serve
--share-port` port of this run. `enabled: true` while paused resumes
and records the current network as home ("Resume on this network"); it
is the only way, with the tray and `serve --share`, that records it
(review S1).

**Address and port changes and paired phones** (review R10; the choice:
no confirmation round trip, a count before and a notice after):

- Before applying, the page can warn from `GET /api/share`'s `phones`:
  with `phones > 0`, a new `address` means every phone must pair again
  (the device cookie belongs to the host, i.e. the IP address; a phone
  that opened the `.local` URL keeps working when mDNS is on), and a new
  `port` means phones must open the new URL (bookmarks break) but
  normally stay paired (cookies are not bound to the port).
- The answer to a POST that changed them carries
  `"notice": "<text>"` (absent otherwise, and never in `GET`), one or
  both of: "The sharing address changed: phones paired at the old
  address must be paired again (their sign-in belongs to the address; a
  phone that opened the .local name keeps it)." and "The sharing port
  changed: phones must open the new address (bookmarks of the old one
  stop working). They normally stay paired; a phone that shows \"Not
  paired\" must be paired again." The same text is logged at Warn. It
  is sent only when `phones > 0`.

`POST /api/share/cert/renew` `{}` → the GET body.

`POST /api/share/shares`:

```json
{"name": "Summer guests", "scope": {"kind": "album", "library": "peter", "album": 12},
 "capabilities": {"video": true, "download": false, "rate": false, "location": false},
 "expiresIn": 2592000, "approve": false}
```

→ **201** adminShare. `capabilities`, when given, is taken whole (a
missing one is off; the client always sends all four). Expiry:
`expiresIn` (seconds > 0), or `expiresAt` (RFC 3339, or `null` for
never), or neither (album: 30 days; libraries: never). Client: the
relative choices send `expiresIn`, "Until a date" sends `expiresAt` at
the end of that local day, and "Never" sends `expiresAt: null` (leaving
it out would give an album share 30 days). **400**: unknown library or
album, empty list, bad kind, empty name; **409**: name taken
(case-insensitive), 50 shares, or no workspace served. A library that
has no `user.db` yet is opened once to create it.

`POST /api/share/shares/{sid}` `{name?, libraries?: [id], capabilities?, expiresAt?: time|null, paused?, approve?}`
→ adminShare. Any `kind`, `scope` or `album` key is **400**; `libraries`
on an album share or empty is **400**; unknown sid **404**; name taken
**409**. Pausing also cancels the share's pairings. Client: Extend sends
`expiresAt` 30 days ahead; Pause/Resume send `paused`.

`POST …/delete` → **204**; `…/revoke-all` → adminShare;
`…/pairings` → `{"url": "https://192.168.1.20:7443/#/pair/<token>", "mdnsUrl"?: "https://terra.local:7443/#/pair/<token>", "code": "K7QM-3XRD", "qrSvg": "<svg …>", "expiresAt": "…"}`
(the QR encodes `url`; **409** `{"error"}` when sharing is off ("sharing
on Wi-Fi is off; turn it on first"), paused ("sharing on Wi-Fi is paused
(<reason>); resume it first", review R9) or the share is not active,
shown in the dialog); `…/pairings/cancel` → **204**
(sent when the dialog closes before a phone paired); `GET …/requests` →
`[{"id": "r_…", "name", "browser", "address", "at"}]`;
`POST …/requests/{rid}` `{"approve": bool}` → **204** (**404** unknown:
the phone gave up, and the client drops the request);
`POST /api/share/devices/{did}` `{"name"}` → device JSON;
`…/revoke` → **204**. Share, device and request ids must match
`[A-Za-z0-9_-]{1,80}` before the client puts them in a URL.

### Deviations and decisions

Backend:

- **`scopeAuth` has a sixth method, `bind(ctx, l)`,** run right after
  the library is acquired: the `LibraryID` check (and, for an album, id
  plus `created_at`) lives in the scope, which decides whether a
  mismatch is a 404 (one library of a library share) or `gone` (the album
  share). Routes still never switch on the kind.
- **Unknown scope kinds** are `unavailable` to their phones (they never
  authorize anything) and to the Share page.
- **The auth-failure lockout** counts only suspect cookies (malformed,
  or a wrong secret for an existing device) on any route, and refuses
  only `/api/` routes other than `POST /api/pair` (changed in review,
  R1/S5: it used to count every 401 and refuse every path).
- **Workspace key.** `serve <library>` keys shares by the library root
  (links resolved) with the folder name as id (`singleID`, "library"
  for a drive root); `picmover share create --workspace <library>` does
  the same (`server.ShareWorkspace`).
- **Leaf SANs** are the bound address, every non-virtual candidate,
  127.0.0.1 and `<hostname>.local` (always, when the hostname is a valid
  label; the `mdns` setting only decides whether that Host is accepted
  and the URL shown).
- **Network watch:** every 15 s; leaf renewal near expiry happens there
  too; expiry is logged once a minute ("Share \"x\" has ended").
- **One sharing process:** the process that starts sharing holds
  `share.lock` until it stops (also while paused); a failed start
  releases it. The non-sharing process reads `share.json` per request
  and changes it under a brief lock.
- **CLI:** `--expires` takes `Nd`, `Nh`, `never` or `YYYY-MM-DD` (the
  share ends at the start of the next day, local time); `--album` names
  are matched case-insensitively; `--library` repeats or takes a comma
  list. `share create` refuses a library without `user.db` (never
  opened) instead of creating one. `share list --json` prints the shares
  without `secretSha256`.
- **`serve --share`** fails (exit 1) with "no shares for this folder; …"
  and when sharing cannot start; `--pair` and `--share-port` need
  `--share` (usage error), and `--share` needs the default loopback
  `--addr`. Lines printed:
  `Sharing on Wi-Fi at https://192.168.1.20:7443/; certificate SHA-256 AB:…`,
  then `  My phone: libraries peter, penny; 2 phones` per active share;
  pairings, revocations, pauses and expiry go to the log (stderr).
  Plain `serve` (without `--share`) can still turn sharing on from the
  Share page.
- **Tray:** "Share on Wi-Fi" is a checkbox, titled "Share on Wi-Fi
  (192.168.1.20)" while on and "Share on Wi-Fi (paused: <reason>)"
  while paused ("(paused)" for a long reason); it follows the Share page
  within 5 s. With no shares, or when sharing cannot start, it opens
  `#/share`. "Keep sharing on" (`keepOn`) is honoured at the app's
  launch only, through `Running.StartSharingKept` (review S1/R4).
- **`userdata.PeekLibraryID`** now opens read-only (not immutable) first,
  so it sees a running server's `-wal`; it falls back to the immutable
  open as before.

Frontend:

- `Grid` and `Lightbox` are not reused in a `mode="share"`: the admin
  ones import the selection, jobs, moves, tags, albums, toasts and view
  memory, which would all land in the phone's bundle. `ShareGrid` and
  `ShareLightbox` are small read-only components built on the reused
  `Tile`, `Scrubber`, `RatingStars` and layout code.
- The lightbox route keeps the grid it was opened from in its path
  (`#/l/<id>/albums/<aid>/view/<mid>` besides `#/l/<id>/view/<mid>` and
  `#/view/<mid>`), so a reload reopens it over the same grid. Opening
  from a tile pushes a history entry, so the phone's Back closes it.
- The filter sheet's dates are year chips and month/year pickers
  (native selects), plus the every-year months, presets and day range
  ("Filters on the phone"); the first version had two year fields.
- Swipes count only when they start 32 px or more from the left and
  right edges (the document suggested 24 px; Phase 0 showed the system
  takes edge swipes on Android and Sailfish). Without `touch-action` on
  Gecko 91 the lightbox's touch listeners are non-passive and cancel
  panning while one finger drags; two fingers are left to the browser.
  A swipe on a video that starts in its bottom 72 px is left to the
  player's controls.
- Svelte's `inert` and CSS `touch-action` are used but not relied on
  (Gecko 91 ignores both).
- The pill reads `GET /api/share/status`, not the full `GET /api/share`
  (which looks at every shared library), so pages other than the Share
  page never poll the full state.

### Reconciliation (after the merge)

Mismatches found between the two branches and how they were settled:

- The pill polled the full `GET /api/share` every 10 s on every page and
  counted every share and phone, paused and expired ones included; it
  now reads `GET /api/share/status` (active shares only).
- The client ignored `busy`, `pairing`, `notice`, `requests`,
  `addresses[].virtual` and the empty fingerprints; it now shows them
  (above), and fetches approval requests only when `requests` is not 0.
- A pairing 409 without `current` (20 phones) was taken for "already
  paired" and offered to replace; it now shows the server's message.
- An album share's 503 session showed "Cannot reach the computer"; it
  now says "Not available right now".
- An unknown scope kind read "Not in this workspace"; it now reads
  "Needs a newer picmover".
- The fakes were brought in line with the handlers: `/api/share/status`,
  `mdns`, the share additions, capabilities taken whole, port 1–65535,
  a failed start as 200 `state: "error"`, the 409 body of another
  sharing process, unknown kinds and other workspaces, library names as
  folder ids, 410 with `state`, the 409 of a full share, the 401 and 403
  bodies, the filter messages and per-route whitelists (`year`/`month`
  on `media` only), the ratings check order, and `dur`/`thumbAt` always
  in the detail.
- The end-to-end run found the lightboxes' Download link (`a.btn.ghost`)
  drawn as a white box with unreadable text in the light theme; `.btn.ghost`
  is now transparent like `button.ghost`.

No backend change was needed: every mismatch was on the client side or
in the fakes.

### Review fixes

Three adversarial reviews (security, robustness, frontend) of the
merged branch found the items below; the backend ones were fixed on
`fix/lan-share-backend`. Each has a regression test (`internal/server/
share_review_*_test.go`, `internal/share/review*_test.go` unless noted).

Security:

- **S1** "Keep sharing on" started on any network and recorded it as
  home. Now `sharing.StartKept` (`Running.StartSharingKept`, used by the
  app at launch) compares with the saved home network (`share.Locate`):
  home listens, another network pauses (`network changed`) until the
  user resumes, no home saved pauses with "turn sharing on once on the
  Share page…". Only `Start` (Share page, tray, `serve --share`) and a
  user's address/port change record home.
- **S2** Wrong codes cancelled every open pairing; malformed requests
  from four addresses used up the global 20/min. Wrong codes now count
  per address (`Pairings.Find(token, code, addr)`): 5 block that address
  from `POST /api/pair` for 10 min (`Blocked`), the pairings stay, the
  Share page gets a notice naming the address. The global budget (now
  60/min) is taken only by well-formed codes.
- **S3** 64 idle sockets from one address took every slot.
  `FilterListener` now takes `ConnLimits{Total: 256, PerIP: 12,
  FirstByte: 5s}`: a connection that sends no byte in 5 s is closed
  (read deadlines are clamped until the first byte; then the server's
  own handshake/header/idle timeouts apply).
- **S4** `/assets/..%2Fsecret.txt` served frontend root files. Static
  routes now name their file, or their folder plus `{file...}`; empty,
  `.`, `..` segments, backslashes and escaped separators are 404.
- **S5** (with R1) the lockout counted every 401 by address; see R1.
- Doc: the threat model now says that cookies ignore ports (another
  HTTPS service on the computer's address receives and can overwrite the
  device cookie) and that `download` hands out videos through
  `?download=1` even with `video` off.
- Reviewed with no change: `GET /api/l/{lib}/facets` and the filter
  whitelist. Facets go through the same `restrict` as the timeline
  (album shares count only the album's live members), `gps` needs
  `location`, `tags`/`albums` only where the scope is whole libraries
  and only with live photos, no query parameter is accepted, and the
  `album`, `tag`, `rating`, `gps` filters are refused per scope and
  capability before parsing.

Robustness:

- **R1** A revoked phone's stale thumbnails locked its address out,
  re-pairing included. Only suspect cookies count
  (`Manager.VerifyCookie`), and the lockout refuses only `/api/` routes
  other than `/api/pair`.
- **R2** The per-device 30/s (burst 100) starved a grid. `thumb`,
  `preview`, `large` and `file` have their own 200/s (burst 500).
- **R3** A pause never resumed. No LAN address is transient (paused,
  `waiting: true`): the watcher listens again when the home network is
  back; another network pauses for good.
- **R4** "Keep sharing on" before Wi-Fi was up ended in `error`. It now
  waits (as R3) with the watcher running.
- **R5** `Managed()` probed by taking `share.lock`, making the real
  holder's changes fail. It reads the holder record
  (`library.PeekHolder`) and checks the process is alive; `lock` waits
  up to 1 s for a brief holder (one change) instead of refusing.
- **R6** A new interface reissued the leaf. `Certs.EnsureFor(need, all,
  …)` keeps the leaf while it names the bound address and 127.0.0.1
  (and the name, and is not near expiry).
- **R7** A late auto-off timer stopped a new session. Timers carry a
  generation checked under `sh.mu`.
- **R8** Approval ended by stop/pause/quit was a 403 denial. Now 410
  `{error, state: "off"}`; a share paused or ended during the wait gives
  its own state.
- **R9** Issuing a pairing while paused said "off"; now it says paused
  with the reason.
- **R10** Port/address changes silently unpaired phones, and `enabled`
  plus a port listened twice. One listen; `mdns` no longer re-listens;
  `phones` in `GET /api/share` and a `notice` in the POST answer (see
  the API listing).
- **R11** A library a phone opened kept its lock for the server's
  lifetime. It is closed after 10 min without phone requests and when
  sharing turns off, unless the admin side used it (an admin open or
  library request clears the mark) or a request, job or move uses it.
  Creating a share of a never-opened library closes it right after its
  `LibraryID` is made.
- **R12** A full share used up the pairing; it now refuses first.
- **R13** The 5 s drain ran under `sh.mu`. The listener closes under the
  lock (the port is free at once); `Shutdown` waits after unlocking.
- **R14** `share.json`: unknown members of the settings, shares, scopes
  and devices are kept on rewrite (`extra.go`); damaged files (also
  `{}` or no version) are set aside only under the lock, as
  `share.json.damaged-<UTC>`, the newest 5 kept; the sharing process
  sweeps expired shares and idle devices once a minute.
- **R15** `PeekLibraryID`/`PeekAlbums` of closed libraries are cached by
  the user.db and -wal stamps (`workspace.peekUser`); the tray's status
  looks at each share once.
- **R16** `serve --share-port N` is the port of that run, never saved;
  a port set on the Share page replaces it.

Additions for the frontend (exact JSON in the API listing):
`addresses[].wireless` (always present), `waiting` and `phones` in
`GET /api/share` (`waiting` also in `/api/share/status`), `notice` in
the answer of `POST /api/share`, 410 `{error, state: "off"}` for an
approval wait ended by sharing going off, `current.sameShare` in the
409 "already paired", and `Retry-After` (seconds) on every 429 of the
share listener. Deviation from the review's suggestions: the per-IP
connection cap is 12, not 8 (an HTTP/1.1 browser opens up to six
connections per host, plus preconnects and a second tab); the address
and port warning is a count before and a notice after, not a 409
confirmation round trip, so the current Share page keeps working
unchanged.

### Review fixes (web)

An adversarial review of the phone and admin UI (headless Chromium at
390×844 with touch, and the Sailfish Browser's Gecko 91) found these;
all are fixed on the client, with tests:

- **Video under the bars (W1).** The `<video>` filled the stage and the
  bottom bar covered the player's controls: seek and time could not be
  reached, and tapping pause could hit a star. The video is now inset by
  the bars' height plus the safe areas (`--bar-h`, `.media.video`), so
  the native controls sit above the bottom bar in portrait and
  landscape; the touch guard for the controls' strip at the video's
  bottom now does its job. Tests: `share/layout.test.ts` (the rules),
  `ShareApp.test.ts` (the wrapper, a tap on the video keeps the bars);
  end to end below.
- **Sideways overscroll (W2).** Only `overscroll-behavior-y` was off, so
  a horizontal drag on a bar became the browser's back gesture.
  `overscroll-behavior: none` on `html, body`. Test:
  `share/layout.test.ts`.
- **Pill cut mid-word (W3).** See the pill above (`pillVariants`,
  `fittingVariant` in `lib/sharetext.ts`). Tests: `sharetext.test.ts`,
  `Share.test.ts` with stubbed widths.
- **No Wi-Fi hint for the address (W4).** See `addresses[].wireless`
  above. Tests: `sharetext.test.ts` (parser, labels, suggestion),
  `Share.test.ts` (labels, suggestion only, button, an older server).
- **Capabilities half applied (W5).** Tiles kept ★ after `rate` was
  turned off (the grid keyed on the library only); capabilities turned
  on were not seen until a reload; a revoked video said "cannot be
  played in this browser"; a revoked original left a blank 1:1 view.
  See "Capabilities changed on the computer" and the media checks
  above. `shareSession.refresh()` is the background read (unchanged
  sessions keep their object). Tests: `ShareApp.test.ts` "capabilities
  changed on the computer".
- **Pairing (W6, W9).** See the pairing client above. Tests:
  `routes.test.ts` (`codeProblem`), `ShareApp.test.ts` (hint and
  attributes, already paired with this share, sharing turned off during
  approval).
- **Smaller (W7).** Closing a lightbox, however it was opened (a link, a
  reload), scrolls the grid to the photo it showed. Back on the page the
  grid reads the timeline once and refetches the months only if it
  changed (it used to clear every bucket and reload every thumbnail).
  "Cannot reach the computer" is shown only when `fetch` itself failed
  (`NetworkError`), not for any `TypeError`. The scrubber's position
  marker is a short bar at the edge beside the year labels (it crossed
  the first year at the top); the gutter is 30 px. The ⋮ menu is a plain
  disclosure (`aria-controls`, `aria-expanded`, Escape closes) instead
  of `role="menu"` around text. Tab cycles inside the lightbox, since
  Gecko 91 has no `inert`. Tests: `ShareApp.test.ts`,
  `shareapi.test.ts`, `layout.test.ts`.
- **Grey tiles after a 429 (W8).** `Tile` takes an optional
  `retryDelay` from its client (the admin `LibApi` has none, so the
  admin grid is unchanged: `Grid.test.ts` checks that). The share client
  checks a failed thumbnail's URL (one byte) and answers when to load it
  again: after `Retry-After` (seconds or a date, capped at a minute) or
  a 1-2-4-8 s backoff with jitter on 429, 408, 5xx and no answer; at
  once when it loads now; never on 404 or a refusal; at most 4 times.
  All tiles share one check at a time and one cool-down, so a burst of
  failures does not add to the rate limit; a 401 or 403 `{state}` met
  by a check goes to the refusal handler. Tests: `shareapi.test.ts`
  (`retrier`, `retryAfterMs`, `checkUrl`), `ShareApp.test.ts` (429 with
  Retry-After, 404).
- Also: the library title in a share kept no space before its dot
  ("My phone· peter"); it reads "My phone · peter".

Backend dependencies left open (the client already handles both cases):

- `addresses[].wireless` on `GET /api/share` (in progress); without it
  nothing is labelled or suggested.
- `current.sameShare: true` on the pairing 409 when the cookie's device
  already belongs to the pairing's share. The 409 body has only the
  current share's name, and the phone cannot know the pairing's share
  before redeeming, so without the flag it still asks to replace the
  share with itself.
- 410 `{error, state: "off"}` for a redeem cut short by turning sharing
  off (in progress); until then it is a 403 "did not approve".
- `Retry-After` on 429 answers: honoured when present; without it the
  backoff applies.

End to end after the fixes (2026-10-15, Chromium 153 headless over the
DevTools protocol, 390×844 with touch and an Android user agent; a
scratch binary from this branch with an isolated HOME and config, admin
on 127.0.0.1:18539, share port 18543, a copy of the review's scratch
workspace): the code form showed the misread hint; the phone paired by
QR URL; on the 3 s video the video ran from y=52 to 792 with the bars at
0–52 and 792–844, `elementFromPoint` 12 and 30 px above the video's
bottom (the seek bar and the control row) returned the `VIDEO`, a tap on
the seek bar seeked (currentTime 2.4 s) without touching the stars or
the bars, and in landscape (844×390) the video ended at the bottom
bar's top; html and body computed `overscroll-behavior` none/none and a
sideways drag across the top bar left the URL and history alone; the
scrubber's marker overlapped no year label; closing the lightbox showed
the video's tile in view; no console errors. On the admin page the pill
read "Sharing · 1 phone" at 1366, 1024 and 900 px (a crowded top bar)
and "Sharing · 1" at 700 px, never cut; the Share page showed the
same-network sentence (that run's server did not send `wireless` yet; the merged
backend does).

### Map on the phone (as implemented)

Built on `feat/share-map` after the rest; "Map on the phone" above is
the design, and this is what differs or is exact.

Code:

```
internal/server/share_geo.go     GET /api/l/{lib}/geo and geo/items: location check, shareFilter, shareCluster
internal/server/api_geo.go       geoCells: the admin's clustering, now with the cell size and the cell limit as
                                 parameters, a representative (newest) and the members' bounds per cell
internal/catalog/query.go        GeoPoint.ThumbAt (the marker thumbnail's ?v=)
internal/server/share_routes.go  the two routes in shareRouteTable; shareTileOrigin in shareCSP
web/src/share/ShareMap.svelte    the map (lazy, with Leaflet): markers, the sheet, Show all photos, the lightbox
web/src/share/routes.ts          the map routes, mapPath, mapViewFromQuery, mapQuery
web/src/share/shareapi.ts        getGeo, getGeoItems, toClusters
web/src/share/ShareApp.svelte    the Map/Grid switch, the map page (a dynamic import), filters on the map
web/scripts/share-compat.mjs     jsLazyImports, preloadDeps: the build check follows the map's chunks and CSS
```

`GET /api/l/{lib}/geo?bbox=w,s,e,n&zoom=0..22[&filters]` (needs
`location`, else **403** `{"error": "not allowed in this share"}`, before
anything else is looked at):

```json
{"clusters": [
  {"lat": 60.1712, "lon": 24.9401, "count": 14, "hash": "9f3a0c11d2e4b6a8", "thumbAt": 1781092800,
   "bounds": [24.9306, 60.1654, 24.9462, 60.1744]},
  {"lat": 48.8566, "lon": 2.3522, "count": 1, "hash": "51aa4c08206609cc", "thumbAt": 1781092800, "id": 812}
]}
```

Most populated first; `thumbAt` absent when the representative has no
renditions; `id` only with `count` 1, `bounds` only with `count` > 1.
80 px cells, at most 1024 per answer. A missing or malformed `bbox`, a
`zoom` outside 0–22 or missing, any key outside the whitelist (`year`,
`limit`, `trashed`, …) is **400**; `rating` without `rate`, and `album`
or `tag` in an album share, **400** as on the timeline; an `album` of
another library **404**.

`GET /api/l/{lib}/geo/items?bbox=w,s,e,n[&limit=1..500][&filters]` (needs
`location`): `{"items": [<the media list's items>], "total": n}`, newest
first, `limit` 200 by default; the same 400s (`zoom` is not a key here).

The longitude of a box is now kept exactly when it is in range
(`wrapLon`): the modulo turned 24.94 into 24.939999999999998, so a box
made of a cluster's bounds lost the photos on its east edge (found by
the tests). The admin map gains the same exactness.

Client: the switch is a link (`a.modebtn`, "Map" or "Grid") beside
Filter, shown with `location` on a library's grid, an album page and an
album share. On the map the Photos/Albums tabs are left out (the map
gets their height). The sheet's button is **Zoom in here** (Leaflet's
own + button is called "Zoom in"). The admin map's dark-mode filter on
the tiles is not used on the phone (a CSS filter over every tile costs
older phones). The position is written to the route on every `moveend`
with `replaceState`; the lightbox of a map photo is
`…/map/view/<mid>?<filters>&at=…&z=…`, and closing it goes back to the
map with the sheet as it was. A 403 from either route reads the session
again; the map route then redirects to the grid.

**Bundle** (Vite build, `npm run build`): the phone's first load is the
same four files as before, JS 149.7 KiB (54.1 KiB gzipped) against
142.5 KiB (51.4 KiB) before the map: +7.1 KiB (+2.7 KiB gzipped) for the
routes, the client calls and the switch, plus Vite's preload helper in
the common chunk; CSS unchanged at 21.6 KiB. Opening the map fetches
four more files: Leaflet 1.9.4 (146.5 KiB, 42.2 KiB gzipped, the same
chunk the admin map uses), the map view (10.2 KiB, 4.6 KiB gzipped)
and their stylesheets (Leaflet's 15.2 KiB, the map's 3.1 KiB; 7.3 KiB
gzipped together). `check-share-apis.mjs` now follows dynamic imports
and the stylesheets Vite preloads with them and prints both
("share.html loads 4 files …", "on demand (the map): 4 more files …");
Leaflet passes the Gecko 91 rules; its CSS uses `touch-action`
(tolerated: Leaflet uses Touch Events with non-passive listeners where
the browser has them, as Gecko 91 does).

Tests: Go `share_geo_test.go` (a library share's clusters and items:
every live photo with a position and never the trashed one, the
newest as the representative, bounds, singles at zoom 22, the cluster's
bounds as a box returning exactly its photos, every filter of the
whitelist including `album`, `rating`, dates, size and camera, another
library 404, an album of another library 404, the refused keys and bad
box, zoom and limit, no path or file name in any answer; an album
share: 403 without `location` (its default), only the album's live
members, no rating without `rate`, the folder key 404, `album`/`tag`/
`rating` 400, boxes around photos outside the album empty, a photo
taken out of the album gone at once, `location` turned off 403 at the
next request; `shareCluster` bounded at 1024 for 20 000 points at zoom
0 to 22 and the representative's tie rules; the CSP's one outside
host), plus the geo routes in the golden table, the authentication
test (401, paused, expired, unavailable, revoked on both) and the
capability matrix (both routes 403 without `location`). Web:
`ShareMap.test.ts` with the real Leaflet in jsdom (the switch only with
`location`, in both share kinds; the map routes redirecting without it;
fit to all from the world at zoom 0; the position in the route and a
reload at it; a cluster's sheet with the newest first, its lightbox
turning pages within the sheet and closing back to the sheet; Zoom in
here; a single photo's lightbox and Back; filters on the map and Apply
keeping the position; "Nothing matches" with Clear filters; an album
share's map never asking for `album` or `tag`; `location` turned off
while the map is open; the tile error note; `navigator.geolocation`
never touched), the map routes and the query helpers in
`routes.test.ts`, `toClusters` in `shareapi.test.ts`, and the build
check's new followers in `compat.test.ts`. The fake share listener
answers `geo` and `geo/items` with a simple grid clustering; it gives
four more items positions, so the album share's location count is 3.

End to end (by hand over the DevTools protocol, not in `make check`):
the binary built from this branch, a scratch `HOME` and
`XDG_CONFIG_HOME`, a workspace with one library of 46 generated JPEGs
(41 with GPS EXIF in Helsinki, Espoo, Tampere, Paris, Rome, Sydney and
New York; Paris shot on a "Pixel 7"), an album "Trip" (Paris, Rome and
three of Helsinki), the library share "My phone" (all capabilities)
and an album share "Trip guests" with `location`; `serve --share` on
the LAN address, and two headless Chromium 153 instances at 390×844
with touch emulation and a mobile user agent. All 24 checks passed:
the switch on the grid; the map fitted to all 41 photos over OSM tiles
loaded under the share CSP (exact header checked with curl); no script
errors; a one-finger drag pans and the route follows; a two-finger
pinch zooms in (1 → 3); a Helsinki cluster's sheet with its thumbnails;
a thumbnail opens the lightbox over the map, a swipe turns to the
sheet's next photo, closing returns to the sheet; Zoom in here (10 →
16); a single photo marker opens the lightbox; the camera filter on the
map keeps the position and leaves only Paris (8); Show all photos fits
them (zoom 15); a reload keeps position and filters; with
`tile.openstreetmap.org` blocked the plain background, the note and a
working marker; `location` turned off on the computer sends the map
route to the grid and removes the switch; the album share's map holds
the album's 14 photos only, under `/api/l/album/` without `album` or
`tag`, and a box around New York (photos in the library, none in the
album) is empty. The Sailfish Browser itself was not available: pinch
and drag on Gecko 91 are covered by Leaflet's Touch Events path, not by
a device run (manual device checklist).

### Timeline scrolling and the camera filter (after use)

Two complaints after using the phone app on a real library:

**"Scrolling over time was awkward and mostly snapped to each new
year."** Reproduced in headless Chromium (390×844, touch) against a
fake share listener with 213,000 photos over 2011–2025 (uneven months:
more every year, big summers and Christmases, a few empty months and
big trips). The scrubber's year labels were `<button>`s filling its
30 px gutter (15 of them, 14 px tall each), and a press on one stopped
the drag and jumped to that year's start on release. A finger is wider
than a label: with touches of a finger's size (radius 18 px), Chrome's
touch adjustment sent **13 of 16** presses on the track to the nearest
year button, so the drag never started and the tap landed on a year's
first month (December, newest first). The mapping itself was
proportional; what the finger met were the year buttons. Fixed as
"Timeline scrolling" describes: the labels are marks, the whole gutter
drags, the thumb is large and off the edge. Afterwards **0 of 16**
finger presses went anywhere but the track, and a drag along it passed
58 months (4 of them Januaries), each in turn.

Measured in the same harness (Chromium headless, CPU throttled 4×, the
grid scrolled programmatically for 240 frames; before → after):

| | before | after |
|---|---|---|
| 40 px a frame (reading): mean frame, frames over 33 ms | 16.7 ms, 0 | 16.7 ms, 0 |
| 150 px a frame (a fling): mean, frames over 33 ms, long tasks | 20.6 ms, 12, 3 (238 ms) | 16.7 ms, 0, 0 |
| 600 px a frame (a hard fling): mean, frames over 33 ms, long tasks | 44.1 ms, 125 of 239, 5 (562 ms) | 16.9 ms, 1 of 239, 0 |
| the whole library in 4 s: p95 frame | 33.4 ms | 16.8 ms |
| DOM nodes while scrolling | ≤ 300 | ≤ 314 |
| month requests for a drag down the whole track and five taps on it | 62 | 10 |

The grid was already windowed (about 300 nodes); the frames were lost
creating tiles and decoding thumbnails that flew past, which the grey
placeholders above remove, and the requests were months the drag only
passed, which the longer rest while dragging and the cancelled loads
remove.

**"The phone filter lacks camera."** The camera filter and the
facets' `cameras` were added on 2026-10-14 (`bd205eb`, `d74f37c`), after
the build the user ran (the app's log shows version `c6e8fc0`, which
has neither), so that phone had no camera section at all. In the
current build the section was there but easy to miss: below the dates
and the rating, past the sheet's first screen on a phone, as a select
of bare model names, and photos without a model (most videos) could
not be chosen. Now it comes right after Show, with maker and model,
counts, a search, all cameras on request and "Unknown camera" (above).
Scope is unchanged: the cameras and the `nocamera` count come from the
share's live in-scope photos through the same restrict as the
timeline, and `nocamera=1` is restricted like every other filter.

Tests: `lib/timeline.test.ts` (200k photos over 15 years laid out from
the counts alone in under 50 ms, every month's height from its count;
the scrubber's mapping monotonic, linear and proportional to the
photos, month by month; the anchor across a column change, a month
above changing, a header and a month gone), `ShareScrub.test.ts` (the
grid over that library requests one month and draws at most two;
a finger drag follows proportionally with `preventDefault` on
`touchstart`/`touchmove`, over more than 60 months, few of them
Januaries, with the bubble, loading at most five months; grabbing the
thumb does not jump; the slider's keys and value; Jump to a date by
year and month, empty months disabled, Escape; loads cancelled when the
view leaves; the place kept when the phone turns), `layout.test.ts`
(thumb size and edge offset, labels not targets), `Grid.test.ts` (the
admin scrubber has no buttons), `share/filters.test.ts` (`nocamera` in
the route and chips, `cameraLabel`, `searchCameras`), `ShareApp.test.ts`
(Camera second, the five most used, search, all, Unknown camera into
the route and back to a camera), `shareapi.test.ts`; Go:
`TestFacets` (`NoCamera` counts and filter), `TestShareFilterWhitelist`
(`nocamera` values), `TestShareFacets` (`nocamera` per scope, the album
share's only from its members).

End to end (2026-10-15, Chromium headless over the DevTools protocol,
390×844 with touch, the built bundle against the fake listener of
213,000 photos): a drag on the scrubber showed the bubble "November
2020" beside the thumb; a tap on "December 2025 ▾" opened the years
(with counts), 2019 showed its months with October disabled, March
scrolled to "March 2019" with its photos; scrolling on and turning the
phone to 844×390 kept March 2019 and the same row at the top (5
columns), and back; the filter sheet showed Camera at the top with the
five most used, Unknown camera and "All 8 cameras", "apple" found the
two iPhones, Unknown camera applied as `?nocamera=1` with its chip; no
horizontal scroll, no console errors.

### Not done

- The CA install step (Q3: `/ca.crt`, the "Install the certificate"
  page): not built, as decided, until a phone has shown it enforces
  the name constraints.
- The `?t=` media URL (Q5): not built.
- `make e2e-share` (Playwright, Q11): not built; the end-to-end run
  below was driven by hand over the DevTools protocol.
- The README section.
- Approval requests show only while the Share page or a pairing dialog
  is open; there is no prompt elsewhere in the admin UI.
- Pinch to change the grid's columns.
- The manual device checklist (iOS is untested), including the phone
  map's pinch and drag on the Sailfish Browser.
- Windows `GetIpForwardTable2` and macOS `route -n get default` are
  compiled and vetted but untested on those systems.

### Tests

Go, `internal/share`: config round trip, damaged file, modes, the lock,
unknown kinds, devices and verification, limits, expiry and sweep,
scope validation, pairing (single use, TTL, concurrency, codes, limits,
approval), rate limits, CA and leaf (constraints, SANs, verification,
reissue), candidates, home-network comparison, the connection filter,
QR. `internal/server/share_*_test.go`: the golden route table; every
admin pattern 404 on the share handler with a valid cookie; 401 for
missing, malformed, wrong-secret and revoked cookies on every route,
the exact `Set-Cookie`; paused, expired and unavailable on every route;
the route × scope matrix (in scope, other album, unshared library,
trashed, removed between requests, nonexistent, malformed) with list
results checked against `AlbumHashes ∩ live`, `facets` per scope; the
filter whitelist (every allowed key and malformed values on `timeline`
and `media`, refused keys, no parameters on `facets`); facets per scope
(an album share lists only its members' cameras and years and never
tags or albums; a library share's tags and albums only with live
photos; removal from the album shows at once); the capability matrix
(`rating` and `gps` filters, `gps` in facets); ratings (one hash, 409 during a move, the log
line); redaction (exact detail keys, no paths); headers, CSP, no HSTS,
Range; middleware (Host, Origin, Sec-Fetch-Site, mutation headers, body
cap, lockout, pair limit, mDNS host); album identity (rename, empty,
delete with id reuse, another `LibraryID`); a busy library (503, then
opened on demand); management (loopback only, wildcard admin address,
create/update validation, filled-in keys, devices, revoke-all, delete,
`share.json` without secrets); pairing flows (token once, code, expiry,
wrong codes, replace, approve/deny/timeout, paused share, restart); the
lifecycle over real TLS and HTTP/2 (off/on, rebind to 127.0.0.2,
pause on another subnet or gateway, busy port, auto-off, the pill
status); one sharing process; shutdown flushing last seen; cookie
renewal. `cmd`: `picmover share` end to end (including the ambiguous
album and the refusal while held), `--expires` parsing, `serve --share`
refusals and the printed URL, fingerprint, share line and pairing.

Review fixes (Go): `share_review_test.go` (S4 static paths),
`share_review_auth_test.go` (R1/S5 lockout, R2 media budget),
`share_review_pair_test.go` (S2 wrong codes and the global budget, R12,
R8, R9), `share_review_conn_test.go` (S3 per-address cap and first-byte
deadline over real TCP/TLS), `share_review_life_test.go` (S1 keepOn on
another network and without home, R3 waiting and resuming, R4 keepOn
before the network with the watcher, R6, R7, R13 status during a drain,
R10 one listen, notice and mdns, R16), `share_review_libs_test.go` (R11
idle close, admin use, stop, share creation; R15 no re-reads),
`share_review_api_test.go` (`Retry-After`, `sameShare`, `wireless`,
`phones`); in `internal/share`: `review_test.go` (Locate, EnsureFor),
`review_config_test.go` (Managed never collides, dead holder, unknown
members, damaged copies, periodic sweep), `wireless_linux_test.go`,
`TestFilterListenerPerIP`/`FirstByte`, wrong codes per address and the
approval endings in `pairing_test.go`, `VerifyCookie` in
`config_test.go`. `internal/shell`: the tray's paused labels.

Filters on the phone (`ShareApp.test.ts`, `share/filters.test.ts`,
`share/shareapi.test.ts`): the Filter button and the sheet's sections
per share kind and capability, from the facets; Apply into the route,
the chips with their count, removing one chip, Clear all, a reload
keeping them; every year (months, presets); the lightbox keeping the
query and turning pages within the filtered photos; capability hiding
(no rating or location sections, those keys dropped from the route); an
album share's facets (no tags or albums, only the album's cameras) and a
stale `tag`/`album`/`rating`/`gps` link never reaching the server; the
query parser, chips, year chips and month pickers; the facets parser.
The fake share listener answers `facets`, the new filters and their
400s as the handlers do. A run by hand (headless Chromium at 360×760,
touch, over the DevTools protocol, a scratch workspace) paired a library
and an album share and used the sheet, chips and lightbox; no
horizontal scroll, no console errors.

Web (vitest against the fakes): `Share.test.ts` (the pill from
`/api/share/status` on every page, counting only active shares and never
polling `/api/share` off the Share page; the switch and settings; a
failed start; create/edit for both kinds; pairing with countdown and
cancel; capabilities, pause, extend, revoke, revoke all, delete;
approval requests; broken, busy, unknown-kind and notice states;
`managedElsewhere`; "Share…"/"Shared (n)"), `ShareApp.test.ts` (pairing
by token and code, expired, rate limited, already paired, approval,
refused share (410 with state), full share (409 without `current`); the
replacing screens including the 503 session; both share kinds; the
lightbox per capability; ratings; gestures), and the parsers, routes,
gestures and the Gecko 91 compatibility rules. The vitest timeout is
30 s: under CPU load the first test of each file that mounts `App`
(which transforms the whole admin app after `vi.resetModules`) took
over the default 5 s and failed, ten tests in ten files at a time, with
knock-on failures in the next test of the same file; idle runs never
failed.

End to end (by hand, 2026-10-14, Chromium 153 headless over the
DevTools protocol; not in `make check`): a scratch binary with the
embedded web build served a scratch workspace (two libraries, peter
with eight JPEGs and a 3 s H.264 MP4, penny with three JPEGs; an album
"Summer" of three photos and the video), with the sharing settings in a
scratch config directory. On the Share page (desktop viewport,
loopback) the port was set and sharing turned on (bound to the LAN
address 192.168.1.136); "Share libraries…" (peter, penny) and "Share
album…" (Summer) were created and paired through "Pair a phone now".
Phone A (390×844, touch, an Android Chrome user agent) opened the QR
URL, which lost its token from the address bar, paired, saw both
libraries with covers, browsed peter's grid, opened the lightbox,
turned the page with a swipe, rated 4 stars (`{"updated": 1}`), saw
Download, and closed the lightbox with a swipe down. Phone B, a
separate profile, typed the code in lower case and saw only the album
(4 items), no stars and no Download, and played the video inline.
The device cookie was `__Host-`, HttpOnly, Secure, SameSite=Strict.
Revoking phone A on the Share page gave it "This phone was removed" on
its next request; pausing the album share gave phone B "This share is
paused", and after Resume, Try again brought the album back. The pill
on a library page read "Sharing · 2 shares · 1 phone" from
`/api/share/status`. curl against the share port: every admin path
(`/api/share`, `/api/share/shares`, `/api/libraries`, `/api/jobs`,
`/api/fs`, `/api/app`, `/index.html`) 404 with a valid cookie;
`/api/session` and library routes 401 without one; with phone B's
cookie a hash outside the album 404 on `thumb` and `preview`, a media
id outside it 404, `peter` instead of `album` 404, the original of a
photo 403 (no `download`), the video 206 for a Range, `rating=` 400,
a rating 403; a foreign Host 421, a foreign Origin 403, a POST without
`X-Picmover` 403; the headers and CSP as listed, no HSTS. No console
errors on any page. The run found the Download link styling bug above
(fixed); nothing else failed.
