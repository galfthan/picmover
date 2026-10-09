# Step 1: core restructure — design and contracts

This document is the contract for the `feat/core-restructure` branch. Every
package below is implemented against it so that work on different packages
can proceed in parallel. Read the whole file before writing code.

## Goals

1. **Data safety.** A file is recorded in the catalog only after it has been
   copied through a temp file, hashed on the way, fsynced and renamed into
   place. Sources of a `--move` are deleted only after the catalog
   transaction that records them has committed. Nothing is ever overwritten.
2. **Cross-platform library.** The catalog stores paths relative to the
   library root with forward slashes. The same `media.db` must work when the
   library is seen as `D:\images` from Windows, `/mnt/d/images` from WSL or
   `/Volumes/photos` from macOS. Name collisions are checked
   case-insensitively because NTFS and APFS are case-insensitive.
3. **Pure Go.** SQLite is `modernc.org/sqlite` (driver name `"sqlite"`), so
   `CGO_ENABLED=0 GOOS=windows|darwin|linux go build` works from any host.
4. **Reusable core.** `cmd/` is a thin Cobra layer. All logic lives in
   `internal/` packages that a later HTTP server reuses unchanged.
5. **Performance.** Hash and copy in one streaming pass per file with a
   worker pool; one writer goroutine batches catalog inserts in
   transactions; WAL mode.
6. **Backwards compatible.** An existing v1 `media.db` is migrated in place
   (after a backup) and keeps its xxhash64 values. The CLI commands
   `import`, `db`, `update-metadata` keep working; `verify` is new.

Out of scope for this step: thumbnails, exiftool, HEIC/WebP, the server/UI,
sidecars, backups. Do not add extensions to the media type lists.

## Conventions

- Go 1.27 (see go.mod). `gofmt` clean, `go vet` clean.
- Wrap errors with `%w`; never panic on bad input files.
- Accept `context.Context` on anything that does I/O or may be long-running.
- No package-level mutable state, no global loggers. Use `*slog.Logger`
  passed in; nil means discard.
- Tests use `t.TempDir()`, synthetic fixtures from `internal/testimg`, and
  no network. Keep fixtures tiny (KB, not MB); this dev box is short on disk.
- **Do not run `go mod tidy` or edit go.mod/go.sum**; the orchestrator does
  that once at the end. Run only your own package's tests:
  `go test ./internal/<pkg>/...`.
- Do not touch packages other than the one you are assigned, except to read
  them.

## Package layout

```
main.go                 unchanged: cmd.Execute()
cmd/                    Cobra commands only (root, import, db, update-metadata, verify)
internal/library        DONE. Library root, rel/abs paths, layout, verified copy, hashing
internal/geo            DONE. ISO6709 and "lat,lon" parsing
internal/testimg        DONE. Synthetic JPEG/PNG fixtures with EXIF for tests
internal/catalog        SQLite catalog: schema v2, migration from v1, queries
internal/exif           Bounded EXIF reader (JPEG, TIFF-based raws, CR3, RAF)
internal/metadata       Media classification and metadata extraction (internal/exif, ffprobe)
internal/importer       The import pipeline
```

## internal/library (done — read the source)

Key API:

```go
func Open(dir string) (*Library, error)   // creates dir, .picmover/tmp, .picmover/logs
func Attach(dir string) (*Library, error) // existing directory only, creates nothing (read-only commands)
func (l *Library) Root() string
func (l *Library) DBPath() string            // <root>/media.db
func (l *Library) TempDir() string           // <root>/.picmover/tmp
func (l *Library) LogDir() string            // <root>/.picmover/logs
func (l *Library) Abs(rel string) string     // catalog path -> OS path
func (l *Library) Rel(abs string) (string, error)
func (l *Library) Contains(abs string) bool
func (l *Library) IsInternal(abs string) bool // media.db{,-wal,-shm,-journal}, media.db.v1-*.bak{,.*.tmp}, import_*.log (root), .picmover/**
func (l *Library) CleanTemp() error
func (l *Library) CopyVerified(ctx, src, dst string, want uint64) (int64, error)
func LayoutPath(fileType string, t time.Time, name string) string // "image/2020/03/a.jpg"
func UniqueName(rel string, taken func(string) bool) string       // a.jpg, a_1.jpg, a_2.jpg ...
func HashFile(p string) (hash uint64, size int64, err error)       // xxhash64
```

## internal/catalog

File: `internal/catalog/catalog.go` (open/migrate), `schema.go`, `media.go`
(types and queries), `migrate_legacy.go`, tests in `catalog_test.go`.

### Opening

```go
const SchemaVersion = 2

type DB struct {
    *sql.DB
    // unexported: path, logger
}

// Open opens or creates the catalog at path and migrates it to SchemaVersion.
// logger may be nil.
func Open(ctx context.Context, path string, logger *slog.Logger) (*DB, error)

// ErrNeedsMigration: the catalog is older than SchemaVersion.
var ErrNeedsMigration error

// OpenExisting opens like Open but never creates or migrates: a missing file
// is an error, an older schema (v1 included) wraps ErrNeedsMigration, a newer
// one is the "newer than supported" error. Used by db, verify, update-metadata.
func OpenExisting(ctx context.Context, path string, logger *slog.Logger) (*DB, error)
```

DSN: the plain OS path followed by
`?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(1)&_txlock=immediate`.
No `file:` prefix (the driver then strips the query and opens the path
verbatim, so Windows paths with spaces work). Driver name `"sqlite"`, import
`_ "modernc.org/sqlite"`.

### Schema v2

Schema version lives in `PRAGMA user_version`. Version 0 with a `media`
table that has a `new_path` column is a legacy v1 database.

```sql
CREATE TABLE IF NOT EXISTS media (
    id                INTEGER PRIMARY KEY,
    hash              INTEGER NOT NULL UNIQUE,   -- xxhash64 stored as int64 (bit cast), same as v1
    size              INTEGER,                   -- bytes; NULL for legacy rows until verify fills it
    rel_path          TEXT NOT NULL,             -- relative to library root, forward slashes
    file_type         TEXT NOT NULL,             -- image | image_raw | video
    ext               TEXT NOT NULL,             -- lowercase, no dot: jpg, cr2, mp4
    taken_at          INTEGER,                   -- wall-clock time encoded as Unix seconds "as if UTC"; NULL unknown
    taken_at_offset   INTEGER,                   -- seconds east of UTC when known, else NULL
    taken_at_source   TEXT NOT NULL DEFAULT 'none', -- exif | video | video_utc | mtime | legacy | none
    width             INTEGER,
    height            INTEGER,
    orientation       INTEGER,                   -- EXIF 1..8
    duration_ms       INTEGER,
    lat               REAL,
    lon               REAL,
    altitude          REAL,
    camera_make       TEXT NOT NULL DEFAULT '',
    camera_model      TEXT NOT NULL DEFAULT '',
    camera_type       TEXT NOT NULL DEFAULT '',  -- phone | camera | unknown | ''
    lens              TEXT NOT NULL DEFAULT '',
    original_path     TEXT NOT NULL DEFAULT '',  -- where it was imported from; "<zip>!/<entry>" for zip entries
    original_mtime    INTEGER,                   -- Unix seconds
    imported_at       INTEGER NOT NULL,          -- Unix seconds
    import_session_id INTEGER,
    rating            INTEGER NOT NULL DEFAULT 0,
    deleted_at        INTEGER                    -- soft delete, Unix seconds
);
CREATE INDEX IF NOT EXISTS media_rel_path ON media(rel_path COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS media_taken_at ON media(taken_at);
CREATE INDEX IF NOT EXISTS media_file_type ON media(file_type);
CREATE INDEX IF NOT EXISTS media_latlon ON media(lat, lon) WHERE lat IS NOT NULL;
CREATE INDEX IF NOT EXISTS media_session ON media(import_session_id);

CREATE TABLE IF NOT EXISTS import_sessions (
    id            INTEGER PRIMARY KEY,
    started_at    INTEGER NOT NULL,
    finished_at   INTEGER,
    source        TEXT NOT NULL,
    mode          TEXT NOT NULL,                 -- copy | move
    imported      INTEGER NOT NULL DEFAULT 0,
    existing      INTEGER NOT NULL DEFAULT 0,
    duplicates    INTEGER NOT NULL DEFAULT 0,
    skipped_small INTEGER NOT NULL DEFAULT 0,
    non_media     INTEGER NOT NULL DEFAULT 0,
    errors        INTEGER NOT NULL DEFAULT 0
);
```

`rel_path` is deliberately *not* UNIQUE: the filesystem is the source of
truth for names, the importer enforces uniqueness, and `verify` reports
duplicates. The NOCASE index serves case-insensitive lookups.

### Time encoding

Photos carry the camera's wall-clock time, usually without a zone. We store
the wall clock, plus the offset when known:

```go
// encode: wall := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC).Unix()
//         offset := nil, or the zone offset of t in seconds when OffsetKnown
// decode: t := time.Unix(wall, 0).UTC(); if offset != nil { t = time.Date(..same fields.., time.FixedZone("", offset)) }
```

So `Media.TakenAt` is always wall-clock; its zone is the real offset when
`OffsetKnown` is true and UTC as a placeholder otherwise. Formatting
`TakenAt` with `Format` always yields the wall-clock.

### Types

```go
type Media struct {
    ID              int64
    Hash            uint64
    Size            int64     // 0 = unknown
    RelPath         string
    FileType        string
    Ext             string
    TakenAt         time.Time // zero = unknown; wall-clock, see above
    OffsetKnown     bool
    TakenAtSource   string
    Width, Height   int       // 0 = unknown
    Orientation     int       // 0 = unknown
    DurationMS      int64
    Lat, Lon        float64
    HasGPS          bool
    Altitude        float64
    HasAltitude     bool
    CameraMake, CameraModel, CameraType, Lens string
    OriginalPath    string
    OriginalMTime   time.Time // zero = unknown
    ImportedAt      time.Time
    ImportSessionID int64     // 0 = none
    Rating          int
    DeletedAt       time.Time // zero = not deleted
}

type Summary struct {
    Total, Images, Raw, Videos, WithGPS, Deleted int64
    TotalBytes        int64
    Earliest, Latest  time.Time // zero when no dated media
    UniqueModels      int64     // distinct non-empty camera_model
}

type ListOptions struct {
    FileType       string // "" = all
    Limit          int    // 0 = no limit
    Offset         int
    IncludeDeleted bool
    OrderBy        string // "" = "taken_at DESC, id DESC"; only accept known values: "taken_at", "taken_at DESC", "id", "id DESC", "rel_path"
}

type SessionStats struct {
    Imported, Existing, Duplicates, SkippedSmall, NonMedia, Errors int64
}
```

### Methods

```go
// InsertMedia inserts m inside tx with ON CONFLICT(hash) DO NOTHING.
// Returns inserted=false when the hash already existed. Sets m.ID on success.
// Empty TakenAtSource is stored as "none".
func (d *DB) InsertMedia(ctx context.Context, tx *sql.Tx, m *Media) (inserted bool, err error)

// LookupHash returns the rel_path of the media with this hash.
func (d *DB) LookupHash(ctx context.Context, hash uint64) (relPath string, found bool, err error)

// RelPathTaken reports whether any row has this rel_path, compared case-insensitively.
func (d *DB) RelPathTaken(ctx context.Context, rel string) (bool, error)

// GetMedia returns one row by id (sql.ErrNoRows when missing).
func (d *DB) GetMedia(ctx context.Context, id int64) (Media, error)

// List returns rows matching opts.
func (d *DB) List(ctx context.Context, opts ListOptions) ([]Media, error)

// UpdateMetadata rewrites the metadata columns of row id from m: taken_at,
// taken_at_offset, taken_at_source, width, height, orientation, duration_ms,
// lat, lon, altitude, camera_make, camera_model, camera_type, lens.
// tx may be nil to run outside a transaction.
func (d *DB) UpdateMetadata(ctx context.Context, tx *sql.Tx, id int64, m *Media) error

// UpdateSize sets size for row id. tx may be nil.
func (d *DB) UpdateSize(ctx context.Context, tx *sql.Tx, id, size int64) error

func (d *DB) Summary(ctx context.Context) (Summary, error)

func (d *DB) BeginSession(ctx context.Context, source, mode string) (int64, error)
func (d *DB) FinishSession(ctx context.Context, id int64, s SessionStats) error
```

`Summary` must not fail on an empty database (NULL aggregates). `WithGPS`
counts rows with `lat IS NOT NULL`. Counts exclude soft-deleted rows except
`Deleted`.

### Migration from v1

The v1 schema (from the old `cmd/import.go`):

```sql
CREATE TABLE media (
    id INTEGER PRIMARY KEY AUTOINCREMENT, hash INTEGER UNIQUE, original_path TEXT,
    new_path TEXT, date_taken DATETIME, file_type TEXT, location TEXT,
    camera_model TEXT, camera_make TEXT, camera_type TEXT, resolution TEXT)
```

`date_taken` was written by mattn/go-sqlite3 as text
`"2006-01-02 15:04:05.999999999-07:00"`; images were parsed as UTC so they
carry `+00:00`; a zero time appears as `0001-01-01 ...`. The modernc driver
may hand a DATETIME column back as `time.Time` or as `string`/`[]byte`, so
scan into `any` and handle both. `location` is `"lat,lon"` for images and an
ISO6709 string (possibly with a trailing `/`) for videos; use
`internal/geo`. `resolution` is `"WxH"` or `"unknown"`. `new_path` is an
absolute path in whatever OS wrote it (`D:\images\image\2020\01\a.jpg` or
`/mnt/d/images/image/2020/01/a.jpg`).

Procedure in `Open` when a legacy DB is detected:

1. `VACUUM INTO` (bound parameter) a unique temp file next to the DB, then
   move it to `'<dbdir>/media.db.v1-<YYYYMMDD-HHMMSS>.bak'` without
   overwriting an existing backup. Fail if the backup cannot be written; on
   failure the temp file is removed, so no partial `.bak` is left.
2. In one (IMMEDIATE) transaction: first re-check that the DB is still legacy
   (`user_version` 0 and `media.new_path` exists); if another process
   migrated it meanwhile, roll back, delete this process's backup and carry
   on. Then `ALTER TABLE media RENAME TO media_v1`; create the v2
   schema; read **all** v1 rows into memory first (close the cursor), then
   insert transformed rows; `DROP TABLE media_v1`; `PRAGMA user_version = 2`.
3. Transform per row:
   - `hash`: copy the int64 as-is.
   - `rel_path`: convert `new_path` with backslashes normalised to `/`. If it
     starts with the library root (the directory containing the DB, compared
     case-insensitively) strip it. Otherwise take the last four path
     segments when the fourth-from-last is one of `image`, `image_raw`,
     `video` (the v1 layout was always `<root>/<type>/YYYY/MM/<name>`).
     Otherwise keep the full normalised path and log a warning.
   - `taken_at`: parse `date_taken`; take its wall-clock fields. The offset
     depends on what v1 stored: a `video` at `+00:00` was a true UTC instant
     from ffprobe -> offset 0, source `legacy_utc`; any type at a non-zero
     offset was the file mtime in local time -> keep that offset, source
     `legacy`; an `image`/`image_raw` at `+00:00` (EXIF without zone, the
     `+00:00` is a placeholder) or a value without any zone -> offset NULL,
     source `legacy`. Zero/unparseable -> NULL and source `none`.
   - A `location` of exactly `0,0` (Null Island) counts as no location.
   - `lat/lon/altitude` from `location`; `width/height` from `resolution`.
   - `ext` from the file name; `file_type` as-is; `original_path` as-is;
     `imported_at = now`; `size`, `orientation`, `duration_ms`, `lens` NULL/empty.
4. Log the number of rows migrated and the number with unresolved paths.

A later `migrations` table/map keyed by from-version handles 2 -> 3 etc.
Opening a DB whose `user_version` is greater than `SchemaVersion` must fail
with a clear error.

### Tests (catalog_test.go)

- Fresh open creates v2 and `user_version == 2`; reopening is a no-op.
- Insert, LookupHash, conflict returns inserted=false, GetMedia round-trips
  every field including TakenAt with and without OffsetKnown, GPS, zero
  times.
- RelPathTaken is case-insensitive.
- List with FileType, Limit, OrderBy; IncludeDeleted.
- Summary on empty DB and on populated DB.
- Sessions begin/finish.
- Legacy migration: build a v1 DB with raw SQL (the exact DDL above) and
  rows covering: Windows path, WSL path under a different root, path under
  the actual root, image with `+00:00` date and "lat,lon", video with
  ISO6709 location, "unknown" resolution, zero date, a NULL location. Assert
  rel_path, taken_at wall-clock, lat/lon, width/height, the backup file
  exists, user_version == 2, and that `media_v1` is gone.
- A path with spaces in the directory name opens fine.

## internal/metadata

Files: `metadata.go` (types, Classify, Extractor, Default.Extract),
`exif.go`, `video.go`, `camera.go`, `tools.go`, tests.

```go
type Kind string
const (
    KindImage Kind = "image"
    KindRaw   Kind = "image_raw"
    KindVideo Kind = "video"
)

// Classify maps a file name to its media kind by extension (case-insensitive).
// Lists are exactly the v1 lists:
//  image:     .jpg .jpeg .png .tiff .tif
//  image_raw: .cr2 .crw .cr3 .dng .nef .arw .x3f .orf .rw2 .raf .pef .srw .raw
//  video:     .mp4 .mpg .mpeg .mov .avi .mkv .flv .3gp .wmv .mts .m2ts .ts .webm .asf .m4v
func Classify(name string) (Kind, bool)

type Info struct {
    TakenAt       time.Time // wall-clock; zone = real offset when OffsetKnown, else UTC placeholder
    OffsetKnown   bool
    Source        string    // exif | video | video_utc | mtime | none
    Width, Height int
    Orientation   int
    DurationMS    int64
    Lat, Lon      float64
    HasGPS        bool
    Altitude      float64
    HasAltitude   bool
    CameraMake, CameraModel, CameraType, Lens string
}

// Extractor reads metadata. warnings are non-fatal problems the caller logs.
// err is returned only when the file cannot be read at all.
type Extractor interface {
    Extract(ctx context.Context, path string, kind Kind) (info Info, warnings []string, err error)
}

// Default is the built-in extractor.
type Default struct {
    FFProbe     string         // path to ffprobe; "" = FindTool("ffprobe")
    DefaultZone *time.Location // nil = time.Local. Used to turn instants (video UTC, mtime) into wall-clock.
}

// FindTool looks for an external tool next to the running executable first
// (with .exe on Windows; elsewhere it must have an execute bit), then on
// PATH. Results are cached.
func FindTool(name string) (string, error)

// ClassifyCamera returns "phone", "camera" or "unknown".
func ClassifyCamera(make, model string) string
```

### Default.Extract behaviour

Common: `os.Stat` failure -> err. After kind-specific extraction, if
`TakenAt` is zero: `TakenAt = mtime.In(zone)`, `OffsetKnown = true`,
`Source = "mtime"`. If make and model are both empty, `CameraType = ""`;
otherwise `ClassifyCamera`.

Images and raw (`exif.go`) use `internal/exif`, a small EXIF reader written
for hostile input (it replaced goexif, which could loop forever on an IFD
cycle, allocate gigabytes from a 644-byte file, read whole raws into memory,
drop all EXIF on a bad maker note and pick an XMP APP1 over the Exif one).
`exif.Read(r io.ReaderAt, size int64) (*exif.Fields, error)` detects JPEG,
TIFF-based files (TIFF, CR2, NEF, ARW, DNG, PEF, SRW, ORF, RW2), CR3 and RAF
by magic bytes and returns only the fields picmover uses. Limits: every read
is range-checked against the file size; at most 32 IFDs per file, 512
entries per IFD, nesting depth 4, a visited set against cycles; an entry
value (count*typeSize, computed in 64 bits) must be <= 1 MiB and inside the
file, ASCII values are read up to 1 KiB, only tags it uses are read at all;
CR3 walks at most 64 ISO BMFF boxes. Maker notes, XMP, IPTC, Interop IFDs
and thumbnails are ignored. Malformed parts become warnings; an error means
no EXIF structure was found.

- Date: `DateTimeOriginal`, then `DateTimeDigitized`, then `DateTime`, each
  paired with its own `OffsetTimeOriginal`/`OffsetTimeDigitized`/`OffsetTime`.
  Layouts `2006:01:02 15:04:05`, `2006:01:02 15:04:05-07:00`,
  `2006:01:02 15:04:05Z07:00`, `2006-01-02 15:04:05`, `2006-01-02T15:04:05`,
  `2006-01-02T15:04:05Z07:00`; years before 1900 are rejected. A zone in the
  value or a paired offset that parses as `±HH:MM` gives `OffsetKnown = true`
  (the time is built in that fixed zone). Blank placeholders (cameras write
  `"    :  :     :  :  "`) are skipped silently; a present but unparsable
  date is a warning and the next tag is tried. Source `exif`.
- Dimensions: for `KindImage` try `image.DecodeConfig` first (register
  `image/jpeg` and `image/png` only), then EXIF `PixelXDimension`/
  `PixelYDimension`, then `ImageWidth`/`ImageLength`. For `KindRaw` skip
  DecodeConfig and use only `ImageWidth`/`ImageLength` of the full-resolution
  image IFD: the reader walks IFD0, its SubIFDs (0x014A) and the IFD chain
  and prefers the IFD with `NewSubfileType == 0`, else the largest IFD not
  marked reduced-resolution. DNG and NEF keep a small preview in IFD0 and the
  main image in a SubIFD, so IFD0's size is never used when it is a preview;
  the size is 0 (unknown) when no full-resolution IFD is found.
- `Orientation` only when 1..8; `Make`, `Model`, `LensModel` trimmed of
  spaces and NULs.
- GPS from the GPS IFD (three rationals each, refs N/S/E/W case-insensitive,
  zero denominators skipped with a warning, (0,0) treated as absent);
  altitude from `GPSAltitude` with `GPSAltitudeRef == 1` meaning below sea
  level. Validate with `geo.Valid`.
- Missing EXIF is one warning, not an error. Missing individual tags are
  not warnings (they are normal); malformed ones are.

Video (`video.go`), using ffprobe
`-v quiet -print_format json -show_format -show_streams <path>`:

- If ffprobe cannot be found or fails: one warning, leave fields unknown
  (the mtime fallback then applies). Never an error.
- Parse `streams[].codec_type == "video"` for width/height; if
  `tags.rotate` or `side_data_list[].rotation` is ±90/270, swap width and
  height so they are display dimensions. Use `map[string]string` for tags
  and look keys up case-insensitively.
- Date, first match wins:
  1. `format.tags["com.apple.quicktime.creationdate"]` — local time with
     offset, e.g. `2020-01-05T12:34:56+0200` (layouts
     `2006-01-02T15:04:05-0700`, RFC3339). `OffsetKnown = true`, `Source = "video"`.
  2. `format.tags["creation_time"]` (then the video stream's) — a UTC
     instant like `2020-01-05T10:34:56.000000Z`. Convert with
     `t.In(zone)`, `OffsetKnown = true`, `Source = "video_utc"`.
- Dates before 1980 (1904/1970 epochs from devices without a clock) or more
  than 24 h in the future are rejected with a warning and the next
  candidate is tried.
- Duration from `format.duration` (seconds, string, capped at 1e9 s) -> ms.
- Streams with `disposition.attached_pic == 1` (cover art) are not the video
  stream. Each ffprobe run has a 60 s timeout.
- Location: `com.apple.quicktime.location.ISO6709`, `location`,
  `location-eng` via `geo.ParseISO6709`.
- Camera: `com.apple.quicktime.make`/`model` -> make/model (type via
  ClassifyCamera, so an iPhone is a phone — the old code wrongly said
  camera); `com.android.version` -> make "Android", model
  "Android <ver>", type phone; `software` starting with "Canon" -> make
  Canon, model = software.
- Keep the JSON parsing in a function `parseFFProbe(data []byte, zone
  *time.Location) (Info, []string)` that tests can call with canned JSON.

Camera classifier (`camera.go`): fix the v1 bug where "sony" appeared in
both lists so Sony Alpha cameras were "phone". Order of checks:

1. Model hints for phones (lowercased `Contains`): iphone, galaxy, pixel,
   xperia, redmi, poco, "mi ", nexus, zenfone, smartphone, phone, lumia, and
   prefixes `sm-`, `lg-`, `xq-`, `lm-`, `moto `, `so-`, `asus_`; also the
   regexes `^(SM-|LG-|XT\d{4}|FRD-|LE\d{4}|AC\d{4})` and `^[EFGHJ]\d{4}$`
   (Sony Xperia numeric models such as G8341).
2. Model hints for cameras: ilce, dsc-, "eos", "nikon d", x-t, x-e, x-pro,
   gfx, dmc-, dc-, e-m, om-, "nx" (as a prefix), pen-.
3. Make lists: phones = apple, samsung, huawei, xiaomi, oppo, vivo, oneplus,
   motorola, nokia, htc, google, lenovo, alcatel, zte, blackberry, meizu,
   realme, hmd global, honor, lg electronics, lge, tecno, infinix, fairphone,
   nothing. cameras = canon, nikon, sony, fujifilm, olympus, om digital,
   panasonic, leica, hasselblad, pentax, ricoh, kodak, gopro, dji, sigma,
   phase one, minolta, konica, casio.
4. Otherwise "unknown".

### Tests

- Classify table test including uppercase extensions and no extension.
- EXIF round trip using `testimg.JPEG(testimg.Options{...})`: date (wall
  clock, OffsetKnown false), make/model/lens, orientation, GPS lat/lon and
  altitude, width/height from DecodeConfig; a `NoExif` JPEG yields one
  warning, dimensions from DecodeConfig and `Source == "mtime"`; a PNG works;
  a truncated/garbage file with a `.jpg` name yields warnings, not an error;
  a file that does not exist yields an error.
- ClassifyCamera table: Sony ILCE-7M3 -> camera, Sony XQ-BC72 -> phone,
  Apple iPhone 12 -> phone, Samsung SM-G991B -> phone, Samsung NX1 ->
  camera, Canon EOS R5 -> camera, "" "" -> unknown, HUAWEI ELE-L29 -> phone.
- parseFFProbe with canned JSON for: Apple creationdate with offset, Android
  creation_time UTC converted into a fixed test zone, rotation swap,
  duration, ISO6709 location, each camera branch.
- A real-ffprobe test that is skipped with `t.Skip` when `FindTool`
  fails. Also a test that `Default{FFProbe: "/nonexistent"}` on a `.mp4` of
  garbage bytes returns a warning and mtime fallback, not an error.

## internal/importer

Files: `importer.go`, `scan.go`, `zip.go`, `xdev_unix.go`,
`xdev_windows.go`, tests.

```go
type Status string
const (
    StatusImported     Status = "imported"          // copied or moved into the library and recorded
    StatusExisting     Status = "imported_existing" // already at its destination (or identical file there); recorded
    StatusDuplicate    Status = "duplicate"         // hash already in catalog or in this import; not touched
    StatusSkippedSmall Status = "skipped_small"
    StatusNonMedia     Status = "non_media"
    StatusError        Status = "error"
)

type Result struct {
    Status  Status
    Source  string // original path; "<zip>!/<entry>" for zip entries
    Dest    string // catalog rel path when applicable
    Message string
    Err     error
}

type Stats struct {
    Scanned, Imported, Existing, Duplicates, SkippedSmall, NonMedia, Errors int64
    BytesCopied int64
}

type Options struct {
    Source           string
    Move             bool
    MinDimension     int
    Workers          int                // <=0: min(4, runtime.NumCPU())
    Extractor        metadata.Extractor // nil: &metadata.Default{}
    Logger           *slog.Logger       // nil: discard
    OnResult         func(Result)       // optional, called from pipeline goroutines
    OnProgress       func(Stats)        // optional, called every ProgressInterval and once at the end
    ProgressInterval time.Duration      // <=0: 200ms
    ForceCopy        bool               // testing: never rename when moving; always copy+verify+delete
}

func New(lib *library.Library, db *catalog.DB, opts Options) *Importer
func (im *Importer) Run(ctx context.Context) (Stats, error)
```

`Run` returns the final stats always, plus `ctx.Err()` if cancelled, or any
fatal error (scanner could not stat the source, catalog writer failed).
Per-file problems are results, not errors.

### Pipeline

```
scanner ──jobs──▶ N workers ──pending──▶ writer (1) ──▶ results
```

**Scanner** (`scan.go`): `filepath.WalkDir` over `Options.Source`
(a single file or zip is also accepted). Rules:

- A directory read error is reported as a `StatusError` result for that
  path and the walk continues (v1 aborted the whole import).
- Skip directories and files for which `lib.IsInternal` is true, so a
  library can be imported onto itself (the user's "rebuild the DB" trick:
  `import LIB LIB --move`).
- Skip junk: `.DS_Store`, `Thumbs.db`, `desktop.ini`, `.picasa.ini`, names
  starting with `._`.
- Regular files and symlinks only.
- `.zip` -> a zip job. Other non-media -> `StatusNonMedia` result (counted;
  v1 never counted these outside zips). Media -> file job.
- `Scanned` counts media and non-media files, and zip *entries*, not the
  zip file itself.
- Sending on the jobs channel selects on `ctx.Done()`.

**Worker** `process(ctx, path, origin string, fromZip bool)`:

1. `metadata.Classify` -> non-media result if unknown.
2. `os.Stat`; `library.HashFile` -> error result on failure.
3. `db.LookupHash` -> `StatusDuplicate` with `Dest` = existing rel path.
4. In-flight set (`sync.Map` keyed by hash, `LoadOrStore`): if present ->
   `StatusDuplicate` "duplicate of another file in this import". Release
   the entry on every non-imported outcome; the writer releases it after
   commit.
5. `Extractor.Extract`; log each warning at Warn with `file=origin`; error
   -> error result.
6. Min dimension: only for `KindImage`, only when both width and height are
   known and both are below the limit -> `StatusSkippedSmall`. Unknown
   dimensions are allowed through (v1 made them an error).
7. `takenAt := info.TakenAt`, falling back to mtime if zero.
   `rel := library.LayoutPath(string(kind), takenAt, filepath.Base(path))`.
8. In place? `inPlace := !fromZip && os.SameFile(statOfSource, statOfDest)`
   when the destination exists. If in place nothing is copied.
9. Otherwise `reserveDest(rel, hash)` under a mutex:
   - If a file exists at `rel` and `HashFile` of it equals `hash` ->
     `identical = true`, reuse `rel` (copy nothing; the row is new because
     step 3 found no row).
   - Else `library.UniqueName(rel, taken)` where `taken` is true if the
     lower-cased rel is in the in-memory claimed set, or exists on disk
     (a per-directory name cache read once with `os.ReadDir`, folded with `strings.ToLower`), or `db.RelPathTaken`; any other error aborts the file. Add the result to the claimed set.
     The claimed set is released after commit or on failure.
10. File operation, unless in place or identical:
    - `Move && !fromZip && !ForceCopy`: `os.MkdirAll` then `os.Rename`. On
      success `moved = true`. If the error is cross-device (`EXDEV` on unix,
      `windows.ERROR_NOT_SAME_DEVICE` from `golang.org/x/sys/windows` on
      Windows; put each in a build-tagged file) fall through to copy.
      Other errors -> error result.
    - Copy: `lib.CopyVerified(ctx, path, dst, hash)`; `copied = true`; add
      bytes to `BytesCopied`. Error -> error result (release claims).
11. Build `catalog.Media` (hash, size, rel, kind, ext, taken_at fields,
    dims, GPS, camera, `OriginalPath = origin`, `OriginalMTime` = source
    mtime, `ImportedAt = now`, `ImportSessionID`) and send a `pending` to the
    writer with: media, status (`Existing` if in place or identical, else
    `Imported`), source path, origin, hash, moved, copied, and
    `deleteSrc = Move && !fromZip && !inPlace && !moved`.

After `ctx` is cancelled, workers drain the jobs channel without processing
(check `ctx.Err()` at the top of `process`); files whose copy already
finished are still sent to the writer and committed, so nothing on disk is
left unrecorded.

**Writer**: single goroutine. Batches up to 256 pending items or a 500 ms
tick, whichever first. Per batch, with `context.Background()` so
cancellation does not abort a commit of completed copies:

1. `BeginTx`; `InsertMedia` for each; `Commit`.
2. For each item after commit: release in-flight hash and claimed path. If
   `inserted == false` (lost a race on hash): undo the file operation
   (`copied` -> remove dst; `moved` -> rename dst back to src) and report
   `StatusDuplicate`. Else if `deleteSrc`: `os.Remove(src)`; a failure is
   logged and appended to the result message, the status stays
   `Imported`. Report the result.
3. If `BeginTx`/`Insert`/`Commit` fails: roll back, undo every file
   operation in the batch, report each as `StatusError`, remember the error
   as fatal, keep draining (reporting errors) until the channel closes, and
   return the fatal error from `Run`.

**Zip** (`zip.go`): a worker handles a zip job by opening it with
`archive/zip`, creating a temp dir under `lib.TempDir()`, and for each
non-directory, non-junk entry: count scanned; non-media -> result;
otherwise extract to `<tmpdir>/<basename>`, set its mtime to the entry's
`Modified`, call `process(ctx, tmp, zipPath+"!/"+entry.Name, true)`, send
or report, then remove the temp file. Zip entries are never moved and the
zip itself is never deleted, even with `--move`. A zip that cannot be
opened is one error result.

**Reporting** `report(Result)`: increments the matching counter, logs
(Info for imported/existing/duplicate/skipped, Debug for non-media, Error
for errors; include `source`, `dest`, `message`), and calls `OnResult`.

**Progress**: a goroutine calls `OnProgress(snapshot)` every
`ProgressInterval`; `Run` calls it once more with the final stats. Counters
are `atomic.Int64`.

**Sessions**: `Run` calls `db.BeginSession` at the start and
`db.FinishSession` at the end (with `context.Background()`), and logs start
and end with `source`, `library`, `mode`, `workers` and the final counts.
`Run` calls `lib.CleanTemp()` first.

### Tests (importer_test.go)

Use `testimg` fixtures, `t.TempDir()`, `Workers: 4` unless noted, and a
`metadata.Default{DefaultZone: time.UTC}` extractor (ffprobe is not needed
because there are no video fixtures). Assert sets, not orders, where
workers race.

- Basic copy import: `a.jpg` (EXIF 2020-03-05, Apple/iPhone 12, GPS),
  `b.png` (no EXIF, mtime set to 2019-12-31T23:30 UTC), `sub/c.jpg` (EXIF
  2021-07-01), `notes.txt`, `.DS_Store`. Expect Imported 3, NonMedia 1,
  Scanned 4; files at `image/2020/03/a.jpg`, `image/2019/12/b.png`,
  `image/2021/07/c.jpg`; sources untouched; catalog rows with camera type
  phone, GPS, `taken_at_source` exif/mtime, size, ext; one import session
  with matching counts. Re-running gives Duplicates 3, Imported 0, and the
  `Dest` of each duplicate result is the existing rel path.
- Same name, different content, same month -> `a.jpg` and `a_1.jpg` both
  present and both in the catalog.
- Same content under two names -> Imported 1, Duplicates 1 (run with
  `Workers: 1` and again with `Workers: 4`).
- `MinDimension: 100` with a 16x16 and a 400x400 image -> SkippedSmall 1,
  Imported 1.
- Move on the same volume: sources gone, destinations present, catalog rows
  present. Same again with `ForceCopy: true` (exercises copy+verify+delete).
- Zip: an archive with `a.jpg` and `notes.txt` -> Imported 1, NonMedia 1,
  `OriginalPath == "<zip>!/a.jpg"`, zip still present with `Move: true`,
  temp dir empty afterwards.
- Library onto itself with `Move: true`: place one file already at its
  correct layout path and one at `old/2012/x.jpg`; put a `media.db-wal`-like
  file and a `.picmover/tmp/stale.part` in the root. Expect Existing 1,
  Imported 1 (the old-layout file moved into `image/YYYY/MM/`), the
  internal files untouched, Scanned 2.
- Identical file already at the destination but not in the catalog ->
  Existing 1, row added, source kept (copy mode) or removed (move mode).
- Cancellation: 40 small files, cancel the context from `OnResult` after
  the first result. `Run` returns `context.Canceled`; every catalog row's
  file exists; no `.part` files remain in the temp dir; `Imported +
  Duplicates + ...` is consistent with the number of files on disk under
  `image/`.
- Unreadable directory (skip on Windows and when running as root): one
  `StatusError` result, other files imported.
- A source that does not exist -> `Run` returns an error.

## cmd

Rewrite `cmd/` on top of the packages. Keep `main.go` as is. Root command:
proper `Short`/`Long`, `SilenceUsage: true`, `Execute()` uses
`ExecuteContext(context.Background())`. Remove the Cobra placeholder text and
the unused `toggle` flag.

`import <source> <library>` flags: `--move`, `--min-dimension N`,
`--workers N`, `--timezone IANA` (default system zone; import `time/tzdata`
so names resolve on Windows), `--quiet`. Behaviour: open library and
catalog; log to `<lib>/.picmover/logs/import_<timestamp>.log` with
`slog.NewTextHandler`; `signal.NotifyContext` for Ctrl+C/SIGTERM; print a
live status line (`\r\033[2K` + counters, truncated to the terminal width) when stdout is a terminal and not
`--quiet`; print the summary and the log path at the end; on
`context.Canceled` print "Import cancelled." and the partial summary and exit
130; when `Stats.Errors > 0` print the summary and exit 2; other errors exit 1.

`db <library>` flags: `--list/-l`, `--limit/-n` (default 10), `--type/-t`.
Prints the Summary (correctly: files with GPS, date range, counts by type,
total size human-readable) and the most recent files; `--list` prints rows
as a table with id, hash (hex), rel path, taken_at (wall-clock, with offset
when known), type, WxH, lat/lon, camera.

`update-metadata <library>` flags: `--type`, `--dry-run`, `--timezone`.
Loads all rows with `List`, re-extracts with `metadata.Default`, prints
per-file changes, applies updates in transactions of 500 rows. Must not
crash on warnings (v1 crashed on a nil logger) and must not write while
iterating a cursor.

`verify <library>` flags: `--rehash`, `--fill-size`. For every row: missing
file, size mismatch (when size known), optional hash mismatch; with
`--fill-size`, fill NULL sizes. Then walk `image/`, `image_raw/`, `video/`
under the root and list files that are not in the catalog (orphans) and
catalog rows sharing a rel path case-insensitively. Print counts and the
first 50 of each list; exit 1 when any problem was found.

Makefile: `build`, `test`, `vet`, `build-all` producing
`dist/picmover-linux-amd64`, `dist/picmover-windows-amd64.exe`,
`dist/picmover-darwin-arm64`, `dist/picmover-darwin-amd64` with
`CGO_ENABLED=0 -trimpath -ldflags '-s -w'`; keep the author's `install`
target copying to `/home/alfthan/bin/picmover` and
`/mnt/c/Users/galft/bin/picmover-win64.exe`. `.gitignore`: `dist/`,
`/picmover`, `*.exe`, `*.db`, `*.db-*`, `.picmover/`.

## Implementation notes (as merged)

- importer: when the source is the library root, inside it, or contains it, the scanner lists the whole tree before dispatching jobs so files written during the import are not re-scanned.
- importer: a file already inside the library in its correct type/YYYY/MM directory under any name is treated as in place and not renamed.
- importer: symlinked sources are always copied, never renamed; with --move only the link is removed after commit.
- importer: files abandoned because of cancellation produce no result and are not counted.
- importer: Run cleans .picmover/tmp before starting, so stale temp files never survive.
- catalog: InsertMedia and UpdateMetadata accept a nil tx; zero numeric values are stored as NULL; LookupHash and RelPathTaken also see soft-deleted rows; the v1 backup is media.db.v1-<timestamp>.bak with a -N suffix if it exists.
- metadata: ClassifyCamera runs only when the extractor did not already set a type; KindImage files with no determinable dimensions get a warning; FindTool results are cached for the process lifetime.
- cmd: openExisting refuses to create a library for db/update-metadata/verify; import logs are opened in append mode; a cancelled import prints the partial summary and exits 130 (exit 2 when any file failed).
- cmd (CLI review): exit codes come from a typed `exitError`; positional arguments must not be empty or blank; import stats the source before creating anything and announces a new library. db/verify/update-metadata use `library.Attach` + `catalog.OpenExisting` and never create, migrate or write an old catalog (`catalog.ErrNeedsMigration`); the new `migrate <library>` command upgrades it. update-metadata refuses videos without ffprobe, never replaces a metadata-derived date by an mtime/none fallback ("skipped (extraction degraded)"), never replaces a known field with an empty one, and without `--timezone` re-extracts mtime/video_utc/legacy_utc rows in their stored offset. verify reports case conflicts (files matching a row only case-insensitively) and fills sizes in transactions of 500, skipping hash mismatches. The status line is truncated to the terminal width; a second Ctrl+C kills the process.
- catalog: `OpenExisting(ctx, path, logger)` opens without creating or migrating; `UpdateSize(ctx, tx, id, size)` takes an optional transaction.
- importer: Workers is clamped to 64; a file whose metadata extraction ended because of cancellation is abandoned, not placed by mtime.

## Known limitations and follow-ups (from the adversarial review)

Found by a targeted review of the import pipeline and deliberately left for
later steps. None of them loses data during a normal single run.

- **Concurrent imports into one library are unsupported.** There is no lock
  file; a second run's `CleanTemp` would delete the first run's in-progress
  files, and the never-overwrite guarantee between *processes* rests on an
  `Lstat` check followed by `os.Rename`, which replaces an existing file on
  every OS. Follow-up: a lock file under `.picmover/` and a rename that
  refuses to replace (`RENAME_NOREPLACE` on Linux, `MoveFileEx` without
  `REPLACE_EXISTING` on Windows).
- **Case folding in the catalog is ASCII only.** `COLLATE NOCASE` does not
  fold non-ASCII letters, so `Ä.jpg` and `ä.jpg` can both be catalogued on
  Linux; the on-disk check folds with `strings.ToLower` and does catch it when
  the first file is on disk. Follow-up: a Go-folded key column.
- **Unicode normalisation and Windows-invalid names are not handled.** NFC
  and NFD spellings of one name get different claim keys; names containing
  `\ : ? * " < > |` imported on Linux are invalid on Windows. Follow-up:
  normalise to NFC and sanitise or reject such names at import.
- **Durability on non-journaled filesystems.** Newly created parent
  directories are not fsynced (only the leaf directory on unix), and the
  Windows rename is not forced to disk. Catalog commits are `synchronous=FULL`
  and happen before move sources are deleted, which covers NTFS, ext4, APFS
  in practice; exFAT/FAT library drives remain exposed to a power cut.
- **Overlap detection compares path strings.** A source that reaches the
  library through a symlink, an alias or a differently-cased path is not
  recognised as overlapping; the consequence is spurious duplicates or
  errors, not loss.
- **Identical-file reuse trusts xxhash64.** When a move source is identical
  to a file already at its destination, the source is deleted after commit
  on hash equality alone. A byte compare before deletion would remove even
  the theoretical collision risk.
- **Stale rows after in-place edits.** If a catalogued file is edited in
  place and then imported again, the edited file is recorded under a new
  name and the old row keeps its stale hash; `verify --rehash` reports it.
  A reconcile command that updates rows is a later step.
