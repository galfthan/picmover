// @vitest-environment jsdom
//
// Smoke test: mounts the whole app in jsdom against a fake API and walks
// every route. There is no real browser here; this catches runtime errors
// in components (effects, bindings, event wiring), not layout.

import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest';
import { flushSync, mount, tick, unmount } from 'svelte';
import App from './App.svelte';

const item = (id: number, takenAt: string | null, type = 'image') => ({
  id,
  hash: id.toString(16).padStart(16, '0'),
  type,
  takenAt,
  w: 4000,
  h: 3000,
  thumb: 'ok',
  ...(type === 'video' ? { dur: 7400 } : {}),
});

const bucket2407 = [item(3, '2024-07-14T23:30:00'), item(2, '2024-07-02T10:00:00', 'video'), item(1, '2024-07-01T08:00:00')];
const undated = [item(9, null)];

// The detail carries the same takenAt as the list item: the grid finds the
// item's bucket from it.
const detail = (id: number) => ({
  ...([...bucket2407, ...undated].find((x) => x.id === id) ?? item(id, '2024-07-14T23:30:00')),
  relPath: `image/2024/07/IMG_${id}.jpg`,
  ext: '.jpg',
  size: 123456,
  offset: 10800,
  takenAtSource: 'exif',
  orientation: 1,
  lat: 60.1699,
  lon: 24.9384,
  cameraMake: 'Canon',
  cameraModel: 'Canon EOS R6',
  cameraType: 'camera',
  lens: 'RF24-105mm',
  originalPath: '/media/card/DCIM/IMG.jpg',
  importedAt: '2026-10-10T12:00:00+03:00',
  sessionId: 17,
  rating: 0,
  phash: null,
  thumbState: 'ok',
  thumbError: '',
  displayable: true,
});

const runningJob = {
  id: 4,
  kind: 'import',
  state: 'running',
  source: '/media/card',
  move: false,
  startedAt: '2026-10-10T12:00:00+03:00',
  finishedAt: null,
  stats: { scanned: 1234, imported: 100, existing: 0, duplicates: 0, skippedSmall: 0, nonMedia: 0, errors: 0, bytesCopied: 1e6, thumbsDone: 0, thumbsUnsupported: 0, thumbsFailed: 0 },
  sessionId: 17,
  logPath: '/lib/.picmover/logs/import.log',
  error: null,
  exitCode: null,
};

const requests: string[] = [];

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

function fakeFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = new URL(String(input), 'http://127.0.0.1:7439');
  const path = url.pathname;
  requests.push(`${init?.method ?? 'GET'} ${path}${url.search}`);
  if (init?.method === 'POST') {
    const h = new Headers(init.headers);
    if (h.get('X-Picmover') !== '1' || h.get('Content-Type') !== 'application/json') return Promise.resolve(json({ error: 'forbidden' }, 403));
  }
  const q = url.searchParams;
  let res: Response;
  if (path === '/api/library') {
    res = json({
      root: '/home/me/Photos',
      version: 'test',
      summary: { total: 4, images: 3, raw: 0, videos: 1, withGps: 1, deleted: 0, bytes: 5e6, earliest: '2024-07-01T08:00:00', latest: '2024-07-14T23:30:00' },
      thumbs: { pending: 0, ok: 4, unsupported: 0, error: 0 },
      cameras: [{ make: 'Canon', model: 'Canon EOS R6', count: 3 }],
      ffmpeg: false,
    });
  } else if (path === '/api/timeline') {
    res = json({ buckets: q.get('type') === 'image_raw' ? [] : [{ year: 2024, month: 7, count: 3 }, { year: 0, month: 0, count: 1 }] });
  } else if (path === '/api/media') {
    res = json({ items: q.get('year') === '2024' ? bucket2407 : undated });
  } else if (path.startsWith('/api/media/')) {
    res = json(detail(Number(path.split('/').pop())));
  } else if (path === '/api/jobs' && init?.method === 'POST') {
    res = json({ job: { ...runningJob, id: 5 } }, 201);
  } else if (path === '/api/jobs') {
    res = json({ jobs: [runningJob] });
  } else if (path === '/api/fs') {
    res = q.get('path')
      ? json({ path: q.get('path'), parent: '/', dirs: [{ name: 'DCIM', path: `${q.get('path')}/DCIM` }], mediaFiles: 3, otherFiles: 1 })
      : json({ path: '', dirs: [{ name: '/', path: '/' }] });
  } else if (path === '/api/geo') {
    res = json({ clusters: [{ lat: 60.17, lon: 24.94, count: 1, id: 3, hash: '0000000000000003' }, { lat: 10, lon: 10, count: 12 }] });
  } else if (path === '/api/geo/items') {
    res = json({ items: [bucket2407[0]], total: 1 });
  } else {
    res = json({ error: 'not found' }, 404);
  }
  return Promise.resolve(res);
}

class FakeEventSource extends EventTarget {
  static instances: FakeEventSource[] = [];
  url: string;
  readyState = 0;
  onerror: ((e: Event) => void) | null = null;
  constructor(url: string) {
    super();
    this.url = url;
    FakeEventSource.instances.push(this);
  }
  emit(type: string, data: unknown) {
    this.dispatchEvent(new MessageEvent(type, { data: JSON.stringify(data) }));
  }
  close() {
    this.readyState = 2;
  }
}

class FakeResizeObserver {
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

async function settle() {
  for (let i = 0; i < 6; i++) {
    await vi.advanceTimersByTimeAsync(250);
    await tick();
  }
  flushSync();
}

async function go(hash: string) {
  location.hash = hash;
  window.dispatchEvent(new HashChangeEvent('hashchange'));
  await settle();
}

describe('App smoke test', () => {
  let app: ReturnType<typeof mount> | null = null;
  const errors: unknown[] = [];
  const onError = (e: ErrorEvent) => errors.push(e.error ?? e.message);

  beforeAll(() => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame', 'Date'] });
    vi.stubGlobal('fetch', vi.fn(fakeFetch));
    vi.stubGlobal('EventSource', FakeEventSource);
    vi.stubGlobal('ResizeObserver', FakeResizeObserver);
    window.addEventListener('error', onError);
    vi.spyOn(console, 'error').mockImplementation((...a: unknown[]) => void errors.push(a));
    vi.spyOn(console, 'warn').mockImplementation((...a: unknown[]) => void errors.push(a));
  });

  afterAll(() => {
    if (app) unmount(app);
    window.removeEventListener('error', onError);
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('renders the grid, lightbox, import, library and map pages', async () => {
    location.hash = '#/';
    app = mount(App, { target: document.body });
    await settle();

    // Shell
    expect(document.querySelector('.libchip .name')?.textContent).toBe('Photos');
    expect(document.querySelector('.libchip .root')?.textContent).toBe('/home/me/Photos');
    expect(document.querySelector('.libchip')?.getAttribute('href')).toBe('#/library');
    expect(document.querySelector('.pill')?.textContent).toContain('Importing 1,234 files');

    // Grid: buckets, tiles, badges
    const text = document.body.textContent ?? '';
    expect(text).toContain('July 2024');
    expect(text).toContain('Undated');
    const tiles = [...document.querySelectorAll<HTMLAnchorElement>('a.tile')];
    expect(tiles.map((t) => t.getAttribute('href'))).toEqual(['#/view/3', '#/view/2', '#/view/1', '#/view/9']);
    expect(tiles[1]?.textContent).toContain('0:07');
    expect(requests).toContain('GET /api/media?year=2024&month=7');

    // SSE: progress and done update the pill and trigger a refresh
    const es = FakeEventSource.instances.at(-1)!;
    expect(es.url).toBe('/api/jobs/4/events');
    es.emit('state', runningJob);
    es.emit('progress', { stats: { ...runningJob.stats, scanned: 2000 } });
    es.emit('result', { status: 'imported', source: '/media/card/a.jpg', dest: 'image/2024/07/a.jpg', seq: 1 });
    es.emit('result', { status: 'error', source: '/media/card/b.jpg', message: 'boom', seq: 2 });
    await settle();
    expect(document.querySelector('.pill')?.textContent).toContain('Importing 2,000 files');

    // Lightbox via tile click, info panel, neighbours
    tiles[0]!.click();
    await settle();
    expect(location.hash).toBe('#/view/3');
    const lb = document.querySelector('.lightbox');
    expect(lb).not.toBeNull();
    expect(lb?.textContent).toContain('image/2024/07/IMG_3.jpg');
    expect(lb?.textContent).toContain('UTC+03:00');
    expect(lb?.textContent).toContain('Show on map');
    expect(document.querySelector('.nav.next')).not.toBeNull();
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight' }));
    await settle();
    expect(location.hash).toBe('#/view/2');
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight' }));
    await settle();
    expect(location.hash).toBe('#/view/1');
    // Last dated item → crosses into the undated bucket
    expect(document.querySelector('.nav.next')).not.toBeNull();
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight' }));
    await settle();
    expect(location.hash).toBe('#/view/9');
    expect(document.querySelector('.lightbox')?.textContent).toContain('Undated');
    expect(document.querySelector('.nav.next')).toBeNull();
    expect(document.querySelector('.nav.prev')).not.toBeNull();
    // ...and back across into July.
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft' }));
    await settle();
    expect(location.hash).toBe('#/view/1');
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await settle();
    // history.back() is asynchronous in jsdom.
    for (let i = 0; i < 20 && location.hash !== '#/'; i++) await vi.advanceTimersByTimeAsync(10);
    await settle();
    expect(location.hash).toBe('#/');
    expect(document.querySelector('.lightbox')).toBeNull();
    expect(document.querySelectorAll('a.tile').length).toBe(4);

    // Filters: RAW → no match
    await go('#/?type=image_raw');
    expect(document.body.textContent).toContain('Nothing matches these filters');

    // Import page shows the running job and its results
    await go('#/import');
    expect(document.body.textContent).toContain('Import running');
    expect(document.body.textContent).toContain('boom');
    expect(document.querySelector('.dest')?.textContent).toContain('Importing into');
    expect(document.querySelector('.dest')?.textContent).toContain('/home/me/Photos/media.db');
    // Job finishes
    es.emit('done', { ...runningJob, state: 'done', exitCode: 2, finishedAt: '2026-10-10T12:05:00+03:00', stats: { ...runningJob.stats, errors: 12 } });
    await settle();
    expect(document.body.textContent).toContain('12 files could not be imported; see /lib/.picmover/logs/import.log');
    expect(document.body.textContent).toContain('Show imported');
    expect(document.querySelector('.dest')?.textContent).toContain('Imported into');
    const another = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Import another');
    another!.click();
    await settle();
    expect(document.body.textContent).toContain('Choose a folder to import from');
    expect(document.querySelector('.dest')?.textContent).toContain('Destination library');
    expect(document.querySelector('.dest')?.textContent).toContain('Files will be copied to');
    expect(document.querySelector('.dest')?.textContent).toContain('/home/me/Photos/<image | image_raw | video>/YYYY/MM/');
    const root = [...document.querySelectorAll<HTMLButtonElement>('.entry')].find((b) => b.textContent?.includes('/'));
    root!.click();
    await settle();
    expect(document.body.textContent).toContain('3 media files');
    [...document.querySelectorAll('button')].find((b) => b.textContent === 'Import from this folder')!.click();
    await settle();
    expect(document.body.textContent).toContain('Used for files without zone information');
    document.querySelector<HTMLFormElement>('form.options')!.requestSubmit();
    await settle();
    expect(requests).toContain('POST /api/jobs');
    expect(FakeEventSource.instances.at(-1)?.url).toBe('/api/jobs/5/events');
    expect(document.body.textContent).toContain('Import running');

    // Library page
    await go('#/library');
    expect(document.body.textContent).toContain('/home/me/Photos');
    expect(document.body.textContent).toContain('Canon EOS R6');
    expect(document.body.textContent).toContain('/home/me/Photos/media.db');
    expect(document.body.textContent).toContain('/home/me/Photos/.picmover/');
    expect(document.body.textContent).toContain('Generate missing thumbnails');

    // Map page (Leaflet in jsdom; tiles fail without network)
    // Leaflet is imported dynamically: give the module loader real time.
    vi.useRealTimers();
    location.hash = '#/map';
    for (let i = 0; i < 100 && !document.querySelector('.pm-cluster'); i++) {
      await new Promise((r) => setTimeout(r, 50));
    }
    expect(document.querySelector('.map-page .note.err')?.textContent ?? '').toBe('');
    expect(document.querySelector('.leaflet-container')).not.toBeNull();
    expect(requests.some((r) => r.startsWith('GET /api/geo?bbox='))).toBe(true);
    expect(document.querySelectorAll('.pm-cluster, .pm-single').length).toBe(2);

    location.hash = '#/';
    await new Promise((r) => setTimeout(r, 100));
    expect(document.querySelector('.leaflet-container')).toBeNull();
    expect(errors).toEqual([]);
  });
});
