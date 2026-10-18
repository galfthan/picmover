# Home time zone timeline

Status: implemented on branch `feat/home-time` (catalog schema version 4).

## Problem

A library lives in one place (say Helsinki, EET/EEST) but is filled on
trips. A phone records photos with EXIF `DateTimeOriginal` plus
`OffsetTimeOriginal` (the local wall clock and its offset), but videos only
with the UTC `creation_time` of the MP4 `mvhd` box, which the importer
turned into a wall clock of the import's zone. Sorting by the stored wall
clock put a trip's videos an hour after its photos (CET trip, EET home),
and nothing showed that the photos were taken at 14:30 local time.

## Model

- **Local time** (`taken_at`, unchanged): the wall clock where the item was
  taken, as Unix seconds "as if UTC", with its offset in `taken_at_offset`
  when known.
- **Home zone**: an IANA zone name per library, the meta key `home_zone` in
  the catalog (`media.db`). It travels with the library. The first import
  stores its zone there (the web import sends the browser's zone; the CLI
  uses `--timezone`, default the system zone); a rebuild carries the old
  catalog's value over. It is changed on the Library page
  (`POST /api/l/{lib}/settings {"homeZone": "Europe/Helsinki"}`) or with
  `picmover home-zone <library> <zone>`, which recomputes every home time.
  With no home zone, home times are the local wall clocks (the behaviour
  before home times).
- **Home time** (`taken_at_home`, indexed): the same instant on the home
  zone's clock, with that zone's real rules at that date (summer photos in
  summer time): `instant(taken_at, offset).In(home)`. A row whose offset is
  unknown keeps `taken_at_home = taken_at` (assumed home).

Everything on the timeline uses the home time: ordering, month buckets and
the scrubber, date ranges, months and every-year ranges in filters, the map
lists, covers, the newest item of an import, the selection of a CLI move,
backup order, and the `<type>/YYYY/MM` folder of new imports. `taken_at`
stays the recorded local time; the viewer shows the home time and, when the
offset differs from the home zone's at that instant, the local time:
`14/07/2024 15:30:00 UTC+03:00 · 14:30 local (UTC+02:00)`.

The API's item `takenAt` is the home time. The detail adds `localTakenAt`,
`offset` (local), `offsetSource` and `homeOffset`.

## Offset source

`taken_at_offset_source` (CHECK-constrained, NULL when the offset is
unknown) says where the local offset came from:

| value    | meaning                                                         |
|----------|-----------------------------------------------------------------|
| `exif`   | EXIF `OffsetTime*` (or an offset in the date itself)            |
| `video`  | QuickTime creation date with its offset                         |
| `gps`    | reserved: a zone looked up from coordinates (not implemented)   |
| `nearby` | estimated from a photo taken near the video (below)             |
| `home`   | an instant (video UTC, file time) shown in the home zone        |

Rows on `home` are re-expressed in the new zone whenever the home zone
changes, so they never show a separate local time. `nearby` and `gps` are
marked as estimated in the viewer (`≈`, with a tooltip).

## Video offsets (`video_utc`)

The instant of such a video is known; its local offset is inferred in this
order:

1. GPS of the video, through a time zone lookup: **not done**. The only
   pure-Go, CGO-free library with embedded data that was evaluated,
   `github.com/ringsaturn/tzf` v1.2.5 with its default (lite topology)
   data, adds 12.3 MB to a stripped binary (1.58 MB -> 13.93 MB for a
   hello-world; the smallest data file alone is 6.9 MB), over the 5 MB
   budget. `gps` stays reserved in the CHECK constraint.
2. The nearest photo by instant with an EXIF offset within ±6 hours from
   the same camera. Ranking: same `camera_make`+`camera_model` in the same
   import session, then the same camera in any session, then (for a video
   without a real camera name: Android phones write only
   `com.android.version`, stored as make `Android`; or a video of the same
   make) a photo of the same import session (for an Android video, a
   phone's). Nearest instant within a rank.
3. The home zone (or, without one, the offset of the import's zone).

Photos and videos arrive in any order, so the inference is a pass at the
end of every import (`catalog.InferVideoOffsets`) over the session's
videos plus every video still on `home` (a later import may bring its
photos). `update-metadata` runs it over all videos after changing rows; it
keeps an estimated offset while the re-read date is unchanged.

## Migration (schema 3 -> 4)

`Open` and `OpenExisting` migrate a version 3 catalog in place, in one
IMMEDIATE transaction (other versions still need `picmover rebuild`):

1. add `taken_at_home`, `taken_at_offset_source` and the
   `media_taken_at_home` index (and the `meta` table if missing);
2. offset sources from `taken_at_source` (`exif`, `video`, else `home`);
3. the home zone: meta's if set, else the system zone when its IANA name
   is known (`$TZ`, the `/etc/localtime` link), which is the zone the old
   catalog's instants were most likely converted to; else none;
4. every home time, with `home` rows re-expressed in the home zone;
5. the video offsets inferred as above (stored wall clock minus stored
   offset is the instant).

Files on disk are not moved: folders laid out by the old wall clock stay.
`PeekFile` (the library list) reads a version 3 catalog without migrating
it, ordering by `taken_at`.
