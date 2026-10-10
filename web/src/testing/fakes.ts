// Test doubles shared by the component tests: a stateful fake of the
// step 4 API (a workspace with a few libraries; one of them, "peter", has
// items, tags, albums, a trash and similar groups), EventSource and
// ResizeObserver fakes, and helpers to let effects and timers settle.

import { vi } from 'vitest';
import type { Lib, MediaItem } from '../lib/api';

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
  set: { files: number; bytes: number; extras: number; damaged: number; unverified: number } | null;
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
        set: { files: 8, bytes: 8000, extras: 3, damaged: 1, unverified: 2 },
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
    plans: {
      [T1]: {
        current: cls(5, 5000),
        new: cls(2, 2048, ['image/2024/07/1.jpg', 'image/2024/07/2.jpg']),
        changed: cls(0, 0),
        moved: cls(1, 1000, [{ from: 'image/2024/06/7.jpg', to: 'image/2024/06/7b.jpg' }]),
        extra: cls(3, 3000, ['image/2023/01/x.jpg', 'image/2023/01/y.jpg']),
      },
    } as Record<string, Record<string, FakeClass>>,
    runs: {
      [T1]: [
        {
          id: 3,
          kind: 'scrub',
          startedAt: unix('2026-10-01T10:00:00'),
          finishedAt: unix('2026-10-01T10:15:00'),
          stats: {
            checked: 8,
            bytes: 8000,
            damaged: [
              { path: 'image/2024/06/7.jpg', repaired: true },
              { path: 'image/2024/06/8.jpg', repaired: false, error: 'the library copy differs too' },
            ],
            unknown: ['notes/stray.txt'],
            missing: 0,
          },
        },
        {
          id: 2,
          kind: 'backup',
          startedAt: unix('2026-10-10T09:50:00'),
          finishedAt: unix('2026-10-10T10:00:00'),
          stats: { copied: 7, moved: 0, changed: 0, bytes: 7000, failed: [{ path: 'image/2024/07/3.jpg', error: 'read: input/output error' }] },
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
  const targetJSON = (t: FakeTarget) => ({ ...t, set: t.set && t.reachable ? { ...t.set, behind: behindOf(t.id) } : null });
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
      if (body.path === '/mnt/taken') return json({ error: 'the folder holds another library\'s backup set named peter' }, 409);
      const t: FakeTarget = {
        id: (backup.nextId++).toString(16).padStart(16, '0'),
        label: typeof body.label === 'string' ? body.label : '',
        path: body.path,
        reachable: true,
        lastBackupAt: null,
        lastScrubAt: null,
        set: null,
      };
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
      backup.planned.add(id);
      return json(backup.plans[id] ?? { current: cls(0, 0), new: cls(0, 0), changed: cls(0, 0), moved: cls(0, 0), extra: cls(0, 0) });
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
        if (body.path === '/mnt/wrong') return json({ error: 'the marker in that folder belongs to another target' }, 409);
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
    if (kind === 'scrub' && body.budget !== undefined && (typeof body.budget !== 'number' || body.budget <= 0)) return json({ error: 'budget: expected seconds' }, 400);
    if (kind === 'prune') {
      if (body.confirm !== true) return json({ error: 'confirm required' }, 400);
      const n = backup.plans[t.id]?.extra?.count ?? 0;
      if (body.expected !== n) return json({ error: `the plan changed: ${n} files would be removed` }, 409);
    }
    if (kind === 'restore' && body.mode !== 'missing' && body.mode !== 'user-data') return json({ error: 'mode: expected missing or user-data' }, 400);
    // Like the server's job object: the target is not echoed, so the client remembers it.
    return json({ id: ++jobId, kind, state: 'running', library: 'peter', startedAt: '2026-10-10T12:00:00Z', finishedAt: null, stats: {}, error: null }, 201);
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
    if (method === 'GET' && path === 'jobs') return json({ jobs: [] });
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
      res = json({ jobs: [] });
    } else if (p === '/api/fs') {
      const at = url.searchParams.get('path') ?? '';
      res = at
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

  return { calls, items, tags, albums, libs, folders, similar, openErrors, userdb, backup, install, posts };
}

export type FakeServer = ReturnType<typeof fakeServer>;
