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
   `"<origin>: changed while it was read"` and the temp is removed. The
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
