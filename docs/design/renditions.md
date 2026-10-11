# Renditions: a 640 px large thumbnail, WebP encoding

Status: contract for branch `feat/renditions` (from master after PR #7).
"As implemented" sections go at the end, backend then frontend.

## Why

The grid tile goes up to 320 CSS px and the stored thumbnail is 256 px, so
on a HiDPI screen (device pixel ratio 2) a 320 px tile shows a 256 px image
scaled up 2.5 times. Thumbnails and previews are plain JPEG from Go's
standard encoder (quality 80 and 85). The WebP encoder (gen2brain/webp,
WebAssembly, no cgo) is already a dependency.

## Renditions

| name    | long side | where                                    | format (new writes) |
|---------|-----------|------------------------------------------|---------------------|
| thumb   | 256 px    | `thumbs.db`, table `thumbs.data`          | WebP, quality 80    |
| large   | 640 px    | `.picmover/large/<hh>/<hash>.webp`        | WebP, quality 82    |
| preview | 1600 px   | `.picmover/previews/<hh>/<hash>.<ext>`    | WebP, quality 85    |

`<hh>` is the first byte of the hash in hex as today. Existing JPEG
renditions stay valid: the preview lookup tries `.webp` then `.jpg`; the
thumb blob and every served file are sniffed (`imaging.Sniff`) for the
content type. Nothing is regenerated unless asked (`thumbs --rebuild`).
Embedded JPEG previews copied out of RAW files stay JPEG (`.jpg`), as now.

`thumbs.db` keeps its schema and `FormatVersion`: no column changes. The
`large` presence is a file-existence check like the preview's (`Has`
reports thumb, large, preview).

### Generation

`Generator.store` scales the upright image to the preview (1600), the
large (640) from the preview and the thumb (256) from the preview as now,
encodes all three as WebP and stores them in one `Put`. The pHash is still
computed from the encoded preview decoded at 256, so it stays comparable
with pHashes computed from JPEG previews (a different encoder moves a few
bits at most; the similarity threshold absorbs that; say so in the doc).
Video frames (ffmpeg MJPEG pipe) go through the same path.

**Filling the large rendition for existing content** never decodes the
original: the large is derived from the stored preview (`DecodeFit` to
640, WebP encode). Two paths do this:

1. The thumbnail job (`thumbs` CLI and the `thumbs` job) treats a row that
   has a thumb and a preview but no large as pending "large" work, done
   from the preview, counted in a new `Stats.LargeDone` and shown in the
   job card as "N large thumbnails filled". Rows without a preview (video
   without ffmpeg, unsupported) are skipped as today. `--rebuild`
   regenerates everything from the originals as WebP.
2. On demand: `GET /api/l/{lib}/large/{hash}` serves the file when it
   exists; otherwise, when the preview exists, it derives the large, writes
   it atomically (temp + rename, never replacing an existing file) and
   serves it. Per-hash singleflight; at most `max(2, NumCPU/2)` derivations
   at once, others wait; a derivation failure answers with the thumb
   bytes (so the grid still shows something) and logs at debug. The
   server's store is opened read-only today (`Store` "read-only store
   renditions are served from"): the on-demand writer needs a write path
   for the large directory only; thumbs.db is not written by the server.
   When the preview is missing too, 404 as for the preview.

### API

- `GET /api/l/{lib}/large/{hash}`: as `thumb`/`preview`; same cache
  headers and versioned URLs; `Content-Type` from the sniffed bytes
  (`image/webp` or `image/jpeg`); 404 when neither large nor preview exist.
- `thumb` and `preview` answer with the sniffed content type (they may now
  be WebP).
- Library JSON `thumbs` summary gains `largeMissing` (rows with a preview
  but no large) so the Library page can offer "Fill large thumbnails" (it
  is the normal thumbs job; the button just starts it).
- Job stats JSON gains `largeDone`.

### Web

- `api.largeUrl(ref)`.
- Grid tiles choose the rendition: `tile * devicePixelRatio <= 256` →
  thumb, else large. `MAX_TILE` becomes 480. Switching zoom across the
  threshold swaps the `src`; keep the thumb as the placeholder (CSS
  background or an `<img>` underneath) so there is no flash of empty tiles.
- Lightbox: the blurred placeholder uses the large rendition instead of
  the thumb; the full image stays the preview.
- Library page: the Thumbnails section shows the large count and the
  button; the job card shows the large progress line.
- Tests: rendition choice at several tile sizes and pixel ratios
  (pure function in `lib/`), the Library page counts and button, the job
  card line, api URL building.

### CLI

`picmover thumbs <library>` fills missing large renditions as part of its
normal pass and reports "Large filled: N". `--rebuild` as above. The
summary names the formats written (WebP).

### Tests (Go)

- Generator writes WebP for all three (sniff), sizes right, pHash agrees
  with the JPEG path within 4 bits on the sample images.
- Store: `Has` with large; preview lookup `.webp` then `.jpg`; mixed
  formats served with the right content type; `cleanOld` covers the large
  dir.
- Large from preview: JPEG preview → WebP large; a RAW embedded JPEG
  preview stays `.jpg` and gets a WebP large.
- Server: on-demand derivation (file appears, second request served from
  disk, concurrent requests derive once), 404 without preview, fallback to
  thumb bytes on failure, read-only library folder (derivation fails
  quietly and the thumb bytes are served).
- Thumbs job: missing-large rows filled without touching originals (an
  original deleted after import still gets its large from the preview).

### Not in scope

Regenerating existing renditions automatically; AVIF; changing the
preview size; client-side format negotiation.

## As implemented (backend)

Branch `feat/renditions-backend` (from `fe7d5b4`).

### What was built

- `internal/imaging`: `EncodeWebP(w, img, quality)` (gen2brain/webp, method
  `WebPMethod = 0`; the pixels are packed into a tight `*image.RGBA` first,
  since the encoder reads `Pix` as one block). Decoding WebP already worked
  by sniffing (x/image/webp); `TestEncodeWebPRoundTrip` and
  `TestDecodeFitWebPPreview` confirm the `DecodeFit` path reads both our
  output and another encoder's.
- `internal/thumbs`:
  - Constants `LargeSize = 640`, `LargeQuality = 82`, `LargeDir = "large"`;
    the other sizes and qualities are unchanged (now WebP qualities).
  - `PreviewPath(hash)` returns the existing preview, `.webp` first, then
    `.jpg`; the `.webp` path when neither exists. `LargePath(hash)` is
    `<large>/<hh>/<hash>.webp`.
  - `Has(ctx, hash) (thumb, large, preview bool, err error)`.
  - `Put(ctx, hash, thumb, tw, th, large, preview)`: the preview is named
    after its sniffed bytes (`.webp` or `.jpg`; anything else is refused);
    every existing preview of the hash (either format) is moved to `.old`
    and removed on success, so a rebuild that converts a JPEG preview to
    WebP leaves no `.jpg` behind. `large == nil` removes an existing large
    thumbnail (it was derived from the previous preview). A failed Put
    restores the previous previews and removes the large thumbnail (filled
    again later from whichever preview remains).
  - `PutLarge(hash, data) (written bool, err error)`: fsynced temp file
    `.<hash>-*.tmp` in the fan-out directory, then
    `library.RenameNoReplace`; an existing file wins (`false, nil`). Works
    on a read-only Store.
  - `DeriveLarge(ctx, store, hash, opts)` / `EncodeLarge(...)` /
    `Generator.FillLarge(ctx, hash)`: open the preview, sniff (JPEG or
    WebP), `DecodeFit` to 640, `Fit`, WebP q82, `PutLarge`. A missing
    preview wraps `thumbs.ErrNoPreview`. The original is never opened.
  - `Generator.store` encodes all three as WebP (large and thumb are
    scaled from the 1600 px preview); the pHash is still computed from the
    encoded preview decoded at 256. `Generate` on cached renditions with a
    missing large thumbnail derives it from the preview
    (`Result.LargeFilled`, `Result.LargeError`).
  - `Presence` (`store.NewPresence()`): one `ReadDir` per fan-out
    directory, cached, answering `Preview(hash)` / `Large(hash)`. At most
    2 x 256 listings for any catalog size.
  - `cleanOld` (at `OpenStore`): a `.old` preview is removed when a
    preview of the hash exists in either format, else renamed back; large
    temp files older than one hour are removed (younger ones may be a
    running server's).
  - `Stats` counts the large directory too (not `.old`, not temp files);
    `Delete` removes both preview formats and the large thumbnail.
- `internal/catalog`: `ListThumbOK(ctx, afterID, limit, sessionID)` and
  `ThumbOKHashes(ctx, fn)`. No schema change.
- `internal/importer`: `Stats.LargeDone`. `RunThumbs` without force runs
  the existing pass, then walks the `thumb_state = 'ok'` rows (pages of
  512, by id) and sends a large-only job for each row whose preview exists
  and whose large thumbnail does not, according to the `Presence`
  listings. Large jobs bypass the catalog writer (nothing is recorded).
  `ThumbLimit` counts both kinds. With `--rebuild` (force) only the first
  pass runs, and it rewrites everything from the originals.
- `cmd`: the thumbs/import summary prints `Large filled:      N` and
  `Written as:        WebP (thumbnail 256 px, large 640 px, preview 1600 px)`;
  the thumbs status line appends ` | Large filled N` once nonzero; the
  `thumbs` help text describes the large fill and WebP.
- `internal/server`: route `GET /api/l/{lib}/large/{hash}`; `thumb`,
  `preview` and `large` all send the sniffed content type; library JSON
  `thumbs.largeMissing`; job stats `largeDone`.

### JSON shapes and HTTP behaviour (for the frontend)

`GET /api/l/{lib}/large/{hash}?v=<thumbAt>`

| case | status | body | headers |
|---|---|---|---|
| large file exists | 200 | the file | `Content-Type: image/webp` (sniffed), `ETag: "<hash>-1-<v>"`, `Cache-Control: private, max-age=604800`, Range/If-None-Match as for previews |
| missing, preview exists | 200 | derived now, then served as above | as above |
| derivation failed (read-only folder, undecodable preview, timeout) | 200 | the 256 px thumbnail bytes | `Content-Type` sniffed (`image/webp` or `image/jpeg`), `Cache-Control: no-store`, no ETag |
| no preview (pending, unsupported, video without ffmpeg) | 404 | `{"error":"no large thumbnail"}` | |
| failed derivation and no thumbnail either | 404 | `{"error":"no large thumbnail"}` | |
| bad hash | 400 | `{"error":"invalid hash: want 16 lower-case hex digits"}` | |

Use the same `?v=<thumbAt>` as the thumb URL. A fallback response is not
cached, so the next request (or a reload) gets the real large thumbnail
once it exists.

`thumb` and `preview` are unchanged except `Content-Type`, now
`image/webp` for new renditions and `image/jpeg` for older ones
(`application/octet-stream` for anything else, which never happens in
practice).

Library JSON (`GET /api/l/{lib}/library`), `thumbs` object:

```json
"thumbs": {"pending": 0, "ok": 5, "unsupported": 0, "error": 0, "largeMissing": 3}
```

`largeMissing` counts rows with `thumb_state = 'ok'`, not soft-deleted,
whose preview file exists and whose large file does not: exactly the rows
the thumbs job fills. "Fill large thumbnails" starts the normal job:
`POST /api/l/{lib}/jobs {"kind":"thumbs"}`.

Job stats object (job JSON `stats`, SSE `progress`/`state`/`done` events):

```json
"stats": {"scanned": 0, "imported": 0, "existing": 0, "duplicates": 0,
          "skippedSmall": 0, "nonMedia": 0, "errors": 0, "bytesCopied": 0,
          "rate": 0, "thumbsDone": 0, "thumbsUnsupported": 0,
          "thumbsFailed": 0, "largeDone": 3}
```

`largeDone` counts large thumbnails filled from stored previews only (rows
whose thumbnail already existed). Fresh renditions (`thumbsDone`) include
their large thumbnail and are not counted again. A large fill that fails
is logged (warn, `large thumbnail not filled`) and is not a job failure.

### Deviations and decisions made while implementing

1. **RAW embedded previews are not copied verbatim, so they are WebP.**
   The contract says embedded JPEG previews copied out of RAW files stay
   `.jpg` "as now", but the code never copied them: `Generator.raw`
   decodes the largest embedded preview, scales it to 1600 px and applies
   the EXIF orientation (embedded previews are stored unrotated), then
   `store` re-encodes it. Copying the bytes would lose the rotation and
   the size cap, so RAW renditions go through the same WebP path as every
   image. The listed test "a RAW embedded JPEG preview stays `.jpg` and
   gets a WebP large" became: the RAW generation test checks WebP preview
   and large (oriented), and `TestFillLargeFromJPEGPreview` checks that a
   JPEG preview (what every existing library has) stays `.jpg` and gets a
   WebP large.
2. **WebP method 0.** libwebp's default method 4 is too slow as
   WebAssembly: on a 1600x1067 camera-like image (gradient plus sensor
   noise) method 0 takes 102 ms (150 KB), method 2 201 ms (159 KB), method
   4 544 ms (164 KB), against 24 ms (265 KB) for Go's JPEG encoder at the
   same quality. On a smooth scene: method 0 72 ms (20.7 KB), 2 115 ms
   (15.8 KB), 4 291 ms (12.7 KB), JPEG 15 ms (48.8 KB). Method 0 is still
   much smaller than JPEG; `imaging.WebPMethod` is the one knob.
3. **pHash within 4 bits holds for photo-like content only.**
   `TestPHashWebPMatchesJPEG` compares `Generate`'s pHash (WebP preview
   decoded at 256) with the old JPEG path (JPEG q85 preview decoded at
   256). On six photo-like scenes (overlapping shapes, colours, mild
   noise; 1200 to 2400 px) the distance is 0 to 2 bits, asserted <= 4. On
   synthetic gradients and noise (testimg's sample JPEGs and PNG, the
   smooth sine scenes) it is 2 to 28 bits, but there the pHash is unstable
   anyway: JPEG q80 vs q85 already moves it 0 to 24 bits, because many AC
   coefficients sit next to the median. Those samples are logged with that
   "noise floor", not asserted. Near-duplicate pairs among real photos
   should survive a `--rebuild`; pairs involving flat, synthetic or
   noise-like images may change.
4. **`largeMissing` is cached.** The existing library summary is pure SQL
   and not cached; `largeMissing` needs the catalog's ok hashes plus one
   listing of each fan-out directory (two of 256), so it is cached per
   open library for 60 s (`largeMissingTTL`), and invalidated when any job
   on that library ends and after each successful on-demand derivation.
   The first library request after that pays the walk.
5. **Fill selection by directory listings.** The thumbs job's large pass
   reads the ok rows in pages of 512 by id and checks them against
   `Presence` (one `ReadDir` per fan-out directory, read once per run), not
   one stat or SQL query per row. Listings can be stale within a run (a
   file appearing later); `FillLarge` rechecks before writing and
   `PutLarge` never replaces, so a stale "missing" costs only one stat.
   Unlike `Has`, a listing counts an empty file as present.
6. **On-demand derivation details.** Singleflight per (store, hash): later
   requests wait for the first one's result. At most `max(2, NumCPU/2)`
   derivations run at once, with a 256 MiB decode memory budget shared by
   them. The derivation runs detached from the request that started it
   (`context.WithoutCancel` plus a 30 s timeout), so waiters are not failed
   by that client going away. A failure is logged at debug with the
   library id and hash.
7. **The server's store stays read-only.** `PutLarge` is allowed on a
   read-only `Store`; it writes only into `.picmover/large/` (temp file in
   the fan-out directory, no-replace rename), never thumbs.db, never
   `.picmover/tmp/`.
8. **`Put` with a nil large thumbnail removes the existing one** (used by
   tests and by anything storing renditions without a large size), since
   it was derived from the replaced preview.

### Timings

AMD Ryzen 7 7800X3D (8 cores, 16 threads), Linux, `-tags nodynamic`.

| measurement | before (JPEG) | after (WebP) |
|---|---|---|
| `BenchmarkGenerate12MP`: one 4000x3000 JPEG, decode + all renditions + pHash + store, one goroutine | 247 ms | 293-299 ms |
| `BenchmarkGenerate12MPParallel`: 16 at once, per file (thumbnail-stage throughput) | 39-42 ms (about 25 files/s) | 49-51 ms (about 20 files/s) |
| 136k files at that rate | about 91 min | about 113 min |
| `BenchmarkDeriveLarge`: one large from a stored 1600 px preview, one goroutine | - | 61 ms from a WebP preview, 39 ms from a JPEG preview |
| `BenchmarkDeriveLargeParallel`: 16 at once from JPEG previews, per large | - | 6.2-6.6 ms (about 15 min for 136k) |

Decoding a WebP preview (x/image/webp, full 1600 px decode) is slower than
a JPEG preview (jpegli decodes at a reduced DCT scale), which is why the
two derive timings differ. The 12 MP benchmark image is the synthetic
camera-like `testimg.BigJPEG`; real photos with more detail encode
somewhat slower in WebP (see the encoder numbers above).

### Tests

- `internal/imaging`: `TestEncodeWebPRoundTrip` (RGBA, offset sub-image,
  YCbCr, transparent NRGBA; sniff, decode, pHash), `TestDecodeFitWebPPreview`,
  `BenchmarkEncodeWebP`.
- `internal/thumbs`: `TestPHashWebPMatchesJPEG`; `TestStoreLargeAndMixedFormats`
  (`Has` with large, `.webp` before `.jpg`, WebP Put replaces a JPEG
  preview, `Stats` with large, nil large removes it, `Delete` removes all);
  `TestFillLargeFromJPEGPreview` (JPEG preview to WebP large, no
  replacement, `ErrNoPreview`, small preview kept at its size);
  `TestGenerateCachedFillsLarge` (original deleted, large derived from
  the preview, same pHash); `TestPutLargeReadOnlyAndClean` (read-only
  store, no replace, stale vs fresh temp files, `Presence`);
  `TestCleanOldAcrossFormats`; the existing generator tests now check
  that preview, thumb and large are WebP with the right sizes (including
  the oriented RAW preview); `BenchmarkGenerate12MP`,
  `BenchmarkGenerate12MPParallel`, `BenchmarkDeriveLarge`,
  `BenchmarkDeriveLargeParallel`.
- `internal/importer`: `TestRunThumbsFillsMissingLarge` (originals deleted
  after import, large filled from previews, catalog rows untouched, a row
  without a preview skipped, `ThumbLimit`, second run fills nothing).
- `internal/server`: `TestLargeServedAndDerived` (served from disk, derived
  on demand and the file appears, second request from disk,
  `largeMissing` before and after, 404 without preview, 400),
  `TestLargeConcurrentDerivesOnce` (8 concurrent requests, one
  derivation), `TestLargeFallbackToThumb`, `TestLargeReadOnlyFolder`
  (skipped as root and on Windows), `TestMixedFormatsServed` (JPEG thumb
  and preview served as `image/jpeg`, their large as `image/webp`, new
  renditions as `image/webp`), `TestThumbsJobFillsLarge` (job
  `largeDone`, `largeMissing` back to 0); `TestThumbAndPreview` now expects
  `image/webp`.
