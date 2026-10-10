// Typed client for the picmover HTTP API (docs/design/server.md "HTTP API",
// docs/design/management.md "HTTP API"). This is the only module that knows
// URLs. All paths are absolute (/api/...) so the same build works behind any
// local origin. Library-scoped endpoints live under /api/l/<id>/ and are
// reached through libApi(id); the workspace, file-system and job endpoints
// are global.

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
  /** 1..5; omitted when 0 (unrated). */
  rating?: number;
  /** Wall-clock string; only in trash listings. */
  deletedAt?: string | null;
}

export interface TagRef {
  id: number;
  name: string;
}

export interface Tag extends TagRef {
  /** Items carrying the tag (absent in some answers). */
  count?: number;
}

export interface AlbumRef {
  id: number;
  name: string;
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
  rating?: number;
  phash: string | null;
  thumbState: ThumbState | string;
  thumbError: string;
  displayable: boolean;
  tags?: TagRef[] | null;
  albums?: AlbumRef[] | null;
  /** Wall-clock string while the item is in the trash, null when live. */
  deletedAt?: string | null;
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

/** Counts from user.db. */
export interface UserdbCounts {
  ratings: number;
  tags: number;
  albums: number;
  trashed: number;
  ignoredPairs: number;
}

/** The last near-duplicate run; every field null before the first run. */
export interface SimilarInfo {
  /** Unix seconds, or a time string; see `formatWhen`. */
  at: number | string | null;
  distance: number | null;
  items: number | null;
  pairs: number | null;
}

export interface LibraryInfo {
  root: string;
  version: string;
  summary: LibrarySummary;
  thumbs: ThumbCounts;
  cameras: Camera[];
  ffmpeg: boolean;
  userdb?: UserdbCounts | null;
  similar?: SimilarInfo | null;
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

// ---- Workspace ----

export type LibState = 'closed' | 'open' | 'busy' | 'rebuilding' | 'error' | 'folder';

/** The process holding a library's lock. */
export interface Holder {
  pid: number;
  cmd: string;
  /** RFC 3339 instant. */
  since: string;
}

export interface Lib {
  /** The folder name; percent-encoded in URLs. */
  id: string;
  name: string;
  path: string;
  state: LibState | string;
  summary?: LibrarySummary | null;
  /** Hash of the newest item with a thumbnail. */
  cover?: string | null;
  holder?: Holder | null;
  error?: string | null;
}

/** A plain subfolder of the workspace root (no media.db); its id is its name. */
export interface FolderEntry {
  name: string;
  path: string;
}

export interface Workspace {
  root: string;
  mode: 'workspace' | 'single';
  libraries: Lib[];
  folders: FolderEntry[];
}

// ---- Albums, trash, similar ----

export interface Album {
  id: number;
  name: string;
  count: number;
  cover: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface Failure {
  hash: string;
  error: string;
}

export interface SimilarItem extends MediaItem {
  size: number;
  width: number;
  height: number;
  relPath: string;
}

export interface SimilarGroup {
  items: SimilarItem[];
  maxDistance: number;
}

export interface SimilarPage {
  groups: SimilarGroup[];
  total: number;
  at: number | string | null;
  distance: number | null;
}

// ---- Jobs ----

export type JobKind = 'import' | 'thumbs' | 'similar' | 'rebuild';
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

/** Phase-style progress (the similar job): {phase:"load", items} … {phase:"done", pairs}. */
export interface JobPhase {
  phase: string;
  items?: number;
  bands?: number;
  pairs?: number;
}

export interface Job {
  id: number;
  kind: JobKind | string;
  state: JobState | string;
  /** Id of the library the job runs in. */
  library?: string;
  source?: string;
  move?: boolean;
  startedAt: string | null;
  finishedAt: string | null;
  stats: JobStats;
  sessionId?: number | null;
  logPath?: string;
  error: string | null;
  exitCode?: number | null;
  distance?: number;
  /** Client-side: the last phase-style progress event. */
  phase?: JobPhase | null;
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

export interface SimilarJobRequest {
  kind: 'similar';
  distance?: number;
}

export interface RebuildJobRequest {
  kind: 'rebuild';
  noThumbs?: boolean;
}

export type JobRequest = ImportJobRequest | ThumbsJobRequest | SimilarJobRequest | RebuildJobRequest;

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
  /** "MM-DD" or "MM-DD..MM-DD", every year (see dates.ts). */
  recur?: string;
  /** Minimum rating 1..5. */
  rating?: number;
  /** Tag ids; all must match. */
  tags?: number[];
  album?: number;
  /** List trashed rows instead of live ones (the trash page; not in the URL). */
  trashed?: boolean;
}

export interface BBox {
  west: number;
  south: number;
  east: number;
  north: number;
}

export class ApiError extends Error {
  readonly status: number;
  /** The parsed JSON error body, when there was one. */
  readonly body: Record<string, unknown> | null;
  constructor(status: number, message: string, body: Record<string, unknown> | null = null) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.body = body;
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

export function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

async function errorFrom(res: Response): Promise<ApiError> {
  let msg = res.statusText || `HTTP ${res.status}`;
  let body: Record<string, unknown> | null = null;
  try {
    const text = await res.text();
    if (text) {
      try {
        const parsed: unknown = JSON.parse(text);
        if (isRecord(parsed)) body = parsed;
        if (isRecord(parsed) && typeof parsed.error === 'string') msg = parsed.error;
        else msg = text.slice(0, 300);
      } catch {
        msg = text.slice(0, 300);
      }
    }
  } catch {
    // keep statusText
  }
  return new ApiError(res.status, msg, body);
}

/**
 * Called when a library-scoped request finds the library not open (409) or
 * being rebuilt (503), so the shell can reopen it or show the state.
 */
export type UnavailableHandler = (lib: string, status: number, state: string | null) => void;
let unavailable: UnavailableHandler | null = null;

export function setUnavailableHandler(h: UnavailableHandler | null): void {
  unavailable = h;
}

function reportUnavailable(lib: string | undefined, e: ApiError): void {
  if (lib === undefined || !unavailable) return;
  if (e.status === 503 || (e.status === 409 && typeof e.body?.state === 'string')) {
    unavailable(lib, e.status, typeof e.body?.state === 'string' ? e.body.state : null);
  }
}

async function getJSON<T>(url: string, signal?: AbortSignal, lib?: string): Promise<T> {
  const res = await fetch(url, { headers: { Accept: 'application/json' }, signal });
  if (!res.ok) {
    const e = await errorFrom(res);
    reportUnavailable(lib, e);
    throw e;
  }
  const body: unknown = await res.json();
  return body as T;
}

async function postJSON<T>(url: string, body: unknown, signal?: AbortSignal, lib?: string): Promise<T> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Picmover': '1', Accept: 'application/json' },
    body: JSON.stringify(body ?? {}),
    signal,
  });
  if (!res.ok) {
    const e = await errorFrom(res);
    reportUnavailable(lib, e);
    throw e;
  }
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
  if (f.recur) q.set('recur', f.recur);
  if (f.rating) q.set('rating', String(f.rating));
  for (const t of f.tags ?? []) q.append('tag', String(t));
  if (f.album) q.set('album', String(f.album));
  if (f.trashed) q.set('trashed', '1');
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

const HASH_RE = /^[0-9a-f]{16}$/;

export function isHash(h: unknown): h is string {
  return typeof h === 'string' && HASH_RE.test(h);
}

/** Anything that names a thumbnail: an item, or a map cluster (which may lack thumbAt). */
export interface ThumbRef {
  hash: string;
  thumbAt?: number;
}

// ---- Chunking ----

/** Hash lists in request bodies are capped at 2000 by the server; the client sends 1000 at a time. */
export const CHUNK = 1000;

/** Splits a list into consecutive slices of at most `size` (no slices for an empty list). */
export function chunks<T>(list: readonly T[], size = CHUNK): T[][] {
  const out: T[][] = [];
  for (let i = 0; i < list.length; i += size) out.push(list.slice(i, i + size));
  return out;
}

/** Progress of a chunked operation: hashes sent so far and in total. */
export type ChunkProgress = (done: number, total: number) => void;

export interface TrashResult {
  trashed: number;
  failed: Failure[];
}
export interface RestoreResult {
  restored: number;
  failed: Failure[];
}
export interface DeleteResult {
  deleted: number;
  failed: Failure[];
}

export interface TagChange {
  add?: number[];
  remove?: number[];
  /** Names of tags to create (or reuse, case-insensitively) and add. */
  create?: string[];
}

// ---- Global URLs ----

const enc = encodeURIComponent;

/** URL prefix of a library's endpoints. */
export function libBase(id: string): string {
  return `/api/l/${enc(id)}`;
}

// ---- Library-scoped client ----

export interface LibApi {
  readonly id: string;
  getLibrary(signal?: AbortSignal): Promise<LibraryInfo>;
  getTimeline(f: Filters, signal?: AbortSignal): Promise<TimelineBucket[]>;
  getBucket(f: Filters, year: number, month: number, signal?: AbortSignal): Promise<MediaItem[]>;
  getMedia(id: number, signal?: AbortSignal): Promise<MediaDetail>;
  thumbUrl(ref: ThumbRef): string;
  previewUrl(ref: ThumbRef): string;
  fileUrl(id: number, download?: boolean): string;
  getGeoClusters(f: Filters, bbox: BBox, zoom: number, signal?: AbortSignal): Promise<GeoCluster[]>;
  getGeoItems(f: Filters, bbox: BBox, limit: number, signal?: AbortSignal): Promise<{ items: MediaItem[]; total: number }>;
  listJobs(signal?: AbortSignal): Promise<Job[]>;
  startJob(req: JobRequest): Promise<Job>;

  setRating(hashes: readonly string[], rating: number, progress?: ChunkProgress): Promise<{ updated: number }>;
  getTags(signal?: AbortSignal): Promise<Tag[]>;
  createTag(name: string): Promise<Tag>;
  renameTag(id: number, name: string): Promise<Tag>;
  deleteTag(id: number): Promise<{ removed: number }>;
  tagMedia(hashes: readonly string[], change: TagChange, progress?: ChunkProgress): Promise<{ tags: Tag[] }>;

  getAlbums(signal?: AbortSignal): Promise<Album[]>;
  createAlbum(name: string, hashes?: readonly string[], progress?: ChunkProgress): Promise<Album>;
  updateAlbum(id: number, patch: { name?: string; cover?: string | null }): Promise<Album>;
  deleteAlbum(id: number): Promise<void>;
  albumItems(id: number, change: { add?: readonly string[]; remove?: readonly string[] }, progress?: ChunkProgress): Promise<{ count: number }>;

  trash(hashes: readonly string[], progress?: ChunkProgress): Promise<TrashResult>;
  restore(hashes: readonly string[], progress?: ChunkProgress): Promise<RestoreResult>;
  deleteForever(hashes: readonly string[], progress?: ChunkProgress): Promise<DeleteResult>;
  emptyTrash(): Promise<DeleteResult>;

  getSimilar(offset: number, limit: number, signal?: AbortSignal): Promise<SimilarPage>;
  ignoreSimilar(hashes: readonly string[]): Promise<{ ignored: number }>;
}

function versioned(base: string, kind: 'thumb' | 'preview', ref: ThumbRef): string {
  const u = `${base}/${kind}/${enc(ref.hash)}`;
  const v = ref.thumbAt;
  return typeof v === 'number' && Number.isFinite(v) ? `${u}?v=${enc(String(Math.trunc(v)))}` : u;
}

function failures(v: unknown): Failure[] {
  return Array.isArray(v) ? (v.filter((x) => isRecord(x)) as unknown as Failure[]) : [];
}

const num = (v: unknown): number => (typeof v === 'number' && Number.isFinite(v) ? v : 0);

function makeLibApi(id: string): LibApi {
  const base = libBase(id);
  const get = <T>(path: string, signal?: AbortSignal) => getJSON<T>(base + path, signal, id);
  const post = <T>(path: string, body: unknown) => postJSON<T>(base + path, body, undefined, id);

  /** Sends `hashes` in chunks of CHUNK; returns every answer in order. */
  async function chunked<R>(
    hashes: readonly string[],
    progress: ChunkProgress | undefined,
    send: (part: string[]) => Promise<R>,
  ): Promise<R[]> {
    const out: R[] = [];
    let done = 0;
    for (const part of chunks(hashes)) {
      out.push(await send(part));
      done += part.length;
      progress?.(done, hashes.length);
    }
    return out;
  }

  return {
    id,
    getLibrary: (signal) => get<LibraryInfo>('/library', signal),
    async getTimeline(f, signal) {
      const r = await get<{ buckets: TimelineBucket[] | null }>(withQuery('/timeline', filterParams(f)), signal);
      return r.buckets ?? [];
    },
    async getBucket(f, year, month, signal) {
      const r = await get<{ items: MediaItem[] | null }>(withQuery('/media', filterParams(f, { year, month })), signal);
      return r.items ?? [];
    },
    getMedia: (mid, signal) => get<MediaDetail>(`/media/${enc(String(mid))}`, signal),
    thumbUrl: (ref) => versioned(base, 'thumb', ref),
    previewUrl: (ref) => versioned(base, 'preview', ref),
    fileUrl: (mid, download = false) => `${base}/file/${enc(String(mid))}${download ? '?download=1' : ''}`,
    async getGeoClusters(f, bbox, zoom, signal) {
      const q = filterParams(f, { bbox: bboxParam(bbox), zoom: Math.max(0, Math.min(22, Math.round(zoom))) });
      const r = await get<{ clusters: GeoCluster[] | null }>(withQuery('/geo', q), signal);
      return r.clusters ?? [];
    },
    async getGeoItems(f, bbox, limit, signal) {
      const q = filterParams(f, { bbox: bboxParam(bbox), limit });
      const r = await get<{ items: MediaItem[] | null; total?: number }>(withQuery('/geo/items', q), signal);
      const items = r.items ?? [];
      return { items, total: typeof r.total === 'number' ? r.total : items.length };
    },
    async listJobs(signal) {
      const r = await get<{ jobs: Job[] | null }>('/jobs', signal);
      return r.jobs ?? [];
    },
    async startJob(req) {
      return unwrapJob(await post<unknown>('/jobs', req));
    },

    async setRating(hashes, rating, progress) {
      const r = await chunked(hashes, progress, (part) => post<{ updated?: number }>('/ratings', { hashes: part, rating }));
      return { updated: r.reduce((n, x) => n + num(x.updated), 0) };
    },
    async getTags(signal) {
      const r = await get<{ tags: Tag[] | null }>('/tags', signal);
      return r.tags ?? [];
    },
    createTag: (name) => post<Tag>('/tags', { name }),
    renameTag: (tid, name) => post<Tag>(`/tags/${enc(String(tid))}`, { name }),
    async deleteTag(tid) {
      const r = await post<{ removed?: number }>(`/tags/${enc(String(tid))}/delete`, {});
      return { removed: num(r.removed) };
    },
    async tagMedia(hashes, change, progress) {
      // Tags named in `create` are created by the first chunk; later chunks
      // add them by id so the server does not look them up again.
      const add = new Set(change.add ?? []);
      let create = [...(change.create ?? [])];
      const remove = change.remove ?? [];
      const seen = new Map<number, Tag>();
      await chunked(hashes, progress, async (part) => {
        const r = await post<{ tags: Tag[] | null }>('/media/tags', { hashes: part, add: [...add], remove, create });
        for (const t of r.tags ?? []) {
          seen.set(t.id, t);
          add.add(t.id);
        }
        create = [];
        return r;
      });
      return { tags: [...seen.values()] };
    },

    async getAlbums(signal) {
      const r = await get<{ albums: Album[] | null }>('/albums', signal);
      return r.albums ?? [];
    },
    async createAlbum(name, hashes = [], progress) {
      const [first = [], ...rest] = chunks(hashes);
      let album = await post<Album>('/albums', first.length ? { name, hashes: first } : { name });
      progress?.(first.length, hashes.length);
      let done = first.length;
      for (const part of rest) {
        const r = await post<{ count?: number }>(`/albums/${enc(String(album.id))}/items`, { add: part, remove: [] });
        album = { ...album, count: num(r.count) };
        done += part.length;
        progress?.(done, hashes.length);
      }
      return album;
    },
    updateAlbum: (aid, patch) => post<Album>(`/albums/${enc(String(aid))}`, patch),
    async deleteAlbum(aid) {
      await post<unknown>(`/albums/${enc(String(aid))}/delete`, {});
    },
    async albumItems(aid, change, progress) {
      const add = change.add ?? [];
      const remove = change.remove ?? [];
      const total = add.length + remove.length;
      let count = 0;
      let done = 0;
      const path = `/albums/${enc(String(aid))}/items`;
      for (const part of chunks(add)) {
        count = num((await post<{ count?: number }>(path, { add: part, remove: [] })).count);
        done += part.length;
        progress?.(done, total);
      }
      for (const part of chunks(remove)) {
        count = num((await post<{ count?: number }>(path, { add: [], remove: part })).count);
        done += part.length;
        progress?.(done, total);
      }
      return { count };
    },

    async trash(hashes, progress) {
      const r = await chunked(hashes, progress, (part) => post<{ trashed?: number; failed?: unknown }>('/trash', { hashes: part }));
      return { trashed: r.reduce((n, x) => n + num(x.trashed), 0), failed: r.flatMap((x) => failures(x.failed)) };
    },
    async restore(hashes, progress) {
      const r = await chunked(hashes, progress, (part) => post<{ restored?: number; failed?: unknown }>('/trash/restore', { hashes: part }));
      return { restored: r.reduce((n, x) => n + num(x.restored), 0), failed: r.flatMap((x) => failures(x.failed)) };
    },
    async deleteForever(hashes, progress) {
      const r = await chunked(hashes, progress, (part) =>
        post<{ deleted?: number; failed?: unknown }>('/trash/delete', { hashes: part, confirm: true }),
      );
      return { deleted: r.reduce((n, x) => n + num(x.deleted), 0), failed: r.flatMap((x) => failures(x.failed)) };
    },
    async emptyTrash() {
      const r = await post<{ deleted?: number; failed?: unknown }>('/trash/delete', { all: true, confirm: true });
      return { deleted: num(r.deleted), failed: failures(r.failed) };
    },

    async getSimilar(offset, limit, signal) {
      const q = new URLSearchParams({ offset: String(offset), limit: String(limit) });
      const r = await get<Partial<SimilarPage> & { groups?: SimilarGroup[] | null }>(withQuery('/similar', q), signal);
      const groups = (r.groups ?? []).map((g) => ({ ...g, items: g.items ?? [] }));
      return { groups, total: num(r.total), at: r.at ?? null, distance: typeof r.distance === 'number' ? r.distance : null };
    },
    async ignoreSimilar(hashes) {
      // Not chunked: the server ignores the pairs *among* the hashes of one
      // call, and a group is far smaller than the cap.
      const r = await post<{ ignored?: number }>('/similar/ignore', { hashes });
      return { ignored: num(r.ignored) };
    },
  };
}

const clients = new Map<string, LibApi>();

/** The client for one library; the same object for the same id. */
export function libApi(id: string): LibApi {
  let c = clients.get(id);
  if (!c) {
    c = makeLibApi(id);
    clients.set(id, c);
  }
  return c;
}

// ---- Workspace ----

export async function getWorkspace(signal?: AbortSignal): Promise<Workspace> {
  const r = await getJSON<Partial<Workspace>>('/api/libraries', signal);
  return {
    root: typeof r.root === 'string' ? r.root : '',
    mode: r.mode === 'single' ? 'single' : 'workspace',
    libraries: r.libraries ?? [],
    folders: r.folders ?? [],
  };
}

export function createLibrary(name: string): Promise<Lib> {
  return postJSON<Lib>('/api/libraries', { name });
}

/** Opens a library: 200 lib; ApiError 409 (body.holder) when locked, 503 while rebuilding. */
export function openLibrary(id: string): Promise<Lib> {
  return postJSON<Lib>(`/api/libraries/${enc(id)}/open`, {});
}

/** Starts a job through the workspace route (also for folders and unreadable catalogs; rebuild only). */
export async function startWorkspaceJob(id: string, req: RebuildJobRequest): Promise<Job> {
  return unwrapJob(await postJSON<unknown>(`/api/libraries/${enc(id)}/jobs`, req));
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

export async function getJob(id: number, signal?: AbortSignal): Promise<Job> {
  return unwrapJob(await getJSON<unknown>(`/api/jobs/${id}`, signal));
}

export async function cancelJob(id: number): Promise<void> {
  await postJSON<unknown>(`/api/jobs/${id}/cancel`, {});
}

export interface JobEventHandlers {
  onState?(job: Job): void;
  onProgress?(stats: JobStats): void;
  /** Phase-style progress (the similar job). */
  onPhase?(p: JobPhase): void;
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
    if (!isRecord(d)) return;
    if (isRecord(d.stats)) h.onProgress?.(d.stats as unknown as JobStats);
    // The similar job reports {phase, items|bands|pairs}, at the top level or under `progress`.
    const p = isRecord(d.progress) ? d.progress : d;
    if (typeof p.phase === 'string') h.onPhase?.(p as unknown as JobPhase);
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
