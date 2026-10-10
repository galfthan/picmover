// Workspace state machine: the pure gate function and the store's open,
// create and rebuild flows against a fake fetch.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Lib } from './api';
import { gateOf, holderText, libraryNameProblem, type GateInput } from './libraries.svelte';

const summary = { total: 3, images: 2, raw: 0, videos: 1, withGps: 0, deleted: 0, bytes: 1, earliest: null, latest: null };
const L = (id: string, state: string, extra: Partial<Lib> = {}): Lib => ({ id, name: id, path: `/media/${id}`, state, summary, ...extra });

function input(p: Partial<GateInput> = {}): GateInput {
  return {
    loaded: true,
    error: null,
    libraries: [],
    folders: [],
    opening: new Set(),
    failures: new Map(),
    rebuilding: new Set(),
    ...p,
  };
}

describe('gateOf', () => {
  it('waits for the listing, or reports its failure', () => {
    expect(gateOf(input({ loaded: false }), 'x')).toEqual({ kind: 'loading' });
    expect(gateOf(input({ loaded: false, error: 'down' }), 'x')).toEqual({ kind: 'failed', message: 'down' });
  });

  it('maps every library state', () => {
    const holder = { pid: 812, cmd: 'import', since: '2026-10-10T12:00:00+03:00' };
    const libs = [L('o', 'open'), L('c', 'closed'), L('b', 'busy', { holder }), L('e', 'error', { error: 'needs rebuild' }), L('r', 'rebuilding')];
    const s = input({ libraries: libs, folders: [{ name: 'f', path: '/media/f' }] });
    expect(gateOf(s, 'o').kind).toBe('open');
    expect(gateOf(s, 'c').kind).toBe('closed');
    const b = gateOf(s, 'b');
    expect(b.kind === 'busy' && b.holder).toEqual(holder);
    expect(b.kind === 'busy' && b.message).toMatch(/^in use by picmover import \(pid 812\) since /);
    expect(gateOf(s, 'e')).toEqual({ kind: 'error', lib: libs[3], message: 'needs rebuild' });
    expect(gateOf(s, 'r').kind).toBe('rebuilding');
    expect(gateOf(s, 'f')).toEqual({ kind: 'folder', folder: { name: 'f', path: '/media/f' } });
    expect(gateOf(s, 'nope')).toEqual({ kind: 'missing' });
  });

  it('shows opening, failures and rebuilds started here', () => {
    const libs = [L('c', 'closed'), L('e', 'error')];
    const folders = [{ name: 'f', path: '/media/f' }];
    expect(gateOf(input({ libraries: libs, opening: new Set(['c']) }), 'c').kind).toBe('opening');
    expect(gateOf(input({ libraries: libs, failures: new Map([['c', 'boom']]) }), 'c')).toEqual({ kind: 'openFailed', lib: libs[0], message: 'boom' });
    expect(gateOf(input({ libraries: libs, rebuilding: new Set(['e']) }), 'e').kind).toBe('rebuilding');
    expect(gateOf(input({ folders, rebuilding: new Set(['f']) }), 'f')).toEqual({ kind: 'rebuilding', lib: null, folder: folders[0] });
    // Once open, a stale rebuild mark does not hide it.
    expect(gateOf(input({ libraries: [L('e', 'open')], rebuilding: new Set(['e']) }), 'e').kind).toBe('open');
  });
});

describe('names and holders', () => {
  it('checks new library names like the server', () => {
    expect(libraryNameProblem('penny')).toBeNull();
    expect(libraryNameProblem('  ')).toMatch(/Enter a name/);
    expect(libraryNameProblem('a/b')).toMatch(/cannot contain/);
    expect(libraryNameProblem('a\\b')).toMatch(/cannot contain/);
    expect(libraryNameProblem('c:')).toMatch(/cannot contain/);
    expect(libraryNameProblem('.hidden')).toMatch(/dot/);
    expect(libraryNameProblem('CON')).toMatch(/reserved/);
    expect(libraryNameProblem('lpt1.txt')).toMatch(/reserved/);
    expect(libraryNameProblem('console')).toBeNull();
    expect(libraryNameProblem('x'.repeat(256))).toMatch(/too long/);
    expect(libraryNameProblem('peter', ['peter'])).toMatch(/exists/);
  });
  it('describes a lock holder', () => {
    expect(holderText(null)).toBe('in use by another picmover process');
    expect(holderText({ pid: 0, cmd: 'serve', since: '' })).toBe('in use by picmover serve');
  });
});

// ---- Store ----

type Store = (typeof import('./libraries.svelte'))['libraries'];
let routes: Record<string, () => Response>;
let requests: string[];

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

async function flush() {
  for (let i = 0; i < 10; i++) await Promise.resolve();
}

describe('libraries store', () => {
  let store: Store;

  beforeEach(async () => {
    vi.resetModules();
    requests = [];
    routes = {
      'GET /api/libraries': () =>
        json({ root: '/media', mode: 'workspace', libraries: [L('peter', 'closed'), L('busy', 'closed')], folders: [{ name: 'old', path: '/media/old' }] }),
      'GET /api/jobs': () => json({ jobs: [] }),
    };
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const key = `${init?.method ?? 'GET'} ${new URL(String(input), 'http://x').pathname}`;
        requests.push(key);
        const r = routes[key];
        return Promise.resolve(r ? r() : json({ error: 'not found' }, 404));
      }),
    );
    vi.stubGlobal(
      'EventSource',
      class extends EventTarget {
        onerror = null;
        close() {}
      },
    );
    store = (await import('./libraries.svelte')).libraries;
  });

  afterEach(() => vi.unstubAllGlobals());

  it('loads, then opens a closed library', async () => {
    expect(store.gate('peter').kind).toBe('loading');
    await store.load();
    expect(store.mode).toBe('workspace');
    expect(store.root).toBe('/media');
    expect(store.gate('peter').kind).toBe('closed');
    routes['POST /api/libraries/peter/open'] = () => json(L('peter', 'open'));
    const p = store.open('peter');
    expect(store.gate('peter').kind).toBe('opening');
    await p;
    expect(store.gate('peter').kind).toBe('open');
  });

  it('turns 409 into busy with the holder, 503 into rebuilding, others into a failure', async () => {
    await store.load();
    routes['POST /api/libraries/busy/open'] = () => json({ error: 'locked', holder: { pid: 9, cmd: 'import', since: '2026-10-10T10:00:00Z' } }, 409);
    await store.open('busy');
    const g = store.gate('busy');
    expect(g.kind).toBe('busy');
    expect(g.kind === 'busy' && g.holder?.pid).toBe(9);

    routes['POST /api/libraries/peter/open'] = () => json({ error: 'rebuilding' }, 503);
    await store.open('peter');
    expect(store.gate('peter').kind).toBe('rebuilding');

    store.libraries[0] = L('peter', 'closed');
    routes['POST /api/libraries/peter/open'] = () => json({ error: 'disk on fire' }, 500);
    await store.open('peter');
    expect(store.gate('peter')).toMatchObject({ kind: 'openFailed', message: 'disk on fire' });
    // Retry clears the failure.
    routes['POST /api/libraries/peter/open'] = () => json(L('peter', 'open'));
    await store.open('peter');
    expect(store.gate('peter').kind).toBe('open');
  });

  it('creates a library and replaces a folder entry of the same name', async () => {
    await store.load();
    routes['POST /api/libraries'] = () => json(L('old', 'closed'), 201);
    const lib = await store.create(' old ');
    expect(lib.id).toBe('old');
    expect(store.folders).toEqual([]);
    expect(store.gate('old').kind).toBe('closed');
  });

  it('records a folder in place through the workspace jobs route', async () => {
    await store.load();
    routes['POST /api/libraries/old/jobs'] = () =>
      json({ id: 3, kind: 'rebuild', state: 'running', library: 'old', startedAt: '2026-10-10T12:00:00Z', finishedAt: null, stats: {}, error: null }, 201);
    await store.rebuild('old');
    expect(requests).toContain('POST /api/libraries/old/jobs');
    expect(store.gate('old').kind).toBe('rebuilding');
    // The listing shows it as a library being rebuilt, later as open.
    routes['GET /api/libraries'] = () => json({ root: '/media', mode: 'workspace', libraries: [L('old', 'rebuilding')], folders: [] });
    await store.load();
    expect(store.gate('old').kind).toBe('rebuilding');
    routes['GET /api/libraries'] = () => json({ root: '/media', mode: 'workspace', libraries: [L('old', 'open')], folders: [] });
    await store.load();
    expect(store.gate('old').kind).toBe('open');
  });

  it('reopens a library the server reports as not open, but not in a loop', async () => {
    await store.load();
    routes['POST /api/libraries/peter/open'] = () => json(L('peter', 'open'));
    await store.open('peter');
    // Right after opening, a 409 is reported instead of retried.
    store.unavailable('peter', 409, 'closed');
    expect(store.gate('peter').kind).toBe('openFailed');
    await flush();
  });

  it('knows the single-mode library', async () => {
    routes['GET /api/libraries'] = () => json({ root: '/home/me/Photos', mode: 'single', libraries: [L('Photos', 'open')], folders: [] });
    await store.load();
    expect(store.singleId).toBe('Photos');
  });
});
