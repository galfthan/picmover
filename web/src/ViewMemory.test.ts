// @vitest-environment jsdom
//
// View memory in the app (docs/design/management.md "View memory (web)"):
// the nav links go back to a page's remembered filters, Clear forgets
// them, and the grid scrolls back to its place when it mounts again with
// the same filters. jsdom has no layout: the fake ResizeObserver gives the
// scroller 1000 x 800 px and scrollTop is a plain property, so the layout
// is the one of lib/timeline.ts with Grid.svelte's constants.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fakeServer, go, hashOf, settle, type FakeServer } from './testing/fakes';
import { layoutTimeline } from './lib/timeline';
import type { TimelineBucket } from './lib/api';

let server: FakeServer;
let unmountApp: (() => void) | null = null;
const errors: unknown[] = [];

async function start(hash = '#/') {
  location.hash = hash;
  const svelte = await import('svelte');
  const App = (await import('./App.svelte')).default;
  const app = svelte.mount(App, { target: document.body });
  unmountApp = () => void svelte.unmount(app);
  await settle();
}

const navLink = (label: string) => [...document.querySelectorAll<HTMLAnchorElement>('nav[aria-label="Main"] a')].find((a) => a.textContent?.trim().startsWith(label))!;
const scroller = () => document.querySelector<HTMLDivElement>('.scroller')!;
const scrubber = () => document.querySelector<HTMLElement>('.scrubber')!;
const timelines = (q: string) => server.calls.filter((c) => c.path.startsWith('/api/l/peter/timeline') && c.path.includes(q)).length;

/** Grid.svelte's layout for the fake 1000 px scroller and the default tile size. */
const layout = (b: TimelineBucket[]) => layoutTimeline(b, { width: 1000 - 16 - 56, tileSize: 160, gap: 4, headerHeight: 44, bucketPadding: 12 });

// Two months of 100 videos each, so the grid scrolls.
const VIDEO_BUCKETS: TimelineBucket[] = [
  { year: 2023, month: 5, count: 100 },
  { year: 2022, month: 1, count: 100 },
];
/** The fake server does not filter by type: every view has the fake's photos (July and June 2024) too. */
const BUCKETS: TimelineBucket[] = [{ year: 2024, month: 7, count: 6 }, { year: 2024, month: 6, count: 2 }, ...VIDEO_BUCKETS];

async function scrollTo(y: number) {
  const el = scroller();
  el.scrollTop = y;
  el.dispatchEvent(new Event('scroll'));
  await settle();
}

describe('view memory', () => {
  beforeEach(() => {
    vi.resetModules();
    server = fakeServer('single');
    server.install();
    for (const [i, b] of VIDEO_BUCKETS.entries()) {
      for (let n = 0; n < b.count; n++) {
        const id = 1000 + i * 100 + n;
        server.items.push({ id, hash: hashOf(id), type: 'video', takenAt: `${b.year}-${String(b.month).padStart(2, '0')}-01T10:00:00`, w: 1920, h: 1080, thumb: 'ok', tagIds: [] });
      }
    }
    errors.length = 0;
    vi.spyOn(console, 'error').mockImplementation((...a: unknown[]) => void errors.push(a));
    vi.spyOn(console, 'warn').mockImplementation((...a: unknown[]) => void errors.push(a));
    localStorage.clear();
    sessionStorage.clear();
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

  it('takes the Photos and Map links back to the remembered filters; Clear forgets them', async () => {
    await start('#/l/peter/');
    expect(navLink('Photos').getAttribute('href')).toBe('#/l/peter/');
    await go('#/l/peter/?type=video');
    await go('#/l/peter/import');
    expect(navLink('Photos').getAttribute('href')).toBe('#/l/peter/?type=video');
    expect(navLink('Map').getAttribute('href')).toBe('#/l/peter/map');
    expect(sessionStorage.getItem('picmover.view./l/peter/')).toContain('type=video');

    // Explicit links with their own query are unaffected.
    await go('#/l/peter/?gps=1');
    expect(navLink('Photos').getAttribute('href')).toBe('#/l/peter/?gps=1');
    await go(navLink('Photos').getAttribute('href')!);
    await go('#/l/peter/?type=video');

    // The filter bar's Clear goes to the bare path, which is remembered.
    const clear = [...document.querySelectorAll<HTMLButtonElement>('.toolbar button')].find((b) => b.textContent?.trim() === 'Clear')!;
    clear.click();
    await settle();
    expect(location.hash).toBe('#/l/peter/');
    await go('#/l/peter/import');
    expect(navLink('Photos').getAttribute('href')).toBe('#/l/peter/');

    // A reload within the tab keeps the memory (sessionStorage).
    await go('#/l/peter/map?type=video');
    await go('#/l/peter/import');
    unmountApp?.();
    document.body.innerHTML = '';
    vi.resetModules();
    await start('#/l/peter/import');
    expect(navLink('Map').getAttribute('href')).toBe('#/l/peter/map?type=video');
  });

  it('scrolls the grid back to its place when it comes back with the same filters', async () => {
    await start('#/l/peter/?type=video');
    const jan = layout(BUCKETS).buckets[3]!;
    const y = jan.top + 250;
    await scrollTo(y);
    expect(scrubber().getAttribute('aria-valuetext')).toBe('January 2022');

    await go('#/l/peter/import');
    expect(JSON.parse(sessionStorage.getItem('picmover.view./l/peter/') ?? 'null')).toMatchObject({
      query: 'type=video',
      anchor: { year: 2022, month: 1, offset: 250 },
    });

    const before = timelines('type=video');
    await go(navLink('Photos').getAttribute('href')!);
    expect(location.hash).toBe('#/l/peter/?type=video');
    expect(scroller().scrollTop).toBe(y);
    expect(Number(scrubber().getAttribute('aria-valuenow'))).toBe(Math.round(y));
    expect(document.querySelector('section[aria-label="January 2022"] a.tile')).not.toBeNull();
    // One timeline request: the restore does not reload.
    expect(timelines('type=video')).toBe(before + 1);
  });

  it('starts at the top with other filters, or when the bucket is gone', async () => {
    await start('#/l/peter/?type=video');
    const jan = layout(BUCKETS).buckets[3]!;
    await scrollTo(jan.top + 100);
    await go('#/l/peter/import');

    // Another query (Back/Forward or a link): the anchor is not used.
    await go('#/l/peter/?type=video&minpx=1000');
    expect(scroller().scrollTop).toBe(0);
    expect(scrubber().getAttribute('aria-valuenow')).toBe('0');

    // The remembered bucket no longer exists.
    await scrollTo(jan.top + 100);
    await go('#/l/peter/import');
    const kept = server.items.filter((x) => !(x.type === 'video' && x.takenAt?.startsWith('2022-01')));
    server.items.splice(0, server.items.length, ...kept);
    await go(navLink('Photos').getAttribute('href')!);
    expect(location.hash).toBe('#/l/peter/?type=video&minpx=1000');
    expect(scroller().scrollTop).toBe(0);
  });
});
