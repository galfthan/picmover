// Typed client for the picmover HTTP API (docs/design/server.md, "HTTP API").
// This is the only module that knows URLs. All paths are absolute (/api/...)
// so the same build works behind any local origin.

export type FileType = 'image' | 'image_raw' | 'video';
export type ThumbState = 'pending' | 'ok' | 'unsupported' | 'error' | 'stale';

/** Compact item, as returned by list endpoints. */
export interface MediaItem {
  id: number;
  /** 16 lower-case hex digits. */
  hash: string;
  type: FileType | string;
  /** Wall-clock "2024-07-14T13:05:22", null when unknown. */
  takenAt: string | null;
  w: number;
  h: number;
  /** Milliseconds; omitted when 0. */
  dur?: number;
  thumb: ThumbState | string;
  /** Omitted when false. */
  gps?: boolean;
  /** Unix seconds of the last thumbnail generation; omitted when absent. Busts the browser cache. */
  thumbAt?: number;
}

export interface MediaDetail extends MediaItem {
  relPath: string;
  ext: string;
  size: number;
  /** Seconds east of UTC, null when unknown. */
  offset: number | null;
  takenAtSource: string;
  orientation: number;
  lat?: number | null;
  lon?: number | null;
  altitude?: number | null;
  cameraMake: string;
  cameraModel: string;
  cameraType: string;
  lens: string;
  originalPath: string;
  /** RFC 3339 with zone (a real instant). */
  importedAt: string | null;
  sessionId: number | null;
  rating: number;
  phash: string | null;
  thumbState: ThumbState | string;
  thumbError: string;
  displayable: boolean;
}

export interface Camera {
  make: string;
  model: string;
  count: number;
}

export interface LibrarySummary {
  total: number;
  images: number;
  raw: number;
  videos: number;
  withGps: number;
  deleted: number;
  bytes: number;
  /** Wall-clock strings, null when the library has no dated rows. */
  earliest: string | null;
  latest: string | null;
}

export interface ThumbCounts {
  pending: number;
  ok: number;
  unsupported: number;
  error: number;
  stale?: number;
}

export interface LibraryInfo {
  root: string;
  version: string;
  summary: LibrarySummary;
  thumbs: ThumbCounts;
  cameras: Camera[];
  ffmpeg: boolean;
}

export interface TimelineBucket {
  year: number;
  month: number;
  count: number;
}

export interface GeoCluster {
  lat: number;
  lon: number;
  count: number;
  id?: number;
  hash?: string;
  /** Present only if the server sends it; the URL is unversioned otherwise. */
  thumbAt?: number;
}

export interface FsDir {
  name: string;
  path: string;
}

export interface FsListing {
  /** "" for the roots listing. */
  path: string;
  parent?: string;
  dirs: FsDir[];
  mediaFiles?: number;
  otherFiles?: number;
}

export type JobKind = 'import' | 'thumbs';
export type JobState = 'queued' | 'running' | 'done' | 'failed' | 'cancelled';

export interface JobStats {
  scanned: number;
  imported: number;
  existing: number;
  duplicates: number;
  skippedSmall: number;
  nonMedia: number;
  errors: number;
  bytesCopied: number;
  thumbsDone: number;
  thumbsUnsupported: number;
  thumbsFailed: number;
}

export interface Job {
  id: number;
  kind: JobKind | string;
  state: JobState | string;
  source?: string;
  move?: boolean;
  startedAt: string | null;
  finishedAt: string | null;
  stats: JobStats;
  sessionId?: number | null;
  logPath?: string;
  error: string | null;
  exitCode?: number | null;
}

export interface ImportJobRequest {
  kind: 'import';
  source: string;
  move: boolean;
  minDimension: number;
  timezone: string;
  noThumbs: boolean;
}

export interface ThumbsJobRequest {
  kind: 'thumbs';
  rebuild: boolean;
  limit: number;
}

export type JobRequest = ImportJobRequest | ThumbsJobRequest;

export type ResultStatus = 'imported' | 'imported_existing' | 'duplicate' | 'skipped_small' | 'error';

export interface JobResult {
  status: ResultStatus | string;
  source: string;
  dest?: string;
  message?: string;
  seq: number;
}

/** Browsing filters shared by timeline, media, geo and geo/items. */
export interface Filters {
  type?: FileType;
  gps?: boolean;
  camera?: string;
  session?: number;
  /** YYYY-MM-DD, wall clock. */
  from?: string;
  /** YYYY-MM-DD, wall clock, exclusive. */
  to?: string;
}

export interface BBox {
  west: number;
  south: number;
  east: number;
  north: number;
}

export class ApiError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

/** Human-readable message for anything thrown by this module (or fetch). */
export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof DOMException && e.name === 'AbortError') return 'Request cancelled';
  if (e instanceof Error) return e.message;
  return String(e);
}

export function isAbort(e: unknown): boolean {
  return e instanceof DOMException && e.name === 'AbortError';
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

async function errorFrom(res: Response): Promise<ApiError> {
  let msg = res.statusText || `HTTP ${res.status}`;
  try {
    const text = await res.text();
    if (text) {
      try {
        const body: unknown = JSON.parse(text);
        if (isRecord(body) && typeof body.error === 'string') msg = body.error;
        else msg = text.slice(0, 300);
      } catch {
        msg = text.slice(0, 300);
      }
    }
  } catch {
    // keep statusText
  }
  return new ApiError(res.status, msg);
}

async function getJSON<T>(url: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(url, { headers: { Accept: 'application/json' }, signal });
  if (!res.ok) throw await errorFrom(res);
  const body: unknown = await res.json();
  return body as T;
}

async function postJSON<T>(url: string, body: unknown, signal?: AbortSignal): Promise<T> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Picmover': '1', Accept: 'application/json' },
    body: JSON.stringify(body ?? {}),
    signal,
  });
  if (!res.ok) throw await errorFrom(res);
  const text = await res.text();
  return (text ? JSON.parse(text) : {}) as T;
}

/** Query string for filters (no leading '?'); empty values are left out. */
export function filterParams(f: Filters, extra?: Record<string, string | number>): URLSearchParams {
  const q = new URLSearchParams();
  if (f.type) q.set('type', f.type);
  if (f.gps) q.set('gps', '1');
  if (f.camera) q.set('camera', f.camera);
  if (f.session) q.set('session', String(f.session));
  if (f.from) q.set('from', f.from);
  if (f.to) q.set('to', f.to);
  if (extra) for (const [k, v] of Object.entries(extra)) q.set(k, String(v));
  return q;
}

function withQuery(path: string, q: URLSearchParams): string {
  const s = q.toString();
  return s ? `${path}?${s}` : path;
}

function bboxParam(b: BBox): string {
  const r = (v: number) => String(Math.round(v * 1e6) / 1e6);
  return `${r(b.west)},${r(b.south)},${r(b.east)},${r(b.north)}`;
}

// ---- Library and browsing ----

export function getLibrary(signal?: AbortSignal): Promise<LibraryInfo> {
  return getJSON<LibraryInfo>('/api/library', signal);
}

export async function getTimeline(f: Filters, signal?: AbortSignal): Promise<TimelineBucket[]> {
  const r = await getJSON<{ buckets: TimelineBucket[] | null }>(withQuery('/api/timeline', filterParams(f)), signal);
  return r.buckets ?? [];
}

export async function getBucket(f: Filters, year: number, month: number, signal?: AbortSignal): Promise<MediaItem[]> {
  const r = await getJSON<{ items: MediaItem[] | null }>(withQuery('/api/media', filterParams(f, { year, month })), signal);
  return r.items ?? [];
}

export function getMedia(id: number, signal?: AbortSignal): Promise<MediaDetail> {
  return getJSON<MediaDetail>(`/api/media/${encodeURIComponent(String(id))}`, signal);
}

const HASH_RE = /^[0-9a-f]{16}$/;

export function isHash(h: unknown): h is string {
  return typeof h === 'string' && HASH_RE.test(h);
}

/** Anything that names a thumbnail: an item, or a map cluster (which may lack thumbAt). */
export interface ThumbRef {
  hash: string;
  thumbAt?: number;
}

function versioned(kind: 'thumb' | 'preview', ref: ThumbRef): string {
  const u = `/api/${kind}/${encodeURIComponent(ref.hash)}`;
  const v = ref.thumbAt;
  return typeof v === 'number' && Number.isFinite(v) ? `${u}?v=${encodeURIComponent(String(Math.trunc(v)))}` : u;
}

/** Thumbnail URL, versioned by thumbAt so a regenerated thumbnail is refetched. */
export function thumbUrl(ref: ThumbRef): string {
  return versioned('thumb', ref);
}

/** Preview URL, versioned like thumbUrl. */
export function previewUrl(ref: ThumbRef): string {
  return versioned('preview', ref);
}

export function fileUrl(id: number, download = false): string {
  return `/api/file/${encodeURIComponent(String(id))}${download ? '?download=1' : ''}`;
}

// ---- Map ----

export async function getGeoClusters(f: Filters, bbox: BBox, zoom: number, signal?: AbortSignal): Promise<GeoCluster[]> {
  const q = filterParams(f, { bbox: bboxParam(bbox), zoom: Math.max(0, Math.min(22, Math.round(zoom))) });
  const r = await getJSON<{ clusters: GeoCluster[] | null }>(withQuery('/api/geo', q), signal);
  return r.clusters ?? [];
}

export async function getGeoItems(
  f: Filters,
  bbox: BBox,
  limit: number,
  signal?: AbortSignal,
): Promise<{ items: MediaItem[]; total: number }> {
  const q = filterParams(f, { bbox: bboxParam(bbox), limit });
  const r = await getJSON<{ items: MediaItem[] | null; total?: number }>(withQuery('/api/geo/items', q), signal);
  const items = r.items ?? [];
  return { items, total: typeof r.total === 'number' ? r.total : items.length };
}

// ---- Directory browser ----

export async function listDir(path: string, signal?: AbortSignal): Promise<FsListing> {
  const r = await getJSON<FsListing>(withQuery('/api/fs', new URLSearchParams({ path })), signal);
  return { ...r, dirs: r.dirs ?? [] };
}

// ---- Jobs ----

export async function listJobs(signal?: AbortSignal): Promise<Job[]> {
  const r = await getJSON<{ jobs: Job[] | null }>('/api/jobs', signal);
  return r.jobs ?? [];
}

/** The job object, whether the server wraps it as {job} or not. */
function unwrapJob(v: unknown): Job {
  if (isRecord(v) && isRecord(v.job)) return v.job as unknown as Job;
  return v as Job;
}

export async function startJob(req: JobRequest): Promise<Job> {
  return unwrapJob(await postJSON<unknown>('/api/jobs', req));
}

export async function getJob(id: number, signal?: AbortSignal): Promise<Job> {
  return unwrapJob(await getJSON<unknown>(`/api/jobs/${id}`, signal));
}

export async function cancelJob(id: number): Promise<void> {
  await postJSON<unknown>(`/api/jobs/${id}/cancel`, {});
}

export interface JobEventHandlers {
  onState?(job: Job): void;
  onProgress?(stats: JobStats): void;
  onResult?(r: JobResult): void;
  /** Final event; the stream is closed before this is called. */
  onDone?(job: Job): void;
  /** Connection error; the stream is closed before this is called. */
  onError?(): void;
}

function parseData(ev: Event): unknown {
  if (!(ev instanceof MessageEvent) || typeof ev.data !== 'string') return null;
  try {
    return JSON.parse(ev.data) as unknown;
  } catch {
    return null;
  }
}

/**
 * Subscribes to /api/jobs/{id}/events. The returned function closes the
 * stream. EventSource's own reconnection is disabled (the stream is closed
 * on the first error) so the caller controls retries and backoff.
 */
export function subscribeJob(id: number, h: JobEventHandlers): () => void {
  const es = new EventSource(`/api/jobs/${id}/events`);
  let closed = false;
  const close = () => {
    if (!closed) {
      closed = true;
      es.close();
    }
  };
  es.addEventListener('state', (ev) => {
    const d = parseData(ev);
    if (isRecord(d)) h.onState?.(unwrapJob(d));
  });
  es.addEventListener('progress', (ev) => {
    const d = parseData(ev);
    if (isRecord(d) && isRecord(d.stats)) h.onProgress?.(d.stats as unknown as JobStats);
  });
  es.addEventListener('result', (ev) => {
    const d = parseData(ev);
    if (isRecord(d) && typeof d.status === 'string') h.onResult?.(d as unknown as JobResult);
  });
  es.addEventListener('done', (ev) => {
    const d = parseData(ev);
    close();
    if (isRecord(d)) h.onDone?.(unwrapJob(d));
  });
  es.onerror = () => {
    if (closed) return;
    close();
    h.onError?.();
  };
  return close;
}
