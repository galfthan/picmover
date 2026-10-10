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

## As implemented (backend)

Everything outside `web/`. Notes that matter to the client are marked
**(frontend)**; the shapes the frontend asked for during the build are
matched exactly. Where this section is silent, the contract holds.

### API shapes, exactly

- **(frontend)** Times (`lastBackupAt`, `lastScrubAt`, a run's
  `startedAt`/`finishedAt`) are **Unix seconds** as numbers, `null` when
  unknown or not finished.
- **(frontend)** `GET /api/l/{lib}/backups[?plan=1]` → `{targets: [target]}`
  in the order the targets were added. `target` = `{id, label, path,
  reachable, lastBackupAt, lastScrubAt, set, error?}`; `path` is the folder
  the target was last seen at on this machine. `set` is `null` when the
  target is unreachable, else `{name, files, bytes, behind: {new, changed,
  moved, bytes} | null, extras, damaged, unverified}`: `files`/`bytes`/
  `unverified` (rows never scrubbed or found damaged) from `backup.db`
  (zeros before the first backup); `behind` from the plan (`bytes` = new +
  changed bytes, what a run copies), `extras` = the plan's `extra.count`;
  `damaged` = damaged and not repaired in the last scrub. The plan behind
  `behind`/`extras` is cached per library and target for 60 s;
  `?plan=1` computes it again; any job on the library, a target add,
  update or forget drops the cache. `error` (reachable targets only) is set
  when the set cannot be shown, e.g. the set name holds another library's
  backup; `behind` is then `null`. Addition: `set.name` (the set folder).
- **(frontend)** `POST /api/l/{lib}/backups/targets {path, label?, set?}` →
  201 target. `path` rules below; an existing target (marker present) is
  attached as it is, an empty folder gets a new marker. `label` defaults to
  the folder's name; 1-64 runes, no control characters (400 `label: ...`).
  `set` (addition) chooses the set folder name; without it the set is the
  one on the target that already belongs to this library (found by
  `library_id`), else the library's folder name. 409 `{error: "the target
  already holds a backup of another library under this name; choose
  another set name"}` when that set belongs to another library; nothing is
  written then. Adding an attached target again updates its label, path
  and host. 400 texts: `path: path must be absolute`, `path: path contains
  a NUL byte`, `path: device paths are not supported`, `path: a backup
  target must be outside every library and outside the workspace folder`,
  `the folder is not empty and is not a picmover backup target; ...`,
  `the target path is not a folder`, `the folder does not exist`,
  `permission denied`, marker errors.
- **(frontend)** `POST /api/l/{lib}/backups/targets/{id} {label?, path?}` →
  200 target; a new `path` must hold a marker with the same id (409 `the
  folder is another backup target (its marker has another id)`).
- **(frontend)** `POST .../targets/{id}/forget` → `{}`; the target is not
  touched.
- **(frontend)** `GET .../targets/{id}/plan[?keepDays=N]` → `{new, changed,
  moved, current, extra, setName, symlinks, missing, keepDays}`; each class
  is `{count, bytes, paths}` with at most 500 set-relative paths (strings;
  for `moved` `{from, to}` objects). `extra` is exactly what a prune with
  that keep window removes: the set's files whose path and content the
  library no longer has, plus earlier versions under
  `.picmover/previous/` older than the keep window (default 30 days), so
  `extra.count` is the `expected` of a prune. `symlinks` and `missing`
  (catalog rows whose file is missing in the library) are counts. The plan
  is computed on every call. 409 when the target is unreachable or the set
  is another library's.
- **(frontend)** `GET .../targets/{id}/runs` → `{runs: [{id, kind,
  startedAt, finishedAt, stats}]}`, newest first, at most 50; `[]` before
  the first run; 409 when unreachable.
- Ids: a malformed `{id}` (not 16 lower-case hex digits) is **400**
  `invalid target id: ...`, an unknown one 404 `no such target`, on every
  route. Every POST is under the step 3 rules (JSON content type,
  `X-Picmover: 1`, origin checks).
- **(frontend)** Jobs, `POST /api/l/{lib}/jobs`:
  - `{kind: "backup", target, partial?, workers?}` (`workers` 0-16, 0 = 2);
  - `{kind: "scrub", target, budget?: seconds, repair?: bool}` (no or 0
    `budget`: the whole set; `repair` defaults to true);
  - `{kind: "prune", target, keepDays?, confirm: true, expected: n}`:
    400 without `confirm` or `expected`; the plan is computed when the job
    is requested and a different `extra.count` is 409 `{error: "the files
    to remove changed: the plan now lists N, not M; review them again"}`
    without starting; the job checks the count again before removing;
  - `{kind: "restore", target, mode?: "missing"}`.
  Before starting: 400 malformed target or field, 404 unknown target, 409
  unreachable target / another library's set / no backup of this library
  on the target yet (scrub, prune, restore), and the usual 409 while any
  job runs (one job at a time, imports included).
- **(frontend)** Job object: backup kinds carry `target` (the marker id)
  next to `library`, in `GET /api/jobs`, `GET /api/l/{lib}/jobs`, replays
  and SSE `state`/`done`; restore jobs carry `mode`; `progress` is the
  backup progress object (below); `result` is the run's stats once the job
  has finished (absent before). `stats` (the importer counters) stays
  present and zero.
- **(frontend)** Progress events (`event: progress`, coalesced, every
  500 ms): top level `{phase, files, bytes, totalFiles, totalBytes,
  current, failed, rate, eta}`: `phase` `plan|copy|snapshot|scrub|prune|
  restore`, `failed` a count, `current` a set-relative path; `rate`
  (bytes/s since the phase began) and `eta` (seconds, -1 unknown) are
  additions. Files that fail are also sent as `result` events
  `{status: "error", source: <relative path>, dest: "", message}`
  (scrub: unrepaired damaged files with message `damaged`).
- **(frontend)** Exit codes and states: 0 `done`; 1 `failed` with a message
  (backup: files failed, `--partial` left files or a snapshot failed;
  scrub: damage or missing files found, repaired ones included; prune and
  restore: files failed); 2 `failed` (target or library unusable: not
  reachable, another library's set, not enough space without `partial`,
  count changed); 130 `cancelled`. Messages carry no absolute paths.
- **(frontend)** Run stats (`result`, and `stats` in `runs`); every list
  is capped at 500 with `truncated: true` on the stats when one was:
  - backup: `{copied, moved, changed, bytes, failed: [{path, error}]}` plus
    `current, new, extra, adopted, refreshed, failedCount, partial,
    leftFiles, leftBytes, symlinkCount, symlinks, missingCount, missing,
    invalidCount, userSnapshot, mediaSnapshot, snapshotError?, cancelled,
    seconds`. `moved` = files renamed on the target, `changed`/`new` the
    plan's counts, `copied` new and changed files copied.
  - scrub: `{checked, bytes, damaged: [{path, repaired, error?}], unknown:
    [paths], missing: [paths]}` plus `ok, damagedCount, repaired,
    missingCount, unknownCount, passComplete, remaining, cancelled,
    seconds`.
  - prune: `{removed, bytes}` plus `failed: [{path, error}], seconds`.
  - restore: `{restored, bytes, failed: [{path, error}]}` plus `present,
    elsewhere, failedCount, userDB, needsRebuild, cancelled, seconds`.
- **(frontend)** `GET /api/libraries`: a library object carries `backup:
  {targets, lastBackupAt, label?, behind?}` when it has targets
  (omitted otherwise, and for libraries in state `error`): `targets` and
  `lastBackupAt` (the newest backup over all targets) from user.db, read
  through a read-only, immutable open for libraries that are not open;
  `label` (addition) is that target's label; `behind` (files: new +
  changed + moved) only when an open library has a cached plan for that
  target.

### Target path rules (POST only)

`server.ValidTargetPath`: not empty, absolute, no NUL, not a device path
(`\\?\`, `\\.\`, `//?/`, `//./`, `\??\`). UNC paths (`\\nas\share\x`) are
accepted here, unlike `/api/fs` and import sources. Then lexically and
with links resolved (`EvalSymlinks` of the path and of each root) the
folder must not be, lie inside or contain the workspace root (workspace
mode: also any listed folder), or the library root (single mode); then
`backup.CheckSeparate` against the library. GET routes take ids only.

### Package `internal/backup`

- `marker.go`: `ValidID`, `NewID`, `ReadMarker` (≤ 4 KiB JSON, regular
  file, `format` 1; a higher format is `ErrNewerMarker`), `OpenTarget`,
  `InitTarget` (marker written to a temp file, fsynced, renamed without
  replacing; only in an empty folder, where "empty" ignores file system
  bookkeeping: `System Volume Information`, `$RECYCLE.BIN`, `lost+found`,
  `.Trashes`, `.Trash-*`, `.Spotlight-V100`, `.fseventsd`, `.DS_Store`,
  `desktop.ini`, `Thumbs.db`, `._*`), `CheckSeparate`.
- `set.go`: `backup.db` exactly as in the contract (journal_mode DELETE,
  synchronous FULL, busy_timeout 10 s); `meta` keys `name`, `library_id`,
  `scrub_cursor`. `OpenSet`, `Sets`, `SetExists`, `FindSet` (by
  library_id), `SetOwner`, `ResolveSetName`, `Claim` (a set without owner
  takes the library's id; another id is `ErrOtherLibrary`), `Files`,
  `Summary`, `Runs`, `LastRun`. Rows whose path is not a set path (empty,
  `.`/`..` components, `\`, `:`, NUL, under `.picmover` other than
  `.picmover/trash/`, a database name at the root) are ignored. Every
  write and removal on the target first checks that the parent folder,
  with links resolved, is inside the set (`inSet`), so a link planted on
  the target is never followed.
- `scan.go`/`plan.go`: `ScanLibrary` (catalog rows newest first, `taken_at
  DESC, id DESC`, undated last, matched against a walk of the tree; other
  regular files newest first by mtime; trashed rows at
  `.picmover/trash/<rel>`; skipped: `.picmover` except the trash, every
  `IsInternal` file at the root, links (listed), other non-regular files;
  catalog rows with a non-library path are counted as invalid),
  `MakePlan` (pure; NOCASE keys as SQLite folds them), `PlanFor`. A
  non-media file whose size or mtime changed is `changed`; the run hashes
  it and, when the content is the same, only refreshes the row
  (`refreshed`).
- `run.go`: `Run`. Order: claim the set, plan, clean picmover's own temp
  names in `<set>/.picmover/tmp` (`copy-*`, `stage-*`, `snap-*`; left by a
  killed run), free space, runs row, renames, copies, snapshots, end of
  run. Copies use `library.CopyReadBack` (below) into the set's tmp folder.
  A changed file's new content is staged and verified first; only then
  does the old file move to `previous/<rel>.<unix>` and the new one take
  its place. A destination that exists without a row (a killed run) is
  hashed: the right content is adopted (`adopted`), anything else is moved
  to `previous/` first. Rows are written 64 per transaction (and at least
  every 5 s); a cancelled run writes the rows of the files done and its
  runs row. The partial budget is taken in copy order by the feeder, so
  the newest files get the space whatever the workers' timing.
- `scrub.go`, `prune.go`, `restore.go`: below.
- Free space: `statfs` (Linux, macOS, FreeBSD), `GetDiskFreeSpaceEx`
  (Windows), unknown elsewhere (no check).

`internal/library`: `CopyReadBack(ctx, src, dst, tmpDir, want *uint64)`
(write, fsync, close, **read the temp file back and hash it**, compare with
the hash of the bytes read and with `want`, refuse a source whose size or
mtime changed during the copy, set the source mtime, `RenameNoReplace`,
sync the folder), `HashFileContext`, `SyncDir`, `IsUserDBBackup`
(`user.db.bak-<stamp>[-wal|-shm|-journal]` is now a database name:
internal, skipped by imports and backups). The importer's `CopyVerified`
is unchanged.

`internal/catalog`: `PathRows` (hash, size, rel_path, trashed; newest
first) and `Snapshot` (`VACUUM INTO`).

`internal/userdata`: schema 2 = schema 1 + `backup_targets` (the
contract's columns plus `set_name TEXT NOT NULL DEFAULT ''`) + `meta(key,
value)` with `library_id` (16 random hex digits) and `created_at`. A fresh
file is created at version 2 directly. Version 1 is migrated on `Open`:
`VACUUM INTO user.db.bak-<YYYYMMDD-HHMMSS>` (never replacing a file), then
one transaction (tables, identity, `user_version = 2`); a failed backup
leaves version 1 untouched. `CheckFile` accepts 1 and 2; 3+ is refused with
the existing `VersionError` ("user.db has schema version 3; this picmover
supports 2: upgrade picmover"). Typed API: `LibraryID`, `CreatedAt`,
`BackupTargets`, `BackupTarget`, `AddBackupTarget` (re-adding updates
label, path and host), `UpdateBackupTarget(TargetUpdate{Label, LastPath,
LastHost, SetName})`, `ForgetBackupTarget`, `MarkBackup`, `MarkScrub`,
`BackupSummary`, `PeekBackupSummary` (read-only), `Snapshot`, `Migrated`,
`CleanLabel`.

### Deviations and additions

- **Set names.** The contract's "unless the user chooses another set name"
  needed somewhere to keep the choice: `backup_targets.set_name` (user.db),
  `set` in `POST .../targets`, `--set` on `targets add`. The set is also
  found by `library_id`, so renaming the library folder keeps its set.
- **user.db snapshot history.** The previous `user.db` snapshot is moved to
  `previous/user.db.<unix>` (pruned with the keep window) instead of being
  replaced: user data is the one thing a rebuild cannot recreate. The
  `media.db` snapshot is replaced (it is derived).
- **Exit codes.** Usage errors of the backup commands exit 2 (not 1), so a
  scheduled job never reads them as "some files failed". `--partial`
  leaving files is exit 1 (the backup is incomplete).
- **Restore in the server** supports `mode: "missing"` only: `user-data`
  replaces the `user.db` the server holds open, so the API answers 400 and
  names the CLI (`picmover restore --mode user-data` while the library is
  not served). Missing mode also restores `user.db` when the library has
  none (never in the server, which always has one open). A restore into an
  open library leaves its catalog alone (it has a `media.db`); restored
  files the catalog does not list show up in `verify` as orphans.
- **Restore "present elsewhere"** is decided by content, not the catalog:
  library files of the same size are hashed (lazily, each once), so it
  also works for a library without `media.db`.
- **Missing target files.** A backup run trusts its rows (it does not stat
  136k files on the target); a file deleted from the target is found by the
  next scrub (listed as missing, and copied again with repair).
- **Scrub passes.** The cursor (`meta.scrub_cursor` = `{pass, v, p}`) keeps
  a pass's position in `(coalesce(verified_at, -1), rel_path)` order;
  rows verified at or after the pass start are left for the next pass. A
  new pass starts one second after "now" (verified_at has second
  resolution), so a pass started right after another reads everything
  again. A scrub reads at least one file whatever the budget. Missing and
  unknown files are found by walking the set on every scrub.
- **Prune** removes folders its removals left empty (not the set,
  `.picmover`, `previous` or the trash folder itself). Unknown files (no
  row) are never pruned. A row whose file is already gone is dropped and
  counted as removed.
- **The library lock** is held by `backup`, `prune`, `restore` and `scrub
  --library` (unless `--no-repair`); `targets`, `backup --dry-run`, `restore
  --dry-run` and plain `scrub` take none. In the server the job runs under
  the server's lock; trash operations are not blocked during a backup (a
  file moved meanwhile fails or is picked up as moved by the next run).
- **CLI additions:** `--quiet` (no status line) on backup, scrub and
  restore; `--dry-run` and `--thumbs` (generate thumbnails in the rebuild
  after a restore) on `restore`; `prune <library> <target>` takes a folder
  or an id; `scrub` without `--budget` reads the whole set (`--all` says so
  explicitly); `--keep` takes `30d` or a Go duration. When a folder given
  to `backup` holds a marker the library does not know yet, it is attached
  (label = folder name) and the run goes on.
- **Logs:** `<library>/.picmover/logs/{backup,scrub,prune,restore}_<time>.log`
  (the job's `logPath`) and the same names in `<set>/.picmover/logs/`.

### Tests

`internal/backup` (`backup_test.go`): marker create/read/validate incl.
system entries, non-empty folder, bad/newer markers, `ValidID`;
`CheckSeparate` incl. a link into the library; `MakePlan` classes (current,
moved, changed, new, extra, non-media by size and mtime, NOCASE, a hash
used by one move only, extras excluded when the catalog or a non-media
file has the hash, copy order); a full run with a trashed file, a symlink
(skipped and listed), internal files and a catalog backup (not copied),
snapshots, mtimes, runs row; an incremental run (nothing copied, previous
user.db snapshot kept); move, change and delete in the library (rename on
the target, previous version kept, nothing removed); touch without change
(refreshed); another library's set refused, `FindSet`, `SetOwner`; a
verification failure (catalog hash mismatch: listed with a path-free
text, no row, nothing at the destination, no temp files) and a file
missing from the library; **a kill mid-run** (`crashAfter`: exactly the
first 64-row batch survives, every row's file is intact on the target, the
run row is unfinished, the next run adopts the unrecorded files by hash
and copies the rest); free-space refusal and `--partial` (newest file
first); scrub budget and cursor over five runs, damage, missing and
unknown, repair with the damaged copy kept, clean afterwards; prune rules
(extras, trashed content kept, previous/ by age, wrong count refused,
snapshots kept, empty folders removed, a planted link not followed);
restore into an empty library (dry run, files, user.db with its ratings,
rebuild needed), missing mode never overwriting and skipping content
present elsewhere, user-data mode with and without force; set name and
set path rules. `bench_test.go`: `MakePlan` for 136k rows: 40 ms (Ryzen 7
7800X3D); the library walk comes on top and is bounded by the file system.
`internal/library/copyfile_test.go` (`CopyReadBack`: success with mtime,
no replace, wrong hash, a read-back mismatch, no temp files left, a
folder refused). `internal/userdata/targets_test.go` (v1 → v2 migration
with the backup copy, id kept on reopen and no second backup, a refused
migration leaves version 1, fresh v2 identity, target CRUD incl. labels,
set names, marks, summary, read-only peek, snapshot); version 3 refusal
in the existing tests (moved from 2). `internal/server/backup_test.go`
(endpoints and shapes, the backup, scrub and restore jobs with results and
Unix-second times, the libraries one-liner, rename and forget; prune
confirm/expected 400/409 and the job; path rules incl. relative, NUL,
device paths, inside/containing the library and the workspace, a link into
the library, non-empty and missing folders, no path in answers, nothing
written on refusal; another library's set 409 and `set`; re-pointing with
the same and another id; unreachable target in the listing and for jobs;
id validation on every route and job; mutation headers; `ValidTargetPath`
with UNC accepted only there; a backup job refused while an import runs;
the progress event and job object shape). `cmd/backup_test.go` (targets
list/add/forget, exit codes 0/1/2 incl. usage errors, dry-run output,
target resolution messages for a missing marker, a moved drive and a
re-pointed one, scrub damage/repair/budget, prune with and without
`--yes`, restore with rebuild, user-data with and without `--force`, the
library lock refusal, another library's set and `--set`).

Not measured: the 136k-row plan end to end on real disks (only the
classification), throughput on a USB drive, `GET /backups` warm (it is a
cache read plus one `backup.db` summary per target).

## As implemented (frontend)

Built against this contract while the Go side was written in parallel, so
it is tested against the fake server (`web/src/testing/fakes.ts`), not the
real one. Everything in "Frontend" is implemented.

### Files

`lib/api.ts` (backup types, `LibApi.getBackups/addTarget/updateTarget/
forgetTarget/getPlan/getRuns`, the four job request bodies, tolerant
normalizers `toTarget`, `toPlan`, `pathOf`), `lib/backupinfo.ts` (pure:
headline, status line and staleness, run problems, run summaries,
`RateMeter`/`etaMs`, phase labels, `targetPathProblem`),
`lib/backups.svelte.ts` (per-library store: targets, plan cache, runs, job
→ target map, `refresh()`), `pages/Backups.svelte` (cards, Add target,
status line, the library's path), `components/TargetCard.svelte`,
`components/BackupProgress.svelte`. Touched: `App.svelte` (nav **Backups**
between Import and Library, route `#/l/<id>/backups`), `lib/routes.ts`,
`lib/jobtext.ts` (titles and outcomes of the backup kinds),
`components/JobPill.svelte` ("Backing up 12 of 40 files…", opens the
Backups page), `components/FolderPicker.svelte` (labels are props),
`pages/Library.svelte` and `pages/Libraries.svelte` (status lines).

### Shapes relied on

- `GET …/backups[?plan=1]` → `{targets: [...]}` exactly as in the API
  table. Missing fields are defaulted (`reachable` false unless `true`,
  `set` and `behind` null when absent or not objects). `lastBackupAt` and
  `lastScrubAt` may be Unix seconds, RFC 3339 or a wall-clock string in the
  server's zone (`parseWhen`). The page calls the plain GET on arrival and
  `?plan=1` once when a reachable target has no `behind`, after every job,
  after re-pointing or adding a target, and on "Check now".
- `POST …/backups/targets` and `POST …/backups/targets/{id}` answer the
  target object (also accepted wrapped as `{target}`); errors are shown
  from `error`. The update sends only the changed fields.
- **Plan** (`GET …/targets/{id}/plan`; the contract leaves the JSON open):
  one object per class, `{new, changed, moved, current, extra}`, each
  `{count, bytes, paths}`. Also accepted: the classes under `classes` or
  `plan`, `extras` for `extra`, `examples` for `paths`, example entries as
  strings or objects with `path`, `relPath`, or `{from, to}` (shown as
  "from → to"), and a bare number as the count.
- **Prune** sends `{kind: "prune", target, confirm: true, expected}` with
  `expected` = the `extra.count` of a plan fetched when the dialog opened
  (never the cached one), after the user typed that number. The client
  assumes the server's prune count is the plan's `extra.count`; if
  `.picmover/previous/` entries are counted separately the backend must add
  them to `extra` or the 409 will say so.
- **Restore missing** previews the same `extra` class ("on the target but
  not in the library": path and hash absent, which is what restore
  `missing` copies back) and sends `{kind: "restore", target, mode:
  "missing"}`. `user-data` mode is CLI-only for now.
- **Scrub** sends `budget` in seconds (900, 3600) or omits it for "All",
  and always `repair: true`. Backup sends `{kind, target}` only.
- **Progress**: an SSE `progress` event whose data is `{phase, files,
  bytes, totalFiles, totalBytes, current, failed}` at the top level, under
  `progress`, or under `stats` (all three are read); a job object's
  `progress` field (listings, state events) too. It is kept as `job.phase`
  like the similar job's. `failed` is a count. Rate and ETA are computed in
  the browser from `bytes` over a 20 s window; the fraction uses bytes when
  `totalBytes` is known, else files.
- **Job ↔ target**: the job object's `target` is used when present; else
  the store remembers which target each job started from this page was for.
  A running backup-kind job with no known target (started in another tab
  or after a reload) is shown at the top of the page instead of in a card.
- **Exit codes** of the backup kinds (`exitCode`): 0 clean, 1 partial
  (some files failed; scrub: damage found), shown as a warning even when
  the job's `state` is `failed`; 2 or `failed` without a code as an error;
  130 / `cancelled` as cancelled. The job's `stats` (the run stats) feed
  the one-line summary.
- **Run stats** (`GET …/runs`, `stats` as an object or as JSON text). The
  contract leaves the counters open; the UI reads: backup `copied`,
  `moved`, `changed`, `bytes`, `failed` (or `failures`); scrub `checked`,
  `bytes`, `damaged`, `repaired`, `unknown`, `missing`; prune `removed`,
  `bytes`; restore `restored`, `bytes`, `failed`. Each of the problem
  fields may be a count or a list of paths or of `{path, error?,
  repaired?}` objects; `repaired` may also be a separate list of paths. The
  problems panel takes `damaged`, `unknown` and `missing` from the newest
  scrub run and `failed` from the newest backup run.
- **Workspace listing**: a library may carry `backup: {lastBackupAt,
  behind, targets, label?}` where `behind` is a count or a `behind` object
  and `targets` a count or a list. Without it the card shows no line.

### Deviations and additions

- The status line says "Backed up 2 hours ago to Red WD drive; 0 behind"
  (`Intl.RelativeTimeFormat` wording, not "2 h"); stale lines get a ⚠ and
  "(older than 30 days)". Without a target: "No backup target yet"; with
  targets but no backup: "Never backed up (N targets)". The Library page
  line links to the Backups page ("Add a backup target" when there is
  none). The "behind" count is that of the target with the newest backup.
- The card headline counts new + changed + moved files and `behind.bytes`
  ("1,204 files, 3.2 GiB not backed up yet", binary units as everywhere in
  the UI); other states: "Up to date", "Not checked yet", "Not reachable",
  "Nothing backed up here yet".
- Label and path are edited together behind an Edit button (the contract
  only asks for the path); Browse opens the directory browser in a dialog,
  and the field takes any absolute path including `\\server\share`
  (checked in the browser with the server's rules: absolute, no NUL, no
  device paths; the directory browser itself still refuses UNC).
- Add target defaults the label to the chosen folder's name and requires
  one. A server error stays in the dialog until a field changes.
- Forget asks inline (ConfirmButton) and says the files stay.
- While any job runs, every card's actions are disabled (one job per
  process); an unreachable target only offers Edit and Forget.
- The last finished job of a target (this server process) is shown under
  its actions with the job's outcome text.
- The problems panel lists up to 100 entries per kind, then "and N more;
  see the run log".

### Tests

`make web-check`: svelte-check (0 errors, 0 warnings), 171 vitest tests
in 18 files (32 new), plus `npm run build` without warnings.

- `lib/backupinfo.test.ts`: headline variants (incl. 1,204 files, 3.2
  GiB), status line from targets (newest target, behind), no target, never,
  older than 30 days vs 29 days, every time form; listing summary absent,
  count and object `behind`, `targets` as list; run problems (newest scrub
  and backup, separate `repaired` list, counts only), run summaries; rate
  window, reset on a lower count, ETA math and wording, fraction by bytes
  then files; target path checks (POSIX, drive, UNC accepted; relative,
  `E:x`, bare `\\nas`, device and NUL refused); backup exit-code outcomes.
- `lib/api.test.ts`: `?plan=1`, defaults, add/update/forget URLs and
  bodies, invalid ids never reach `fetch`, plan normalization (`examples`,
  `{from, to}`, missing classes), runs with JSON-text stats, the four job
  bodies.
- `lib/jobs.test.ts`: backup progress from a listing's `progress`, and SSE
  progress at the top level, under `progress` and under `stats`.
- `Backups.test.ts` (whole app, fake server): cards with headline, facts,
  reachable state and problems (damaged with repaired / not repaired, copy
  failures, unknown files), the automatic plan request, the status line;
  Back up now with inline progress (files, bytes, current file, progress
  bar, rate 2.0 MiB/s and "about 10s left" after 2 s, failed count), the
  pill, Cancel, and the reload of plan and runs after `done`; scrub budgets
  (900, 3600, omitted); prune dialog with the extras list, the typed count,
  a 409 when the plan changed, and the fresh count on reopening; restore
  preview and "Nothing is missing"; Add target with Browse, typed UNC, a
  409 shown in the dialog, focus and Escape; Edit path with a marker
  mismatch, rename sending only the label, Forget; the empty state; the
  Library page line (recent, older than 30 days, no target); the Libraries
  page lines (recent, stale, absent).
- `App.test.ts`: the nav gained Backups.
