# Step 4: management and multi-library

Contract for step 4 of the modernization: a workspace of libraries, user
data that survives rebuilds (ratings, tags, albums, trash), near-duplicate
review, and richer date filters. Written before implementation; the
implementers append "As implemented" sections, as in `server.md`.

## Goals

- **Workspace mode.** `picmover serve /media` where `/media` holds
  libraries as subfolders (`/media/peter`, `/media/penny`, ...). The UI
  starts with a page that lists the libraries, opens any of them, creates
  new ones, and records a plain folder tree in place (rebuild) so it
  becomes a library. Single-library `serve <library>` keeps working and
  uses the same code path with one library.
- **User data in `<library>/user.db`**, keyed by content hash. `media.db`
  stays a derived cache that `rebuild` may recreate at any time; `user.db`
  is never touched by rebuild and is small enough to back up trivially.
- **Ratings (0–5), tags, albums** with multi-select in the grid, a
  selection bar for bulk actions, editing in the lightbox, and filters.
- **Trash.** Deleting moves the file to `<library>/.picmover/trash/` and
  hides it; restore puts it back; emptying the trash is the only permanent
  deletion and needs an explicit confirmation. picmover never deletes
  originals otherwise.
- **Near-duplicates.** A job finds visually similar images from the pHash
  computed in step 2; a review page shows the groups and lets the user keep
  one and trash the rest, or ignore the group.
- **Date filters.** Exact day-precision ranges, and recurring ranges every
  year ("24–26 December every year", "June–August every year", "today in
  history").

## Non-goals (this step)

- Cross-library search and cross-library duplicate detection. The user
  wants this later: an identical photo may live in several libraries. The
  hash-keyed data model makes it a join across catalogs; see "Later".
- Multi-user, authentication, a Wails shell, pixel editing, manual album
  ordering, hierarchical tags, smart albums, faces (step 6).
- A migration framework for `media.db`. Step 4 needs **no catalog
  version bump**: the two derived tables it adds are created on open when
  absent. `user.db` is the one database whose schema will get real
  migrations in the future, because it cannot be regenerated; this step
  creates it at version 1.

## Terminology

Code and CLI say *library* (a folder with `media.db`); the user says
*catalogue*; the UI says **library** everywhere and shows the folder
path. A *workspace* is a folder whose subfolders are libraries.

## Toolchain

Unchanged: Go 1.27, `-tags nodynamic` on every build/vet/test, pure-Go
SQLite (`modernc.org/sqlite`, which includes the JSON1 functions used
below), Svelte 5 runes + TypeScript strict + Vite 7, exact pins, no new
dependencies on either side.

## Layout

```
internal/userdata/        user.db: schema v1-v3 and migrations, Open, ratings/tags/albums/trash/ignored, targets (v2), import history (v3)
internal/similar/         pHash near-duplicate search (pure functions) + Run (job body)
internal/library/trash.go TrashDir, TrashPath, MoveToTrash, Restore (rename-only, same volume)
internal/catalog/         Filter additions (hash sets, recur, trashed), similar_pairs + meta tables,
                          SetDeleted/ClearDeleted/Remove, ListTrashed
internal/server/workspace.go     library registry: discover, peek, open (lock + store + user.db), states
internal/server/api_libraries.go /api/libraries*, /api/l/{lib}/library
internal/server/api_user.go      ratings, tags, albums
internal/server/api_trash.go     trash, restore, delete
internal/server/api_similar.go   similar groups, ignore
internal/importer/               RecordInPlace also scans .picmover/trash (rows recorded as trashed)
cmd/serve.go                     mode detection (single | workspace | new), --workspace
cmd/rebuild.go, cmd/verify.go    trash-aware
web/src/pages/Libraries.svelte, Albums.svelte, Album.svelte, Trash.svelte, Similar.svelte
web/src/components/SelectionBar.svelte, RatingStars.svelte, TagInput.svelte,
                   AlbumPicker.svelte, DateFilter.svelte, LibrarySwitcher.svelte
web/src/lib/selection.svelte.ts, dates.ts, libraries.svelte.ts
```

## Data model

### `user.db` (schema version 1, `PRAGMA user_version = 1`)

Versions since: 2 (step 5, `docs/design/backups.md`: `backup_targets`,
`meta` with the library identity) and 3 (`docs/design/imports.md`:
`imports`, `import_items`, meta `imports_backfilled`). `Open` migrates
an older file in order (1 → 2 → 3) in one transaction after one
`user.db.bak-<stamp>` copy, re-checking the version inside it and setting
`user_version` last; a fresh file is created at the current version. A
newer version is refused as described below (`VersionError`, "upgrade
picmover"), with the file left untouched.

Lives at `<library>/user.db`, next to `media.db`. Created on first open of
the library by the server (an empty one is a few KiB). WAL, busy_timeout
10 s, `foreign_keys = ON`. A `user_version` other than 0 (fresh) or 1 is
refused with an error naming the file; never silently ignored.

```sql
CREATE TABLE ratings (
    hash       INTEGER PRIMARY KEY,
    rating     INTEGER NOT NULL CHECK (rating BETWEEN 1 AND 5),
    updated_at INTEGER NOT NULL
);
CREATE TABLE tags (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE COLLATE NOCASE,
    created_at INTEGER NOT NULL
);
CREATE TABLE media_tags (
    hash     INTEGER NOT NULL,
    tag_id   INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    added_at INTEGER NOT NULL,
    PRIMARY KEY (hash, tag_id)
);
CREATE INDEX media_tags_tag ON media_tags(tag_id);
CREATE TABLE albums (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    cover_hash INTEGER
);
CREATE TABLE album_items (
    album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    hash     INTEGER NOT NULL,
    position INTEGER NOT NULL,
    added_at INTEGER NOT NULL,
    PRIMARY KEY (album_id, hash)
);
CREATE INDEX album_items_hash ON album_items(hash);
CREATE TABLE trash (
    hash       INTEGER PRIMARY KEY,
    deleted_at INTEGER NOT NULL
);
CREATE TABLE similar_ignored (
    hash_a   INTEGER NOT NULL,   -- hash_a < hash_b as int64
    hash_b   INTEGER NOT NULL,
    added_at INTEGER NOT NULL,
    PRIMARY KEY (hash_a, hash_b)
);
```

Rules:
- Hashes are the catalog's content hash stored as int64 (same conversion
  as `media.hash`). Rating 0 means "no row"; setting 0 deletes the row.
- Tag names are trimmed, 1–64 runes, no control characters, unique
  case-insensitively (ASCII NOCASE, as the catalog). Album names: 1–128
  runes. `position` is `max(position)+1` on add; manual ordering is later.
- Rows are never deleted when a file is trashed or permanently deleted
  (except `trash` itself): if the same content is imported again, its
  rating, tags and album memberships come back. This is intentional.
- `internal/userdata` exposes typed functions, no SQL leaks to callers:
  `SetRating(ctx, hashes, rating)`, `Ratings(ctx, hashes) map`,
  `Tags(ctx) []Tag{ID, Name, Count}`, `CreateTag`, `RenameTag`,
  `DeleteTag`, `TagMedia(ctx, hashes, add, remove []int64)`,
  `TagsOf(ctx, hash)`, `HashesWithTag(ctx, tagID) []uint64`,
  `HashesWithRating(ctx, min int) []uint64`, `Albums`, `CreateAlbum`,
  `UpdateAlbum`, `DeleteAlbum`, `AlbumAdd/Remove`, `AlbumHashes`,
  `AlbumsOf(hash)`, `Trash(hashes, at)`, `Untrash(hashes)`,
  `TrashedAt(hashes) map`, `Ignore(pairs)`, `Ignored() set`. All writes
  are single transactions.

### `media.db` additions (derived; created on open if absent, no version bump)

```sql
CREATE TABLE IF NOT EXISTS similar_pairs (
    hash_a   INTEGER NOT NULL,   -- hash_a < hash_b as int64
    hash_b   INTEGER NOT NULL,
    distance INTEGER NOT NULL,
    PRIMARY KEY (hash_a, hash_b)
);
CREATE INDEX IF NOT EXISTS similar_pairs_b ON similar_pairs(hash_b);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
```

`meta` keys used: `similar_at` (Unix seconds), `similar_distance`,
`similar_items` (images considered). `media.rating` is no longer read or
written (it stays 0; drop it at the next version bump, together with
renaming `original_mtime`). `media.deleted_at` is set while the file is in
the trash and is the catalog's view of trash state; `user.db.trash` keeps
the authoritative timestamp so a rebuild restores it.

### Restricting catalog queries by a hash set

Rating, tag and album filters resolve to a hash set in `user.db` first;
the catalog then takes `Filter.Hashes []uint64` and adds
`hash IN (SELECT value FROM json_each(?))` with the set encoded as a JSON
array of int64. No `ATTACH`, so the catalog stays independent of
`user.db` and the connection pool needs no hooks. A 50k-hash set costs a
few milliseconds. `Filter.Hashes == nil` means no restriction; an empty
non-nil set matches nothing (the caller may short-circuit).

### Trash on disk

- `TrashDir() = <library>/.picmover/trash`; `TrashPath(rel) =
  TrashDir()/rel` (same relative path, so restore is unambiguous).
- `MoveToTrash(rel)`: `MkdirAll` the parent, `os.Rename`. A cross-device
  error (EXDEV / Windows ERROR_NOT_SAME_DEVICE) is returned as is: picmover
  never copies and deletes. The target must not exist (refuse, never
  overwrite). `Restore(rel)`: rename back; if the original path is now
  taken, `UniqueName` picks `name-2.ext` and the catalog row's `rel_path`
  is updated. Each op is reported per hash; a failure for one hash never
  stops the batch.
- The catalog row keeps its `rel_path` and gets `deleted_at`; `user.db`
  gets a `trash` row. Thumbnails and previews stay cached (hash-keyed), so
  the trash page shows pictures.
- Permanent delete (`trash/delete`): `os.Remove` the trash file, delete the
  catalog row, delete the `trash` row, `store.Delete(hash)` for the
  renditions. Ratings, tags and album memberships stay (see above). A
  missing trash file counts as already deleted (row cleanup still runs).
- `rebuild` (RecordInPlace) additionally scans `.picmover/trash/` and
  records those files with `deleted_at` = the `user.db` trash timestamp
  when present, else the file's mtime; `rel_path` is relative to the trash
  dir. The scanner treats the trash tree exactly like the library tree
  (same junk and extension rules). Trash files are never moved by rebuild
  or import.
- `verify` checks trashed rows at their trash path. `import` and `thumbs`
  skip trashed rows (already true via `deleted_at IS NULL`).
- Trash ops require the library lock, which `serve` holds.

### Similarity (`internal/similar`)

- Input: `(hash, phash)` for non-trashed images (`file_type` image or
  image_raw) with `phash != 0`. Output: pairs with Hamming distance ≤ `d`.
- Multi-index hashing: split the 64-bit pHash into 8 bands of 8 bits; any
  pair at distance ≤ 7 shares at least one band (pigeonhole). Build a map
  per band from band value to item indexes, compare candidates within each
  bucket with popcount, dedupe pairs. `d` is 1–7, default 6. A bucket
  larger than 5000 items is skipped with a warning (near-black or flat
  images collapse into the same bands; comparing them pairwise is
  pointless).
- Pairs are capped per item: each item keeps its 32 nearest partners (by
  distance, then partner hash), and a pair is stored when either item
  keeps it. A burst or time-lapse of n near-identical frames stores at
  most 32·n pairs instead of n(n−1)/2 (2000 identical frames: 63k instead
  of 2M) and stays one group, since every item keeps its nearest edge.
- Job kind `similar` (`{kind:"similar", distance?: 1..7}`): loads items,
  computes, replaces `similar_pairs` in one transaction, writes `meta`.
  Progress events: `{phase:"load", items}`, `{phase:"compare", bands}`,
  `{phase:"done", pairs}`. 136k items must finish in under 10 s.
- Groups (`GET .../similar`): union-find over `similar_pairs` minus
  `similar_ignored` minus pairs touching a trashed hash; groups of size ≥ 2;
  sorted by size desc, then by max distance asc, then by smallest hash.
  Computed per request (20k pairs → a few ms), paginated with
  `offset`/`limit` (default 50, max 200).
- "Ignore": `{group: hash}` inserts the stored pairs of the current
  group containing that hash into `similar_ignored`; `{hashes}` inserts
  the stored pairs among the given hashes (not every combination).
  "Keep": the client trashes the other hashes with the trash endpoint; no
  special server support.

### Date filters

All endpoints that accept filters (timeline, media, geo, geo/items) accept:

| Param | Meaning |
|---|---|
| `from`, `to` | unchanged: `YYYY-MM-DD` wall clock, `to` exclusive |
| `recur` | `MM-DD` or `MM-DD..MM-DD`, inclusive, every year; the range may wrap the year end (`12-30..01-02`); `02-29` allowed |
| `rating` | minimum rating 1–5 |
| `tag` | tag id; repeatable; all must match (AND) |
| `album` | album id |
| `trashed=1` | list trashed rows instead of live ones (other filters still apply) |
| `minpx` | longer edge `MAX(width, height)` ≥ this many pixels, 1–1 000 000; unknown dimensions never match (see `renditions.md`, "Dimension filter") |
| `maxpx` | longer edge ≤ this many pixels, 1–1 000 000; `minpx > maxpx` is 400 |

`recur` is evaluated as `strftime('%m-%d', taken_at, 'unixepoch')`
compared with the bounds (`BETWEEN` or `OR` of two ranges when wrapping),
inside the existing dated-range guard, so undated rows never match. It is
a scan of the `media_taken_at` index (136k rows in a few ms). Buckets with
`recur` return only the months that contain matches. Malformed values →
400 `{"error": "..."}` naming the parameter.

## HTTP API

Everything from `server.md` applies (JSON, hashes as 16 hex digits, times
as wall-clock strings, generic 500 bodies, 64 KiB body cap, POST requires
`Content-Type: application/json` and `X-Picmover: 1`, Sec-Fetch rules).
**All mutations are POST** (no PATCH/DELETE), so the one set of request
rules covers everything. Hash lists in bodies are capped at 2000 per call;
the client chunks larger selections.

### Workspace

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/libraries` | `{root, mode: "workspace"\|"single", libraries: [lib...], folders: [{name, path}]}` |
| POST | `/api/libraries` | `{name}` → 201 `lib`; creates `<root>/<name>` with an empty catalog (workspace mode only; 400 single mode) |
| POST | `/api/libraries/{lib}/open` | opens (lock, store, user.db) → 200 `lib`; 409 `{error, holder:{pid, cmd, since}}` when locked by another process; 503 while rebuilding |
| POST | `/api/libraries/{lib}/jobs` | same as `/api/l/{lib}/jobs` but also allowed for a plain folder or an unreadable catalog, for `kind: "rebuild"` only |

`lib` object: `{id, name, path, state, summary?, cover?, holder?, error?}`
with `state` one of `closed` (listed, summary from a read-only peek, not
yet opened), `open`, `busy` (locked by another process; `holder` set),
`rebuilding` (a rebuild job is running), `error` (`media.db` unreadable
or needs rebuild; `error` text set), `folder` (no `media.db`; only in
`folders`). `summary` is the `/api/library` summary (counts, earliest,
latest) when readable; `cover` is the hash of the newest item with a
thumbnail, when any. `id` is the folder name, percent-encoded in URLs.

Ids are validated on every request: one path component, not `.`/`..`,
no `/`, `\`, `:` or NUL, not starting with `.`, at most 255 bytes; the
Windows reserved device names (`CON`, `PRN`, `AUX`, `NUL`, `COM1`–`COM9`,
`LPT1`–`LPT9`, case-insensitively, with or without an extension) are
refused on all platforms for creation. The id must be in the current
directory listing (exact byte match) or the answer is 404.

Discovery rescans `<root>` on every `GET /api/libraries` (one `ReadDir`);
a subfolder is a library when it has a regular `media.db`; everything else
that is a directory (not `.picmover`, not hidden) is a `folder`. Peeks open
`media.db` read-only without the lock. A successful peek is cached by
the size and mtime of `media.db`, `media.db-wal` and `user.db` until one
of them changes; errors are not cached. In single mode the listing has exactly one
library, `state: "open"` from startup, id = base name of the root folder
(`"library"` when that is empty, e.g. a drive root).

Opening takes the library lock for the server's lifetime (like `serve`
today), opens the thumbs store read-only (creating an empty one when
absent) and `user.db`. Libraries stay open until shutdown; shutdown
releases all locks after cancelling the running job.

### Library-scoped endpoints

Every endpoint from step 3 moves under `/api/l/{lib}/`:
`library`, `timeline`, `media`, `media/{id}`, `thumb/{hash}`,
`preview/{hash}`, `file/{id}`, `geo`, `geo/items`, `jobs` (list and start).
`/api/fs`, `/api/jobs/{id}`, `/api/jobs/{id}/events`,
`/api/jobs/{id}/cancel` and `GET /api/jobs` (all libraries) stay global.
The old unprefixed paths are gone (404); the step 3 UI is the only client.
A request for a library that is not open is answered 409
`{"error": "library is not open", "state": ...}` (the client opens it
first); `rebuilding` → 503.

`/api/l/{lib}/library` gains `userdb: {ratings, tags, albums, trashed,
ignoredPairs}` counts and `similar: {at, distance, items, pairs}` (nulls
before the first run).

Item JSON gains `rating` (omitted when 0) and, in trash listings,
`deletedAt` (wall-clock string). Detail JSON gains `rating`,
`tags: [{id, name}]`, `albums: [{id, name}]`, `deletedAt` (null when
live). `/api/l/{lib}/file/{id}` for a trashed row serves from the trash
path with the same shape, containment and symlink checks applied against
`TrashDir()`.

### Ratings, tags, albums

| Method | Path | Body → Result |
|---|---|---|
| POST | `/api/l/{lib}/ratings` | `{hashes: [...], rating: 0..5}` → `{updated}` |
| GET | `/api/l/{lib}/tags` | `{tags: [{id, name, count}]}` sorted by name |
| POST | `/api/l/{lib}/tags` | `{name}` → 201 tag, or 200 the existing tag (case-insensitive) |
| POST | `/api/l/{lib}/tags/{tid}` | `{name}` rename → tag; 409 when the name is taken |
| POST | `/api/l/{lib}/tags/{tid}/delete` | → `{removed: n}` memberships removed |
| POST | `/api/l/{lib}/media/tags` | `{hashes: [...], add: [tid...], remove: [tid...], create: ["name", ...]}` → `{tags: [...]}` (the tags added, including created ones) |
| GET | `/api/l/{lib}/albums` | `{albums: [{id, name, count, cover, createdAt, updatedAt}]}` by `updatedAt` desc |
| POST | `/api/l/{lib}/albums` | `{name, hashes?: [...]}` → 201 album |
| POST | `/api/l/{lib}/albums/{aid}` | `{name?, cover?: hash\|null}` → album |
| POST | `/api/l/{lib}/albums/{aid}/delete` | → `{}` (memberships go with it; files untouched) |
| POST | `/api/l/{lib}/albums/{aid}/items` | `{add: [...], remove: [...]}` → `{count}` |

Album contents are read with the ordinary `timeline?album=` and
`media?album=` (plus `geo` for an album on the map). `cover` defaults to
the newest item in the album when `cover_hash` is null or no longer in the
album.

### Trash

| Method | Path | Body → Result |
|---|---|---|
| POST | `/api/l/{lib}/trash` | `{hashes: [...]}` → `{trashed, failed: [{hash, error}]}` |
| POST | `/api/l/{lib}/trash/restore` | `{hashes: [...]}` → `{restored, failed: [...]}` |
| POST | `/api/l/{lib}/trash/delete` | `{hashes: [...]}` or `{all: true}`, and `confirm: true` → `{deleted, failed: [...]}`; 400 without `confirm` |

Trashing a hash that is already trashed, or restoring one that is live, is
reported in `failed` with a clear message, not an error status. `all: true`
deletes every trashed row. Contents are listed with `trashed=1`.

### Similar

| Method | Path | Result |
|---|---|---|
| GET | `/api/l/{lib}/similar?offset=&limit=` | `{groups: [{items: [item + {size, width, height, relPath}], maxDistance}], total, at, distance}`; `at` null before the first run |
| POST | `/api/l/{lib}/similar/ignore` | `{group: hash}` or `{hashes: [...]}` → `{ignored: pairs}`; 404 when the group hash is in no group |

### Jobs

The job object gains `library` (id). Kinds accepted by
`POST /api/l/{lib}/jobs` (and `/api/libraries/{lib}/jobs`): `import`,
`thumbs` (as today), `similar` (`distance?`), `rebuild` (`noThumbs?`).
One job runs at a time across the whole workspace (409 as today); the
queue stays "one at a time, the UI retries". A `rebuild` job closes the
library (catalog, store; `user.db` stays open), runs the CLI rebuild
logic (backup when `media.db` exists, record in place, trash scan, then
thumbs unless `noThumbs`), and reopens it; requests meanwhile get 503. A
rebuild of a plain `folder` turns it into a library; the listing shows it
as `rebuilding`, then `open`.

## `picmover serve`

Mode detection for `serve <dir>`:
1. `<dir>/media.db` exists → **single** (as today, including the lock and
   the unreadable-catalog hint).
2. Else, `<dir>` has at least one subdirectory with a regular `media.db`,
   or `--workspace` was given → **workspace**: no lock on `<dir>` itself;
   libraries lock when opened. Prints `Serving workspace <dir> (N
   libraries) at <url>`.
3. Else, `<dir>` is absent or empty (or only `.picmover`) → **single, new**
   library as today (`(new)`).
4. Else → the existing refusal, whose hint now also says that a folder of
   libraries is served as a workspace and that `--workspace` starts an
   empty one.

`--workspace` on a folder that has `media.db` is a usage error. Everything
else (`--addr`, `--no-open`, `--cors-origin`, `--verbose`, port fallback,
graceful shutdown) is unchanged.

## Security

- Library ids and new library names validated as above; every path is
  built from the validated id joined to the workspace root and re-checked
  with `Contains`; a symlinked subfolder is allowed only when it resolves
  to a plain folder directly under the root (no leading dot, no
  `media.db` or `.picmover`), otherwise it is skipped from the listing and
  404 on access; no library is ever created through a link.
- Trash, restore and permanent deletion resolve links like the file
  endpoint and only move or delete regular files inside the library or
  its trash; renames never replace a file.
- An import source may not be, contain or lie inside a library of the
  workspace, or be the workspace root (400).
- Error texts sent to clients name no absolute paths and carry no raw OS
  errors; the detail goes to the server log.
- Trash moves and restores operate only on catalog rows, never on request
  paths; permanent deletion only on rows with `deleted_at` set, only with
  `confirm: true`. Nothing in this step deletes a live file.
- The one-job-at-a-time rule and the library lock make rebuild safe
  against concurrent imports; the lock is held across the backup rename.
- Mutation bodies: hash lists ≤ 2000, names length-checked, ids numeric;
  unknown fields ignored; 400 for malformed JSON with a generic message.

## Frontend

### Routes

```
#/                         Libraries page (workspace) — single mode redirects to #/l/<id>/
#/l/<id>/                  grid (with filters in the query, as today)
#/l/<id>/map, /import, /library
#/l/<id>/albums            albums list
#/l/<id>/albums/<aid>      one album (grid in album mode)
#/l/<id>/trash             trash (grid in trash mode)
#/l/<id>/similar           near-duplicate review
#/l/<id>/view/<mid>        lightbox over the current page (as today)
#/jobs                     all jobs (optional; the pill still shows the running one)
```

`api.ts` gets `libApi(id)` returning the scoped client; pages take the id
from the route. Navigating to a library that is `closed` calls `open`
first (a one-line "Opening peter…" state); `busy` shows the holder and a
Retry button; `error` offers Rebuild.

### Shell

The library chip becomes a **switcher**: the current library's name, the
full path as title, a menu listing the other libraries (name + count) and
"All libraries" (the picker). In single mode the menu has only the path.
The nav gains Albums, Trash (with the count when non-zero) and
Duplicates. The jobs pill shows the library name of the running job.

### Libraries page (`#/`)

A card per library: cover thumbnail, name, path, counts (photos, videos,
date span), state. Actions: Open (the card itself), New library (dialog:
name; creates and opens), Record files in place (on `folder` entries:
starts a `rebuild` job with progress, then opens), Rebuild (on `error`),
Retry (on `busy`, with the holder shown: "in use by picmover import since
…"). Keyboard accessible: cards are links; dialogs trap focus and close
on Escape. The root path is shown in the page header.

### Selection and the selection bar

- Tiles get a checkbox that appears on hover and stays visible while any
  selection exists. Clicking the checkbox, or Ctrl/Cmd-clicking a tile,
  toggles; Shift-click selects the range from the last toggled tile in
  display order (within loaded months; unloaded gaps are skipped and the
  bar says so). Escape clears. Selection is a `Set<hash>` in
  `selection.svelte.ts`, cleared on library or filter change, kept across
  scrolling and the lightbox.
- The bar (sticky bottom) shows "N selected", Rate (five stars, click
  sets, click the current value clears), Tag (TagInput with autocomplete
  and create), Album (AlbumPicker: existing or new), Trash, Clear. In an
  album page: Remove from album. In the trash page: Restore, Delete
  permanently (confirm). All actions chunk by 1000 hashes and refresh the
  affected items in place (ratings on tiles, counts in the nav).

### Lightbox

Keys: `0`–`5` rate, `t` focus the tag input, `b` add to album, Delete or
Backspace trash with an 8 s Undo toast (restore), in the trash page
`r` restore. The info panel gains Rating (stars), Tags (chips with ×,
input with autocomplete and create on Enter), Albums (chips, add), and in
trash "Deleted <when>" with Restore and Delete permanently. Arrow
navigation and the rest are unchanged.

### Filters

The filter bar gains Rating (≥ 1…5 stars), Tags (multi-select chips, AND),
Album (select), and **Dates** with two modes:
- Range: two date inputs (day precision), either side open.
- Every year: month/day from and to, with presets Today, Christmas
  (12-24..12-26), New Year (12-31..01-01), Midsummer (06-19..06-26),
  Summer (06-01..08-31), This month. The year range inputs stay available
  to limit the years.
Everything is reflected in the URL query (`from`, `to`, `recur`,
`rating`, `tag`, `album`) so views are linkable; `filtersKey` includes all
of them. The timeline scrubber reflects the filtered buckets as now.

### Albums pages

`#/l/<id>/albums`: cards (cover, name, count), New album, rename/delete in a
card menu. `#/l/<id>/albums/<aid>`: the grid with `album` filter, header
with the name, count, "Set cover from selection", and the selection bar's
Remove action.

### Trash page

The grid with `trashed=1`, tiles show "deleted <relative time>", the
selection bar has Restore and Delete permanently, and an Empty trash
button that asks the user to type the number of files to confirm. The
lightbox works here (file served from the trash path).

### Duplicates page

Header: "Last run <when> at distance d, N groups" with Run (distance
select 1–7) that starts the `similar` job and shows progress. List of
groups; each group is a row of items (thumbnail, dimensions, size, date,
path, rating) with a Keep radio per item and buttons "Trash the others"
and "Ignore group". Click a thumbnail to open the lightbox; keyboard: the
row is focusable, arrows move the Keep radio, Enter applies. Paginated.

### Client code rules

As in `server.md`: no UI/state/router libraries, exact pins,
`svelte-check --fail-on-warnings`, vitest with jsdom for logic
(selection ranges, date filter parsing and presets, route prefixing,
libraries state machine), no `any`, wall-clock date handling via
`format.ts`.

## Tests (Go)

- `userdata`: schema and version refusal; rating set/clear; tag NOCASE
  uniqueness, rename conflict, delete cascades; album items position and
  cover fallback; trash rows; ignored pairs normalized `a < b`.
- `catalog`: `Hashes` restriction with 0, 1, 50k hashes (json_each);
  `recur` plain and wrapping, undated excluded, with `from`/`to`;
  `trashed` listing; `similar_pairs` replace; `meta`; `SetDeleted`/
  `ClearDeleted`/`Remove`.
- `similar`: pairs equal brute force on 3000 random hashes for d = 1..7;
  phash 0 skipped; bucket cap; symmetry and dedupe.
- `library`: trash move/restore round trip; refuse overwrite; restore into
  a taken name picks `-2`; cross-device error returned verbatim (injected).
- `importer`: RecordInPlace records the trash tree as deleted rows with
  the `user.db` timestamp, else mtime.
- `server`: workspace discovery (libraries, folders, hidden and symlinked
  entries), peek caching, open/busy/error states, create (validation, 409
  on existing, reserved names), id validation on every scoped route,
  every mutation endpoint including header rules, chunk cap, trash file
  serving, permanent delete needs `confirm`, similar groups and ignore,
  rebuild job closes and reopens, single-mode listing and redirect.
- `cmd`: serve mode detection (four cases, `--workspace`), rebuild records
  trash, verify checks the trash path.

## Performance targets (136k rows, dev box)

- Ratings for a 5k-item month: < 10 ms extra. Tag/album/rating filtered
  timeline: < 150 ms. `recur` timeline: < 100 ms.
- `similar` job: < 10 s; groups page: < 50 ms for 20k pairs.
- `GET /api/libraries` with 10 libraries: < 100 ms warm.
- Trash/restore of 1000 files: bounded by the file system; progress is
  per call, so the client chunks by 1000 and shows a count.

## Later (recorded so the model allows it)

- **Cross-library search and dedup.** Because every user table and the
  similarity tables are keyed by content hash, a workspace-level view is a
  union over the open libraries' catalogs: "this photo is also in penny and
  peter", a search across all libraries, and near-duplicates across
  libraries by concatenating their `(hash, phash)` lists. No schema change
  is needed for it; it is a server feature plus UI.
- Manual album ordering (`position` is already there), tag hierarchies,
  smart albums (saved filters), moving items between libraries.

## As implemented (backend)

Everything outside `web/`. Notes that matter to the client are marked
**(frontend)**. Where this section is silent, the contract holds.

### API shapes, exactly

- **(frontend)** `GET /api/libraries` → `{root, mode, libraries: [lib],
  folders: [{id, name, path, state: "folder"}]}`, both lists sorted by id
  (byte order). `folders` gained `id` (= name) and `state`. A folder that
  is being rebuilt is listed under `libraries` with `state: "rebuilding"`
  and no summary; once it has a `media.db` it is a library.
- **(frontend)** `lib` = `{id, name, path, state, summary?, cover?,
  holder?, error?}`; `name` = `id`; `path` is the folder in the listing,
  the resolved root once opened; `summary` is the `/library` summary
  object; `cover` a hash. `holder` = `{pid, cmd, since}` with `since` a
  wall-clock string in the server's zone (or `null`), `pid` 0 and `cmd`
  `""` when unknown. A closed library whose catalog cannot be peeked is
  listed as `error` with the reason. `busy` is known only after an open
  attempt (the listing does not probe locks); it stays until the next
  open succeeds.
- **(frontend)** `GET /api/libraries/{lib}/cover` (added at the client's
  request): the 256 px JPEG of the library's cover item for a library in
  any readable state, closed included, read through the cached peek and
  a read-only thumbnail store without the lock. `ETag:
  "<hash>-<FormatVersion>-<thumbAt or 0>"`, `Cache-Control: private,
  max-age=3600`, `If-None-Match` → 304. 404 when the id is unknown or
  invalid, for a folder, an unreadable catalog, no cover or no thumbnail.
- **(frontend)** `POST /api/libraries {name}` → 201 `lib` (state `open`).
  400 in single mode, for malformed JSON and for an invalid name: the id
  rules plus no `< > " | ? *`, control characters, trailing dot or space,
  or Windows device name (`CON`, `nul.txt`, `Com1`, `LPT9.jpg`; `COM10`
  and `console` are fine). 409 when a file or folder of that name exists.
- **(frontend)** `POST /api/libraries/{lib}/open` → 200 `lib`; 409
  `{error, state: "busy", holder}`; 409 `{error, state: "error"}`
  (unreadable or needs-rebuild catalog; `error` says which); 409 `{error,
  state: "folder"}` for a folder; 503 `{error, state: "rebuilding"}`.
  Opening an open library is 200 again.
- **(frontend)** Scoped routes `/api/l/{lib}/...`: 404 `{"error": "no such
  library"}` for a malformed or unknown id; 409 `{"error": "library is not
  open", "state": "closed"|"busy"|"error"|"folder"}`; 503 `{"error": "the
  library is being rebuilt", "state": "rebuilding"}`. Only these library
  state answers (and the open/jobs ones above) carry `state`; every other
  409 (job running, tag name taken) is a plain `{error}`. After shutdown
  every library route is 503 `{"error": "the server is shutting down"}`.
  A literal `..` segment is cleaned by Go's mux (307 to the unprefixed
  path, which is 404); `%2e%2e`, `%2F`, `%5C`, `:` and NUL reach the id
  check and are 404.
- **(frontend)** `/api/l/{lib}/library` adds `id`, `userdb: {ratings,
  tags, albums, trashed, ignoredPairs}` and `similar: {at, distance,
  items, pairs}`, all `null` before the first run; `at` is **Unix seconds**
  (a number, as `thumbAt`), not a string.
- **(frontend)** Items: `rating` (omitted when 0) and `deletedAt`
  (wall-clock string in the server's zone, `2024-07-14T13:05:22`, only on
  trashed rows). Detail: `rating` (0 when unrated), `tags: [{id, name}]`,
  `albums: [{id, name}]` (both sorted by name), `deletedAt` (`null` when
  live). **The detail and `/file` of a trashed row are now 200** (step 3
  answered 404 for deleted rows): the file comes from the trash path.
- **(frontend)** Filters: `recur` is `MM-DD` or `MM-DD..MM-DD`; `tag`
  repeatable, at most 32; `rating` 1–5; `album` and `tag` positive ids;
  `trashed` `1` (or `0`/empty). Each malformed value is 400 naming the
  parameter (`invalid recur "13-01": ...`). An unknown tag or album id is
  not an error: it matches nothing.
- **(frontend)** `POST ratings {hashes, rating}`: `rating` is required
  (0 clears); `updated` counts the hashes whose stored rating changed.
- **(frontend)** Tags: `GET tags` → `{tags: [{id, name, count}]}` sorted
  case-insensitively. `POST tags` → 201 or 200 `{id, name, count}`. Rename
  → the tag; 404 unknown id; 409 `{error}` taken (a case-only rename of
  the same tag is allowed). `POST tags/{tid}/delete` → `{removed}`.
  `POST media/tags` → `{tags: [{id, name, count}]}`: every tag added, the
  `add` ids first then the `create` names (created or found
  case-insensitively), without duplicates, with their new counts; one
  transaction; 404 when an `add`/`remove` id does not exist (nothing
  changes). At most 100 ids per list and 100 names.
- **(frontend)** Albums: `{id, name, count, cover, createdAt, updatedAt}`
  with `cover` a hash or `null` (empty album) and the times RFC 3339 in
  the server's zone. Without an explicit cover that is still a member,
  `cover` is the newest live member by capture time, else the member
  added last. `POST albums/{aid}` takes `name` and/or `cover` (absent:
  unchanged, `null`: cleared, a hash: set; a non-member hash is stored but
  only shown once it is a member); it bumps `updatedAt`, as do item
  changes. `albums/{aid}/items`: `add` and `remove` together at most 2000;
  adds first, members keep their position. 404 for an unknown album.
- **(frontend)** Trash: `failed` entries are `{hash, error}` with texts
  such as `not in the catalog`, `already in the trash`, `not in the
  trash`, `the file is missing`, `the trash already holds a different file
  at this path`, `the file is on another volume ...`. `trash/delete` needs
  `confirm: true` and exactly one of a non-empty `hashes` or `all: true`
  (400 otherwise); live rows are reported in `failed`.
- **(frontend)** `GET similar` → `{groups: [{items, maxDistance}], total,
  at, distance}` with `at` Unix seconds or `null`. Items are the item
  object plus `size, width, height, relPath`, ordered by pixel count,
  then file size (largest first), then hash, so the first is the natural
  "keep". `offset` 0–2^30 and `limit` 1–200 (default 50); other values
  400. Groups use only pairs whose rows both exist and are live, minus
  ignored pairs; an offset past the end gives `groups: []` with the
  `total`.
- **(frontend)** `POST similar/ignore` takes `{group: hash}` (the whole
  current group, any member's hash; 404 when it is in no group) or
  `{hashes}` with **2–200 hashes**, exactly one of them (400 otherwise);
  only the stored pairs are ignored (see "Review fixes"), and it returns
  `{ignored}` = new pairs.
- **(frontend)** Jobs: the job object always has `library` (the id),
  also in `GET /api/jobs` and SSE replays. `similar` jobs add `distance`
  (the effective one, default 6) and `progress: {phase, items, bands,
  pairs}` once running; their SSE `progress` events carry that object at
  the top level (`{"phase":"compare","items":136000,"bands":3,"pairs":0}`;
  all four fields always present). `rebuild` jobs show `noThumbs` and the
  importer stats (`existing` = files recorded) and send `progress`/`result`
  events like an import. Exit codes: 0, 2 (per-file errors or thumbnail
  failures, as `import`), 130 cancelled, 1 failed. Logs:
  `.picmover/logs/similar_<time>.log`, `rebuild_<time>.log`.
  `GET /api/l/{lib}/jobs` lists that library's jobs; `GET /api/jobs` all.
  Job kinds other than these four are 400.
- `POST /api/libraries/{lib}/jobs`: an open library takes every kind; a
  folder or an unreadable catalog (state `error`, or a `closed` one whose
  peek fails) only `rebuild`, else 409 `{error, state}`; `closed`/`busy`
  409 `{error, state}`; `rebuilding` 503.

### Deviations and additions

- **Restore into a taken path** uses `library.UniqueName`, the importer's
  rule: `name_1.ext`, `name_2.ext`, ... (the contract said `name-2.ext`).
  Taken means present on disk or recorded in the catalog.
- **Open libraries are not re-listed per request**: a scoped request on
  an open library uses the registry; the directory listing is read for
  ids that are not open, for `/api/libraries*` and for opens.
- **`server.Options`**: `Workspace` (folder of libraries; excludes
  `Library/Catalog/Store`) and `UserData` (single mode; nil opens
  `<library>/user.db`). In single mode the server now owns the handles:
  `Shutdown` cancels the job and then closes every library (catalog,
  store, user.db) and releases the locks it took; closing them again is
  harmless. `(*Server).Mode()`.
- **Rebuild job**: waits up to 10 s for in-flight requests on the library
  before closing its catalog and store (a long file stream holds no
  catalog handle, so it is not cut). On a failure before the new catalog
  exists (backup refused, ...) the old catalog is reopened; when it cannot
  be, the library becomes `error`. A folder or unreadable library is
  attached and locked at the start and stays locked and open afterwards.
  Times stored as instants use the server's zone (the CLI default).
- **user.db API** refinements: `SetRating` returns the number changed;
  `TagMedia(ctx, hashes, add, remove, create)` also creates names, in the
  same transaction, and returns the tags added; `HashesWithTags` (AND)
  next to `HashesWithTag`; `AlbumEdit(id, add, remove)` with
  `AlbumAdd/AlbumRemove` as wrappers; `Album`, `UpdateAlbum(id,
  AlbumUpdate{Name, Cover, ClearCover})`; `TrashedAt(nil)` returns every
  row (rebuild); `Rating`, `Counts`, exported `CleanName`, `NewPair`,
  errors `ErrNotFound`, `ErrNameTaken`, `ErrInvalidName`,
  `ErrInvalidRating`. Tag and album names also refuse U+2028/2029 and
  U+FFFD. `userdata.Album.Cover` falls back to the member added last; the
  server applies the "newest by capture time" rule on top (the catalog
  knows capture times).
- **Catalog**: `Filter.Recur *Recur{From, To}` and `ValidMonthDay`;
  `GetByHash`, `ByHashes`, `NewestOf`, `Cover`, `PHashItems`,
  `SimilarPairs` (live rows only), `CountSimilarPairs`,
  `ReplaceSimilarPairs(pairs, meta)` (one transaction), `Meta`, `SetMeta`,
  `PeekFile` (read-only `mode=ro`, no lock, refuses another schema with
  `ErrNeedsRebuild`). `ClearDeleted(hash, rel)` sets the restored path in
  the same statement; `Remove` also drops the hash's similar pairs. The
  derived tables are created by `Open` and `OpenExisting`.
- **Library**: `MoveToTrashAs` (undoing a restore that renamed),
  `IsCrossDevice`, `BackupCatalog`/`CatalogFileSuffixes` moved from `cmd`
  (the rebuild job shares them), `UserDBName`; `user.db` and its
  `-wal/-shm/-journal` are internal (`IsInternal`), like `media.db`.
- **Importer**: `Options.TrashTimes`. The trash is walked as a second
  pipeline after the library tree, so a trash file identical to a live
  one is a duplicate (the live row wins) and a trash file whose path is
  recorded live is an error result. Result message `in trash`.
- **`serve`**: `Use` is `serve <library-or-workspace>`. `--workspace` on a
  folder with `media.db` is a usage error; on an absent folder it creates
  it. The banner counts the subfolders with a regular `media.db`
  (`(1 library)`, `(N libraries)`). The refusal hint adds `picmover serve
  --workspace <dir>`. `rebuild` reads trash times from an existing
  `user.db` (it never creates one); `verify` checks trashed rows at their
  trash path and counts a file left at a trashed row's library path as an
  orphan.
- Trash operations are serialised per library and never cancelled half
  way by a client disconnect (a moved file is always recorded). If the
  catalog update fails after a move, the move is undone.

### Tests

`internal/userdata` (schema, reopen, version 2 and foreign-file refusal
naming the file; ratings incl. a negative int64 hash; tag NOCASE
uniqueness, 64-rune limit, control characters, rename conflict and case
rename, delete cascade; album positions, explicit/fallback cover, delete
cascade, unknown ids; trash rows; ignored pairs normalised as int64;
counts). `internal/catalog/manage_test.go` (`Hashes` nil/empty/one/
trashed/50k via json_each, on buckets, items and geo; `recur` plain, one
day, wrapping, 02-29, whole year, with `from`/`to` and with `Hashes`,
bucket months, invalid bounds; trashed listing, set/clear/rel path,
remove with pairs; similar pairs replace, live-only read, int64
normalisation; meta; derived tables recreated by both opens without a
version change; `PeekFile`, cover, `PHashItems`). `internal/library`
(trash round trip, refuse overwrite, restore into a taken name, injected
EXDEV returned verbatim; `IsInternal` cases for the trash and user.db).
`internal/similar` (equality with brute force on 3000 clustered hashes for
d = 1..7, symmetry and dedupe, order independence, phash 0, bucket cap
with a pair still found through another band, groups and ignore, `Run`
with meta, progress and cancellation; a 136k benchmark: 80 ms on random
clustered data). `internal/importer` (`TestRecordInPlaceTrash`).
`internal/server` (`management_test.go`, `management_review_test.go`,
`workspace_test.go`: ratings/tags/albums incl. 400s, the 2000 cap, unknown
fields, 409 without `state`, header and origin rules on every new POST;
new filter parameters on all four endpoints; the trash flow incl. serving
from the trash, restore into a taken name, `confirm`, `all`, renditions
deleted and ratings kept; trash serving refuses symlinks and escaping
paths, deletion removes a link not its target; the similar job, groups,
pagination, trash and ignore; the rebuild job's 503 window and reopen
with trash and ratings kept; workspace discovery with hidden, symlinked
inside/outside entries and files; cover thumbnails; id validation on
scoped, open and jobs routes; closed/open/busy (holder)/error/folder;
locks released at shutdown; create with reserved names and 409; peek
cache by size, mtime and TTL; rebuilding a folder and an unreadable
catalog; job lists with `library`; single-mode listing and the removed
step 3 paths; the similar progress event shape). Existing server tests
moved to `/api/l/lib/...`. `cmd` (`serve_mode_test.go`: the four modes,
`--workspace` cases, refusal hint, a served workspace with per-library
locking; `trash_test.go`: rebuild records the trash with user.db times
and keeps ratings, verify checks the trash path).

Not measured: the step 4 performance targets other than the similarity
search (no 136k-row benchmarks were added for the rating/tag/album
filters, `recur` timelines or the groups page). The pre-existing
`TestThumbsCancellationLeavesPending` in `internal/importer` is timing
dependent and fails now and then on the base commit too.

### Review fixes (security and file safety)

From the step 4 security and data-integrity reviews. These override the
notes above where they differ.

- **Locks.** An entry tracks the lock it holds and reuses it: an open or
  rebuild never takes a lock this process already holds (in single mode
  the lock is `cmd/serve`'s, recorded as held). A rebuild that ends
  without a catalog releases the lock and `user.db` (workspace mode), so
  a retried rebuild or open works.
- **Replaced folders.** Opening records the lock file's identity. Before
  trash, restore, delete and job start, the lock file at the library's
  path must still be that file (`os.SameFile`); otherwise the answer is
  409 `{error: "the library folder was replaced or moved while it was
  open; open it again", state: "error"}`. The handles close once no
  request uses them, and the next open locks the new folder. Creating a
  library whose old folder was deleted while open gets a new entry (409
  while that name is being rebuilt).
- **Trash, restore, delete** resolve the path like `/file`: the parent is
  resolved with `EvalSymlinks`, and the result must stay inside the
  library and not be internal (live rows) or inside the trash (trashed
  rows), for the source and for the target directory. The last component
  must be a regular file. Otherwise the hash fails with `the file's
  catalog path is not a library path` or `the path is a folder or a link,
  not a file`. A link in the trash is no longer deleted (step 4 removed
  the link itself). Renames never replace: `library.RenameNoReplace`
  (`renameat2` `RENAME_NOREPLACE`, `renamex_np` `RENAME_EXCL`, link and
  unlink on other Unix systems, `MoveFileEx` without
  `REPLACE_EXISTING`). Only a file system with neither the flag nor hard
  links falls back to check-then-rename. Restore retries the next free
  name when one is taken in between. The importer's moves and copies use
  the same rename.
- **Rebuild vs trash.** A rebuild holds the library's trash lock from
  start to end. It waits for a running trash, restore or delete batch,
  and none starts while it runs. The 10 s drain still applies to reads.
- **Import sources.** An import job's source is refused (400) when it is
  a library of the workspace (a listed folder with `media.db` or
  `.picmover`), lies inside one, or contains one. This includes the target
  library, the workspace root and any folder containing the root. In
  single mode the library is refused the same way. The importer skips
  every `.picmover` directory and every catalog or `user.db` file
  (`media.db`, `user.db`, their `-wal/-shm/-journal`, catalog backups)
  anywhere in a source, case-insensitively.
- **Symlinked workspace entries** are listed only when they resolve to a
  folder directly under the root whose name has no leading dot and that
  has no `media.db` or `.picmover`, so a link is never a second name for
  a library or a way into one. A rebuild through a link is refused: a
  library is never created through a link.
- **No paths or OS text to clients.** Trash failures use fixed texts
  (`permission denied`, `the library is on a read-only file system`, `no
  space left on the device`, `the file is in use by another program`,
  else `the file could not be moved to the trash` / `restored` /
  `deleted`). Library `error` texts (listing, open, jobs) are short
  reasons: `the catalog needs a rebuild: ...`, `the catalog was written by
  a newer picmover: upgrade picmover`, `media.db is not a picmover
  catalog`, `media.db is not a regular file`, `user.db has schema version
  N; this picmover supports 1: upgrade picmover`, `the folder is
  read-only (...)`, else `the catalog could not be read (see the server
  log)`. Job error messages say `see the job log` instead of naming the
  log path (the job object's `logPath` is unchanged), and an import
  source that cannot be stat'ed is `source: the path does not exist` /
  `permission denied` / `cannot be read`. Details go to the server log.
  Per-file `result` events are unchanged.
- **Peek cache.** A successful peek is cached by the size and mtime of
  `media.db`, `media.db-wal` and `user.db`, with no expiry. Every listing
  re-stats the files and peeks again only when one of them changed. An
  error is never cached. The peeks a listing needs run concurrently, at
  most 4 at a time (warm listing of 10 libraries: about 3 ms). The peek
  also checks `user.db` read-only when it exists (`userdata.CheckFile`),
  so a library whose `user.db` has another schema version is listed as
  `error` with the open's message. In a read-only folder, where SQLite
  cannot create `-shm`, the peek is retried with `immutable=1` when no
  `-wal` holds data. Opening such a library is 409 `error` `the folder is
  read-only ...`.
- **Rebuild never leaves a partial or missing catalog** (job and CLI).
  The trash times are read from `user.db` before the backup, so an
  unreadable `user.db` leaves `media.db` in place. When the record stage
  does not finish (a fatal error, or a cancellation before the trash
  pass ends), the new catalog and its `-wal/-shm` are removed and the
  backup is renamed back. A folder that had no catalog is left without
  one (state `folder`). The job ends `failed` or `cancelled` with "...;
  the previous catalog was restored" (or "the incomplete catalog was
  removed"). The CLI prints the same. A cancellation during the
  thumbnail stage keeps the complete new catalog
  (`importer.RecordStageDone`).
- **Re-imported trashed content** is restored, not reported as a
  duplicate. When an import (job or CLI, not record-in-place) meets a
  file whose hash is a trashed row:
  - The trashed copy is hashed. On a mismatch the result is an error and
    the source is kept.
  - The row is restored through `importer.RestoreFromTrash`, the restore
    endpoint's code path, under the library's trash lock, with a unique
    name on collision.
  - The `user.db` trash row is removed.
  - The file is reported `imported` with the message `restored from
    trash`. With `move`, the source is deleted only after the restored
    copy hashes to the row's hash.
- **Empty trash** (`trash/delete` with `all: true`) also removes regular
  files under the trash directory that no trashed row names. A rebuild
  records no row for a trash file whose content or path is live. These
  files are counted in `deleted`. `verify` lists them as `orphan file in
  trash: <path>` and counts them as problems.
- **Shutdown** runs in three phases: `Server.StopJobs` (cancel the job;
  its streams end), then `http.Server.Shutdown` (in-flight requests,
  trash batches included, finish against open catalogs and under the
  lock), then `Server.Close` (close catalogs, stores, `user.db`, release
  locks). `Server.Shutdown` remains as StopJobs followed by Close.
- **Similar.** At most 32 pairs per item (see "Similarity"). The groups
  endpoint drops trashed rows in Go instead of joining every pair with
  `media` twice. `similar/ignore` takes `{group: hash}`; the `hashes`
  form now stores only the pairs that exist among the hashes.
  `Buckets` scans `NOT INDEXED` for hash sets over 10k.
- **Albums.** `count` and the default `cover` consider live members only.
  An explicit cover that is trashed (or not a member) falls back to the
  newest live member, and no live member gives `cover: null`. The
  `albums/{aid}/items` count is the live count too.
- **Smaller ones.** `validRelPath` also refuses a first component starting
  with `user.db`. `IsInternal` compares case-insensitively.
  `TagMedia` with a tag in both `add` and `remove` removes it and does
  not report it as added. `catalog.ErrNewerSchema`, `catalog.ErrNotCatalog`,
  `userdata.VersionError`/`ErrUnsupportedVersion`, `userdata.ErrNotUserDB`
  are new sentinels. `catalog.LiveHashes` is new.
- **Tests:** `internal/library/trash_test.go` (no-replace rename, restore
  and trash races, symlinked folders on both sides, directories and
  links refused); `internal/importer/restore_test.go`;
  `internal/catalog/peek_readonly_test.go`;
  `internal/userdata/checkfile_test.go`, `tagremovewins_test.go`;
  `internal/similar/cap_test.go`;
  `internal/server/review_fixes_test.go` and `management_fixes_test.go`
  (one test per finding); `cmd/rebuild_safety_test.go`. The thumbnail
  writer's commit interval is a package variable, so
  `TestThumbsCancellationLeavesPending` can cancel between commits (20 of
  20 runs pass). `TestFilterHashes`' time guard is 10 s (race builds).

## As implemented (frontend)

The Svelte app under `web/` was built against this contract while the Go
side was written in parallel, so it was tested against a fake of the API
(`web/src/testing/fakes.ts`), not against the server. Everything in
"Frontend" is implemented except the optional `#/jobs` page.

### Files

`lib/api.ts` (`libApi(id)`, workspace calls, chunking), `lib/routes.ts`
(app routes on the hash router, redirects), `lib/libraries.svelte.ts`
(workspace store, `gateOf`, `holderText`, `libraryNameProblem`),
`lib/library.svelte.ts` (the open library's summary, tags and albums),
`lib/selection.svelte.ts`, `lib/dates.ts`, `lib/actions.ts` (rate, tag,
album, trash, restore, delete: API call + change event + count refresh),
`lib/changes.ts` (change events so pages patch in place),
`lib/toast.svelte.ts`, `lib/similar.ts`; components `LibrarySwitcher`,
`LibraryGate` (what `#/l/<id>/…` shows while the library is not open),
`SelectionBar`, `RatingStars`, `TagInput` (combobox, also used for albums
and the tag filter), `AlbumPicker`, `DateFilter`, `Dialog` (focus trap,
Escape, focus return), `Toasts`; pages `Libraries`, `Albums`, `Album`,
`Trash`, `Similar`.

### Shapes relied on

- `GET /api/libraries` → `{root, mode, libraries, folders}`; a folder's id
  is its `name` (the server also sends `id` and `state: "folder"`).
  `POST /api/libraries/{id}/open`: 200 → the lib object (state taken as
  `open` when empty); 409 is read by its `state`: `busy` → `body.holder`
  `{pid, cmd, since}` with `since` a wall-clock string in the server's
  zone, shown as a local time (busy gate with Retry); `error` → the
  server's message (error gate with Rebuild); `folder` → the listing is
  reloaded; a 409 without `state` is treated as busy. 503 → rebuilding;
  404 → the listing is reloaded. `POST /api/libraries` → 201 lib (state
  `open`); the client still sends an open, which answers 200 again.
- A library-scoped answer counts as "not open" only when it is 409 **and**
  carries a string `state` (409 without `state`, e.g. "another job is
  running" or a tag rename conflict, is an ordinary error); 503 counts as
  rebuilding. The shell then marks the library closed and reopens it. A
  409 within 5 s of a successful open is shown as an open failure with
  Retry instead (no reopen loop).
- Jobs: library jobs go to `POST /api/l/{id}/jobs`; a rebuild of a
  `folder` or an `error` library goes to `POST /api/libraries/{id}/jobs`
  with `{kind: "rebuild"}`. The job's `library` is read for the pill and
  for filtering the Import and Library pages; a started job without it gets
  the id filled in client-side. Rebuild stats are read like an import's
  (`scanned`, `imported` + `existing` as "recorded", `errors`).
- `similar` progress: an SSE `progress` event whose data has `phase`
  (`load`/`compare`/`done` with `items`/`bands`/`pairs`) at the top level
  or under `progress`; a `stats` object, if present, is applied as before.
  The phase is kept on the job as `job.phase` (client-side field).
- Times: `similar.at` / `GET …/similar` `at` may be Unix seconds, an RFC
  3339 instant or a wall-clock string (`format.ts` `parseWhen`); the
  contract does not fix it. `deletedAt` is a wall-clock string in the
  server's zone and is read as a local time of the browser (same machine)
  for "deleted 3 days ago" (`parseLocalWallClock`, `formatAgo`).
- Items: `rating` omitted when 0; detail `rating` 0 or absent, `tags` and
  `albums` arrays or null, `deletedAt` null when live. Similar items: the
  extra `width`, `height`, `size`, `relPath` (falling back to `w`/`h`).
- `POST …/media/tags`: the answer's `tags` (added, including created) are
  sent by id in later chunks, so a tag named in `create` is only created by
  the first chunk. `POST …/albums` with more than 1000 hashes: the first
  1000 in the create body, the rest through `…/albums/{aid}/items`.
  `…/albums/{aid}/items` chunks `add` and `remove` separately; the last
  `count` wins. Trash, restore and delete sum their counts and concatenate
  `failed`. "Ignore group" sends `similar/ignore {group: <hash>}` (any
  hash of the group; the server ignores the whole current group, of any
  size), one call per group. Empty trash sends
  `{all: true, confirm: true}`; per-item deletes always send `confirm:
  true`.
- `/api/l/{id}/library`: the trash count (nav, Trash header, Empty trash
  and its typed count) is `summary.deleted`, the catalog rows the trash
  page lists and `all: true` deletes; the Library page shows the `userdb`
  counts and `similar.pairs`.
- Album `cover` is a hash or null and is shown through the scoped
  `thumb` endpoint.
- Library cards use `GET /api/libraries/{id}/cover?v=<cover hash>`
  (`libraryCoverUrl`): a JPEG thumbnail for a library in any readable
  state, 404 when there is none. The `v` parameter makes a new cover item
  a new URL despite `max-age=3600`.

### Deviations and additions

- **Covers on the Libraries page** come from the workspace route
  `GET /api/libraries/{id}/cover` for every card, closed libraries
  included (the scoped `thumb` endpoint answers 409 for a closed library;
  the backend added the cover route for this). The lib object's `cover`
  hash is not used by the page. A load error or 404 shows the library's
  initial instead.
- Step 3 hashes (`#/map`, `#/view/3`, `#/?session=4`) are redirected: in
  single mode to the same page of the one library, in workspace mode to
  `#/`. Unknown routes go home the same way. No `#/jobs` page (optional in
  the contract); the pill and the Library page's job table (now filtered
  to the library) cover it.
- The selection's scope key is library + page + album + filters; changing
  any of them clears it. A plain click on a tile still opens the lightbox
  while selecting; the checkbox, Ctrl/Cmd-click and Shift-click select.
  The bar shows "(N in months not loaded were skipped)" after a range over
  unloaded months. While the bar is shown, the grid (also on the album and
  trash pages, which reuse it) grows at the bottom by the bar's measured
  height plus its 24 px margin, so the last row can be scrolled clear of
  the bar; the padding goes away with the selection.
- Undo toasts (8 s) also follow Trash in the selection bar and "Trash the
  others" on the duplicates page, not only the lightbox's Delete.
- Lightbox additions: Trash/Restore buttons in its top bar, × on album
  chips (remove from album), tag and album chips link to the filtered
  grid and the album. `t`/`b` open the info panel when it is hidden.
- Date filter: the Range tab edits the inclusive last day (the URL keeps
  the exclusive `to`); editing it clears `recur`. The Every-year tab's
  month/day selects start at today until changed; its year limits are
  whole years (`from=YYYY-01-01`, `to=` the next Jan 1). Changing one
  limit keeps the other bound as it is (a day bound is shown as that day
  in its select, not "Any"); limits that would cross move the other one to
  the same year. The summary uses years only when every bound is a whole
  year, else days on both sides. The step 3 year selects moved into this
  panel.
- `hasFilters` ignores what the page implies (the album page's `album`,
  the trash page's `trashed`), so their empty states and Clear button
  behave like the grid's.
- New library names are checked in the browser with the server's rules
  (path separators, `:`, NUL and control characters, leading dot, 255
  bytes, Windows device names) before the request.
- "Set cover from selection" needs exactly one selected item.
- Duplicates: the default Keep is the item with the most pixels, then the
  larger file. Page size 50. A handled group (trashed, ignored) or items
  trashed elsewhere (the lightbox) leave the page at once, and the page is
  then fetched again from the server so later groups move up; the empty
  text shows only when the server's `total` is 0.
- The Library page gained a "Your data" card: the `user.db` path and the
  user data counts with links (rated, albums, trash, duplicates).

### Tests

`make web-check`: svelte-check (0 errors, 0 warnings), 139 vitest tests in
16 files, plus `npm run build`.

- `lib/routes.test.ts`: every route under `#/l/<id>/`, id decoding and
  encoding round trip, not-found shapes, step 3 paths, single-mode
  redirect of `#/` with the query, workspace redirects, no redirect before
  the listing.
- `lib/selection.test.ts`: ranges in display order either way, unloaded
  months skipped and counted, toggle without anchor, scope clearing,
  removal of hashes and the anchor.
- `lib/dates.test.ts`: `recur` parsing incl. `02-29` and wrapping ranges,
  malformed values, canonical serialization, presets (incl.
  This month in February = `02-01..02-29`), real-day checks, inclusive /
  exclusive conversions, year ranges, summaries.
- `lib/filters.test.ts`: the new query parameters, repeated `tag`,
  malformed values dropped, round trip, `filtersKey` and `hasFilters`.
- `lib/libraries.test.ts`: `gateOf` for every state; the store's load,
  open (opening → open; 409 → busy with holder; 503 → rebuilding; other →
  openFailed, Retry), create replacing a folder entry, record in place
  through the workspace route, the reopen guard, single mode; name
  validation and holder text.
- `lib/api.test.ts`: scoped URLs incl. encoded ids, filter parameters,
  chunking (ratings with progress, trash failures across chunks, tag
  create only in the first chunk, album create + items, album add/remove,
  delete with confirm, empty list sends nothing), the unavailable handler.
- `lib/format.test.ts` (relative times, `parseWhen`), `lib/similar.test.ts`
  (`bestOf`), `lib/jobs.test.ts` (start in a library).
- Component tests (jsdom, whole app or component mounted):
  `Libraries.test.ts` (cards in every state, covers from the workspace
  route with the initial on error, Retry, Record files in place
  with progress and opening afterwards, New library dialog: focus, Tab
  wrap, Escape with focus return, validation, create and open; opening a
  closed library, the switcher menu, gates for busy, error with Rebuild
  and missing), `SelectionBar.test.ts` (hidden when empty; rate and clear,
  tag autocomplete and create, album pick and create, trash with partial
  failure and Undo, 2500 hashes in three chunks, album and trash modes),
  `Management.test.ts` (Ctrl and Shift selection, the grid's bottom padding
  while the bar is shown, checkbox, in-place
  rating without a timeline reload, trash with the nav count and Undo,
  Escape and filter change clear; lightbox `4`, `t` + create, `b`,
  Delete with Undo; trash page tiles, `r`, Empty trash typed count; albums
  list, rename, album page, cover, Remove from album; duplicates Keep
  default and arrows, lightbox over the page, Enter, Run with distance;
  date presets, range, rating, tag and album filters in the URL and the
  requests), and the step 3 `App.test.ts` / `Grid.test.ts` moved under
  `#/l/<id>/`.

### Review fixes (frontend)

After review C (frontend and integration). The fake server
(`testing/fakes.ts`) now sends the real shapes: open answers 409 `{error,
state: "busy", holder}`, `{error, state: "error"}` and `{error, state:
"folder"}`, 503 while rebuilding; `holder.since` is a wall-clock string;
`POST /api/libraries` answers `state: "open"`; folders carry `id` and
`state`; `summary.deleted` counts the trash; restore reports `failed`;
similar groups are paginated and ignored by group.

- **Enter on a duplicate thumbnail** opens the viewer; the group's Enter
  (trash the others) acts only on the row itself or a Keep radio
  (`Management.test.ts` "reviews near-duplicates").
- **Open 409 by `state`**: `error` shows the server's reason with Rebuild,
  `folder` reloads the listing, `busy` shows the holder with Retry
  (`libraries.test.ts`, `Libraries.test.ts` "shows the reason when opening
  fails with state error"). `holderText` formats the wall-clock `since`.
- **Trash count** from `summary.deleted` everywhere (nav, header, Empty
  trash, typed count), not `userdb.trashed` (trash page test with
  `userdb.trashed` 0 and one catalog row in the trash).
- **Duplicates pagination**: the current page is fetched again after a
  group is handled or items are trashed elsewhere; the empty text needs
  `total` 0; an emptied last page moves back (`Management.test.ts` "pages
  through duplicates with the server").
- **Ignore group** sends `{group: <hash>}` (`LibApi.ignoreSimilarGroup`);
  the `hashes` form is gone from the client (`api.test.ts`, the
  pagination test).
- **Undo** of a trash (selection bar, lightbox, duplicates) goes through
  `undoTrash`, which shows restore failures (`failed`) and request errors
  as an error toast ("says so when Undo cannot restore").
- **Library counts**: `library.load()` while a load is in flight loads
  once more after it, so a write made meanwhile is reflected ("reloads the
  library counts after a write made while a load was in flight").
- **Empty trash** clears the selection; a Shift-clicked checkbox of a tile
  that stays selected shows checked; the selection bar's error is cleared
  with the selection (three tests in `Management.test.ts`).
- **Similar job phase**: a job object's `progress` (listings, state and
  done events) becomes `job.phase` (`jobs.test.ts`).
- **Gates poll**: the error, folder and rebuilding gates reload the
  listing every 10 s and watch the jobs ("keeps a gate current when a
  rebuild starts elsewhere").
- **Library covers** are `…/cover?v=<cover hash>`.
- **Date filter** year limits keep the other bound, show a day bound as
  that day, never cross; the summary is in days unless every bound is a
  whole year (`dates.test.ts`, "keeps an existing day bound when a year
  limit changes").
- **Dialog**: rendered at the end of `<body>` with its siblings inert while
  open; when the focused control becomes disabled, focus moves to the
  dialog; Escape and Tab that reach the window from outside it are handled
  by the topmost dialog ("keeps focus in a dialog whose focused button is
  disabled").
- **Removed** unused client code: `pagePath`, `inRecur` (with `wraps` and
  `yearRange`, unused after the date filter change), `LibApi.listJobs`,
  `createTag`, `renameTag`, `deleteTag`, and their tests.
- Album counts and covers that include trashed members (review L6) are a
  backend fix; the client shows what the server answers.

## View memory (web)

Filters live in the route query, so leaving a page (Import, Library, ...)
used to lose them: the main nav linked to the bare page. The web UI now
remembers, per page path, the last filter query and the place in the grid
(`web/src/lib/viewmemory.svelte.ts`).

- **What is remembered**: for the Photos grid (`/l/<id>/`), the Map
  (`/l/<id>/map`), the trash, each album (`/l/<id>/albums/<aid>`) and each
  import record (`/l/<id>/imports/<rid>`): the canonical filter query
  (`filtersToQuery`; the map's `at`/`z` are not part of it) and, for grid
  pages, an anchor: the bucket at the top of the view (`{year, month}`, or
  `{undated: true}`) and the pixel offset into it.
- **Recording**: the shell records the query of every visit to one of these
  pages; a different query than the remembered one drops the anchor. The
  grid saves its anchor when it unmounts (route change) and when its
  filters change; an anchor for a query the page has moved on from is
  ignored.
- **Nav links**: the main nav (Photos, Map, Trash), the library switcher,
  the Libraries page's library links, the album's "Go to Photos" and the
  single-mode redirect from `#/` go to the remembered query. Links that
  carry their own query ("Show imported", the Library page's stats, tag and
  session links) and the Imports page's record links are unchanged.
- **Clear**: the filter bar's Clear navigates to the bare path, which is
  recorded like any visit and so replaces the remembered query.
- **Scroll restore**: a grid that mounts with the remembered query (nav
  link, browser Back/Forward) scrolls to the anchor's bucket top plus the
  offset as soon as the first timeline is laid out and the scroller
  measured; the scroll state is set before the rows render, so the top is
  not painted or fetched first, and no second timeline load happens. A
  bucket that no longer exists, or another query, starts at the top.
- **Storage**: in memory plus `sessionStorage` under
  `picmover.view.<path>` (for example `picmover.view./l/peter/`), JSON
  `{query, anchor?, savedAt}`, validated on load. A reload within the tab
  keeps it; a new tab starts fresh. The map's viewport stays in
  localStorage (`picmover.map.view`) as before.
- **Tests**: `src/lib/viewmemory.test.ts` (store, keys, validation) and
  `src/ViewMemory.test.ts` (nav hrefs, Clear, reload, scroll restore, other
  query and a vanished bucket start at the top).
