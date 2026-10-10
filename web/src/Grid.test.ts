// @vitest-environment jsdom
//
// Grid and viewer behaviour that needs the whole app: bucket fetches racing
// filter changes, timeline counts that disagree with the bucket, zoom and
// resize anchoring, the scrubber's keyboard use, thumbnail versions and the
// viewer's focus handling. Each test mounts a fresh copy of the app (fresh
// stores) against a fake API.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { MediaItem, TimelineBucket } from './lib/api';
import { layoutTimeline } from './lib/timeline';

// ---- Fake API ----

const item = (id: number, takenAt: string | null, extra: Partial<MediaItem> = {}): MediaItem => ({
  id,
  hash: id.toString(16).padStart(16, '0'),
  type: 'image',
  takenAt,
  w: 4000,
  h: 3000,
  thumb: 'ok',
  ...extra,
});

let timeline: TimelineBucket[];
let media: Map<string, MediaItem[]>;
let jobList: unknown[];
/** When set, /api/media responses wait for it. */
let gate: Promise<void> | null;
let requests: string[];

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

function allItems(): MediaItem[] {
  return [...media.values()].flat();
}

async function fakeFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = new URL(String(input), 'http://127.0.0.1:7439');
  requests.push(`${init?.method ?? 'GET'} ${url.pathname}${url.search}`);
  const q = url.searchParams;
  if (url.pathname === '/api/libraries') {
    return json({ root: '/home/me/Photos', mode: 'single', libraries: [{ id: 'Photos', name: 'Photos', path: '/home/me/Photos', state: 'open' }], folders: [] });
  }
  if (url.pathname === '/api/l/Photos/tags') return json({ tags: [] });
  if (url.pathname === '/api/l/Photos/albums') return json({ albums: [] });
  const path = url.pathname.startsWith('/api/l/Photos/') ? '/api/' + url.pathname.slice('/api/l/Photos/'.length) : url.pathname;
  if (path === '/api/library') {
    return json({
      root: '/home/me/Photos',
      version: 'test',
      summary: { total: 3, images: 3, raw: 0, videos: 0, withGps: 0, deleted: 0, bytes: 1, earliest: null, latest: null },
      thumbs: { pending: 0, ok: 3, unsupported: 0, error: 0 },
      cameras: [],
      ffmpeg: false,
    });
  }
  if (path === '/api/timeline') return json({ buckets: timeline });
  if (path === '/api/media') {
    const g = gate;
    if (g) await g;
    return json({ items: media.get(`${q.get('year')}-${q.get('month')}`) ?? [] });
  }
  if (path.startsWith('/api/media/')) {
    const id = Number(path.split('/').pop());
    const it = allItems().find((x) => x.id === id);
    if (!it) return json({ error: 'not found' }, 404);
    return json({
      ...it,
      relPath: `image/x/${id}.jpg`,
      ext: '.jpg',
      size: 1,
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
      rating: 0,
      phash: null,
      thumbState: it.thumb,
      thumbError: '',
      displayable: true,
    });
  }
  if (path === '/api/jobs') return json({ jobs: jobList });
  return json({ error: 'not found' }, 404);
}

class FakeEventSource extends EventTarget {
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

class FakeResizeObserver {
  static instances: FakeResizeObserver[] = [];
  cb: ResizeObserverCallback;
  target: Element | null = null;
  constructor(cb: ResizeObserverCallback) {
    this.cb = cb;
    FakeResizeObserver.instances.push(this);
  }
  observe(target: Element) {
    this.target = target;
    queueMicrotask(() => this.resize(1000, 800));
  }
  resize(width: number, height: number) {
    if (!this.target) return;
    const entry = { target: this.target, contentRect: { width, height } } as unknown as ResizeObserverEntry;
    this.cb([entry], this as unknown as ResizeObserver);
  }
  unobserve() {}
  disconnect() {
    this.target = null;
  }
}

// ---- Helpers ----

async function settle() {
  for (let i = 0; i < 6; i++) {
    await vi.advanceTimersByTimeAsync(250);
  }
}

async function go(hash: string) {
  location.hash = hash;
  window.dispatchEvent(new HashChangeEvent('hashchange'));
  await settle();
}

/** Escape closes with history.back(), which is asynchronous in jsdom. */
async function closeViewer() {
  window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
  await settle();
  for (let i = 0; i < 20 && location.hash.includes('/view/'); i++) await vi.advanceTimersByTimeAsync(10);
  await settle();
}

const isInert = (sel: string) => {
  const el = document.querySelector<HTMLElement & { inert?: boolean }>(sel);
  return !!el && (el.inert === true || el.hasAttribute('inert'));
};
const tiles = () => [...document.querySelectorAll<HTMLAnchorElement>('a.tile')];
const placeholders = () => document.querySelectorAll('.canvas .ph').length;
const mediaRequests = () => requests.filter((r) => r.startsWith('GET /api/l/Photos/media?'));

// Layout constants of Grid.svelte, with the fake 1000 px wide scroller.
const layout = (b: TimelineBucket[], width = 1000) =>
  layoutTimeline(b, { width: width - 16 - 56, tileSize: 160, gap: 4, headerHeight: 44, bucketPadding: 12 });

let unmountApp: (() => void) | null = null;

async function start(hash = '#/') {
  location.hash = hash;
  const svelte = await import('svelte');
  const App = (await import('./App.svelte')).default;
  const app = svelte.mount(App, { target: document.body });
  unmountApp = () => void svelte.unmount(app);
  await settle();
}

describe('grid', () => {
  const errors: unknown[] = [];

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame', 'Date'] });
    vi.stubGlobal('fetch', vi.fn(fakeFetch));
    vi.stubGlobal('EventSource', FakeEventSource);
    vi.stubGlobal('ResizeObserver', FakeResizeObserver);
    vi.spyOn(console, 'error').mockImplementation((...a: unknown[]) => void errors.push(a));
    vi.spyOn(console, 'warn').mockImplementation((...a: unknown[]) => void errors.push(a));
    vi.resetModules();
    localStorage.clear();
    FakeEventSource.instances = [];
    FakeResizeObserver.instances = [];
    errors.length = 0;
    requests = [];
    gate = null;
    jobList = [];
    timeline = [{ year: 2024, month: 7, count: 3 }];
    media = new Map([['2024-7', [item(3, '2024-07-14T10:00:00'), item(2, '2024-07-02T10:00:00'), item(1, '2024-07-01T10:00:00')]]]);
  });

  afterEach(() => {
    unmountApp?.();
    unmountApp = null;
    document.body.innerHTML = '';
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    expect(errors).toEqual([]);
  });

  it('fills buckets whose fetch was in flight during a filter change', async () => {
    let open: () => void = () => {};
    gate = new Promise<void>((r) => (open = r));
    await start();
    expect(mediaRequests()).toHaveLength(1);
    expect(tiles()).toHaveLength(0);
    expect(placeholders()).toBe(3);

    // Same buckets under the new filter; the first fetch is now stale.
    await go('#/l/Photos/?gps=1');
    open();
    gate = null;
    await settle();
    expect(tiles()).toHaveLength(3);
    expect(placeholders()).toBe(0);
    expect(mediaRequests().length).toBeGreaterThanOrEqual(2);
    expect(mediaRequests().at(-1)).toContain('gps=1');
  });

  it('re-lays out a bucket that has fewer items than the timeline said', async () => {
    timeline = [{ year: 2024, month: 7, count: 5 }, { year: 2023, month: 1, count: 1 }];
    media.set('2023-1', [item(10, '2023-01-01T10:00:00')]);
    await start();
    expect(tiles()).toHaveLength(4);
    expect(placeholders()).toBe(0);
    const header = document.querySelector('section[aria-label="July 2024"] .n');
    expect(header?.textContent).toBe('3');
    const expected = layout([{ year: 2024, month: 7, count: 3 }, { year: 2023, month: 1, count: 1 }]);
    expect(document.querySelector<HTMLElement>('.canvas')?.style.height).toBe(`${expected.totalHeight}px`);
    expect(document.body.textContent).toContain('4 items');
  });

  it('shows every item of a bucket that has more items than the timeline said', async () => {
    timeline = [{ year: 2024, month: 7, count: 2 }, { year: 2023, month: 1, count: 1 }];
    const july = Array.from({ length: 7 }, (_, i) => item(20 + i, `2024-07-${String(20 - i).padStart(2, '0')}T10:00:00`));
    media.set('2024-7', july);
    media.set('2023-1', [item(10, '2023-01-01T10:00:00')]);
    await start();
    expect(tiles().map((t) => t.getAttribute('href'))).toEqual([...july.map((x) => `#/l/Photos/view/${x.id}`), '#/l/Photos/view/10']);
    expect(placeholders()).toBe(0);
    const expected = layout([{ year: 2024, month: 7, count: 7 }, { year: 2023, month: 1, count: 1 }]);
    expect(expected.buckets[0]?.rows).toBe(2); // the patched bucket really grew a row
    expect(document.querySelector<HTMLElement>('.canvas')?.style.height).toBe(`${expected.totalHeight}px`);
    // The 2023 bucket sits below the patched July bucket.
    expect(document.querySelector<HTMLElement>('section[aria-label="January 2023"]')?.style.top).toBe(`${expected.buckets[1]?.top}px`);

    // The viewer walks the extra items and crosses into the next bucket.
    tiles()[6]!.click();
    await settle();
    expect(location.hash).toBe('#/l/Photos/view/26');
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight' }));
    await settle();
    expect(location.hash).toBe('#/l/Photos/view/10');
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft' }));
    await settle();
    expect(location.hash).toBe('#/l/Photos/view/26');
    await closeViewer();
    expect(document.activeElement?.getAttribute('href')).toBe('#/l/Photos/view/26');
  });

  it('zooms with many small ctrl+wheel deltas (trackpad pinch)', async () => {
    await start();
    const scroller = document.querySelector<HTMLElement>('.scroller')!;
    const slider = document.querySelector<HTMLInputElement>('.size input')!;
    expect(slider.value).toBe('160');
    for (let i = 0; i < 40; i++) scroller.dispatchEvent(new WheelEvent('wheel', { deltaY: -1, ctrlKey: true, cancelable: true }));
    await settle();
    expect(Number(slider.value)).toBeGreaterThan(160);
    for (let i = 0; i < 80; i++) scroller.dispatchEvent(new WheelEvent('wheel', { deltaY: 1, ctrlKey: true, cancelable: true }));
    await settle();
    expect(Number(slider.value)).toBeLessThan(160);
  });

  it('moves a year with the keyboard on the scrubber and keeps the place on resize', async () => {
    timeline = [
      { year: 2024, month: 7, count: 100 },
      { year: 2023, month: 5, count: 100 },
      { year: 2022, month: 1, count: 100 },
    ];
    for (const b of timeline) {
      media.set(
        `${b.year}-${b.month}`,
        Array.from({ length: 100 }, (_, i) => item(b.year * 1000 + i, `${b.year}-${String(b.month).padStart(2, '0')}-01T10:00:00`)),
      );
    }
    await start();
    const scrub = document.querySelector<HTMLElement>('.scrubber')!;
    expect(scrub.getAttribute('role')).toBe('slider');
    expect(scrub.tabIndex).toBe(0);
    expect(scrub.getAttribute('aria-valuetext')).toBe('July 2024');
    scrub.focus();
    scrub.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }));
    await settle();
    expect(scrub.getAttribute('aria-valuetext')).toBe('May 2023');
    expect(Number(scrub.getAttribute('aria-valuenow'))).toBe(Math.round(layout(timeline).buckets[1]?.top ?? -1));
    scrub.dispatchEvent(new KeyboardEvent('keydown', { key: 'PageDown', bubbles: true, cancelable: true }));
    await settle();
    expect(scrub.getAttribute('aria-valuetext')).toBe('January 2022');
    scrub.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true, cancelable: true }));
    await settle();
    expect(scrub.getAttribute('aria-valuetext')).toBe('May 2023');

    // Narrower window: fewer columns, taller buckets. The view stays on May 2023.
    const ro = FakeResizeObserver.instances.find((o) => o.target?.classList.contains('scroller'))!;
    ro.resize(600, 800);
    await settle();
    expect(scrub.getAttribute('aria-valuetext')).toBe('May 2023');
    expect(Number(scrub.getAttribute('aria-valuenow'))).toBe(Math.round(layout(timeline, 600).buckets[1]?.top ?? -1));
    expect(document.querySelector('section[aria-label="May 2023"] a.tile')).not.toBeNull();

    scrub.dispatchEvent(new KeyboardEvent('keydown', { key: 'Home', bubbles: true, cancelable: true }));
    await settle();
    expect(scrub.getAttribute('aria-valuenow')).toBe('0');
  });

  it('versions thumbnail URLs and retries a failed tile after a job with the refetched bucket', async () => {
    media.set('2024-7', [item(3, '2024-07-14T10:00:00', { thumbAt: 100 }), item(2, '2024-07-02T10:00:00'), item(1, '2024-07-01T10:00:00')]);
    jobList = [{ id: 4, kind: 'thumbs', state: 'running', startedAt: '2026-10-10T12:00:00+03:00', finishedAt: null, stats: {}, error: null }];
    await start();
    const img3 = () => document.querySelector<HTMLImageElement>('a.tile[href="#/l/Photos/view/3"] img');
    const img2 = () => document.querySelector<HTMLImageElement>('a.tile[href="#/l/Photos/view/2"] img');
    expect(img3()?.getAttribute('src')).toBe('/api/l/Photos/thumb/0000000000000003?v=100');
    expect(img2()?.getAttribute('src')).toBe('/api/l/Photos/thumb/0000000000000002');

    // Both 404 while the job is running.
    img3()!.dispatchEvent(new Event('error'));
    img2()!.dispatchEvent(new Event('error'));
    await settle();
    expect(img3()).toBeNull();
    expect(img2()).toBeNull();

    // The job finishes: the bucket is refetched with new versions.
    media.set('2024-7', [item(3, '2024-07-14T10:00:00', { thumbAt: 200 }), item(2, '2024-07-02T10:00:00'), item(1, '2024-07-01T10:00:00')]);
    const before = mediaRequests().length;
    const es = FakeEventSource.instances.at(-1)!;
    es.emit('done', { ...(jobList[0] as object), state: 'done', finishedAt: '2026-10-10T12:01:00+03:00' });
    await settle();
    expect(mediaRequests().length).toBeGreaterThan(before);
    expect(img3()?.getAttribute('src')).toBe('/api/l/Photos/thumb/0000000000000003?v=200');
    expect(requests.some((r) => r.includes('retry='))).toBe(false);
    // No thumbAt: one more try at the same URL, and only one.
    expect(img2()?.getAttribute('src')).toBe('/api/l/Photos/thumb/0000000000000002');
    img2()!.dispatchEvent(new Event('error'));
    await settle();
    expect(img2()).toBeNull();
  });

  it('keeps focus and keys inside the viewer and gives focus back on close', async () => {
    media.set('2024-7', [item(3, '2024-07-14T10:00:00'), item(2, '2024-07-02T10:00:00', { thumb: 'unsupported' }), item(1, '2024-07-01T10:00:00')]);
    await start();
    tiles()[0]!.click();
    await settle();
    const lb = document.querySelector<HTMLElement>('.lightbox')!;
    // Focus is on the dialog, not on the Close button (Space would close it).
    expect(document.activeElement).toBe(lb);
    lb.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true }));
    await settle();
    expect(location.hash).toBe('#/l/Photos/view/3');
    // The page behind is inert and its shortcuts are off.
    // (jsdom has no inert semantics; Svelte sets the property.)
    expect(isInert('main')).toBe(true);
    expect(isInert('header.topbar')).toBe(true);
    lb.dispatchEvent(new KeyboardEvent('keydown', { key: '/', bubbles: true, cancelable: true }));
    expect(document.activeElement).toBe(lb);

    // Double-clicking the next arrow does not switch to 1:1 ...
    const next = document.querySelector<HTMLButtonElement>('.nav.next')!;
    next.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }));
    await settle();
    expect(document.querySelector('.lightbox .full')).toBeNull();
    // ... double-clicking the picture does.
    document.querySelector('.stage')!.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }));
    await settle();
    expect(document.querySelector('.lightbox .full')).not.toBeNull();
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'f' }));
    await settle();
    expect(document.querySelector('.lightbox .full')).toBeNull();

    // An item without a possible thumbnail does not request one.
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight' }));
    await settle();
    expect(location.hash).toBe('#/l/Photos/view/2');
    expect(document.querySelector('.lightbox img.blur')).toBeNull();
    expect(document.querySelector('.lightbox img.preview')?.getAttribute('src')).toBe('/api/l/Photos/preview/0000000000000002');

    await closeViewer();
    expect(document.querySelector('.lightbox')).toBeNull();
    expect(isInert('main')).toBe(false);
    // Focus goes to the tile of the item shown last, not to <body>.
    expect(document.activeElement?.getAttribute('href')).toBe('#/l/Photos/view/2');
  });
});
