# Import: one read per source file (copy-first)

Status: contract for a fix on branch `fix/import-copy-first` (from master
eb63230). Implementation notes go into "As implemented" at the end.

## Problem

Importing from a phone (gvfs MTP or PTP/gphoto2 FUSE mount on Linux) ran
at about 14 s per file: 50 of 166 files in 12 minutes. The importer opens
every new source file three times (`internal/importer/importer.go`,
`process`):

1. `library.HashFile(src)`: one full sequential read.
2. `Extractor.Extract(src)`: a second open; `imaging.Config` reads the
   header and `exif.Read` walks the IFDs with many small random reads
   through `io.ReaderAt`; videos run ffprobe, which seeks.
3. `CopyVerified(src)`: a third full sequential read into the library
   temp file, hashed again and compared with 1.

On a local disk reads 2 and 3 come from the page cache. On a FUSE mount
every open is a new USB transfer: the MTP backend turns every random read
into a round trip, the gphoto2 backend downloads the whole file on every
open. Four workers in parallel interleave transfers on one serial link.

## Rules

### Copy-first path

A source file takes the copy-first path when all of these hold:

- it is *foreign*: its cleaned absolute path is not inside the library
  root (use the same inside-the-library decision the importer makes today
  for `inLayoutDir` / `Rel`; lexical, no symlink resolution);
- it is a regular file reached directly, not a symbolic link, and not an
  entry extracted from a zip (`fromZip` false);
- the import copies it: `Options.Move` is false, or `ForceCopy` is set, or
  `Move` is set but source and library are on different devices (see
  "Device check").

Everything else keeps today's order (hash, extract, place): record in
place (`rebuild`), zip entries (already extracted into `.picmover/tmp`),
symlink sources, sources inside the library, same-device moves.

The copy-first path in `process`:

1. **Stage.** `library.Stage(ctx, src, tmpDir)` (new, next to
   `CopyVerified`) streams the source once, sequentially, with the pooled
   1 MiB buffer, into `<library>/.picmover/tmp/copy-*.part`, hashing the
   bytes as they are written (xxhash64, identical to `HashFile`). It
   returns the temp path, hash and size. The size written must equal the
   size from the source `Stat`; otherwise the error is
   `"<origin>: size changed while it was read"` and the temp is removed. The
   source mtime is set on the temp (`Chtimes`), so the final file keeps
   it as `CopyVerified` does today. No fsync yet. Bytes are added to
   `Stats.BytesCopied` as they are written, per buffer, so the status line
   moves during a large video, not once per file.
2. **Duplicate check** by hash exactly as today (catalog row, trashed row
   restore via `restoreTrashed`, in-flight duplicate). A duplicate removes
   the temp and reports the same result as today.
3. **Metadata** is extracted from the temp file, never from the source:
   `Extract(ctx, tmpPath, kind)` with the kind from the source name. Log
   lines, warnings and results keep naming the origin. `MinDimension`
   skips remove the temp.
4. **Destination.** Layout path and `reserveDest` as today. The in-place
   checks (`os.SameFile` at the destination, `inLayoutDir`) are skipped: a
   foreign file is never in place. `identical` from `reserveDest` removes
   the temp and reports existing, as today.
5. **Place.** `library.Commit(tmpPath, dst)` (factored out of
   `CopyVerified` so both share it): fsync, close, `MkdirAll` the parent,
   rename into place, directory sync as today. On failure nothing is left
   at `dst` and the temp is removed. The verification read-back is gone:
   the hash recorded is the hash of the bytes written, and the thumbnail
   stage decodes every imported file afterwards. Say so in the README.
6. **Cross-device move** sets `deleteSrc` as today (the writer deletes
   the source after the catalog batch commits).
7. **Cleanup.** Any error, cancellation or early return after staging
   removes the temp (a deferred cleanup cleared only when the file is
   placed). After a run, `.picmover/tmp` holds no `copy-*.part` of this
   run.

`CopyVerified` stays for the other paths and for other callers; it may
use `Commit` internally.

### Device check

`sameDevice(src, dir string) bool` in the importer's platform files:

- Unix: `syscall.Stat_t.Dev` of the source file and of the library temp
  dir.
- Windows: volume serial numbers from `GetFileInformationByHandle` on
  both (the directory opened with `FILE_FLAG_BACKUP_SEMANTICS`); if that
  fails, `filepath.VolumeName` equality, case-insensitive.
- Any error: report same device. The old flow then tries the rename and
  falls back to `CopyVerified` on `EXDEV`, as today.

### Source readers

`Options.SourceReaders int` (<= 0: auto) bounds how many workers read
from the source at once; it wraps only the staging read. Auto: on Linux,
when `statfs` of the source root reports `FUSE_SUPER_MAGIC` (0x65735546),
1; otherwise, and on every other OS, equal to `Workers`. The value is
logged on the "import started" line. `import` gets a `--source-readers`
flag with a one-line help text; the server passes nothing (auto).

### Status line

`cmd/import.go` adds the transfer rate to the live status line, computed
from `BytesCopied` deltas between ticks (`humanSize(...)/s`), shown only
while bytes are moving. If the server's import job progress maps `Stats`
field by field, add `bytesCopied` there; no web changes in this fix.

## Tests

In `internal/importer` (new `copyfirst_test.go` plus adjustments):

- An unexported test hook on `Importer` wrapping `os.Open` of source
  files counts opens per path. A foreign new file is opened exactly once;
  a foreign duplicate exactly once; metadata extraction never opens the
  source path. The same hook shows a same-device move does not stage.
- Duplicate, in-flight duplicate, below `MinDimension`, extraction error
  (an `Extractor` stub), cancellation during staging, and a failing
  `Commit` leave no `copy-*.part` in the temp dir and nothing at the
  destination.
- Cross-device move (a test hook forcing `sameDevice` false): the file is
  staged once, placed, and the source deleted after commit; the row's
  hash, size and mtime equal the source's.
- `ForceCopy` imports take the copy-first path (existing tests relying on
  `ForceCopy` must keep passing).
- `SourceReaders: 1` serialises staging (a gated `Extractor` or open hook
  observing at most one concurrent stage).
- Auto `SourceReaders` on a plain temp dir equals `Workers`.
- Every existing importer, zip, symlink, rebuild and server import test
  passes unchanged.

## Constraints

- Go 1.27; `-tags nodynamic` on every build, vet and test; `go vet` for
  `GOOS=linux`, `GOOS=windows` and `GOOS=darwin GOARCH=arm64`; the
  importer tests also with `-race`; gofmt clean.
- No catalog schema change, no `web/` change, no new dependencies.
- Docs: this file gets an "As implemented" section; the package comment
  of `internal/importer/importer.go` describes the new order; README's
  "Verified Copies" bullet and the import section say what is true now.
- Commits on `fix/import-copy-first` with explicit paths (`git add <files>`),
  never `git add -A`; no push.

## As implemented

Built as specified, in three code commits on `fix/import-copy-first`
(library, importer, CLI) plus this documentation commit.

- `internal/library/stage.go`: `Stage(ctx, src, tmpDir, onWrite)`, its
  core `StageFile(ctx, *os.File, tmpDir, onWrite)` (reads an already
  opened source, so the importer's test hook sees every source open),
  the `Staged{Path, Hash, Size}` result, `ErrSizeChanged` (the message
  is `"<path>: size changed while it was read"`), and `Commit(tmpPath, dst)`.
  `CopyVerified` now places its temp file through the same `commitFile`
  (chmod 0644, fsync, close, `MkdirAll`, `RenameNoReplace`, mtime, dir
  sync). `HashReader` was added next to `HashFile` for the importer's
  hash-first path.
- `internal/importer/importer.go`: `process` decides `copyFirst`
  (`!RecordInPlace && !trash && !fromZip && !isLink &&
  !lib.Contains(src) && (!Move || ForceCopy || !sameDevice(src,
  lib.TempDir()))`); the copy-first branch stages, checks duplicates
  (catalog, trashed restore, in-flight) on the staged hash, extracts
  metadata from the temp file, skips the in-place checks, reserves the
  destination and `Commit`s. One deferred cleanup removes the temp unless
  it was placed. `Options.SourceReaders` is resolved in `Run`
  (`sourceReaders`) and logged as `source_readers` on "import started";
  a channel semaphore wraps the open and staging read.
- `xdev_unix.go` / `xdev_windows.go`: `sameDevice` (`Stat_t.Dev`; volume
  serial via `CreateFile(FILE_READ_ATTRIBUTES,
  FILE_FLAG_BACKUP_SEMANTICS)` + `GetFileInformationByHandle`, falling
  back to case-insensitive `filepath.VolumeName`, and to "same" when a
  volume name is empty). `fuse_linux.go` / `fuse_other.go`: `isFUSE`
  (`statfs` type `0x65735546`; false elsewhere).
- `cmd/import.go`: `--source-readers`; the status line appends
  `(<humanSize>/s)` after `Copied` when the `BytesCopied` delta between
  two ticks is positive. The server job progress
  (`internal/server/jobs.go`) already maps `bytesCopied` field by field;
  nothing changed there.
- Test hooks on `Importer`: `openSource` (wraps `os.Open` of source
  files), `sameDev` and `place` (`library.Commit`).

Deviations and clarifications:

- `Stage` takes a fourth argument, `onWrite func(n int64)`, called per
  buffer written: that is how the importer adds to `Stats.BytesCopied`
  while a large file is read, and the library has no access to the
  importer's counters.
- `BytesCopied` counts bytes placed in the library. Bytes counted while
  staging a file that is then discarded (duplicate, trashed restore,
  identical file at the destination, below `MinDimension`, error,
  cancellation) are subtracted again. Without this the existing
  `TestIdenticalAtDestination` (copy, `BytesCopied == 0`) would fail, and
  the summary's "Copied" would include files that were not imported. The
  status line therefore shows no rate when the delta is zero or negative.
- `--source-readers` larger than `--workers` is clamped to the worker
  count.
- The cleanup of a foreign file in the trashed-restore case: the staged
  copy is discarded and the trashed copy restored as before
  (`restoreTrashed`); with `--move` the source is then deleted by
  `restoreTrashed` as today, without opening it again.

Tests (`go test -tags nodynamic`):

- `internal/library/stage_test.go`: `TestStageAndCommit` (hash and size
  equal `HashFile`, `onWrite` per buffer, mtime and 0644 kept, temp
  gone), `TestCommitFailureLeavesNothing` (existing destination kept,
  uncreatable directory; temp removed, nothing at `dst`),
  `TestStageCancelledAndChanged` (cancellation; a source growing during
  the read gives `ErrSizeChanged`; temp removed).
- `internal/importer/copyfirst_test.go`: `TestCopyFirstReadsSourceOnce`
  (new files, an in-flight duplicate and a catalogued duplicate are each
  opened once; metadata is only extracted from staged temp files; row
  hash, size and mtime equal the source's; `BytesCopied` excludes the
  duplicate), `TestSameDeviceMoveDoesNotStage`,
  `TestCrossDeviceMoveStagesOnce` (`sameDev` forced false: one open,
  source deleted after commit, row hash/size/mtime equal the source's),
  `TestForceCopyTakesCopyFirst`, `TestCopyFirstCleanup` (catalog
  duplicate, below `MinDimension`, extraction error stub, cancellation
  during staging, failing `Commit`: no `copy-*.part`, nothing at the
  destination, `BytesCopied` 0), `TestSourceReadersSerialisesStaging`
  (at most one concurrent staging open with `SourceReaders: 1`; more
  than one with 4, so the test can fail), `TestSourceReadersAuto`.
- `cmd/cmd_test.go`: `TestStatusTransferRate`.
- All existing tests pass unchanged; the importer and library packages
  also under `-race`. Vet passes for linux, windows and darwin/arm64.
  The Windows volume-serial code is vetted, not run.

## Review fixes

An adversarial review of the branch found the items below. Where this
section differs from the rules above (one read per file under `--move`,
the restore deleting its source "without opening it again"), this
section is what the code does.

1. **Move verifies before the source is deleted.** Copy is one read of
   the source; move is two, the second one proving the bytes before the
   original is deleted. In the copy-first path, when the item will delete
   its source (`Move`: a cross-device or `ForceCopy` move, not in place),
   the worker hashes the source again (`verifySource`, holding a
   `readers` slot for that read only) after metadata extraction and
   destination reservation and before `Commit`, and before an `identical`
   file at the destination is accepted. If the hash differs from the
   staged hash, or the source cannot be read, the staged temp is removed,
   nothing is placed, and the file is an error result with message
   `copy` whose error wraps `library.ErrHashMismatch` ("the source changed
   or could not be read consistently"); the source is left untouched. A
   restore from the trash of a staged move checks the source the same way
   before deleting it; on a mismatch the restore stands (it is the
   library's own copy) and the source is kept. The README no longer
   presents the thumbnail stage as a safeguard: it runs after the copy
   stage, when move sources are already gone.
2. **In-place guard for `identical`.** When `reserveDest` reports
   `identical`, the destination is stat'ed and, if `os.SameFile` with the
   source, the file is in place: status existing, no `deleteSrc`, never
   deleted (a library file reached through a path `Contains` does not
   know, such as a hard link or bind mount). On Windows `volumeSerial`
   now opens with `os.Open` (Go's long-path handling) and calls
   `GetFileInformationByHandle` on its handle; the volume-name fallback
   is unchanged.
3. **Fallback capture date from the source.** When extraction of a staged
   copy reports `Source == "mtime"`, `taken_at` is the source's mtime (the
   `os.Stat` in `process`, in the extractor's zone), not the `.part`
   file's. `StageFile` keeps `Chtimes` best effort; the date no longer
   depends on it.
4. **Source readers for every source read.** `sourceSlot` takes a
   `readers` slot for a path outside the library and returns its release.
   It is held around staging, the move verification, `hashSource`, the
   hash-first `CopyVerified`, opening a zip archive and extracting each of
   its entries, and released as soon as the read ends (never across
   metadata extraction or catalog calls). Cancellation while waiting
   returns `ctx.Err()`, which `process` turns into nil, nil (a zip stops
   silently).
5. **Error renamed.** `library.ErrSourceChanged` is now
   `library.ErrSizeChanged` ("size changed while it was read"), so it does
   not clash with the backup copy's `ErrSourceChanged` on `feat/backup`;
   they should be unified once both branches are in master.
6. **Phone mounts in the folder browser.** `roots()` on Unix also lists
   the gvfs directory (`$XDG_RUNTIME_DIR/gvfs`, else
   `/run/user/<uid>/gvfs`) when it exists.
7. **Tests.** `TestCopyFirstCleanup/cancelled during staging` hands the
   importer a pipe fed with a 3 MiB file and cancels after more than
   1 MiB is staged (a partly written `.part` on disk, bytes counted); it
   checks the `.part` is removed and `BytesCopied` is 0.
   `TestStageCancelledAndChanged` cancels from `onWrite` after the first
   buffer. `TestCopyFirstReadsSourceOnce` gates extraction until the
   duplicate is reported, so it is always the in-flight duplicate. New:
   `TestCopyFirstMoveVerifiesSource` (second open returns other content:
   error, `ErrHashMismatch`, nothing placed, no row, no `.part`, source
   kept), `TestCopyFirstIdenticalAtDestinationMove`,
   `TestCopyFirstIdenticalIsSourceItself` (hard link outside the library
   to the library file, move, `sameDev` false: existing, nothing
   deleted), `TestCopyFirstMoveRestoresTrashed` (verified and changed
   source), `TestCopyFirstMtimeFallbackUsesSource`, and
   `TestFSRootsGvfs`. `TestCrossDeviceMoveStagesOnce` and
   `TestForceCopyTakesCopyFirst` now expect two opens.

### Transfer rate

The rate is computed once, in the importer, and shown by the CLI and the
web UI. `importer.Stats` has `Rate int64` (bytes per second). On every
progress tick `startProgress` records the growth of `bytesCopied` since
the previous tick (a negative delta, when a discarded staged file is
subtracted, counts as 0) and stores the bytes over the time covered by
the ticks of about the last two seconds (`rateWindow`, always at least the
newest tick). The final `OnProgress` call reports 0; `Importer.Stats()`
returns the last computed rate (0 without `OnProgress`). The CLI status
line shows `(<humanSize>/s)` from `s.Rate` (its own delta computation,
`transferRate`, is gone); the server job stats carry it as `rate` next to
`bytesCopied`, and the import progress card shows `(12.3 MiB/s)` after
the copied size while it is positive. Tests: `TestRateWindow`,
`TestStatusTransferRate`, the final-progress check in
`TestImportGeneratesThumbs`, and the web smoke test.

Not changed in code:

- A staged size that differs from the `Stat` size still fails that file
  with a clear error (`ErrSizeChanged`) rather than falling back to the
  hash-first path. It fails safe: nothing is placed or deleted. A FUSE
  backend that reports an inexact size (for example a PTP size capped at
  4 GiB) cannot import such files this way.
- Duplicates are staged before the duplicate check, so an import needs
  temp space in the library for up to `Workers` files at a time,
  duplicates included (noted in the README).
