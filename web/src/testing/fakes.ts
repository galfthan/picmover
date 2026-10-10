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
  let ignored = 0;

  const libs: Lib[] =
    mode === 'single'
      ? [{ id: 'peter', name: 'peter', path: '/media/peter', state: 'open', summary: summary(8) }]
      : [
          { id: 'peter', name: 'peter', path: '/media/peter', state: 'closed', summary: summary(8), cover: hashOf(1) },
          { id: 'penny', name: 'penny', path: '/media/penny', state: 'busy', summary: summary(3), holder: { pid: 812, cmd: 'import', since: '2026-10-10T09:00:00Z' } },
          { id: 'broken', name: 'broken', path: '/media/broken', state: 'error', error: 'media.db needs a rebuild' },
        ];
  const folders = mode === 'single' ? [] : [{ name: 'scans', path: '/media/scans' }];
  let jobId = 0;

  const live = () => items.filter((x) => !x.deletedAt);
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
        summary: summary(live().length),
        thumbs: { pending: 0, ok: 9, unsupported: 0, error: 0 },
        cameras: [],
        ffmpeg: true,
        userdb: { ratings: items.filter((x) => x.rating).length, tags: tags.length, albums: albums.length, trashed: items.length - live().length, ignoredPairs: ignored },
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
      const g = live().filter((x) => x.id === 7 || x.id === 8);
      const groups = g.length === 2 && ignored === 0 ? [{ maxDistance: 3, items: g.map((x, i) => ({ ...wire(x), size: 1000 + i, width: x.w, height: x.h, relPath: `image/2024/06/${x.id}.jpg` })) }] : [];
      return json({ groups, total: groups.length, at: 1760090000, distance: 6 });
    }
    if (method === 'GET' && path === 'jobs') return json({ jobs: [] });
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
      case 'trash/restore':
        for (const x of items) if (hs.includes(x.hash)) x.deletedAt = null;
        return json({ restored: hs.length, failed: [] });
      case 'trash/delete': {
        if (!body.confirm) return json({ error: 'confirm required' }, 400);
        const gone = body.all ? items.filter((x) => x.deletedAt) : items.filter((x) => hs.includes(x.hash) && x.deletedAt);
        for (const g of gone) items.splice(items.indexOf(g), 1);
        return json({ deleted: gone.length, failed: [] });
      }
      case 'similar/ignore':
        ignored++;
        return json({ ignored: 1 });
      case 'jobs':
        return json({ id: ++jobId, kind: body.kind, state: 'running', library: 'peter', startedAt: '2026-10-10T12:00:00Z', finishedAt: null, stats: {}, error: null }, 201);
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
      const l: Lib = { id: String(body.name), name: String(body.name), path: `/media/${String(body.name)}`, state: 'closed', summary: summary(0) };
      libs.push(l);
      res = json(l, 201);
    } else if (/^\/api\/libraries\/[^/]+\/cover$/.test(p) && method === 'GET') {
      const l = libs.find((x) => x.id === decodeURIComponent(p.split('/')[3] ?? ''));
      res = l?.cover ? new Response(new Uint8Array([0xff, 0xd8, 0xff, 0xd9]), { headers: { 'Content-Type': 'image/jpeg', ETag: '"c1"' } }) : json({ error: 'no cover' }, 404);
    } else if (/^\/api\/libraries\/[^/]+\/open$/.test(p)) {
      const id = decodeURIComponent(p.split('/')[3] ?? '');
      const l = libs.find((x) => x.id === id);
      if (!l) res = json({ error: 'not found' }, 404);
      else if (l.state === 'busy') res = json({ error: 'locked', holder: l.holder }, 409);
      else {
        l.state = 'open';
        res = json(l);
      }
    } else if (/^\/api\/libraries\/[^/]+\/jobs$/.test(p)) {
      const id = decodeURIComponent(p.split('/')[3] ?? '');
      res = json({ id: ++jobId, kind: 'rebuild', state: 'running', library: id, startedAt: '2026-10-10T12:00:00Z', finishedAt: null, stats: {}, error: null }, 201);
    } else if (p === '/api/jobs') {
      res = json({ jobs: [] });
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

  return { calls, items, tags, albums, libs, install, posts };
}

export type FakeServer = ReturnType<typeof fakeServer>;
