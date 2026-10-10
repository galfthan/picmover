# Step 5: backups

Contract for step 5 of the modernization: back a library up to one or more
targets, keep the copies current, prove they are intact, and restore from
them. Written before implementation; the implementers append "As
implemented" sections, as in `management.md`.

## Goals

- **Targets by marker, not by path.** A backup target is a folder on another
  volume or share holding a marker file with a random id. A library remembers
  the targets it has been backed up to by id, so a drive that mounts as `E:`
  today and `/media/alfthan/Backup` tomorrow is still the same target.
- **Never loses data, never surprises.** A backup run only adds and moves on
  the target. Nothing on a target is removed except by an explicit *prune*
  with a count the user confirms. Nothing in the library is ever modified by
  a backup run. Every copy is verified by hash before it counts.
- **Incremental, move-aware, resumable.** Files are keyed by content hash,
  so a photo moved within the library is renamed on the target instead of
  copied again, and an interrupted run leaves a consistent state that the
  next run continues from.
- **Scrub.** A target can be re-hashed, fully or with a time budget that
  rolls through the target over several runs, so bit rot is found and
  reported, and repaired from the library when the library still has the
  good copy.
- **Restore.** Files missing from the library (a dead disk, an accidental
  delete) are copied back from a target; the catalog is rebuilt from the
  restored files and `user.db` comes back too.
- **Works unattended.** CLI commands for cron and Task Scheduler with
  machine-readable exit status, and the same operations as jobs in the UI.

## Non-goals (this step)

- Cloud object storage, encryption, compression, deduplication across
  libraries on a target, versioned history of edited files beyond keeping
  the previous version, scheduling inside picmover, backing up the
  thumbnail cache, a Wails shell. Cross-library search stays deferred.

## Terminology

*Target*: a folder with a marker file; one target can hold several
libraries, each under `<target>/<library-name>/`. *Backup set*: one
library's copy on one target. *Scrub*: re-hash target files and compare
with what the backup set recorded. *Prune*: remove from the target what
the library no longer has.

## Layout

```
internal/backup/            target marker, backup.db (schema v1), plan, run, scrub, prune, restore
internal/library/           copy helpers reused (CopyVerified, RenameNoReplace, HashFile)
internal/userdata/          backup_targets table (v2 of user.db with a migration from v1)
internal/server/api_backup.go   endpoints below; job kinds backup, scrub, prune, restore
cmd/backup.go, cmd/scrub.go, cmd/restore.go, cmd/targets.go
web/src/pages/Backups.svelte, components/TargetCard.svelte, BackupProgress.svelte,
web/src/lib/backups.svelte.ts
```

## What is backed up

For a library at `<root>`, the backup set at `<target>/<name>/` mirrors
the library tree so it can be browsed with any file manager:

- **Every regular file under `<root>`** except picmover's caches: nothing
  under `.picmover/` other than `.picmover/trash/`, and no `media.db*`
  side files (`-wal`, `-shm`, `-journal`, `.bak-*`) or `user.db-*` side
  files. Media files are matched by catalog hash; any other file (sidecars,
  notes) by path, size and mtime, hashed on copy. Trashed files are backed
  up at their trash path; a later prune removes them once the library has
  emptied its trash.
- **`user.db`**, copied as a consistent snapshot (`VACUUM INTO` a temp
  file, then renamed into place) on every run, as `<set>/user.db`.
- **`media.db`** as a snapshot the same way, as `<set>/media.db`, so a
  restored library opens without a rebuild; it is a convenience, the
  restore path still verifies every file.
- Symlinks are skipped and listed in the report; the backup never follows
  them.

## On the target

```
<target>/picmover-backup.json       marker: {"id": "<16 hex>", "created": "...", "format": 1}
<target>/<name>/                    the backup set (mirror tree, user.db, media.db)
<target>/<name>/.picmover/backup.db set state (schema v1)
<target>/<name>/.picmover/previous/<rel_path>.<unix>   earlier content of a file that changed in place
<target>/<name>/.picmover/logs/     run logs
```

`backup.db` (SQLite, WAL off, `PRAGMA user_version = 1`):

```sql
CREATE TABLE files (
    rel_path    TEXT PRIMARY KEY COLLATE NOCASE,  -- forward slashes, relative to the set
    hash        INTEGER NOT NULL,
    size        INTEGER NOT NULL,
    mtime       INTEGER NOT NULL,                 -- source mtime, Unix seconds
    backed_at   INTEGER NOT NULL,
    verified_at INTEGER                           -- last scrub that read the whole file
);
CREATE INDEX files_hash ON files(hash);
CREATE INDEX files_verified ON files(verified_at);
CREATE TABLE runs (
    id          INTEGER PRIMARY KEY,
    kind        TEXT NOT NULL,                    -- backup | scrub | prune | restore
    started_at  INTEGER NOT NULL,
    finished_at INTEGER,
    library     TEXT NOT NULL,                    -- source root as given, for the report only
    stats       TEXT NOT NULL DEFAULT '{}'        -- JSON of the run's counters
);
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);  -- library_id, name, scrub_cursor
```

`files` is the truth about the set. A file counts as backed up only after
its copy was read back and its hash matched; the row is written then. The
hash is xxhash64 as in the catalog.

## In the library

`user.db` schema version 2 adds:

```sql
CREATE TABLE backup_targets (
    id         TEXT PRIMARY KEY,                  -- marker id
    label      TEXT NOT NULL,                     -- user-given, e.g. "Red WD drive"
    last_path  TEXT NOT NULL,                     -- where it was last seen on this machine
    last_host  TEXT NOT NULL,                     -- hostname, so another machine re-points without a prompt
    added_at   INTEGER NOT NULL,
    last_backup_at INTEGER,
    last_scrub_at  INTEGER
);
```

This is the first `user.db` migration: on open, version 1 becomes 2 by
creating the table and setting `user_version` (inside one transaction, with
a copy of the file as `user.db.bak-<stamp>` first). Version 2 is refused by
older picmover with the existing message.

Each library needs a stable identity so a target can tell "the same library again" from "a different library with the same folder name". The migration adds `meta(key, value)` to `user.db` with `library_id` = 16 random hex digits created once (and `created_at`). The set's `backup.db` stores that id in `meta.library_id`; a backup into an existing set whose `library_id` differs is refused (409 / exit 2) unless the user chooses another set name. `media.db` is derived and carries no identity.

## Operations (`internal/backup`)

All operations take a `Target` (validated path with marker) and a
`Library`, run under the library lock, and report progress through the
same callback shape the importer uses.

### Plan

Walk the library (catalog rows plus the tree walk for non-media files) and
read `backup.db`; classify each library file:

| Class | Condition | Action |
|---|---|---|
| current | same path, same hash | none |
| moved | hash present under another path, that path's source is gone | rename on target (no-replace), update row |
| changed | same path, different hash | move the old target file to `.picmover/previous/`, copy new |
| new | hash not on target | copy |
| extra | target row whose path and hash are not in the library | report only (prune removes) |

Hashes for non-media files come from size+mtime matching against the row;
a size or mtime change re-hashes the source. The plan is computed in
memory (136k rows in well under a second) and reported before copying:
counts and bytes per class.

### Backup run

Copies in catalog order (newest first, so a short run secures the recent
imports), with `CopyVerified` (temp file in the set's `.picmover/tmp/`,
fsync, hash read-back, rename into place with `RenameNoReplace`), 2
parallel copies by default (`--workers`), progress every 500 ms (files,
bytes, current file, rate, ETA), and a `backup.db` row per verified file in
batches of 64 (so an interruption loses at most the last batch of rows,
whose files are then re-verified by hash on the next run rather than
recopied). The run ends with the `user.db` and `media.db` snapshots and a
`runs` row. Exit status 0 when everything verified, 1 when some files
failed (listed), 2 when the target or library could not be used.

Free space is checked before copying: when the plan's bytes exceed the
target's free space, the run refuses with the numbers unless `--partial`
(copies newest first until full and reports what is left).

### Scrub

`Scrub(target, set, budget time.Duration | all)`: re-reads target files and
compares with `files.hash`; walks in `verified_at` ascending order (never
verified first) and stops at the budget, storing the cursor so the next
scrub continues. Also lists files on the target that are not in
`backup.db` ("unknown") and rows whose file is missing ("missing"). A
mismatch is recorded as `verified_at = NULL` plus a `damaged` entry in the
run's stats; when the library still has the file with the right hash, the
scrub repairs it (`--repair`, default on in the UI: copy again, then the
old damaged file goes to `.picmover/previous/`). Exit 0 clean, 1 damage
found, 2 unusable.

### Prune

Removes target files whose path is not in the library's current tree *and*
whose hash is not in the catalog (live or trashed), plus everything under
`.picmover/previous/` older than `--keep` (default 30 days). Prints the
list and the count; the CLI needs `--yes`, the API needs `confirm: true`
with the exact count returned by the preceding plan. Never touches
`user.db`/`media.db` snapshots.

### Restore

`Restore(target, set, library, mode)` where the library may be empty or
partial:
- `missing` (default): for every `files` row whose path is absent in the
  library (and whose hash is not present elsewhere in it), copy it back
  with verification; existing library files are never overwritten.
- `user-data`: copy `user.db` back when the library has none (or `--force`
  to replace, after backing the current one up as `.bak-<stamp>`).
- After the files, if the library has no `media.db`, run the in-place
  rebuild (record, no thumbs unless asked) so it opens; else leave the
  catalog alone and tell the user to run `verify`.
Exit codes as above.

## CLI

```
picmover targets <library>                         list targets (id, label, last path, last backup/scrub, reachable?)
picmover targets add <library> <folder> [--label]  create the marker when the folder has none (must be empty or a target), attach
picmover targets forget <library> <id>             detach (target untouched)
picmover backup <library> [<target-path>|--target <id>] [--all-targets] [--dry-run] [--partial] [--workers N] [--prune --yes] [--scrub <budget>]
picmover scrub <target-path> [<set>] [--budget 30m|--all] [--no-repair] [--library <path>]
picmover prune <library> <target> [--keep 30d] [--yes]
picmover restore <target-path> <set> <library> [--mode missing|user-data] [--force]
```

`<target-path>` is resolved to a marker; when a library's remembered target
is not at `last_path`, the CLI says which id it expects and how to point at
it. `--dry-run` prints the plan. Logs go to `<library>/.picmover/logs/`
and `<set>/.picmover/logs/`.

## HTTP API (library-scoped, same rules as step 4)

| Method | Path | Body → Result |
|---|---|---|
| GET | `/api/l/{lib}/backups` | `{targets: [{id, label, path, reachable, lastBackupAt, lastScrubAt, set: {files, bytes, behind: {new, changed, moved, bytes}, extras, damaged, unverified}}]}` (the set summary is read from `backup.db` and a cached plan; `behind` is computed at most every 60 s or on demand with `?plan=1`) |
| POST | `/api/l/{lib}/backups/targets` | `{path, label}` → 201 target; creates the marker when the folder is empty; 409 when the folder holds another library's set under the same name |
| POST | `/api/l/{lib}/backups/targets/{id}` | `{label?, path?}` → target (re-point: the marker id must match) |
| POST | `/api/l/{lib}/backups/targets/{id}/forget` | → `{}` |
| GET | `/api/l/{lib}/backups/targets/{id}/plan` | the plan: counts, bytes, and up to 500 example paths per class |
| GET | `/api/l/{lib}/backups/targets/{id}/runs` | `{runs: [{id, kind, startedAt, finishedAt, stats}]}` newest first, 50 |
| POST | `/api/l/{lib}/jobs` | kinds `backup` (`{target, partial?, workers?}`), `scrub` (`{target, budget?: seconds, repair?: true}`), `prune` (`{target, keepDays?, confirm: true, expected: n}`), `restore` (`{target, mode}`) |

Target paths given to the API: absolute, no NUL, no device paths; UNC
paths **are** allowed here (a NAS share is the normal case) because every
mutation is a POST under the step 3 rules, which a cross-site page cannot
send; the GET endpoints never take a path from the request, only ids.
Marker ids are 16 hex digits and are validated on every route.

Job progress events: `{phase: "plan"|"copy"|"snapshot"|"scrub"|"prune"|"restore", files, bytes, totalFiles, totalBytes, current, failed}`; the job's result carries the run stats.

## Frontend

- Nav gains **Backups**. The page lists the library's targets as cards:
  label, path (editable, with a Browse button that uses the directory
  browser and accepts UNC when typed), reachable or not, last backup and
  scrub, the set's size, and the "behind" numbers as the headline ("1,204
  files, 3.2 GB not backed up yet"). Buttons: Back up now, Scrub (with a
  budget select: 15 min, 1 h, all), Prune (opens the plan's extras list
  and asks for the count), Restore missing (shows what would come back),
  Forget. Add target: folder picker plus label.
- A run shows progress inline (files and bytes done of total, rate, ETA,
  current file) using the job SSE as the import does, with Cancel.
- A problems panel per target: damaged files from the last scrub (with
  "repaired" state), copy failures from the last run, unknown files.
- The Library page shows a one-line backup status ("backed up 2 h ago to
  Red WD drive; 0 behind") and a warning when no target exists or the last
  backup is older than 30 days.
- Workspace: the Libraries page shows the same one-liner per card.

## Security and safety rules

- A target folder must be outside every library and the workspace root,
  and a library must be outside every target; checked lexically and after
  `EvalSymlinks`.
- The marker is required for every operation; a target path without a
  marker is only accepted by *add* on an empty folder.
- Copies write to the set's temp dir and rename with no-replace; the
  previous content of a changed file is kept until prune. Prune and restore
  never overwrite; prune needs the confirmed count; nothing deletes in the
  library.
- The library lock is held for the whole run (backup reads the tree while
  nothing moves). Scrub of a target does not need the library lock unless
  repairing.
- Errors to clients carry relative paths only.

## Tests

- `backup`: marker create/validate; plan classes incl. moved, changed,
  extra, non-media files, trashed files; copy verification failure handling;
  resume after a kill mid-run (rows vs files consistency); free-space
  refusal and `--partial`; scrub budget and cursor, damage detection and
  repair, unknown and missing; prune rules incl. `previous/` age; restore
  missing without overwriting, user-data mode, rebuild afterwards; symlinks
  skipped.
- `userdata`: v1 → v2 migration with backup file; refusal of v3.
- `server`: endpoints, id validation, UNC accepted on POST only, job kinds
  and progress, confirm/expected for prune, lock interplay with import.
- `cmd`: exit codes, dry-run output, target resolution messages.
- `web`: backups state, progress rendering, prune confirmation count,
  Library and Libraries status lines.

## Performance targets

- Plan for 136k rows: < 1 s. Backup throughput bounded by the slower disk;
  two workers keep a USB 3 drive saturated. Scrub reads at disk speed with a
  1 MiB buffer. `GET /backups` warm: < 20 ms.

## Later

Cloud/object targets behind the same `Target` interface; scheduled runs
from inside `serve`; cross-library dedup on a shared target.
