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
