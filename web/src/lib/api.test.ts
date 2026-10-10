// Library-scoped URLs and chunked mutations, against a recording fake fetch.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, chunks, filterParams, libApi, libraryCoverUrl, setUnavailableHandler } from './api';

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
