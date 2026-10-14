// Client for the share listener (docs/design/lan-share.md "Share listener:
// routes"), the only module of the phone app that knows URLs. It imports
// nothing at run time from the admin client (lib/api.ts): a module shared
// by both Vite entries lands in a common chunk whole, and the phone must
// not download the admin API. Answers are parsed leniently (missing fields
// defaulted, malformed entries dropped) because the server and this client
// were written in parallel against the design; a capability the session
// does not grant is off, never defaulted on.

import type { MediaItem, ShareCapabilities, ShareCapability, TimelineBucket } from '../lib/api';

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

/** Text for anything this module (or fetch) throws; a failed fetch is the network. */
export function errorText(e: unknown): string {
  if (e instanceof ShareError) return e.message;
  if (e instanceof TypeError) return 'Cannot reach the computer. Is it on, and is this phone on the same Wi-Fi?';
  if (e instanceof Error) return e.message;
  return String(e);
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
  offset: number | null;
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
}

/** The filters a share accepts (a whitelist on the server; anything else is 400). */
export interface ShareFilters {
  type?: 'image' | 'image_raw' | 'video';
  /** YYYY-MM-DD, wall clock. */
  from?: string;
  /** YYYY-MM-DD, wall clock, exclusive. */
  to?: string;
  /** Minimum rating 1..5; only with `rate`. */
  rating?: number;
  /** Library shares only: an album of the library. */
  album?: number;
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
    offset: typeof r.offset === 'number' && Number.isFinite(r.offset) ? r.offset : null,
    size: count(r.size),
    ext: str(r.ext),
    fileName: str(r.fileName).replace(/^.*[\\/]/, ''),
    cameraMake: str(r.cameraMake),
    cameraModel: str(r.cameraModel),
    lens: str(r.lens),
    displayable: caps.download && r.displayable === true,
    lat: caps.location ? lat : null,
    lon: caps.location ? lon : null,
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
  const res = await fetch(url, { credentials: 'same-origin', cache: 'no-store', ...init });
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

/** Query string for the share's filters (no leading '?'). */
export function shareFilterParams(f: ShareFilters, extra?: Record<string, number>): URLSearchParams {
  const q = new URLSearchParams();
  if (f.type) q.set('type', f.type);
  if (f.from) q.set('from', f.from);
  if (f.to) q.set('to', f.to);
  if (f.rating) q.set('rating', String(f.rating));
  if (f.album) q.set('album', String(f.album));
  if (extra) for (const k of Object.keys(extra)) q.set(k, String(extra[k]));
  return q;
}

function withQuery(path: string, q: URLSearchParams): string {
  const s = q.toString();
  return s ? `${path}?${s}` : path;
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
  getLibrary(signal?: AbortSignal): Promise<ShareLibrarySummary>;
  getTimeline(f: ShareFilters, signal?: AbortSignal): Promise<TimelineBucket[]>;
  getBucket(f: ShareFilters, year: number, month: number, signal?: AbortSignal): Promise<MediaItem[]>;
  getMedia(id: number, signal?: AbortSignal): Promise<ShareDetail>;
  getAlbums(signal?: AbortSignal): Promise<ShareAlbum[]>;
  /** One visible hash; ShareError 409 while a move runs, 404 when gone, 403 without `rate`. */
  setRating(hash: string, rating: number): Promise<{ updated: number }>;
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
    async setRating(hash, rating) {
      const r = await postJSON(`${base}/ratings`, { hashes: [hash], rating });
      return { updated: isRecord(r) ? count(r.updated) : 0 };
    },
  };
}
