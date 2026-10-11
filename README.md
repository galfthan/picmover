# PicMover - Media Import and Organization Tool

## Overview

PicMover is a robust command-line tool designed to efficiently import, organize, and manage large collections of images and videos. It's built in Go and offers features like duplicate detection, metadata extraction, and intelligent file organization.

## Features

- **Intelligent File Organization**: Automatically organizes files into `<type>/YYYY/MM/` based on their capture date and file type.
- **Duplicate Detection**: Uses hash-based comparison to prevent duplicate imports.
- **Metadata Extraction**: Extracts and stores EXIF data for images, including camera information and GPS coordinates.
- **Support for Various File Types**: Images (JPEG, PNG, TIFF, HEIC/HEIF, AVIF, WebP, GIF, BMP), RAW files (CR2, CR3, NEF, ARW, DNG, ORF, RW2, RAF, PEF, SRW, ...) and common video formats. See [Supported Formats](#supported-formats).
- **Thumbnails and Previews**: Every imported file gets a 256 px thumbnail, a 1600 px preview and a perceptual hash (for finding near-duplicates later), cached by content hash in `.picmover/`.
- **Database Management**: Uses SQLite to maintain a catalog (`media.db`) of all imported files for quick access and management. SQLite is built in (pure Go, no cgo).
- **Portable Libraries**: Paths in the catalog are relative to the library root, so one library works from Linux, Windows and macOS.
- **Safe Copies**: Each source file is read once, into a temp file inside the library, hashing the bytes as they are written; duplicates are dropped and metadata is read from that copy, which is then fsynced and renamed into place before it is recorded in the catalog. The hash recorded is the hash of the bytes written. A copy reads the source once; a move reads it twice: before the original is deleted it is read again, and nothing is placed or deleted unless that second read gives the same hash as the copy (otherwise the file is an error and the source is kept). A file that changes size while it is read is an error and is not imported.
- **Safe Moves**: With `--move`, source files are deleted only after the catalog transaction that records them has committed.
- **Parallel Import**: Hashing and copying run in a worker pool, and catalog writes are batched in transactions.
- **Backups**: `backup` keeps verified, move-aware copies of a library on other drives or shares, `scrub` checks them for bit rot, `restore` brings files back. See [Backups](#backups).
- **Verification**: A `verify` command checks the catalog against the files in the library, with optional re-hashing.
- **Performance Optimized**: Designed to handle large collections with hundreds of thousands of files efficiently.

## Installation

### Prerequisites

- Go 1.27 or newer. The `toolchain` line in `go.mod` fetches the required toolchain automatically.
- For videos only: `ffprobe` (metadata) and `ffmpeg` (video thumbnails), both part of FFmpeg, on the PATH or placed next to the `picmover` binary. Without `ffprobe` videos are imported with their file time; without `ffmpeg` their thumbnails are marked unsupported. Everything else is pure Go: no cgo and no system libraries.

### Steps

1. Clone the repository:
   ```
   git clone https://github.com/galfthan/picmover.git
   ```

2. Navigate to the project directory:
   ```
   cd picmover
   ```

3. Build the application:
   ```
   make build
   ```

   `make build` first builds the web frontend (`make web`, needs Node 24; the Makefile uses `~/.local/node/bin` when it exists) and embeds it in the binary. A plain `go build -tags nodynamic .` works without Node; `picmover serve` then answers the browser with a note that the frontend is not built, while the API works.

   The Makefile passes `-tags nodynamic` to every `go build`, `go test` and `go vet`, so the HEIC/AVIF/WebP codecs use their embedded WebAssembly builds and never load a system library. Pass the tag yourself when you run the Go tools directly, for example `go test -tags nodynamic ./...`.

   To cross-compile all targets, run `make build-all`. It writes:
   - `dist/picmover-linux-amd64`
   - `dist/picmover-windows-amd64.exe`
   - `dist/picmover-darwin-arm64`
   - `dist/picmover-darwin-amd64`

## Usage

### Import

To import media from a source into a library:

```
./picmover import /path/to/source /path/to/library
```

The source may be a folder, a single file or a `.zip` archive. Files whose content is already in the catalog are skipped as duplicates.

Each source file is read once: it is copied into `<library>/.picmover/tmp`, hashed while it is copied, and the duplicate check and the metadata use that copy. This keeps imports from slow sources fast, such as a phone mounted over MTP or PTP (gvfs FUSE mounts on Linux), where every extra open of a file is a new USB transfer. On a FUSE source only one worker reads from the source at a time (`--source-readers`, which also covers zip archives on the source); the others extract metadata and place files meanwhile. The temp files are written before the duplicate check, so the library volume needs free space for up to `--workers` files at a time, duplicates included. The status line shows the transfer rate while bytes are being copied. A duplicate is still read once in full, to hash it. With `--move` to another volume each source file is read a second time to verify the copy before the original is deleted. With `--move` on the same volume the file is hashed in place and renamed, without a copy. If the library has no `media.db` yet, a new library is created there (`Creating new library at <path>` is printed first, and the summary marks the library as new).

Phones on Linux: a phone or camera mounted by gvfs (the file manager's MTP or PTP mount, under `/run/user/<uid>/gvfs/mtp:host=...` or `gphoto2:host=...`, also listed in the browser's folder picker) is slow to read through its FUSE mount, about 1 MB/s, because every read is a separate USB request. With `gio` installed (part of GLib, present on every Linux desktop) picmover has the gvfs daemon pull each file whole with `gio copy`, at USB speed, the way the file manager copies; the files are then hashed and read from the library's temp copy. Without `gio` the FUSE mount is read as before. If a `gio copy` fails, that file is read through FUSE; after three failed pulls in a row a warning `gio copy failed repeatedly, reading the rest through FUSE` is logged and the rest of the import reads through FUSE. `gio` needs the desktop session (it talks to the gvfs daemon over D-Bus), so run the import from that session. The import log's `import started` line shows `gio=true` when the source is on a phone mount (or is the gvfs directory itself) and `gio` is used. Zip archives on a phone are still read through FUSE.

Press Ctrl+C to stop cleanly: files already copied are recorded, the partial summary is printed and the command exits with status 130. Press Ctrl+C a second time to terminate at once.

After the files are copied, the thumbnail stage generates a thumbnail and a preview for every file imported in this run (one worker per CPU, at most 16). The status line shows `Thumbs N` during that stage, and the summary lists the thumbnails done, unsupported (for example a video without `ffmpeg`, a RAW file without an embedded preview, or an image above 100 megapixels) and failed. Ctrl+C during the thumbnail stage keeps every imported file; the remaining thumbnails stay pending and `picmover thumbs` generates them later.

Exit status: 0 on success; 2 when some files could not be imported or thumbnails failed (they are counted under `Errors` and listed in the log; with `--move` they are left in the source), or when the thumbnail stage could not run at all (for example an incompatible thumbnail cache: the files are imported, the cache error is printed as a warning and the thumbnails stay pending); 130 when cancelled; 1 for any other error, never after the files were imported successfully. So `picmover import --move a lib && rm -rf a` only deletes `a` when everything was imported.

Flags:

- `--move`: move files instead of copying. Sources are deleted only after they are recorded.
- `--min-dimension N`: skip images whose width and height are both below N pixels (0 = no limit).
- `--workers N`: number of parallel workers (0 = auto).
- `--source-readers N`: how many workers read from the source at once (0 = auto: 1 when the source is a FUSE mount such as a phone, otherwise `--workers`).
- `--timezone ZONE`: IANA zone for times stored as instants (video UTC, file modification time). Defaults to the system zone.
- `--quiet`: do not show the live status line.
- `--no-thumbs`: skip the thumbnail stage; the files are marked pending for `picmover thumbs`.

Importing a library onto itself (the same path as source and library) records files that are in the library but not in the catalog; files outside the `<type>/YYYY/MM` layout are copied (or with `--move` moved) into it. To recreate the whole catalog without touching any file use `rebuild`.

### Browsing: picmover serve

To browse a library and import from a web browser:

```
./picmover serve /path/to/library
```

This starts a local web server on `127.0.0.1:7439` (another free port when that one is busy), prints `Serving <library> at http://127.0.0.1:7439/` and opens the browser. The page has a date-ordered photo grid, a map, an import wizard with live progress and library statistics. Imports started there run exactly like `picmover import` (same log files under `.picmover/logs/`, same exit codes shown as the outcome); one job runs at a time. Press Ctrl+C to stop: a running import is cancelled safely first, and the command exits with status 0.

`serve` creates a new library when the directory does not exist or is empty, so the first import can be started from the browser. A folder that already holds files but no `media.db` is refused: record its files in place with `picmover rebuild <dir>` first, or import into it with `picmover import <source> <dir>`. The server answers only this machine and only its own page: requests with another `Host` or `Origin` are refused, so other web sites cannot read the library or start imports.

Flags:

- `--addr HOST:PORT`: listen address (default `127.0.0.1:7439`). A non-loopback address makes the library reachable from the network without any authentication; a warning is printed.
- `--no-open`: do not open the browser (it is also not opened when stdout is not a terminal).
- `--cors-origin URL`: allow a development page at URL (for example `http://localhost:5173`) to call the API cross-origin; repeatable.
- `--verbose`: log every request to stderr.
- `--workspace`: serve the folder as a workspace (below) even when it holds no library yet; it is created when absent.

**Workspaces.** `picmover serve /media`, where `/media` holds libraries as subfolders (`/media/peter`, `/media/penny`), serves all of them: the browser starts with a page listing the libraries (cover, counts, date span, state), opens any of them, creates new ones, and records a plain subfolder in place (the same as `picmover rebuild`) so it becomes a library. The folder is served as a workspace when it has no `media.db` itself and at least one subfolder has one, or with `--workspace`; it prints `Serving workspace /media (N libraries) at ...`. Each library is locked only once it is opened in the browser, so the others stay free for the CLI; a library in use by another picmover process is shown with that process. `picmover serve /media/peter` still serves one library.

**Ratings, tags, albums, trash.** Select photos in the grid (checkbox, Ctrl/Cmd-click, Shift-click for a range) or use the lightbox to rate (0–5), tag, add to albums, or move to the trash. This user data lives in `<library>/user.db`, keyed by content hash; `rebuild` never changes it (it only adds its import record), so back it up with your photos. Deleting moves a file to `<library>/.picmover/trash/` (a rename on the same volume, never a copy) and hides it; restore puts it back (under `name_1.ext` when the original name is taken meanwhile). Emptying the trash, after an explicit confirmation, is the only way picmover deletes a file. The filters gain minimum rating, tags, album and "every year" date ranges (for example 24–26 December of every year).

**Import history.** Every import and rebuild is remembered in `user.db` (so a `rebuild` never loses it): when it ran, where the files came from, how it went, and which pictures it brought in. The Imports page lists the records newest first; one click shows an import's pictures in the grid (the `import` filter), where a record can be renamed or deleted (deleting a record never touches a file). A rebuild's record keeps only its counts, not a list of pictures: it covers the whole library, so its page links to the plain grid. The detail panel says when and from where a picture was imported. `import` and `rebuild` print the record's id (`Import record: #12`); a record that cannot be written never fails the import (the summary and the log say so). Record ids are never reused. An import whose process died (power loss, kill) is closed as failed the next time the library is opened, with the pictures it had already brought in. The first time this version opens a library (`serve`, `import`, `rebuild`, `imports --rename`/`--delete`), the imports its catalog remembers are copied into the history (a rebuild reads them from the old catalog, also one of an older format; if that fails it warns and tries again later).

```
./picmover imports /path/to/library                      # list: id, start, mode, outcome, source, counts, bytes, name (reads user.db only)
./picmover imports /path/to/library --rename 12 "Phone, summer"   # an empty name clears it
./picmover imports /path/to/library --delete 12          # forgets the record; the files stay
```

**Near-duplicates.** The Duplicates page runs a job that compares the perceptual hashes computed with the thumbnails (Hamming distance 1–7, default 6; 136k photos take well under 10 s) and shows groups of similar photos: keep one and trash the rest, or ignore the group.

Frontend development: run `picmover serve /path/to/library --no-open --cors-origin http://localhost:5173` and, in `web/`, `npm run dev`; Vite serves the app on `http://localhost:5173` with hot reload and proxies `/api` to `127.0.0.1:7439` (the proxy rewrites the `Host` header, `changeOrigin: true`, so the server's host check passes). The `--cors-origin` flag accepts the dev page's `Origin` header, which browsers send on every POST even through the proxy, and also lets the page call `http://127.0.0.1:7439/api/...` directly, bypassing the proxy. `make check` runs the Go vet and tests plus the frontend checks and tests.

### Thumbnails

To generate the thumbnails and previews that are still pending (after `import --no-thumbs` or a cancelled import), retry the files whose thumbnails failed before, and regenerate stale ones:

```
./picmover thumbs /path/to/library
```

Flags:

- `--workers N`: number of parallel workers (0 = one per CPU, at most 16).
- `--rebuild`: regenerate the thumbnails of every file, not only the pending, failed and stale ones.
- `--limit N`: process at most N files (0 = no limit).
- `--quiet`: do not show the live status line.

Every run retries the files whose thumbnails failed, so a file that can never be decoded is reported as failed (exit status 2) each time. A file becomes `stale` when `update-metadata` changes its orientation; `thumbs` then regenerates its thumbnail and preview even though they are cached.

It exits with status 2 when thumbnails failed for some files (they are listed in the log in `.picmover/logs/`), and 130 when cancelled.

### One writer at a time

Commands that change a library (`serve` for as long as it runs, `import`, `rebuild`, `thumbs`, `update-metadata`, `verify --rehash`/`--fill-size`, and `backup`, `prune`, `restore` and `scrub --library`) hold a lock on `<library>/.picmover/lock`. A second such command on the same library stops at once with `library is in use by another picmover process (pid N, <command>, since <time>)`; stop that process first (for example the browser server) and try again. `db` and plain `verify` only read and work at any time. The lock belongs to the running process, so it disappears when that process ends, even after a crash: there is never a stale lock to delete. On a file system that cannot lock files (some network mounts) a warning is printed and the command runs without the lock.

### Rebuild

To recreate the catalog from the files in the library:

```
./picmover rebuild /path/to/library
```

The current `media.db` (with its `-wal`, `-shm` and `-journal` files) is renamed to `media.db.bak-<YYYYMMDD-HHMMSS>`. Then every media file under the library root is recorded where it is, whatever folder it is in: rebuild never copies, moves, renames or deletes a file. A file whose content is already recorded (an identical copy elsewhere in the library) is counted as a duplicate and left in place; zip archives are not extracted. Finally the thumbnail stage runs. Thumbnails are cached by content hash, so they are reused and not generated again. The backup name, the import summary and the thumbnail summary are printed; the exit status is as for `import`. Flags: `--workers`, `--timezone`, `--quiet`, `--no-thumbs` as for `import`.

Files in the trash (`.picmover/trash/`) are recorded as trashed, with the time they were trashed from `user.db` (else their modification time); the ratings, tags and albums in `user.db` are never changed, and the rebuild is recorded in its import history (mode `rebuild`, counts only). A folder without `media.db` (for example a tree already in the `<type>/YYYY/MM` layout) must exist and is handled the same way, without a backup: rebuild records the existing files into a new catalog without copying or moving any of them.

To undo a rebuild, delete the new `media.db` (and its `-wal`/`-shm`) and rename the newest `media.db.bak-<timestamp>` files back to `media.db`. If `media.db` is missing but backups exist, `rebuild` names the newest one before recording the files, so a backup that should have been restored is noticed. To move stray files into the layout instead, use `picmover import <library> <library>`.

### Database Query

To show statistics about the library and its most recent files:

```
./picmover db /path/to/library
```

Flags:

- `--list`, `-l`: list catalog rows as a table.
- `--limit N`, `-n N`: maximum number of files to show (default 10, 0 = no limit).
- `--type TYPE`, `-t TYPE`: only show files of this type: `image`, `image_raw` or `video`.

The summary includes the thumbnail states (ok, pending, unsupported, failed; stale files are counted as pending) and the size of the thumbnail cache; `--list` shows each file's thumbnail state. `db` only reads the thumbnail cache: if it is locked, damaged or of another format, it prints `Thumbnail cache: unavailable (<reason>)` and carries on.

### Update Metadata

To re-read capture metadata (time, GPS, dimensions, camera) from the files in the catalog:

```
./picmover update-metadata /path/to/library
```

Flags:

- `--type TYPE`, `-t TYPE`: type of media to update: `all`, `image`, `image_raw` or `video` (default `all`).
- `--dry-run`, `-d`: show changes without writing them.
- `--timezone ZONE`: IANA zone for times stored as instants (video UTC, file modification time), applied to every row. Without it, such rows keep the UTC offset already stored in the catalog, so running on a machine in another time zone does not change them; other rows use the system zone.
- `--verbose`, `-v`: print extractor warnings to stderr, and the rows skipped because extraction degraded.

`update-metadata` never makes the catalog worse: if a file's date can now only be taken from its modification time, but the catalog has a date read from the file's metadata, the row is left alone and counted as `Skipped (extraction degraded)`; and an empty new value (no GPS, no camera, unknown size) never replaces a known one. If the catalog has videos, `ffprobe` must be available (or use `--type` to skip videos). When a file's orientation changes, its thumbnail state becomes `stale` and the next `picmover thumbs` regenerates its thumbnail and preview. It exits with status 2 when any row could not be updated.

### Verify

To check the catalog against the files in the library:

```
./picmover verify /path/to/library
```

Every catalog row must point to an existing file with the recorded size (a trashed row: its file in `.picmover/trash/`). The command also lists files under `image/`, `image_raw/` and `video/` that are not in the catalog, files whose name matches a catalog row only when case is ignored (they would collide when the library is copied to Windows or macOS), and catalog rows whose paths differ only in case. It exits with status 1 when any problem is found.

Flags:

- `--fill-size`: record the file size for rows whose size is unknown (not for rows whose hash does not match with `--rehash`). Catalogs created by this version always record sizes.
- `--rehash`: re-hash every file and compare with the catalog (slow).

### Backups

A backup target is a folder on another drive or a network share. Attach one to a library (an empty folder becomes a target; it gets a `picmover-backup.json` marker with a random id, so the drive is recognised wherever it is mounted):

```
./picmover targets add --label "Red WD drive" /path/to/library /media/me/RedWD/backups
./picmover targets /path/to/library                 # id, label, last backup and scrub, reachable, folder
./picmover targets forget /path/to/library <id>     # the target's files are left alone
```

Back up (to the folder given, `--target <id>`, `--all-targets`, or the library's only target):

```
./picmover backup /path/to/library [<target-folder>] [--dry-run] [--partial] [--workers N] [--prune [--yes] [--force]] [--scrub 30m] [--take-over]
```

The library's copy is `<target>/<library-name>/`, a plain mirror of the library tree (the trash included) plus `user.db` and `media.db` snapshots, so it can be browsed with any file manager. Files are matched by content hash: a photo moved in the library is renamed on the target, a changed file keeps its earlier content in `<set>/.picmover/previous/`, and only new content is copied, newest first, each copy verified by reading it back. Nothing in the library is changed and nothing on the target is removed by a backup; an interrupted backup is continued by the next one. When the target is too small the backup refuses with the numbers; `--partial` copies the newest files until it is full. Symbolic links are skipped and listed, and so are folders of the library that cannot be read and files whose names differ from another file's only in upper/lower case or Unicode form (a backup cannot hold both: rename one); either makes the backup exit 1. `--dry-run` prints the plan and writes nothing (not even to `user.db`). A backup, scrub, prune or restore holds a lock on the backup set (`<set>/.picmover/lock`), so a second one on the same set, from this or another computer, stops at once naming the process that holds it. Unchanged `user.db` and `media.db` snapshots are not written again. An existing folder `<target>/<library-name>/` that is not a backup set (another library copied there, or any other folder) is never taken over, and a file the set does not know at a path the backup needs is reported and left alone. Logs go to `<library>/.picmover/logs/` and `<set>/.picmover/logs/`.

A backup set belongs to one library, and remembers which copy of it wrote it last (its folder and host). A copy of the library folder (or the library after it was moved, or used from another computer) is refused with `this backup set was last written by the library at <folder> on <host>`; if this copy is the one to keep backing up, run the backup again with `--take-over` (`prune` and `scrub --library` take it too).

```
./picmover scrub <target-folder> [<set>] [--budget 30m|--all] [--library /path/to/library] [--no-repair] [--take-over]
./picmover prune /path/to/library <target-folder-or-id> [--keep 30d] [--yes] [--force] [--take-over]
./picmover restore <target-folder> <set> /path/to/library [--mode missing|user-data] [--force] [--dry-run] [--thumbs]
```

`scrub` re-reads the backed-up files and compares them with the hashes recorded when they were copied, least recently checked first; with `--budget` it stops after that long and the next scrub continues. Damaged and missing files are copied again from the library given with `--library` when it still has the right content. `prune` lists the files the library no longer has (neither the path nor the content, live or trashed) and earlier versions older than `--keep` (`--keep 0d` keeps none), and removes them only with `--yes`. While the library cannot be read completely (an unreadable folder) or catalog files are missing in it, files it still has could look deleted: `--yes` then refuses unless `--force`. `restore` (which also works from a write-protected drive or share) copies back the files a library lacks (never overwriting anything) and `user.db` when it has none; into a library without `media.db` the files are then recorded into a new catalog. `--mode user-data` restores only `user.db` (`--force` replaces an existing one, which is kept as `user.db.bak-<time>`; the snapshot is checked first, against the hash recorded at backup time and with SQLite's integrity check, and the current file is put back if anything fails).

Exit status of `backup`, `scrub`, `prune`, `restore` and `targets`: 0 when everything was verified; 1 when some files failed (listed), a scrub found damage, `--partial` left files, or a backup could not read a library folder or found catalog files missing; 2 when the target or the library could not be used (including usage errors); 130 when cancelled. That makes them suitable for cron or Task Scheduler. In the browser, the Backups page does the same as jobs.

The first command of this version that opens a library's `user.db` upgrades it to schema version 3 (version 2: backup targets and a library id; version 3: the import history); a copy of the old file is kept as `user.db.bak-<time>`. Older picmover versions refuse the upgraded file.

Usage errors (a missing argument, an unknown flag) end with a hint such as `see 'picmover import --help'`. Empty arguments (for example an unset shell variable) are rejected.

## Library Layout

```
<library>/
  image/YYYY/MM/<name>
  image_raw/YYYY/MM/<name>
  video/YYYY/MM/<name>
  media.db              catalog (derived from the files; rebuild recreates it)
  user.db               ratings, tags, albums, trash times, import history (never rebuilt; back it up)
  .picmover/trash/      trashed files, under their library path
  .picmover/thumbs.db   thumbnail cache (256 px JPEGs, keyed by content hash)
  .picmover/previews/   preview cache (1600 px JPEGs, <hh>/<hash>.jpg)
  .picmover/logs/       import, thumbs and rebuild logs
  .picmover/tmp/        in-progress copies
```

The thumbnail and preview caches are derived data: they are safe to delete (stop picmover first), and `picmover thumbs --rebuild <library>` regenerates them.

Times are stored as the camera's wall-clock time, plus the UTC offset when it is known. `--timezone` only affects videos and files without an embedded timestamp.

## Upgrading

The catalog is derived entirely from the files, so there is no migration: when a new version changes the catalog format, every command refuses the old catalog (without changing it) with a message to run

```
picmover rebuild /path/to/library
```

which keeps the old catalog as `media.db.bak-<timestamp>` and recreates it from the files. Libraries from the first version of picmover (absolute paths in `media.db`) are upgraded the same way. Afterwards the backup can be deleted.

Width and height in the catalog are the display (upright) dimensions: a portrait photo stored sideways with an EXIF rotation is recorded as portrait.

## Supported Formats

- Images: JPEG, PNG, TIFF, HEIC/HEIF, AVIF, WebP, GIF, BMP. Dimensions, dates, GPS and camera come from the file header and EXIF (also inside HEIC/AVIF).
- RAW: CR2, CR3, CRW, NEF, ARW, DNG, ORF, RW2, RAF, PEF, SRW, X3F, RAW. Thumbnails come from the JPEG preview embedded in the RAW file; a RAW file without one is marked unsupported.
- Video: MP4, MOV, M4V, AVI, MKV, WebM, MTS/M2TS/TS, MPG, 3GP, WMV, FLV, ASF. Metadata needs `ffprobe`, thumbnails need `ffmpeg`.

All image decoding is pure Go (HEIC and AVIF through WebAssembly builds of libheif and libavif); images above 100 megapixels are not decoded and get no thumbnail.

## Notes

- The application creates a `media.db` file in the library root to store file information.
- RAW files are stored separately from standard image files for easier management.
- The import process can be safely interrupted. Re-running it skips files that are already in the catalog.

## Limitations

- Video metadata extraction requires `ffprobe`, and video thumbnails require `ffmpeg` (both part of FFmpeg).
- The application does not modify or edit the original files; it only copies them to the new location (or moves them with `--move`).

## Contributing

Contributions to PicMover are welcome! Please feel free to submit pull requests, create issues or spread the word.

## License

PicMover is free software: you can redistribute it and/or modify it under the terms of the GNU General Public License as published by the Free Software Foundation, either version 3 of the License, or (at your option) any later version.

This program is distributed in the hope that it will be useful, but WITHOUT ANY WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU General Public License for more details.

You should have received a copy of the GNU General Public License along with this program. If not, see https://www.gnu.org/licenses/.

### What this means:

You are free to use, modify, and distribute this software.
If you distribute this software or any derivative works, you must do so under the GPL v3 license.
You must provide the source code for any distributed versions or modifications.
There is no warranty for this program.

For more details on the GPL v3 license, please visit: https://www.gnu.org/licenses/gpl-3.0.en.html

## Contact

Sebastian von Alfthan
