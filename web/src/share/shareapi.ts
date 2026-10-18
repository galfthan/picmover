// Client for the share listener (docs/design/lan-share.md "Share listener:
// routes"), the only module of the phone app that knows URLs. It imports
// nothing at run time from the admin client (lib/api.ts): a module shared
// by both Vite entries lands in a common chunk whole, and the phone must
// not download the admin API. Answers are parsed leniently (missing fields
// defaulted, malformed entries dropped) because the server and this client
// were written in parallel against the design; a capability the session
// does not grant is off, never defaulted on.

import type { MediaItem, ShareCapabilities, ShareCapability, TimelineBucket } from '../lib/api';
import type { VideoInfo } from '../lib/playback';

export const CAPABILITIES: readonly ShareCapability[] = ['video', 'download', 'rate', 'location'];

/** Why a paired phone is refused (403 `{state}`): its share is paused, expired, broken or not served now. */
export type BlockedState = 'paused' | 'expired' | 'gone' | 'unavailable';

export class ShareError extends Error {
  readonly status: number;
  /** The parsed JSON error body, when there was one. */
  readonly body: Record<string, unknown> | null;
  /** 403 `{state}`: the share's state; "" otherwise. */
  readonly state: string;
  constructor(status: number, message: string, body: Record<string, unknown> | null = null) {
    super(message);
    this.name = 'ShareError';
    this.status = status;
    this.body = body;
    this.state = body && typeof body.state === 'string' ? body.state : '';
  }
}

export function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

export function isAbort(e: unknown): boolean {
  return e instanceof DOMException && e.name === 'AbortError';
}

/**
 * fetch itself failed (no answer at all: the computer is off, asleep, or
 * on another network). Only that is "cannot reach"; a TypeError thrown
 * elsewhere (a bug, a bad answer) keeps its own message.
 */
export class NetworkError extends Error {
  constructor(cause: unknown) {
    super(cause instanceof Error ? cause.message : String(cause));
    this.name = 'NetworkError';
  }
}

export const UNREACHABLE = 'Cannot reach the computer. Is it on, and is this phone on the same Wi-Fi?';

/** Text for anything this module throws. */
export function errorText(e: unknown): string {
  if (e instanceof ShareError) return e.message;
  if (e instanceof NetworkError) return UNREACHABLE;
  if (e instanceof Error) return e.message;
  return String(e);
}

/** fetch, with a failure to get any answer turned into NetworkError (an abort stays an abort). */
async function request(url: string, init: RequestInit): Promise<Response> {
  try {
    return await fetch(url, init);
  } catch (e) {
    if (isAbort(e) || init.signal?.aborted) throw e;
    throw new NetworkError(e);
  }
}

// ---- Shapes ----

export interface ShareLib {
  /** The share-local key: the library's folder id in a library share. */
  id: string;
  name: string;
  /** Hash of the cover item, or null. */
  cover: string | null;
  total: number;
}

export interface ShareAlbumInfo {
  name: string;
  count: number;
  cover: string | null;
}

/** `shareJSON`: what the phone may know about its share. */
export interface ShareInfo {
  name: string;
  kind: 'libraries' | 'album' | string;
  capabilities: ShareCapabilities;
  /** RFC 3339; null: never expires. */
  expiresAt: string | null;
  /** A library share's libraries (empty for an album share). */
  libraries: ShareLib[];
  /** An album share's album (null for a library share). */
  album: ShareAlbumInfo | null;
}

export interface Session {
  device: { id: string; name: string };
  /** The computer's name, "" when not sent. */
  computer: string;
  share: ShareInfo;
}

/** A library share's album (GET /api/l/{lib}/albums). */
export interface ShareAlbum {
  id: number;
  name: string;
  count: number;
  cover: string | null;
}

export interface ShareLibrarySummary {
  id: string;
  name: string;
  total: number;
  images: number;
  raw: number;
  videos: number;
  earliest: string | null;
  latest: string | null;
}

/** The redacted detail (docs/design/lan-share.md "Redacted detail"). */
export interface ShareDetail extends MediaItem {
  /** Wall clock where it was taken; `takenAt` is the home time (docs/design/home-time.md). */
  localTakenAt: string | null;
  /** The local UTC offset; null when unknown. */
  offset: number | null;
  /** exif | video | gps | nearby | home; null when unknown. */
  offsetSource: string | null;
  /** The home zone's offset at that instant; null when unknown. */
  homeOffset: number | null;
  size: number;
  ext: string;
  /** Base name only. */
  fileName: string;
  cameraMake: string;
  cameraModel: string;
  lens: string;
  /** False without `download`: no 1:1 view of the original. */
  displayable: boolean;
  /** With `location` only. */
  lat: number | null;
  lon: number | null;
  /** In a library share only. */
  albums: { id: number; name: string }[];
  /** A video: how it is encoded (null when unknown), whether the server converts it, and whether its container plays in browsers. */
  video: VideoInfo | null;
  canConvert: boolean;
  browserFormat: boolean;
}

/** The video info of a detail; null unless it names a codec field. */
export function toVideoInfo(v: unknown): VideoInfo | null {
  if (!isRecord(v) || typeof v.codec !== 'string') return null;
  const out: VideoInfo = { codec: v.codec, hdr: v.hdr === true };
  for (const k of ['codecTag', 'profile', 'pixFmt', 'transfer', 'primaries', 'audio', 'container'] as const) {
    if (typeof v[k] === 'string' && v[k] !== '') out[k] = v[k];
  }
  for (const k of ['level', 'bitDepth', 'fps'] as const) {
    const n = num(v[k]);
    if (n > 0) out[k] = n;
  }
  return out;
}

/** GET /stream/{id}/status: is the conversion cached, and where a stream from t starts. */
export interface ShareStreamStatus {
  cached: boolean;
  start?: number;
}

/**
 * The filters a share accepts (a whitelist on the server; anything else is
 * 400). `rating` needs `rate`, `gps` needs `location`, `album` and `tags`
 * exist only in a library share: the server answers 400 otherwise, so the
 * phone never sends them (share/filters.ts drops them from the route).
 */
export interface ShareFilters {
  type?: 'image' | 'image_raw' | 'video';
  /** YYYY-MM-DD, wall clock. */
  from?: string;
  /** YYYY-MM-DD, wall clock, exclusive. */
  to?: string;
  /** Every year: "MM-DD" or "MM-DD..MM-DD" (inclusive, may wrap the year end). */
  recur?: string;
  /** Every year: months 1..12, sorted and unique. */
  months?: number[];
  /** Minimum rating 1..5; only with `rate`. */
  rating?: number;
  /** Library shares only: an album of the library. */
  album?: number;
  /** Exact camera model. */
  camera?: string;
  /** Only photos without a camera model ("Unknown camera"); never with `camera`. */
  nocamera?: boolean;
  /** Only photos with a location; only with `location`. */
  gps?: boolean;
  /** Library shares only: tag ids, all must match; sorted and unique. */
  tags?: number[];
  /** Bounds on the longer edge in pixels (inclusive). */
  minPx?: number;
  maxPx?: number;
}

/** GET /api/l/{lib}/facets: what the filter sheet can offer, inside the share's scope. */
export interface ShareFacets {
  total: number;
  types: { image: number; image_raw: number; video: number };
  /** Years of the dated photos, newest first. */
  years: { year: number; count: number }[];
  undated: number;
  /** Most frequent first. */
  cameras: { make: string; model: string; count: number }[];
  /** Photos without a camera model. */
  nocamera: number;
  /** With `location` only; null otherwise. */
  gps: number | null;
  /** Library shares only (empty otherwise): with live photos, by name. */
  tags: { id: number; name: string; count: number }[];
  albums: { id: number; name: string; count: number }[];
}

/** A map box: west, south, east, north (west > east crosses the antimeridian). */
export interface GeoBox {
  west: number;
  south: number;
  east: number;
  north: number;
}

/**
 * A cluster of the map (GET /api/l/{lib}/geo, only with `location`): its
 * mean position, how many photos, and its representative (the newest
 * photo) for the marker. One photo has its `id`; several have the
 * `bounds` of their positions.
 */
export interface ShareCluster {
  lat: number;
  lon: number;
  count: number;
  hash: string;
  thumbAt?: number;
  id: number | null;
  bounds: GeoBox | null;
}

// ---- Lenient parsers ----

const str = (v: unknown): string => (typeof v === 'string' ? v : '');
const instant = (v: unknown): string | null => (typeof v === 'string' && v !== '' ? v : null);
const num = (v: unknown): number => (typeof v === 'number' && Number.isFinite(v) ? v : 0);
const count = (v: unknown): number => (typeof v === 'number' && Number.isFinite(v) && v >= 0 ? Math.trunc(v) : 0);
const HASH_RE = /^[0-9a-f]{16}$/;
const hashOrNull = (v: unknown): string | null => (typeof v === 'string' && HASH_RE.test(v) ? v : null);
const positiveId = (v: unknown): number | null => (typeof v === 'number' && Number.isSafeInteger(v) && v > 0 ? v : null);

/** Only an explicit `true` grants a capability. */
export function toCapabilities(v: unknown): ShareCapabilities {
  const r = isRecord(v) ? v : {};
  return { video: r.video === true, download: r.download === true, rate: r.rate === true, location: r.location === true };
}

function toShareLib(v: unknown): ShareLib | null {
  if (!isRecord(v) || typeof v.id !== 'string' || v.id === '') return null;
  return { id: v.id, name: str(v.name) || v.id, cover: hashOrNull(v.cover), total: count(v.total ?? v.count) };
}

export function toShareLibs(v: unknown): ShareLib[] {
  const list = Array.isArray(v) ? v : isRecord(v) && Array.isArray(v.libraries) ? v.libraries : [];
  return list.map(toShareLib).filter((x): x is ShareLib => x !== null);
}

export function toShareInfo(v: unknown): ShareInfo {
  const r = isRecord(v) ? v : {};
  const a = isRecord(r.album) ? r.album : null;
  const kind = str(r.kind) || (a ? 'album' : 'libraries');
  return {
    name: str(r.name),
    kind,
    capabilities: toCapabilities(r.capabilities),
    expiresAt: instant(r.expiresAt),
    libraries: kind === 'album' ? [] : toShareLibs(r.libraries),
    album: kind === 'album' ? { name: str(a?.name), count: count(a?.count), cover: hashOrNull(a?.cover) } : null,
  };
}

/** GET /api/session's 200 body, or the 200 body of POST /api/pair (`{device, share}`). */
export function toSession(v: unknown): Session {
  const r = isRecord(v) ? v : {};
  const d = isRecord(r.device) ? r.device : {};
  return {
    device: { id: str(d.id), name: str(d.name) },
    computer: typeof r.computer === 'string' ? r.computer : isRecord(r.computer) ? str(r.computer.name) : '',
    share: toShareInfo(r.share),
  };
}

/** A list item; null when it lacks an id or hash. `rating` and `gps` survive only with their capability. */
export function toItem(v: unknown, caps: ShareCapabilities): MediaItem | null {
  if (!isRecord(v)) return null;
  const id = positiveId(v.id);
  const hash = hashOrNull(v.hash);
  if (id === null || hash === null) return null;
  const out: MediaItem = {
    id,
    hash,
    type: str(v.type) || 'image',
    takenAt: instant(v.takenAt),
    w: num(v.w),
    h: num(v.h),
    thumb: str(v.thumb) || 'ok',
  };
  if (num(v.dur) > 0) out.dur = num(v.dur);
  if (typeof v.thumbAt === 'number' && Number.isFinite(v.thumbAt)) out.thumbAt = v.thumbAt;
  if (caps.rate && num(v.rating) > 0) out.rating = Math.min(5, Math.trunc(num(v.rating)));
  if (caps.location && v.gps === true) out.gps = true;
  return out;
}

export function toDetail(v: unknown, caps: ShareCapabilities): ShareDetail {
  const r = isRecord(v) ? v : {};
  const item = toItem(r, caps) ?? { id: num(r.id), hash: str(r.hash), type: str(r.type) || 'image', takenAt: null, w: 0, h: 0, thumb: 'ok' };
  const lat = typeof r.lat === 'number' && Number.isFinite(r.lat) ? r.lat : null;
  const lon = typeof r.lon === 'number' && Number.isFinite(r.lon) ? r.lon : null;
  return {
    ...item,
    localTakenAt: 'localTakenAt' in r ? instant(r.localTakenAt) : item.takenAt,
    offset: typeof r.offset === 'number' && Number.isFinite(r.offset) ? r.offset : null,
    offsetSource: typeof r.offsetSource === 'string' && r.offsetSource !== '' ? r.offsetSource : null,
    homeOffset: typeof r.homeOffset === 'number' && Number.isFinite(r.homeOffset) ? r.homeOffset : null,
    size: count(r.size),
    ext: str(r.ext),
    fileName: str(r.fileName).replace(/^.*[\\/]/, ''),
    cameraMake: str(r.cameraMake),
    cameraModel: str(r.cameraModel),
    lens: str(r.lens),
    displayable: caps.download && r.displayable === true,
    lat: caps.location ? lat : null,
    lon: caps.location ? lon : null,
    video: toVideoInfo(r.video),
    canConvert: caps.video && r.canConvert === true,
    // Absent (an older computer): try the original; the player falls back.
    browserFormat: r.browserFormat === undefined || r.browserFormat === true,
    albums: Array.isArray(r.albums)
      ? r.albums.filter(isRecord).flatMap((a) => {
          const id = positiveId(a.id);
          return id === null ? [] : [{ id, name: str(a.name) }];
        })
      : [],
  };
}

export function toAlbums(v: unknown): ShareAlbum[] {
  const list = isRecord(v) && Array.isArray(v.albums) ? v.albums : Array.isArray(v) ? v : [];
  return list.filter(isRecord).flatMap((a) => {
    const id = positiveId(a.id);
    return id === null ? [] : [{ id, name: str(a.name), count: count(a.count), cover: hashOrNull(a.cover) }];
  });
}

function toNamedCounts(v: unknown): { id: number; name: string; count: number }[] {
  return (Array.isArray(v) ? v : []).filter(isRecord).flatMap((x) => {
    const id = positiveId(x.id);
    return id === null ? [] : [{ id, name: str(x.name) || `#${id}`, count: count(x.count) }];
  });
}

/** The facets; `gps` only with `location`, tags and albums as sent (a library share's). */
export function toFacets(v: unknown, caps: ShareCapabilities): ShareFacets {
  const r = isRecord(v) ? v : {};
  const t = isRecord(r.types) ? r.types : {};
  return {
    total: count(r.total),
    types: { image: count(t.image), image_raw: count(t.image_raw), video: count(t.video) },
    years: (Array.isArray(r.years) ? r.years : []).filter(isRecord).flatMap((y) => {
      const year = num(y.year);
      return Number.isInteger(year) && year >= 1 && year <= 9999 ? [{ year, count: count(y.count) }] : [];
    }),
    undated: count(r.undated),
    cameras: (Array.isArray(r.cameras) ? r.cameras : []).filter(isRecord).flatMap((c) => {
      const model = str(c.model);
      return model ? [{ make: str(c.make), model, count: count(c.count) }] : [];
    }),
    nocamera: count(r.nocamera),
    gps: caps.location && typeof r.gps === 'number' ? count(r.gps) : null,
    tags: toNamedCounts(r.tags),
    albums: toNamedCounts(r.albums),
  };
}

const coord = (v: unknown, limit: number): number | null => (typeof v === 'number' && Number.isFinite(v) && Math.abs(v) <= limit ? v : null);

/** The map's clusters; entries without a position, a count or a representative are dropped. */
export function toClusters(v: unknown): ShareCluster[] {
  const list = isRecord(v) && Array.isArray(v.clusters) ? v.clusters : [];
  return list.filter(isRecord).flatMap((c) => {
    const lat = coord(c.lat, 90);
    const lon = coord(c.lon, 180);
    const n = count(c.count);
    const hash = hashOrNull(c.hash);
    if (lat === null || lon === null || n < 1 || hash === null) return [];
    const b = Array.isArray(c.bounds) && c.bounds.length === 4 ? c.bounds : null;
    const w = coord(b?.[0], 180);
    const so = coord(b?.[1], 90);
    const e = coord(b?.[2], 180);
    const no = coord(b?.[3], 90);
    const out: ShareCluster = {
      lat,
      lon,
      count: n,
      hash,
      id: n === 1 ? positiveId(c.id) : null,
      bounds: n > 1 && w !== null && so !== null && e !== null && no !== null && so <= no ? { west: w, south: so, east: e, north: no } : null,
    };
    if (typeof c.thumbAt === 'number' && Number.isFinite(c.thumbAt) && c.thumbAt > 0) out.thumbAt = c.thumbAt;
    return [out];
  });
}

function toSummary(v: unknown, key: string): ShareLibrarySummary {
  const r = isRecord(v) ? v : {};
  const s = isRecord(r.summary) ? r.summary : {};
  return {
    id: str(r.id) || key,
    name: str(r.name) || str(r.id) || key,
    total: count(s.total),
    images: count(s.images),
    raw: count(s.raw),
    videos: count(s.videos),
    earliest: instant(s.earliest),
    latest: instant(s.latest),
  };
}

function toBuckets(v: unknown): TimelineBucket[] {
  const list = isRecord(v) && Array.isArray(v.buckets) ? v.buckets : [];
  return list.filter(isRecord).flatMap((b) => {
    const year = num(b.year);
    const month = num(b.month);
    const n = count(b.count);
    return Number.isInteger(year) && Number.isInteger(month) && n > 0 ? [{ year, month, count: n }] : [];
  });
}

// ---- Requests ----

/**
 * Called on every 401 (the phone is not paired, or was removed) and every
 * 403 with a share state, so the shell can replace whatever is shown.
 */
export type RefusalHandler = (status: number, state: string, message: string) => void;
let refusal: RefusalHandler | null = null;

export function setRefusalHandler(h: RefusalHandler | null): void {
  refusal = h;
}

async function errorFrom(res: Response): Promise<ShareError> {
  let msg = res.statusText || `HTTP ${res.status}`;
  let body: Record<string, unknown> | null = null;
  try {
    const text = await res.text();
    if (text) {
      try {
        const parsed: unknown = JSON.parse(text);
        if (isRecord(parsed)) body = parsed;
        if (isRecord(parsed) && typeof parsed.error === 'string') msg = parsed.error;
      } catch {
        // not JSON: keep the status text (a proxy page is not worth showing)
      }
    }
  } catch {
    // keep statusText
  }
  return new ShareError(res.status, msg, body);
}

async function send(url: string, init: RequestInit, report = true): Promise<unknown> {
  const res = await request(url, { credentials: 'same-origin', cache: 'no-store', ...init });
  if (!res.ok) {
    const e = await errorFrom(res);
    if (report && (e.status === 401 || (e.status === 403 && e.state !== ''))) refusal?.(e.status, e.state, e.message);
    throw e;
  }
  const text = await res.text();
  return text ? (JSON.parse(text) as unknown) : {};
}

const getJSON = (url: string, signal?: AbortSignal, report = true) => send(url, { headers: { Accept: 'application/json' }, signal }, report);

const postJSON = (url: string, body: unknown, signal?: AbortSignal, report = true) =>
  send(
    url,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Picmover': '1', Accept: 'application/json' },
      body: JSON.stringify(body),
      signal,
    },
    report,
  );

// ---- Media URLs that failed (an <img> or <video> error carries no status) ----

/** What a media URL answers now: its status (0: no answer) and Retry-After in ms (null: none). */
export interface UrlCheck {
  status: number;
  retryAfter: number | null;
  /** 403 `{state}` and 401 are reported to the refusal handler; this is the state ("" otherwise). */
  state: string;
}

/** Retry-After as milliseconds: delta seconds or an HTTP date; null when absent or unreadable. */
export function retryAfterMs(h: string | null, now = Date.now()): number | null {
  if (!h) return null;
  const v = h.trim();
  if (/^\d+$/.test(v)) return Number(v) * 1000;
  const t = Date.parse(v);
  return Number.isFinite(t) ? Math.max(0, t - now) : null;
}

/**
 * Asks a media URL what it answers, for an image or video that failed to
 * load: one byte of it (a Range request, and the body is dropped as soon
 * as the headers are in), so checking a video costs nothing. 401 and 403
 * `{state}` go to the refusal handler like any API answer, so the shell
 * switches to "removed" or "paused" from a failed thumbnail too.
 */
export async function checkUrl(url: string, signal?: AbortSignal): Promise<UrlCheck> {
  let res: Response;
  try {
    res = await request(url, { credentials: 'same-origin', cache: 'no-store', headers: { Range: 'bytes=0-0' }, signal });
  } catch (e) {
    if (e instanceof NetworkError) return { status: 0, retryAfter: null, state: '' };
    throw e;
  }
  const retryAfter = retryAfterMs(res.headers.get('Retry-After'));
  if (res.ok) {
    void res.body?.cancel().catch(() => {});
    return { status: res.status, retryAfter, state: '' };
  }
  const e = await errorFrom(res);
  if (e.status === 401 || (e.status === 403 && e.state !== '')) refusal?.(e.status, e.state, e.message);
  return { status: e.status, retryAfter, state: e.state };
}

/** Tries of one failed thumbnail before it stays grey. */
export const THUMB_RETRIES = 4;

/** Backoff without Retry-After: 1, 2, 4, 8 s (capped at 30 s), plus up to half a second of jitter. */
export function backoffMs(attempt: number, jitter = Math.random()): number {
  return Math.min(30_000, 1000 * 2 ** Math.max(0, attempt)) + Math.round(jitter * 500);
}

/**
 * When to load a failed thumbnail again (ms), or null to give up: on 429,
 * 503, other 5xx and no answer after Retry-After or the backoff; at once
 * when it loads fine now; never on 404 (gone), 401/403 (refused; the shell
 * takes over) or after THUMB_RETRIES. Many tiles fail together when the
 * computer rate-limits the phone, so the client checks one URL at a time
 * and holds the others until its answer (and any Retry-After) has passed.
 */
export function retrier(check: (url: string) => Promise<UrlCheck> = checkUrl, now: () => number = () => Date.now(), jitter: () => number = () => Math.random()) {
  let coolUntil = 0;
  let inflight: Promise<UrlCheck> | null = null;
  const transient = (st: number) => st === 0 || st === 408 || st === 429 || st >= 500;
  const refused = (st: number) => st === 401 || st === 403;
  return async (url: string, attempt: number): Promise<number | null> => {
    if (attempt >= THUMB_RETRIES) return null;
    if (now() < coolUntil) return coolUntil - now() + Math.round(jitter() * 500);
    if (inflight) {
      // Another tile is asking: its answer stands for this one too, except that a 404 is its own.
      const r = await inflight;
      if (refused(r.status)) return null;
      return Math.max(0, coolUntil - now()) + backoffMs(attempt, jitter());
    }
    const p = check(url);
    inflight = p;
    let r: UrlCheck;
    try {
      r = await p;
    } finally {
      if (inflight === p) inflight = null;
    }
    if (transient(r.status)) {
      const wait = r.retryAfter !== null ? Math.min(60_000, r.retryAfter) : backoffMs(attempt, jitter());
      coolUntil = now() + wait;
      return wait;
    }
    return r.status >= 200 && r.status < 300 ? 0 : null;
  };
}

/** The phone's one retrier: the rate limit is per phone, so all tiles share it. */
export const thumbRetryDelay = retrier();

/** The session; ShareError 401 when not paired, 403 `{state}` when the share refuses. Not reported to the handler. */
export async function getSession(signal?: AbortSignal): Promise<Session> {
  return toSession(await getJSON('/api/session', signal, false));
}

export interface PairBody {
  token?: string;
  code?: string;
  name: string;
  replace?: boolean;
}

/**
 * Redeems a pairing. The server may hold the request for up to a minute
 * while the computer asks for approval. Errors: 409 `{current: {share}}`
 * (already paired: ask, then resend with `replace`), 410 (expired, used,
 * not approved in time), 403 (denied), 429 (too many tries), 400.
 */
export async function pair(body: PairBody, signal?: AbortSignal): Promise<Session> {
  return toSession(await postJSON('/api/pair', body, signal, false));
}

/** A library share's libraries. */
export async function getLibraries(signal?: AbortSignal): Promise<ShareLib[]> {
  return toShareLibs(await getJSON('/api/libraries', signal));
}

const enc = encodeURIComponent;

/** Query string for the share's filters (no leading '?'); `tag` repeats. */
export function shareFilterParams(f: ShareFilters, extra?: Record<string, number>): URLSearchParams {
  const q = new URLSearchParams();
  if (f.type) q.set('type', f.type);
  if (f.from) q.set('from', f.from);
  if (f.to) q.set('to', f.to);
  if (f.recur) q.set('recur', f.recur);
  if (f.months?.length) q.set('months', f.months.join(','));
  if (f.rating) q.set('rating', String(f.rating));
  if (f.album) q.set('album', String(f.album));
  if (f.camera) q.set('camera', f.camera);
  else if (f.nocamera) q.set('nocamera', '1');
  if (f.gps) q.set('gps', '1');
  for (const t of f.tags ?? []) q.append('tag', String(t));
  if (f.minPx) q.set('minpx', String(f.minPx));
  if (f.maxPx) q.set('maxpx', String(f.maxPx));
  if (extra) for (const k of Object.keys(extra)) q.set(k, String(extra[k]));
  return q;
}

function withQuery(path: string, q: URLSearchParams): string {
  const s = q.toString();
  return s ? `${path}?${s}` : path;
}

/** "w,s,e,n", each number exactly (a cluster's bounds sent back must keep the photos on their edges). */
/** The query of /stream, as the admin client builds it. */
function streamQuery(mode: 'remux' | 'transcode', h: number, t: number): string {
  const q = new URLSearchParams({ mode });
  if (mode === 'transcode' && h > 0) q.set('h', String(Math.trunc(h)));
  if (t > 0 && Number.isFinite(t)) q.set('t', (Math.round(t * 1000) / 1000).toString());
  return q.toString();
}

function bboxParam(b: GeoBox): string {
  return [b.west, b.south, b.east, b.north].map(String).join(',');
}

interface ThumbRef {
  hash: string;
  thumbAt?: number;
}

/** One library of the share (the key `album` in an album share), with the capabilities the session granted. */
export interface ShareClient {
  readonly id: string;
  readonly caps: ShareCapabilities;
  thumbUrl(ref: ThumbRef): string;
  largeUrl(ref: ThumbRef): string;
  previewUrl(ref: ThumbRef): string;
  fileUrl(id: number, download?: boolean): string;
  /** A video converted by the computer (`video` only): remuxed, or transcoded to short side h, from t seconds. */
  streamUrl(id: number, mode: 'remux' | 'transcode', h?: number, t?: number): string;
  streamStatus(id: number, mode: 'remux' | 'transcode', h?: number, t?: number): Promise<ShareStreamStatus>;
  getLibrary(signal?: AbortSignal): Promise<ShareLibrarySummary>;
  getTimeline(f: ShareFilters, signal?: AbortSignal): Promise<TimelineBucket[]>;
  getBucket(f: ShareFilters, year: number, month: number, signal?: AbortSignal): Promise<MediaItem[]>;
  getMedia(id: number, signal?: AbortSignal): Promise<ShareDetail>;
  getAlbums(signal?: AbortSignal): Promise<ShareAlbum[]>;
  /** The filter sheet's choices within the share's scope. */
  getFacets(signal?: AbortSignal): Promise<ShareFacets>;
  /** The map's clusters of a box at a zoom (`location` only; 403 without). */
  getGeo(f: ShareFilters, box: GeoBox, zoom: number, signal?: AbortSignal): Promise<ShareCluster[]>;
  /** The photos of a box, newest first (at most `limit`), and how many there are (`location` only). */
  getGeoItems(f: ShareFilters, box: GeoBox, limit: number, signal?: AbortSignal): Promise<{ items: MediaItem[]; total: number }>;
  /** One visible hash; ShareError 409 while a move runs, 404 when gone, 403 without `rate`. */
  setRating(hash: string, rating: number): Promise<{ updated: number }>;
  /** A failed thumbnail: when to try it again (ms), or null to give up (see `retrier`). */
  retryDelay(url: string, attempt: number): Promise<number | null>;
  /** What a failed media URL (preview, original, video) answers now; see checkUrl. */
  check(url: string, signal?: AbortSignal): Promise<UrlCheck>;
}

export function shareClient(id: string, caps: ShareCapabilities): ShareClient {
  const base = `/api/l/${enc(id)}`;
  const versioned = (kind: string, ref: ThumbRef) => {
    const u = `${base}/${kind}/${enc(ref.hash)}`;
    const v = ref.thumbAt;
    return typeof v === 'number' && Number.isFinite(v) ? `${u}?v=${enc(String(Math.trunc(v)))}` : u;
  };
  return {
    id,
    caps,
    thumbUrl: (ref) => versioned('thumb', ref),
    largeUrl: (ref) => versioned('large', ref),
    previewUrl: (ref) => versioned('preview', ref),
    fileUrl: (mid, download = false) => `${base}/file/${enc(String(mid))}${download ? '?download=1' : ''}`,
    streamUrl: (mid, mode, h = 0, t = 0) => `${base}/stream/${enc(String(mid))}?${streamQuery(mode, h, t)}`,
    async streamStatus(mid, mode, h = 0, t = 0) {
      const r = await getJSON(`${base}/stream/${enc(String(mid))}/status?${streamQuery(mode, h, t)}`);
      const v = isRecord(r) ? r : {};
      return { cached: v.cached === true, start: typeof v.start === 'number' && Number.isFinite(v.start) ? v.start : undefined };
    },
    async getLibrary(signal) {
      return toSummary(await getJSON(`${base}/library`, signal), id);
    },
    async getTimeline(f, signal) {
      return toBuckets(await getJSON(withQuery(`${base}/timeline`, shareFilterParams(f)), signal));
    },
    async getBucket(f, year, month, signal) {
      const r = await getJSON(withQuery(`${base}/media`, shareFilterParams(f, { year, month })), signal);
      const list = isRecord(r) && Array.isArray(r.items) ? r.items : [];
      return list.map((x) => toItem(x, caps)).filter((x): x is MediaItem => x !== null);
    },
    async getMedia(mid, signal) {
      return toDetail(await getJSON(`${base}/media/${enc(String(mid))}`, signal), caps);
    },
    async getAlbums(signal) {
      return toAlbums(await getJSON(`${base}/albums`, signal));
    },
    async getFacets(signal) {
      return toFacets(await getJSON(`${base}/facets`, signal), caps);
    },
    async getGeo(f, box, zoom, signal) {
      const q = shareFilterParams(f);
      q.set('bbox', bboxParam(box));
      q.set('zoom', String(Math.max(0, Math.min(22, Math.round(zoom)))));
      return toClusters(await getJSON(withQuery(`${base}/geo`, q), signal));
    },
    async getGeoItems(f, box, limit, signal) {
      const q = shareFilterParams(f);
      q.set('bbox', bboxParam(box));
      q.set('limit', String(limit));
      const r = await getJSON(withQuery(`${base}/geo/items`, q), signal);
      const list = isRecord(r) && Array.isArray(r.items) ? r.items : [];
      const items = list.map((x) => toItem(x, caps)).filter((x): x is MediaItem => x !== null);
      return { items, total: Math.max(items.length, isRecord(r) ? count(r.total) : 0) };
    },
    async setRating(hash, rating) {
      const r = await postJSON(`${base}/ratings`, { hashes: [hash], rating });
      return { updated: isRecord(r) ? count(r.updated) : 0 };
    },
    retryDelay: (url, attempt) => thumbRetryDelay(url, attempt),
    check: (url, signal) => checkUrl(url, signal),
  };
}
