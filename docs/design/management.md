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
internal/userdata/        user.db: schema v1, Open, ratings/tags/albums/trash/ignored
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
- Job kind `similar` (`{kind:"similar", distance?: 1..7}`): loads items,
  computes, replaces `similar_pairs` in one transaction, writes `meta`.
  Progress events: `{phase:"load", items}`, `{phase:"compare", bands}`,
  `{phase:"done", pairs}`. 136k items must finish in under 10 s.
- Groups (`GET .../similar`): union-find over `similar_pairs` minus
  `similar_ignored` minus pairs touching a trashed hash; groups of size ≥ 2;
  sorted by size desc, then by max distance asc, then by smallest hash.
  Computed per request (20k pairs → a few ms), paginated with
  `offset`/`limit` (default 50, max 200).
- "Ignore": all pairs among the given hashes are inserted into
  `similar_ignored`. "Keep": the client trashes the other hashes with the
  trash endpoint; no special server support.

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
`media.db` read-only without the lock, and are cached per path by the
file's size+mtime for 10 s. In single mode the listing has exactly one
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
| POST | `/api/l/{lib}/similar/ignore` | `{hashes: [...]}` → `{ignored: pairs}` |

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
  with `Contains`; symlinked subfolders are allowed only when they resolve
  inside the root (`EvalSymlinks` + `Contains`), otherwise skipped from
  the listing and 404 on access.
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
  is its `name`. `POST /api/libraries/{id}/open`: 200 → the lib object
  (state taken as `open` when empty); 409 → `body.holder` `{pid, cmd,
  since}` with `since` an RFC 3339 instant; 503 → rebuilding; 404 → the
  listing is reloaded. `POST /api/libraries` → 201 lib (state `closed`),
  which the client then opens.
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
  `failed`. `similar/ignore` is not chunked (the server ignores pairs
  *within* one call; a group is far below the cap). Empty trash sends
  `{all: true, confirm: true}`; per-item deletes always send `confirm:
  true`.
- `/api/l/{id}/library`: the trash count in the nav is `userdb.trashed`,
  falling back to `summary.deleted`; the Library page shows the `userdb`
  counts and `similar.pairs`.
- Album `cover` is a hash or null and is shown through the scoped
  `thumb` endpoint.
- Library cards use `GET /api/libraries/{id}/cover` (`libraryCoverUrl`):
  a JPEG thumbnail for a library in any readable state, 404 when there is
  none, cached by its ETag.

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
  whole years (`from=YYYY-01-01`, `to=` the next Jan 1). The step 3 year
  selects moved into this panel.
- `hasFilters` ignores what the page implies (the album page's `album`,
  the trash page's `trashed`), so their empty states and Clear button
  behave like the grid's.
- New library names are checked in the browser with the server's rules
  (path separators, `:`, NUL and control characters, leading dot, 255
  bytes, Windows device names) before the request.
- "Set cover from selection" needs exactly one selected item.
- Duplicates: the default Keep is the item with the most pixels, then the
  larger file. Page size 50. Items trashed elsewhere (the lightbox) leave
  their group; a group under two items disappears.
- The Library page gained a "Your data" card: the `user.db` path and the
  user data counts with links (rated, albums, trash, duplicates).

### Tests

`make web-check`: svelte-check (0 errors, 0 warnings), 123 vitest tests in
16 files, plus `npm run build`.

- `lib/routes.test.ts`: every route under `#/l/<id>/`, id decoding and
  encoding round trip, not-found shapes, step 3 paths, single-mode
  redirect of `#/` with the query, workspace redirects, no redirect before
  the listing.
- `lib/selection.test.ts`: ranges in display order either way, unloaded
  months skipped and counted, toggle without anchor, scope clearing,
  removal of hashes and the anchor.
- `lib/dates.test.ts`: `recur` parsing incl. `02-29` and wrapping ranges,
  malformed values, canonical serialization, `inRecur`, presets (incl.
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
