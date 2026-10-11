// Test doubles shared by the component tests: a stateful fake of the
// step 4 API (a workspace with a few libraries; one of them, "peter", has
// items, tags, albums, a trash and similar groups), EventSource and
// ResizeObserver fakes, and helpers to let effects and timers settle.

import { vi } from 'vitest';
import type { Job, Lib, MediaItem } from '../lib/api';

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
export const ZERO_STATS = { scanned: 0, imported: 0, existing: 0, duplicates: 0, skippedSmall: 0, nonMedia: 0, errors: 0, bytesCopied: 0, thumbsDone: 0, thumbsUnsupported: 0, thumbsFailed: 0 };

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

interface FakeClass {
  count: number;
  bytes: number;
  paths: unknown[];
}

const cls = (count: number, bytes: number, paths: unknown[] = []): FakeClass => ({ count, bytes, paths });

const summary = (n: number) => ({ total: n, images: n, raw: 0, videos: 0, withGps: 0, deleted: 0, bytes: 1, earliest: '2024-06-01T10:00:00', latest: '2024-07-31T10:00:00' });

/** A fresh fake server; `install()` stubs fetch, EventSource and ResizeObserver. */
export function fakeServer(mode: 'workspace' | 'single' = 'workspace') {
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
  /** Near-duplicate groups as hash lists, before trash and ignore; tests may replace them. */
  const similar: { groups: string[][] } = { groups: [[hashOf(7), hashOf(8)]] };
  /** Ignored pairs "a|b" (a < b), as user.db similar_ignored. */
  const ignoredPairs = new Set<string>();
  const pairKey = (a: string, b: string) => (a < b ? `${a}|${b}` : `${b}|${a}`);
  /** Ids whose open answers 409 state "error" with this message although the listing says closed (user.db too new, ...). */
  const openErrors = new Map<string, string>();
  /** user.db counts that differ from the catalog's (null: the same). */
  const userdb: { trashed: number | null } = { trashed: null };

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

  function select(q: URLSearchParams): Item[] {
    let list = q.get('trashed') === '1' ? items.filter((x) => !!x.deletedAt) : live();
    const album = Number(q.get('album'));
    if (album) list = list.filter((x) => albums.find((a) => a.id === album)?.hashes.includes(x.hash));
    const rating = Number(q.get('rating'));
    if (rating) list = list.filter((x) => (x.rating ?? 0) >= rating);
    for (const t of q.getAll('tag')) list = list.filter((x) => x.tagIds.includes(Number(t)));
    return list;
  }

  function lib(path: string, q: URLSearchParams, method: string, body: Record<string, unknown>): Response {
    const hs = (body.hashes as string[] | undefined) ?? [];
    if (method === 'GET' && path === 'library') {
      return json({
        root: '/media/peter',
        version: 'test',
        summary: { ...summary(live().length), deleted: items.length - live().length },
        thumbs: { pending: 0, ok: 9, unsupported: 0, error: 0 },
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

  function fetchImpl(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
    const url = new URL(String(input), 'http://127.0.0.1:7439');
    const method = init?.method ?? 'GET';
    const body = typeof init?.body === 'string' ? (JSON.parse(init.body) as Record<string, unknown>) : {};
    calls.push({ method, path: url.pathname + url.search, body: method === 'POST' ? body : null });
    const p = url.pathname;
    let res: Response;
    if (p === '/api/libraries' && method === 'GET') {
      res = json({ root: mode === 'single' ? '/media/peter' : '/media', mode, libraries: libs, folders });
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

  return { calls, items, tags, albums, libs, folders, similar, openErrors, userdb, backup, install, posts, finish };
}

export type FakeServer = ReturnType<typeof fakeServer>;
