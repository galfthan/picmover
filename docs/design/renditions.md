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

## As implemented (frontend)

- `web/src/lib/rendition.ts`: `renditionFor(tileCssPx, devicePixelRatio)`
  returns `'thumb'` when `tile * dpr <= 256` (`THUMB_PX`), else `'large'`;
  a missing, zero or NaN ratio counts as 1. `pixelRatio()` and
  `watchPixelRatio(cb)` (a `matchMedia('(resolution: Ndppx)')` listener,
  re-armed on every change; a no-op without `matchMedia`) follow browser
  zoom and moves between screens.
- `api.largeUrl(ref)` (`web/src/lib/api.ts`): `/api/l/{lib}/large/{hash}`
  with the same `?v=<thumbAt>` versioning as `thumbUrl`/`previewUrl`.
  Types: `ThumbCounts.largeMissing?` (optional: older servers omit it),
  `JobStats.largeDone` (required in the type; `emptyStats()` defaults it
  to 0, so a server that omits it reads as 0).
- Grid (`pages/Grid.svelte`): `MAX_TILE` 480 (slider, Ctrl+wheel and the
  stored `picmover.grid.tileSize` all go through the same clamp). The
  grid computes one rendition from the laid-out tile size
  (`timeline.tile`, the stretched size, not the requested one) and the
  pixel ratio, and passes it to every `Tile` as `rendition`.
- Tile (`components/Tile.svelte`): shows the large or the thumb URL. While
  a new URL loads (threshold crossed either way, or a job versioned the
  URL) the last loaded image is kept as a second `<img class="under">`
  beneath it; that element exists only during the swap and is removed
  when the new image fires `load`. A large that fails to load (404: no
  preview) falls back to the thumb URL for that tile; a failed thumb
  behaves as before (placeholder, one retry after a job).
- Lightbox: the blurred placeholder is `largeUrl`; the full image and the
  video poster stay `previewUrl`.
- Library page: when `thumbs.largeMissing > 0` the Thumbnails card says
  "N large thumbnails to fill" and the existing "Generate missing
  thumbnails" button (the normal `thumbs` job, `rebuild: false`) fills
  them. The Jobs table line of a thumbs job appends "N large filled" when
  `stats.largeDone > 0`; `JobProgress` shows a "Large filled" stat while
  running under the same condition.
- Fake server (`web/src/testing/fakes.ts`): `GET thumb|large|preview/{hash}`
  answers a tiny image for a known hash and 404 otherwise; the library
  JSON `thumbs` comes from the exported `thumbCounts` (tests set
  `largeMissing`); `ZERO_STATS.largeDone`.
- Tests: `lib/rendition.test.ts` (sizes 100-480 at ratios 1, 1.25, 1.5,
  2, 3, bogus ratios), `lib/api.test.ts` (large URL versioned, unversioned,
  non-finite version, library id encoding), `Grid.test.ts` (src swaps
  thumb to large and back across the threshold with the underlay present
  only until `load`, large failure falls back to the thumb, ratio 2 picks
  the large at the default zoom, stored zoom clamped to 480 and wheel zoom
  stops there, a stored 400 is kept), `Renditions.test.ts` (Library page
  count and button posting the thumbs job, nothing shown when 0 or
  absent, the job line with and without `largeDone`).

Assumptions for the backend: `thumbs.largeMissing` and `stats.largeDone`
are plain JSON numbers with those exact names; the `large` route accepts
the same `?v=` query as `thumb` and ignores it; a large that cannot be
produced answers with an error status (404) or the thumb bytes, either of
which the tile handles.
