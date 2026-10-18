# Video playback in the viewer

Status: as implemented on branch `feat/video-playback`.

## Problem

The viewer gave a `<video>` the original file whenever the extension was
MP4, M4V, MOV or WebM. Phones record HEVC (often 10-bit HDR) that Chrome,
Chromium and WebView2 without the HEVC extension cannot decode: the player
stayed black with no explanation. Camcorder MTS, AVI and MKV files had no
player at all, although most hold H.264 that every browser plays.

## Decision in the browser

`web/src/lib/playback.ts` decides, `VideoPlayer.svelte` acts:

1. **Direct** (the original from `/file`) when the container is one browsers
   play and `canPlayType` says yes to the real codec string (`avc1.640029`,
   `hvc1.2.4.L153.B0`, `vp09.02.41.10`, ...) built from the catalog's codec
   info. With an unknown codec the original is tried.
2. **Remux** when the container is the problem but the video codec plays in
   MP4 (H.264 in MTS/AVI/MKV), or when only the audio is the problem (AC-3,
   PCM).
3. **Transcode** otherwise, when the server has ffmpeg (`canConvert`).
4. **None**: a message naming the codec ("This browser can't play HEVC
   (H.265) 10-bit HDR video.") and the remedy (install ffmpeg; on Windows
   also Microsoft's HEVC Video Extensions), with Download original.

At run time a failure moves to the next step (direct → transcode, remux →
transcode, transcode → message): the `error` event, or `loadedmetadata`
with `videoWidth == 0` (the browser parsed the file but has no decoder for
its video track).

The transcode size is a **tier**, the short side of the output: 480, 720,
1080, 1440 or 2160. The player takes the size the video is shown at (the
player box, contain-fit, times `devicePixelRatio`), rounds its short side up
to a tier (5% slack), and asks for the source size (`h=0`) when that tier is
not below the source's short side. The server applies the same cap with the
catalog's dimensions, so a tier is never above the source and all such
requests share one cache entry. Short side rather than height keeps portrait
phone video sharp.

## Codec info in the catalog

`video_info` (catalog, keyed by content hash): codec, codec tag, profile,
level, pixel format, bit depth, colour transfer and primaries, frame rate,
first audio codec, container. It is a derived table like `similar_pairs`:
created on open when absent, no schema version change, so a catalog from
before keeps working (`internal/catalog/video.go`). It is filled at import
(from the same ffprobe run as the other metadata), by `update-metadata`
(also on rows whose other metadata did not change), and lazily by the
server for a video row without one (first detail or stream request:
ffprobe, then recorded). A file ffprobe fails on is remembered for the
process (hash, size, modification time) and not probed again until it
changes. The media detail carries it as `video`, plus
`canConvert` and `canOpen`.

## Conversion endpoints

```
GET /api/l/{lib}/stream/{id}?mode=remux|transcode[&h=480|720|1080|1440|2160|0][&t=seconds]
GET /api/l/{lib}/stream/{id}/status?mode=...&h=...[&t=...]
    -> {cached, encoding, bytes, encoder, encoders, tier, start}
```

Ordinary scoped GETs: any client the server answers can use them (the LAN
share's phone viewer too, once it exists). `h` is only for transcode and
must be a tier; `t` must be finite and non-negative.

- **Remux**: `-c:v copy` (`-tag:v hvc1` for HEVC), audio copied when AAC or
  MP3, else AAC 160k stereo.
- **Transcode**: H.264 High, 8-bit 4:2:0, AAC 160k stereo, at most 60 fps,
  scaled so the short side is the tier. ffmpeg's autorotate applies the
  rotation, so the output is upright. HDR (PQ `smpte2084` or HLG
  `arib-std-b67`) is tone-mapped to SDR BT.709 with
  `zscale` (linear, npl 100) → `tonemap=hable` → `zscale` BT.709 when the
  build has both filters; else it is only converted to 8-bit (washed out but
  playable).

Both write a **fragmented MP4** (`frag_keyframe+empty_moov+default_base_moof`,
fragments of at most 1 s) to ffmpeg's stdout. The server appends it to a
partial file and streams that file to every client of the conversion as it
grows, so the first picture arrives as soon as the first fragment exists
and a late client still starts at byte 0. When ffmpeg finishes, the
fragmented file is rewritten with `-c copy -movflags +faststart` into
`<library>/.picmover/video/<hh>/<hash>-v1-<variant>.mp4` (`variant` is
`remux`, `src` or `h<tier>`, plus `-tm` when an HDR source was
tone-mapped, so a build without zscale never serves another build's
output; `v1` is the format version), which is then
served with `http.ServeContent`: Range requests, seeking, ETag.

### Seeking while it encodes

A live stream has no length and no Range support, and Chromium cannot seek
in it (its `seekable` range is empty and its duration grows with the data,
checked with headless Chromium). While the conversion runs, the player hides
the native controls and shows its own: play/pause, the real duration from
the catalog, a seek bar, mute, full screen.

- A seek inside what the browser has buffered is a plain `currentTime`.
- A seek beyond it asks `/status`: when the conversion has finished
  meanwhile, the player loads the cached file (native controls) and seeks
  there; otherwise it loads `...&t=<seconds>`, a new uncached stream that
  starts at that time (`-ss` before `-i`), and adds the offset to the times
  it shows. A transcode starts exactly there. A remux copies from a
  keyframe: the status call (with `t`) answers `start`, the last video
  keyframe at or before t (ffprobe of the packets of the 30 s before), and
  the player asks for and counts from that time (the stream also says it in
  `X-Picmover-Start`).
- A seek stream keeps the full conversion of the same output running (it
  counts as one of its readers), so a later seek finds it cached.
- A failed seek stream is asked for once more, then the full conversion is
  loaded (seeking in it once cached); only then does the player fall back
  to the next plan, at the same position (a failed remux seek becomes a
  transcode from there, not from 0).

### Concurrency, cancellation, cache

- One conversion per (library, hash, variant); readers attach to it. Seek
  streams are separate and never cached.
- At most 2 full conversions at a time (`defaultMaxEncodes`); others wait
  for a slot (their request waits with them). Seek streams have their own 2
  slots, so a seek never waits for a whole conversion, and a new seek of
  the same output cancels the previous seek stream (one per viewer).
- A conversion nobody reads any more is cancelled after 5 s (1 s for a seek
  stream): browsers often drop a media request and issue it again. ffmpeg is
  killed (`exec.CommandContext`), the partial file removed.
- The cache is capped per library (`--video-cache-mb`, app.json
  `videoCacheMB`; default 5 GB, minimum 256 MB). Everything in the
  directory counts, partial and temporary files too. Before the faststart
  rewrite the oldest files go until the copy fits next to the partial file;
  after it, again until all fits. A conversion larger than half the cap is
  only streamed, never cached. Serving a cached file renews its time (at
  most once a minute). Running conversions can still exceed the cap
  briefly: their size is not known in advance. Partial files
  (`*.part`, `*.tmp`) left by a crash are removed before the first conversion
  in a library's cache after start.
- Shutdown (`StopJobs`) cancels the import job and every conversion (seek
  streams and faststart rewrites included: they all derive from the
  manager's context) and waits for both in parallel within its timeout.
- Trash, restore and permanent delete first cancel the conversions of
  those hashes and wait for their ffmpeg to exit; a move between libraries
  cancels the library's conversions and `/stream` answers 409 while a move
  runs. On Windows a file ffmpeg holds open cannot be renamed or removed.

## Encoder choice

Probed once per process (in the background at start): `ffmpeg -encoders`
and `-filters`, then a half-second test encode with each hardware candidate
in order until one works: NVENC, Quick Sync, VA-API (first render node),
AMF on Linux; NVENC, Quick Sync, AMF on Windows; VideoToolbox on macOS. Then
the same for the software ones, `libx264 -preset veryfast -crf 23`, then
`libopenh264`. A hardware encoder that fails on a particular file before
writing anything is replaced by the software one for that conversion. No
working encoder: a transcode fails with that reason (remux still works).
The `encoders` field of `/status` shows the choice. Commands are argument
lists (`exec.CommandContext`, no shell), inputs prefixed `file:`; on Windows
ffmpeg gets no console window.

## Open original

`POST /api/l/{lib}/media/{id}/open` opens the original in the default
program of the server's computer: `xdg-open` (Linux, BSD), `open` (macOS),
`ShellExecute` "open" (Windows, no `cmd /c start`, which would parse `&`
and `^` in file names). Only for requests from that computer: a loopback
remote address, and not through a listener wrapped with
`server.RemoteOnly` (the LAN share must wrap its handler with it); 403
otherwise. The path comes only from the row and passes the same checks as
`/file`, and only a photo or video extension reaches the opener. The detail's `canOpen` tells the viewer whether to offer the
button (false for other devices and when there is no opener).

## Measured (RTX 4070 Ti SUPER, ffmpeg 8.1)

- 4K HEVC 10-bit HDR10, portrait (rotation 90), 20 s → 1080p tier, NVENC
  with tone mapping: first byte 0.31 s, whole stream 6.0 s (3.4× real time),
  output 1080×1920 yuv420p bt709; cached file then served with 206 ranges.
- 1080p H.264 + AC-3 MTS, 30 s, remux: first byte 0.02 s, done 0.34 s.
- Seek stream from 10 s of the same HEVC at 720: first byte 0.63 s.
- Headless Chromium on the viewer: metadata 0.8 s after navigation, a seek
  beyond the buffer plays from there within a second; after the conversion
  finished, a seek switches to the cached file with native controls.

## Not done

- Hardware decoding (`-hwaccel`): decoding runs on the CPU, only encoding on
  the GPU.
- Adaptive streaming (HLS/DASH) and switching tiers during playback: the tier
  is chosen once when the video is opened.
- The phone viewer of the LAN share (branch `feat/lan-share`) has its own
  lightbox; it can use the same endpoints.
- `video_info` rows of deleted media are not removed (a hash names the same
  content forever; they are tiny).
