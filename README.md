# PicMover - Media Import and Organization Tool

## Overview

PicMover is a robust command-line tool designed to efficiently import, organize, and manage large collections of images and videos. It's built in Go and offers features like duplicate detection, metadata extraction, and intelligent file organization.

## Features

- **Intelligent File Organization**: Automatically organizes files into `<type>/YYYY/MM/` based on their capture date and file type.
- **Duplicate Detection**: Uses hash-based comparison to prevent duplicate imports.
- **Metadata Extraction**: Extracts and stores EXIF data for images, including camera information and GPS coordinates.
- **Support for Various File Types**: Handles different image formats (JPEG, PNG, TIFF) and RAW files (CR2, DNG, etc.), as well as common video formats.
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
- For video metadata only: `ffprobe` (part of FFmpeg) on the PATH, or placed next to the `picmover` binary. `ffmpeg` itself is not needed.

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

Exit status: 0 on success; 2 when some files could not be imported (they are counted under `Errors` and listed in the log; with `--move` they are left in the source); 130 when cancelled; 1 for any other error. So `picmover import --move a lib && rm -rf a` only deletes `a` when everything was imported.

Flags:

- `--move`: move files instead of copying. Sources are deleted only after they are recorded.
- `--min-dimension N`: skip images whose width and height are both below N pixels (0 = no limit).
- `--workers N`: number of parallel workers (0 = auto).
- `--timezone ZONE`: IANA zone for times stored as instants (video UTC, file modification time). Defaults to the system zone.
- `--quiet`: do not show the live status line.

Importing a library onto itself (the same path as source and library) rebuilds the catalog from the files; it works with or without `--move`.

### Database Query

To show statistics about the library and its most recent files:

```
./picmover db /path/to/library
```

Flags:

- `--list`, `-l`: list catalog rows as a table.
- `--limit N`, `-n N`: maximum number of files to show (default 10, 0 = no limit).
- `--type TYPE`, `-t TYPE`: only show files of this type: `image`, `image_raw` or `video`.

### Update Metadata

To re-read capture metadata (time, GPS, dimensions, camera) from the files in the catalog:

```
./picmover update-metadata /path/to/library
```

Flags:

- `--type TYPE`, `-t TYPE`: type of media to update: `all`, `image`, `image_raw` or `video` (default `all`).
- `--dry-run`, `-d`: show changes without writing them.
- `--timezone ZONE`: IANA zone for times stored as instants (video UTC, file modification time, legacy UTC), applied to every row. Without it, such rows keep the UTC offset already stored in the catalog, so running on a machine in another time zone does not change them; other rows use the system zone.
- `--verbose`, `-v`: print extractor warnings to stderr, and the rows skipped because extraction degraded.

`update-metadata` never makes the catalog worse: if a file's date can now only be taken from its modification time, but the catalog has a date read from the file's metadata, the row is left alone and counted as `Skipped (extraction degraded)`; and an empty new value (no GPS, no camera, unknown size) never replaces a known one. If the catalog has videos, `ffprobe` must be available (or use `--type` to skip videos). It exits with status 2 when any row could not be updated.

### Verify

To check the catalog against the files in the library:

```
./picmover verify /path/to/library
```

Every catalog row must point to an existing file with the recorded size. The command also lists files under `image/`, `image_raw/` and `video/` that are not in the catalog, files whose name matches a catalog row only when case is ignored (they would collide when the library is copied to Windows or macOS), and catalog rows whose paths differ only in case. It exits with status 1 when any problem is found.

Flags:

- `--fill-size`: record the file size for rows whose size is unknown (not for rows whose hash does not match with `--rehash`).
- `--rehash`: re-hash every file and compare with the catalog (slow).

### Migrate

To upgrade the catalog of an existing library to the current format:

```
./picmover migrate /path/to/library
```

See [Upgrading from the Previous Version](#upgrading-from-the-previous-version).

Usage errors (a missing argument, an unknown flag) end with a hint such as `see 'picmover import --help'`. Empty arguments (for example an unset shell variable) are rejected.

## Library Layout

```
<library>/
  image/YYYY/MM/<name>
  image_raw/YYYY/MM/<name>
  video/YYYY/MM/<name>
  media.db              catalog
  .picmover/logs/       import logs
  .picmover/tmp/        in-progress copies
```

Times are stored as the camera's wall-clock time, plus the UTC offset when it is known. `--timezone` only affects videos and files without an embedded timestamp.

## Upgrading from the Previous Version

- Run `picmover migrate <library>` to upgrade an existing `media.db`; the first `import` into the library also migrates it. `db`, `verify` and `update-metadata` do not write to an old catalog: they refuse with a message to run `picmover migrate` first. Before migrating, a backup is written next to the catalog as `media.db.v1-<timestamp>.bak`.
- The absolute paths stored by the old version become relative to the library root.
- Run `picmover verify <library> --fill-size` once afterwards to record file sizes.
- Optionally run `picmover update-metadata <library>` to refresh metadata fields that the old version did not store.
- The old version stored video times in UTC; after migrating they are marked `legacy_utc` and shown in UTC. Run `picmover update-metadata --timezone <zone> <library>` (for example `--timezone Europe/Helsinki`) to convert them to local time in that zone. Without `--timezone` they keep their stored offset.

## Notes

- The application creates a `media.db` file in the library root to store file information.
- RAW files are stored separately from standard image files for easier management.
- The import process can be safely interrupted. Re-running it skips files that are already in the catalog.

## Limitations

- Video metadata extraction requires `ffprobe` (part of FFmpeg) to be installed on the system.
- HEIC, WebP and AVIF files are not imported yet.
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
