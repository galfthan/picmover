# Sharing on the home Wi-Fi (phone viewing)

Status: design for review, written before any implementation. Nothing
here is built yet. When it is, "As implemented" sections go at the end,
backend first and then frontend, as in the other design documents.
Anything marked **unverified** is a browser or OS behaviour that a spike
on real devices has to confirm before the related code is written (see
"Phase 0" and "Open questions").

## Goal

The user turns on sharing on the computer, scans a QR code with a phone,
and can then browse chosen libraries on the phone over the home Wi-Fi:
the timeline grid, albums, a lightbox with swipe, videos, downloads of
originals, and star ratings. Nothing else. The admin UI with its imports,
moves, trash, backups and folder browser stays reachable only from the
computer itself.

Linux is the main platform. Windows and macOS are covered where they
differ (firewall prompts, config paths, the tray). The phone browsers to
support are iOS Safari, Android Chrome, and the Sailfish Browser on
Sailfish OS 5.1 (Gecko-based, see "Browser support").

## Decisions (from the request)

| Question | Decision |
|---|---|
| Login | QR-code pairing. The computer shows a QR code; scanning it signs that phone in, and it stays signed in. Paired phones are listed and can be revoked. No passwords. |
| Encryption | HTTPS with a self-signed certificate, with a one-time warning on the phone |
| Which libraries | Only the libraries the user explicitly marks as shared |
| What a phone may do | View (grid, albums read-only, lightbox, video), download originals, set star ratings. Nothing else. |
| Ratings from a phone | The library's normal rating (`user.db` `ratings`), the same one the admin UI sets. Per-person ratings are open question Q1. |

Decisions this document makes (justified below):

| Question | Decision |
|---|---|
| Architecture | A **second listener with its own handler and its own `ServeMux`**, built from an explicit allowlist table. The admin mux is never reachable from it. |
| Admin listener | Unchanged: loopback only. Sharing refuses to start when the admin `--addr` is not loopback. |
| Settings, devices, certificate | Per-user config dir (`os.UserConfigDir()/picmover/share/`), never inside the workspace |
| Shared library identity | Workspace root + library folder id + the `user.db` `LibraryID`; all three must match |
| Device credential | 256-bit random secret in a `__Host-` cookie (`HttpOnly; Secure; SameSite=Strict`), stored server-side only as SHA-256 |
| Pairing token | 256-bit, single use, 2-minute TTL, in memory only, carried in the URL **fragment**. Typed fallback: an 8-character one-time code. |
| TLS | A local CA (per user, name-constrained) signs a leaf certificate for the current LAN addresses and `<host>.local`. ECDSA P-256, TLS 1.2+. No HSTS. |
| Bind address | The chosen LAN IPv4 address (by default the one of the default route), not `0.0.0.0`. Connections are filtered to that subnet (and loopback). Sharing pauses when the network changes. |
| Phone UI | A separate Vite entry (`share.html`) with its own small shell, reusing Grid, Tile, Lightbox and RatingStars in a read-only mode |
| Restart | Paired devices and settings persist. Sharing is **off** after a restart unless "Keep sharing on" is set (open question Q4). |

## Non-goals

- Access from outside the home network (internet, VPN, relays). Remote
  access is a separate project with its own threat model.
- Multiple user accounts, per-user permissions beyond "this device may
  view these libraries", and per-device library sets (open question Q6).
- Editing anything other than ratings from a phone: tags, albums,
  trash, imports, moves, backups, jobs, settings, and the map.
- A native phone app, push notifications, offline sync, uploading from
  the phone (a later "import from phone" would need its own design).
- A publicly trusted certificate (ACME needs a public name; open question
  Q3 covers the local-CA option).
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
  - `devices.go`: the registry; `Verify(cookie) (Device, bool)` in
    constant time; revoke, rename, last seen.
  - `pairing.go`: tokens and codes, TTL, single use, attempt limits.
  - `ratelimit.go`: token buckets per remote IP and global.
  - `tlscert.go`: local CA, leaf, SANs, fingerprints, renewal.
  - `netif.go`: LAN address detection, the change watcher, the remote
    address filter.
  - `qr.go`: QR as SVG and as terminal text.

### Layout

```
internal/share/                       config, devices, pairing, rate limits, TLS, network, QR (above)
internal/server/share.go              share lifecycle in Running (start/stop/pause), admin /api/share/* handlers
internal/server/share_routes.go       shareRouteTable, the share mux, shareMiddleware, device auth
internal/server/share_api.go          redacted handlers: session, pair, libraries, library, detail, rating; live-row checks
internal/server/share_static.go       static allowlist for the share listener (share.html, /assets/*, icons, manifest)
cmd/serve.go                          --share, --share-library, --share-port, --pair
internal/shell/                       tray item "Share on Wi-Fi", keep-on start
web/share.html                        second Vite entry
web/src/share/main.ts, ShareApp.svelte, Pair.svelte, ShareLibraries.svelte, shareapi.ts
web/src/pages/Share.svelte            admin page: toggle, libraries, QR, devices, status
web/src/components/SharePill.svelte   "Sharing" indicator in the admin top bar
```

## Share listener: routes (the allowlist)

All on the share listener only. `{lib}` is a shared library id. Every
route except `GET /api/session`, `POST /api/pair` and the static files
needs a valid device cookie.

| Method | Path | Result | Notes |
|---|---|---|---|
| GET | `/api/session` | `{device: {id, name}, computer, libraries: [shareLib]}`, or 401 `{error, paired: false}` | Renews the cookie when due |
| POST | `/api/pair` | `{token}` or `{code}` plus `{name}` → 200 `{device: {id, name}}` and `Set-Cookie` | 400/410/429, see "Pairing" |
| GET | `/api/libraries` | `{libraries: [shareLib]}` | Shared, existing, matching identity only |
| GET | `/api/l/{lib}/library` | redacted: `{id, name, summary: {total, images, raw, videos, earliest, latest}}` | No root, version, ffmpeg, cameras, userdb or similar |
| GET | `/api/l/{lib}/timeline` | as admin | Allowed filters only (below) |
| GET | `/api/l/{lib}/media` | as admin (item JSON) | Allowed filters only |
| GET | `/api/l/{lib}/media/{id}` | redacted detail (below) | Live rows only |
| GET | `/api/l/{lib}/thumb/{hash}` | as admin | Hash must be a live row of `{lib}` |
| GET | `/api/l/{lib}/preview/{hash}` | as admin | Same |
| GET | `/api/l/{lib}/large/{hash}` | as admin | Same; derivation is bounded by the existing slots |
| GET | `/api/l/{lib}/file/{id}` | as admin, Range, `?download=1` | Live rows only; `Cache-Control: private, no-store` |
| GET | `/api/l/{lib}/albums` | `{albums: [{id, name, count, cover}]}` | No createdAt/updatedAt needed; read only |
| POST | `/api/l/{lib}/ratings` | `{hashes: [h], rating: 0..5}` → `{updated}` | Exactly one hash, which must be a live row; `notMoving` → 409 |
| GET | `/`, `/share.html`, `/assets/*`, `/favicon.png`, `/icons/*`, `/manifest.webmanifest` | the share SPA | No auth; the code is public anyway |

`shareLib` is `{id, name, cover, total}`: no path, no state. `name` is
the folder name (the admin UI already shows that).

Redacted detail: `{id, hash, type, takenAt, offset, w, h, dur, size,
ext, fileName, cameraMake, cameraModel, lens, lat, lon, rating, albums:
[{id, name}], displayable, thumbAt}`. Left out: `relPath`,
`originalPath`, `imports`, `sessionId`, `importedAt`, `thumbState`,
`thumbError`, `phash`, `deletedAt`, `tags`, `takenAtSource`. `fileName`
is the base name only. Whether to send GPS at all is open question Q8
(recommended: yes, shown as text with no map, since the photos are the
user's own).

Filters accepted (a whitelist in the share handler; anything else is
400): `type`, `from`, `to`, `recur`, `months`, `rating`, `album`. Refused:
`trashed`, `import`, `session`, `tag`, `camera`, `gps`, and any unknown
key. Tags and cameras are left out because the phone has no tag or camera
list (open question Q7).

There are **no** routes for `/api/fs`, `/api/jobs*`, SSE, `/api/app*`,
library open or create, tags, albums (write), imports, trash, transfer,
similar, backups, geo, cover, or `/api/share/*`. All of those are 404 on
the share listener, because the share mux has no pattern for them.

### Per-request authorization (`shareScoped`)

Every library route runs these steps in this order. Each refusal is the
same 404 `{"error":"not found"}` unless stated, so nothing can probe
which libraries exist:

1. **Device:** cookie → `devices.Verify` (look up by id, then
   `subtle.ConstantTimeCompare` of SHA-256(secret)). Missing or bad →
   401 `{error, paired: false}`. A revoked device is the same as an
   unknown one.
2. **Library:** `validLibID({lib})`, and `{lib}` is in the shared set of
   the *currently served* workspace root (`ws.root()`). Its `user.db`
   `LibraryID`, read when the library is acquired, equals the stored
   one. A renamed folder is no longer shared (safe failure: the admin
   page shows "a shared library is missing"). A copied library at
   another folder is not shared either.
3. **Acquire:** `ws.entry`/`entryFor` + `acquire`. A closed shared
   library is **opened on demand** (`ws.open`: the same code path and
   lock as the admin open). Busy (locked by another process), error,
   retired → 503 `{error: "This library is not available right now."}`.
   Rebuilding → 503 with the same text. The phone never sees holder pids
   or paths.
4. **Row checks:**
   - `{id}` routes: `GetMedia` must return a row with `DeletedAt.IsZero()`.
   - `{hash}` routes: a new `catalog.LiveHash(ctx, hash) (bool, error)`
     (`SELECT 1 FROM media WHERE hash = ? AND deleted_at IS NULL`; `hash`
     is `UNIQUE`, so it is one index probe).
   - Ratings: the one hash must pass `LiveHash`.
5. **Handler.**

Hash enumeration: hashes are 64-bit content hashes. A device can learn
the live hashes of a shared library by listing it, which is allowed
anyway. Step 4 means that guessing a hash (2^64) gains nothing beyond
that list: it never reaches trashed items or other libraries. Path
traversal: no share route takes a path. Files come only from catalog
rows through `openOriginal` (shape check, containment, `EvalSymlinks`,
`Lstat`, `SameFile`), unchanged.

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
  every rating change ("rating 4 on 9f3a… in peter by device 'Anna's
  iPhone'") log at Info.

## Turning sharing on and off

### Admin API (admin listener only)

| Method | Path | Body → Result |
|---|---|---|
| GET | `/api/share` | `{state: "off"\|"on"\|"paused"\|"error", reason?, url?, mdnsUrl?, port, address, addresses: [{ip, iface, subnet}], libraries: [{id, shared, missing?}], devices: [device], fingerprint: {leaf, ca}, keepOn, autoOff: {mode, until?}, managedElsewhere?: {pid, cmd}, firewallHint}` |
| POST | `/api/share` | `{enabled?, libraries?: [id], address?, port?, keepOn?, autoOff?: "never"\|"1h"\|"4h"\|"quit", mdns?: bool}` → the GET body |
| POST | `/api/share/pairings` | `{}` → `{url, code, qrSvg, expiresAt}`. 409 when sharing is off. |
| POST | `/api/share/pairings/cancel` | `{}` → 204 (drops every open token) |
| POST | `/api/share/devices/{id}` | `{name}` → device |
| POST | `/api/share/devices/{id}/revoke` | `{}` → 204 |
| POST | `/api/share/cert/renew` | `{}` → the GET body (new leaf; the CA is kept) |

`device` is `{id, name, pairedAt, lastSeen, lastAddress, browser}`.
`browser` is a coarse label from the User-Agent at pairing time ("Safari
on iPhone", "Chrome on Android", "Sailfish Browser"). It is for
recognition only, never for decisions.

These routes manage the LAN exposure, so they get one extra rule beyond
the existing admin checks. They answer 403 unless the admin listener is
loopback (`IsLoopbackAddr`) **and** the request's `RemoteAddr` is
loopback. With `serve --addr 0.0.0.0:7439` (the existing warned mode),
sharing cannot be managed or started. `serve --share` with a non-loopback
`--addr` is a usage error: "--share needs the default loopback --addr;
the shared view has its own listener".

The admin page polls `GET /api/share` every 2 s while it is open, which
shows "Paired: Anna's iPhone" as soon as a scan completes. A share event
stream is not worth it for this.

### One sharing process

The app and `serve` can run at the same time (different ports) and read
the same per-user `share.json`. The process that turns sharing on takes
an exclusive lock on `<config>/picmover/share/share.lock` (the same
`flock`/`LockFileEx` helpers as the library lock) and keeps it while
sharing is on or paused. Another process's share page then shows
"Sharing is managed by another picmover process (pid N, picmover-app)"
with every control disabled (`managedElsewhere`). Writes to `share.json`
take the lock briefly. The process that is not sharing never writes the
file while another process holds the lock.

### Entry points

- **Admin UI:** a "Share" item in the top bar (next to the job pill)
  opens `#/share` (Share page, below). While sharing is on, the item is
  a green pill: "Sharing · 2 phones". It is visible on every page, in
  serve mode and in app mode.
- **Desktop app tray** (Windows, Linux): a checkbox item "Share on
  Wi-Fi". When the tooltip and the label show the address, it reads
  "Share on Wi-Fi (192.168.1.20)".
  - Turning it on with a saved library set starts sharing.
  - Turning it on without one opens the window on `#/share`.
  - Turning it off stops sharing at once.

  macOS has no tray (stub), so the UI is the entry point there.
- **CLI:**
  `picmover serve <dir> --share [--share-library NAME]... [--share-port 7443] [--pair]`.
  - `--share` turns sharing on for this run with the saved library set,
    replaced by `--share-library` when given (and saved).
  - Without any library it fails: "no library is shared; pass
    --share-library NAME or choose libraries on the Share page".
  - `--pair` prints a QR code in the terminal (Unicode half blocks) and
    the one-time code at start, valid for 2 minutes. Pressing Enter
    prints a new one.
  - Without `--share`, `serve` never starts sharing by itself, even when
    the app's "Keep sharing on" is set. That setting belongs to the app
    (Q4).
  - On start it prints `Sharing on Wi-Fi at https://192.168.1.20:7443
    (peter, penny); certificate SHA-256 AB:CD:…`, and one line per
    pairing, revocation and pause.

### Restart, auto-off, network changes

- **Persistent:** paired devices, the shared library set, the port, the
  chosen interface, the CA and the leaf, and `keepOn`. **Not
  persistent:** pairing tokens and codes, rate-limit state, and the
  on/off state unless `keepOn`.
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
  "workspaces": {
    "/media": {"libraries": [{"id": "peter", "libraryId": "6f1c…"}]}
  },
  "devices": [
    {"id": "d_3f9a1c2e7b5d4a10", "name": "Anna's iPhone", "secretSha256": "…64 hex…",
     "pairedAt": "2026-10-14T19:02:11+03:00", "lastSeen": "…", "lastAddress": "192.168.1.34",
     "browser": "Safari on iPhone", "cookieIssuedAt": "…"}
  ]
}
```

Why the user config and not the workspace:

- The secrets (CA key, device hashes) must not travel with an external
  photo drive.
- Another computer serving the same drive must not accept this
  computer's phones.
- The workspace folder can be readable by other accounts.
- Sharing is a decision about this machine and network, not about the
  photos.

Shared libraries are keyed by workspace root (links resolved, as
`/api/libraries` reports it). After an app workspace switch, the shared
set of the new root applies, and it is usually empty, so the phone sees
no libraries until the user shares some. A single-library `serve <lib>`
uses the library's root as the key, with its `singleID` as the id.

`lastSeen`/`lastAddress` are updated in memory on every request and
written at most every 5 minutes and at shutdown, so browsing does not
rewrite the file constantly.

## Pairing protocol

### Issue (computer)

1. The user opens Share → "Pair a phone". The admin UI posts
   `/api/share/pairings`.
2. The server creates:
   - a **token**: 32 random bytes (`crypto/rand`), base64url, 43
     characters;
   - a **code**: 8 characters from a 32-letter alphabet with no
     look-alikes (`23456789ABCDEFGHJKMNPQRSTUVWXYZ`, 40 bits), shown as
     `K7QM-3XRD`.

   Both are kept **in memory only** as SHA-256 hashes, with
   `expiresAt = now + 2 min`, `used = false`, and a counter of wrong
   code attempts. At most 3 pairings are open at once; issuing a fourth
   drops the oldest.
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
   - Marks it used (an atomic swap under a mutex, so two concurrent
     redeems cannot both win).
   - Creates the device:
     - `id = "d_" + 8 random bytes hex`;
     - `secret` = 32 random bytes;
     - `secretSha256` stored;
     - `name` trimmed to 1–60 characters, control characters removed,
       "Phone" when empty;
     - `browser` from the User-Agent;
     - `pairedAt`.

     It saves `share.json` (with at most 20 devices: "Revoke a phone
     first").
   - Answers 200 `{device: {id, name}}` with
     `Set-Cookie: __Host-picmover=<id>.<base64url secret>; Path=/; Secure; HttpOnly; SameSite=Strict; Max-Age=34560000`
     (400 days, Chrome's cap).
8. The phone goes to `#/`. The computer's Share page (polling) shows
   "Paired: Anna's iPhone, just now" with **Rename** and **Revoke**
   buttons right there, and the QR is gone.

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
shows "This phone was removed from picmover on the computer". Revoking
also offers "Revoke all phones". Renaming changes the label only.

### Threats to pairing

- **Someone photographs the QR or reads the code:** it is valid for 2
  minutes, works once, and works only on the home Wi-Fi with sharing on.
  If an intruder redeems it first:
  - the user's own scan fails with "already used";
  - the computer shows the unexpected device's name, browser label and
    address in the paired list, with Revoke next to it.

  A mandatory approval click on the computer would close that window
  completely (open question Q2).
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

| Route | View |
|---|---|
| `#/pair/<token>`, `#/pair` | pairing (name field, Pair; the code form when there is no token) |
| `#/` | the shared libraries (cards with cover and count). With one library it redirects there. |
| `#/l/<id>/` | grid (timeline, newest first) |
| `#/l/<id>/albums` | album list (cards) |
| `#/l/<id>/albums/<aid>` | grid of the album |
| `#/l/<id>/view/<mid>` | lightbox over the grid it came from |
| anything else | `#/` |

On 401, every route shows the "Not paired" screen (show a code on the
computer, or type the code). On 503 it shows "This library is not
available right now (the computer is busy with it). Try again later."

### What is shown

- **Top bar** (compact, 48 px): library name, a Photos/Albums switch, a
  filter button (a bottom sheet with: Photos/Videos/All; rating ≥ 1–5;
  year range), and a menu (Libraries, "Paired as Anna's iPhone",
  Help).
- **Grid:** the timeline with month headers and the scrubber.
  - Column count by width: 3 in portrait under 500 CSS px, 4–5 in
    landscape or on tablets. Pinch-to-change-columns is later.
  - Tiles show the video badge and duration and the rating stars when
    rated, always visible: there is no hover on touch.
  - No selection and no checkboxes. A tap opens the lightbox.
- **Lightbox:**
  - Swipe left and right (exists), swipe down to close, tap to toggle
    the chrome.
  - Double-tap toggles fit and 1:1 on the original when displayable, else
    on the preview. Native pinch-zoom is allowed inside the lightbox only
    (`touch-action: pinch-zoom` on the image).
  - Bottom bar: **RatingStars** (5 stars, 44 px targets, tap the current
    rating to clear), **Download** (`file?download=1`), **Info** (a
    bottom sheet with the redacted detail: date, camera, lens, size,
    dimensions, albums, GPS as text).
  - Video uses `<video controls playsinline preload="metadata">` with the
    preview as poster, and Download beside it.
- **Hidden** (absent from the share bundle, not merely hidden): Import,
  Imports, Library stats, Map, Trash, Similar, Backups, Libraries
  management, workspace setup, the selection bar, tags, the album editor,
  move, the job pill, toasts about jobs, and the folder picker.
- **Rating feedback:** optimistic, rolled back on error. A 409 (a move
  runs on the library) shows "Ratings are paused while photos are being
  moved on the computer"; 404 shows "This photo is no longer in the
  library".

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
- **Workspace switch** (app): the share mux reads `ws.root()` per
  request, so a phone sees the new workspace's shared set (usually
  nothing) at its next request. Entries retired by the switch answer 503.
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

1. The photos and videos of the **shared** libraries.
2. Everything **else** on the computer: other libraries, the trash,
   import sources, the file system (through `/api/fs`), and the
   admin powers (import with move, trash, empty trash, move between
   libraries, rebuild, backups).
3. The integrity of `user.db` (ratings).
4. The availability of the computer and the library locks.
5. Location data (GPS) in the photos.

Adversaries and what stops them:

| Adversary | Can | Stopped by |
|---|---|---|
| A guest or IoT device on the home Wi-Fi, not paired | Connect to the share port | Device auth on every API route (401); rate limits; static files reveal only public code; the admin listener is loopback-only |
| The same, with the QR or code in view | Pair within 2 minutes, before the user | Single use, short TTL, the paired list shown at once with Revoke; Q2 (approval) closes it fully |
| A paired phone (family member, or lost or stolen) | View the shared libraries, download originals, change ratings | Intended. Revoke; "last seen"; ratings logged with the device name; no access to anything in asset 2 |
| A malicious web page in the phone's browser | Make requests to the share origin | `SameSite=Strict` cookie, Origin and Sec-Fetch checks, `X-Picmover` on POST, no CORS, CORP same-origin, `frame-ancestors 'none'` |
| A malicious page in the computer's browser (DNS rebinding, CSRF) | Reach the admin API or the share management routes | Unchanged admin checks; `/api/share/*` additionally require a loopback listener and peer |
| An attacker on the LAN doing ARP spoofing (MITM) at first contact | Present its own certificate and relay | Accepted risk for a home network: the user clicks through a warning either way. Mitigations: the fingerprint comparison, the optional CA install (Q3), and the LAN-only listener. Documented honestly on the Share page. |
| Other networks (the laptop on café Wi-Fi) | Reach the share port | Bound to one interface; connection filter; pause on network change; auto-off; device auth still required |
| A local malicious process on the computer | Read `share.json`, keys and the libraries | Out of scope: it can read the photos directly. Files are 0600 so that other local *accounts* cannot. |
| A future developer adding an admin route | Expose it to phones by accident | The separate allowlisted mux plus the golden-list test |

## Security and safety rules

1. The share listener serves **only** `shareRouteTable`. It has no
   catch-all into admin code, and a test fails when the table changes
   without the golden list.
2. The admin listener stays loopback-only. Sharing cannot start, and
   `/api/share/*` answers 403, unless the admin address and the request's
   peer are loopback.
3. Every share API request except `session` and `pair` needs a valid,
   unrevoked device. Verification compares SHA-256 hashes in constant
   time. Secrets are never stored or logged in clear.
4. Every library route checks the library against the shared set of the
   currently served root **and** its `LibraryID`, and then that the row
   or hash is **live**. Trashed items, other libraries, imports, jobs,
   settings and the file system are unreachable.
5. The only write is a single-hash rating (0–5) on a live row of a
   shared library. It is refused during a move.
6. Pairing tokens and codes are single use, expire in 2 minutes, live in
   memory only, travel in the URL fragment, and are rate-limited. A
   device credential is only ever created by a successful redeem.
7. TLS 1.2+, a leaf from a name-constrained local CA, keys 0600 in a
   0700 directory, and no HSTS.
8. Bind one LAN address. Accept only connections from that subnet or
   loopback. Pause when the network changes. picmover never touches the
   firewall.
9. Responses to phones contain no absolute paths, no import sources, no
   holder pids, and no raw errors (500 bodies stay generic).
10. Sharing is visible whenever it is on: the admin pill, the tray label,
    and `serve` output lines for pair, revoke and pause.

## Tests

### Go, `internal/share`

- **Config:** round trip, defaults, a damaged file (logged, kept as
  `share.json.damaged`, defaults with no devices and sharing off: fail
  closed, phones re-pair), modes 0600/0700, refusing a
  group-readable key, the one-sharing-process lock (two lockers, second
  refused with the holder).
- **Devices:** add/verify/revoke/rename. Verify rejects a wrong secret,
  a wrong id and a revoked device. `share.json` never contains the
  secret (a grep in the test). The device cap is 20. Expiry after 400
  days uses a fake clock.
- **Pairing:** a token redeems once; the second redeem is 410; expired
  at TTL+1 s (fake clock); concurrent redeems give exactly one winner;
  codes are case- and dash-insensitive; 5 wrong codes cancel all open
  pairings; at most 3 are open; tokens are gone after "restart" (a new
  registry).
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

The tests use the existing fixtures: a real library imported with
`testimg`, through `httptest` and a real TLS listener where the
connection matters.

- **Allowlist:** `shareRouteTable` patterns equal the golden list. For
  every admin route pattern (taken from a table that `routes()` is
  refactored to use), a request with a valid device cookie to the share
  handler gets 404. Unknown paths are 404, and static files outside the
  allowlist are 404.
- **Authentication:** every share API route without a cookie is 401;
  with a revoked cookie 401; with a malformed cookie 401. `session` and
  `pair` work without a cookie. The `Set-Cookie` attributes are checked
  exactly.
- **Authorization:** a non-shared library, an unknown library and a
  shared id with another `LibraryID` are all the same 404. Trashed media
  id → 404 for detail and file. A trashed hash → 404 for
  thumb/preview/large. Each refused filter key → 400. Rating with more
  than one hash or a non-live hash → 400/404.
- **Redaction:** the detail JSON keys are exactly the allowed set (the
  test fails on any extra key), and no JSON body contains the library
  root or `originalPath`.
- **Headers:** CSP, no HSTS, no-store on JSON and originals, CORP and
  COOP. Range on file → 206.
- **Middleware:** wrong Host → 421; foreign Origin → 403; POST without
  `X-Picmover` → 403; `Sec-Fetch-Site: cross-site` → 403; the 4 KiB body
  cap; 429 after the limits.
- **Connection filter:** a remote address outside the subnet is closed
  (a wrapped listener with an injected remote address).
- **Management:** `/api/share/*` returns 403 with a non-loopback admin
  address or peer. `serve --share --addr 0.0.0.0:…` is a usage error.
  Pairing returns 409 while sharing is off.
- **Lifecycle:** start, stop and restart the share listener while the
  admin one serves; a rebind on a same-network address change (fake
  netif); pause on a network change; auto-off timer (fake clock);
  `keepOn` honoured by the app path and ignored by `serve` without
  `--share`; shutdown order.
- **Concurrency:** rating during a move → 409; rebuild → 503; a closed
  shared library opened on demand and locked; a library locked by
  another process → 503; a workspace switch moves to the new shared set.

### `cmd`

Flags (`--share`, `--share-library`, `--share-port`, `--pair`),
messages, and the printed URL and fingerprint.

### Web (vitest, jsdom, fake server)

- The share fake (`testing/fakes.ts` gains `fakeShareServer`):
  - pairing with a token (the fragment is removed with `replaceState`),
    with a code, expired (410), used, rate-limited (429);
  - the "Not paired" screen on 401 from any request;
  - one library → redirect; libraries list; album list and album grid.
- Lightbox in share mode: no tag, album, trash or move controls are
  rendered; the rating sends one hash; a 409 shows the move message;
  Download links to `?download=1`; the video has `playsinline`; a video
  error shows the fallback.
- The rendition cap in share mode (pure function tests in
  `rendition.test.ts`).
- Admin Share page: toggle, library checkboxes, the pairing dialog with
  its countdown and expiry, the devices list with rename and revoke,
  `managedElsewhere` with disabled controls, the paused and error states
  with their messages, and the pill on every page.
- Build check: `scripts/check-share-apis.mjs` runs in `npm run build`
  and fails on unpolyfilled post-Gecko-91 APIs in share chunks.

### End to end (not in `make check`)

`make e2e-share`, a developer target that needs Playwright browsers.
This adds the first browser-driven tests to the repo, so it is open
question Q11.

- It starts `picmover serve <fixture> --share --share-library x` on
  loopback, issues a pairing through the admin API, and drives the
  share app with Playwright device profiles "iPhone 13" (WebKit),
  "Pixel 7" (Chromium) and a Firefox profile at 360×640 with touch
  enabled. The Firefox profile is a stand-in for Gecko, not ESR 91.
  It runs with `ignoreHTTPSErrors`.
- Flows: pair → grid → lightbox swipe → rate → download → album →
  video plays (Chromium) → revoke on the admin side → 401 screen.
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

## Phases

0. **Spike on real devices** (half a day, throwaway code). A Go
   program serves a self-signed HTTPS page with an image, a video (H.264
   MP4 and HEVC MOV), a `__Host-` cookie set via fetch, a fragment
   redirect test, and the production build of a tiny Svelte 5 page with
   the planned build target. Run checklist items 1–8 and 12 on all three
   phones. The results settle the **unverified** cells of the browser
   table and questions Q3, Q5 and Q9, and they are recorded in this
   document before Phase 1.
1. **Backend:** `internal/share` (config, devices, pairing, TLS, netif,
   rate limits), the share listener in `Running`, the allowlisted mux,
   the redacted handlers, live-row checks, the admin `/api/share/*`
   routes, and `serve --share`. Go tests.
2. **Admin UI:** the Share page and the pill. Then the tray item and
   `keepOn` in the app.
3. **Phone UI:** the share entry, shell, pairing, grid/lightbox share
   modes, the Gecko 91 build target and polyfills. Web tests.
4. **E2E target and the manual checklist on devices. README section.**

## Open questions (with recommendations)

1. **Q1. Per-person ratings?** Today a phone's rating overwrites the
   library rating, so two people who disagree overwrite each other.
   - *Recommendation:* keep the library rating, as the user decided. It
     needs no `user.db` schema change, and the admin filters keep
     working.
   - Log the device name on each phone rating, so a later per-person
     feature has a history to start from.
   - Revisit if a partner or children will rate. Per-person ratings
     would need a `ratings_by_device` table, a "whose ratings" filter,
     and a merge rule, which is a design of its own.
2. **Q2. Require approval on the computer for each pairing?** Today the
   first scan within 2 minutes wins, and the computer shows who paired.
   - *Recommendation:* no approval click by default (one fewer step,
     and the QR is shown only while the dialog is open). Offer a setting
     "Ask on this computer before a phone is paired" that holds the
     redeem until the user clicks Approve (60 s timeout).
   - Make it the default if the computer is often in a shared room.
3. **Q3. Offer "Install the certificate on this phone"?** This means
   downloading `ca.pem`, or a `.mobileconfig` on iOS, from the share
   listener, and then trusting it on the phone (iOS: Settings → General
   → About → Certificate Trust Settings).
   - It removes the warnings, survives IP changes for `.local` and
     reissued leaves, and probably fixes iOS video.
   - The cost: a CA key on the computer that the phone trusts. It is
     limited by name constraints, but whether the phone *enforces* those
     constraints is unverified.
   - *Recommendation:* build it as an advanced, opt-in step, after Phase
     0 shows that iOS (and Sailfish, if possible) enforce the name
     constraints. If a platform does not enforce them, do not offer the
     install there.
4. **Q4. Should sharing survive a restart?**
   - *Recommendation:* off after a restart by default. Add an app-only
     "Keep sharing on" setting (default off), always with the network
     check.
   - `serve` shares only with `--share`, so a script never exposes a
     library by surprise.
5. **Q5. iOS video over an untrusted certificate.**
   - *Recommendation:* decide after Phase 0. If video fails with the
     certificate exception, offer Q3's install as the fix and keep
     Download as the fallback.
   - Build the media capability URL (`?t=`) only if the spike shows that
     cookies, not the certificate, are the problem.
6. **Q6. Per-device library sets?** For example, a child's phone sees
   only one library.
   - *Recommendation:* not now: one shared set for all paired phones.
   - The device record can gain `libraries` later without changing the
     protocol.
7. **Q7. Read-only tags and camera filters on the phone?**
   - *Recommendation:* not in the first version. They add list
     endpoints and UI, and the user asked for browsing, albums, video,
     download and ratings.
   - Adding `GET tags` read-only later is safe under the allowlist.
8. **Q8. Send GPS coordinates to phones?**
   - *Recommendation:* yes, as text in the info sheet (the user's own
     phones, and the user's own photos).
   - No map, because the map needs internet tiles and a CSP exception.
   - A "Hide locations on phones" switch on the Share page is cheap if
     the user wants it.
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
14. **Q14. Should the phone see albums that contain only some shared
    items?** This cannot happen today, because albums are per library.
    Recorded so that a future cross-library album feature re-checks the
    live-row rule.
