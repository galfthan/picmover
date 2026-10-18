// Typed client for the picmover HTTP API (docs/design/server.md "HTTP API",
// docs/design/management.md "HTTP API"). This is the only module that knows
// URLs. All paths are absolute (/api/...) so the same build works behind any
// local origin. Library-scoped endpoints live under /api/l/<id>/ and are
// reached through libApi(id); the workspace, file-system and job endpoints
// are global.

import type { VideoInfo } from './playback';

export type FileType = 'image' | 'image_raw' | 'video';
export type ThumbState = 'pending' | 'ok' | 'unsupported' | 'error' | 'stale';

/** Compact item, as returned by list endpoints. */
export interface MediaItem {
  id: number;
  /** 16 lower-case hex digits. */
  hash: string;
  type: FileType | string;
  /**
   * Home time: the wall clock of the library's home zone, "2024-07-14T13:05:22";
   * null when unknown. The timeline orders and groups by it.
   */
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
  /** Wall clock where it was taken (null when undated). */
  localTakenAt?: string | null;
  /** Its offset, seconds east of UTC; null when unknown. */
  offset: number | null;
  /** Where the offset came from: exif | video | gps | nearby | home; null when unknown. */
  offsetSource?: string | null;
  /** The home zone's offset at that instant; null when it or the local offset is unknown. */
  homeOffset?: number | null;
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
  /** The import records that brought the item in (user.db), oldest first as the server lists them. */
  imports?: ImportRef[] | null;
  /** Wall-clock string while the item is in the trash, null when live. */
  deletedAt?: string | null;
  /** How a video is encoded; null for images and when unknown (absent from older servers). */
  video?: VideoInfo | null;
  /** The server has ffmpeg: /stream can remux or transcode a video. */
  canConvert?: boolean;
  /** "Open original" works for this client (it runs on the server's computer). */
  canOpen?: boolean;
}

/** GET /stream/{id}/status. */
export interface StreamStatus {
  /** The complete conversion is cached: served with Range support (seekable). */
  cached: boolean;
  encoding: boolean;
  bytes: number;
  encoder?: string;
  encoders: string[];
  /** The short side produced (0: source size). */
  tier: number;
  /** Where a stream asked for with t really starts (a remux starts at the keyframe before t); absent from older servers. */
  start?: number;
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
  /** Rows with a preview but no large (640 px) rendition; absent from older servers. */
  largeMissing?: number;
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
  /** The timeline's time zone; name null when none is set. */
  homeZone?: HomeZone | null;
}

export interface HomeZone {
  /** IANA zone name, null when not set. */
  name: string | null;
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
  /** The holder's command ("import", "serve"); "" when unknown. */
  cmd: string;
  /** Wall-clock string in the server's zone ("2026-10-10T09:00:00"); "" when unknown. */
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
  /** Backup summary, when the server provides it (step 5); absent otherwise. */
  backup?: LibBackup | null;
  /** Unfinished moves involving this library (docs/design/library-move.md); absent when the server does not say. */
  interruptedMoves?: InterruptedMove[] | null;
}

/** The workspace listing's per-library backup summary (docs/design/backups.md "Frontend"). */
export interface LibBackup {
  /** Newest backup over all targets: Unix seconds, RFC 3339 or a wall-clock string; null when never. */
  lastBackupAt?: number | string | null;
  /** Files not backed up yet (a count, or the `behind` object of a set). */
  behind?: number | BackupBehind | null;
  /** Number of targets (or the list of them). */
  targets?: number | unknown[] | null;
  /** Label of the target of the newest backup, when sent. */
  label?: string | null;
}

/** A plain subfolder of the workspace root (no media.db); its id is its name. */
export interface FolderEntry {
  name: string;
  path: string;
  /** Sent by the server (= name, state "folder"); the client keys folders by `name`. */
  id?: string;
  state?: string;
}

export interface Workspace {
  root: string;
  /** "unset": the desktop app runs without a workspace folder yet (docs/design/desktop-shell.md). */
  mode: 'workspace' | 'single' | 'unset';
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

// ---- Import history (docs/design/imports.md) ----

export type ImportMode = 'copy' | 'move' | 'rebuild' | 'record' | 'transfer';
export type ImportOutcome = 'running' | 'done' | 'cancelled' | 'failed';

/** One import record of user.db, as GET imports and GET imports/<iid> answer. */
export interface ImportRecord {
  id: number;
  /** User-given; "" when unnamed. */
  name: string;
  /** RFC 3339 instants; finishedAt null while running or if never finished. */
  startedAt: string | null;
  finishedAt: string | null;
  /** The path given to import. */
  source: string;
  /** Its base name, for lists. */
  sourceName: string;
  mode: ImportMode | string;
  host: string;
  outcome: ImportOutcome | string;
  imported: number;
  existing: number;
  duplicates: number;
  skipped: number;
  errors: number;
  bytes: number;
  /**
   * Item rows (imported, existing, restored): in the list the number stored
   * when the record was finished, for one record (getImport) those that are
   * live rows now. null for a record that keeps no items (a rebuild: mode
   * rebuild or record covers the whole library).
   */
  items: number | null;
  /** Hash of the cover item (16 hex digits; stored in the list, live for one record), null when none. */
  cover: string | null;
}

/** An import record as the media detail lists it. */
export interface ImportRef {
  id: number;
  name: string;
  startedAt: string | null;
  mode: ImportMode | string;
  /** The source's base name (the server sends it; older ones did not). */
  sourceName?: string;
}

const str = (v: unknown): string => (typeof v === 'string' ? v : '');
const instant = (v: unknown): string | null => (typeof v === 'string' && v !== '' ? v : null);

/** An import record with every field present (missing ones defaulted); a 0, "" or malformed cover is null. */
export function toImport(v: unknown): ImportRecord {
  const r = isRecord(v) ? (isRecord(v.import) ? v.import : v) : {};
  return {
    id: num(r.id),
    name: str(r.name),
    startedAt: instant(r.startedAt),
    finishedAt: instant(r.finishedAt),
    source: str(r.source),
    sourceName: str(r.sourceName),
    mode: str(r.mode),
    host: str(r.host),
    outcome: str(r.outcome),
    imported: num(r.imported),
    existing: num(r.existing),
    duplicates: num(r.duplicates),
    skipped: num(r.skipped),
    errors: num(r.errors),
    bytes: num(r.bytes),
    items: typeof r.items === 'number' && Number.isFinite(r.items) ? r.items : null,
    cover: isHash(r.cover) ? r.cover : null,
  };
}

/** The media detail's `imports`, dropping malformed entries. */
export function toImportRefs(v: unknown): ImportRef[] {
  if (!Array.isArray(v)) return [];
  return v.filter(isRecord).flatMap((r) => {
    const id = num(r.id);
    if (!Number.isSafeInteger(id) || id <= 0) return [];
    const ref: ImportRef = { id, name: str(r.name), startedAt: instant(r.startedAt), mode: str(r.mode) };
    if (typeof r.sourceName === 'string' && r.sourceName !== '') ref.sourceName = r.sourceName;
    return [ref];
  });
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

// ---- Backups (docs/design/backups.md "HTTP API") ----

/** What the library has that the set lacks (from the cached plan). */
export interface BackupBehind {
  new: number;
  changed: number;
  moved: number;
  bytes: number;
}

/** One library's copy on one target, as summarized by GET …/backups. */
export interface BackupSet {
  /** The set folder's name on the target ("" when not sent). */
  name: string;
  files: number;
  bytes: number;
  /** null until a plan has been computed (or the target is not reachable). */
  behind: BackupBehind | null;
  extras: number;
  damaged: number;
  unverified: number;
}

export interface BackupTarget {
  /** Marker id, 16 hex digits. */
  id: string;
  label: string;
  /** Where the target was last seen on this machine. */
  path: string;
  reachable: boolean;
  /** Unix seconds, RFC 3339 or a wall-clock string (see `parseWhen`); null when never. */
  lastBackupAt: number | string | null;
  lastScrubAt: number | string | null;
  /** null when the target holds no set of this library yet (or is not reachable). */
  set: BackupSet | null;
  /** Why the set cannot be shown or used (reachable targets only), e.g. the set name holds another library's backup. */
  error?: string;
  /**
   * The set was last written by another copy of this same library (the
   * folder was copied or moved to another machine); `error` names it. A
   * backup with `takeOver` makes this library the set's writer.
   */
  foreign?: boolean;
}

export type PlanClassName = 'current' | 'moved' | 'changed' | 'new' | 'extra';
export const PLAN_CLASSES: readonly PlanClassName[] = ['new', 'changed', 'moved', 'extra', 'current'];

export interface PlanClass {
  count: number;
  bytes: number;
  /** Up to 500 example paths (relative to the set), as sent. */
  paths: string[];
}

export type BackupPlan = Record<PlanClassName, PlanClass> & {
  /** What a restore `missing` copies back (set rows missing from the library); absent from older servers. */
  restore?: PlanClass;
};

export type BackupRunKind = 'backup' | 'scrub' | 'prune' | 'restore';

export interface BackupRun {
  id: number;
  kind: BackupRunKind | string;
  startedAt: number | string | null;
  finishedAt: number | string | null;
  /** The run's counters as recorded in backup.db (`runs.stats`); see backupinfo.ts for the fields read. */
  stats: Record<string, unknown>;
}

/** Progress of a backup, scrub, prune or restore job (SSE `progress`, or the job's `progress`). */
export interface BackupProgressEvent {
  phase: 'plan' | 'copy' | 'snapshot' | 'scrub' | 'prune' | 'restore' | string;
  files?: number;
  bytes?: number;
  totalFiles?: number;
  totalBytes?: number;
  /** The file being worked on (relative path). */
  current?: string;
  /** Files that failed so far (a count). */
  failed?: number;
  /** Bytes per second since the phase began (server-computed). */
  rate?: number;
  /** Seconds left (server-computed); -1 when unknown. */
  eta?: number;
}

// ---- Jobs ----

export type JobKind = 'import' | 'thumbs' | 'similar' | 'rebuild' | 'transfer' | BackupRunKind;
export const BACKUP_KINDS: readonly string[] = ['backup', 'scrub', 'prune', 'restore'];
export const isBackupKind = (k: string): k is BackupRunKind => BACKUP_KINDS.includes(k);
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
  /** Transfer rate in bytes per second; 0 when nothing is moving. */
  rate: number;
  thumbsDone: number;
  thumbsUnsupported: number;
  thumbsFailed: number;
  /** Large (640 px) renditions derived from stored previews by a thumbs job. */
  largeDone: number;
}

/**
 * Phase-style progress: the similar job's {phase:"load", items} …
 * {phase:"done", pairs}, and the backup kinds' {phase, files, bytes,
 * totalFiles, totalBytes, current, failed}.
 */
export interface JobPhase extends Omit<BackupProgressEvent, 'phase'> {
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
  /** user.db import record of an import job (docs/design/imports.md); absent or null when none was written. */
  importId?: number | null;
  logPath?: string;
  error: string | null;
  exitCode?: number | null;
  distance?: number;
  /** Target id of a backup-kind job, when the server sends it. */
  target?: string;
  /** Restore mode of a restore job. */
  mode?: string;
  /** Target library id of a transfer job (`library` is the source). */
  to?: string;
  /** Run stats of a finished backup-kind job (absent before it finishes); `stats` stays the importer's counters. */
  result?: Record<string, unknown>;
  /** Server-side phase progress of a running similar job (also in listings). */
  progress?: JobPhase | null;
  /** Client-side: the last phase-style progress event (or `progress`). */
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

export interface BackupJobRequest {
  kind: 'backup';
  target: string;
  partial?: boolean;
  workers?: number;
  /** Take over a set last written by another copy of this library (`foreign` targets). */
  takeOver?: boolean;
}

export interface ScrubJobRequest {
  kind: 'scrub';
  target: string;
  /** Seconds; omitted for the whole target. */
  budget?: number;
  repair?: boolean;
}

export interface PruneJobRequest {
  kind: 'prune';
  target: string;
  keepDays?: number;
  confirm: true;
  /** The extras count of the plan the user confirmed. */
  expected: number;
}

export interface RestoreJobRequest {
  kind: 'restore';
  target: string;
  mode: 'missing' | 'user-data';
}

export type BackupJobKindRequest = BackupJobRequest | ScrubJobRequest | PruneJobRequest | RestoreJobRequest;

/** Moves `hashes` (at most TRANSFER_CHUNK) from the job's library to library `to`. */
export interface TransferJobRequest {
  kind: 'transfer';
  to: string;
  hashes: string[];
}

export type JobRequest = ImportJobRequest | ThumbsJobRequest | SimilarJobRequest | RebuildJobRequest | BackupJobKindRequest | TransferJobRequest;

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
  /** Months 1..12 of any year (`months=1,2,7`), sorted and unique (see dates.ts). */
  months?: number[];
  /** Longer edge, MAX(width, height), at least this many pixels (1..1 000 000; `minpx`). Unknown dimensions never match. */
  minPx?: number;
  /** Longer edge at most this many pixels (1..1 000 000; `maxpx`). Unknown dimensions never match. */
  maxPx?: number;
  /** Minimum rating 1..5. */
  rating?: number;
  /** Tag ids; all must match. */
  tags?: number[];
  album?: number;
  /** An import record of user.db (`import=<iid>`). */
  import?: number;
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
  if (f.months?.length) q.set('months', f.months.join(','));
  if (f.minPx) q.set('minpx', String(f.minPx));
  if (f.maxPx) q.set('maxpx', String(f.maxPx));
  if (f.rating) q.set('rating', String(f.rating));
  for (const t of f.tags ?? []) q.append('tag', String(t));
  if (f.album) q.set('album', String(f.album));
  if (f.import) q.set('import', String(f.import));
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
  /** Sets the home zone (an IANA name); every home time is recomputed. */
  setHomeZone(zone: string): Promise<HomeZone>;
  getTimeline(f: Filters, signal?: AbortSignal): Promise<TimelineBucket[]>;
  getBucket(f: Filters, year: number, month: number, signal?: AbortSignal): Promise<MediaItem[]>;
  getMedia(id: number, signal?: AbortSignal): Promise<MediaDetail>;
  thumbUrl(ref: ThumbRef): string;
  previewUrl(ref: ThumbRef): string;
  /** The 640 px rendition (derived on demand from the preview by the server). */
  largeUrl(ref: ThumbRef): string;
  fileUrl(id: number, download?: boolean): string;
  /** A video converted by the server: remuxed, or transcoded to short side h (0: source size), from t seconds. */
  streamUrl(id: number, mode: 'remux' | 'transcode', h?: number, t?: number): string;
  /** The conversion's state; with t, also where a stream from t really starts (`start`). */
  streamStatus(id: number, mode: 'remux' | 'transcode', h?: number, t?: number, signal?: AbortSignal): Promise<StreamStatus>;
  /** Opens the original in its default program on the server's computer (canOpen). */
  openOriginal(id: number): Promise<void>;
  getGeoClusters(f: Filters, bbox: BBox, zoom: number, signal?: AbortSignal): Promise<GeoCluster[]>;
  getGeoItems(f: Filters, bbox: BBox, limit: number, signal?: AbortSignal): Promise<{ items: MediaItem[]; total: number }>;
  startJob(req: JobRequest): Promise<Job>;

  setRating(hashes: readonly string[], rating: number, progress?: ChunkProgress): Promise<{ updated: number }>;
  getTags(signal?: AbortSignal): Promise<Tag[]>;
  tagMedia(hashes: readonly string[], change: TagChange, progress?: ChunkProgress): Promise<{ tags: Tag[] }>;

  getAlbums(signal?: AbortSignal): Promise<Album[]>;
  createAlbum(name: string, hashes?: readonly string[], progress?: ChunkProgress): Promise<Album>;
  updateAlbum(id: number, patch: { name?: string; cover?: string | null }): Promise<Album>;
  deleteAlbum(id: number): Promise<void>;
  albumItems(id: number, change: { add?: readonly string[]; remove?: readonly string[] }, progress?: ChunkProgress): Promise<{ count: number }>;

  /** Import records, newest first. */
  getImports(signal?: AbortSignal): Promise<ImportRecord[]>;
  getImport(id: number, signal?: AbortSignal): Promise<ImportRecord>;
  /** Renames a record ("" clears the name). */
  renameImport(id: number, name: string): Promise<void>;
  /** Deletes the record only; the files stay. */
  deleteImport(id: number): Promise<void>;

  trash(hashes: readonly string[], progress?: ChunkProgress): Promise<TrashResult>;
  restore(hashes: readonly string[], progress?: ChunkProgress): Promise<RestoreResult>;
  deleteForever(hashes: readonly string[], progress?: ChunkProgress): Promise<DeleteResult>;
  emptyTrash(): Promise<DeleteResult>;

  getSimilar(offset: number, limit: number, signal?: AbortSignal): Promise<SimilarPage>;
  /** Ignores the whole current group that contains `hash` (any size; the server finds the group). */
  ignoreSimilarGroup(hash: string): Promise<{ ignored: number }>;

  /** The library's targets; `plan` asks the server to recompute `behind` now. */
  getBackups(plan?: boolean, signal?: AbortSignal): Promise<BackupTarget[]>;
  addTarget(path: string, label: string): Promise<BackupTarget>;
  updateTarget(id: string, patch: { label?: string; path?: string }): Promise<BackupTarget>;
  forgetTarget(id: string): Promise<void>;
  getPlan(id: string, signal?: AbortSignal): Promise<BackupPlan>;
  getRuns(id: string, signal?: AbortSignal): Promise<BackupRun[]>;

  /**
   * The pre-flight of moving `hashes` to library `to` (no writes). More than
   * TRANSFER_CHUNK hashes are planned per chunk and summed.
   */
  transferPlan(to: string, hashes: readonly string[], progress?: ChunkProgress, signal?: AbortSignal): Promise<TransferPlan>;
}

function versioned(base: string, kind: 'thumb' | 'large' | 'preview', ref: ThumbRef): string {
  const u = `${base}/${kind}/${enc(ref.hash)}`;
  const v = ref.thumbAt;
  return typeof v === 'number' && Number.isFinite(v) ? `${u}?v=${enc(String(Math.trunc(v)))}` : u;
}

/** The query of /stream: mode, h (transcode only, when not 0) and t (when > 0, millisecond precision). */
function streamQuery(mode: 'remux' | 'transcode', h: number, t: number): string {
  const q = new URLSearchParams({ mode });
  if (mode === 'transcode' && h > 0) q.set('h', String(Math.trunc(h)));
  if (t > 0 && Number.isFinite(t)) q.set('t', (Math.round(t * 1000) / 1000).toString());
  return q.toString();
}

function failures(v: unknown): Failure[] {
  return Array.isArray(v) ? (v.filter((x) => isRecord(x)) as unknown as Failure[]) : [];
}

const num = (v: unknown): number => (typeof v === 'number' && Number.isFinite(v) ? v : 0);

/** Record ids are positive integers; anything else never reaches a URL. */
function encId(id: number): string {
  if (!Number.isSafeInteger(id) || id <= 0) throw new ApiError(400, `invalid id ${JSON.stringify(id)}`);
  return String(id);
}

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
    setHomeZone: (zone) => post<HomeZone>('/settings', { homeZone: zone }),
    async getTimeline(f, signal) {
      const r = await get<{ buckets: TimelineBucket[] | null }>(withQuery('/timeline', filterParams(f)), signal);
      return r.buckets ?? [];
    },
    async getBucket(f, year, month, signal) {
      const r = await get<{ items: MediaItem[] | null }>(withQuery('/media', filterParams(f, { year, month })), signal);
      return r.items ?? [];
    },
    async getMedia(mid, signal) {
      const d = await get<MediaDetail>(`/media/${enc(String(mid))}`, signal);
      return 'imports' in d ? { ...d, imports: toImportRefs(d.imports) } : d;
    },
    thumbUrl: (ref) => versioned(base, 'thumb', ref),
    previewUrl: (ref) => versioned(base, 'preview', ref),
    largeUrl: (ref) => versioned(base, 'large', ref),
    fileUrl: (mid, download = false) => `${base}/file/${enc(String(mid))}${download ? '?download=1' : ''}`,
    streamUrl: (mid, mode, h = 0, t = 0) => `${base}/stream/${encId(mid)}?${streamQuery(mode, h, t)}`,
    streamStatus: (mid, mode, h = 0, t = 0, signal) => get<StreamStatus>(`/stream/${encId(mid)}/status?${streamQuery(mode, h, t)}`, signal),
    async openOriginal(mid) {
      await post<unknown>(`/media/${encId(mid)}/open`, {});
    },
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

    async getImports(signal) {
      const r = await get<{ imports?: unknown }>('/imports', signal);
      return Array.isArray(r.imports) ? r.imports.filter(isRecord).map(toImport) : [];
    },
    async getImport(iid, signal) {
      return toImport(await get<unknown>(`/imports/${encId(iid)}`, signal));
    },
    async renameImport(iid, name) {
      await post<unknown>(`/imports/${encId(iid)}`, { name });
    },
    async deleteImport(iid) {
      await post<unknown>(`/imports/${encId(iid)}/delete`, {});
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
    async ignoreSimilarGroup(hash) {
      // The group form: the server ignores every pair of the current group
      // containing `hash`, so groups larger than the hash-list cap work too.
      const r = await post<{ ignored?: number }>('/similar/ignore', { group: hash });
      return { ignored: num(r.ignored) };
    },

    async getBackups(plan = false, signal) {
      const r = await get<{ targets?: unknown }>(plan ? '/backups?plan=1' : '/backups', signal);
      return Array.isArray(r.targets) ? r.targets.filter(isRecord).map(toTarget) : [];
    },
    async addTarget(path, label) {
      return toTarget(await post<unknown>('/backups/targets', { path, label }));
    },
    async updateTarget(tid, patch) {
      return toTarget(await post<unknown>(`/backups/targets/${encTarget(tid)}`, patch));
    },
    async forgetTarget(tid) {
      await post<unknown>(`/backups/targets/${encTarget(tid)}/forget`, {});
    },
    async getPlan(tid, signal) {
      return toPlan(await get<unknown>(`/backups/targets/${encTarget(tid)}/plan`, signal));
    },
    async getRuns(tid, signal) {
      const r = await get<{ runs?: unknown }>(`/backups/targets/${encTarget(tid)}/runs`, signal);
      return Array.isArray(r.runs) ? r.runs.filter(isRecord).map(toRun) : [];
    },

    async transferPlan(to, hashes, progress, signal) {
      const parts: TransferPlan[] = [];
      let done = 0;
      for (const part of chunks(hashes, TRANSFER_CHUNK)) {
        parts.push(toTransferPlan(await postJSON<unknown>(`${base}/transfer/plan`, { to, hashes: part }, signal, id)));
        done += part.length;
        progress?.(done, hashes.length);
      }
      return sumPlans(parts);
    },
  };
}

// ---- Moving between libraries (docs/design/library-move.md) ----

/** Hashes per transfer plan request and per transfer job (the server's cap). */
export const TRANSFER_CHUNK = 2000;

/** A move whose journal has no `end`: it must be finished (resumed in `to`) before another move involving either library. */
export interface InterruptedMove {
  /** The source library; '' when the journal cannot be read. */
  from: string;
  to: string;
  /** The target's import record of the move. */
  id: number;
  /** Why it cannot be finished now (the source is not in the workspace, the journal cannot be read); '' when "Finish moving" can. */
  problem: string;
}

export interface TransferPlan {
  /** Not in the target yet: placed there (renamed on one volume, copied across volumes). */
  move: { count: number; bytes: number };
  /** Already live in the target: this library's copy goes to its trash. */
  existing: { count: number };
  /** In the target's trash: restored there, this library's copy goes to its trash. */
  restore: { count: number };
  /** Not movable (trashed here, not in the catalog, bad path), with the reason. */
  refused: Failure[];
  /** Both libraries on one volume: a move is a rename. */
  sameVolume: boolean;
  /** Free bytes on the target's volume (other volumes only); null when unknown or not asked. */
  free: number | null;
  interrupted: InterruptedMove[];
}

export interface TransferProgress {
  phase: 'plan' | 'move' | 'recover' | string;
  files: number;
  totalFiles: number;
  bytes: number;
  totalBytes: number;
  current: string;
}

export interface TransferResult {
  moved: number;
  existing: number;
  restored: number;
  /** At most 200 entries; `failedCount` is the total. */
  failed: Failure[];
  failedCount: number;
  /** Photos now in the target whose original could not be moved to the trash here: they are still in this library too. */
  sourcePending: number;
  /** The target's import record of the move; null when none. */
  record: number | null;
  targetLibrary: string;
}

const count = (v: unknown): number => (typeof v === 'number' && Number.isFinite(v) && v >= 0 ? v : 0);
const positiveId = (v: unknown): number | null => (typeof v === 'number' && Number.isSafeInteger(v) && v > 0 ? v : null);

/** `{hash, error}` entries with a valid hash; anything else is dropped. */
function hashFailures(v: unknown): Failure[] {
  if (!Array.isArray(v)) return [];
  return v.filter(isRecord).flatMap((f) => (isHash(f.hash) ? [{ hash: f.hash, error: typeof f.error === 'string' ? f.error : '' }] : []));
}

/**
 * Interrupted moves (plan `interrupted`, lib `interruptedMoves`); malformed
 * entries are dropped. One with a `problem` may lack `from` (its journal
 * cannot be read) and have the id 0.
 */
export function toInterrupted(v: unknown): InterruptedMove[] {
  if (!Array.isArray(v)) return [];
  return v.filter(isRecord).flatMap((m) => {
    const problem = typeof m.problem === 'string' ? m.problem : '';
    const id = positiveId(m.id) ?? (problem ? 0 : null);
    const from = typeof m.from === 'string' ? m.from : '';
    if ((from === '' && !problem) || typeof m.to !== 'string' || m.to === '' || id === null) return [];
    return [{ from, to: m.to, id, problem }];
  });
}

/** The plan answer with every field present; missing or malformed values are zero, false, null or empty. */
export function toTransferPlan(v: unknown): TransferPlan {
  const r = isRecord(v) ? v : {};
  const move = isRecord(r.move) ? r.move : {};
  const existing = isRecord(r.existing) ? r.existing : {};
  const restore = isRecord(r.restore) ? r.restore : {};
  return {
    move: { count: count(move.count), bytes: count(move.bytes) },
    existing: { count: count(existing.count) },
    restore: { count: count(restore.count) },
    refused: hashFailures(r.refused),
    sameVolume: r.sameVolume === true,
    free: typeof r.free === 'number' && Number.isFinite(r.free) && r.free >= 0 ? r.free : null,
    interrupted: toInterrupted(r.interrupted),
  };
}

/** Chunked plans as one: counts and bytes summed, `sameVolume` and `free` from the first, lists concatenated (interrupted moves once). */
export function sumPlans(parts: readonly TransferPlan[]): TransferPlan {
  const first = parts[0];
  const seen = new Set<string>();
  const interrupted = parts.flatMap((p) => p.interrupted).filter((m) => {
    const k = `${m.from}\u0000${m.to}\u0000${m.id}`;
    if (seen.has(k)) return false;
    seen.add(k);
    return true;
  });
  return {
    move: { count: parts.reduce((n, p) => n + p.move.count, 0), bytes: parts.reduce((n, p) => n + p.move.bytes, 0) },
    existing: { count: parts.reduce((n, p) => n + p.existing.count, 0) },
    restore: { count: parts.reduce((n, p) => n + p.restore.count, 0) },
    refused: parts.flatMap((p) => p.refused),
    sameVolume: first?.sameVolume ?? false,
    free: first?.free ?? null,
    interrupted,
  };
}

/** A transfer job's `progress`; null when absent or without a phase. */
export function toTransferProgress(v: unknown): TransferProgress | null {
  if (!isRecord(v) || typeof v.phase !== 'string') return null;
  return {
    phase: v.phase,
    files: count(v.files),
    totalFiles: count(v.totalFiles),
    bytes: count(v.bytes),
    totalBytes: count(v.totalBytes),
    current: typeof v.current === 'string' ? v.current : '',
  };
}

/** A finished transfer job's `result`; null before it finishes (absent or not an object). */
export function toTransferResult(v: unknown): TransferResult | null {
  if (!isRecord(v)) return null;
  const failed = hashFailures(v.failed);
  return {
    moved: count(v.moved),
    existing: count(v.existing),
    restored: count(v.restored),
    failed,
    failedCount: Math.max(count(v.failedCount), failed.length),
    sourcePending: count(v.sourcePending),
    record: positiveId(v.record),
    targetLibrary: typeof v.targetLibrary === 'string' ? v.targetLibrary : '',
  };
}

// ---- Backup shapes ----

const TARGET_ID_RE = /^[0-9a-f]{16}$/;

/** Marker ids are 16 lower-case hex digits; anything else never reaches a URL. */
export function isTargetId(v: unknown): v is string {
  return typeof v === 'string' && TARGET_ID_RE.test(v);
}

function encTarget(id: string): string {
  if (!isTargetId(id)) throw new ApiError(400, `invalid target id ${JSON.stringify(id)}`);
  return id;
}

const when = (v: unknown): number | string | null => (typeof v === 'number' || (typeof v === 'string' && v !== '') ? v : null);

function toBehind(v: unknown): BackupBehind | null {
  if (!isRecord(v)) return null;
  return { new: num(v.new), changed: num(v.changed), moved: num(v.moved), bytes: num(v.bytes) };
}

function toSet(v: unknown): BackupSet | null {
  if (!isRecord(v)) return null;
  return {
    name: typeof v.name === 'string' ? v.name : '',
    files: num(v.files),
    bytes: num(v.bytes),
    behind: toBehind(v.behind),
    extras: num(v.extras),
    damaged: num(v.damaged),
    unverified: num(v.unverified),
  };
}

/** A target object with every field present (missing ones defaulted). */
export function toTarget(v: unknown): BackupTarget {
  const r = isRecord(v) ? (isRecord(v.target) ? v.target : v) : {};
  return {
    id: typeof r.id === 'string' ? r.id : '',
    label: typeof r.label === 'string' ? r.label : '',
    path: typeof r.path === 'string' ? r.path : '',
    reachable: r.reachable === true,
    lastBackupAt: when(r.lastBackupAt),
    lastScrubAt: when(r.lastScrubAt),
    set: toSet(r.set),
    ...(typeof r.error === 'string' && r.error !== '' ? { error: r.error } : {}),
    ...(r.foreign === true ? { foreign: true } : {}),
  };
}

/** An example path: a string, or an object naming it (`path`, `relPath`, or a move's `to`). */
export function pathOf(v: unknown): string {
  if (typeof v === 'string') return v;
  if (!isRecord(v)) return '';
  if (typeof v.path === 'string') return v.path;
  if (typeof v.relPath === 'string') return v.relPath;
  if (typeof v.to === 'string') return typeof v.from === 'string' ? `${v.from} → ${v.to}` : v.to;
  return '';
}

function toClass(v: unknown): PlanClass {
  if (typeof v === 'number') return { count: v, bytes: 0, paths: [] };
  if (!isRecord(v)) return { count: 0, bytes: 0, paths: [] };
  const list = Array.isArray(v.paths) ? v.paths : Array.isArray(v.examples) ? v.examples : [];
  return { count: num(v.count), bytes: num(v.bytes), paths: list.map(pathOf).filter((p) => p !== '') };
}

/** The plan: one entry per class, from `{new: {count, bytes, paths}, …}` (also under `classes`). */
export function toPlan(v: unknown): BackupPlan {
  const r = isRecord(v) ? (isRecord(v.classes) ? v.classes : isRecord(v.plan) ? v.plan : v) : {};
  return {
    current: toClass(r.current),
    moved: toClass(r.moved),
    changed: toClass(r.changed),
    new: toClass(r.new),
    extra: toClass(r.extra ?? r.extras),
    ...(r.restore !== undefined && r.restore !== null ? { restore: toClass(r.restore) } : {}),
  };
}

function toRun(v: Record<string, unknown>): BackupRun {
  let stats: Record<string, unknown> = {};
  if (isRecord(v.stats)) stats = v.stats;
  else if (typeof v.stats === 'string') {
    // backup.db keeps the stats as JSON text; accept it unparsed too.
    try {
      const parsed: unknown = JSON.parse(v.stats);
      if (isRecord(parsed)) stats = parsed;
    } catch {
      // leave empty
    }
  }
  return { id: num(v.id), kind: typeof v.kind === 'string' ? v.kind : '', startedAt: when(v.startedAt), finishedAt: when(v.finishedAt), stats };
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

/** The `/api/libraries` body (also the answer of POST /api/app/workspace). */
export function parseWorkspace(v: unknown): Workspace {
  const r = isRecord(v) ? v : {};
  return {
    root: typeof r.root === 'string' ? r.root : '',
    mode: r.mode === 'single' ? 'single' : r.mode === 'unset' ? 'unset' : 'workspace',
    libraries: Array.isArray(r.libraries) ? (r.libraries as Lib[]) : [],
    folders: Array.isArray(r.folders) ? (r.folders as FolderEntry[]) : [],
  };
}

export async function getWorkspace(signal?: AbortSignal): Promise<Workspace> {
  return parseWorkspace(await getJSON<unknown>('/api/libraries', signal));
}

export function createLibrary(name: string): Promise<Lib> {
  return postJSON<Lib>('/api/libraries', { name });
}

/**
 * Cover thumbnail of a library in any readable state (closed included):
 * a 256 px JPEG, 404 when there is none. The server caches it for an hour,
 * so the URL carries the cover hash (`?v=`): a new cover item is a new URL.
 */
export function libraryCoverUrl(id: string, cover?: string | null): string {
  const u = `/api/libraries/${enc(id)}/cover`;
  return cover ? `${u}?v=${enc(cover)}` : u;
}

/**
 * Opens a library: 200 lib. ApiError 409 with `body.state` "busy" (and
 * `body.holder`), "error" (unreadable catalog; the message says why) or
 * "folder"; 503 while rebuilding.
 */
export function openLibrary(id: string): Promise<Lib> {
  return postJSON<Lib>(`/api/libraries/${enc(id)}/open`, {});
}

/** Starts a job through the workspace route (also for folders and unreadable catalogs; rebuild only). */
export async function startWorkspaceJob(id: string, req: RebuildJobRequest): Promise<Job> {
  return unwrapJob(await postJSON<unknown>(`/api/libraries/${enc(id)}/jobs`, req));
}

/** Finishes the interrupted moves into library `id` (a `transfer` job that only recovers). */
export async function resumeTransfer(id: string): Promise<Job> {
  return unwrapJob(await postJSON<unknown>(`/api/libraries/${enc(id)}/transfer/resume`, {}));
}

// ---- Desktop app (docs/design/desktop-shell.md "App endpoints") ----

export type AppPlatform = 'windows' | 'linux' | 'darwin';

/**
 * Why the UI runs (or does not run) in the native window: `ok` it does;
 * `no-runtime` the WebView2 runtime is missing (Windows); `browser-flag`
 * the app was started with --browser; `failed` creating the window failed;
 * `unsupported` the platform has no native window.
 */
export type WindowReason = 'ok' | 'no-runtime' | 'browser-flag' | 'failed' | 'unsupported';

const WINDOW_REASONS: readonly WindowReason[] = ['ok', 'no-runtime', 'browser-flag', 'failed', 'unsupported'];

/** GET /api/app: served only by picmover-app; 404 under `picmover serve`. */
export interface AppInfo {
  app: true;
  version: string;
  /** The workspace folder; "" while none is set (libraries mode "unset"). */
  workspace: string;
  closeToTray: boolean;
  platform: AppPlatform;
  /** Whether the UI runs in the native window (false: the browser fallback). */
  window: boolean;
  /** Why `window` is what it is (older servers leave it out: see `parseAppInfo`). */
  windowReason: WindowReason;
}

export function parseAppInfo(v: unknown): AppInfo | null {
  if (!isRecord(v) || v.app !== true) return null;
  const p = v.platform;
  return {
    app: true,
    version: typeof v.version === 'string' ? v.version : '',
    workspace: typeof v.workspace === 'string' ? v.workspace : '',
    closeToTray: v.closeToTray !== false,
    platform: p === 'windows' || p === 'darwin' ? p : 'linux',
    window: v.window === true,
    windowReason: (WINDOW_REASONS as readonly unknown[]).includes(v.windowReason)
      ? (v.windowReason as WindowReason)
      : v.window === true
        ? 'ok'
        : p === 'windows'
          ? 'no-runtime'
          : 'unsupported',
  };
}

/** The app's description, or null when the server is not the desktop app (404). */
export async function getApp(signal?: AbortSignal): Promise<AppInfo | null> {
  try {
    return parseAppInfo(await getJSON<unknown>('/api/app', signal));
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null;
    throw e;
  }
}

/**
 * Switches the app to another workspace folder: the new `/api/libraries`
 * body (mode "single" when the folder is a library). ApiError 400
 * `{error}` for an invalid path, 409 `{error, jobs}` while a job runs, 409
 * `{error}` when the library cannot be opened or is in use elsewhere.
 */
export async function setAppWorkspace(path: string): Promise<Workspace> {
  return parseWorkspace(await postJSON<unknown>('/api/app/workspace', { path }));
}

/** Opens or focuses the app window (or the browser where there is none); 204. */
export async function appOpen(): Promise<void> {
  await postJSON<unknown>('/api/app/open', {});
}

// ---- Sharing on the home Wi-Fi (docs/design/lan-share.md "Admin API") ----
//
// The admin side of sharing: the global switch (the share listener), the
// shares with their devices, pairing and approval requests. The answers are
// parsed leniently (missing fields defaulted) because the server and this
// client were written in parallel against the design document.

export type ShareKind = 'libraries' | 'album';
export type ShareCapability = 'video' | 'download' | 'rate' | 'location';
export type ShareCapabilities = Record<ShareCapability, boolean>;
export const SHARE_CAPABILITIES: readonly ShareCapability[] = ['video', 'download', 'rate', 'location'];

/** The kind's defaults: everything for a library share; only video for an album share. */
export function defaultCapabilities(kind: ShareKind | string): ShareCapabilities {
  const all = kind !== 'album';
  return { video: true, download: all, rate: all, location: all };
}

/** "active", or why the share's phones are refused. */
export type ShareStateName = 'active' | 'paused' | 'expired' | 'gone' | 'unavailable';

export interface ShareDevice {
  id: string;
  name: string;
  /** RFC 3339 instants; null when unknown. */
  pairedAt: string | null;
  lastSeen: string | null;
  lastAddress: string;
  /** A coarse label from the User-Agent at pairing time ("Chrome on Android"). */
  browser: string;
}

export interface ShareLibRef {
  id: string;
  name: string;
  /** The library folder is gone, or holds another library (LibraryID mismatch). */
  missing: boolean;
  /** Locked by another process or being rebuilt: its phones get "not available right now" (503). */
  busy: boolean;
}

export interface ShareAlbumRef {
  id: number;
  name: string;
  /** Members in user.db, trashed ones included (the phone counts live members only). */
  count: number;
  /** The album was deleted (or its id reused). */
  missing: boolean;
}

/** A share as the admin API describes it. */
export interface Share {
  id: string;
  name: string;
  kind: ShareKind | string;
  /** The libraries of a library share (empty for an album share). */
  libraries: ShareLibRef[];
  /** The library and album of an album share (null for a library share). */
  library: ShareLibRef | null;
  album: ShareAlbumRef | null;
  capabilities: ShareCapabilities;
  /** RFC 3339; null: never expires. */
  expiresAt: string | null;
  state: ShareStateName | string;
  approve: boolean;
  createdAt: string | null;
  devices: ShareDevice[];
  /** The open pairing's expiry (RFC 3339); null when none is open. */
  pairingUntil: string | null;
  /** Why the share's pairings were cancelled ("Too many wrong codes; show a new one."); "" when none. */
  notice: string;
  /** Phones waiting for approval (shares with `approve`). */
  requests: number;
}

export interface ShareAddress {
  ip: string;
  iface: string;
  subnet: string;
  /** A virtual interface (docker, VPN): rarely the Wi-Fi. */
  virtual: boolean;
  /** true: a Wi-Fi interface, false: wired; null: the server did not say (an older one). */
  wireless: boolean | null;
}

export type AutoOffMode = 'never' | '1h' | '4h' | 'quit';

/** GET /api/share: the global switch, the listener's address and the shares. */
export interface SharingStatus {
  state: 'off' | 'on' | 'paused' | 'error' | string;
  /** Why it is paused or failed ("network changed", "Port 7443 is in use…"); "" when none. */
  reason: string;
  /** https://<ip>:<port>/ while on; "" otherwise. */
  url: string;
  /** https://<host>.local:<port>/ when the mDNS name is on; "" otherwise. */
  mdnsUrl: string;
  /** Whether the .local name is offered (the `mdns` setting; else inferred from `mdnsUrl`). */
  mdns: boolean;
  port: number;
  address: string;
  addresses: ShareAddress[];
  fingerprint: { leaf: string; ca: string };
  keepOn: boolean;
  autoOff: { mode: AutoOffMode | string; until: string | null };
  managedElsewhere: { pid: number; cmd: string } | null;
  /** The commands to open the firewall, as text (one or more lines). */
  firewallHint: string;
  shares: Share[];
}

/** GET /api/share/status: the top-bar pill's cheap summary (no library is looked at). */
export interface SharingSummary {
  state: 'off' | 'on' | 'paused' | 'error' | string;
  reason: string;
  url: string;
  /** Shares that phones can use now (not paused, expired, of another workspace or of an unknown kind). */
  shares: number;
  /** Phones of those shares. */
  phones: number;
  autoOff: { mode: AutoOffMode | string; until: string | null };
  managedElsewhere: { pid: number; cmd: string } | null;
}

export interface SharingPatch {
  enabled?: boolean;
  address?: string;
  port?: number;
  keepOn?: boolean;
  autoOff?: AutoOffMode;
  mdns?: boolean;
}

export type ShareScopeRequest = { kind: 'libraries'; libraries: string[] } | { kind: 'album'; library: string; album: number };

export interface ShareCreate {
  name: string;
  scope: ShareScopeRequest;
  capabilities?: ShareCapabilities;
  /** Seconds from now; or `expiresAt` (null: never). */
  expiresIn?: number;
  expiresAt?: string | null;
  approve?: boolean;
}

export interface ShareUpdate {
  name?: string;
  /** Library shares only. */
  libraries?: string[];
  capabilities?: ShareCapabilities;
  /** null: never expires. */
  expiresAt?: string | null;
  paused?: boolean;
  approve?: boolean;
}

/** A pairing for one share: the QR (an SVG of `url`) and the typed code, valid until `expiresAt`. */
export interface Pairing {
  url: string;
  code: string;
  qrSvg: string;
  expiresAt: string | null;
}

/** A phone waiting for approval (the share's `approve` setting). */
export interface PairRequest {
  id: string;
  name: string;
  browser: string;
  address: string;
  at: string | null;
}

const bool = (v: unknown): boolean => v === true;

function toCapabilities(v: unknown, kind: string): ShareCapabilities {
  const out = defaultCapabilities(kind);
  if (!isRecord(v)) return out;
  for (const c of SHARE_CAPABILITIES) {
    const x = v[c];
    if (typeof x === 'boolean') out[c] = x;
  }
  return out;
}

function toShareLib(v: unknown): ShareLibRef | null {
  if (typeof v === 'string' && v !== '') return { id: v, name: v, missing: false, busy: false };
  if (!isRecord(v) || typeof v.id !== 'string' || v.id === '') return null;
  return { id: v.id, name: str(v.name) || v.id, missing: bool(v.missing), busy: bool(v.busy) };
}

export function toShareDevice(v: unknown): ShareDevice {
  const r = isRecord(v) ? (isRecord(v.device) ? v.device : v) : {};
  return {
    id: str(r.id),
    name: str(r.name),
    pairedAt: instant(r.pairedAt),
    lastSeen: instant(r.lastSeen),
    lastAddress: str(r.lastAddress),
    browser: str(r.browser),
  };
}

/** A share object with every field present; the scope's parts are read from `scope` (or the top level). */
export function toShare(v: unknown): Share {
  const r = isRecord(v) ? (isRecord(v.share) ? v.share : v) : {};
  const scope = isRecord(r.scope) ? r.scope : r;
  const kind = str(r.kind) || str(scope.kind) || (isRecord(scope.album) ? 'album' : 'libraries');
  const libs = Array.isArray(scope.libraries) ? scope.libraries.map(toShareLib).filter((x): x is ShareLibRef => x !== null) : [];
  const a = isRecord(scope.album) ? scope.album : null;
  return {
    id: str(r.id),
    name: str(r.name),
    kind,
    libraries: kind === 'album' ? [] : libs,
    library: kind === 'album' ? toShareLib(scope.library) : null,
    album: a ? { id: num(a.id), name: str(a.name), count: count(a.count), missing: bool(a.missing) } : null,
    capabilities: toCapabilities(r.capabilities, kind),
    expiresAt: instant(r.expiresAt),
    state: str(r.state) || 'active',
    approve: bool(r.approve),
    createdAt: instant(r.createdAt),
    devices: Array.isArray(r.devices) ? r.devices.map(toShareDevice).filter((d) => d.id !== '') : [],
    pairingUntil: isRecord(r.pairing) ? instant(r.pairing.expiresAt) : null,
    notice: str(r.notice),
    requests: count(r.requests),
  };
}

function hintText(v: unknown): string {
  if (typeof v === 'string') return v;
  if (Array.isArray(v)) return v.filter((x): x is string => typeof x === 'string').join('\n');
  if (isRecord(v)) {
    return Object.entries(v)
      .filter((e): e is [string, string] => typeof e[1] === 'string' && e[1] !== '')
      .map(([k, c]) => `${k}: ${c}`)
      .join('\n');
  }
  return '';
}

/** The GET /api/share body (also every POST /api/share answer) with every field present. */
export function toSharingStatus(v: unknown): SharingStatus {
  const r = isRecord(v) ? v : {};
  const fp = isRecord(r.fingerprint) ? r.fingerprint : {};
  const ao = isRecord(r.autoOff) ? r.autoOff : { mode: r.autoOff };
  const me = isRecord(r.managedElsewhere) ? r.managedElsewhere : null;
  const mdnsUrl = str(r.mdnsUrl);
  return {
    state: str(r.state) || 'off',
    reason: str(r.reason),
    url: str(r.url),
    mdnsUrl,
    mdns: typeof r.mdns === 'boolean' ? r.mdns : mdnsUrl !== '',
    port: count(r.port),
    address: str(r.address),
    addresses: Array.isArray(r.addresses)
      ? r.addresses
          .filter(isRecord)
          .map((a) => ({ ip: str(a.ip), iface: str(a.iface), subnet: str(a.subnet), virtual: bool(a.virtual), wireless: typeof a.wireless === 'boolean' ? a.wireless : null }))
          .filter((a) => a.ip !== '')
      : [],
    fingerprint: { leaf: str(fp.leaf), ca: str(fp.ca) },
    keepOn: bool(r.keepOn),
    autoOff: { mode: str(ao.mode) || 'quit', until: instant(ao.until) },
    managedElsewhere: me ? { pid: count(me.pid), cmd: str(me.cmd) } : null,
    firewallHint: hintText(r.firewallHint),
    shares: Array.isArray(r.shares) ? r.shares.map(toShare).filter((x) => x.id !== '') : [],
  };
}

export function toSharingSummary(v: unknown): SharingSummary {
  const r = isRecord(v) ? v : {};
  const ao = isRecord(r.autoOff) ? r.autoOff : { mode: r.autoOff };
  const me = isRecord(r.managedElsewhere) ? r.managedElsewhere : null;
  return {
    state: str(r.state) || 'off',
    reason: str(r.reason),
    url: str(r.url),
    shares: count(r.shares),
    phones: count(r.phones),
    autoOff: { mode: str(ao.mode) || 'quit', until: instant(ao.until) },
    managedElsewhere: me ? { pid: count(me.pid), cmd: str(me.cmd) } : null,
  };
}

export function toPairing(v: unknown): Pairing {
  const r = isRecord(v) ? v : {};
  return { url: str(r.url), code: str(r.code), qrSvg: str(r.qrSvg), expiresAt: instant(r.expiresAt) };
}

/** The approval requests, as a bare list or under `requests`; malformed entries are dropped. */
export function toPairRequests(v: unknown): PairRequest[] {
  const list: unknown[] = Array.isArray(v) ? v : isRecord(v) && Array.isArray(v.requests) ? v.requests : [];
  return list.filter(isRecord).flatMap((x) => {
    const id = typeof x.id === 'number' ? String(x.id) : str(x.id);
    return id ? [{ id, name: str(x.name), browser: str(x.browser), address: str(x.address), at: instant(x.at) }] : [];
  });
}

const SHARE_ID_RE = /^[A-Za-z0-9_-]{1,80}$/;

/** Share, device and request ids are short tokens ("s_8c1f…"); anything else never reaches a URL. */
function encShareId(id: string): string {
  if (!SHARE_ID_RE.test(id)) throw new ApiError(400, `invalid id ${JSON.stringify(id)}`);
  return id;
}

/** The sharing state, or null when the server has no sharing (404: an older picmover). */
export async function getSharing(signal?: AbortSignal): Promise<SharingStatus | null> {
  try {
    return toSharingStatus(await getJSON<unknown>('/api/share', signal));
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null;
    throw e;
  }
}

/** The pill's summary, or null when the server has no sharing (404: an older picmover). */
export async function getSharingSummary(signal?: AbortSignal): Promise<SharingSummary | null> {
  try {
    return toSharingSummary(await getJSON<unknown>('/api/share/status', signal));
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null;
    throw e;
  }
}

export async function setSharing(patch: SharingPatch): Promise<SharingStatus> {
  return toSharingStatus(await postJSON<unknown>('/api/share', patch));
}

/** A new leaf certificate (the CA is kept). */
export async function renewShareCert(): Promise<SharingStatus> {
  return toSharingStatus(await postJSON<unknown>('/api/share/cert/renew', {}));
}

export async function createShare(req: ShareCreate): Promise<Share> {
  return toShare(await postJSON<unknown>('/api/share/shares', req));
}

export async function updateShare(id: string, patch: ShareUpdate): Promise<Share> {
  return toShare(await postJSON<unknown>(`/api/share/shares/${encShareId(id)}`, patch));
}

export async function deleteShare(id: string): Promise<void> {
  await postJSON<unknown>(`/api/share/shares/${encShareId(id)}/delete`, {});
}

export async function revokeAllDevices(id: string): Promise<Share> {
  return toShare(await postJSON<unknown>(`/api/share/shares/${encShareId(id)}/revoke-all`, {}));
}

/** A new pairing for the share; ApiError 409 while sharing is off or the share is not active. */
export async function startPairing(id: string): Promise<Pairing> {
  return toPairing(await postJSON<unknown>(`/api/share/shares/${encShareId(id)}/pairings`, {}));
}

export async function cancelPairing(id: string): Promise<void> {
  await postJSON<unknown>(`/api/share/shares/${encShareId(id)}/pairings/cancel`, {});
}

export async function getPairRequests(id: string, signal?: AbortSignal): Promise<PairRequest[]> {
  return toPairRequests(await getJSON<unknown>(`/api/share/shares/${encShareId(id)}/requests`, signal));
}

export async function answerPairRequest(id: string, request: string, approve: boolean): Promise<void> {
  await postJSON<unknown>(`/api/share/shares/${encShareId(id)}/requests/${encShareId(request)}`, { approve });
}

export async function renameDevice(id: string, name: string): Promise<ShareDevice> {
  return toShareDevice(await postJSON<unknown>(`/api/share/devices/${encShareId(id)}`, { name }));
}

export async function revokeDevice(id: string): Promise<void> {
  await postJSON<unknown>(`/api/share/devices/${encShareId(id)}/revoke`, {});
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
    // The similar job reports {phase, items|bands|pairs}, the backup kinds
    // {phase, files, bytes, totalFiles, totalBytes, current, failed}: at the
    // top level, under `progress`, or (backup kinds) under `stats`.
    const p = isRecord(d.progress) ? d.progress : isRecord(d.stats) && typeof d.stats.phase === 'string' ? d.stats : d;
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
