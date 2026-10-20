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

## Phone mounts: pulling through the gvfs daemon

Branch `fix/import-gio` (from `fix/import-copy-first` 27da386).

### What and why

With one read per file, a phone import is still about 1 MB/s through
the gvfs FUSE mount: the gvfs MTP backend turns every FUSE read chunk
into a separate MTP partial-object request, one USB round trip each.
The gvfs daemon can pull a whole object at USB speed (the backend's pull
operation, `LIBMTP_Get_File_To_File` for MTP); that is what Nautilus and
`gio copy` use. `gio cat` is not fast: it reads through the same
per-request path. So on Linux a source file on a phone mount is staged by
running

    gio copy -- <uri> <library>/.picmover/tmp/pull-XXXX/copy.part

and hashing the local temp file.

### Mapping (`internal/gvfs`)

- `Dir()`: `$XDG_RUNTIME_DIR/gvfs`, else `/run/user/<uid>/gvfs` (the
  server's folder browser lists it as a root, as before, now through
  `gvfs.Dir`), with symlinks resolved (`filepath.EvalSymlinks`) when the
  directory exists, since the importer resolves its source the same way;
  otherwise the unresolved path.
- `Mount(p)`: for an absolute clean path below `Dir()`, the mount
  directory name is `<scheme>:host=<host>` possibly followed by
  `,key=value` parts; it returns the scheme, the host as written there,
  and the slash path below the mount directory. Linux only; elsewhere
  false.
- `IsDevice(p)`: scheme `mtp` (Android) or `gphoto2` (PTP cameras,
  iPhones). Other gvfs mounts (sftp, smb) keep the FUSE read.
- `URI(p)`: `<scheme>://<host>/<rel>`, each segment of `rel`
  percent-encoded with `url.PathEscape` (`/` kept). The host is copied
  as it is in the directory name: gvfs already escapes reserved
  characters there as in its URIs (older gvfs: `mtp:host=%5Busb%3A002%2C005%5D`).
  Example: `.../gvfs/mtp:host=Google_Pixel_7_1A2B3C4D/Internal shared
  storage/DCIM/Camera/PXL_2025 (1).jpg` is
  `mtp://Google_Pixel_7_1A2B3C4D/Internal%20shared%20storage/DCIM/Camera/PXL_2025%20%281%29.jpg`.
- `Pull(ctx, src, dst, onProgress)`: `gio` is found with
  `exec.LookPath` once (`ErrUnavailable` when missing) and run with
  `exec.CommandContext` as `gio copy -- <uri> <dst>`, with `LC_ALL=C`
  added to its environment; stdout and stderr are kept in one 4 KiB tail
  buffer. If that fails for any reason other than cancellation,
  `gio copy -- <fuse path> <dst>` is tried once; the form that worked is
  tried first by later pulls (`plainFirst`). When both fail the error
  carries both messages. While gio
  runs, `dst`'s size is polled every 200 ms and growth reported through
  `onProgress` (on the caller's goroutine). A non-zero exit returns an
  error ending in gio's last output line; cancellation kills gio and
  returns `ctx.Err()`; both remove `dst`.

### Library (`internal/library/pull.go`)

`StagePulled(ctx, src, tmpDir, pull, onWrite)` returns the same
`Staged` as `Stage`: it stats `src` (never reads it), runs `pull` into
a directory of its own (`pullTemp`, below), passes the pull's progress
to `onWrite` as increments (and the rest of the final size afterwards),
hashes the temp file locally, requires its size to equal the source's
`Stat` size (`ErrSizeChanged`), and sets the source mtime.
`HashPulled(ctx, src, tmpDir, pull)` stats `src`, pulls into a temp
file, hashes it, removes it, and applies the same size check
(`ErrSizeChanged`). Every error, cancellation included, removes the
temp.

`pullTemp`: every pull gets its own directory `pull-*` in the library
temp directory (`os.MkdirTemp`), and gio creates `<dir>/copy.part`
there. On success the file is renamed onto a `copy-*.part` name in the
temp directory reserved with `CreateTemp` (the rename replaces that
empty reservation) and the directory is removed; on failure or
cancellation the whole directory is removed. gio must create the
destination itself: `gio copy` would overwrite an existing file, but GIO
replaces an existing local file by writing a hidden
`.goutputstream-XXXXXX` file in the same directory and renaming it at
the end (the generic copy fallback; the gvfs pull path writes directly),
so the size polled would stay 0 until the end and a killed gio would
leave the hidden file behind. The pull's name is never one that another
`CreateTemp` in the shared temp directory (other workers' `StageFile`,
`CopyVerified`) can draw, which matters because libmtp opens the
destination without `O_EXCL`. A file the daemon still creates after gio
was killed finds its directory gone (or is removed with it);
`CleanTemp` removes everything in the temp directory, `pull-*`
directories included, at the start of every run.

### Importer

- `Run` sets `usePull` when gio is available (not in record-in-place
  mode) and logs `gio=true` on "import started" when it is set and the
  source root is on a device mount or is the gvfs directory itself.
- `stage`: for a device path while `usePull` holds, `StagePulled` with
  `gvfs.Pull`, holding the `readers` slot for the whole pull; progress
  goes to `bytesCopied` and `*counted` as with `StageFile`. Debug log
  "pulled through gvfs" per file.
- `hashSource` (the move verification `verifySource` and the hash-first
  path): for a device path, `HashPulled` into a second temp file. A move
  therefore still reads the source twice, as two independent pulls.
- Fallback: the pull error `gvfs.ErrUnavailable` turns pulling off for
  the run silently. Any other pull error reads that file through FUSE
  (debug log "gio copy failed, reading this file through FUSE") and
  counts as a failure; after three failed pulls in a row
  (`maxPullFailures`; a pull that works resets the count) pulling is
  turned off for the rest of the run with one warning `gio copy failed
  repeatedly, reading the rest through FUSE` (`in_a_row`, `file`,
  `err`). The bytes a failed pull counted are taken back first. A pull
  that worked but whose copy has the wrong size, or cannot be hashed,
  reads only that file through FUSE (debug log "pulled copy not used");
  the FUSE read then applies its own size check. This holds for staging
  and for the move verification (`hashSource`) alike. Cancellation
  returns the error without a fallback.
- Zip archives on a phone keep the FUSE read. `Stats` are unchanged.

### Tests

- `internal/gvfs/gvfs_test.go`: `TestParseMountAndURI` (Pixel, iPhone,
  Samsung names with spaces, `%`, `#`, `?`, parentheses; old escaped USB
  host; extra key=value parts; the mount directory; non-device and
  malformed paths), `TestDir`.
- `internal/gvfs/gvfs_linux_test.go`, with a shell script standing in for
  gio: `TestPull` (the exact argv, `dst` created by gio in a directory
  of its own, monotonic progress, `LC_ALL=C`), `TestPullFailure` (both
  forms tried, both stderr lines, `dst` removed),
  `TestPullRetriesOtherForm` (URI form fails with a Finnish message, or
  the plain path fails; the working form is tried first afterwards),
  `TestDirSymlinked`, `TestPullCancelled` (killed, `dst` removed),
  `TestPullNotOnMount`.
- `internal/library/pull_test.go`: `TestStagePulled` (hash, size,
  progress increments, mtime, `Commit`, `HashPulled`; the pull writes
  `pull-*/copy.part`), `TestStagePulledFailures` (size mismatch, short
  `HashPulled`, pull error, a late write into a removed pull directory,
  cancellation during and right after the pull, missing source; no temp
  left), `TestCleanTempRemovesPullDirs`.
- `internal/importer/gio_test.go` (hooks `pull`, `isDevice`, `gioOK`):
  `TestDevicePullStages` (device files pulled once, never opened through
  FUSE; a local file in the same import is), `TestDevicePullLogsGio`,
  `TestDevicePullLogsGioForGvfsDir`, `TestDevicePullFallsBack` (the
  three-failure rule with a reset in between: one warning, a debug line
  per failed pull, `BytesCopied` exact), `TestDevicePullNoGio` (gio
  unavailable from the start: no pull, no warning),
  `TestDevicePullSizeMismatch`, `TestDevicePullMoveVerifies` (second
  pull; a differing second pull of the same size keeps the source),
  `TestDevicePullMoveVerifyFallsBack`, `TestDevicePullShortMove`.

### Not verified

The development container has neither gvfs nor gio: the real
`gio copy` from an MTP or gphoto2 mount, its speed, its exact error
messages (no longer matched: any failure retries the other form),
whether the gphoto2 backend pulls whole files, and what the daemon does
with a pull whose gio client was killed (it may keep writing the
unlinked temp file until the transfer ends, or create it late in the
removed pull directory) are untested. gio needs the session's D-Bus; run
outside the desktop session every pull fails and after three files the
import falls back to FUSE with the warning.

### Review fixes (gvfs)

Fixes for the review of `fix/import-gio` (27da386..b74a5cd).

1. Short verification pull (serious): `HashPulled` takes the source
   path, stats it and returns an error wrapping `ErrSizeChanged` when
   the pulled size differs, like `StagePulled`. `hashSource` then reads
   that file through FUSE, so a device whose pulls come back short no
   longer blocks a move (before: every run ended in `ErrHashMismatch`).
   Test `TestDevicePullShortMove`: a pull always one byte short with
   `--move` imports and moves the file with 2 pulls and 2 FUSE opens.
2. One failed pull no longer turns pulling off for the run: that file is
   read through FUSE (debug line), and pulling is turned off only after
   three failed pulls in a row (`maxPullFailures`; a pull that works
   resets the count), with one warning. A pulled copy of the wrong size
   is not a pull failure. Test `TestDevicePullFallsBack`.
3. Retry on any failure: `gvfs.pull` no longer matches English "not
   mounted" messages (they come translated from the daemon); when the
   first form fails and ctx is not cancelled, the other form is tried
   once and the working one remembered (`plainFirst`, now also cleared
   when the URI form is the one that works). gio runs with `LC_ALL=C`.
   Tests `TestPullRetriesOtherForm` (both orders), `TestPullFailure`.
4. Per-pull temp directory: `pullTemp` pulls into
   `<tmp>/pull-*/copy.part` and renames the result onto a `copy-*.part`
   reservation; the name gio writes is never one another `CreateTemp`
   can draw. The design text no longer calls the shared temp directory
   private. `CleanTemp` already removed every entry with `RemoveAll`, so
   stale `pull-*` directories are covered (doc comment and
   `TestCleanTempRemovesPullDirs` added).
5. Late file after cancellation: the whole pull directory is removed on
   failure or cancellation, so a file the daemon opens after gio was
   killed finds no directory (tested with a late write).
6. Logging and symlinks: `gvfs.Dir()` resolves symlinks when the
   directory exists (`TestDirSymlinked`); `gio=true` is also logged when
   the source root is the gvfs directory itself
   (`TestDevicePullLogsGioForGvfsDir`).
7. Tests: the "no gio" case now has gio unavailable from the start
   (`TestDevicePullNoGio`: no pull, no warning, every file through FUSE)
   instead of a pull hook returning `ErrUnavailable`; `TestPull` no
   longer pre-creates the destination; the differing verification pull
   in `TestDevicePullMoveVerifies` keeps the source's size, so it still
   tests the hash mismatch rather than the new size fallback.

## Read-back of the library copy (#27)

Branch `fix/import-move-readback`. Before this, nothing read the library
copy back from the medium: the copy was hashed as it was written, fsynced
and renamed into place, the catalog committed, and a move source was then
deleted. Silent corruption below `write()` (USB-SATA bridges, firmware,
NTFS/exFAT drivers, bit flips after the user buffer was hashed) went
unnoticed, and in a move the only other copy was gone. The inter-library
move and the backup already read their copies back (`CopyReadBack`).
Users also clear a card by hand after a Copy import, so Copy gets the
same check as Move.

### Rule

Every library file an import produced or accepted is read back from the
disk and its hash compared with the catalog hash before its result is
reported as imported or existing, and before a move source is deleted:

| path | read back | source deleted after it (Move) |
| --- | --- | --- |
| copy-first copy (`library.Commit`) | yes | yes (cross-device or `ForceCopy` move) |
| hash-first `CopyVerified` (symlink source, cross-device rename fallback, zip entries) | yes | yes, except zip entries |
| identical file accepted at the destination (copy-first and hash-first) | yes | yes |
| restore from the trash (`restoreTrashed`) | yes, the trashed copy before the rename out of the trash | yes |
| same-device rename, file in place, record in place | no (no new bytes) | no source left / nothing deleted |

`library.ReadBackUncached` (the existing `readBackUncached` that
`CopyReadBack` uses) reads the file through a fresh descriptor after
`posix_fadvise(POSIX_FADV_DONTNEED)` on Linux. On Windows, macOS and the
BSDs `dropCache` is a no-op, so there the read may come from the cache:
it then proves the bytes reached the file system, not the drive. (Ways
to bypass the cache there, untested and not done: `F_NOCACHE` on macOS,
which does not evict pages that are already cached, and
`FILE_FLAG_NO_BUFFERING` on Windows, which needs sector-aligned buffers
and reads.)

The source is never read again for this; the check compares against the
hash in the catalog row, which is the hash of the bytes staged (and, in a
move, re-read from the source by `verifySource`).

### Where it runs

The writer commits a batch (`record`) and hands it to a finisher
goroutine (`finish`) through a channel with room for one batch, then
collects and commits the next batch while the finisher reads the copies
of the previous one back, `readBackWorkers` (2) at a time. `finish`
undoes items that lost the hash race (as before), checks the others
(`readBackAt`), then reports each one and deletes a move source
only after its check passed. `commit` (record + finish on one
goroutine) is kept for the tests. `writer` returns only after the
finisher is done, so `pipeline` and `Run` still return after every
result. The read-back is not cancellable: a committed file is always
checked before it is reported; after a cancellation the finisher still
finishes the batches already committed (at most two).
`Stats.Verifying` counts the read-backs still pending once the run's
context is done (0 otherwise), the log gets one "stopping: verifying the
copies already recorded" line, the CLI status line shows `Verifying N
recorded copies` and the web job card says it is reading them back.

`restoreTrashed` runs the same check, in a worker, on the trashed copy
before the restore (a rename within the library, which does not touch
the bytes), in Copy as in Move.

### On a failure

A copy that reads back with another hash or size
(`library.ErrHashMismatch`, "reads back from the disk as …, not …") or
that cannot be read (the read error as it is) is an error result
(`readBackFailed`), with `Dest` the library path. The source is never
deleted. The item is taken back out of the library, so that the content
is not in the catalog: its new row is removed (`catalog.Remove`, a
transaction of its own) and, for a copy this import wrote (`copied`),
the library file too (`undo`, which also takes its bytes off
`BytesCopied`). An identical file that was already at the destination
is left as it was; only the row this import added goes. The message is
"the library copy does not read back from the disk as written" or "the
library copy could not be read back from the disk", then "; the copy
was removed and the file is not imported" or "; the file already at
this path was left as it was and the file is not imported", then "; the
source is kept" (Move) or "; keep the source" (Copy), then "; check the
library drive, then import again". The claims and the in-flight hash are
released only after the row is gone.

Why undo rather than keep: a kept row holds the source's hash, so the
next import of the same source found it in the catalog and reported a
duplicate with no error. The user would then clear the card and keep
only the damaged copy. With the row gone a retry copies the file again
(or fails the same way again), and a transient read error of a sound
copy costs only a second copy. If the row cannot be removed, the row and
the file are kept and the message says so, pointing at `verify
--rehash`.

A trashed copy that fails its read-back is not restored: the row stays
trashed and the file stays in the trash, the source is kept, and the
error is "the file is in the trash, but its trashed copy does not read
back from the disk as written; delete it in the trash, then import
again; the source file is kept". Every later import of that content
fails the same way (never a duplicate) until the copy is deleted.

The import counts these under Errors and exits 2, like any other file
it could not import.

Not covered (follow-up): rows recorded before a crash or a kill, whose
read-back never ran, are never read back later; their move sources were
not deleted either, so a later import of them reports duplicates.

### Is the read uncached on Linux?

`DONTNEED` drops only clean pages. `library.Commit` and `CopyVerified`
fsync the temp file before the rename, so by the time of the read-back
the pages are clean. Measured with `mincore` on ext4 (a 64 MiB file
written with `os.WriteFile`): 16384/16384 pages resident after the
write; 2048/16384 after `DONTNEED` without an fsync (only the pages
already written back were dropped); 0/16384 after fsync + `DONTNEED`.
An identical file at the destination and a file restored from the trash
were not written by this import, so their pages are clean too. The
libraries here are on `ntfs3` (a kernel driver that uses the page cache
of the file, so `DONTNEED` applies the same way). It cannot bypass the
drive's own cache. A FUSE file system such as ntfs-3g reads through the
block device's cache, which `DONTNEED` on the file does not drop.

### Cost

Synthetic import into a library on the system NVMe drive (ext4, 91 %
full), source on the same drive with its pages dropped before each run,
300 files of 10 MiB (3 GiB) of random bytes, a stub extractor, no
thumbnails, 4 workers, cross-device forced for Move. Seconds per run,
four runs each, the arms interleaved in one test binary (no read-back
vs. read-back), `readBackWorkers` 2:

| | no read-back | read-back | median |
| --- | --- | --- | --- |
| Copy | 8.6, 9.9, 10.8, 11.7 | 12.5, 13.3, 14.8, 27.8 | 10.3 → 14.0 s (+35 %) |
| Move | 8.6, 11.2, 14.3, 21.8 | 11.9, 11.9, 12.9, 14.2 | 12.8 → 12.4 s (within noise) |

The read-backs of a run summed 22–27 s of reader time (51 s in the
outlier), all overlapped with the copy of the following batches. Single
runs of about 28 s occurred in every arm, also without the read-back:
background writeback on the drive. Earlier runs with 1 and 4 readers
gave the same medians within that noise (Copy 13.6 and 11.9 s against
9.3 s before the change, built from master). On this drive the source,
the copies and the read-back share one device, so Copy pays for reading
3 GiB more. When the source is a card or a phone, it is usually slower
than the library drive and the read-back runs while the next files are
read from the source. When the library drive is the bottleneck (a USB
hard disk), expect up to the time it takes to read the imported data
from that drive.
