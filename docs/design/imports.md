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
