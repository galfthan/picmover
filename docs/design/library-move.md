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
  on Windows the volume serial), so the UI can say "moved directly" or
  "copies X GB";
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
failed: [{hash, error}], record: importId, targetLibrary}`.

The job object of a transfer: `library` is the source id, `kind:
"transfer"`, `to` the target id, `progress` the progress object above
(null before the first event, sent in `progress` events like the backup
kinds), `result` the result above once finished (null before), `importId`
the B record id; `state`, `exitCode`, `error` as for every job.
`GET /api/l/{lib}/jobs` lists a transfer under both its libraries.
`failed` in the result holds at most 200 entries; `failedCount` the
total. A plan or a
job with `to` equal to `{lib}`, or naming a library outside the
workspace, is 400; single-library mode has no target and is 400.

## Frontend

- **Selection bar:** "Move to…" (workspace mode, at least two
  libraries). **Lightbox:** `m` and a button in the info panel.
- **Move dialog:** pick the target (library cards like the switcher,
  closed ones included; busy ones disabled with the holder). Then the
  plan: "Move 312 photos (4.1 GB) to Penny. 12 are already there; their
  copies here go to the trash." / "Different drive: copies 4.1 GB, then
  puts the originals in this library's trash." / "Same drive: the photos
  are moved directly, so this is quick." /
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

## As implemented (backend)

Branch `feat/library-move`. Everything above holds except where this
section says otherwise.

### Code

```
internal/transfer/transfer.go   Side, Options, Item, Result, Run, the per-file protocol, Message (user-facing error texts), SameLibrary
internal/transfer/journal.go    journal format, JournalPath, ListInterrupted (locked library, prunes), ListInterruptedAt (read-only)
internal/transfer/recover.go    Recover, RecoverPair, IsSource
internal/transfer/plan.go       MakePlan
internal/transfer/volume_*.go, free_*.go   sameVolume, diskFree (per platform; copies of importer/backup's unexported helpers)
internal/library/move.go        LiveFile, LiveTarget, MoveFileTo (ErrTargetExists), RestoreTo
internal/catalog/manage.go      SetDeletedAs, MatchingHashes
internal/thumbs/store.go        Store.CopyFrom
internal/userdata/merge.go      MergeFrom, MergeIgnored; imports.go: ImportModeTransfer
internal/importrec              CloseCrashed skips mode transfer unless the record has no journal
internal/server/api_transfer.go plan, resume, the transfer job (runTransfer), interruptedMoves
cmd/move.go                     picmover move
```

### Deviations and additions

- **Journal lines are JSON**, not space-separated (catalog paths may
  contain spaces): `{"op":"begin","hash","kind","rel","to","mode"}`,
  `{"op":"trash","hash","to"}`, `{"op":"done"|"fail","hash"}`,
  `{"op":"end"}`, after a JSON header `{v:1, id, source:{id, root, name},
  target:{id, root, name}, startedAt}`. A reopened journal starts with a
  newline so a torn last line never glues onto the next. `trash` is
  written before the source copy goes to the source trash under a
  `UniqueName` variant (its own trash path taken), so recovery knows
  where to look.
- **`done` follows the user data.** A file's `done` line is written at
  the flush after its batch: after the source step, the source's
  `user.db` trash rows, the target's `user.db` untrash (restores) and
  `MergeFrom`, in that order. A crash anywhere before leaves the entry
  open and recovery does those steps again (each is idempotent; a
  recovered entry whose source row is already trashed gets its `user.db`
  trash row). When a `user.db` step fails the entries stay open too.
- **`end` only with no open entry.** The journal tracks the entries
  begun and not done or failed; `end` is refused while there is one, in
  `Run` and in `Recover`. An entry left open on purpose (a source step
  that failed, a placement whose row was not inserted and could not be
  undone, a recovery step that failed or a file that could not be read)
  therefore keeps the move listed as interrupted, and the next recovery
  retries it. `Recover` closes a still running record only when it ends
  the journal (the record first, then `end`), so a recovery done in
  several passes records every file.
- **Batches** are taken out of the pending list before they are
  flushed, so a journal error in a flush (which stops the run) never
  flushes a batch twice; every file of the batch is still finished and
  reported, and the error is returned at the end. The per-row fallback
  counts a row as recorded only when `InsertMedia` inserted it (or the
  live row of the hash is already at that path); otherwise the placement
  is undone like a failed insert. Recovery's insert does the same and
  leaves the entry open.
- **Duplicates (case 1)**: B's file is checked to exist as a regular
  file and **not hashed** (as the contract says): the content is
  identified by B's catalog hash. When A's file is already missing, A's
  row is forgotten (as recovery does) instead of leaving the source step
  pending.
- **Restore (case 2)** hashes B's trashed copy first, as the importer
  does before a restore: a missing copy fails the file with
  `ErrTargetCopyMissing`, a different one with `ErrTargetTrashMismatch`,
  and A is left alone. A restore whose catalog update failed and whose
  undo (back into the trash) failed too is left open for recovery. B's
  `user.db` trash row is removed at the flush.
- **Same library** is refused by root, or by `os.SameFile` of the two
  `media.db` files, not by `library_id`: a copied library folder shares
  its id and is a different library. Journals are matched to their
  source by id **and** root (`IsSource`); the root may differ only when
  the journal's root no longer holds a library (the folder was moved).
- **Ignored pairs** are copied when both hashes moved in the same run
  (`MergeIgnored`), not "both in B after the move".
- **Undo of a copy** whose target row cannot be inserted removes the
  fresh copy in B (verified equal to the original, which is still in
  A). It is the only file removal of the feature, and only of a file
  the run itself created and never recorded.
- **Source trash name taken**: when A's trash already holds another
  file at the photo's path, the original goes to the trash under the
  first free `UniqueName` variant and the row records it
  (`SetDeletedAs`); an undo uses `RestoreTo`.
- **Renditions**: copied when A has the thumbnail and the preview (the
  large thumbnail too when present); otherwise B's row is `pending` and
  `picmover thumbs` (or the Library page) generates them. A's
  renditions are deleted after a same-volume move, kept after a copy
  (the trash page shows them).
- **Batches** fall back to one insert per row when the batch
  transaction fails, so one bad row fails only its file. Before the
  rows, each folder of a copy is synced together with its parents up to
  the library root (folders the copy may have created).
- **Recovery of a restore** whose target row is still trashed counts as
  not committed (`ErrInterrupted`), unless the trash no longer holds
  B's file and B's path or one of its `UniqueName` variants holds a file
  of the hash: a crash inside the restore, which recovery finishes
  (`ClearDeleted` at that path). Recovery adds `ErrUnrecorded` (file in
  B, source row gone: run rebuild in B) next to `ErrLostTrack`.
- **Recovery and the source trash**: when A's row is live but its file
  is not in place (and the entry is not a plain rename), recovery hashes
  A's trash file at the journalled `trash` name, else at the row's own
  path. A match is a crash between the rename to the trash and the
  catalog update: going forward it marks the row trashed there
  (`SetDeleted`/`SetDeletedAs`, `user.db` trash row), going back (B's
  row gone) it puts the file back in place (`RestoreTo`). Only when it
  is not there is A's row forgotten (forward) or the entry lost track
  (back).
- **Recovery errors**: a placed file or a source file that cannot be
  read (anything but "does not exist", not a regular file, outside the
  library) leaves the entry open instead of failing it or calling it
  lost. The placed file's folders are synced (up to the root) before its
  row is inserted and the source step runs.
- **Recovery is addressed by root.** `Recover` refuses a journal whose
  target root is another folder that still holds a library
  (`ErrOtherTarget`), and the listings skip such journals: a copied
  library folder carries the original's journals. The target id is not
  compared (a `user.db` made anew has a new id).
- **Cancelling a recovery** stops between entries (never inside one);
  the rest stay open and `Result.Cancelled` is set. `RecoverPair` stops
  between journals; `Run` returns cancelled after its recovery when ctx
  is done.
- **Run** finishes interrupted moves between the two libraries, in both
  directions, before it starts (`Result.Recovered`).
- **A record without a journal**: a crash after `BeginImport` and
  before the journal is created leaves a running transfer record that
  no recovery can find. `importrec.CloseCrashed` closes a transfer
  record whose journal (`transfer.JournalPath` next to `user.db`) does
  not exist; nothing was moved under it.
- **The plan** lists interrupted moves with the read-only listing
  (`ListInterruptedAt`): it never prunes a journal.
- **HTTP**: answers about the target carry no `state` (on a
  library-scoped path a state describes the source): busy is 409
  `{error, holder}`, others `{error}`. The plan adds `need` and
  `noSpace`; transfer progress adds `rate` (bytes/s) and `eta` (s); the
  result adds `sourcePending`, `recovered` and `bytes`; `result` is
  absent until the job finishes. `POST /api/libraries/{lib}/transfer/resume`
  finishes every interrupted move from **or** into `{lib}` (400 when
  there is none). Lib objects carry `interruptedMoves: [{from, to, id}]`
  (omitted when empty) on both libraries. The job log is in the
  target's `.picmover/logs/transfer_<time>.log`.
- **CLI**: `--album` and `--tag` take names (an album name shared by
  several albums selects all of them); `--hashes FILE` allows `#`
  comments; `--resume <library>` finishes the interrupted moves **into**
  that library (journals live in the target). Without a terminal and
  without `--yes` it exits 1 after printing the plan.

### After review (server, CLI)

- **Interrupted moves block** as "Pre-flight" says, outside the dialog
  too: starting a transfer job is 409 (`{error}`, no `state`) "the move
  from peter to paul was interrupted; finish it first (Finish moving, on
  the Libraries page)" while a move into or out of either library is
  unfinished; `picmover move` exits 1 with `picmover move --resume
  <target>`. The interrupted moves between the two libraries themselves
  do not block (Run finishes them first). The CLI looks for them in both
  libraries and, for moves out of them, in the library folders next to
  them.
- **Moves that cannot be finished** do not block anything and are never
  counted as failed files. Each listed move (`interruptedMoves`, the
  plan's `interrupted`) carries `problem` when it cannot be finished now:
  "its journal (.picmover/transfers/7.journal) cannot be read, so
  picmover cannot finish it; look at it by hand" (then `from` is `""` and
  `id` is the journal's file name), or "the library it came from (peter)
  is not in this workspace; put it back to finish the move". `from` is
  the source's current id when it is found in the workspace (by library
  id and `IsSource`, so a renamed folder is found). Nothing is offered to
  dismiss such a move: the journal stays until its source is back (then
  Finish moving works) or a person deals with it; no file is touched.
  The resume endpoint answers 409 with the reasons when no move it would
  finish can be finished.
- **Resume** goes on past a move it cannot finish (a problem above, a
  library that cannot be opened, a recovery error) and reports each as
  "the interrupted move from X to Y could not be finished: <reason>" in
  the job's `error`: exit 2 when another was finished, exit 1 when none
  was. Only a cancel stops it. The CLI's `--resume` does the same, finds
  a source whose folder was renamed among the folders next to the target
  (by library id), prints "Interrupted moves not finished: N of M", and
  exits 2 (1 when none could be finished, 130 when cancelled).
- **Edits during a move**: while a transfer job holds a library (both
  libraries of the pair; `libEntry.moving`, set by `lockPair`), its trash
  endpoints (`trash`, `trash/restore`, `trash/delete`) and its user data
  changes (`ratings`, `tags` create/rename/delete, `media/tags`, `albums`
  create/update/delete/items, `similar/ignore`) answer 409 `{error: "a
  move is running on this library; try again when it has finished"}` at
  once. Before, the trash requests waited for the whole move (the trash
  lock), and a rating or tag set after a photo's batch had merged was
  lost in the target.
- **Cancel**: a job is `cancelled`/130 only when the cancel left work
  undone: Run (or Recover) reports `Cancelled`, or failed with
  `context.Canceled` (or after the cancel). A cancel arriving after the
  last file is `done`. The CLI exits 130 the same way, also when the
  cancel stops it before the move begins ("Cancelled; nothing was
  moved.").
- **Originals still in the source** (`sourcePending`) are counted from
  the items (they include those of the moves Run finishes first) and the
  job's `error` says "N photos were moved to <B>, but their originals
  could not be moved to the trash in <A>; finish the move from the
  Libraries page" (the journal stays open, so the move is listed as
  interrupted). Failed files: "N files could not be moved (see the job
  log)". The parts are joined with "; ".
- **CLI summary**: `failed: N` counts the failures listed below it,
  including those of an interrupted move between the two libraries that
  the run finished first; the exit code follows the same counts.
  `--import` must be a positive record id (a negative one is a usage
  error; before, it selected nothing and still counted as a selection).
- **Cost**: the move journals of the workspace (and, when a move is
  unfinished, the library ids) are read once per request: once for
  `GET /api/libraries`, not once per library. The job log is opened
  without a thumbs store (one writable store per side per job).
- Recovery closes the target's import record as `failed` even when it
  finished every file (left as it is; the record is not shown as the
  outcome of the resume anywhere).

### Tests

`internal/transfer`: same-volume and cross-volume moves (metadata,
renditions, user data, record, journal), existing and restored content,
target name taken (case-insensitively), a changed source on both paths,
refusals, no space, a taken source trash name, cancellation mid-run,
merge by tag and album name; recovery of a rename and of a copy not yet
recorded, of a recorded file whose source step is missing, of nothing
placed, of a wrong file at the target path, the lost-track case, a torn
line with done entries, Run recovering first, `IsSource`. Review fixes,
with crashes injected through the `crashAt` hook and journal write
failures through `journalFail`: a journal error in a flush never
flushes twice (rename and copy), a row not inserted by the fallback is
undone, an undo that fails keeps the journal open, B's damaged trashed
copy is not restored, a duplicate whose source is gone, the plan does
not prune; a crash before the merge (one volume, two volumes, and right
after the file is reported), between the move to the source trash and
the catalog update (copy, duplicate, taken trash name, and a duplicate
whose target copy is gone), after a restore before its untrash, inside
the target's restore (own path and variant); a failed recovery step and
an unreadable placed file staying open, a cancelled recovery, a journal
of another target, `syncParents`. `internal/importrec`: a transfer
record is closed only without a journal.
`internal/library`: `MoveFileTo` never replaces, refuses internal targets
and links; `RestoreTo`. `internal/server`: plan shapes and 400s, the
job end to end (result, record, listed under both libraries), busy
target without `state`, `interruptedMoves` and resume. `cmd`: usage
errors, unknown album, dry run, refusal without `--yes`, move by album
and by date, the album carried over, `imports` listing, `--resume` with
nothing to do.

After review: `internal/server/transfer_fixes_test.go` (a move refused
while another involving either library is interrupted, the pair's own
finished; unreadable journals and missing sources listed with their
problem, resume 409, not blocking; a resume finishing one move and
reporting another; trash and user data requests refused at once while
moving; the job outcome for early, cut-short and late cancels, source
pending, no space; an early cancel never `failed`).
`cmd/move_fixes_test.go` (negative `--import`; refusal while another
move is interrupted, the pair's own finished; `--resume` going on past
an unreadable journal and finding a renamed source; the failed count
matching the listed failures; exit 130 for a cancel before the move).

Checked by hand against the built binary: `picmover serve` on a
two-library workspace, driven in headless Chromium: select two photos,
Move to… → penny → plan "Move 2 photos (10.6 KiB) to penny. Same drive:
instant." → Move → "Moved 2 photos to penny."; peter's grid kept one
tile, penny's showed two with their thumbnails, the files were renamed
on disk and penny's Imports listed a `transfer` record from peter.

## As implemented (frontend)

Built against this contract while the Go side was written in parallel, so
it is tested against the fake server (`web/src/testing/fakes.ts`), not the
real one. Everything in "Frontend" is implemented.

### Files

`lib/api.ts` (`TRANSFER_CHUNK`, `TransferPlan`, `TransferProgress`,
`TransferResult`, `InterruptedMove`, `TransferJobRequest`; strict parsers
`toTransferPlan`, `toTransferProgress`, `toTransferResult`,
`toInterrupted`, `sumPlans`; `LibApi.transferPlan`, `resumeTransfer`;
`Job.to`, `Lib.interruptedMoves`, job kind and import mode `transfer`),
`lib/movetext.ts` (pure wording: plan lines, the free-space refusal,
reasons grouped, results, interrupted-move lines), `lib/moves.svelte.ts`
(the dialog request and the run: consecutive jobs, totals, changes,
refreshes, the toast when the dialog is closed), `components/MoveDialog.svelte`
(rendered by `App.svelte` from `moves.dialog`),
`components/InterruptedMoves.svelte` (banner on the photo pages, one line
on the Libraries page cards). Touched: `lib/jobs.svelte.ts` (`waitFor(id)`
resolves when a job has ended, a lost job as failed; `adopt(job, lib)`
follows a job started through another route), `lib/changes.ts` (change
`moved {lib, hashes}`), `pages/Grid.svelte` (drops moved tiles; reloads in
the trash page), `components/SelectionBar.svelte` and
`components/Lightbox.svelte` (entry points), `components/JobPill.svelte`
("Moving to kids: 12 of 300 files…", "Finishing the move to kids…"; opens
the dialog), `components/BackupProgress.svelte` (reused for the run's
progress: files, bytes, rate, ETA, current file, Cancel),
`lib/backupinfo.ts` (phases `move`, `recover`), `lib/jobtext.ts` (title
"Move to library", outcome with the result counts), `lib/imports.ts` (mode
`transfer` reads "Moved from a library").

### Shapes relied on

- **Plan** exactly as the API table. Missing or malformed fields read as
  0, `false`, `null` or empty: `sameVolume` only when it is `true`, `free`
  only as a number ≥ 0 (and only used when `sameVolume` is false),
  `refused` entries need a 16-hex `hash` (`error` a string), interrupted
  entries need non-empty `from`/`to` and an `id` that is a **positive
  integer number** (a string id is dropped). The client refuses a copy
  when `move.bytes + 1 GiB > free` (the server's rule) and says so before
  the request; the server checks again.
- **Chunks**: plans of more than 2000 hashes are sent as consecutive
  requests of 2000 and summed (`sameVolume` and `free` from the first,
  `refused` concatenated, `interrupted` deduplicated). The job request is
  `{kind: "transfer", to, hashes}` with at most 2000 hashes, without the
  plan's `refused` hashes; a larger selection runs as one job per 2000,
  each started after the previous has ended.
- **Job object**: `to`, `progress` (also in SSE `progress` events at the
  top level, under `progress` or under `stats`, as the backup kinds),
  `result` once finished, `importId`, `state`, `exitCode`, `error`. A job
  **continues the run** when `state` is `done` or `exitCode` is 2 (some
  files failed); `cancelled` or exit 130 stops it as cancelled, anything
  else as failed with `error`. The result's `failedCount` is read as at
  least `failed.length`; `record` must be a positive integer.
- **Moved tiles**: the hashes of a job minus `failed` leave the source's
  pages at once, but only when the job finished as above and
  `failedCount == failed.length` (else the in-place reload after every
  job, `jobs.doneCount`, has the grid right).
- **Busy target at job start**: the 409 must **not** carry a `state`
  field. A library-scoped 409 with `state` means "this (source) library
  is not open" to the client and makes the shell reopen the source.
- **Resume**: `POST /api/libraries/{to}/transfer/resume` (the target of
  the interrupted move, where the journal is) answers the job object
  (bare or `{job}`); a missing `library` is filled with `to`. After it the
  dialog plans again.
- **Listing**: a library may carry `interruptedMoves: [{from, to, id}]`
  (optional; absent means none). It is shown on both libraries, so the
  server should list the move under its source and its target.
- **Open in <target>**: `#/l/<to>/imports/<record>` (the import record's
  grid, as the Imports page links it) when the run had one record; the
  target's Imports page when it had several (one record per job).

### Deviations and additions

- The headline counts every photo that leaves this library (to move +
  already there + in the target's trash) and the bytes to move ("Move 312
  photos (4.1 GiB) to Penny."); sizes are in binary units as everywhere in
  the UI. Items in the target's trash get their own line ("3 are in
  Penny's trash; they come back there, and their copies here go to the
  trash."). Within the 1 GiB margin the refusal says "4.0 GiB needed plus
  1.0 GiB to spare, 4.5 GiB free". Refused items and failures are listed
  as reasons with counts ("the file is missing (3)"), not per hash.
- With backup targets the plan adds "This library's backups keep the
  moved files until they are pruned."
- The dialog can be closed while a move runs (it says so); the pill opens
  it again. A run that ends with the dialog closed shows a toast with
  "Open in <target>".
- The selection loses the hashes of the jobs that started (all of them
  after a whole run); a move that cannot start keeps the selection.
- Picking a closed target opens it through the libraries store first (the
  same open as the shell's); a target that turns out busy or broken is
  reported in the dialog and the list stays.
- Keys reaching the lightbox while the move dialog is open are ignored
  (arrows, Delete, `m`). After the shown item moved, the viewer goes on to
  the next item, else the previous, else closes.
- "Move to…" needs workspace mode and at least two entries in the
  listing's `libraries` (folders do not count); the dialog offers open,
  closed and busy libraries other than the source.

### After review (frontend)

- **Wording**: a same-drive plan says "Same drive: the photos are moved
  directly, so this is quick." (not "instant": each file is hashed and
  journalled, about 10 ms a file); another drive's says "Different drive:
  copies 4.1 GB, then puts the originals in this library's trash." The
  CLI plan uses the same words, naming the source library's trash.
- **Originals still here**: `TransferResult.sourcePending` is parsed and
  summed into the run's totals. A job that ends with exit 2 keeps the
  server's `error` in the run's `notes`; the dialog shows each as an
  error box, the toast appends them, and the job outcome reads "Move to
  library finished with problems (2 moved, 2 still here too): <error>"
  instead of "some files failed". A job with `sourcePending > 0` does not
  drop its tiles at once (those photos are still live here); the reload
  after the job has the grid right.
- **Interrupted moves** carry `problem` (`InterruptedMove.problem`, ''
  when none; an entry with a problem may have an empty `from` and id 0).
  Only those without a problem block the dialog and get "Finish moving";
  the others are shown with "It cannot be finished now: <problem>." on
  the plan, the banner and the Libraries page card. After Finish moving,
  the dialog plans again only when the resume finished cleanly;
  otherwise it stays on the result: "The interrupted move to kids is not
  finished." with the server's reasons (the banner shows them too).
- **Edits during a move**: `movingOn(lib)` (a transfer job running with
  the library as `library` or `to`, or this page's run) disables rating,
  tags, albums, Remove from album, Trash, Restore and Delete permanently
  in the selection bar and the viewer, with "A move is running on this
  library; ratings, tags, albums and the trash can be changed when it has
  finished." A 409 from the server (another tab, a resume of other
  libraries) shows its message where the action was.
- **Leaving mid-run**: the parts of a run of more than 2000 photos are
  started from the page, so while parts remain the page asks before it
  is left (`beforeunload`) and the dialog says "Keep this page open: the
  remaining parts are started from it." A reload anyway ends the run
  after the running job (that job's outcome is shown by the job pill; no
  "Moved N photos" for the whole selection is ever claimed).
- The fake server follows: `transfer.pending` (originals that stay, with
  the server's message), `problem` on interrupted moves (resume 409 when
  none can be finished), 409 for trash and user data changes while a
  transfer job runs on peter, and a resume ending with exit 2 leaves the
  move listed.

### Tests

`npm run check` (0 errors, 0 warnings) and `npm test`: 318 tests in 31
files (21 new), all passing with `TZ=UTC`; in a UTC+3 zone two older
tests (`Imports.test.ts` "lists the records newest first",
`Management.test.ts` "reviews near-duplicates") fail on relative-time
wording before and after this change.

- `lib/transfer.test.ts`: plan, progress, result and interrupted shapes
  (malformed entries dropped), `sumPlans`, a 4500-hash plan sent as
  2000 + 2000 + 500 with progress and summed, the resume URL; wording of
  plans (same drive, across drives, duplicates, restores, nothing to
  move), the free-space refusal (with and within the margin, unknown free
  space, same drive), reasons, results, interrupted lines, `continues`.
- `Move.test.ts` (whole app, fake server): Move to… in grid and album
  pages, not in the trash, not in single mode (bar and viewer); the target
  list (busy with holder and disabled, broken absent) and Escape; a closed
  target opened before the plan; same drive with the progress (files,
  current file, pill) and the result with "Open in kids" →
  `#/l/kids/imports/50`, tiles gone, selection cleared, libraries and
  library counts reloaded; across drives, then not enough space with Move
  disabled; refused and duplicate items with the job sending only the
  movable hashes; failures with reasons keeping their tiles; a job that
  cannot start keeping the selection; an interrupted move blocking until
  Finish moving (resume request, pill, re-plan); 4100 hashes as three
  consecutive jobs (2000, 2000, 100) and several records; a cancelled job
  stopping the run; `m` in the viewer moving the shown item and going on
  to the next, with the dialog's keys not reaching the viewer; the
  interrupted-move lines on both Libraries page cards with Finish moving
  and the toast.

After review (325 tests, `TZ=UTC`): `lib/transfer.test.ts` reads
problems and `sourcePending`, `blocking`, the problem wording and the
exit-2 job outcome with the server's message; `Move.test.ts`: originals
that could not go to the trash (message, tile kept), a move that
cannot be finished not blocking (dialog and Libraries card, no Finish
moving), a resume ending with a reason staying on the result instead of
planning again, edits disabled and refused while a move runs, and
`beforeunload` while parts of a run remain.
