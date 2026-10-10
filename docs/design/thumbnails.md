# Step 2: thumbnails, previews, modern formats, clean schema

Branch `feat/thumbnails`. This document is the contract for the packages
below; read it fully before writing code. Step 1's contract is
`core-restructure.md`; everything there still holds unless changed here.

## Policy change: no backwards compatibility with old catalogs

There is one user and one library. The catalog is derived entirely from
the files, so the upgrade path for any schema change is a rebuild, not a
migration:

- `PRAGMA user_version` is checked on open. A catalog with a different
  version is refused with: "catalog format changed; run `picmover rebuild
  <library>`". There is no migration code. The v1 migration from step 1,
  the `migrate` command and the `legacy`/`legacy_utc` sources are removed.
- `picmover rebuild <library>` renames `media.db` (and its `-wal`/`-shm`/
  `-journal`) to `media.db.bak-<timestamp>`, then imports the library onto
  itself in record-only mode (`importer.Options.RecordInPlace`): every
  regular, non-internal media file under the root is recorded at its
  current path, whatever folder it is in, and nothing is copied, moved or
  deleted (identical copies are reported as duplicates and left in place).
  It then runs the thumbnail stage. Thumbnails are keyed by content hash in
  a separate cache file, so a rebuild reuses them. `picmover import <lib>
  <lib>` is different: it copies (or with `--move` moves) files that are
  outside the layout into it.
- Design the schema for the viewer, maps, backups and AI that come later;
  never for the old format.

## Goals

1. Fast grid and viewer later: a 256 px thumbnail and a 1600 px preview per
   file, generated at import and on demand, content-addressed by hash.
2. Modern formats imported and decoded in pure Go: HEIC/HEIF, AVIF, WebP,
   GIF, BMP, TIFF, plus RAW via the embedded JPEG previews every RAW
   carries. Video thumbnails via ffmpeg when it is present.
3. A perceptual hash per file for near-duplicate detection later.
4. `CGO_ENABLED=0` on every platform stays non-negotiable. WebAssembly
   ports (wazero) are pure Go and acceptable.
5. Throughput: decoding is CPU-bound and runs with one worker per CPU; the
   import's copy stage is unchanged. Memory is bounded: images above 100
   megapixels are not decoded (`unsupported`).

## Package layout (new or changed)

```
internal/imaging   decode by sniffing magic bytes, bounded decode, EXIF orientation, resize, JPEG encode
internal/exif      + HEIF/AVIF EXIF extraction, + embedded preview locator for RAW files
internal/thumbs    thumbnail/preview generation and the cache store (thumbs.db + previews/ dir)
internal/catalog   schema v3 (strict), thumb state + phash columns, no migrations
internal/metadata  extension lists extended; HEIC/AVIF/WebP/GIF/BMP dimensions and EXIF
internal/importer  thumbnail stage after the copy stage; progress fields
cmd                thumbs, rebuild; import --no-thumbs; db shows thumb stats; migrate removed
```

## internal/imaging

```go
// Format is the sniffed container format.
type Format string // "jpeg" "png" "gif" "webp" "bmp" "tiff" "heif" "avif" "" (unknown)

// Sniff reads at most 64 bytes from r and reports the format.
func Sniff(r io.ReaderAt) Format

// Config returns dimensions without decoding pixels, when the format allows.
func Config(r io.ReaderAt, size int64) (w, h int, f Format, err error)

// Decode decodes the whole image. It refuses images larger than
// opts.MaxPixels (0 = DefaultMaxPixels, 100e6) before allocating anything.
func Decode(ctx context.Context, r io.ReaderAt, size int64, opts Options) (image.Image, Format, error)
// DecodeFit is Decode for a caller that will Fit to fitMax (jpegli
// reduced-scale decode for large JPEGs).
func DecodeFit(ctx context.Context, r io.ReaderAt, size int64, fitMax int, opts Options) (image.Image, Format, error)

type Known struct { W, H int; Format Format } // an earlier Config result
type Options struct {
    MaxPixels int64            // 0 = DefaultMaxPixels
    Budget    *MemBudget       // nil = no memory limit
    HEIFSlots chan struct{}    // nil = no HEIF concurrency limit
    Known     *Known           // nil = run Config
}
func NewMemBudget(bytes int64) *MemBudget
func (b *MemBudget) Acquire(ctx context.Context, n int64) error
func (b *MemBudget) Release(n int64)

// Orient applies an EXIF orientation (1..8) and returns the upright image.
func Orient(img image.Image, orientation int) image.Image

// Fit returns img scaled so that its longer side is at most max pixels
// (never upscaled). Large reductions use an integer box downsample first,
// then draw.CatmullRom for the final step, so quality is good and time
// stays roughly linear in the source pixel count.
func Fit(img image.Image, max int) image.Image

// EncodeJPEG writes img as a baseline JPEG at the given quality.
func EncodeJPEG(w io.Writer, img image.Image, quality int) error
```

Decoders and pinned versions (already in go.mod; do not edit go.mod):

| Format | Package | Notes |
|---|---|---|
| JPEG | `github.com/gen2brain/jpegli` v0.4.2, fallback `image/jpeg` | `jpegli.DecodeWithOptions(r, &jpegli.DecodingOptions{ScaleTarget: rect})` decodes at a reduced DCT scale. Use it whenever the source is more than 2x the requested size; verify empirically what size it returns and always finish with `Fit`. On any error fall back to the standard decoder. |
| PNG, GIF | standard library | |
| TIFF, BMP | `golang.org/x/image/tiff`, `.../bmp` v0.46.0 | |
| WebP | `golang.org/x/image/webp` v0.46.0 (decode) | `github.com/gen2brain/webp` v0.6.4 is available for *encoding* test fixtures only. |
| HEIF/HEIC | `github.com/gen2brain/heic` v0.7.2 | wazero; `heic.Decode`, `heic.DecodeConfig`. |
| AVIF | `github.com/gen2brain/avif` v0.6.0 | wazero; also has `avif.Encode` for test fixtures. |

All gen2brain packages must be used in WebAssembly mode only: every
`go build`/`go test`/`go vet` in the Makefile and in your checks passes
`-tags nodynamic` so the purego dynamic-library path is never compiled in.
Scaling uses `golang.org/x/image/draw` (`draw.CatmullRom.Scale(dst, dr, src,
sr, draw.Src, nil)` for the final step after an integer box downsample).
`image.RegisterFormat` is not used for the wasm decoders; `Sniff` dispatches
explicitly.

Also in this package:

```go
// PHash returns the 64-bit DCT perceptual hash of img (32x32 grayscale,
// 8x8 low-frequency DCT, median threshold). Implemented here; no
// dependency.
func PHash(img image.Image) uint64
// Distance is the Hamming distance between two hashes.
func Distance(a, b uint64) int
```

Benchmarks (`go test -bench`) must exist for: decoding a synthetic 24 MP
JPEG with the standard decoder versus jpegli at 1/4 and 1/8 scale, and
`Fit` from 24 MP to 1600. Report the numbers.

As implemented (hardening after review):

- **HEIF image sequences are refused** (`ErrUnsupported`, "HEIF image
  sequence") by `Config`, `Decode` and `DecodeFit` before any
  gen2brain/heic call, when a bounded top-level box walk
  (`exif.HasBox(r, size, "moov")`: at most 64 boxes, sizes validated)
  finds a `moov` box. gen2brain/heic v0.7.2 parses `moov` itself and
  allocates slices sized by 32-bit sample counts from the file (a
  160-byte file could request ~48 GiB: a fatal out-of-memory, not a
  panic), decodes every frame to return the first, and reports a size the
  decoder does not use. More than 64 top-level boxes is refused too.
- **Size checks.** Before decoding: the declared size against
  `MaxPixels`, and plausibility against the file size (JPEG: more than
  1024 pixels per byte, as jpegli checks; PNG: more than 4096) gives
  `ErrTooLarge`, so `image/jpeg`/`image/png` never allocate for a header
  that lies. After decoding: a decoded image larger than `MaxPixels` is
  dropped with `ErrTooLarge` (HEIF grids decode to the tile-derived size
  whatever `ispe` says).
- **Memory budget and HEIF slots.** Every decode holds declared
  `w*h*4` bytes (x2 for HEIF/AVIF, whose wasm decoders keep a second copy)
  of `Options.Budget` while it runs; a request larger than the whole
  budget proceeds when nothing else is held. HEIF decodes also hold one
  slot of `Options.HEIFSlots`. Both waits end with the context.
- **`Options.Known`** lets a caller that already ran `Config` skip the
  second sniff and header parse (for HEIF, a second wasm instantiation).
  The sequence check, pixel budget and plausibility checks still run.
- **jpegli path.** A jpegli `ErrTooLarge` is final (no fallback to
  `image/jpeg`). A CMYK result (`*image.CMYK`) is discarded and the file
  decoded by `image/jpeg`, which applies the Adobe APP14 inversion that
  jpegli does not. Other jpegli failures still fall back.

## internal/exif additions

```go
// PreviewRef locates an embedded JPEG inside a RAW or HEIF container.
type PreviewRef struct {
    Offset, Length int64
    Width, Height  int // 0 when unknown
}

// Previews returns embedded JPEG previews, largest first. It never reads
// the preview bytes themselves; it only validates that the range lies in
// the file and (when cheap) that it starts with FF D8.
func Previews(r io.ReaderAt, size int64) ([]PreviewRef, error)
```

Sources, in the same bounded style as `Read`:

- TIFF-based (CR2, NEF, ARW, DNG, PEF, SRW, ORF, RW2): `JPEGInterchangeFormat`
  (0x0201) + `JPEGInterchangeFormatLength` (0x0202) in any IFD; IFDs with
  `Compression == 6 or 7` and a single strip (`StripOffsets` 0x0111 +
  `StripByteCounts` 0x0117, count 1) whose first bytes are FF D8 (CR2 IFD0
  full-size JPEG; DNG preview SubIFDs); Panasonic RW2 `JpgFromRaw` (0x002E);
  Olympus ORF has its preview in the maker note (`PreviewImageStart`/
  `PreviewImageLength` 0x0101/0x0102 inside the Olympus CameraSettings
  sub-IFD) — implement if straightforward, otherwise ORF has no preview and
  that is acceptable.
- CR3: the `PRVW` box lives inside the `uuid` box
  eaf42b5e-1c98-4b88-b9fb-b7dc406e4d16; community notes disagree on the
  exact header length before the JPEG, so locate the JPEG by scanning the
  (bounded, at most 64 KiB) box payload for the first `FF D8 FF`. The
  full-size JPEG is the first `moov/trak` whose sample data begins with
  `FF D8` (data at `co64`/`stco` + `stsz`, one sample). Return both,
  full-size first. Treat these as best effort: if neither is found, the file
  simply has no preview.
- RAF: the JPEG at the big-endian offset/length at header bytes 84/88.
- HEIF/AVIF (ISO BMFF `meta`): also extend `Read` to find the primary item's
  `Exif` item (iinf item type "Exif", location via `iloc`, the payload starts
  with a 4-byte offset to the TIFF header) so HEIC photos get dates, GPS and
  orientation. `irot`/`imir` properties are the HEIF orientation; map them
  to EXIF orientation 1..8 and expose them as `Fields.Orientation` when the
  EXIF block has none. Previews in HEIF are `thmb` items; report them.

Bounds: the same caps as `Read`; at most 8 previews; `Previews` reads only
headers. Tests use `internal/testimg` TIFF/box builders (extend them for
`JPEGInterchangeFormat`, strips, CR3/RAF/HEIF layouts).

As implemented (deviations and precisions):

- `Previews` checks the first three bytes are `FF D8 FF`. It returns
  `nil, nil` for formats without previews (including plain JPEG); the error
  is non-nil only when the header cannot be read.
- Ordering: by `Width*Height` when every preview's size is known, otherwise
  by `Length` (a total order; mixed comparisons would not be transitive).
  At most 32 candidates are collected; the 8 largest are returned.
- Strips with Compression 6/7 are skipped when PhotometricInterpretation is
  CFA (32803) or LinearRaw (34892), or the IFD has CR2 slices (0xC640):
  those are lossless-JPEG raw data that also start with `FF D8`.
- ORF is supported: `0x927C` maker note with an `OLYMPUS\0II|MM` or
  `OM SYSTEM\0\0\0II` header (offsets relative to the maker note),
  `CameraSettings` (0x2020) -> 0x0101/0x0102. Old-style `OLYMP\0` notes are
  recognised but have no CameraSettings IFD, so those ORFs have no preview.
- CR3 PRVW: the JPEG is found by scanning at most 64 KiB of the uuid box for
  `FF D8 FF`; when it sits exactly 16 bytes into a `PRVW` box, that header's
  width/height/length are used, otherwise the length runs to the box end
  and the size is unknown. The full-size track's size comes from its
  `CRAW` sample entry.
- HEIF: only `thmb` items of type `jpeg` that reference the primary item;
  size from their `ispe`. Items must use construction_method 0, data
  reference 0 and exactly one extent (iloc length 0 = to end of file). The
  Exif item is the one with a `cdsc` reference to the primary, else the
  first. A payload whose TIFF offset is 0 but still begins `Exif\0\0`
  is accepted. `irot`/`imir` are applied in `ipma` association order; the
  mapping table is in `heif.go` (`heifOrientation`).
- Orientation policy: when the primary item has `irot`/`imir`, their
  mapping is `Fields.Orientation` even when the Exif item has an
  Orientation tag (libheif, libavif-based viewers and browsers follow
  `irot`/`imir`); a disagreement adds a warning. The EXIF tag is used only
  without `irot`/`imir`.
- `Read` also handles WebP (RIFF chunks, at most 64, sizes validated; the
  `EXIF` chunk, with or without an `Exif\0\0` prefix) and PNG (chunks up to
  the first `IDAT`, at most 64; the `eXIf` chunk). A truncated EXIF chunk is
  parsed as far as the file goes.
- `HasBox(r, size, name)` reports a top-level ISO BMFF box (at most 64
  boxes, sizes validated; `ErrBoxLimit` past that).
- `Read` on a HEIF with no readable Exif item but with `irot`/`imir`
  returns Fields with only `Orientation` set (and a warning) instead of
  `ErrNoExif`.
- Caps: 64 boxes per container, depth 6, 4096 boxes per file; 1024 entries
  in iinf/iloc/ipma/iref/ipco; at most 64 KiB read of one iloc/ipma/iref.
- Caution for imaging/thumbs: libheif (gen2brain/heic) applies
  `irot`/`imir` when decoding, so a decoded HEIC primary image is already
  upright. iPhone files carry both EXIF Orientation 6 and `irot` 3, so
  applying `Fields.Orientation` again would rotate the image twice.

## internal/thumbs

```go
const ThumbSize = 256    // longer side, JPEG q80, stored in thumbs.db
const PreviewSize = 1600 // longer side, JPEG q85, stored on disk

type Store struct { /* thumbs.db handle, previews dir */ }
func OpenStore(lib *library.Library) (*Store, error)   // <root>/.picmover/thumbs.db and <root>/.picmover/previews/
func (s *Store) Thumb(ctx, hash uint64) (jpeg []byte, w, h int, ok bool, err error)
func (s *Store) PreviewPath(hash uint64) string          // <previews>/<hh>/<hash-16hex>.jpg
func (s *Store) Has(ctx, hash uint64) (thumb, preview bool, err error)
func (s *Store) Put(ctx, hash uint64, thumb []byte, tw, th int, preview []byte) error // atomic: preview via temp+rename, thumb row last
func (s *Store) Delete(ctx, hash uint64) error
func (s *Store) Stats(ctx) (count int64, bytes int64, err error)
func (s *Store) Close() error

type Source struct {
    Path        string
    Kind        metadata.Kind
    Hash        uint64
    Orientation int   // from the catalog; 0 unknown
}

type Result struct {
    State      string // ok | unsupported | error
    Message    string
    Width, Height int // of the oriented full image when known
    PHash      uint64 // perceptual hash of the preview, 0 when none
    ThumbW, ThumbH int
}

type Generator struct {
    Store   *Store
    FFmpeg  string // path, "" = metadata.FindTool("ffmpeg"); not found = videos unsupported
    MaxPixels int64
}
// Generate decodes, orients, scales and stores both sizes for one file.
// Images: decode via imaging; RAW: the largest embedded preview that
// decodes; video: one frame via ffmpeg at min(1s, 10% of duration).
// Never panics; errors become Result.State "error". It skips work when the
// store already has both sizes (unless force).
func (g *Generator) Generate(ctx context.Context, src Source, force bool) Result
```

thumbs.db schema (own `user_version` = 1):

```sql
CREATE TABLE thumbs (
    hash       INTEGER PRIMARY KEY,
    width      INTEGER NOT NULL,
    height     INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    data       BLOB NOT NULL
);
```

As implemented (hardening after review):

- `Source` gains `Size int64` and `MTime time.Time` (zero = unchecked).
  When set, `Generate` stats the file first and returns `error` "file
  changed since import; run verify or rebuild" if the size differs or the
  mtime differs by more than 2 s, before the cache is consulted or written.
- `Generator` gains `Budget *imaging.MemBudget` (nil = one
  `NewMemBudget(2 GiB)` per Generator, created once) and
  `HEIFSlots chan struct{}` (nil = 4 slots). Every decode (images, RAW
  previews, ffmpeg frames, cached previews) passes them in
  `imaging.Options`; images pass the `Config` result as `Known`.
- Images are `Fit` to `PreviewSize` before `Orient`, so orienting works
  on the small image.
- Cache hits are verified: the stored preview is decoded with
  `DecodeFit(..., 256)`; failure regenerates the renditions as with
  `force`; success returns `ok` with the pHash of that decode. Fresh
  renditions compute the pHash the same way (from the encoded preview
  decoded at 256), so every `ok` Result carries a pHash and the value is
  identical for fresh and cached results.
- HEIF sequences and other `imaging.ErrUnsupported` results map to
  `unsupported`. A cancelled context anywhere gives `error` with
  `ctx.Err()`.

Previews live on disk because they are large; the directory fan-out is the
first two hex digits of the hash. Opening uses the same DSN pragmas as the
catalog. The perceptual hash is the 64-bit DCT pHash computed from the
preview reduced to 32x32 grayscale (implement it directly, about 60 lines;
no new dependency needed), plus nothing else for now.

ffmpeg frame grab: `ffmpeg -hide_banner -loglevel error -ss <t> -i <path>
-frames:v 1 -vf scale='min(1600,iw)':-2 -f image2pipe -vcodec mjpeg -q:v 3 -`
with a 60 s timeout; the JPEG comes from stdout.

## internal/catalog: schema v3

`SchemaVersion = 3`. Fresh creation only; any other `user_version` is
refused as described above. Changes from v2:

```sql
size              INTEGER NOT NULL,              -- always known now
rel_path          TEXT NOT NULL UNIQUE COLLATE NOCASE,
taken_at_source   TEXT NOT NULL,                 -- exif | video | video_utc | mtime | none
phash             INTEGER,                       -- perceptual hash, NULL until generated
thumb_state       TEXT NOT NULL DEFAULT 'pending', -- pending | ok | unsupported | error
thumb_error       TEXT NOT NULL DEFAULT '',
thumb_at          INTEGER,                       -- Unix seconds of the last attempt
```

plus an index on `thumb_state` and one on `phash`. New methods:

```go
func (d *DB) ListThumbPending(ctx, limit int, sessionID int64) ([]Media, error) // sessionID 0 = any
func (d *DB) SetThumb(ctx, tx *sql.Tx, id int64, state, msg string, phash uint64, w, h int) error // also fills width/height when they were 0
func (d *DB) ThumbStats(ctx) (pending, ok, unsupported, failed int64, err error)
```

Remove: `OpenExisting`'s migration handling becomes a plain version check
(`ErrNeedsRebuild`), `migrate_legacy.go`, legacy tests, `UpdateSize`
nullable-size handling (size is always set at import), and `List`'s
tolerance for NULL size.

As implemented: `taken_at_source` and `thumb_state` are enforced with CHECK
constraints; the separate NOCASE `rel_path` index is replaced by the
column's UNIQUE constraint. `Media` gains `PHash uint64` (0 = none),
`ThumbState`, `ThumbError` and `ThumbAt time.Time`; `InsertMedia` rejects
`Size <= 0` and stores an empty `ThumbState` as `pending`. The states are
exported as `ThumbStatePending`, `ThumbStateOK`, `ThumbStateUnsupported`
and `ThumbStateError`. `ErrNeedsRebuild` is returned (wrapped) by both
`Open` and `OpenExisting` for any `user_version` other than 3, except that
`Open` creates the schema in a file with `user_version` 0 and no `media`
table; the version is probed without enabling WAL, so a refused file is
not modified. `SetThumb` fills width/height only when both `w, h > 0` and
the stored value is NULL or 0. `ThumbStats` counts non-deleted rows;
`failed` is `thumb_state = 'error'`.

## internal/metadata

- Extensions: image adds `.heic .heif .avif .webp .gif .bmp`. Raw and video
  lists unchanged.
- Dimensions for KindImage: `imaging.Config` first (it covers every image
  format), then EXIF. Orientation for HEIC from the `irot`/`imir` mapping.
- EXIF for HEIC/AVIF through the extended reader.

## internal/importer

A second stage after the copy stage: `Run` collects the ids of rows it
imported or recorded (Imported + Existing), then runs the thumbnail stage
over them with `min(runtime.NumCPU(), 16)` workers unless
`Options.NoThumbs`. Each result is written with `SetThumb` in batches by a
single writer. `Stats` gains `ThumbsDone, ThumbsUnsupported, ThumbsFailed`;
`OnProgress` keeps firing. Cancellation stops the stage; remaining rows stay
`pending` and `picmover thumbs` finishes them later. The stage reads the
library copy of each file (zip temp files are gone by then).

## cmd

- `thumbs <library> [--workers N] [--rebuild] [--limit N]`: generates for
  every `pending` row (or all rows with `--rebuild`), with a status line and
  summary; exit 2 if any failed.
- `rebuild <library>`: as described in the policy section; prints the backup
  name and the import plus thumbnail summaries.
- `import`: gains `--no-thumbs`; summary shows thumbnail counts.
- `db`: summary shows thumbnail counts and cache size; `--list` shows the
  thumb state.
- `migrate`: removed. README updated: upgrade = `rebuild`.

## Tests

- imaging: every format decodes from tiny fixtures. WebP and AVIF fixtures
  are synthesised at test time with the gen2brain encoders (`testimg` gains
  `WebP(w,h,seed)` and `AVIF(w,h,seed)`). HEIC has no pure-Go encoder: a
  real HEIC test file is expected at `internal/testimg/testdata/sample.heic`
  (the user supplies it; keep it under 300 KB); when the file is missing the
  HEIC tests are skipped with a message, not failed. Optional tests against
  real RAW/HEIC/video samples read the directory named by the environment
  variable `PICMOVER_SAMPLES` and skip when it is unset; orientation 1..8 produce the expected pixel positions; Fit never
  upscales and preserves aspect; a 200-megapixel header is refused without
  allocation.
- exif: `Previews` for synthetic CR2-like TIFF, DNG with preview SubIFD,
  RW2 tag, CR3 PRVW + CRAW track, RAF; HEIF Exif item + irot/imir mapping;
  hostile variants stay bounded.
- thumbs: Store round trip and atomicity (a failed Put leaves nothing);
  Generator on JPEG/PNG/WebP/RAW-with-preview/unsupported/too-large/garbage;
  pHash of an image equals the pHash of its scaled copy within a few bits
  and differs from another image; video skipped when ffmpeg is absent.
- importer/cmd: import produces thumbs and previews for every imported
  file, `thumbs` finishes pending rows after a cancelled import, `rebuild`
  recreates the catalog with identical rows (except ids and timestamps) and
  reuses the cache, `--no-thumbs` leaves rows pending.

## Implementation notes (as merged)

Importer, cmd and the remaining glue (deviations and precisions):

- **Display dimensions.** `metadata.Info.Width/Height` and therefore the
  catalog's `width`/`height` are the upright (display) dimensions. For
  KindImage and KindRaw, `Default.Extract` swaps width and height when the
  final Orientation is 5..8, except when the size came from `imaging.Config`
  of a HEIF file (libheif applies `irot`/`imir` and reports the transformed
  size). AVIF is swapped like JPEG: `avif.DecodeConfig` (gen2brain/avif
  v0.6.0) returns the stored `ispe` size. An EXIF fallback size of a HEIF is
  the stored size and is swapped. Video dimensions already follow the
  display rotation. `SetThumb` only fills a size that is unknown, and
  `thumbs.Result.Width/Height` are oriented too, so both sources agree.
- **Importer API.** `Options` gains `NoThumbs bool`, `ThumbWorkers int`
  (<= 0: min(NumCPU, 16); clamped to 64), `ThumbLimit int` (not in the
  contract; needed for `thumbs --limit`) and `FFmpeg string`. `Stats` gains
  `ThumbsDone, ThumbsUnsupported, ThumbsFailed int64`. New
  `func (im *Importer) RunThumbs(ctx context.Context, sessionID int64, force bool) (Stats, error)`.
  `Run` calls `RunThumbs(ctx, sessionID, false)` after the copy stage only
  when the copy stage was neither cancelled nor fatal; the session's rows
  are exactly Imported + Existing (both are inserted with this session id).
- **Row selection.** New catalog method
  `func (d *DB) ListForThumbs(ctx context.Context, afterID int64, limit int, sessionID int64, all bool) ([]Media, error)`:
  non-deleted rows with `id > afterID` ordered by id, pending only unless
  `all`. `RunThumbs` pages through it 512 rows at a time by id cursor, so
  memory stays bounded and the pass is not confused by rows changing state
  behind it. `force` (= `thumbs --rebuild`) selects every non-deleted row
  and passes `force` to `Generate`.
- **Writer.** One goroutine applies `SetThumb` in transactions of up to 256
  rows (also flushed every 500 ms), with `context.Background()` so a
  cancellation never drops finished results. Mapping: ok -> `ok` (phash,
  w, h; message cleared), unsupported -> `unsupported` + message, error ->
  `error` + message. Any `error` result received after the context was
  cancelled is not written (the row stays `pending` and is retried), which
  covers `Generate`'s context errors and failures caused by the shutdown.
  A row deleted meanwhile (`sql.ErrNoRows`) is skipped. A catalog write
  error cancels the stage and is returned. Counters are updated as results
  reach the writer; `OnProgress` fires every interval and once at the end
  of each stage; `OnResult` is not used for thumbnails.
- **Perceptual hash.** `Generate` returns `PHash == 0` when it reuses cached
  renditions, so a rebuild would have lost the hashes. The importer
  therefore computes the stored phash from the *stored preview file*
  (`imaging.DecodeFit(..., 256)` then `imaging.PHash`) for every ok result,
  fresh or cached. The value is then identical before and after a rebuild
  or `thumbs --rebuild`; it differs from `Result.PHash` by only a few bits
  (JPEG round trip), which is the tolerance pHash is used with anyway. If
  the preview cannot be read, `Result.PHash` is kept. (Since the review
  hardening, `Generate` itself returns this same value for every ok
  result, fresh or cached: see "internal/thumbs", "As implemented".)
- **cmd.** `migrate` and every `ErrNeedsMigration`/legacy reference are
  gone. `openExisting` and `import` (and so `rebuild`'s import) wrap
  `catalog.ErrNeedsRebuild` in an error that names
  `picmover rebuild <library>` with the actual path (quoted when needed) and
  still `errors.Is` the sentinel. `rebuild` refuses a library without
  `media.db` (suggesting `import`), renames `media.db`, `-wal`, `-shm` to
  `media.db.bak-<YYYYMMDD-HHMMSS>[-wal|-shm]` (never overwriting; renames
  already done are undone if one fails, so a catalog is never separated
  from its WAL) and then runs the shared import path with source = library,
  copy mode, logging to `.picmover/logs/rebuild_*.log`. `library.IsInternal`
  treats `media.db.bak-*` as internal, so backups are never scanned.
  `thumbs` logs to `.picmover/logs/thumbs_*.log`, prints Done/Unsupported/
  Failed/Still pending and has an extra `--quiet` flag. `db` opens the cache
  only when `thumbs.db` exists, so it never creates one. The import exit
  status is also 2 when thumbnails failed.
- **jpegli stderr noise.** gen2brain/jpegli v0.4.2 prints libjpeg's
  decode warnings and errors (for example "Corrupt JPEG data: premature
  end of data segment") directly to the process's stderr from inside the
  WebAssembly module. There is no option to silence or capture them, so a
  damaged JPEG can add lines to the terminal during an import or
  `thumbs` run; the decode result itself is handled normally (fallback or
  error). Fixing this needs a fork of the module.
- **Catalog fix.** `schemaState` now reads `user_version` and the media
  table check in one statement (`pragma_user_version`): read separately, a
  concurrent schema creation committing in between made a brand-new catalog
  look like a v1 catalog, and `TestConcurrentOpenAndInsert` failed about
  one run in ten.

### Review fixes: data safety and commands

Deviations from, and precisions to, the notes above:

- **Symlinked library root.** `library.Open` (after creating the directory)
  and `library.Attach` resolve the root with `filepath.EvalSymlinks`; when
  that fails the unresolved path is kept, as before. `Root()` is the
  resolved path. The spelling the library was opened with is kept as an
  alias that `Rel` (and so `Contains` and `IsInternal`) also accepts, so
  paths spelled through the link or the target both work; no extra system
  call is made per path. Previously a rebuild through a symlinked path
  imported `.picmover/previews/*.jpg` as photos.
- **`IsInternal` backups.** Only `media.db.bak-YYYYMMDD-HHMMSS` (the 14
  digits of `library.BackupStampLayout`, as `rebuild` writes them),
  optionally followed by `-wal`, `-shm` or `-journal`, is internal;
  `media.db.bak-holiday.jpg` is a photo. New exported
  `library.IsCatalogBackup(base string) bool` and
  `library.BackupStampLayout`.
- **Rebuild records in place.** New `importer.Options.RecordInPlace bool`.
  The source must be the library root (compared with `os.SameFile`;
  otherwise `Run` fails before anything is recorded). Every regular,
  non-internal media file is recorded at its own rel path, status
  `imported_existing`, session mode `record`. A file whose hash is already
  recorded (or in flight) is a `duplicate` and is left where it is. A path
  already recorded, or claimed in this run, case-insensitively, is an
  `error` result ("duplicate path"), never a copy. Zip archives count as
  `non_media` (not extracted, since extraction would copy); symbolic links
  are `error` results; `Move` is ignored. `rebuild` uses it; `import LIB
  LIB` is unchanged. Previously a file outside its computed layout folder
  (another folder, or a layout folder that changed with `--timezone` or
  better date extraction) was copied, leaving two copies on disk.
- **`Store.Put` keeps the previous preview.** An existing preview is renamed
  to `<preview>.old` before the new one (written and fsynced in the temp
  directory) is renamed into place; then the row is inserted. On any
  failure the new preview is removed and `.old` renamed back, so the old
  row and preview stay consistent; on success `.old` is removed. `Has` and
  `Thumb` never look at `.old` files and `Stats` does not count them.
  `OpenStore` walks the previews (one fan-out level, at most 2^20 entries,
  best effort) and, for each `*.old`, renames it back when the preview is
  missing (a crash between the two renames; its row was never replaced) or
  removes it otherwise. Only one process may write a library at a time, so
  this never races with another Put.
- **`OpenStoreReadOnly`.** New
  `func OpenStoreReadOnly(lib *library.Library) (*Store, error)`. It needs
  an existing `thumbs.db` (no directory or file is created), opens it as
  the SQLite URI `file:<path>?mode=ro` (modernc passes `file:` names to
  SQLite with `SQLITE_OPEN_URI`, so `mode=ro` applies; on a plain path it
  would be silently dropped) plus `_pragma=busy_timeout(10000)` and
  `_pragma=query_only(1)`, without the journal-mode pragma, and checks the
  format version (`ErrFormat`). `Put` and `Delete` on it fail. `db` uses it
  and prints `Thumbnail cache: unavailable (<reason>)` for any error (a
  locked, damaged or incompatible cache) instead of failing; it still
  prints `0 entries` when there is no cache.
- **Thumbnail states.** `thumb_state` gains `stale` (CHECK constraint;
  `catalog.ThumbStateStale`). Schema v3 is not bumped: v3 has not been
  released, and a v3 catalog created before this change only lacks `stale`
  in its CHECK (rebuild it if `update-metadata` reports a constraint
  failure). `ListForThumbs(all=false)` selects `pending`, `stale` and
  `error` rows, so `picmover thumbs` retries failed rows on every run; the
  import's own stage still selects only its session's rows. The importer
  calls `Generate(ctx, src, force || state == stale)`. New
  `func (d *DB) MarkThumbStale(ctx context.Context, tx *sql.Tx, id int64) error`
  sets `stale` unconditionally (a `pending` row may have renditions cached
  under its hash after a rebuild); `update-metadata` calls it in the same
  transaction whenever it changes a row's Orientation. `ThumbStats` counts
  `stale` rows as pending.
- **Thumbnail writer.** Counters are updated only after the batch commits
  (rows deleted meanwhile are not counted). Every result that reaches the
  writer after cancellation is dropped, whatever its state, so the row
  keeps its state. When a batch fails, each row is retried once in its own
  statement; rows that fail again are logged and keep their state, and the
  failure is fatal only when every row of the batch fails again.
- **Perceptual hash.** Supersedes the note above: `Generate` now computes
  `Result.PHash` from the stored preview also on cache hits (and
  regenerates an unreadable cached preview), so the importer uses
  `Result.PHash` directly; its own preview-decode fallback is removed.
- **Changed-file check.** The importer passes the row's `Size` and
  `OriginalMTime` as `thumbs.Source.Size`/`MTime`, so `Generate` refuses a
  file changed since it was recorded. To make that check valid,
  `original_mtime` is now the mtime of the file as it lies in the library
  after placement (a stat of the destination; the source's mtime only when
  that stat fails). It equals the source's mtime for moves and verified
  copies (which preserve it), but differs for an identical file adopted at
  the destination and for a copy whose mtime could not be set.
- **Thumbnail stage failure.** When the copy stage succeeded but
  `RunThumbs` fails fatally (not by cancellation), `Run` returns
  `*importer.ThumbStageError` (wrapping the cause). `import` and `rebuild`
  then print the full summary, a `Warning: thumbnails were not generated:
  <cause>` line on stderr, and exit 2 with a message naming `picmover
  thumbs <library>`. A successful copy stage never exits 1.
- **rebuild.** The interrupt handler is installed before the backup
  rename, and the same context is used by the import (`runImport` takes
  it as a parameter; `nil` installs one). The backup set is `media.db`,
  `-wal`, `-shm` and `-journal`. Without `media.db`, when
  `media.db.bak-<stamp>` backups exist, the error names the newest one and
  explains how to restore it (rename it and its companions back) or to
  start over with `picmover import <lib> <lib>`.

