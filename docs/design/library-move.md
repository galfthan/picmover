# Moving photos between libraries

Status: contract, written before implementation. "As implemented"
sections go at the end, backend then frontend, as in the other design
documents.

## Goal

Select photos in one library of a workspace and move them to another
library: the files, their catalog metadata, their thumbnails and their
user data (rating, tags, album memberships). Afterwards the photos are
in the target library exactly as if they had always been there, and gone
from the source library's grid.

"Safely" means the same promises picmover makes everywhere else:

- No file is ever replaced, and nothing is deleted. A copy across volumes
  is read back and checked against the catalog hash before anything
  else happens, and the source copy then goes to the **source
  library's trash**, not away. Only "Empty trash" deletes it, as always.
- Every state a crash can leave behind is either harmless (an extra copy)
  or finished by the next run from a journal. A photo is never in
  neither library.
- Each file succeeds or fails on its own; a failure never stops the
  batch and never leaves that file half-moved.

## Decisions (from the request)

| Question | Decision |
|---|---|
| Source file after a cross-volume copy | Moved to the source library's trash; emptying the trash deletes it |
| Album memberships | Recreated in the target by album name (created when missing) |
| Entry points | Web UI (selection bar, lightbox) **and** CLI `picmover move` |

## Non-goals

- Copying to another library while keeping the original live (a "copy
  to library" action). The protocol below allows it later: it is the
  move without the source step.
- Moving between workspaces, or to a folder that is not a library.
- Moving trashed photos (restore them first) or whole albums as objects
  (album *memberships* move; the album in the source library stays).
- Cross-library duplicate detection beyond "this content is already in
  the target" (see `management.md`, "Later").

## Terminology

The UI says **Move to library**. Code says *transfer* (job kind
`transfer`, package `internal/transfer`, import mode `transfer`) so it is
never confused with `import --move`. The CLI command is `picmover move`.
*Source* (A) and *target* (B) are the two libraries.

## Layout

```
internal/transfer/          Plan, Run, Recover; the per-file protocol; journal; user data merge
internal/transfer/journal.go  <B>/.picmover/transfers/<id>.journal (append-only, fsynced)
internal/catalog/manage.go  InsertMoved (copy of a row into another catalog), RemoveMoved
internal/userdata/          ImportModeTransfer; MergeFrom(ctx, src *DB, hashes) (ratings, tags, albums, ignored pairs)
internal/thumbs/store.go    CopyFrom(src *Store, hash) (renditions, no regeneration)
internal/server/api_transfer.go  plan endpoint; job kind transfer; recovery on open
cmd/move.go                 picmover move
web/src/components/MoveDialog.svelte
web/src/components/SelectionBar.svelte, Lightbox.svelte (entry points)
```

## What moves

| Thing | Where in A | What happens in B | What happens in A |
|---|---|---|---|
| File | `<A>/<rel>` | placed at `<B>/<rel>` (or a free `UniqueName` variant) | same volume: gone (one rename); other volume: in A's trash |
| Catalog row | `media.db` | inserted with A's metadata, `imported_at` = the move time, new `import_session_id` | same volume: row removed (`Remove`, drops similar pairs); other volume: `deleted_at` set (it is a trashed row) |
| Renditions | thumbs store, by hash | copied (thumb, large, preview, size, `phash`, `thumb_state`) | same volume: deleted; other volume: kept (the trash page shows them) |
| Rating | `user.db.ratings` | set when B has none for the hash; B's own rating wins (it exists only for content that is or was in B) | kept (never deleted; see "Rows are never deleted" in `management.md`) |
| Tags | `media_tags` by tag id | by tag **name** (case-insensitive), created in B when missing | kept |
| Albums | `album_items` | the B album with exactly the same name (lowest id when several), created when missing; position appended | kept (the album in A stops showing it, since it lists live rows only) |
| Ignored similar pairs | `similar_ignored` | copied for pairs whose two hashes are both in B after the move | kept |
| Import history | `imports` | **one new record** for the whole run, mode `transfer`, source = A's root and name, items `imported` / `existing` / `restored` | none (A's old records keep their items, as for trashed files) |

Metadata is never re-read from the file: B's row is A's row, so a
`rebuild` of B later produces the same values (it reads the same bytes).
The row keeps `original_path` and `original_mtime` from A.

## Per-file protocol

Preconditions for the whole run: both libraries open and locked by this
process, neither rebuilding, A ≠ B (different `library_id` **and**
different resolved root), the move journal written (below).

For each selected hash `h`, read A's row `m` (must be live, with a valid
rel path), then exactly one of:

**1. Already live in B** (B has a live row for `h`): no file is placed.
Verify B's file exists and is a regular file (not hashed). Then the
*source step*: A's file to A's trash (always, even on one volume: this
is the "safe" answer to "there are two copies, drop one"). Item status
`existing`.

**2. Trashed in B**: B's trashed copy is restored with
`importer.RestoreFromTrash` (the one restore path), then the source step.
Item status `restored`.

**3. Not in B, same volume** (the default first attempt):
1. Hash A's file (`HashFileContext`). Must equal `h`, else fail
   "the file does not match the catalog" and leave everything.
2. Choose `rel'` = `m.RelPath`, or, when that is taken on B's disk or
   in B's catalog, the first free `UniqueName` variant.
3. Journal `begin h rel rel' rename`.
4. `RenameNoReplace(<A>/rel, <B>/rel')`. `EXDEV` → go to case 4 with
   the same `rel'` (the journal line is superseded by case 4's).
5. Insert B's row (in the batch transaction, see "Batches").
   On failure: rename back (no-replace); if that fails too the journal
   line stays open and recovery finishes it.
6. A: remove the row, delete the renditions.
7. Journal `done h`.

**4. Not in B, other volume** (known from the plan, or after `EXDEV`):
1. Choose `rel'` as in case 3; journal `begin h rel rel' copy`.
2. `CopyReadBackDeferSync(<A>/rel, <B>/rel', B.TempDir(), &h)`: copies
   via B's temp dir, fails unless the bytes read from A hash to `h` and
   the fsynced copy reads back (cache dropped) as `h`; renamed into place
   without replacing; source size or mtime changing fails it.
3. Sync the folder, insert B's row (batch transaction).
4. The source step: A's file to A's trash (`MoveToTrash`), `SetDeleted`
   in A's catalog, `Trash` in A's `user.db`.
5. Journal `done h`.

The source step's failure after B's row committed is not a file
failure: the photo is in B, and A still shows it. It is reported as
`moved, but the original could not be moved to the trash in <A>` and the
journal line stays open, so recovery retries it.

Errors use `fileOpError` texts (no paths, no raw OS errors) plus:
`already in the trash in <A>`, `the file does not match the catalog`,
`not enough free space in <B>`, `the target library is <A> itself`.

### Batches

Rows are inserted in B in transactions of up to 200 files or 10 s,
after one `SyncDir` per touched folder (as the backup does). A's side
of a batch runs after B's transaction commits. User data is merged per
batch (one `user.db` transaction in B). The journal is fsynced before
each file op and after each batch commit.

## The journal and recovery

`<B>/.picmover/transfers/<id>.journal`, where `<id>` is the B import
record id. First line: JSON header `{v:1, id, sourceLibraryId,
sourceRoot, targetLibraryId, startedAt}`. Then one line per event:
`begin <hash> <rel> <rel'> rename|copy`, `done <hash>`, `fail <hash>`,
and a final `end`. Each line is written with one `write` and `fsync`ed.
A torn last line is ignored.

A journal without `end` is an **interrupted move**. It is found when B
is opened (server) or by any `picmover move` touching B, and finished by
`transfer.Recover`, which needs A open too. For each `begin` without
`done`/`fail`, it looks at the state and always goes **forward** when the
file reached B, back otherwise:

| B row for h | file at `<B>/rel'` (hash h) | A's file | Action |
|---|---|---|---|
| live | yes | present | source step (to A's trash), merge user data |
| live | yes | missing | remove A's row if live (rename case), merge user data |
| none | yes | any | insert B's row from A's row, then as above |
| none | no / wrong hash | present | nothing happened: `fail h` (a wrong-hash file at `rel'` is left alone and reported) |
| none | no | missing | **lost-track case**: report loudly, change nothing (cannot happen with the order above; listed so it is tested) |

B's import record (outcome `running`) is closed by recovery with the
items the journal proves, not by `closeCrashedImports` (which skips mode
`transfer`). When A cannot be opened (missing, busy), recovery waits;
the Libraries page shows B with "a move from A was interrupted", and
the CLI says `picmover move --resume <B>`. A finished journal is kept
for 30 days, then removed on open.

## Pre-flight (plan)

Before anything moves, a plan reads both catalogs and the file system
(no writes) and returns:

- counts and bytes of: to move, already in B, trashed in B (restore),
  not movable (trashed in A, not in A's catalog, bad path);
- `sameVolume`: whether A's and B's roots are on one volume (device id;
  on Windows the volume serial), so the UI can say "instant" or "copies
  X GB";
- for other volumes: free space on B's volume; the run is refused when
  `bytes to copy + 1 GiB` exceeds it;
- interrupted moves involving A or B, which must be finished first.

The plan is advisory; every check is repeated per file during the run.

## Concurrency and locking

- **Server:** the transfer is a job (one job at a time across the
  workspace, as all jobs). It opens B if closed (409 when busy), holds
  both entries' `trashMu` for the run, taken in library-id order, and
  opens B's thumbs store writable for the run. Both grids are told to
  refresh at the end (the job's `libraries: [A, B]`).
- **CLI:** takes both library locks (in resolved-root order, so two
  processes never deadlock), refuses when either is held, like `import`.
- Cancel: stops between files; the current file finishes its protocol
  step (a moved file is always recorded), the batch commits, the journal
  gets `end`, the outcome is `cancelled`.

## CLI

```
picmover move <from-library> <to-library> [selection] [--dry-run] [--yes]
picmover move --resume <library>
```

Selection (AND, at least one, or `--all`): `--album NAME`, `--tag NAME`
(repeatable), `--rating N`, `--from YYYY-MM-DD`, `--to YYYY-MM-DD`,
`--recur MM-DD..MM-DD`, `--months 6,7,8`, `--type image|image_raw|video`,
`--import ID`, `--hashes FILE` (16-hex hashes, one per line).

Without `--yes` it prints the plan and asks (on a terminal), or prints
the plan and exits 1 (not a terminal). `--dry-run` prints the plan only.
Prints a status line like `import`; the summary lists moved, already
there, restored, failed (each failure with its reason), and the B import
record id. Logs to `<B>/.picmover/logs/transfer_<time>.log`. Exit codes
as `import`: 0, 2 when some files failed, 130 cancelled, 1 otherwise.

## HTTP API (rules from `server.md` / `management.md`)

| Method | Path | Body → Result |
|---|---|---|
| POST | `/api/l/{lib}/transfer/plan` | `{to: libId, hashes: [...]}` → `{move: {count, bytes}, existing: {count}, restore: {count}, refused: [{hash, error}], sameVolume, free: bytes\|null, interrupted: [{from, to, id}]}` |
| POST | `/api/l/{lib}/jobs` | `{kind: "transfer", to: libId, hashes: [...]}` (≤ 2000 hashes per call; the UI chunks larger selections into one job per 2000) |
| POST | `/api/libraries/{lib}/transfer/resume` | starts a `transfer` job that only recovers |

Job progress: `{phase: "plan"|"move"|"recover", files, totalFiles,
bytes, totalBytes, current}`. Result: `{moved, existing, restored,
failed: [{hash, error}], record: importId, targetLibrary}`. A plan or a
job with `to` equal to `{lib}`, or naming a library outside the
workspace, is 400; single-library mode has no target and is 400.

## Frontend

- **Selection bar:** "Move to…" (workspace mode, at least two
  libraries). **Lightbox:** `m` and a button in the info panel.
- **Move dialog:** pick the target (library cards like the switcher,
  closed ones included; busy ones disabled with the holder). Then the
  plan: "Move 312 photos (4.1 GB) to Penny. 12 are already there; their
  copies here go to the trash." / "Copies across drives, then puts the
  originals in this library's trash." / "Same drive: instant." /
  "Not enough space in Penny: 4.1 GB needed, 2.0 GB free." Confirm.
- Progress via the job pill and the dialog (files, bytes, rate, ETA).
  The result lists failures with reasons; "Open in Penny" goes to the
  target's grid filtered to the new import record.
- After the job the source grid drops moved tiles, the selection clears,
  and nav counts (Trash) refresh.
- An interrupted move shows a banner on both libraries with "Finish
  moving".

## Security and safety rules

- Rows, not request paths: hashes name rows; paths come from catalogs
  and pass `validRelPath` plus the trash/file-server link checks in both
  libraries. Only regular files move; links are never followed out of a
  library.
- No replacement anywhere (`RenameNoReplace`, `CopyReadBack`).
- Nothing deletes a file; the only deletion remains "Empty trash".
- A same-volume rename is preceded by a hash of the source; a copy is
  checked twice (read and read-back).
- Libraries must both be in the served workspace; target id validated
  like every library id.
- Backups: no change. A's backup keeps the moved file as an *extra*
  until a prune; B's next backup copies it. The plan's text mentions
  this for libraries with backup targets.

## Tests

- `transfer`: each case 1–4 end to end with two temp libraries; EXDEV
  injected (the `rename` test hook) to exercise copy; hash mismatch on
  the source; target name taken on disk and in the catalog
  (`UniqueName`); catalog insert failure (rename undone); source-step
  failure (reported, journal open, recovery retries); free-space refusal.
- Recovery: a crash injected after each protocol step of cases 3 and 4
  (journal + state), then `Recover` reaches the table's outcome; torn
  last journal line; the lost-track row.
- User data merge: rating precedence, tags by name with case
  differences, albums by name (missing, one, several), ignored pairs.
- Renditions copied; missing renditions leave `thumb_state` pending.
- `cmd`: selection flags, `--dry-run`, refusal without `--yes`, lock
  order, `--resume`.
- `server`: plan shapes, 400s, job progress/result, busy target, both
  trash mutexes held, refresh of both libraries.
- `web`: dialog states (same volume, cross volume, no space, interrupted),
  chunking, grid refresh.

## Settled in review

- **Duplicates (case 1):** when identical content (same hash) is
  already live in B, A's copy goes to A's trash, on one volume too.
  Nothing outside "Empty trash" deletes a file.
- **`imported_at` in B** is the time of the move (the B import record
  says where it came from).
- **Rating conflict:** B's own rating wins. B has one only when the
  content is live there (a duplicate) or was there before: `user.db`
  keeps ratings of trashed and permanently deleted content, so content
  coming back gets its old rating, as `management.md` intends.
- **CLI with no selection flag** is refused unless `--all` is given.
