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
- **Verified Copies**: Each copy goes through a temp file, is hash-checked, fsynced and renamed into place before it is recorded in the catalog.
- **Safe Moves**: With `--move`, source files are deleted only after the catalog transaction that records them has committed.
- **Parallel Import**: Hashing and copying run in a worker pool, and catalog writes are batched in transactions.
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

The source may be a folder, a single file or a `.zip` archive. Files whose content is already in the catalog are skipped as duplicates. If the library has no `media.db` yet, a new library is created there (`Creating new library at <path>` is printed first, and the summary marks the library as new).

Press Ctrl+C to stop cleanly: files already copied are recorded, the partial summary is printed and the command exits with status 130. Press Ctrl+C a second time to terminate at once.

After the files are copied, the thumbnail stage generates a thumbnail and a preview for every file imported in this run (one worker per CPU, at most 16). The status line shows `Thumbs N` during that stage, and the summary lists the thumbnails done, unsupported (for example a video without `ffmpeg`, a RAW file without an embedded preview, or an image above 100 megapixels) and failed. Ctrl+C during the thumbnail stage keeps every imported file; the remaining thumbnails stay pending and `picmover thumbs` generates them later.

Exit status: 0 on success; 2 when some files could not be imported or thumbnails failed (they are counted under `Errors` and listed in the log; with `--move` they are left in the source), or when the thumbnail stage could not run at all (for example an incompatible thumbnail cache: the files are imported, the cache error is printed as a warning and the thumbnails stay pending); 130 when cancelled; 1 for any other error, never after the files were imported successfully. So `picmover import --move a lib && rm -rf a` only deletes `a` when everything was imported.

Flags:

- `--move`: move files instead of copying. Sources are deleted only after they are recorded.
- `--min-dimension N`: skip images whose width and height are both below N pixels (0 = no limit).
- `--workers N`: number of parallel workers (0 = auto).
- `--timezone ZONE`: IANA zone for times stored as instants (video UTC, file modification time). Defaults to the system zone.
- `--quiet`: do not show the live status line.
- `--no-thumbs`: skip the thumbnail stage; the files are marked pending for `picmover thumbs`.

Importing a library onto itself (the same path as source and library) records files that are in the library but not in the catalog; files outside the `<type>/YYYY/MM` layout are copied (or with `--move` moved) into it. To recreate the whole catalog without touching any file use `rebuild`.

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

### Rebuild

To recreate the catalog from the files in the library:

```
./picmover rebuild /path/to/library
```

The current `media.db` (with its `-wal`, `-shm` and `-journal` files) is renamed to `media.db.bak-<YYYYMMDD-HHMMSS>`. Then every media file under the library root is recorded where it is, whatever folder it is in: rebuild never copies, moves, renames or deletes a file. A file whose content is already recorded (an identical copy elsewhere in the library) is counted as a duplicate and left in place; zip archives are not extracted. Finally the thumbnail stage runs. Thumbnails are cached by content hash, so they are reused and not generated again. The backup name, the import summary and the thumbnail summary are printed; the exit status is as for `import`. Flags: `--workers`, `--timezone`, `--quiet` as for `import`.

To undo a rebuild, delete the new `media.db` (and its `-wal`/`-shm`) and rename the newest `media.db.bak-<timestamp>` files back to `media.db`. If `media.db` is missing but backups exist, `rebuild` says so and explains this. To move stray files into the layout instead, use `picmover import <library> <library>`.

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

Every catalog row must point to an existing file with the recorded size. The command also lists files under `image/`, `image_raw/` and `video/` that are not in the catalog, files whose name matches a catalog row only when case is ignored (they would collide when the library is copied to Windows or macOS), and catalog rows whose paths differ only in case. It exits with status 1 when any problem is found.

Flags:

- `--fill-size`: record the file size for rows whose size is unknown (not for rows whose hash does not match with `--rehash`). Catalogs created by this version always record sizes.
- `--rehash`: re-hash every file and compare with the catalog (slow).

Usage errors (a missing argument, an unknown flag) end with a hint such as `see 'picmover import --help'`. Empty arguments (for example an unset shell variable) are rejected.

## Library Layout

```
<library>/
  image/YYYY/MM/<name>
  image_raw/YYYY/MM/<name>
  video/YYYY/MM/<name>
  media.db              catalog
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
