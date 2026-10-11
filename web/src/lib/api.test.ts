// Library-scoped URLs and chunked mutations, against a recording fake fetch.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, chunks, filterParams, libApi, libraryCoverUrl, setUnavailableHandler, toImport, toImportRefs } from './api';

interface Call {
  method: string;
  path: string;
  body: Record<string, unknown> | null;
}
let calls: Call[];
let reply: (c: Call) => Response;

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

beforeEach(() => {
  calls = [];
  reply = () => json({});
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://127.0.0.1:7439');
      const body = typeof init?.body === 'string' ? (JSON.parse(init.body) as Record<string, unknown>) : null;
      const c = { method: init?.method ?? 'GET', path: url.pathname + url.search, body };
      calls.push(c);
      if (c.method === 'POST') {
        const h = new Headers(init?.headers);
        expect(h.get('X-Picmover')).toBe('1');
        expect(h.get('Content-Type')).toBe('application/json');
      }
      return Promise.resolve(reply(c));
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
  setUnavailableHandler(null);
});

const hashes = (n: number) => Array.from({ length: n }, (_, i) => i.toString(16).padStart(16, '0'));

describe('thumbnail and preview URLs', () => {
  const hash = '00000000000000ab';
  const api = libApi('peter');
  it('carry the library prefix and thumbAt as a cache-busting version', () => {
    expect(api.thumbUrl({ hash, thumbAt: 1760090000 })).toBe('/api/l/peter/thumb/00000000000000ab?v=1760090000');
    expect(api.previewUrl({ hash, thumbAt: 1760090000 })).toBe('/api/l/peter/preview/00000000000000ab?v=1760090000');
    expect(api.fileUrl(12, true)).toBe('/api/l/peter/file/12?download=1');
  });
  it('are unversioned without thumbAt (map clusters, older servers)', () => {
    expect(api.thumbUrl({ hash })).toBe('/api/l/peter/thumb/00000000000000ab');
    expect(api.previewUrl({ hash })).toBe('/api/l/peter/preview/00000000000000ab');
  });
  it('build the large rendition URL like the thumb and preview', () => {
    expect(api.largeUrl({ hash, thumbAt: 1760090000 })).toBe('/api/l/peter/large/00000000000000ab?v=1760090000');
    expect(api.largeUrl({ hash })).toBe('/api/l/peter/large/00000000000000ab');
    expect(api.largeUrl({ hash, thumbAt: Number.POSITIVE_INFINITY })).toBe('/api/l/peter/large/00000000000000ab');
    expect(libApi('my lib').largeUrl({ hash })).toBe('/api/l/my%20lib/large/00000000000000ab');
  });
  it('ignore a thumbAt that is not a finite number', () => {
    const bad = JSON.parse('{"hash":"00000000000000ab","thumbAt":"\\"><img>"}') as { hash: string; thumbAt?: number };
    expect(api.thumbUrl(bad)).toBe('/api/l/peter/thumb/00000000000000ab');
    expect(api.thumbUrl({ hash, thumbAt: Number.NaN })).toBe('/api/l/peter/thumb/00000000000000ab');
  });
  it('percent-encode the library id', () => {
    expect(libApi('my photos#1').thumbUrl({ hash })).toBe('/api/l/my%20photos%231/thumb/00000000000000ab');
    expect(libApi('peter')).toBe(api);
  });
  it('name library covers through the workspace route', () => {
    expect(libraryCoverUrl('peter')).toBe('/api/libraries/peter/cover');
    expect(libraryCoverUrl('my photos')).toBe('/api/libraries/my%20photos/cover');
  });
  it('version library covers by the cover hash (the server caches them for an hour)', () => {
    expect(libraryCoverUrl('peter', '00000000000000ab')).toBe('/api/libraries/peter/cover?v=00000000000000ab');
    expect(libraryCoverUrl('peter', null)).toBe('/api/libraries/peter/cover');
  });
});

describe('filters in queries', () => {
  it('repeat tag and add the new filters', () => {
    const q = filterParams({ recur: '12-24..12-26', rating: 3, tags: [4, 9], album: 2, trashed: true, from: '2020-01-01' });
    expect(q.toString()).toBe('from=2020-01-01&recur=12-24..12-26&rating=3&tag=4&tag=9&album=2&trashed=1');
  });
  it('reach the scoped timeline endpoint', async () => {
    reply = () => json({ buckets: null });
    expect(await libApi('penny').getTimeline({ tags: [1] })).toEqual([]);
    expect(calls[0]?.path).toBe('/api/l/penny/timeline?tag=1');
  });
});

describe('chunking', () => {
  it('splits lists by 1000', () => {
    expect(chunks([])).toEqual([]);
    expect(chunks(hashes(2500)).map((c) => c.length)).toEqual([1000, 1000, 500]);
    expect(chunks([1, 2, 3], 2)).toEqual([[1, 2], [3]]);
  });

  it('rates in chunks and sums the answers, reporting progress', async () => {
    reply = (c) => json({ updated: (c.body?.hashes as unknown[]).length });
    const seen: [number, number][] = [];
    const r = await libApi('peter').setRating(hashes(2001), 4, (d, t) => seen.push([d, t]));
    expect(r.updated).toBe(2001);
    expect(calls.map((c) => [c.path, (c.body?.hashes as unknown[]).length, c.body?.rating])).toEqual([
      ['/api/l/peter/ratings', 1000, 4],
      ['/api/l/peter/ratings', 1000, 4],
      ['/api/l/peter/ratings', 1, 4],
    ]);
    expect(seen).toEqual([
      [1000, 2001],
      [2000, 2001],
      [2001, 2001],
    ]);
  });

  it('sends nothing for an empty selection', async () => {
    expect(await libApi('peter').trash([])).toEqual({ trashed: 0, failed: [] });
    expect(calls).toEqual([]);
  });

  it('concatenates trash failures across chunks', async () => {
    reply = (c) => {
      const hs = c.body?.hashes as string[];
      return json({ trashed: hs.length - 1, failed: [{ hash: hs[0], error: 'already in the trash' }] });
    };
    const r = await libApi('peter').trash(hashes(1500));
    expect(r.trashed).toBe(1498);
    expect(r.failed.map((f) => f.hash)).toEqual([hashes(1)[0], hashes(1001)[1000]]);
  });

  it('creates new tags with the first chunk only and adds them by id afterwards', async () => {
    reply = () => json({ tags: [{ id: 7, name: 'Beach' }, { id: 3, name: 'sea' }] });
    const r = await libApi('peter').tagMedia(hashes(2500), { add: [3], create: ['Beach'] });
    expect(calls.map((c) => [c.body?.add, c.body?.create, (c.body?.hashes as unknown[]).length])).toEqual([
      [[3], ['Beach'], 1000],
      [[3, 7], [], 1000],
      [[3, 7], [], 500],
    ]);
    expect(r.tags.map((t) => t.id).sort()).toEqual([3, 7]);
  });

  it('creates an album with the first chunk and adds the rest', async () => {
    reply = (c) =>
      c.path === '/api/l/peter/albums'
        ? json({ id: 5, name: 'Trip', count: 1000, cover: null, createdAt: '', updatedAt: '' }, 201)
        : json({ count: 1000 + (c.body?.add as unknown[]).length });
    const a = await libApi('peter').createAlbum('Trip', hashes(1200));
    expect(calls.map((c) => [c.path, (c.body?.hashes as unknown[] | undefined)?.length ?? (c.body?.add as unknown[]).length])).toEqual([
      ['/api/l/peter/albums', 1000],
      ['/api/l/peter/albums/5/items', 200],
    ]);
    expect(a.count).toBe(1200);
  });

  it('chunks album adds and removes separately', async () => {
    reply = () => json({ count: 9 });
    const r = await libApi('peter').albumItems(5, { add: hashes(1001), remove: hashes(2) });
    expect(calls.map((c) => [(c.body?.add as unknown[]).length, (c.body?.remove as unknown[]).length])).toEqual([
      [1000, 0],
      [1, 0],
      [0, 2],
    ]);
    expect(r.count).toBe(9);
  });

  it('confirms permanent deletes', async () => {
    reply = (c) => json({ deleted: c.body?.all ? 12 : (c.body?.hashes as unknown[]).length, failed: [] });
    expect((await libApi('peter').deleteForever(hashes(3))).deleted).toBe(3);
    expect((await libApi('peter').emptyTrash()).deleted).toBe(12);
    expect(calls.map((c) => c.body)).toEqual([
      { hashes: hashes(3), confirm: true },
      { all: true, confirm: true },
    ]);
  });
});

describe('similar', () => {
  it('ignores a whole group by one of its hashes (any group size)', async () => {
    reply = () => json({ ignored: 45 });
    expect(await libApi('peter').ignoreSimilarGroup('00000000000000ab')).toEqual({ ignored: 45 });
    expect(calls).toHaveLength(1);
    expect(calls[0]?.path).toBe('/api/l/peter/similar/ignore');
    expect(calls[0]?.body).toEqual({ group: '00000000000000ab' });
  });
});

describe('errors', () => {
  it('report a library that is not open and keep the body', async () => {
    const seen: unknown[] = [];
    setUnavailableHandler((id, status, state) => seen.push([id, status, state]));
    reply = () => json({ error: 'library is not open', state: 'closed' }, 409);
    const err = await libApi('peter').getLibrary().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).body).toEqual({ error: 'library is not open', state: 'closed' });
    reply = () => json({ error: 'rebuilding' }, 503);
    await libApi('peter').getTags().catch(() => {});
    // A 409 without a state (another job is running) is not about the library.
    reply = () => json({ error: 'another job is running' }, 409);
    await libApi('peter').startJob({ kind: 'similar' }).catch(() => {});
    expect(seen).toEqual([
      ['peter', 409, 'closed'],
      ['peter', 503, null],
    ]);
  });
});

describe('backups', () => {
  const api = libApi('peter');
  const T = 'a1b2c3d4e5f60718';

  it('lists targets, asking for a fresh plan with ?plan=1, and fills missing fields', async () => {
    reply = () =>
      json({
        targets: [
          { id: T, label: 'Red', path: '/mnt/red', reachable: true, lastBackupAt: 1760090000, lastScrubAt: null, set: { files: 3, bytes: 9, behind: { new: 1, changed: 0, moved: 2, bytes: 5 }, extras: 1, damaged: 0, unverified: 2 } },
          { id: '0f1e2d3c4b5a6978', label: 'NAS', path: '\\\\nas\\photos', reachable: false },
          { id: '086c53713dc7bcf4', label: 'Shared', path: '/mnt/s', reachable: true, lastBackupAt: null, lastScrubAt: null, set: null, error: 'the target already holds a backup of another library under this name' },
        ],
      });
    const list = await api.getBackups(true);
    expect(calls.map((c) => c.path)).toEqual(['/api/l/peter/backups?plan=1']);
    expect(list[0]).toEqual({
      id: T,
      label: 'Red',
      path: '/mnt/red',
      reachable: true,
      lastBackupAt: 1760090000,
      lastScrubAt: null,
      set: { name: '', files: 3, bytes: 9, behind: { new: 1, changed: 0, moved: 2, bytes: 5 }, extras: 1, damaged: 0, unverified: 2 },
    });
    expect(list[1]).toMatchObject({ reachable: false, lastBackupAt: null, set: null });
    expect(list[1]).not.toHaveProperty('error');
    // A reachable target whose set cannot be used carries the server's reason.
    expect(list[2]).toMatchObject({ reachable: true, set: null, error: 'the target already holds a backup of another library under this name' });
    expect(list[2]).not.toHaveProperty('foreign');
    reply = () => json({ targets: [{ id: T, label: 'Red', path: '/mnt/red', reachable: true, set: null, error: 'written by another copy', foreign: true }] });
    expect((await api.getBackups())[0]).toMatchObject({ set: null, error: 'written by another copy', foreign: true });
    reply = () => json({ targets: null });
    expect(await api.getBackups()).toEqual([]);
    expect(calls[2]?.path).toBe('/api/l/peter/backups');
  });

  it('adds, updates and forgets targets by id', async () => {
    reply = (c) => json(c.path.endsWith('/forget') ? {} : { id: T, label: 'Red', path: '/mnt/red', reachable: true }, c.path.endsWith('/targets') ? 201 : 200);
    expect((await api.addTarget('\\\\nas\\photos', 'NAS')).id).toBe(T);
    await api.updateTarget(T, { path: 'E:\\Backup' });
    await api.forgetTarget(T);
    expect(calls.map((c) => [c.method, c.path, c.body])).toEqual([
      ['POST', '/api/l/peter/backups/targets', { path: '\\\\nas\\photos', label: 'NAS' }],
      ['POST', `/api/l/peter/backups/targets/${T}`, { path: 'E:\\Backup' }],
      ['POST', `/api/l/peter/backups/targets/${T}/forget`, {}],
    ]);
  });

  it('never puts an invalid target id into a URL', async () => {
    await expect(api.getPlan('../../x')).rejects.toBeInstanceOf(ApiError);
    await expect(api.forgetTarget('A1B2C3D4E5F60718')).rejects.toBeInstanceOf(ApiError);
    expect(calls).toEqual([]);
  });

  it('reads the plan per class, with examples as strings or objects', async () => {
    reply = () =>
      json({
        new: { count: 2, bytes: 20, paths: ['a.jpg', 'b.jpg'] },
        moved: { count: 1, bytes: 0, examples: [{ from: 'c.jpg', to: 'd/c.jpg' }] },
        extra: { count: 600, bytes: 6000, paths: [{ path: 'x.jpg' }, 'y.jpg'] },
      });
    const p = await api.getPlan(T);
    expect(calls[0]?.path).toBe(`/api/l/peter/backups/targets/${T}/plan`);
    expect(p.new).toEqual({ count: 2, bytes: 20, paths: ['a.jpg', 'b.jpg'] });
    expect(p.moved.paths).toEqual(['c.jpg → d/c.jpg']);
    expect(p.extra).toEqual({ count: 600, bytes: 6000, paths: ['x.jpg', 'y.jpg'] });
    expect(p.changed).toEqual({ count: 0, bytes: 0, paths: [] });
    expect(p.current).toEqual({ count: 0, bytes: 0, paths: [] });
    // No restore class from this server: none is invented.
    expect(p.restore).toBeUndefined();
  });

  it('reads the restore class and the set name', async () => {
    reply = () => json({ extra: { count: 3, bytes: 30, paths: ['a.jpg', '.picmover/previous/b.jpg.1700000000'] }, restore: { count: 1, bytes: 10, paths: ['a.jpg'] } });
    const p = await api.getPlan(T);
    expect(p.restore).toEqual({ count: 1, bytes: 10, paths: ['a.jpg'] });
    expect(p.extra.count).toBe(3);
    reply = () => json({ targets: [{ id: T, label: 'Red', path: '/mnt/red', reachable: true, set: { name: 'peter', files: 0, bytes: 0, behind: { new: 6, changed: 0, moved: 0, bytes: 32127 }, extras: 0, damaged: 0, unverified: 0 } }] });
    expect((await api.getBackups())[0]?.set?.name).toBe('peter');
  });

  it('reads runs newest first, with stats as an object or JSON text', async () => {
    reply = () =>
      json({
        runs: [
          { id: 2, kind: 'scrub', startedAt: 10, finishedAt: 20, stats: { damaged: 1 } },
          { id: 1, kind: 'backup', startedAt: '2026-10-10T10:00:00Z', finishedAt: null, stats: '{"copied":3}' },
        ],
      });
    const runs = await api.getRuns(T);
    expect(calls[0]?.path).toBe(`/api/l/peter/backups/targets/${T}/runs`);
    expect(runs).toEqual([
      { id: 2, kind: 'scrub', startedAt: 10, finishedAt: 20, stats: { damaged: 1 } },
      { id: 1, kind: 'backup', startedAt: '2026-10-10T10:00:00Z', finishedAt: null, stats: { copied: 3 } },
    ]);
  });

  it('starts the backup job kinds with their bodies', async () => {
    reply = (c) => json({ id: 7, kind: c.body?.kind, state: 'queued', startedAt: null, finishedAt: null, stats: {}, error: null }, 201);
    await api.startJob({ kind: 'backup', target: T });
    await api.startJob({ kind: 'scrub', target: T, budget: 900, repair: true });
    await api.startJob({ kind: 'prune', target: T, confirm: true, expected: 3 });
    await api.startJob({ kind: 'restore', target: T, mode: 'missing' });
    await api.startJob({ kind: 'backup', target: T, takeOver: true });
    expect(calls.map((c) => c.body)).toEqual([
      { kind: 'backup', target: T },
      { kind: 'scrub', target: T, budget: 900, repair: true },
      { kind: 'prune', target: T, confirm: true, expected: 3 },
      { kind: 'restore', target: T, mode: 'missing' },
      { kind: 'backup', target: T, takeOver: true },
    ]);
    expect(calls.every((c) => c.path === '/api/l/peter/jobs')).toBe(true);
  });
});

describe('import records', () => {
  const api = libApi('peter');
  const H = '00000000000000a1';
  const rec = {
    id: 12,
    name: 'Summer',
    startedAt: '2026-10-09T10:00:00+03:00',
    finishedAt: '2026-10-09T10:05:00+03:00',
    source: '/media/card/DCIM',
    sourceName: 'DCIM',
    mode: 'copy',
    host: 'laptop',
    outcome: 'done',
    imported: 12,
    existing: 3,
    duplicates: 2,
    skipped: 1,
    errors: 1,
    bytes: 123456,
    items: 15,
    cover: H,
  };

  it('lists records and fills missing fields', async () => {
    reply = () => json({ imports: [rec, { id: 3, startedAt: '2026-01-01T00:00:00Z', finishedAt: null, cover: 0 }, 'junk'] });
    const list = await api.getImports();
    expect(calls.map((c) => c.path)).toEqual(['/api/l/peter/imports']);
    expect(list[0]).toEqual(rec);
    expect(list[1]).toEqual({
      id: 3,
      name: '',
      startedAt: '2026-01-01T00:00:00Z',
      finishedAt: null,
      source: '',
      sourceName: '',
      mode: '',
      host: '',
      outcome: '',
      imported: 0,
      existing: 0,
      duplicates: 0,
      skipped: 0,
      errors: 0,
      bytes: 0,
      items: null,
      cover: null,
    });
    expect(list).toHaveLength(2);
    reply = () => json({ imports: null });
    expect(await api.getImports()).toEqual([]);
  });

  it('keeps a rebuild record without items', () => {
    expect(toImport({ ...rec, mode: 'rebuild', items: null, cover: null })).toMatchObject({ mode: 'rebuild', items: null, cover: null });
    expect(toImport({ ...rec, items: 0 }).items).toBe(0);
    expect(toImport({ ...rec, items: 'x' }).items).toBeNull();
  });

  it('reads a cover only as a hash', () => {
    expect(toImport({ ...rec, cover: '' }).cover).toBeNull();
    expect(toImport({ ...rec, cover: 'A1' }).cover).toBeNull();
    expect(toImport({ ...rec, cover: null }).cover).toBeNull();
    // A wrapped answer works too.
    expect(toImport({ import: rec })).toEqual(rec);
  });

  it('gets, renames and deletes one record by id', async () => {
    reply = (c) => json(c.method === 'GET' ? rec : {});
    expect(await api.getImport(12)).toEqual(rec);
    await api.renameImport(12, '');
    await api.deleteImport(12);
    expect(calls.map((c) => [c.method, c.path, c.body])).toEqual([
      ['GET', '/api/l/peter/imports/12', null],
      ['POST', '/api/l/peter/imports/12', { name: '' }],
      ['POST', '/api/l/peter/imports/12/delete', {}],
    ]);
  });

  it('never puts an invalid id into a URL', async () => {
    await expect(api.getImport(0)).rejects.toBeInstanceOf(ApiError);
    await expect(api.deleteImport(-1)).rejects.toBeInstanceOf(ApiError);
    await expect(api.renameImport(1.5, 'x')).rejects.toBeInstanceOf(ApiError);
    expect(calls).toEqual([]);
  });

  it('passes the import filter to every listing', async () => {
    expect(filterParams({ album: 2, import: 7 }).toString()).toBe('album=2&import=7');
    reply = () => json({ buckets: [] });
    await api.getTimeline({ import: 7 });
    expect(calls[0]?.path).toBe('/api/l/peter/timeline?import=7');
  });

  it('reads the records in the media detail', async () => {
    reply = () =>
      json({
        id: 1,
        hash: H,
        imports: [{ id: 4, name: '', startedAt: '2026-10-09T10:00:00Z', mode: 'move', sourceName: 'DCIM' }, { id: 0 }, { id: 5, startedAt: '' }],
      });
    const d = await api.getMedia(1);
    expect(d.imports).toEqual([
      { id: 4, name: '', startedAt: '2026-10-09T10:00:00Z', mode: 'move', sourceName: 'DCIM' },
      { id: 5, name: '', startedAt: null, mode: '' },
    ]);
    reply = () => json({ id: 1, hash: H });
    expect(await api.getMedia(1)).not.toHaveProperty('imports');
    expect(toImportRefs(null)).toEqual([]);
  });
});
