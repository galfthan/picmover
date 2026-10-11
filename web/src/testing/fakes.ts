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
      case 'jobs':
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

  return { calls, items, tags, albums, imports, libs, folders, similar, openErrors, userdb, thumbCounts, largeFallback, backup, ws, libraryFolders, app, install, posts, finish };
}

export type FakeServer = ReturnType<typeof fakeServer>;
