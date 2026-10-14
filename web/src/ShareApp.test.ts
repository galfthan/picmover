// @vitest-environment jsdom
//
// The phone app (docs/design/lan-share.md "Phone UI") against the fake
// share listener: pairing (token, code, expired, rate-limited, already
// paired, approval), the screens that replace every page (not paired,
// removed, paused, ended, no longer shared, busy library), library and
// album shares (redirects, lists, titles, which requests are made), the
// lightbox's controls per capability, ratings, and touch gestures.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fakeShareServer, go, settle, type FakeShareOptions, type FakeShareServer } from './testing/fakes';

let server: FakeShareServer;
let unmountApp: (() => void) | null = null;
/** The app's own flushSync (the modules are reset for every test). */
let flushSync = () => {};
const errors: unknown[] = [];

function setup(kind: 'libraries' | 'album' = 'libraries', opts: FakeShareOptions = {}) {
  server = fakeShareServer(kind, opts);
  server.install();
}

async function start(hash = '#/') {
  history.replaceState(null, '', '/' + hash);
  const svelte = await import('svelte');
  flushSync = () => svelte.flushSync();
  const ShareApp = (await import('./share/ShareApp.svelte')).default;
  const app = svelte.mount(ShareApp, { target: document.body });
  unmountApp = () => void svelte.unmount(app);
  await settle();
}

const click = (el: Element) => el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
const text = () => document.body.textContent ?? '';
const buttons = () => [...document.querySelectorAll<HTMLButtonElement | HTMLAnchorElement>('button, a')];
const button = (label: string) => buttons().find((b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label);
const lightbox = () => document.querySelector<HTMLElement>('.lightbox');
const tiles = () => [...document.querySelectorAll<HTMLAnchorElement>('a.tile')];
const paths = () => server.calls.map((c) => c.path);

function type(el: HTMLInputElement, value: string) {
  el.value = value;
  el.dispatchEvent(new Event('input', { bubbles: true }));
  flushSync();
}

/** A touch event with one finger at (x, y); jsdom has no Touch constructor. */
function touch(el: Element, kind: 'touchstart' | 'touchmove' | 'touchend', x: number, y: number) {
  const e = new Event(kind, { bubbles: true, cancelable: true });
  const t = { clientX: x, clientY: y, identifier: 0, target: el };
  Object.defineProperty(e, 'touches', { value: kind === 'touchend' ? [] : [t] });
  Object.defineProperty(e, 'changedTouches', { value: [t] });
  el.dispatchEvent(e);
  return e;
}

async function swipe(from: [number, number], to: [number, number]) {
  const stage = document.querySelector('.lightbox .stage')!;
  touch(stage, 'touchstart', ...from);
  const mid = touch(stage, 'touchmove', (from[0] + to[0]) / 2, (from[1] + to[1]) / 2);
  touch(stage, 'touchend', ...to);
  await settle();
  return mid;
}

describe('phone app', () => {
  beforeEach(() => {
    vi.resetModules();
    errors.length = 0;
    vi.spyOn(console, 'error').mockImplementation((...a: unknown[]) => void errors.push(a));
    vi.spyOn(console, 'warn').mockImplementation((...a: unknown[]) => void errors.push(a));
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

  describe('pairing', () => {
    it('takes the token out of the address bar, names the phone and pairs', async () => {
      setup('libraries', { paired: false });
      const token = 'tok_' + 'a'.repeat(40);
      const replace = vi.spyOn(history, 'replaceState');
      await start(`#/pair/${token}`);
      expect(location.hash).toBe('#/pair');
      expect(replace.mock.calls.some((c) => String(c[2]).endsWith('#/pair'))).toBe(true);
      expect(location.href).not.toContain(token);
      // No code field with a token: only the name.
      expect(document.querySelector('input[aria-label="Pairing code"]')).toBeNull();
      const name = document.querySelector<HTMLInputElement>('input[aria-label="Phone name"]')!;
      expect(name.value).toBe('Phone');
      type(name, "Anna's phone");
      click(button('Pair')!);
      await settle();
      const post = server.calls.find((c) => c.path === '/api/pair')!;
      expect(post.body).toEqual({ token, name: "Anna's phone" });
      // Two libraries: the list.
      expect(location.hash).toBe('#/');
      expect(document.querySelector('ul[aria-label="Libraries"]')?.textContent).toContain('Peter');
      expect(text()).toContain('Penny');
      expect(document.querySelector('.topbar')?.textContent).toContain('My phone');
    });

    it('pairs with a typed code and opens an album share straight into its album', async () => {
      setup('album', { paired: false });
      await start('#/');
      expect(text()).toContain('Not paired');
      const code = document.querySelector<HTMLInputElement>('input[aria-label="Pairing code"]')!;
      type(code, 'k7qm-3xr');
      expect((button('Pair') as HTMLButtonElement).disabled).toBe(true);
      type(code, 'k7qm-3xrd');
      click(button('Pair')!);
      await settle();
      expect(server.calls.find((c) => c.path === '/api/pair')?.body).toMatchObject({ code: 'K7QM3XRD' });
      expect(location.hash).toBe('#/');
      expect(document.querySelector('.topbar .title')?.textContent).toBe('Summer guests');
      expect(document.querySelector('.topbar .sub')?.textContent).toContain('Summer · 3 items');
      expect(tiles()).toHaveLength(3);
    });

    it('falls back to the code when the token expired or was used, and says when there were too many tries', async () => {
      setup('libraries', { paired: false });
      await start('#/pair/tok_' + 'b'.repeat(40));
      click(button('Pair')!);
      await settle();
      expect(document.querySelector('[role="alert"]')?.textContent).toContain('expired or was already used');
      const code = document.querySelector<HTMLInputElement>('input[aria-label="Pairing code"]')!;
      expect(code).not.toBeNull();
      server.pairing.limited = true;
      type(code, 'K7QM3XRD');
      click(button('Pair')!);
      await settle();
      expect(document.querySelector('[role="alert"]')?.textContent).toContain('Too many tries');
      click(button('Pair')!);
      await settle();
      expect(location.hash).toBe('#/');
    });

    it('asks before moving a phone paired with another share over', async () => {
      setup('album');
      server.cookie.otherShare = 'My phone';
      await start('#/pair/tok_' + 'a'.repeat(40));
      click(button('Pair')!);
      await settle();
      expect(text()).toContain('already paired with “My phone”');
      click(button('Pair with this share')!);
      await settle();
      const posts = server.calls.filter((c) => c.path === '/api/pair');
      expect(posts).toHaveLength(2);
      expect(posts[0]?.body?.replace).toBeUndefined();
      expect(posts[1]?.body).toMatchObject({ replace: true });
      expect(document.querySelector('.topbar .title')?.textContent).toBe('Summer guests');
    });

    it('keeps the current share when the phone says no', async () => {
      setup('album');
      server.cookie.otherShare = 'My phone';
      await start('#/pair/tok_' + 'a'.repeat(40));
      click(button('Pair')!);
      await settle();
      click(button('Keep “My phone”')!);
      await settle();
      expect(location.hash).toBe('#/');
      expect(server.calls.filter((c) => c.path === '/api/pair')).toHaveLength(1);
    });

    it('waits while the computer asks for approval, and says when it was denied', async () => {
      setup('libraries', { paired: false });
      server.pairing.approve = true;
      await start('#/pair/tok_' + 'a'.repeat(40));
      click(button('Pair')!);
      await settle();
      expect(text()).toContain('Waiting for the computer');
      server.deny();
      await settle();
      expect(text()).toContain('did not approve');
      // A new code, approved this time.
      type(document.querySelector<HTMLInputElement>('input[aria-label="Pairing code"]')!, 'K7QM3XRD');
      click(button('Pair')!);
      await settle();
      expect(text()).toContain('Waiting for the computer');
      server.approve();
      await settle();
      expect(location.hash).toBe('#/');
      expect(text()).toContain('Penny');
    });

    it("shows the server's message for a share that refuses phones (410 with a state) or is full (409 without current)", async () => {
      setup('libraries', { paired: false });
      server.share.state = 'paused';
      await start('#/pair/tok_' + 'a'.repeat(40));
      click(button('Pair')!);
      await settle();
      expect(document.querySelector('[role="alert"]')?.textContent).toContain('This share is paused.');
      // The token form fell back to the code.
      const code = document.querySelector<HTMLInputElement>('input[aria-label="Pairing code"]')!;
      server.share.state = '';
      server.pairing.full = true;
      type(code, 'K7QM3XRD');
      click(button('Pair')!);
      await settle();
      expect(document.querySelector('[role="alert"]')?.textContent).toContain('this share has 20 phones');
      expect(text()).not.toContain('already paired');
      expect(button('Pair with this share')).toBeUndefined();
    });
  });

  describe('screens that replace every page', () => {
    it('says the phone was removed when a request meets 401 after pairing', async () => {
      setup('libraries');
      await start('#/l/peter/');
      expect(tiles().length).toBeGreaterThan(0);
      server.cookie.paired = false;
      await go('#/l/peter/albums');
      expect(text()).toContain('This phone was removed from picmover on the computer');
      expect(document.querySelector('input[aria-label="Pairing code"]')).not.toBeNull();
      expect(tiles()).toHaveLength(0);
    });

    it.each([
      ['paused', 'This share is paused'],
      ['expired', 'This share has ended'],
      ['gone', 'This album is no longer shared'],
      ['unavailable', 'not available right now'],
    ] as const)('shows the %s screen and nothing else', async (state, title) => {
      setup('album');
      server.share.state = state;
      await start('#/');
      expect(text()).toContain(title);
      expect(tiles()).toHaveLength(0);
      expect(document.querySelector('.topbar')).toBeNull();
      server.share.state = '';
      click(button('Try again')!);
      await settle();
      expect(tiles()).toHaveLength(3);
    });

    it('switches to the paused screen when a later request meets 403 with a state', async () => {
      setup('libraries');
      await start('#/l/peter/');
      server.share.state = 'paused';
      await go('#/l/peter/albums');
      expect(text()).toContain('This share is paused');
    });

    it('says a busy library is not available right now, and retries', async () => {
      setup('libraries');
      server.busy.add('peter');
      await start('#/l/peter/');
      expect(text()).toContain('This library is not available right now (the computer is busy with it)');
      server.busy.delete('peter');
      click(button('Try again')!);
      await settle();
      expect(tiles()).toHaveLength(8);
    });

    it("says an album share's busy library is not available right now (503 on the session)", async () => {
      setup('album');
      server.busy.add('peter');
      await start('#/');
      expect(text()).toContain('Not available right now');
      expect(text()).not.toContain('Cannot reach the computer');
      server.busy.delete('peter');
      click(button('Try again')!);
      await settle();
      expect(tiles()).toHaveLength(3);
    });

    it('says when the computer cannot be reached', async () => {
      setup('libraries');
      vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new TypeError('Failed to fetch'))));
      await start('#/');
      expect(text()).toContain('Cannot reach the computer');
    });
  });

  describe('library share', () => {
    it('redirects #/ into the only library', async () => {
      setup('libraries', { libraries: ['peter'] });
      await start('#/');
      expect(location.hash).toBe('#/l/peter/');
      expect(tiles()).toHaveLength(8);
      // The grid stays on the thumb.
      expect(tiles()[0]?.querySelector('img')?.getAttribute('src')).toBe(`/api/l/peter/thumb/${'1'.padStart(16, '0')}?v=1700000000`);
      expect(document.querySelector('.cell input[type="checkbox"]')).toBeNull();
    });

    it('lists the albums and opens one', async () => {
      setup('libraries');
      await start('#/l/peter/albums');
      const list = document.querySelector('ul[aria-label="Albums"]')!;
      expect(list.textContent).toContain('Summer');
      expect(list.textContent).toContain('3 items');
      click(list.querySelector('a')!);
      location.hash = list.querySelector('a')!.getAttribute('href')!;
      await settle();
      expect(location.hash).toBe('#/l/peter/albums/2');
      expect(document.querySelector('.albumhead')?.textContent).toContain('Summer');
      expect(tiles()).toHaveLength(3);
      expect(server.requests('/api/l/peter/timeline').at(-1)?.path).toBe('/api/l/peter/timeline?album=2');
    });

    it('sends a library outside the share and unknown routes home', async () => {
      setup('libraries');
      await start('#/l/secret/');
      expect(location.hash).toBe('#/');
      await go('#/view/1');
      expect(location.hash).toBe('#/');
      expect(paths().some((p) => p.startsWith('/api/l/secret'))).toBe(false);
    });

    it('filters by type, rating and years through the route', async () => {
      setup('libraries');
      await start('#/l/peter/');
      click(button('Filter')!);
      await settle();
      click(button('Videos')!);
      click(button('4★')!);
      type(document.querySelector<HTMLInputElement>('input[aria-label="From year"]')!, '2024');
      type(document.querySelector<HTMLInputElement>('input[aria-label="To year"]')!, '2024');
      click(button('Apply')!);
      await settle();
      expect(location.hash).toBe('#/l/peter/?type=video&from=2024-01-01&to=2025-01-01&rating=4');
      expect(server.requests('/api/l/peter/timeline').at(-1)?.path).toBe('/api/l/peter/timeline?type=video&from=2024-01-01&to=2025-01-01&rating=4');
      expect(text()).toContain('Nothing matches these filters');
      click(button('Clear filters')!);
      await settle();
      expect(location.hash).toBe('#/l/peter/');
    });
  });

  describe('album share', () => {
    it('is the album and nothing else: no library or album routes or requests', async () => {
      setup('album');
      await start('#/');
      expect(document.querySelector('.topbar .title')?.textContent).toBe('Summer guests');
      expect(document.querySelector('nav.tabs')).toBeNull();
      expect(tiles()).toHaveLength(3);
      for (const h of ['#/l/peter/', '#/l/album/albums', '#/l/peter/albums/2']) {
        await go(h);
        expect(location.hash).toBe('#/');
      }
      expect(paths().filter((p) => p.startsWith('/api/l/')).every((p) => p.startsWith('/api/l/album/'))).toBe(true);
      expect(paths().some((p) => p === '/api/libraries' || p.endsWith('/library') || p.includes('/albums'))).toBe(false);
      // No rating filter without `rate`.
      click(button('Filter')!);
      await settle();
      expect(text()).not.toContain('Rating at least');
      // The menu: who this phone is and until when.
      click(button('Menu')!);
      await settle();
      const menu = document.querySelector('[role="menu"]')!;
      expect(menu.textContent).toContain("Paired as Anna's phone");
      expect(menu.textContent).toContain('Available until 13 Nov');
      expect(menu.textContent).not.toContain('Libraries');
    });
  });

  describe('lightbox', () => {
    it('in an album share: no stars, no Download, no 1:1 original and no location', async () => {
      setup('album');
      await start('#/');
      click(tiles()[0]!);
      await settle();
      expect(location.hash).toBe('#/view/1');
      const lb = lightbox()!;
      expect(lb.querySelector('[aria-label="Rating"]')).toBeNull();
      expect([...lb.querySelectorAll('a')].some((a) => a.textContent?.trim() === 'Download')).toBe(false);
      click(button('Info')!);
      await settle();
      expect(lb.textContent).toContain('Canon EOS R6');
      expect(lb.textContent).not.toContain('Location');
      expect(lb.textContent).not.toContain('Albums');
      // Double-click zooms the preview, never the original.
      lb.querySelector('.stage')!.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }));
      await settle();
      expect(lb.querySelector('.full img')?.getAttribute('src')).toContain('/api/l/album/preview/');
      expect(paths().some((p) => p.includes('/file/'))).toBe(false);
      // No admin controls, ever.
      for (const label of ['Trash', 'Move to…', 'Restore', 'Add tag (T)']) expect(button(label)).toBeUndefined();
    });

    it('in a library share: stars, Download, the original at 1:1, albums and the location', async () => {
      setup('libraries');
      await start('#/l/peter/');
      click(tiles()[0]!);
      await settle();
      expect(location.hash).toBe('#/l/peter/view/1');
      const lb = lightbox()!;
      expect(lb.querySelectorAll('[aria-label="Rating"] button')).toHaveLength(5);
      expect([...lb.querySelectorAll('a')].find((a) => a.textContent?.trim() === 'Download')?.getAttribute('href')).toBe('/api/l/peter/file/1?download=1');
      click(button('Info')!);
      await settle();
      expect(lb.textContent).toContain('60.17000, 24.94000');
      expect(lb.querySelector('dd a[href="#/l/peter/albums/2"]')?.textContent).toBe('Summer');
      lb.querySelector('.stage')!.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }));
      await settle();
      expect(lb.querySelector('.full img')?.getAttribute('src')).toBe('/api/l/peter/file/1');
    });

    it('plays videos inline with a Download fallback, or says they are not available', async () => {
      setup('libraries');
      await start('#/l/peter/view/3');
      const video = lightbox()!.querySelector('video')!;
      expect(video.hasAttribute('playsinline')).toBe(true);
      expect(video.getAttribute('src')).toBe('/api/l/peter/file/3');
      video.dispatchEvent(new Event('error'));
      await settle();
      expect(lightbox()!.textContent).toContain('This video cannot be played in this browser');
      expect(lightbox()!.querySelector('.banner a')?.getAttribute('href')).toBe('/api/l/peter/file/3?download=1');
    });

    it('shows a video as its poster when the share does not allow videos', async () => {
      setup('album', { capabilities: { video: false } });
      await start('#/view/3');
      expect(lightbox()!.querySelector('video')).toBeNull();
      expect(lightbox()!.textContent).toContain('Video not available in this share');
    });

    it('rates one hash, and rolls back with a clear message while a move runs', async () => {
      setup('libraries');
      await start('#/l/peter/view/2');
      const stars = () => [...lightbox()!.querySelectorAll<HTMLButtonElement>('[aria-label="Rating"] button')];
      click(stars()[2]!);
      await settle();
      expect(server.calls.filter((c) => c.path === '/api/l/peter/ratings').map((c) => c.body)).toEqual([{ hashes: [server.items[1]!.hash], rating: 3 }]);
      expect(stars().filter((s) => s.classList.contains('on'))).toHaveLength(3);
      server.moving.on = true;
      click(stars()[4]!);
      await settle();
      expect(lightbox()!.textContent).toContain('Ratings are paused while photos are being moved on the computer');
      expect(stars().filter((s) => s.classList.contains('on'))).toHaveLength(3);
      expect(server.items[1]!.rating).toBe(3);
    });

    it('re-reads the session when ratings were turned off meanwhile', async () => {
      setup('libraries');
      await start('#/l/peter/view/2');
      server.caps.rate = false;
      const sessions = server.requests('/api/session').length;
      click(lightbox()!.querySelectorAll('[aria-label="Rating"] button')[0]!);
      await settle();
      expect(server.requests('/api/session').length).toBe(sessions + 1);
      expect(lightbox()!.querySelector('[aria-label="Rating"]')).toBeNull();
    });

    it('says a photo is no longer in the album', async () => {
      setup('album');
      await start('#/');
      server.albums[0]!.hashes.splice(0, 1);
      click(tiles()[0]!);
      await settle();
      expect(lightbox()!.textContent).toContain('This photo is no longer in this album');
    });

    it('swipes from the middle turn the page; swipes from an edge do not; swipe down closes', async () => {
      setup('libraries');
      await start('#/l/peter/');
      click(tiles()[1]!);
      await settle();
      expect(location.hash).toBe('#/l/peter/view/2');
      // From the left edge: the system's back gesture, not ours.
      await swipe([10, 400], [300, 400]);
      expect(location.hash).toBe('#/l/peter/view/2');
      // From the middle, to the left: next.
      const move = await swipe([600, 400], [400, 410]);
      expect(move.defaultPrevented).toBe(true);
      expect(location.hash).toBe('#/l/peter/view/3');
      await swipe([400, 400], [650, 400]);
      expect(location.hash).toBe('#/l/peter/view/2');
      // Down: close, back to the grid.
      await swipe([500, 200], [510, 450]);
      await vi.waitFor(() => expect(location.hash).toBe('#/l/peter/'));
      expect(lightbox()).toBeNull();
    });

    it('a tap toggles the bars and a double tap zooms', async () => {
      setup('libraries');
      await start('#/l/peter/view/1');
      const stage = lightbox()!.querySelector('.stage')!;
      touch(stage, 'touchstart', 500, 400);
      const end = touch(stage, 'touchend', 500, 400);
      expect(end.defaultPrevented).toBe(true);
      await settle();
      expect(lightbox()!.classList.contains('bare')).toBe(true);
      touch(stage, 'touchstart', 500, 400);
      touch(stage, 'touchend', 500, 400);
      touch(stage, 'touchstart', 502, 401);
      touch(stage, 'touchend', 502, 401);
      await settle();
      expect(lightbox()!.querySelector('.full')).not.toBeNull();
      expect(lightbox()!.classList.contains('bare')).toBe(true);
    });
  });
});
