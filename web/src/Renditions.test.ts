// @vitest-environment jsdom
//
// Renditions (docs/design/renditions.md) on the Library page against the
// fake server: the large-thumbnail count in the Thumbnails section, the
// button that fills it (the normal thumbs job), and the "large filled"
// part of a thumbs job's line in the Jobs table.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { backupJobJSON, fakeServer, hashOf, settle, ZERO_STATS, type FakeServer } from './testing/fakes';

let server: FakeServer;
let unmountApp: (() => void) | null = null;
const errors: unknown[] = [];

async function start(hash: string) {
  location.hash = hash;
  const svelte = await import('svelte');
  const App = (await import('./App.svelte')).default;
  const app = svelte.mount(App, { target: document.body });
  unmountApp = () => void svelte.unmount(app);
  await settle();
}

const text = (el: Element | null | undefined) => (el?.textContent ?? '').replace(/\s+/g, ' ').trim();
const thumbsCard = () => [...document.querySelectorAll('section.card')].find((c) => c.querySelector('h2')?.textContent === 'Thumbnails')!;
const jobsCard = () => [...document.querySelectorAll('section.card')].find((c) => c.querySelector('h2')?.textContent === 'Jobs')!;

function thumbsJob(id: number, stats: Partial<typeof ZERO_STATS>) {
  return backupJobJSON({ id, kind: 'thumbs', target: '', state: 'done', stats: { ...ZERO_STATS, ...stats } });
}

describe('renditions on the Library page', () => {
  beforeEach(() => {
    vi.resetModules();
    server = fakeServer('single');
    server.install();
    errors.length = 0;
    vi.spyOn(console, 'error').mockImplementation((...a: unknown[]) => void errors.push(a));
    vi.spyOn(console, 'warn').mockImplementation((...a: unknown[]) => void errors.push(a));
    localStorage.clear();
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

  it('shows the large thumbnails to fill and starts the thumbs job for them', async () => {
    server.thumbCounts.largeMissing = 1234;
    await start('#/l/peter/library');
    const card = thumbsCard();
    expect(text(card)).toContain('1,234 large thumbnails to fill');
    const button = [...card.querySelectorAll('button')].find((b) => b.textContent === 'Generate missing thumbnails')!;
    expect(button.disabled).toBe(false);
    button.click();
    await settle();
    expect(server.posts('l/peter/jobs').map((c) => c.body)).toEqual([{ kind: 'thumbs', rebuild: false, limit: 0 }]);
  });

  it('says nothing about large thumbnails when none are missing or the server does not count them', async () => {
    await start('#/l/peter/library');
    expect(text(thumbsCard())).not.toContain('large');
    unmountApp?.();
    document.body.innerHTML = '';
    vi.resetModules();
    server.thumbCounts.largeMissing = undefined;
    await start('#/l/peter/library');
    expect(text(thumbsCard())).not.toContain('large');
  });

  it('adds the large count to a thumbs job line when there is one', async () => {
    server.backup.jobs.push(thumbsJob(7, { thumbsDone: 3, largeDone: 1500 }), thumbsJob(6, { thumbsDone: 2 }));
    await start('#/l/peter/library');
    // Result column by job id.
    const result = (id: number) => text([...jobsCard().querySelectorAll('tbody tr')].find((r) => text(r.querySelector('td')) === String(id))?.querySelectorAll('td')[4]);
    expect(result(7)).toBe('3 done, 0 unsupported, 0 failed, 1,500 large filled');
    const six = result(6);
    expect(six).toBe('2 done, 0 unsupported, 0 failed');
    expect(six).not.toContain('large');
  });

  // The browser's part, against the fake server: an <img> fires `load`
  // when its URL answers 200 with an image, `error` otherwise.
  async function loadImages(root: ParentNode) {
    for (const img of root.querySelectorAll<HTMLImageElement>('img')) {
      const res = await fetch(img.getAttribute('src')!);
      const ok = res.ok && (res.headers.get('Content-Type') ?? '').startsWith('image/');
      img.dispatchEvent(new Event(ok ? 'load' : 'error'));
    }
    await settle();
  }

  it('shows a large answered with the thumb bytes (no-store) as loaded, and falls back to the thumb on a 404', async () => {
    vi.stubGlobal('devicePixelRatio', 2);
    server.largeFallback.add(hashOf(3));
    await start('#/l/peter');
    const tile = (id: number) => document.querySelector<HTMLAnchorElement>(`a.tile[href="#/l/peter/view/${id}"]`)!;
    const main = (id: number) => tile(id).querySelector<HTMLImageElement>('img:not(.under)')!;

    // The fake answers like the server: WebP and cacheable for a large, the thumb's bytes uncached for a fallback.
    const real = await fetch(main(2).getAttribute('src')!);
    expect([real.status, real.headers.get('Content-Type'), real.headers.get('Cache-Control')]).toEqual([200, 'image/webp', 'private, max-age=604800']);
    const fallback = await fetch(main(3).getAttribute('src')!);
    expect([fallback.status, fallback.headers.get('Content-Type'), fallback.headers.get('Cache-Control')]).toEqual([200, 'image/jpeg', 'no-store']);
    const thumb = await fetch(`/api/l/peter/thumb/${hashOf(3)}`);
    expect(new Uint8Array(await fallback.arrayBuffer())).toEqual(new Uint8Array(await thumb.arrayBuffer()));

    // Before anything loads, each large has the thumb underneath.
    for (const id of [2, 3]) {
      expect(main(id).getAttribute('src')).toMatch(/\/large\//);
      expect(tile(id).querySelector('img.under')?.getAttribute('src')).toMatch(/\/thumb\//);
    }
    await loadImages(tile(3));
    await loadImages(tile(2));
    // The fallback is an image: the tile keeps the large URL (asked again later, as it is not cached) and drops the underlay.
    for (const id of [2, 3]) {
      expect(main(id).getAttribute('src')).toMatch(/\/large\//);
      expect(tile(id).querySelector('img.under')).toBeNull();
    }

    // A large the server cannot answer (404: the item has no preview) falls back to the thumb.
    server.items.splice(server.items.findIndex((x) => x.id === 4), 1);
    await loadImages(tile(4));
    expect(main(4).getAttribute('src')).toBe(`/api/l/peter/thumb/${hashOf(4)}`);
  });
});

