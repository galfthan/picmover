# Import history

Status: contract for branch `feat/phone-import` (PR #7, together with the phone fast path and the web transfer rate).
"As implemented" sections go at the end, backend then frontend.

## Goal

Every import run is remembered as a browsable record, like an album made by
the importer: when it ran, where the files came from, how it went, and
which pictures it brought in. The record lives in `user.db` so a `rebuild`
never loses it. One click shows the pictures of an import in the grid.

Today the catalog has `import_sessions` and `media.import_session_id`,
which the web's "Show imported" button uses (`?session=<id>`). They stay
as they are (the thumbnail stage needs them) but they are derived data
and vanish with a rebuild. The user.db record is the durable one.

## user.db version 3

`userdata.SchemaVersion` becomes 3. Migration v2 → v3 follows `migrateV1`
exactly: back up to `user.db.bak-<stamp>` first, one transaction, re-check
the version inside it, set `user_version` last. A version-1 file runs both
steps in order (one backup is enough). Version 3 files are refused by older
builds as before.

```sql
CREATE TABLE imports (
    id           INTEGER PRIMARY KEY,
    name         TEXT NOT NULL DEFAULT '',   -- user-given, optional
    started_at   INTEGER NOT NULL,           -- unix seconds
    finished_at  INTEGER,                    -- NULL while running or if never finished
    source       TEXT NOT NULL,              -- the path given to import (as typed, cleaned)
    source_name  TEXT NOT NULL,              -- its base name, for lists
    mode         TEXT NOT NULL,              -- copy | move | rebuild | record
    host         TEXT NOT NULL,              -- os.Hostname at the time
    outcome      TEXT NOT NULL,              -- running | done | cancelled | failed
    imported     INTEGER NOT NULL DEFAULT 0,
    existing     INTEGER NOT NULL DEFAULT 0,
    duplicates   INTEGER NOT NULL DEFAULT 0,
    skipped      INTEGER NOT NULL DEFAULT 0, -- skipped_small + non_media
    errors       INTEGER NOT NULL DEFAULT 0,
    bytes        INTEGER NOT NULL DEFAULT 0,
    catalog_session INTEGER                  -- media.db import_sessions.id at the time, informational
);
CREATE TABLE import_items (
    import_id INTEGER NOT NULL REFERENCES imports(id) ON DELETE CASCADE,
    hash      INTEGER NOT NULL,
    status    TEXT NOT NULL,                 -- imported | existing | restored
    PRIMARY KEY (import_id, hash)
);
CREATE INDEX import_items_hash ON import_items(hash);
```

`existing` counts files found already in place or identical at the
destination; `restored` is trashed content brought back by the import.
Duplicates (content already in the catalog) are counted but have no item
row: they belong to the earlier import that brought them.

## Recording

`userdata.DB` gets:

```go
type ImportRecord struct { ID int64; Name string; StartedAt, FinishedAt time.Time; Source, SourceName, Mode, Host, Outcome string; Imported, Existing, Duplicates, Skipped, Errors, Bytes int64; CatalogSession int64 }
type ImportItem struct { Hash uint64; Status string }
func (d *DB) BeginImport(ctx, rec ImportRecord) (id int64, err error)            // started_at, source, source_name, mode, host; outcome running
func (d *DB) FinishImport(ctx, id int64, rec ImportRecord, items []ImportItem) error // one transaction: update counts/outcome/finished_at, insert items
func (d *DB) ListImports(ctx) ([]ImportSummary, error)                            // newest first; ImportSummary = ImportRecord + Items int64 + Cover uint64 (hash of the first item, 0 if none)
func (d *DB) GetImport(ctx, id) (ImportRecord, error)
func (d *DB) ImportHashes(ctx, id) ([]uint64, error)
func (d *DB) RenameImport(ctx, id, name string) error
func (d *DB) DeleteImport(ctx, id) error                                          // the record only; never a file
func (d *DB) ImportsOf(ctx, hash uint64) ([]ImportRecord, error)                  // for the detail panel: "imported on …"
```

The record is written in two steps so a crash mid-import still leaves a
row: `BeginImport` before `importer.Run` (outcome `running`), then
`FinishImport` after it returns, whatever the outcome. The item hashes come
from the catalog: `catalog.DB.SessionItems(ctx, sessionID) ([]SessionItem,
error)` is not needed: the importer already distinguishes imported,
existing and restored in its `Result`. Add `Hash uint64` to
`importer.Result` (set for imported, existing, restored and duplicate
results) so callers collect `(hash, status)` from `OnResult`. Results are
reported only after the catalog batch committed, so rows whose insert was
undone (hash race, failed batch) never appear.

Both callers record: `cmd/import.go` (it already opens user.db for
`OnRestored`) and the server's import job (`internal/server/jobs.go`). A
`rebuild` records too, with mode `rebuild` and every row as `existing`
(this is how a library laid out by an earlier version gets its first
record; the list shows it as a rebuild, not an import). `thumbs` and
`update-metadata` do not record. The CLI prints the import id in the
summary (`Import record: #12`); the job JSON carries `importId`.

A failure to write the record never fails the import: it is logged at
error level and the summary says the record could not be written.

### Backfill

The first time a library's user.db is at version 3 and `meta` has no
`imports_backfilled` key, the server (on library open) and the CLI
commands that open both databases (`import`, `rebuild`, `serve`) copy the
catalog's `import_sessions` into `imports` (one row each, with the
session's counts, `catalog_session` set, mode as stored, outcome `done` if
`finished_at` is set else `cancelled`, name empty, host empty) and their
rows into `import_items` (status `imported`; trashed rows included), then
set `imports_backfilled = <unix now>`. Sessions with no rows are skipped.
This runs once per library, in one transaction, and is skipped when
`imports` already has rows. It gives the libraries the user has not
rebuilt their history on the first open.

## API (all under `/api/l/{lib}/`)

```
GET  imports                      → {imports: [{id, name, startedAt, finishedAt, source, sourceName, mode, host, outcome, imported, existing, duplicates, skipped, errors, bytes, items, cover}]}
GET  imports/{iid}                → the same object plus nothing else (hashes come through the grid filter)
POST imports/{iid}                {name}      → rename (same rules as album names: trimmed, ≤ 200 chars, may be empty)
POST imports/{iid}/delete         {}          → deletes the record; 404 when missing
GET  media/{id}                   → gains "imports": [{id, name, startedAt, mode}] for the detail panel
```

Grid filter: `import=<iid>` on every media/bucket/geo listing, resolved to
`Filter.Hashes` like `album=`; unknown id → 404 `{error}`. `import` and
`album` together are allowed (intersection). `session=` stays.

`cover`: hash of the newest item that has a thumbnail, else 0; the client
asks `/thumb/<hash>` as for albums (reuse whatever the album cover does).

Ids are validated like album ids (positive int). POST rules of step 3 apply
(JSON content type, `X-Picmover: 1`, 64 KiB cap).

## CLI

`picmover imports <library>`: lists the records newest first (id, date,
mode, source name, imported/existing/duplicates/errors, bytes, name).
`picmover imports <library> --rename <id> <name>` and `--delete <id>`.
`import` and `rebuild` print the record id.

## Web

- A new library page `imports` at `#/l/<id>/imports`, in the nav next to
  Albums. A list, newest first: cover thumbnail, name or source name, date
  (relative when under a week, else the local date), mode chip, counts
  (`12 new, 3 already there, 1 error`), bytes. Click → the grid with the
  `import` filter and the usual selection tools, titled "Import: <name or
  source name>", with Rename and Delete record (confirm dialog; says the
  files stay) in the title bar, like the Album page. Reuse the Album page
  structure; do not duplicate the grid.
- The import wizard's finished panel: "Show imported" links to
  `?import=<importId>` when the job has one, else `?session=` as today.
- The lightbox/detail panel shows "Imported <date> from <source name>"
  per record, linking to that import.
- Filter chips show `Import: <name>` like `Album: <name>`.
- Every page shows the library it operates on, as everywhere else.
- Tests: `lib/api.ts` parsing, the imports page list rendering and the
  rename/delete flows against the fake server in `testing/fakes.ts`; the
  routes test for the new page.

## Not in scope

Cross-library import history, re-running an import, editing items, moving
items between imports, and statistics over time.

## As implemented (backend)

Branch `feat/import-history-backend` (from `feat/phone-import` at
a02f117).

### Code

- `internal/userdata/userdata.go`: `SchemaVersion = 3`, `schemaV3`. The
  migration is one function, `migrate`, driven by a `migrations` table
  (1 → 2: `backup_targets`, `meta`, identity; 2 → 3: `imports`,
  `import_items`): one `user.db.bak-<stamp>` copy, one transaction,
  version re-checked inside it (another process that migrated meanwhile
  wins and the copy is removed), steps in order, `user_version` last.
  `Migrated()` reports the version migrated from (1 or 2). `CheckFile`,
  `IntegrityCheck` accept 1, 2 and 3; `PeekBackupSummary` reads 2 and 3.
  A file of version 4 or more is refused as before (`VersionError`, file
  untouched); `OpenReadOnly` (backup dry run) opens a version-2 file as
  it is (it has the identity and the targets it reads), version 1 is
  still `ErrNeedsMigration`.
- `internal/userdata/imports.go`: the record API (below), `CleanImportName`
  (trimmed, ≤ 200 runes, no control characters, empty allowed),
  constants for modes, outcomes and item statuses, `BackfillImports` and
  `ImportsBackfilled`.
- `internal/catalog`: `Sessions` (every session with its counters and the
  hashes of its rows, trashed included, row id order) and `SessionsFile`
  (the same on a catalog file opened read-only like `PeekFile`: nothing is
  written or checkpointed; a rebuild reads the old catalog with it).
- `internal/importer`: `Result.Hash` (imported, existing, restored,
  duplicate; 0 otherwise) and `Result.Restored` (an imported result that
  restored trashed content). Results of rows whose insert was undone are
  duplicates or errors, so never become items.
- `internal/importrec` (new): `Recorder` (`Begin`, `OnResult`, `Finish`,
  `ID`, `Err`), `ItemStatus`, `Outcome` (done for success and for a
  failed thumbnail stage, cancelled, failed), `Backfill(catalog, user)`,
  `BackfillFile(path, user)`. Shared by the CLI and the server. Every
  write failure is logged at error level and kept in `Err`; nothing in it
  fails an import.
- `cmd/import.go`: opens (creating) user.db, backfills from the live
  catalog (not for a rebuild), begins the record before `importer.Run`,
  finishes it after, whatever the outcome (a rebuild whose record stage
  did not finish is `failed` or `cancelled`), and prints `Import record:
  #N`, or `Import record: could not be written (<reason>; see the log)`.
  `cmd/rebuild.go` backfills from the old catalog, read-only, before its
  backup. `cmd/imports.go`: the `imports` command.
- `internal/server`: `api_imports.go` (endpoints), the `import=` filter in
  `parseFilter`/`resolveFilter` (`filterError` answers 404), the detail's
  `imports`, the job's `importId` (`beginRecord`/`finishRecord` in
  `jobs.go`, for import and rebuild jobs), the backfill in `ws.open` and in
  `New` (single mode), and `runRebuild` backfills from the old catalog
  before its backup.

### userdata API (deviations from the contract)

```go
func (d *DB) BeginImport(ctx, rec ImportRecord) (int64, error)  // StartedAt zero: now; SourceName empty: base of Source
func (d *DB) FinishImport(ctx, id int64, rec ImportRecord, items []ImportItem) error // ErrNotFound when deleted meanwhile; a repeated hash keeps its first status
func (d *DB) ListImports(ctx) ([]ImportSummary, error)            // started_at DESC, id DESC
func (d *DB) GetImport(ctx, id) (ImportRecord, error)             // ErrNotFound
func (d *DB) ImportHashes(ctx, id) ([]uint64, error)              // insertion order
func (d *DB) RenameImport(ctx, id, name string) (ImportRecord, error) // returns the record (the contract had error only)
func (d *DB) DeleteImport(ctx, id) error                          // ErrNotFound
func (d *DB) ImportsOf(ctx, hash) ([]ImportRecord, error)         // newest first
func (d *DB) ImportsBackfilled(ctx) (bool, error)
func (d *DB) BackfillImports(ctx, []BackfillSession) (int, error) // the transaction of "Backfill"
```

`Result.Restored` is new: `Result` alone did not tell a restore from an
import (only by its message).

### JSON (what the frontend uses)

Import object (`GET imports` items, `GET imports/{iid}`, the answer of
`POST imports/{iid}`):

```json
{"id": 12, "name": "", "startedAt": "2026-10-11T09:30:00+03:00",
 "finishedAt": "2026-10-11T09:41:12+03:00", "source": "/media/u/phone/DCIM",
 "sourceName": "DCIM", "mode": "copy", "host": "box", "outcome": "done",
 "imported": 120, "existing": 3, "duplicates": 7, "skipped": 2, "errors": 1,
 "bytes": 734003200, "items": 123, "cover": "00ab34cd56ef7890"}
```

- Times are RFC 3339 in the server's zone; `finishedAt` is `null` while
  running or for a record never finished (outcome `running`).
- `mode`: `copy`, `move`, `rebuild`, or `record` (a rebuild session
  backfilled from the catalog, under its catalog name).
- `host` is `""` for backfilled records.
- `items` is the number of the record's items that are live catalog rows
  now, and `cover` the newest of them by capture time as 16 hex digits,
  or `null` when none is live: exactly the album rules (`LiveHashes`,
  `NewestOf`). The contract said "0 if none" and "has a thumbnail"; the
  album cover does not check the thumbnail, so neither does this.
- `GET imports` → `{"imports": [...]}`, newest first. `GET imports/{iid}`
  → the bare object.
- `POST imports/{iid}` `{"name": "..."}` → 200, the object. A missing
  `name` is 400 `name is required`; a name over 200 characters or with
  control characters is 400 `invalid name: ...`.
- `POST imports/{iid}/delete` `{}` → 200 `{}`.
- Unknown or malformed `{iid}` → 404 `{"error": ...}` (`not found:
  import 12`, or `no such import` for a malformed id), as for albums.
- `GET media/{id}` gains `"imports": [{"id", "name", "startedAt", "mode",
  "sourceName"}]`, newest first, `[]` when none (`sourceName` added for
  the "Imported <date> from <source name>" line).
- Grid filter `import=<iid>` on `timeline`, `media`, `geo`, `geo/items`:
  a malformed value is 400 `invalid import ...`, an unknown id 404
  `{"error": "no such import"}` (unlike `album=`, whose unknown id gives
  an empty grid, unchanged). It intersects with `album`, `tag` and
  `rating`.
- Job object: `"importId": <number>|null` for import and rebuild jobs,
  set before the running state is published; `null` for other kinds and
  when the record could not be begun.

### CLI

`picmover imports <library>` prints `Library: <path>` and a table `ID
STARTED MODE OUTCOME SOURCE IMPORTED EXISTING DUPLICATES ERRORS BYTES
NAME` (an OUTCOME column beyond the contract: a crashed import shows
`running`). `--rename <id> <name>` (an empty name clears it) and
`--delete <id>`. It opens the library without the lock (as `targets`) and
backfills too. Exit status 2 for usage errors and unknown ids, like the
backup management commands.

### Other deviations and notes

- `import` now creates `user.db` when a library has none (the record
  needs it); before, it only opened an existing one for a trash restore.
- The backfill also runs in `imports` and in the server's rebuild job
  (before the backup, read-only), not only on open. A rebuild of a
  library whose `user.db` was never backfilled copies the old catalog's
  sessions first; without that the first rebuild by this version would
  lose them.
- `BackfillImports` sets `imports_backfilled` also when `imports` already
  has rows (it then writes nothing), so the check is not repeated.
- Backfill success is logged at debug level in the server (its info log
  stays quiet).
- `ListImports` and `newImport` read each record's hashes to compute the
  live count and cover; a rebuild record holds every hash of the library,
  so the list costs one `json_each` scan per record. Fine for hundreds of
  records; a summary cache would be the next step if it is not.

### Tests

- `internal/userdata/imports_test.go`: v2 → v3 (data, identity, targets
  kept; one backup; `CheckFile`, `IntegrityCheck`, `PeekBackupSummary`,
  `OpenReadOnly` on v2), v1 → v3 (both steps, one backup), migrated schema
  equal to a fresh one, fresh file at v3; records: begin (crash-shaped:
  outcome `running`, no finish time), finish, validation, list order and
  counts, `ImportsOf`, rename rules (trim, clear, 200 runes, control
  characters), delete (items cascade), backfill (two sessions plus an
  empty one, once only, skipped but marked when records exist). Refusal of
  a newer file: the existing `TestOpenSchemaAndVersion`, `TestCheckFile`
  (now version 4).
- `internal/catalog/sessions_test.go`: `Sessions` with an empty session
  and trashed rows; `SessionsFile` reads the same without touching the
  WAL and refuses another schema.
- `internal/importer/hash_test.go`: `Hash` on imported, existing, both
  kinds of duplicate, none on non-media; restored (`Restored`); lost hash
  race (duplicate with hash) and failed batch (error, no hash).
- `internal/importrec/importrec_test.go`: `ItemStatus`, `Outcome`,
  concurrent `OnResult`, finish with a cancelled context, no user.db and
  a record deleted mid-run (logged at error level, `Err` set), `Backfill`
  on a catalog with two sessions, one empty.
- `cmd/imports_test.go`: import and rebuild print and write their records;
  the `imports` list output, rename, clear, delete (catalog unchanged),
  argument errors; backfill on the first import of a library without
  history and from the old catalog on rebuild; a newer `user.db` does not
  fail the import and the summary and the log say so.
- `internal/server/imports_test.go`: backfilled record on open (single
  mode) and on a workspace open; list/get shapes, 404s, rename rules,
  delete (files and rows stay), live count and cover after a trash, POST
  mutation rules; `import=` on timeline, media, geo, geo/items, with
  `album`, unknown id 404, malformed 400; detail `imports`; a running
  (crash-shaped) record; import and rebuild jobs record and carry
  `importId`, a thumbs job does not; records survive a rebuild.

## As implemented (frontend)

Routes (`web/src/lib/routes.ts`): `#/l/<id>/imports` is the page `imports`;
`#/l/<id>/imports/<iid>` is `importrecord` with `importId` on the lib route
(positive int, else not found; extra segments not found). `imports` is not
a step 3 legacy path. `importedHref(lib, job)` picks "Show imported":
`?import=<importId>` when the job has a positive `importId`, else
`?session=<sessionId>`, else no link. Both the import wizard's finished
panel (`pages/Import.svelte`) and the job list on the Library page use it.

API client (`web/src/lib/api.ts`): `ImportRecord`, `ImportRef`;
`getImports`, `getImport(iid)`, `renameImport(iid, name)`,
`deleteImport(iid)` on `LibApi`. `toImport` defaults every missing field
(strings `""`, counts 0, `startedAt`/`finishedAt` null when absent or
empty) and accepts the record bare or wrapped as `{import: …}`. `cover` is
used only when it is a 16-hex-digit hash string (as album covers);
anything else (`0`, `""`, null) means no cover. `getMedia` normalises
`imports` with `toImportRefs` (entries without a positive id are dropped).
Ids are checked before they reach a URL. `Filters.import` is sent as
`import=<iid>` by `filterParams` and lives in the route query
(`lib/filters.ts`), next to `album` and `session`. `Job.importId` is
optional (`number | null`).

Components:

- `pages/Imports.svelte`: the list, newest first as the server orders it.
  Row: cover (`/thumb/<hash>`), title (name, else source name, else
  `Import #<id>`), mode chip, outcome chip unless `done`, date (relative
  under a week, else the local date; full instant in the tooltip), the
  source (the source name when the record is named, else the full path),
  counts (`12 new, 3 already there, 2 duplicates, 1 error`, zeros left
  out, `Nothing imported` when all are zero) and bytes. Shows the library
  root ("In /media/peter") like Albums, an empty state, and a link to the
  import wizard.
- `pages/ImportRecord.svelte`: `Grid` with the new `mode="import"` and
  `importId` (the grid adds `import` to its effective filters, hides the
  import chip and counts it as implied; the selection bar runs as on the
  plain grid). Header: back link, `Import: <title>`, mode and outcome
  chips, Rename and Delete record, and a facts line (started at, full
  source path, host, counts, bytes). Rename uses a Dialog: trimmed, at most
  200 characters (checked client-side too), empty allowed and clears the
  name. Delete record confirms in a Dialog that the record goes and the N
  photos and videos stay in the library with no file touched, then
  returns to the list with a toast. A 404 shows "There is no such import
  record in this library." instead of the grid.
- `components/Filters.svelte`: chip `Import: <title>` with a remove button
  (`aria-label="Remove import filter <title>"`); the title comes from
  `library.imports`, loaded on demand when the id is not known
  (`#<iid>` until then). The old session chip's remove label became
  "Remove import session filter".
- `components/Lightbox.svelte`: under "Imported", one link per record in
  `imports` to `#/l/<id>/imports/<iid>`, text from `importLine`
  ("Imported <instant> from <source name> · <name>", or "Recorded by a
  rebuild …" for mode `rebuild`). Without `imports` (older server) it
  shows `importedAt` and the session link as before.
- `lib/library.svelte.ts`: `imports` and `loadImports()`, kept apart from
  `load()`/`loadLists()` so a server without the endpoint does not mark
  the tag and album lists as failed.
- `lib/imports.ts`: wording helpers (`importTitle`, `modeLabel`,
  `outcomeLabel`, `importCounts`, `formatRecent`, `importLine`).
- Nav: "Imports" right after "Albums", active on both pages.

Fake server (`web/src/testing/fakes.ts`): two records in "peter" (#5
named, copy, items 1..4, one error; #2 unnamed, move, cancelled, items 7
and 8), `GET imports`, `GET/POST imports/<iid>`, `POST
imports/<iid>/delete` (404 when missing, 400 for a bad id or a name over
200 characters), the `import=` filter on listings (404 for an unknown id)
and `imports` in the media detail. `ZERO_STATS` gained `rate: 0` (it was
missing since the transfer-rate commit and broke `svelte-check`; the same
fix in `backupinfo.test.ts`).

Tests:

- `lib/routes.test.ts`: the two routes, malformed ids, `importedHref`
  preferring `importId` and falling back to `sessionId`.
- `lib/api.test.ts`: list parsing with defaults and junk entries, cover
  only as a hash, wrapped answers, get/rename/delete URLs and bodies,
  invalid ids never sent, `import=` in listings, `imports` in the detail.
- `lib/filters.test.ts`: `import=` read, written, keyed and implied.
- `lib/imports.test.ts`: titles, labels, counts, dates, the detail line.
- `Imports.test.ts` (whole app on the fake server): the list rendering and
  nav position; the empty state; the record grid (`timeline?import=5`, other
  filters combine, rename incl. clearing and the length limit); delete
  with the confirm text, no file request, back to the list; a missing
  record; the `Import: <name>` chip on the plain grid and its removal; the
  lightbox link; "Show imported" on the Library page choosing `?import=`
  over `?session=`.
- `App.test.ts`: the nav list, and the wizard's finished panel linking to
  `?import=31` when the done job carries `importId`.

Assumptions beyond the contract (the backend should match):

- `POST imports/<iid>` and `POST imports/<iid>/delete` answers are not
  read; the client reloads the record and the list afterwards. Any 2xx JSON
  body works (the fake returns the updated record and `{}`).
- `cover` is a 16-hex-digit hash string like album covers (a JSON number
  cannot carry a uint64); "none" may be `null`, `""` or `0`.
- `startedAt`/`finishedAt` are RFC 3339 with zone; `finishedAt` is `null`
  when unset.
- The detail's `imports` entries may also carry `sourceName`; the line
  "Imported <date> from <source name>" needs it, and without it the line
  reads "Imported <date>" plus the name. This is the one deviation from the
  contract's `{id, name, startedAt, mode}`: adding `sourceName` there is
  recommended.
- `GET imports/<iid>` for an unknown id answers 404 (the page shows it as
  missing; other errors are shown with Retry).
