// Test doubles shared by the component tests: a stateful fake of the
// step 4 API (a workspace with a few libraries; one of them, "peter", has
// items, tags, albums, import records, a trash and similar groups), EventSource and
// ResizeObserver fakes, and helpers to let effects and timers settle.

import { vi } from 'vitest';
import type { AppInfo, Job, Lib, MediaItem } from '../lib/api';

export function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

export class FakeEventSource extends EventTarget {
  static instances: FakeEventSource[] = [];
  url: string;
  onerror: ((e: Event) => void) | null = null;
  constructor(url: string) {
    super();
    this.url = url;
    FakeEventSource.instances.push(this);
  }
  emit(type: string, data: unknown) {
    this.dispatchEvent(new MessageEvent(type, { data: JSON.stringify(data) }));
  }
  close() {}
}

export class FakeResizeObserver {
  cb: ResizeObserverCallback;
  constructor(cb: ResizeObserverCallback) {
    this.cb = cb;
  }
  observe(target: Element) {
    const entry = { target, contentRect: { width: 1000, height: 800 } } as unknown as ResizeObserverEntry;
    queueMicrotask(() => this.cb([entry], this as unknown as ResizeObserver));
  }
  unobserve() {}
  disconnect() {}
}

export async function settle() {
  for (let i = 0; i < 6; i++) await vi.advanceTimersByTimeAsync(250);
}

export async function go(hash: string) {
  location.hash = hash;
  window.dispatchEvent(new HashChangeEvent('hashchange'));
  await settle();
}

/** Tiny stand-ins for rendition bytes (signatures only). */
const TINY_JPEG = new Uint8Array([0xff, 0xd8, 0xff, 0xd9]);
const TINY_WEBP = new Uint8Array([0x52, 0x49, 0x46, 0x46, 0x04, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42, 0x50]);

export const hashOf = (id: number) => id.toString(16).padStart(16, '0');

/**
 * A large library's timeline, newest first: about 200,000 photos over 15
 * years (2011–2025) with uneven months, as a family library has them (more
 * every year, summers and Christmases big, a few empty months, a few big
 * trips), and an undated bucket. Deterministic.
 */
export function bigTimeline(): { year: number; month: number; count: number }[] {
  let seed = 42;
  const rnd = () => (seed = (seed * 1664525 + 1013904223) >>> 0) / 2 ** 32;
  const out: { year: number; month: number; count: number }[] = [];
  for (let y = 2025; y >= 2011; y--) {
    const base = 600 + (y - 2011) * 70;
    for (let m = 12; m >= 1; m--) {
      let n = base * (0.3 + rnd());
      if (m === 6 || m === 7) n *= 2.6;
      if (m === 12) n *= 1.8;
      if (rnd() < 0.08) n = 0;
      if (rnd() < 0.04) n *= 4;
      n = Math.round(n);
      if (n > 0) out.push({ year: y, month: m, count: n });
    }
  }
  out.push({ year: 0, month: 0, count: 312 });
  return out;
}

export interface Item extends MediaItem {
  deletedAt?: string | null;
  tagIds: number[];
}

export interface Call {
  method: string;
  path: string;
  body: Record<string, unknown> | null;
}

/** Unix seconds of a local wall-clock time (the fake clock runs at 2026-10-10T12:00:00 local). */
export const unix = (wall: string) => Math.floor(new Date(wall).getTime() / 1000);

/** Marker ids of the fake's targets. */
export const T1 = 'a1b2c3d4e5f60718';
export const T2 = '0f1e2d3c4b5a6978';

interface FakeTarget {
  id: string;
  label: string;
  path: string;
  reachable: boolean;
  lastBackupAt: number | null;
  lastScrubAt: number | null;
  set: { name: string; files: number; bytes: number; extras: number; damaged: number; unverified: number } | null;
  /** As the server's describeTarget: the set cannot be used (another library's set under this name). */
  error?: string;
  /** With `error`: the set was last written by another copy of this library; a backup with `takeOver` claims it. */
  foreign?: boolean;
}

/** The importer counters every job object carries; zero for the backup kinds (their stats are in `result`). */
export const ZERO_STATS = { scanned: 0, imported: 0, existing: 0, duplicates: 0, skippedSmall: 0, nonMedia: 0, errors: 0, bytesCopied: 0, rate: 0, thumbsDone: 0, thumbsUnsupported: 0, thumbsFailed: 0, largeDone: 0 };

/** Run stats (`result`, and `stats` in runs) exactly as the step 5 server writes them, per kind. */
export function runResult(kind: string, p: Record<string, unknown> = {}): Record<string, unknown> {
  switch (kind) {
    case 'backup':
      return {
        current: 0, changed: 0, new: 0, extra: 0, copied: 0, moved: 0, adopted: 0, refreshed: 0, failedCount: 0, bytes: 0, partial: false,
        leftFiles: 0, leftBytes: 0, symlinkCount: 0, symlinks: [], missingCount: 0, missing: [], invalidCount: 0, failed: [],
        userSnapshot: true, mediaSnapshot: true, cancelled: false, seconds: 0, ...p,
      };
    case 'scrub':
      return {
        checked: 0, bytes: 0, ok: 0, damaged: [], damagedCount: 0, repaired: 0, missing: [], missingCount: 0, unknown: [], unknownCount: 0,
        passComplete: true, remaining: 0, cancelled: false, seconds: 0, ...p,
      };
    case 'prune':
      return { removed: 0, bytes: 0, failed: [], seconds: 0, ...p };
    default:
      return { restored: 0, bytes: 0, present: 0, elsewhere: 0, failedCount: 0, failed: [], userDB: 'present', needsRebuild: false, cancelled: false, seconds: 0, ...p };
  }
}

/** A progress object as the server sends it (`rate` bytes/s, `eta` seconds or -1). */
export function progress(p: Record<string, unknown> = {}): Record<string, unknown> {
  return { phase: 'copy', files: 0, bytes: 0, totalFiles: 0, totalBytes: 0, current: '', failed: 0, rate: 0, eta: -1, ...p };
}

/** A backup-kind job object as the step 5 server sends it (state, start answer, listing, SSE done). */
export function backupJobJSON(p: { id: number; kind: string; target: string; library?: string; state?: string } & Record<string, unknown>): Job {
  const { id, kind, target, library = 'peter', state = 'running', ...rest } = p;
  return {
    id,
    library,
    kind,
    state,
    move: false,
    target,
    ...(kind === 'restore' ? { mode: 'missing' } : {}),
    // The fake clock's now (tests run in any time zone).
    startedAt: new Date().toISOString(),
    finishedAt: state === 'running' || state === 'queued' ? null : new Date().toISOString(),
    stats: { ...ZERO_STATS },
    sessionId: null,
    logPath: state === 'running' || state === 'queued' ? '' : `/media/${library}/.picmover/logs/${kind}_2026-10-10_12-00-00.log`,
    error: null,
    exitCode: 0,
    ...rest,
  } as Job;
}

export interface FakeImport {
  id: number;
  name: string;
  startedAt: string;
  finishedAt: string | null;
  source: string;
  sourceName: string;
  mode: string;
  host: string;
  outcome: string;
  imported: number;
  existing: number;
  duplicates: number;
  skipped: number;
  errors: number;
  bytes: number;
  /** Item hashes (import_items); none for the modes that keep no items (rebuild, record). */
  hashes: string[];
  /**
   * The stored summary GET imports lists (user.db imports.items/cover,
   * written when the record was finished): undefined computes it from the
   * items when the fake starts; null for a record without items.
   */
  items?: number | null;
  cover?: string | null;
}

/** A share as the fake admin API keeps it (docs/design/lan-share.md "Shares"). */
export interface FakeShare {
  id: string;
  name: string;
  kind: 'libraries' | 'album';
  /** The shared libraries; an album share's one library. */
  libraries: string[];
  album: number;
  /** The album's name when the share was made (shown once the album is gone). */
  albumName: string;
  /** The album was deleted or its id reused (the share is broken). */
  albumMissing: boolean;
  /** Members in user.db, trashed ones included (the admin count); the album's hashes when left out. */
  albumCount?: number;
  /** A scope kind this picmover does not know (another version wrote it): state unavailable, scope {}. */
  unknownKind?: string;
  /** Another workspace's share: state unavailable. */
  elsewhere?: boolean;
  /** Why the share's pairings were cancelled. */
  notice?: string;
  capabilities: { video: boolean; download: boolean; rate: boolean; location: boolean };
  expiresAt: string | null;
  paused: boolean;
  approve: boolean;
  createdAt: string;
  devices: { id: string; name: string; pairedAt: string; lastSeen: string; lastAddress: string; browser: string }[];
}

interface FakeClass {
  count: number;
  bytes: number;
  paths: unknown[];
}

const cls = (count: number, bytes: number, paths: unknown[] = []): FakeClass => ({ count, bytes, paths });

const summary = (n: number) => ({ total: n, images: n, raw: 0, videos: 0, withGps: 0, deleted: 0, bytes: 1, earliest: '2024-06-01T10:00:00', latest: '2024-07-31T10:00:00' });

/** The desktop app's GET /api/app answer (docs/design/desktop-shell.md); tests override fields. */
export function appInfo(p: Partial<AppInfo> = {}): AppInfo {
  const window = p.window ?? true;
  const windowReason = window ? 'ok' : (p.platform ?? 'linux') === 'windows' ? 'no-runtime' : 'unsupported';
  return { app: true, version: 'v0.6.0-test', workspace: '/media', closeToTray: true, platform: 'linux', window, windowReason, ...p };
}

/**
 * A fresh fake server; `install()` stubs fetch, EventSource and ResizeObserver.
 * Mode "unset" is the desktop app without a workspace: the listing is empty,
 * every library route answers 409 state "unset", and POST /api/app/workspace
 * switches it to a workspace with the usual libraries, or (a folder in
 * `libraryFolders`, as one with media.db) to single mode on that library;
 * 409 without `jobs` when that library is busy or broken. `app.info` set (see
 * `appInfo`) makes it the desktop app; null (the default) answers the app
 * routes 404 as `picmover serve` does.
 */
export function fakeServer(mode: 'workspace' | 'single' | 'unset' = 'workspace') {
  /** The listing's mode and root; POST /api/app/workspace changes them. */
  const ws: { mode: 'workspace' | 'single' | 'unset'; root: string; single: Lib | null } = { mode, root: mode === 'single' ? '/media/peter' : mode === 'unset' ? '' : '/media', single: null };
  /** Folders the workspace POST treats as libraries (they have media.db): it serves them in single mode. */
  const libraryFolders = new Set(['/media/peter', '/media/penny', '/media/broken']);
  /** `info` null: the app routes answer 404 (`picmover serve`); `fail`: the next that many GET /api/app answer 500. */
  const app: { info: AppInfo | null; fail: number } = { info: null, fail: 0 };
  const calls: Call[] = [];
  const items: Item[] = [];
  // July: ids 1..6 (newest first), June: 7..8, one trashed (9).
  for (let i = 1; i <= 6; i++) items.push({ id: i, hash: hashOf(i), type: 'image', takenAt: `2024-07-${String(20 - i).padStart(2, '0')}T10:00:00`, w: 4000, h: 3000, thumb: 'ok', tagIds: [] });
  for (let i = 7; i <= 8; i++) items.push({ id: i, hash: hashOf(i), type: 'image', takenAt: `2024-06-${String(20 - i).padStart(2, '0')}T10:00:00`, w: 4000, h: 3000, thumb: 'ok', tagIds: [] });
  items.push({ id: 9, hash: hashOf(9), type: 'image', takenAt: '2024-07-01T10:00:00', w: 10, h: 10, thumb: 'ok', tagIds: [], deletedAt: '2026-10-09T10:00:00' });
  const tags = [{ id: 1, name: 'beach' }];
  const albums: { id: number; name: string; hashes: string[]; cover: string | null }[] = [{ id: 2, name: 'Summer', hashes: [hashOf(1), hashOf(2)], cover: null }];
  let nextTag = 10;
  let nextAlbum = 10;
  /**
   * Import records of user.db (docs/design/imports.md), newest first: a
   * named recent copy that brought in 1..3 and found 4 in place, and an
   * older move from a card (7, 8).
   */
  const imports: FakeImport[] = [
    {
      id: 5,
      name: 'Phone dump',
      startedAt: '2026-10-10T09:00:00Z',
      finishedAt: '2026-10-10T09:05:00Z',
      source: '/mnt/phone/DCIM/Camera',
      sourceName: 'Camera',
      mode: 'copy',
      host: 'laptop',
      outcome: 'done',
      imported: 3,
      existing: 1,
      duplicates: 2,
      skipped: 0,
      errors: 1,
      bytes: 3 * 1024 * 1024,
      hashes: [hashOf(1), hashOf(2), hashOf(3), hashOf(4)],
    },
    {
      id: 2,
      name: '',
      startedAt: '2026-08-01T10:00:00Z',
      finishedAt: null,
      source: '/media/card/DCIM',
      sourceName: 'DCIM',
      mode: 'move',
      host: 'laptop',
      outcome: 'cancelled',
      imported: 2,
      existing: 0,
      duplicates: 0,
      skipped: 0,
      errors: 0,
      bytes: 2048,
      hashes: [hashOf(7), hashOf(8)],
    },
  ];
  /** Near-duplicate groups as hash lists, before trash and ignore; tests may replace them. */
  const similar: { groups: string[][] } = { groups: [[hashOf(7), hashOf(8)]] };
  /** Ignored pairs "a|b" (a < b), as user.db similar_ignored. */
  const ignoredPairs = new Set<string>();
  const pairKey = (a: string, b: string) => (a < b ? `${a}|${b}` : `${b}|${a}`);
  /** Ids whose open answers 409 state "error" with this message although the listing says closed (user.db too new, ...). */
  const openErrors = new Map<string, string>();
  /** user.db counts that differ from the catalog's (null: the same). */
  const userdb: { trashed: number | null } = { trashed: null };
  /** The library JSON `thumbs` counts (tests set `largeMissing`). */
  const thumbCounts: { pending: number; ok: number; unsupported: number; error: number; largeMissing?: number } = { pending: 0, ok: 9, unsupported: 0, error: 0, largeMissing: 0 };
  /** Hashes whose large cannot be derived: the large route answers the thumb bytes with no-store. */
  const largeFallback = new Set<string>();

  /**
   * Backups of "peter" (docs/design/backups.md "HTTP API"): targets as
   * user.db and backup.db would describe them, the plan per target (its
   * `behind` is what GET …/backups reports once planned), and the runs.
   */
  const backup = {
    targets: [
      {
        id: T1,
        label: 'Red WD drive',
        path: '/mnt/red',
        reachable: true,
        lastBackupAt: unix('2026-10-10T10:00:00'),
        lastScrubAt: unix('2026-10-01T10:00:00'),
        set: { name: 'peter', files: 8, bytes: 8000, extras: 3, damaged: 1, unverified: 2 },
      },
      {
        id: T2,
        label: 'NAS',
        path: '\\\\nas\\photos',
        reachable: false,
        lastBackupAt: unix('2026-08-01T10:00:00'),
        lastScrubAt: null,
        set: null,
      },
    ] as FakeTarget[],
    /** Targets whose plan has been computed (GET …/backups?plan=1 or GET …/plan): their `behind` is reported. */
    planned: new Set<string>(),
    /** The real plain GET computes the plan on a cache miss; false makes `behind` null until `?plan=1` (an old cache). */
    planOnGet: true,
    plans: {
      [T1]: {
        current: cls(5, 5000),
        new: cls(2, 2048, ['image/2024/07/1.jpg', 'image/2024/07/2.jpg']),
        changed: cls(0, 0),
        moved: cls(1, 1000, [{ from: 'image/2024/06/7.jpg', to: 'image/2024/06/7b.jpg' }]),
        // Extras: two set files the library no longer has, plus an old version under previous/.
        extra: cls(3, 3000, ['image/2023/01/x.jpg', 'image/2023/01/y.jpg', '.picmover/previous/image/2024/06/8.jpg.1757000000']),
        // What a restore copies back: set rows missing from the library (no previous/ versions).
        restore: cls(2, 2000, ['image/2023/01/x.jpg', 'image/2023/01/y.jpg']),
      },
    } as Record<string, Record<string, FakeClass>>,
    /** Whether the plan carries the `restore` class (false: a server without it). */
    planRestore: true,
    /** Backup-kind jobs as GET /api/jobs lists them (tests add other libraries' jobs here). */
    jobs: [] as Job[],
    runs: {
      [T1]: [
        {
          id: 3,
          kind: 'scrub',
          startedAt: unix('2026-10-01T10:00:00'),
          finishedAt: unix('2026-10-01T10:15:00'),
          stats: runResult('scrub', {
            checked: 8,
            bytes: 8000,
            ok: 6,
            damaged: [
              { path: 'image/2024/06/7.jpg', repaired: true },
              { path: 'image/2024/06/8.jpg', repaired: false, error: 'the library copy differs too' },
            ],
            damagedCount: 2,
            // 7.jpg and the missing 5.jpg were copied again.
            repaired: 2,
            missing: ['image/2024/07/5.jpg'],
            missingCount: 1,
            unknown: ['notes/stray.txt'],
            unknownCount: 1,
            seconds: 900,
          }),
        },
        {
          id: 2,
          kind: 'backup',
          startedAt: unix('2026-10-10T09:50:00'),
          finishedAt: unix('2026-10-10T10:00:00'),
          stats: runResult('backup', { new: 8, copied: 7, bytes: 7000, failed: [{ path: 'image/2024/07/3.jpg', error: 'read: input/output error' }], failedCount: 1, seconds: 600 }),
        },
      ],
    } as Record<string, { id: number; kind: string; startedAt: number; finishedAt: number | null; stats: Record<string, unknown> }[]>,
    nextId: 0x100,
  };
  const TARGET_RE = /^[0-9a-f]{16}$/;
  const behindOf = (id: string) => {
    const p = backup.plans[id];
    if (!p || !backup.planned.has(id)) return null;
    const c = (k: string) => p[k] ?? cls(0, 0);
    return { new: c('new').count, changed: c('changed').count, moved: c('moved').count, bytes: c('new').bytes + c('changed').bytes };
  };
  const targetJSON = (t: FakeTarget) => {
    if (t.reachable && t.error) return { ...t, set: null };
    // foreign is only sent together with error.
    if (t.foreign !== undefined) {
      const { foreign: _f, ...plain } = t;
      t = plain;
    }
    if (t.reachable && backup.planOnGet && backup.plans[t.id]) backup.planned.add(t.id);
    const { error: _e, ...rest } = t;
    return { ...rest, set: t.set && t.reachable ? { ...t.set, behind: behindOf(t.id) } : null };
  };
  const planJSON = (id: string) => {
    const p = backup.plans[id] ?? { current: cls(0, 0), new: cls(0, 0), changed: cls(0, 0), moved: cls(0, 0), extra: cls(0, 0), restore: cls(0, 0) };
    const { restore, ...rest } = p;
    return { ...rest, ...(backup.planRestore && restore ? { restore } : {}), setName: 'peter', symlinks: 0, missing: 0, keepDays: 30 };
  };
  const OTHER_SET = 'the target already holds a backup of another library under this name';
  /** Absolute POSIX, drive or UNC path; no NUL and no device paths (as the server's check). */
  const validTargetPath = (p: unknown): p is string =>
    typeof p === 'string' && !p.includes('\u0000') && !/^[\\/]{2}[?.][\\/]/.test(p) && (/^\/(?!\/)/.test(p) || /^[A-Za-z]:[\\/]/.test(p) || /^[\\/]{2}[^\\/]+[\\/][^\\/]+/.test(p));

  function backups(path: string, q: URLSearchParams, method: string, body: Record<string, unknown>): Response {
    if (method === 'GET' && path === 'backups') {
      if (q.get('plan') === '1') for (const t of backup.targets) if (t.reachable) backup.planned.add(t.id);
      return json({ targets: backup.targets.map(targetJSON) });
    }
    if (method === 'POST' && path === 'backups/targets') {
      if (!validTargetPath(body.path)) return json({ error: 'path: expected an absolute folder path' }, 400);
      if (body.path.startsWith('/media/peter')) return json({ error: 'the target folder is inside the library' }, 400);
      if (body.path === '/mnt/taken') return json({ error: `${OTHER_SET}; choose another set name` }, 409);
      // As the server: a new target gets an empty set and a plan (everything is new).
      const t: FakeTarget = {
        id: (backup.nextId++).toString(16).padStart(16, '0'),
        label: typeof body.label === 'string' ? body.label : '',
        path: body.path,
        reachable: true,
        lastBackupAt: null,
        lastScrubAt: null,
        set: { name: 'peter', files: 0, bytes: 0, extras: 0, damaged: 0, unverified: 0 },
      };
      backup.plans[t.id] = { current: cls(0, 0), new: cls(8, 8000), changed: cls(0, 0), moved: cls(0, 0), extra: cls(0, 0), restore: cls(0, 0) };
      backup.planned.add(t.id);
      backup.targets.push(t);
      return json(targetJSON(t), 201);
    }
    const m = /^backups\/targets\/([^/]+)(\/forget|\/plan|\/runs)?$/.exec(path);
    if (!m) return json({ error: 'not found' }, 404);
    const id = decodeURIComponent(m[1] ?? '');
    if (!TARGET_RE.test(id)) return json({ error: 'invalid target id' }, 400);
    const t = backup.targets.find((x) => x.id === id);
    if (!t) return json({ error: 'no such target' }, 404);
    if (method === 'GET' && m[2] === '/plan') {
      if (!t.reachable) return json({ error: 'the target is not reachable' }, 409);
      if (t.error) return json({ error: t.error }, 409);
      backup.planned.add(id);
      return json(planJSON(id));
    }
    if (method === 'GET' && m[2] === '/runs') return json({ runs: backup.runs[id] ?? [] });
    if (method !== 'POST') return json({ error: 'not found' }, 404);
    if (m[2] === '/forget') {
      backup.targets.splice(backup.targets.indexOf(t), 1);
      return json({});
    }
    if (m[2] === undefined) {
      if (body.path !== undefined) {
        if (!validTargetPath(body.path)) return json({ error: 'path: expected an absolute folder path' }, 400);
        if (body.path === '/mnt/wrong') return json({ error: 'the folder is another backup target (its marker has another id)' }, 409);
        t.path = body.path;
        t.reachable = true;
      }
      if (typeof body.label === 'string') t.label = body.label;
      return json(targetJSON(t));
    }
    return json({ error: 'not found' }, 404);
  }

  /** Job bodies of the backup kinds, checked as the server does. */
  function backupJob(body: Record<string, unknown>): Response | null {
    const kind = body.kind;
    if (kind !== 'backup' && kind !== 'scrub' && kind !== 'prune' && kind !== 'restore') return null;
    if (typeof body.target !== 'string' || !TARGET_RE.test(body.target)) return json({ error: 'target: invalid id' }, 400);
    const t = backup.targets.find((x) => x.id === body.target);
    if (!t) return json({ error: 'no such target' }, 404);
    if (!t.reachable) return json({ error: 'the target is not reachable' }, 409);
    if (t.error && !(kind === 'backup' && t.foreign && body.takeOver === true)) return json({ error: t.error }, 409);
    if (backup.jobs.some((j) => j.state === 'running' || j.state === 'queued')) return json({ error: 'another job is running' }, 409);
    if (kind === 'scrub' && body.budget !== undefined && (typeof body.budget !== 'number' || body.budget <= 0)) return json({ error: 'budget: expected seconds' }, 400);
    if (kind === 'prune') {
      if (body.confirm !== true) return json({ error: 'confirm required' }, 400);
      if (typeof body.expected !== 'number') return json({ error: 'expected: required' }, 400);
      const n = backup.plans[t.id]?.extra?.count ?? 0;
      if (body.expected !== n) return json({ error: `the files to remove changed: the plan now lists ${n}, not ${body.expected}; review them again` }, 409);
    }
    if (kind === 'restore' && body.mode === 'user-data') {
      return json({ error: "mode user-data replaces user.db, which the server has open: stop the server and run 'picmover restore --mode user-data'" }, 400);
    }
    if (kind === 'restore' && body.mode !== undefined && body.mode !== 'missing') return json({ error: 'mode: expected missing or user-data' }, 400);
    // As the server's job object: the target is echoed (and the mode of a restore).
    if (kind === 'backup' && body.takeOver === true && t.foreign) {
      delete t.error;
      delete t.foreign;
    }
    const job = backupJobJSON({ id: ++jobId, kind, target: t.id });
    backup.jobs.unshift(job);
    return json(job, 201);
  }

  /**
   * Ends backup-kind job `id` as the server does: the listing shows it
   * finished and the job's stream sends `done` with `result` (the run's
   * stats; `stats` stays zero), `exitCode`, `error` and the last progress.
   */
  function finish(id: number, p: { result?: Record<string, unknown>; exitCode?: number; error?: string | null; state?: string; progress?: Record<string, unknown> } = {}): Job {
    const i = backup.jobs.findIndex((j) => j.id === id);
    const before = backup.jobs[i];
    if (!before) throw new Error(`no fake job ${id}`);
    const exitCode = p.exitCode ?? 0;
    const state = p.state ?? (exitCode === 0 ? 'done' : exitCode === 130 ? 'cancelled' : 'failed');
    const done = backupJobJSON({
      id,
      kind: before.kind,
      target: before.target ?? '',
      library: before.library,
      state,
      startedAt: before.startedAt,
      exitCode,
      error: p.error ?? null,
      result: runResult(before.kind, p.result),
      progress: progress({ phase: before.kind === 'backup' ? 'snapshot' : before.kind, ...p.progress }),
    });
    backup.jobs[i] = done;
    const es = [...FakeEventSource.instances].reverse().find((e) => e.url === `/api/jobs/${id}/events`);
    es?.emit('done', done);
    return done;
  }

  /**
   * Moving to another library (docs/design/library-move.md): what the plan
   * finds per hash and what a transfer job does with it. Hashes not named
   * here are moved; trashed or unknown ones are refused as the server does.
   */
  const transfer = {
    sameVolume: true,
    free: null as number | null,
    /** Bytes of each item to move. */
    size: 1000,
    /** Already live in the target (case 1). */
    existing: new Set<string>(),
    /** In the target's trash (case 2). */
    restore: new Set<string>(),
    /** Refused by the plan with this reason. */
    refused: new Map<string, string>(),
    /** Failed by the job with this reason. */
    fail: new Map<string, string>(),
    /** Failed to go to the trash after being placed in the target (`sourcePending`): they stay in peter. */
    pending: new Set<string>(),
    /** Unfinished moves the plan reports (and the listing as `interruptedMoves`); `problem` when they cannot be finished. */
    interrupted: [] as { from: string; to: string; id: number; problem?: string }[],
    /** The hashes of each transfer job, by job id. */
    jobHashes: new Map<number, string[]>(),
    nextRecord: 50,
  };

  /** A transfer job object as the server sends it (the source is `library`, the target `to`). */
  function transferJobJSON(p: { id: number; to: string; library?: string; importId: number } & Record<string, unknown>): Job {
    const { id, to, library = 'peter', importId, ...rest } = p;
    return {
      id,
      library,
      kind: 'transfer',
      to,
      state: 'running',
      move: false,
      startedAt: new Date().toISOString(),
      finishedAt: null,
      stats: { ...ZERO_STATS },
      sessionId: null,
      importId,
      progress: null,
      result: null,
      logPath: '',
      error: null,
      exitCode: null,
      ...rest,
    } as unknown as Job;
  }

  /** The checks every transfer request shares; a Response when refused. */
  function transferRefusal(body: Record<string, unknown>): Response | null {
    if (ws.mode !== 'workspace') return json({ error: 'single-library mode has no other library' }, 400);
    const to = body.to;
    if (typeof to !== 'string' || to === '') return json({ error: 'to: expected a library id' }, 400);
    if (to === 'peter') return json({ error: 'the target library is peter itself' }, 400);
    if (!libs.some((l) => l.id === to)) return json({ error: `no library "${to}" in the workspace` }, 400);
    const hs = body.hashes;
    if (!Array.isArray(hs) || hs.length === 0 || hs.length > 2000) return json({ error: 'hashes: expected 1-2000 hashes' }, 400);
    return null;
  }

  function transferPlan(body: Record<string, unknown>): Response {
    const bad = transferRefusal(body);
    if (bad) return bad;
    const hs = body.hashes as string[];
    const out = { move: { count: 0, bytes: 0 }, existing: { count: 0 }, restore: { count: 0 }, refused: [] as { hash: string; error: string }[] };
    for (const h of hs) {
      const x = items.find((i) => i.hash === h);
      const why = transfer.refused.get(h) ?? (!x ? 'not in the catalog' : x.deletedAt ? 'already in the trash in peter' : null);
      if (why) out.refused.push({ hash: h, error: why });
      else if (transfer.existing.has(h)) out.existing.count++;
      else if (transfer.restore.has(h)) out.restore.count++;
      else {
        out.move.count++;
        out.move.bytes += transfer.size;
      }
    }
    return json({ ...out, sameVolume: transfer.sameVolume, free: transfer.sameVolume ? null : transfer.free, interrupted: transfer.interrupted });
  }

  function transferJob(body: Record<string, unknown>): Response {
    const bad = transferRefusal(body);
    if (bad) return bad;
    const target = libs.find((l) => l.id === body.to);
    if (target?.state === 'busy') return json({ error: `${target.name} is in use by another picmover process` }, 409);
    if (backup.jobs.some((j) => j.state === 'running' || j.state === 'queued')) return json({ error: 'another job is running' }, 409);
    const job = transferJobJSON({ id: ++jobId, to: String(body.to), importId: transfer.nextRecord++ });
    transfer.jobHashes.set(job.id, [...(body.hashes as string[])]);
    backup.jobs.unshift(job);
    return json(job, 201);
  }

  /**
   * Ends transfer job `id` as the server does: the moved items leave
   * peter (gone on one volume, in its trash across volumes), duplicates
   * and restored ones go to its trash; `done` carries `result`.
   */
  function finishTransfer(id: number, p: { exitCode?: number; state?: string; error?: string | null; failedCap?: number } = {}): Job {
    const i = backup.jobs.findIndex((j) => j.id === id);
    const before = backup.jobs[i] as (Job & { to?: string }) | undefined;
    if (!before) throw new Error(`no fake job ${id}`);
    const hashes = transfer.jobHashes.get(id) ?? [];
    let moved = 0;
    let existing = 0;
    let restored = 0;
    let sourcePending = 0;
    const failed: { hash: string; error: string }[] = [];
    const cancelled = p.exitCode === 130 || p.state === 'cancelled';
    for (const [n, h] of hashes.entries()) {
      if (cancelled && n >= Math.ceil(hashes.length / 2)) break;
      const why = transfer.fail.get(h);
      if (why) {
        failed.push({ hash: h, error: why });
        continue;
      }
      const x = items.find((it) => it.hash === h);
      if (transfer.existing.has(h)) existing++;
      else if (transfer.restore.has(h)) restored++;
      else moved++;
      if (transfer.pending.has(h)) {
        sourcePending++;
        continue; // the original is still live here
      }
      if (!x) continue;
      if (transfer.sameVolume && !transfer.existing.has(h) && !transfer.restore.has(h)) items.splice(items.indexOf(x), 1);
      else x.deletedAt = '2026-10-10T11:59:00';
    }
    const exitCode = p.exitCode ?? (failed.length || sourcePending ? 2 : 0);
    const state = p.state ?? (exitCode === 0 || exitCode === 2 ? 'done' : exitCode === 130 ? 'cancelled' : 'failed');
    const cap = p.failedCap ?? 200;
    const notes: string[] = [];
    if (failed.length) notes.push(`${failed.length} ${failed.length === 1 ? 'file' : 'files'} could not be moved (see the job log)`);
    if (sourcePending) {
      notes.push(
        `${sourcePending} ${sourcePending === 1 ? 'photo was' : 'photos were'} moved to ${before.to}, but ${sourcePending === 1 ? 'its original' : 'their originals'} could not be moved to the trash in ${before.library}; finish the move from the Libraries page`,
      );
    }
    const error = p.error !== undefined ? p.error : exitCode === 2 && notes.length ? notes.join('; ') : null;
    const done = transferJobJSON({
      id,
      to: before.to ?? '',
      library: before.library,
      importId: before.importId ?? 0,
      startedAt: before.startedAt,
      finishedAt: new Date().toISOString(),
      state,
      exitCode,
      error,
      progress: { phase: before.to && hashes.length === 0 ? 'recover' : 'move', files: hashes.length, totalFiles: hashes.length, bytes: 0, totalBytes: 0, current: '' },
      result: { moved, existing, restored, failed: failed.slice(0, cap), failedCount: failed.length, sourcePending, record: before.importId ?? null, targetLibrary: before.to ?? '' },
      logPath: `/media/${before.to ?? 'x'}/.picmover/logs/transfer_2026-10-10_12-00-00.log`,
    });
    backup.jobs[i] = done;
    if (hashes.length === 0 && exitCode === 0) {
      transfer.interrupted = transfer.interrupted.filter((m) => m.to !== before.to);
      for (const l of libs) if (l.interruptedMoves) l.interruptedMoves = l.interruptedMoves.filter((m) => m.to !== before.to);
    }
    const es = [...FakeEventSource.instances].reverse().find((e) => e.url === `/api/jobs/${id}/events`);
    es?.emit('done', done);
    return done;
  }

  const libs: Lib[] =
    mode === 'single'
      ? [{ id: 'peter', name: 'peter', path: '/media/peter', state: 'open', summary: summary(8) }]
      : [
          {
            id: 'peter',
            name: 'peter',
            path: '/media/peter',
            state: 'closed',
            summary: summary(8),
            cover: hashOf(1),
            backup: { lastBackupAt: unix('2026-10-10T10:00:00'), behind: 3, targets: 2, label: 'Red WD drive' },
          },
          {
            id: 'penny',
            name: 'penny',
            path: '/media/penny',
            state: 'busy',
            summary: summary(3),
            holder: { pid: 812, cmd: 'import', since: '2026-10-10T09:00:00' },
            backup: { lastBackupAt: unix('2026-08-01T10:00:00'), behind: { new: 4, changed: 1, moved: 0, bytes: 5000 }, targets: 1 },
          },
          { id: 'broken', name: 'broken', path: '/media/broken', state: 'error', error: 'media.db needs a rebuild' },
        ];
  const folders = mode === 'single' ? [] : [{ id: 'scans', name: 'scans', path: '/media/scans', state: 'folder' }];
  let jobId = 0;

  const live = () => items.filter((x) => !x.deletedAt);

  /** The current groups: live members only, at least two, not wholly ignored (as the server's union-find, simplified). */
  function similarGroups(): Item[][] {
    const out: Item[][] = [];
    for (const g of similar.groups) {
      const members = g.map((h) => items.find((x) => x.hash === h && !x.deletedAt)).filter((x): x is Item => !!x);
      if (members.length < 2) continue;
      let open = false;
      for (let i = 0; i < members.length && !open; i++)
        for (let j = i + 1; j < members.length && !open; j++) if (!ignoredPairs.has(pairKey(members[i]!.hash, members[j]!.hash))) open = true;
      if (open) out.push(members);
    }
    return out;
  }
  const wire = (x: Item): MediaItem => {
    const { tagIds: _t, ...rest } = x;
    return rest;
  };
  const ym = (x: Item) => {
    const m = /^(\d{4})-(\d{2})/.exec(x.takenAt ?? '');
    return m ? [Number(m[1]), Number(m[2])] : [0, 0];
  };

  /** As the server (docs/design/imports.md): rebuild and record runs keep no items. */
  const keepsItems = (r: FakeImport) => r.mode !== 'rebuild' && r.mode !== 'record';
  /** The live rows among hashes and the newest of them (taken_at DESC, id DESC, undated last), as catalog.LiveSummary. */
  const liveSummary = (hashes: string[]) => {
    const rows = live().filter((x) => hashes.includes(x.hash));
    const newest = [...rows].sort((a, b) => {
      if (!a.takenAt !== !b.takenAt) return a.takenAt ? -1 : 1;
      if (a.takenAt && b.takenAt && a.takenAt !== b.takenAt) return a.takenAt < b.takenAt ? 1 : -1;
      return b.id - a.id;
    })[0];
    return { items: rows.length, cover: newest?.hash ?? null };
  };
  /** The record object: the stored summary (list) or the live one (one record), null for a record without items. */
  const importJSON = (r: FakeImport, how: 'stored' | 'live') => {
    const { hashes, items: _i, cover: _c, ...rest } = r;
    if (!keepsItems(r)) return { ...rest, items: null, cover: null };
    if (how === 'live') return { ...rest, ...liveSummary(hashes) };
    return { ...rest, items: r.items ?? null, cover: r.cover ?? null };
  };
  // The stored summary, as FinishImport wrote it: the item count and the newest item.
  for (const r of imports) {
    if (r.items !== undefined) continue;
    r.items = keepsItems(r) ? r.hashes.length : null;
    r.cover = keepsItems(r) ? liveSummary(r.hashes).cover : null;
  }

  function select(q: URLSearchParams): Item[] {
    let list = q.get('trashed') === '1' ? items.filter((x) => !!x.deletedAt) : live();
    const imp = Number(q.get('import'));
    if (imp) list = list.filter((x) => imports.find((r) => r.id === imp)?.hashes.includes(x.hash));
    const album = Number(q.get('album'));
    if (album) list = list.filter((x) => albums.find((a) => a.id === album)?.hashes.includes(x.hash));
    const rating = Number(q.get('rating'));
    if (rating) list = list.filter((x) => (x.rating ?? 0) >= rating);
    for (const t of q.getAll('tag')) list = list.filter((x) => x.tagIds.includes(Number(t)));
    // Months of any year, by the wall-clock taken time; undated never match.
    const months = q.get('months');
    if (months) {
      const set = new Set(months.split(',').map(Number));
      list = list.filter((x) => !!x.takenAt && set.has(Number(x.takenAt.slice(5, 7))));
    }
    // The dimension filter on the longer edge; unknown dimensions never match.
    const minPx = Number(q.get('minpx') ?? 0);
    const maxPx = Number(q.get('maxpx') ?? 0);
    if (minPx || maxPx) {
      list = list.filter((x) => x.w > 0 && x.h > 0 && (!minPx || Math.max(x.w, x.h) >= minPx) && (!maxPx || Math.max(x.w, x.h) <= maxPx));
    }
    return list;
  }

  function lib(path: string, q: URLSearchParams, method: string, body: Record<string, unknown>): Response {
    const hs = (body.hashes as string[] | undefined) ?? [];
    // The import filter as the server: a malformed id is 400, an unknown one 404, empty is no filter.
    const imp = q.get('import');
    if (imp) {
      if (!/^[1-9]\d*$/.test(imp)) return json({ error: `invalid import "${imp}": want an import id` }, 400);
      if (!imports.some((r) => String(r.id) === imp)) return json({ error: 'no such import' }, 404);
    }
    // The dimension filter as the server: 1..1 000 000, minpx <= maxpx.
    for (const k of ['minpx', 'maxpx']) {
      const v = q.get(k);
      if (v && (!/^[+-]?\d+$/.test(v) || Number(v) < 1 || Number(v) > 1_000_000)) {
        return json({ error: `invalid ${k} "${v}": want a size in pixels 1-1000000` }, 400);
      }
    }
    // The months filter as the server: month numbers 1-12 separated by commas.
    const months = q.get('months');
    if (months !== null && months !== '' && !months.split(',').every((m) => /^[+-]?\d+$/.test(m) && Number(m) >= 1 && Number(m) <= 12)) {
      return json({ error: `invalid months "${months}": want month numbers 1-12 separated by commas` }, 400);
    }
    const [lo, hi] = [Number(q.get('minpx') || 0), Number(q.get('maxpx') || 0)];
    if (lo && hi && lo > hi) return json({ error: `invalid minpx ${lo}: greater than maxpx ${hi}` }, 400);
    // Renditions (docs/design/renditions.md), as the server answers them:
    // a known hash gets a tiny image (the thumb and preview JPEG, the
    // large WebP); a hash in largeFallback has a large that could not be
    // derived, answered with the thumb bytes and no-store.
    const rm = /^(thumb|large|preview)\/([0-9a-f]+)$/.exec(path);
    if (rm && method === 'GET') {
      const [, kind, hash] = rm;
      if (!items.some((x) => x.hash === hash)) return json({ error: `no ${kind === 'large' ? 'large thumbnail' : kind === 'thumb' ? 'thumbnail' : 'preview'}` }, 404);
      if (kind === 'large' && largeFallback.has(hash!)) return new Response(TINY_JPEG, { headers: { 'Content-Type': 'image/jpeg', 'Cache-Control': 'no-store' } });
      const webp = kind === 'large';
      return new Response(webp ? TINY_WEBP : TINY_JPEG, { headers: { 'Content-Type': webp ? 'image/webp' : 'image/jpeg', 'Cache-Control': 'private, max-age=604800' } });
    }
    if (path === 'imports' && method === 'GET') return json({ imports: imports.map((r) => importJSON(r, 'stored')) });
    const im = /^imports\/([^/]+)(\/delete)?$/.exec(path);
    if (im) {
      // A malformed id is 404 like an unknown one (pathID, as for albums).
      if (!/^[1-9]\d*$/.test(im[1] ?? '')) return json({ error: 'no such import' }, 404);
      const r = imports.find((x) => x.id === Number(im[1]));
      if (!r) return json({ error: `not found: import ${im[1]}` }, 404);
      if (method === 'GET' && !im[2]) return json(importJSON(r, 'live'));
      if (method !== 'POST') return json({ error: 'not found' }, 404);
      if (im[2]) {
        imports.splice(imports.indexOf(r), 1);
        return json({});
      }
      if (typeof body.name !== 'string') return json({ error: 'name is required' }, 400);
      const name = body.name.trim();
      if (/[\p{Cc}\u2028\u2029\uFFFD]/u.test(name)) return json({ error: 'invalid name: control characters are not allowed' }, 400);
      if ([...name].length > 200) return json({ error: 'invalid name: want 1-200 characters' }, 400);
      r.name = name;
      return json(importJSON(r, 'live'));
    }
    if (method === 'GET' && path === 'library') {
      return json({
        root: '/media/peter',
        version: 'test',
        summary: { ...summary(live().length), deleted: items.length - live().length },
        thumbs: { ...thumbCounts },
        cameras: [],
        ffmpeg: true,
        userdb: { ratings: items.filter((x) => x.rating).length, tags: tags.length, albums: albums.length, trashed: userdb.trashed ?? items.length - live().length, ignoredPairs: ignoredPairs.size },
        similar: { at: 1760090000, distance: 6, items: 8, pairs: 1 },
      });
    }
    if (method === 'GET' && path === 'timeline') {
      const m = new Map<string, { year: number; month: number; count: number }>();
      for (const x of select(q)) {
        const [y, mo] = ym(x) as [number, number];
        const k = `${y}-${mo}`;
        const b = m.get(k) ?? { year: y, month: mo, count: 0 };
        b.count++;
        m.set(k, b);
      }
      return json({ buckets: [...m.values()].sort((a, b) => b.year - a.year || b.month - a.month) });
    }
    if (method === 'GET' && path === 'media') {
      const y = Number(q.get('year'));
      const mo = Number(q.get('month'));
      return json({ items: select(q).filter((x) => ym(x)[0] === y && ym(x)[1] === mo).map(wire) });
    }
    if (method === 'GET' && path.startsWith('media/')) {
      const x = items.find((i) => i.id === Number(path.slice(6)));
      if (!x) return json({ error: 'not found' }, 404);
      return json({
        ...wire(x),
        rating: x.rating ?? 0,
        relPath: `image/2024/07/${x.id}.jpg`,
        ext: '.jpg',
        size: 1000,
        offset: null,
        takenAtSource: 'exif',
        orientation: 1,
        cameraMake: '',
        cameraModel: '',
        cameraType: '',
        lens: '',
        originalPath: '',
        importedAt: null,
        sessionId: null,
        phash: null,
        thumbState: 'ok',
        thumbError: '',
        displayable: true,
        tags: tags.filter((t) => x.tagIds.includes(t.id)),
        albums: albums.filter((a) => a.hashes.includes(x.hash)).map((a) => ({ id: a.id, name: a.name })),
        imports: imports
          .filter((r) => r.hashes.includes(x.hash))
          .map((r) => ({ id: r.id, name: r.name, startedAt: r.startedAt, mode: r.mode, sourceName: r.sourceName })),
        deletedAt: x.deletedAt ?? null,
        video: null,
        canConvert: false,
        canOpen: false,
      });
    }
    if (method === 'GET' && path === 'tags') return json({ tags: tags.map((t) => ({ ...t, count: items.filter((x) => x.tagIds.includes(t.id)).length })) });
    if (method === 'GET' && path === 'albums') {
      return json({ albums: albums.map((a) => ({ id: a.id, name: a.name, count: a.hashes.length, cover: a.cover ?? a.hashes[0] ?? null, createdAt: '', updatedAt: '' })) });
    }
    if (method === 'GET' && path === 'similar') {
      const all = similarGroups();
      const offset = Number(q.get('offset') ?? 0);
      const limit = Number(q.get('limit') ?? 50);
      const groups = all.slice(offset, offset + limit).map((g) => ({
        maxDistance: 3,
        items: g.map((x, i) => ({ ...wire(x), size: 1000 + i, width: x.w, height: x.h, relPath: `image/2024/06/${x.id}.jpg` })),
      }));
      return json({ groups, total: all.length, at: 1760090000, distance: 6 });
    }
    if (method === 'GET' && path === 'jobs') return json({ jobs: backup.jobs.filter((j) => j.library === 'peter') });
    if (path === 'backups' || path.startsWith('backups/')) return backups(path, q, method, body);
    if (method !== 'POST') return json({ error: 'not found' }, 404);
    // Trash and user data changes wait for no move: refused while one runs on the library.
    if (
      /^(ratings|tags(\/\d+(\/delete)?)?|media\/tags|albums(\/\d+(\/items|\/delete)?)?|trash(\/restore|\/delete)?|similar\/ignore)$/.test(path) &&
      backup.jobs.some((j) => j.kind === 'transfer' && (j.state === 'running' || j.state === 'queued') && (j.library === 'peter' || j.to === 'peter'))
    ) {
      return json({ error: 'a move is running on this library; try again when it has finished' }, 409);
    }
    switch (path) {
      case 'ratings':
        for (const x of items) if (hs.includes(x.hash)) x.rating = (body.rating as number) || undefined;
        return json({ updated: hs.length });
      case 'media/tags': {
        const added: { id: number; name: string }[] = [];
        for (const name of (body.create as string[]) ?? []) {
          const t = tags.find((y) => y.name.toLowerCase() === name.toLowerCase()) ?? { id: nextTag++, name };
          if (!tags.includes(t)) tags.push(t);
          added.push(t);
        }
        for (const id of (body.add as number[]) ?? []) {
          const t = tags.find((y) => y.id === id);
          if (t) added.push(t);
        }
        for (const x of items) {
          if (!hs.includes(x.hash)) continue;
          for (const t of added) if (!x.tagIds.includes(t.id)) x.tagIds.push(t.id);
          x.tagIds = x.tagIds.filter((id) => !((body.remove as number[]) ?? []).includes(id));
        }
        return json({ tags: added });
      }
      case 'albums': {
        const a = { id: nextAlbum++, name: String(body.name), hashes: [...hs], cover: null };
        albums.push(a);
        return json({ id: a.id, name: a.name, count: a.hashes.length, cover: null, createdAt: '', updatedAt: '' }, 201);
      }
      case 'trash': {
        const failed: { hash: string; error: string }[] = [];
        for (const h of hs) {
          const x = items.find((i) => i.hash === h);
          if (!x || x.deletedAt) failed.push({ hash: h, error: 'already in the trash' });
          else x.deletedAt = '2026-10-10T11:58:00';
        }
        return json({ trashed: hs.length - failed.length, failed });
      }
      case 'trash/restore': {
        const failed: { hash: string; error: string }[] = [];
        for (const h of hs) {
          const x = items.find((i) => i.hash === h);
          if (!x) failed.push({ hash: h, error: 'not in the catalog' });
          else if (!x.deletedAt) failed.push({ hash: h, error: 'not in the trash' });
          else x.deletedAt = null;
        }
        return json({ restored: hs.length - failed.length, failed });
      }
      case 'trash/delete': {
        if (!body.confirm) return json({ error: 'confirm required' }, 400);
        const gone = body.all ? items.filter((x) => x.deletedAt) : items.filter((x) => hs.includes(x.hash) && x.deletedAt);
        for (const g of gone) items.splice(items.indexOf(g), 1);
        return json({ deleted: gone.length, failed: [] });
      }
      case 'similar/ignore': {
        // The group form: every pair of the current group containing the hash.
        if (typeof body.group !== 'string') return json({ error: 'group: expected a hash' }, 400);
        const g = similarGroups().find((m) => m.some((x) => x.hash === body.group));
        if (!g) return json({ error: 'no such group' }, 404);
        const before = ignoredPairs.size;
        for (let i = 0; i < g.length; i++) for (let j = i + 1; j < g.length; j++) ignoredPairs.add(pairKey(g[i]!.hash, g[j]!.hash));
        return json({ ignored: ignoredPairs.size - before });
      }
      case 'transfer/plan':
        return transferPlan(body);
      case 'jobs':
        if (body.kind === 'transfer') return transferJob(body);
        return backupJob(body) ?? json({ id: ++jobId, kind: body.kind, state: 'running', library: 'peter', startedAt: '2026-10-10T12:00:00Z', finishedAt: null, stats: {}, error: null }, 201);
    }
    const am = /^albums\/(\d+)(\/items|\/delete)?$/.exec(path);
    if (am) {
      const a = albums.find((x) => x.id === Number(am[1]));
      if (!a) return json({ error: 'not found' }, 404);
      if (am[2] === '/items') {
        for (const h of (body.add as string[]) ?? []) if (!a.hashes.includes(h)) a.hashes.push(h);
        a.hashes = a.hashes.filter((h) => !((body.remove as string[]) ?? []).includes(h));
        return json({ count: a.hashes.length });
      }
      if (am[2] === '/delete') {
        albums.splice(albums.indexOf(a), 1);
        return json({});
      }
      if (typeof body.name === 'string') a.name = body.name;
      if ('cover' in body) a.cover = body.cover as string | null;
      return json({ id: a.id, name: a.name, count: a.hashes.length, cover: a.cover, createdAt: '', updatedAt: '' });
    }
    return json({ error: 'not found' }, 404);
  }

  /** The /api/libraries body; libraries and folders live under the current root. */
  function listing() {
    if (ws.mode === 'unset') return { root: '', mode: 'unset', libraries: [], folders: [] };
    if (ws.mode === 'single') return { root: ws.root, mode: 'single', libraries: ws.single ? [ws.single] : libs, folders: ws.single ? [] : folders };
    const at = <T extends { id?: string; path: string }>(x: T): T => ({ ...x, path: `${ws.root === '/' ? '' : ws.root}/${x.id ?? ''}` });
    return { root: ws.root, mode: ws.mode, libraries: ws.root === '/media' ? libs : libs.map(at), folders: ws.root === '/media' ? folders : folders.map(at) };
  }

  /** The desktop app routes (404 unless `app.info` is set), checked as the server does. */
  function appRoute(p: string, method: string, body: Record<string, unknown>): Response {
    if (p === '/api/app' && method === 'GET' && app.fail > 0) {
      app.fail--;
      return json({ error: 'internal error' }, 500);
    }
    const info = app.info;
    if (!info) return json({ error: 'not found' }, 404);
    if (p === '/api/app' && method === 'GET') return json(info);
    if (p === '/api/app/open' && method === 'POST') return new Response(null, { status: 204 });
    if (p === '/api/app/workspace' && method === 'POST') {
      const path = body.path;
      if (typeof path !== 'string' || path === '' || !validTargetPath(path)) return json({ error: 'path: expected an absolute folder path' }, 400);
      if (path.split('/').includes('.picmover')) return json({ error: "That folder is inside a library's .picmover directory; choose the folder that holds your libraries." }, 400);
      const running = backup.jobs.filter((j) => j.state === 'running' || j.state === 'queued').length;
      if (running) return json({ error: 'a job is running', jobs: running }, 409);
      if (libraryFolders.has(path)) {
        // As the server: a folder with media.db is served as one library (single mode), or refused 409 {error}.
        const name = path.slice(path.lastIndexOf('/') + 1);
        const known = libs.find((l) => l.path === path);
        if (known?.state === 'busy') return json({ error: `The library is in use by another picmover process (pid ${known.holder?.pid ?? 0}, ${known.holder?.cmd ?? ''}, since ${known.holder?.since ?? ''}).` }, 409);
        if (known?.state === 'error') return json({ error: `The library cannot be opened: ${known.error ?? 'unreadable'}` }, 409);
        for (const l of libs) if (l.state === 'open') l.state = 'closed';
        ws.mode = 'single';
        ws.root = path;
        ws.single = { ...(known ?? { id: name, name, path, summary: summary(0) }), state: 'open' };
        app.info = { ...info, workspace: path };
        return json(listing());
      }
      ws.mode = 'workspace';
      ws.root = path;
      ws.single = null;
      for (const l of libs) if (l.state === 'open') l.state = 'closed';
      app.info = { ...info, workspace: path };
      return json(listing());
    }
    return json({ error: 'not found' }, 404);
  }

  /**
   * Sharing on the home Wi-Fi, admin side (docs/design/lan-share.md
   * "Admin API"): the global switch and the shares with their devices, as
   * the server would keep them. `supported: false` answers 404 (an older
   * server). Tests pair phones with `pairPhone`, add approval requests to
   * `requests`, and change shares in place.
   */
  const sharing = {
    supported: true,
    state: 'off' as 'off' | 'on' | 'paused' | 'error',
    reason: '',
    port: 7443,
    address: '192.168.1.20',
    addresses: [
      { ip: '192.168.1.20', iface: 'wlp3s0', subnet: '192.168.1.0/24', wireless: true },
      { ip: '10.0.0.5', iface: 'enp0s31f6', subnet: '10.0.0.0/24', wireless: false },
    ] as { ip: string; iface: string; subnet: string; virtual?: boolean; wireless?: boolean }[],
    fingerprint: { leaf: 'AB:CD:EF:01:23:45:67:89', ca: '12:34:56:78:9A:BC:DE:F0' },
    keepOn: false,
    mdns: false,
    autoOff: { mode: 'quit', until: null as string | null },
    managedElsewhere: null as { pid: number; cmd: string } | null,
    /** One plain-text string, lines separated by \n (as the server sends it on Linux). */
    firewallHint:
      'If the phone cannot connect, allow the port in the firewall (and undo it when done):\n' +
      'ufw:       sudo ufw allow from 192.168.1.0/24 to any port 7443 proto tcp\n' +
      'firewalld: sudo firewall-cmd --zone=home --add-port=7443/tcp --permanent && sudo firewall-cmd --reload',
    /** POST {enabled: true} fails with this reason (state error, still 200), e.g. a busy port. */
    startError: '',
    /** Libraries held by another process (busy: true on the share's library). */
    busy: new Set<string>(),
    shares: [] as FakeShare[],
    /** Approval requests per share id. */
    requests: new Map<string, { id: string; name: string; browser: string; address: string; at: string }[]>(),
    /** Open pairings per share id (the token's tail), as issued. */
    pairings: new Map<string, { token: string; expiresAt: string }>(),
    nextId: 1,
  };

  const sharingNow = () => new Date().toISOString();
  function shareStateOf(s: FakeShare): string {
    if (s.unknownKind || s.elsewhere) return 'unavailable';
    if (s.paused) return 'paused';
    if (s.expiresAt && Date.parse(s.expiresAt) <= Date.now()) return 'expired';
    if (s.kind === 'album' && (s.albumMissing || !albums.some((a) => a.id === s.album))) return 'gone';
    if (s.kind === 'album' && !libs.some((l) => l.id === s.libraries[0])) return 'gone';
    return 'active';
  }
  function shareJSON(s: FakeShare) {
    // The server names libraries by their folder id; busy and missing are absent when false.
    const libRef = (id: string) => {
      const l = libs.find((x) => x.id === id);
      return l ? { id, name: id, ...(sharing.busy.has(id) ? { busy: true } : {}) } : { id, name: id, missing: true };
    };
    const a = albums.find((x) => x.id === s.album);
    const scope =
      s.kind === 'album'
        ? {
            library: libRef(s.libraries[0] ?? ''),
            album: a && !s.albumMissing ? { id: a.id, name: a.name, count: s.albumCount ?? a.hashes.length } : { id: s.album, name: '', count: 0, missing: true },
          }
        : { libraries: s.libraries.map(libRef) };
    const open = sharing.pairings.get(s.id);
    return {
      id: s.id,
      name: s.name,
      kind: s.unknownKind ?? s.kind,
      scope: s.unknownKind ? {} : scope,
      capabilities: { ...s.capabilities },
      expiresAt: s.expiresAt,
      state: shareStateOf(s),
      approve: s.approve,
      createdAt: s.createdAt,
      devices: s.devices.map((d) => ({ ...d })),
      pairing: open ? { expiresAt: open.expiresAt } : null,
      ...(s.notice ? { notice: s.notice } : {}),
      requests: (sharing.requests.get(s.id) ?? []).length,
    };
  }
  /** GET /api/share/status: the pill's counts over the shares phones can use now. */
  function sharingSummaryJSON() {
    const active = sharing.shares.filter((s) => {
      const st = shareStateOf(s);
      return st === 'active' || st === 'gone';
    });
    return {
      state: sharing.state,
      ...(sharing.reason ? { reason: sharing.reason } : {}),
      ...(sharing.state === 'on' ? { url: `https://${sharing.address}:${sharing.port}/` } : {}),
      shares: active.length,
      phones: active.reduce((n, s) => n + s.devices.length, 0),
      autoOff: sharing.autoOff.until ? sharing.autoOff : { mode: sharing.autoOff.mode },
      ...(sharing.managedElsewhere ? { managedElsewhere: sharing.managedElsewhere } : {}),
    };
  }
  function sharingJSON() {
    const on = sharing.state === 'on';
    return {
      state: sharing.state,
      ...(sharing.reason ? { reason: sharing.reason } : {}),
      ...(on ? { url: `https://${sharing.address}:${sharing.port}/` } : {}),
      ...(on && sharing.mdns ? { mdnsUrl: `https://laptop.local:${sharing.port}/` } : {}),
      port: sharing.port,
      address: sharing.address,
      addresses: sharing.addresses,
      fingerprint: sharing.fingerprint,
      keepOn: sharing.keepOn,
      mdns: sharing.mdns,
      autoOff: sharing.autoOff.until ? sharing.autoOff : { mode: sharing.autoOff.mode },
      ...(sharing.managedElsewhere ? { managedElsewhere: sharing.managedElsewhere } : {}),
      firewallHint: sharing.firewallHint,
      shares: sharing.shares.map(shareJSON),
    };
  }
  const CAPS = ['video', 'download', 'rate', 'location'] as const;
  /** The kind's defaults without `v`; with it, `v` taken whole (a missing capability is off), as the server does. */
  function capsFrom(v: unknown, kind: string): FakeShare['capabilities'] {
    const all = kind !== 'album';
    if (!v || typeof v !== 'object') return { video: true, download: all, rate: all, location: all };
    const c = { video: false, download: false, rate: false, location: false };
    for (const k of CAPS) c[k] = (v as Record<string, unknown>)[k] === true;
    return c;
  }
  /** Simulates a phone redeeming a pairing of share `sid` (the server side of POST /api/pair on the share listener). */
  function pairPhone(sid: string, name = "Anna's iPhone", browser = 'Safari on iPhone') {
    const s = sharing.shares.find((x) => x.id === sid);
    if (!s) throw new Error(`no fake share ${sid}`);
    const d = { id: `d_${(sharing.nextId++).toString(16).padStart(16, '0')}`, name, pairedAt: sharingNow(), lastSeen: sharingNow(), lastAddress: '192.168.1.34', browser };
    s.devices.push(d);
    sharing.pairings.delete(sid);
    return d;
  }
  /** Adds a share directly (as `picmover share create` would have). */
  function addShare(p: Partial<FakeShare> & { name: string; kind: 'libraries' | 'album' }): FakeShare {
    const s: FakeShare = {
      id: `s_${(sharing.nextId++).toString(16).padStart(16, '0')}`,
      libraries: p.kind === 'album' ? ['peter'] : ['peter'],
      album: 0,
      albumName: '',
      albumMissing: false,
      capabilities: capsFrom(undefined, p.kind),
      expiresAt: null,
      paused: false,
      approve: false,
      createdAt: sharingNow(),
      devices: [],
      ...p,
    };
    sharing.shares.push(s);
    return s;
  }

  function shareRoute(p: string, method: string, body: Record<string, unknown>): Response {
    if (!sharing.supported) return json({ error: 'not found' }, 404);
    const write = method === 'POST';
    if (write && sharing.managedElsewhere) {
      const me = sharing.managedElsewhere;
      return json({ error: `sharing is managed by picmover pid ${me.pid} (${me.cmd}); use its Share page`, managedElsewhere: me }, 409);
    }
    if (p === '/api/share' && method === 'GET') return json(sharingJSON());
    if (p === '/api/share/status' && method === 'GET') return json(sharingSummaryJSON());
    if (p === '/api/share' && write) {
      if (typeof body.address === 'string') {
        if (!sharing.addresses.some((a) => a.ip === body.address)) return json({ error: 'address: not a LAN address of this computer' }, 400);
        sharing.address = body.address;
      }
      if (body.port !== undefined) {
        if (typeof body.port !== 'number' || body.port < 1 || body.port > 65535) return json({ error: 'port: want 1-65535' }, 400);
        sharing.port = body.port;
      }
      if (body.enabled === false) {
        sharing.state = 'off';
        sharing.reason = '';
        sharing.pairings.clear();
      } else if (body.enabled === true) {
        // A failed start is still 200: the body's state and reason say why.
        sharing.state = sharing.startError ? 'error' : 'on';
        sharing.reason = sharing.startError;
      }
      if (typeof body.keepOn === 'boolean') sharing.keepOn = body.keepOn;
      if (typeof body.mdns === 'boolean') sharing.mdns = body.mdns;
      if (typeof body.autoOff === 'string') {
        if (!['never', '1h', '4h', 'quit'].includes(body.autoOff)) return json({ error: 'autoOff: want never, 1h, 4h or quit' }, 400);
        const h = body.autoOff === '1h' ? 1 : body.autoOff === '4h' ? 4 : 0;
        sharing.autoOff = { mode: body.autoOff, until: h ? new Date(Date.now() + h * 3600_000).toISOString() : null };
      }
      return json(sharingJSON());
    }
    if (p === '/api/share/cert/renew' && write) {
      sharing.fingerprint = { ...sharing.fingerprint, leaf: '99:88:77:66:55:44:33:22' };
      return json(sharingJSON());
    }
    if (p === '/api/share/shares' && write) {
      const sc = (body.scope ?? {}) as Record<string, unknown>;
      const name = typeof body.name === 'string' ? body.name.trim() : '';
      if (!name) return json({ error: 'invalid name' }, 400);
      if (sharing.shares.some((x) => x.name.toLowerCase() === name.toLowerCase())) return json({ error: 'a share with this name exists' }, 409);
      let s: FakeShare;
      if (sc.kind === 'libraries') {
        const ids = Array.isArray(sc.libraries) ? (sc.libraries as string[]) : [];
        if (!ids.length) return json({ error: 'scope: no libraries' }, 400);
        if (ids.some((id) => !libs.some((l) => l.id === id))) return json({ error: 'scope: unknown library' }, 400);
        s = addShare({ name, kind: 'libraries', libraries: ids });
      } else if (sc.kind === 'album') {
        const a = albums.find((x) => x.id === sc.album);
        if (sc.library !== 'peter' || !a) return json({ error: `unknown album ${String(sc.album)} in library "${String(sc.library)}"` }, 400);
        s = addShare({ name, kind: 'album', libraries: ['peter'], album: a.id, albumName: a.name, expiresAt: new Date(Date.now() + 30 * 86400_000).toISOString() });
      } else return json({ error: 'scope: kind must be "libraries" or "album"' }, 400);
      s.capabilities = capsFrom(body.capabilities, s.kind);
      if (typeof body.expiresIn === 'number') s.expiresAt = new Date(Date.now() + body.expiresIn * 1000).toISOString();
      else if (body.expiresAt === null) s.expiresAt = null;
      else if (typeof body.expiresAt === 'string') s.expiresAt = body.expiresAt;
      s.approve = body.approve === true;
      return json(shareJSON(s), 201);
    }
    const dm = /^\/api\/share\/devices\/([^/]+)(\/revoke)?$/.exec(p);
    if (dm && write) {
      for (const s of sharing.shares) {
        const d = s.devices.find((x) => x.id === dm[1]);
        if (!d) continue;
        if (dm[2]) {
          s.devices.splice(s.devices.indexOf(d), 1);
          return new Response(null, { status: 204 });
        }
        if (typeof body.name !== 'string' || !body.name.trim()) return json({ error: 'invalid name' }, 400);
        d.name = body.name.trim();
        return json({ ...d });
      }
      return json({ error: 'no such phone' }, 404);
    }
    const m = /^\/api\/share\/shares\/([^/]+)(\/delete|\/revoke-all|\/pairings|\/pairings\/cancel|\/requests(?:\/([^/]+))?)?$/.exec(p);
    if (!m) return json({ error: 'not found' }, 404);
    const s = sharing.shares.find((x) => x.id === m[1]);
    if (!s) return json({ error: 'no such share' }, 404);
    const sub = m[2] ?? '';
    if (method === 'GET' && sub === '/requests') return json(sharing.requests.get(s.id) ?? []);
    if (!write) return json({ error: 'not found' }, 404);
    switch (sub) {
      case '':
        if (body.kind !== undefined || body.album !== undefined || body.scope !== undefined)
          return json({ error: "the kind and an album share's album cannot change; create a new share" }, 400);
        if (Array.isArray(body.libraries)) {
          if (s.kind !== 'libraries') return json({ error: 'libraries: only a library share has libraries' }, 400);
          if (body.libraries.length === 0) return json({ error: 'libraries: at least one' }, 400);
        }
        if (typeof body.name === 'string') {
          const n = body.name.trim();
          if (sharing.shares.some((x) => x !== s && x.name.toLowerCase() === n.toLowerCase())) return json({ error: 'a share with this name exists' }, 409);
          s.name = n;
        }
        if (Array.isArray(body.libraries)) s.libraries = body.libraries as string[];
        if (body.capabilities !== undefined) s.capabilities = capsFrom(body.capabilities, s.kind);
        if ('expiresAt' in body) s.expiresAt = (body.expiresAt as string | null) ?? null;
        if (typeof body.paused === 'boolean') s.paused = body.paused;
        if (s.paused) sharing.pairings.delete(s.id);
        if (typeof body.approve === 'boolean') s.approve = body.approve;
        return json(shareJSON(s));
      case '/delete':
        sharing.shares.splice(sharing.shares.indexOf(s), 1);
        return new Response(null, { status: 204 });
      case '/revoke-all':
        s.devices = [];
        return json(shareJSON(s));
      case '/pairings': {
        if (sharing.state !== 'on') return json({ error: 'sharing on Wi-Fi is off; turn it on first' }, 409);
        if (shareStateOf(s) !== 'active') return json({ error: `the share is ${shareStateOf(s)}; phones cannot pair with it now` }, 409);
        const token = `tok${sharing.nextId++}`;
        const expiresAt = new Date(Date.now() + 120_000).toISOString();
        sharing.pairings.set(s.id, { token, expiresAt });
        return json({
          url: `https://${sharing.address}:${sharing.port}/#/pair/${token}`,
          ...(sharing.mdns ? { mdnsUrl: `https://laptop.local:${sharing.port}/#/pair/${token}` } : {}),
          code: 'K7QM-3XRD',
          qrSvg: '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 21 21"><path d="M0 0h7v7H0z"/></svg>',
          expiresAt,
        });
      }
      case '/pairings/cancel':
        sharing.pairings.delete(s.id);
        return new Response(null, { status: 204 });
      default: {
        const rid = m[3];
        const list = sharing.requests.get(s.id) ?? [];
        const r = list.find((x) => x.id === rid);
        if (!r) return json({ error: 'no such request' }, 404);
        sharing.requests.set(
          s.id,
          list.filter((x) => x !== r),
        );
        if (body.approve === true) pairPhone(s.id, r.name, r.browser);
        return new Response(null, { status: 204 });
      }
    }
  }

  function fetchImpl(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
    const url = new URL(String(input), 'http://127.0.0.1:7439');
    const method = init?.method ?? 'GET';
    const body = typeof init?.body === 'string' ? (JSON.parse(init.body) as Record<string, unknown>) : {};
    calls.push({ method, path: url.pathname + url.search, body: method === 'POST' ? body : null });
    const p = url.pathname;
    let res: Response;
    const unset = json({ error: 'no workspace', state: 'unset' }, 409);
    if (p === '/api/app' || p.startsWith('/api/app/')) {
      res = appRoute(p, method, body);
    } else if (p === '/api/share' || p.startsWith('/api/share/')) {
      res = shareRoute(p, method, body);
    } else if (p === '/api/libraries' && method === 'GET') {
      res = json(listing());
    } else if (ws.mode === 'unset' && (p.startsWith('/api/libraries') || p.startsWith('/api/l/'))) {
      res = unset;
    } else if (p === '/api/libraries' && method === 'POST') {
      const l: Lib = { id: String(body.name), name: String(body.name), path: `/media/${String(body.name)}`, state: 'open', summary: summary(0) };
      libs.push(l);
      res = json(l, 201);
    } else if (/^\/api\/libraries\/[^/]+\/cover$/.test(p) && method === 'GET') {
      const l = libs.find((x) => x.id === decodeURIComponent(p.split('/')[3] ?? ''));
      res = l?.cover ? new Response(new Uint8Array([0xff, 0xd8, 0xff, 0xd9]), { headers: { 'Content-Type': 'image/jpeg', ETag: '"c1"' } }) : json({ error: 'no cover' }, 404);
    } else if (/^\/api\/libraries\/[^/]+\/open$/.test(p)) {
      const id = decodeURIComponent(p.split('/')[3] ?? '');
      const l = libs.find((x) => x.id === id);
      const msg = openErrors.get(id);
      if (!l && folders.some((f) => f.id === id)) res = json({ error: 'not a library (no media.db)', state: 'folder' }, 409);
      else if (!l) res = json({ error: 'no such library' }, 404);
      else if (l.state === 'busy') res = json({ error: 'library is in use by another picmover process', state: 'busy', holder: l.holder }, 409);
      else if (l.state === 'error') res = json({ error: l.error ?? 'the catalog cannot be read', state: 'error' }, 409);
      else if (msg !== undefined) res = json({ error: msg, state: 'error' }, 409);
      else if (l.state === 'rebuilding') res = json({ error: 'the library is being rebuilt', state: 'rebuilding' }, 503);
      else {
        l.state = 'open';
        res = json(l);
      }
    } else if (/^\/api\/libraries\/[^/]+\/transfer\/resume$/.test(p) && method === 'POST') {
      const id = decodeURIComponent(p.split('/')[3] ?? '');
      const all = [...transfer.interrupted, ...(libs.find((l) => l.id === id)?.interruptedMoves ?? [])].filter((x) => x.to === id || x.from === id);
      const m = all.find((x) => !x.problem);
      if (!libs.some((l) => l.id === id)) res = json({ error: 'no such library' }, 404);
      else if (!all.length) res = json({ error: 'no interrupted move involves this library' }, 400);
      else if (!m) res = json({ error: all.map((x) => `the interrupted move from ${x.from} to ${x.to} could not be finished: ${x.problem}`).join('; ') }, 409);
      else if (backup.jobs.some((j) => j.state === 'running' || j.state === 'queued')) res = json({ error: 'another job is running' }, 409);
      else {
        const job = transferJobJSON({ id: ++jobId, to: id, library: m.from, importId: m.id, progress: { phase: 'recover', files: 0, totalFiles: 0, bytes: 0, totalBytes: 0, current: '' } });
        transfer.jobHashes.set(job.id, []);
        backup.jobs.unshift(job);
        res = json(job, 201);
      }
    } else if (/^\/api\/libraries\/[^/]+\/jobs$/.test(p)) {
      const id = decodeURIComponent(p.split('/')[3] ?? '');
      res = json({ id: ++jobId, kind: 'rebuild', state: 'running', library: id, startedAt: '2026-10-10T12:00:00Z', finishedAt: null, stats: {}, error: null }, 201);
    } else if (p === '/api/jobs') {
      res = json({ jobs: backup.jobs });
    } else if (/^\/api\/jobs\/\d+$/.test(p)) {
      const j = backup.jobs.find((x) => x.id === Number(p.split('/')[3]));
      res = j ? json(j) : json({ error: 'no such job' }, 404);
    } else if (/^\/api\/jobs\/\d+\/cancel$/.test(p) && method === 'POST') {
      res = json({});
    } else if (p === '/api/fs') {
      const at = url.searchParams.get('path') ?? '';
      res = at && !at.startsWith('/')
        ? json({ error: 'path must be absolute' }, 400)
        : at
        ? json({ path: at, parent: at === '/' ? '' : '/', dirs: [{ name: 'backup', path: `${at === '/' ? '' : at}/backup` }], mediaFiles: 0, otherFiles: 0 })
        : json({ path: '', dirs: [{ name: '/', path: '/' }, { name: 'mnt', path: '/mnt' }] });
    } else if (p.startsWith('/api/l/peter/')) {
      res = lib(p.slice('/api/l/peter/'.length), url.searchParams, method, body);
    } else if (p.startsWith('/api/l/')) {
      res = json({ error: 'library is not open', state: 'closed' }, 409);
    } else {
      res = json({ error: 'not found' }, 404);
    }
    return Promise.resolve(res);
  }

  function install() {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame', 'Date'] });
    vi.setSystemTime(new Date('2026-10-10T12:00:00'));
    vi.stubGlobal('fetch', vi.fn(fetchImpl));
    vi.stubGlobal('EventSource', FakeEventSource);
    vi.stubGlobal('ResizeObserver', FakeResizeObserver);
    FakeEventSource.instances = [];
  }

  /** POST calls to a path (relative to /api/), in order. */
  const posts = (path: string) => calls.filter((c) => c.method === 'POST' && c.path === `/api/${path}`);

  return { calls, items, tags, albums, imports, libs, folders, similar, openErrors, userdb, thumbCounts, largeFallback, backup, transfer, ws, libraryFolders, app, sharing, install, posts, finish, finishTransfer, pairPhone, addShare };
}

export type FakeServer = ReturnType<typeof fakeServer>;

// ---- The share listener (docs/design/lan-share.md "Share listener: routes") ----

/** An item of the fake share server (library "peter"). */
export interface ShareItem extends MediaItem {
  lat?: number;
  lon?: number;
  /** Camera model ("" for none). */
  camera?: string;
}

export interface FakeShareOptions {
  /** The share's capabilities; the kind's defaults when left out. */
  capabilities?: Partial<Record<'video' | 'download' | 'rate' | 'location', boolean>>;
  /** Library shares: the shared libraries (default: peter and penny). */
  libraries?: string[];
  /** Whether this browser holds a valid device cookie at the start (default: true). */
  paired?: boolean;
  /**
   * peter's timeline instead of its few items: these months (newest
   * first), their items made up when a month is asked for (unfiltered).
   */
  bulk?: { year: number; month: number; count: number }[];
}

/**
 * A stateful fake of the share listener's API for the phone app's tests:
 * one share of `kind` (a library share "My phone" over peter and penny, or
 * an album share "Summer guests" of peter's album "Summer"), a cookie that
 * pairing sets and revocation clears, the share's state (paused, expired,
 * gone, unavailable), scope and capability checks and redaction as the
 * design specifies them, and the filter whitelist (anything else is 400).
 * Pairing: `pairing.tokens` and `pairing.codes` are open (single use);
 * `pairing.approve` holds a redeem until `approve()`/`deny()`; a paired
 * browser gets 409 `{current: {share}}` without `replace`.
 */
export function fakeShareServer(kind: 'libraries' | 'album' = 'libraries', opts: FakeShareOptions = {}) {
  const calls: Call[] = [];
  const all = kind !== 'album';
  const caps = { video: true, download: all, rate: all, location: all, ...opts.capabilities };
  const libNames: Record<string, string> = { peter: 'Peter', penny: 'Penny' };
  const shared = kind === 'album' ? [] : (opts.libraries ?? ['peter', 'penny']);
  // peter: July 2024 ids 1..6 (3 is a video), June ids 7..8; penny: one item (20).
  const items: (ShareItem & { lib: string })[] = [];
  for (let i = 1; i <= 6; i++) {
    items.push({ id: i, hash: hashOf(i), type: i === 3 ? 'video' : 'image', takenAt: `2024-07-${String(20 - i).padStart(2, '0')}T10:00:00`, w: 4000, h: 3000, thumb: 'ok', thumbAt: 1700000000, lib: 'peter' });
  }
  for (let i = 7; i <= 8; i++) items.push({ id: i, hash: hashOf(i), type: 'image', takenAt: `2024-06-${String(20 - i).padStart(2, '0')}T10:00:00`, w: 4000, h: 3000, thumb: 'ok', lib: 'peter' });
  items.push({ id: 20, hash: hashOf(20), type: 'image', takenAt: '2023-01-05T10:00:00', w: 100, h: 100, thumb: 'ok', lib: 'penny' });
  const first = items[0]!;
  first.rating = 4;
  first.lat = 60.17;
  first.lon = 24.94;
  first.gps = true;
  items[2]!.dur = 7000;
  // More positions for the map: 2 and the video 3 next to 1 in Helsinki
  // (all in "Summer"), 7 in Paris (in "Secret"), penny's 20 in New York.
  const place = (id: number, lat: number, lon: number) => Object.assign(items.find((x) => x.id === id)!, { lat, lon, gps: true });
  place(2, 60.171, 24.941);
  place(3, 60.1705, 24.9405);
  place(7, 48.85, 2.35);
  place(20, 40.7, -74.0);
  // Cameras: peter's July on the Canon (the video has none), June on a
  // Pixel (outside the album "Summer"), penny on an iPhone.
  for (const x of items) x.camera = x.lib === 'penny' ? 'iPhone 12' : x.takenAt?.startsWith('2024-06') ? 'Pixel 7' : x.type === 'video' ? '' : 'Canon EOS R6';
  /** peter's tags: "beach" on 1 and 2, "private" only on 7 (outside the album). */
  const tags = [
    { id: 1, name: 'beach', hashes: [hashOf(1), hashOf(2)] },
    { id: 4, name: 'private', hashes: [hashOf(7)] },
  ];
  /** peter's albums; an album share covers "Summer" (2). */
  const albums = [
    { id: 2, name: 'Summer', hashes: [hashOf(1), hashOf(2), hashOf(3)] },
    { id: 5, name: 'Secret', hashes: [hashOf(7)] },
  ];
  const share = {
    name: kind === 'album' ? 'Summer guests' : 'My phone',
    expiresAt: kind === 'album' ? '2026-11-13T19:00:00+02:00' : null,
    /** '' active, else the 403 state. */
    state: '' as '' | 'paused' | 'expired' | 'gone' | 'unavailable',
  };
  const device = { id: 'd_3f9a1c2e7b5d4a10', name: "Anna's phone" };
  const cookie = { paired: opts.paired ?? true, otherShare: '' };
  /** Libraries answering 503 (rebuilding, held by another process). */
  const busy = new Set<string>();
  /** A move runs: ratings answer 409. */
  const moving = { on: false };
  /** The next `times` requests for an image or file answer `status` (429: rate limited), with Retry-After when set. */
  const mediaFail = { times: 0, status: 429, retryAfter: '' };
  function failMedia(): Response | null {
    if (mediaFail.times <= 0) return null;
    mediaFail.times--;
    const headers: Record<string, string> = { 'Content-Type': 'application/json' };
    if (mediaFail.retryAfter) headers['Retry-After'] = mediaFail.retryAfter;
    return new Response(JSON.stringify({ error: 'too many requests' }), { status: mediaFail.status, headers });
  }
  const pairing = {
    /** The share has its 20 phones: a redeem is 409 without `current`. */
    full: false,
    tokens: new Set<string>(['tok_' + 'a'.repeat(40)]),
    codes: new Set<string>(['K7QM3XRD']),
    /** Redeems wait for the computer while set. */
    approve: false,
    /** Answers 429 to the next redeem. */
    limited: false,
    /** The waiting redeem: approved (true), denied (false), or sharing turned off meanwhile ('off'). */
    pending: null as null | ((r: boolean | 'off') => void),
  };

  const visible = () =>
    items.filter((x) => (kind === 'album' ? x.lib === 'peter' && share.state !== 'gone' && albums[0]!.hashes.includes(x.hash) : shared.includes(x.lib)));
  const libsJSON = () =>
    shared.map((id) => ({ id, name: libNames[id] ?? id, cover: items.find((x) => x.lib === id)?.hash ?? null, total: items.filter((x) => x.lib === id).length }));
  const shareJSON = () => ({
    name: share.name,
    kind,
    capabilities: { ...caps },
    expiresAt: share.expiresAt,
    ...(kind === 'album'
      ? { album: { name: albums[0]!.name, count: visible().length, cover: albums[0]!.hashes[0] } }
      : { libraries: libsJSON() }),
  });
  const itemJSON = (x: ShareItem & { lib: string }): Record<string, unknown> => {
    const { lib: _l, lat: _a, lon: _o, camera: _c, rating, gps, ...rest } = x;
    return { ...rest, ...(caps.rate && rating ? { rating } : {}), ...(caps.location && gps ? { gps } : {}) };
  };
  const ym = (x: ShareItem) => {
    const m = /^(\d{4})-(\d{2})/.exec(x.takenAt ?? '');
    return m ? [Number(m[1]), Number(m[2])] : [0, 0];
  };
  const notFound = () => json({ error: 'not found' }, 404);
  /** The filter whitelist of timeline and media; media adds year and month. */
  const ALLOWED = new Set(['type', 'from', 'to', 'recur', 'months', 'rating', 'album', 'tag', 'camera', 'nocamera', 'gps', 'minpx', 'maxpx']);
  /** The scope's photos of a {lib} key: the album's live members, or the library's. */
  const inLib = (lib: string) => visible().filter((x) => kind === 'album' || x.lib === lib);

  function select(lib: string, q: URLSearchParams, extra: string[] = []): (ShareItem & { lib: string })[] | Response {
    for (const k of q.keys()) if (!ALLOWED.has(k) && !extra.includes(k)) return json({ error: `invalid filter "${k}"` }, 400);
    // The server's capability and scope checks, in its order.
    if (q.has('rating') && !caps.rate) return json({ error: 'invalid filter "rating": not allowed in this share' }, 400);
    if (q.has('gps') && !caps.location) return json({ error: 'invalid filter "gps": not allowed in this share' }, 400);
    if (q.has('album') && kind === 'album') return json({ error: 'invalid filter "album": this share is one album' }, 400);
    if (q.has('tag') && kind === 'album') return json({ error: 'invalid filter "tag": this share is one album' }, 400);
    if ((q.get('camera') ?? '').length > 200) return json({ error: 'invalid filter "camera": too long' }, 400);
    if (q.has('nocamera') && (q.get('nocamera') !== '1' || q.has('camera'))) return json({ error: 'invalid filter "nocamera"' }, 400);
    let list = inLib(lib);
    const aid = Number(q.get('album'));
    if (aid) {
      const a = lib === 'peter' ? albums.find((x) => x.id === aid) : undefined;
      if (!a) return notFound();
      list = list.filter((x) => a.hashes.includes(x.hash));
    }
    for (const v of q.getAll('tag')) {
      if (!/^[1-9]\d*$/.test(v)) return json({ error: `invalid tag "${v}": want a tag id` }, 400);
      const tg = lib === 'peter' ? tags.find((x) => x.id === Number(v)) : undefined;
      list = list.filter((x) => !!tg && tg.hashes.includes(x.hash));
    }
    const t = q.get('type');
    if (t) list = list.filter((x) => x.type === t);
    const rating = Number(q.get('rating'));
    if (rating) list = list.filter((x) => (x.rating ?? 0) >= rating);
    const from = q.get('from');
    if (from) list = list.filter((x) => (x.takenAt ?? '') >= from);
    const to = q.get('to');
    if (to) list = list.filter((x) => (x.takenAt ?? '9999') < to);
    const months = q.get('months');
    if (months) list = list.filter((x) => x.takenAt !== null && months.split(',').map(Number).includes(Number(x.takenAt.slice(5, 7))));
    const recur = q.get('recur');
    if (recur) {
      const [a, b = a] = recur.split('..') as [string, string?];
      list = list.filter((x) => {
        const md = x.takenAt?.slice(5, 10);
        if (!md) return false;
        return a <= b! ? md >= a && md <= b! : md >= a || md <= b!;
      });
    }
    const cam = q.get('camera');
    if (cam) list = list.filter((x) => x.camera === cam);
    if (q.get('nocamera') === '1') list = list.filter((x) => !x.camera);
    if (q.get('gps') === '1') list = list.filter((x) => x.gps === true);
    const minPx = Number(q.get('minpx'));
    if (minPx) list = list.filter((x) => Math.max(x.w, x.h) >= minPx);
    const maxPx = Number(q.get('maxpx'));
    if (maxPx) list = list.filter((x) => Math.max(x.w, x.h) <= maxPx);
    return list;
  }

  /** GET /api/l/{lib}/facets as the server computes it: inside the scope, gps with location, tags and albums in a library share. */
  function facetsOf(lib: string, real: string): Record<string, unknown> {
    const list = inLib(lib);
    const years = new Map<number, number>();
    const cams = new Map<string, number>();
    for (const x of list) {
      const y = ym(x)[0]!;
      if (y) years.set(y, (years.get(y) ?? 0) + 1);
      if (x.camera) cams.set(x.camera, (cams.get(x.camera) ?? 0) + 1);
    }
    const out: Record<string, unknown> = {
      total: list.length,
      types: { image: list.filter((x) => x.type === 'image').length, image_raw: list.filter((x) => x.type === 'image_raw').length, video: list.filter((x) => x.type === 'video').length },
      years: [...years].sort((a, b) => b[0] - a[0]).map(([year, count]) => ({ year, count })),
      undated: list.filter((x) => !x.takenAt).length,
      cameras: [...cams].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])).map(([model, count]) => ({ make: model.split(' ')[0], model, count })),
      nocamera: list.filter((x) => !x.camera).length,
    };
    if (caps.location) out.gps = list.filter((x) => x.gps).length;
    if (kind !== 'album') {
      const live = items.filter((x) => x.lib === real);
      const counted = (g: { id: number; name: string; hashes: string[] }[]) =>
        g
          .map((x) => ({ id: x.id, name: x.name, count: live.filter((i) => x.hashes.includes(i.hash)).length }))
          .filter((x) => x.count > 0)
          .sort((a, b) => a.name.localeCompare(b.name));
      out.tags = real === 'peter' ? counted(tags) : [];
      out.albums = real === 'peter' ? counted(albums) : [];
    }
    return out;
  }

  const STATE_MSG: Record<string, string> = {
    paused: 'This share is paused.',
    expired: 'This share has ended.',
    gone: 'This album is no longer shared.',
    unavailable: 'This share is not available on the computer right now.',
  };
  /** Device → share → scope, as shareScoped: the library key, or a refusal. */
  function auth(): Response | null {
    if (!cookie.paired) return json({ error: 'This phone is not paired with picmover. Show a pairing code on the computer.', paired: false }, 401);
    if (share.state) return json({ error: STATE_MSG[share.state], state: share.state }, 403);
    return null;
  }

  async function redeem(body: Record<string, unknown>): Promise<Response> {
    if (pairing.limited) {
      pairing.limited = false;
      return json({ error: 'Too many pairing attempts; wait a minute and try again.' }, 429);
    }
    const token = typeof body.token === 'string' ? body.token : null;
    const code = typeof body.code === 'string' ? body.code.toUpperCase().replace(/-/g, '') : null;
    if ((token === null) === (code === null)) return json({ error: 'want a token or a code' }, 400);
    const set = token !== null ? pairing.tokens : pairing.codes;
    const key = token ?? code ?? '';
    if (!set.has(key)) return json({ error: 'This pairing code has expired or was already used. Show a new one on the computer.' }, 410);
    // The pairing exists but its share refuses phones: 410 with the state.
    if (share.state) return json({ error: STATE_MSG[share.state], state: share.state }, 410);
    if (cookie.paired && body.replace !== true) {
      const cur = cookie.otherShare || share.name;
      // `sameShare`: the cookie's device is of the pairing's own share (asked of the backend in the review).
      return json({ error: `This phone is already paired with "${cur}".`, current: { share: cur, ...(cookie.otherShare ? {} : { sameShare: true }) } }, 409);
    }
    set.delete(key);
    if (pairing.approve) {
      const ok = await new Promise<boolean | 'off'>((resolve) => (pairing.pending = resolve));
      pairing.pending = null;
      if (ok === 'off') return json({ error: 'Sharing was turned off on the computer.', state: 'off' }, 410);
      if (!ok) return json({ error: 'The computer did not allow this phone.' }, 403);
    }
    if (pairing.full) return json({ error: 'this share has 20 phones; revoke one first' }, 409);
    cookie.paired = true;
    cookie.otherShare = '';
    device.name = (typeof body.name === 'string' ? body.name.trim() : '') || 'Phone';
    return json({ device: { ...device }, share: shareJSON() });
  }

  function libRoute(lib: string, rest: string, q: URLSearchParams, method: string, body: Record<string, unknown>): Response {
    const refused = auth();
    if (refused) return refused;
    if (kind === 'album' ? lib !== 'album' : !shared.includes(lib)) return notFound();
    const real = kind === 'album' ? 'peter' : lib;
    if (busy.has(real)) return json({ error: 'This library is not available right now.' }, 503);
    const inScope = (x: ShareItem & { lib: string }) => visible().includes(x) && x.lib === real;
    if (method === 'GET' && rest === 'library') {
      if (kind === 'album') return notFound();
      const l = items.filter((x) => x.lib === real);
      return json({ id: lib, name: libNames[lib] ?? lib, summary: { total: l.length, images: l.filter((x) => x.type !== 'video').length, raw: 0, videos: l.filter((x) => x.type === 'video').length, earliest: null, latest: null } });
    }
    if (method === 'GET' && rest === 'albums') {
      if (kind === 'album') return notFound();
      return json({ albums: real === 'peter' ? albums.map((a) => ({ id: a.id, name: a.name, count: a.hashes.length, cover: a.hashes[0] ?? null })) : [] });
    }
    if (method === 'GET' && rest === 'facets') {
      for (const k of q.keys()) return json({ error: `invalid filter "${k}"` }, 400);
      return json(facetsOf(lib, real));
    }
    if (method === 'GET' && (rest === 'geo' || rest === 'geo/items')) {
      // The map: `location` only; the filter whitelist plus bbox and zoom or limit.
      if (!caps.location) return json({ error: 'not allowed in this share' }, 403);
      const list = select(lib, q, rest === 'geo' ? ['bbox', 'zoom'] : ['bbox', 'limit']);
      if (list instanceof Response) return list;
      const b = (q.get('bbox') ?? '').split(',').map(Number);
      if (b.length !== 4 || b.some((x) => !Number.isFinite(x))) return json({ error: 'bbox is required' }, 400);
      const [w, s, e, n] = b as [number, number, number, number];
      const inBox = list
        .filter((x) => x.lat !== undefined && x.lon !== undefined && x.lat >= s && x.lat <= n && (w <= e ? x.lon >= w && x.lon <= e : x.lon >= w || x.lon <= e))
        .sort((a, b2) => (b2.takenAt ?? '').localeCompare(a.takenAt ?? '') || b2.id - a.id);
      if (rest === 'geo/items') {
        const limit = Number(q.get('limit') ?? 200);
        return json({ items: inBox.slice(0, limit).map(itemJSON), total: inBox.length });
      }
      const zoom = Number(q.get('zoom'));
      if (!Number.isInteger(zoom) || zoom < 0 || zoom > 22) return json({ error: 'zoom is required: an integer 0-22' }, 400);
      // Cells of 90/2^zoom degrees; the newest photo represents its cell.
      const d = 90 / 2 ** zoom;
      const cells = new Map<string, (ShareItem & { lib: string })[]>();
      for (const x of inBox) {
        const k = `${Math.floor(x.lat! / d)},${Math.floor(x.lon! / d)}`;
        cells.set(k, [...(cells.get(k) ?? []), x]);
      }
      const clusters = [...cells.values()].map((xs) => {
        const lat = xs.reduce((t, x) => t + x.lat!, 0) / xs.length;
        const lon = xs.reduce((t, x) => t + x.lon!, 0) / xs.length;
        const rep = xs[0]!;
        const c: Record<string, unknown> = { lat, lon, count: xs.length, hash: rep.hash, ...(rep.thumbAt ? { thumbAt: rep.thumbAt } : {}) };
        if (xs.length === 1) c.id = rep.id;
        else c.bounds = [Math.min(...xs.map((x) => x.lon!)), Math.min(...xs.map((x) => x.lat!)), Math.max(...xs.map((x) => x.lon!)), Math.max(...xs.map((x) => x.lat!))];
        return c;
      });
      return json({ clusters: clusters.sort((a, b2) => (b2.count as number) - (a.count as number)) });
    }
    if (opts.bulk && real === 'peter' && method === 'GET' && (rest === 'timeline' || rest === 'media')) {
      // A large library made up on the fly: the months' counts, items generated per month.
      if (rest === 'timeline') return json({ buckets: opts.bulk });
      const y = Number(q.get('year'));
      const mo = Number(q.get('month'));
      const b = opts.bulk.find((x) => x.year === y && x.month === mo);
      let id = 1_000_000;
      for (const x of opts.bulk) {
        if (x === b) break;
        id += x.count;
      }
      const day = (i: number, n: number) => String(Math.max(1, 28 - Math.floor((i / n) * 27))).padStart(2, '0');
      return json({
        items: Array.from({ length: b?.count ?? 0 }, (_, i) => ({
          id: id + i,
          hash: hashOf(id + i),
          type: 'image',
          takenAt: `${y}-${String(mo).padStart(2, '0')}-${day(i, b?.count ?? 1)}T10:00:00`,
          w: 4000,
          h: 3000,
          thumb: 'ok',
        })),
      });
    }
    if (method === 'GET' && rest === 'timeline') {
      const list = select(lib, q);
      if (list instanceof Response) return list;
      const m = new Map<string, { year: number; month: number; count: number }>();
      for (const x of list) {
        const [y, mo] = ym(x) as [number, number];
        const b = m.get(`${y}-${mo}`) ?? { year: y, month: mo, count: 0 };
        b.count++;
        m.set(`${y}-${mo}`, b);
      }
      return json({ buckets: [...m.values()].sort((a, b) => b.year - a.year || b.month - a.month) });
    }
    if (method === 'GET' && rest === 'media') {
      const list = select(lib, q, ['year', 'month']);
      if (list instanceof Response) return list;
      const y = Number(q.get('year'));
      const mo = Number(q.get('month'));
      return json({ items: list.filter((x) => ym(x)[0] === y && ym(x)[1] === mo).map(itemJSON) });
    }
    const mm = /^media\/(\d+)$/.exec(rest);
    if (method === 'GET' && mm) {
      const x = items.find((i) => i.id === Number(mm[1]));
      if (!x || !inScope(x)) return notFound();
      const d: Record<string, unknown> = {
        id: x.id,
        hash: x.hash,
        type: x.type,
        takenAt: x.takenAt,
        offset: 7200,
        w: x.w,
        h: x.h,
        dur: x.dur ?? 0,
        size: 2048,
        ext: x.type === 'video' ? '.mp4' : '.jpg',
        fileName: `IMG_${x.id}.${x.type === 'video' ? 'mp4' : 'jpg'}`,
        cameraMake: 'Canon',
        cameraModel: 'Canon EOS R6',
        lens: '',
        displayable: caps.download,
        // dur and thumbAt are always present (0 when none).
        thumbAt: x.thumbAt ?? 0,
      };
      if (caps.location && x.lat !== undefined) Object.assign(d, { lat: x.lat, lon: x.lon });
      if (caps.rate) d.rating = x.rating ?? 0;
      if (kind !== 'album') d.albums = albums.filter((a) => a.hashes.includes(x.hash)).map((a) => ({ id: a.id, name: a.name }));
      return json(d);
    }
    const rm = /^(thumb|preview|large)\/([0-9a-f]+)$/.exec(rest);
    if (method === 'GET' && rm) {
      const failed = failMedia();
      if (failed) return failed;
      const x = items.find((i) => i.hash === rm[2]);
      if (!x || !inScope(x)) return notFound();
      return new Response(new Uint8Array([0xff, 0xd8, 0xff, 0xd9]), { headers: { 'Content-Type': 'image/jpeg' } });
    }
    const fm = /^file\/(\d+)$/.exec(rest);
    if (method === 'GET' && fm) {
      const failed = failMedia();
      if (failed) return failed;
      const x = items.find((i) => i.id === Number(fm[1]));
      if (!x || !inScope(x)) return notFound();
      const need = q.get('download') === '1' || x.type !== 'video' ? caps.download : caps.video;
      if (!need) return json({ error: 'not allowed in this share' }, 403);
      return new Response(new Uint8Array([0]), { headers: { 'Content-Type': 'application/octet-stream' } });
    }
    if (method === 'POST' && rest === 'ratings') {
      // The server's order: the body, the row (404), the capability (403), a move (409).
      const hashes = Array.isArray(body.hashes) ? body.hashes : [];
      if (hashes.length !== 1) return json({ error: 'hashes: want exactly one hash' }, 400);
      if (typeof hashes[0] !== 'string' || !/^[0-9a-f]{16}$/.test(hashes[0])) return json({ error: 'hashes: invalid hash: want 16 lower-case hex digits' }, 400);
      const r = Number(body.rating);
      if (typeof body.rating !== 'number' || !Number.isInteger(r) || r < 0 || r > 5) return json({ error: 'rating: want 0-5 (0 clears)' }, 400);
      const x = items.find((i) => i.hash === hashes[0]);
      if (!x || !inScope(x)) return notFound();
      if (!caps.rate) return json({ error: 'not allowed in this share' }, 403);
      if (moving.on) return json({ error: 'a move is running on this library; try again when it has finished' }, 409);
      if (r > 0) x.rating = r;
      else delete x.rating;
      return json({ updated: 1 });
    }
    return notFound();
  }

  async function fetchImpl(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
    const url = new URL(String(input), 'https://192.168.1.20:7443');
    const method = init?.method ?? 'GET';
    const body = typeof init?.body === 'string' ? (JSON.parse(init.body) as Record<string, unknown>) : {};
    calls.push({ method, path: url.pathname + url.search, body: method === 'POST' ? body : null });
    const p = url.pathname;
    if (method === 'POST') {
      const h = new Headers(init?.headers);
      if (h.get('X-Picmover') !== '1' || h.get('Content-Type') !== 'application/json') return json({ error: 'forbidden' }, 403);
    }
    if (p === '/api/session' && method === 'GET') {
      // An album share's session opens its library: 503 while that is busy.
      const refused = auth() ?? (kind === 'album' && busy.has('peter') ? json({ error: 'This library is not available right now.' }, 503) : null);
      return refused ?? json({ device: { ...device }, computer: 'laptop', share: shareJSON() });
    }
    if (p === '/api/pair' && method === 'POST') return redeem(body);
    if (p === '/api/libraries' && method === 'GET') {
      const refused = auth();
      if (refused) return refused;
      if (kind === 'album') return notFound();
      return json({ libraries: libsJSON() });
    }
    const m = /^\/api\/l\/([^/]+)\/(.+)$/.exec(p);
    if (m) return libRoute(decodeURIComponent(m[1] ?? ''), m[2] ?? '', url.searchParams, method, body);
    return notFound();
  }

  function install() {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame', 'Date'] });
    vi.setSystemTime(new Date('2026-10-10T12:00:00'));
    vi.stubGlobal('fetch', vi.fn(fetchImpl));
    vi.stubGlobal('ResizeObserver', FakeResizeObserver);
  }

  /** Requests to a path (with its query), in order. */
  const requests = (prefix: string) => calls.filter((c) => c.path.startsWith(prefix));

  return {
    calls,
    items,
    albums,
    tags,
    caps,
    share,
    device,
    cookie,
    busy,
    moving,
    mediaFail,
    pairing,
    install,
    requests,
    approve: () => pairing.pending?.(true),
    deny: () => pairing.pending?.(false),
    /** Sharing turned off while a redeem waits for approval. */
    turnOff: () => pairing.pending?.('off'),
  };
}

export type FakeShareServer = ReturnType<typeof fakeShareServer>;
