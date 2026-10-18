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

    it('a phone already paired with this very share is told so and carries on', async () => {
      setup('libraries');
      await start('#/pair/tok_' + 'a'.repeat(40));
      click(button('Pair')!);
      await settle();
      expect(text()).toContain('This phone is already paired with this share (“My phone”).');
      expect(text()).not.toContain('instead');
      expect(button('Pair with this share')).toBeUndefined();
      click(button('Continue')!);
      await settle();
      expect(location.hash).toBe('#/');
      expect(server.calls.filter((c) => c.path === '/api/pair')).toHaveLength(1);
      expect(text()).toContain('Penny');
    });

    it('hints at misread characters as the code is typed, with a keyboard set up for codes', async () => {
      setup('libraries', { paired: false });
      await start('#/');
      const code = document.querySelector<HTMLInputElement>('input[aria-label="Pairing code"]')!;
      expect(code.getAttribute('autocapitalize')).toBe('characters');
      expect(code.getAttribute('autocorrect')).toBe('off');
      expect(code.getAttribute('spellcheck')).toBe('false');
      expect(code.getAttribute('inputmode')).toBe('text');
      const hint = () => document.getElementById('code-hint')?.textContent ?? '';
      type(code, 'k7qm-3x');
      expect(hint()).toBe('');
      type(code, 'k7qm-3xr0');
      expect(hint()).toBe('Codes don’t use 0, 1, I, L or O. Check the code on the computer.');
      expect(code.getAttribute('aria-invalid')).toBe('true');
      expect((button('Pair') as HTMLButtonElement).disabled).toBe(true);
      type(code, 'k7qm-3xrd');
      expect(hint()).toBe('');
      expect((button('Pair') as HTMLButtonElement).disabled).toBe(false);
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

    it('says when sharing was turned off while the computer was asked, and starts over', async () => {
      setup('libraries', { paired: false });
      server.pairing.approve = true;
      await start('#/pair/tok_' + 'a'.repeat(40));
      click(button('Pair')!);
      await settle();
      expect(text()).toContain('Waiting for the computer');
      server.turnOff();
      await settle();
      expect(text()).toContain('Sharing was turned off on the computer');
      expect(text()).not.toContain('did not approve');
      click(button('Start over')!);
      await settle();
      // The token went with it: the code form, empty.
      const code = document.querySelector<HTMLInputElement>('input[aria-label="Pairing code"]')!;
      expect(code.value).toBe('');
      expect(document.querySelector('[role="alert"]')).toBeNull();
      type(code, 'K7QM3XRD');
      server.pairing.approve = false;
      click(button('Pair')!);
      await settle();
      expect(location.hash).toBe('#/');
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

    it('loads a thumbnail again after a 429 (honouring Retry-After), and gives up on a 404', async () => {
      setup('libraries');
      await start('#/l/peter/');
      const img = (i: number) => tiles()[i]!.querySelector('img');
      const thumbChecks = () => server.calls.filter((c) => c.path.includes('/thumb/')).length;
      server.mediaFail.times = 1;
      server.mediaFail.retryAfter = '3';
      img(0)!.dispatchEvent(new Event('error'));
      await settle();
      // Grey while the computer asked to wait; one check of the URL, not one per tile.
      expect(img(0)).toBeNull();
      expect(thumbChecks()).toBe(1);
      await vi.advanceTimersByTimeAsync(2_000);
      expect(img(0)?.getAttribute('src')).toBe(`/api/l/peter/thumb/${'1'.padStart(16, '0')}?v=1700000000`);
      // A thumbnail that is gone stays grey.
      server.mediaFail.times = 1;
      server.mediaFail.status = 404;
      server.mediaFail.retryAfter = '';
      img(1)!.dispatchEvent(new Event('error'));
      await vi.advanceTimersByTimeAsync(120_000);
      expect(img(1)).toBeNull();
      expect(thumbChecks()).toBe(2);
    });

    it('back on the page, reads the timeline once and refetches the months only when it changed', async () => {
      setup('libraries');
      await start('#/l/peter/');
      const media = () => server.requests('/api/l/peter/media?').length;
      const timelines = () => server.requests('/api/l/peter/timeline').length;
      const [m0, t0] = [media(), timelines()];
      Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true });
      document.dispatchEvent(new Event('visibilitychange'));
      await settle();
      expect(timelines()).toBe(t0 + 1);
      expect(media()).toBe(m0);
      expect(tiles()).toHaveLength(8);
      // A photo went meanwhile: the months are read again.
      server.items.splice(1, 1);
      document.dispatchEvent(new Event('visibilitychange'));
      await settle();
      expect(media()).toBeGreaterThan(m0);
      expect(tiles()).toHaveLength(7);
    });

    it('closing a lightbox opened from a link scrolls the grid to the photo it showed', async () => {
      setup('libraries');
      for (let i = 0; i < 90; i++) {
        const id = 100 + i;
        server.items.push({ id, hash: id.toString(16).padStart(16, '0'), type: 'image', takenAt: `2023-03-${String(1 + (i % 28)).padStart(2, '0')}T10:00:00`, w: 400, h: 300, thumb: 'ok', lib: 'peter' });
      }
      await start('#/l/peter/view/150');
      const scroller = document.querySelector<HTMLElement>('.share-grid .scroller')!;
      expect(scroller.scrollTop).toBe(0);
      click(button('Close')!);
      await settle();
      expect(location.hash).toBe('#/l/peter/');
      expect(scroller.scrollTop).toBeGreaterThan(0);
      expect(document.querySelector('a.tile[href="#/l/peter/view/150"]')).not.toBeNull();
    });

    it('names the library after the share with a spaced dot', async () => {
      setup('libraries');
      await start('#/l/peter/');
      expect(document.querySelector('.topbar .title')?.textContent).toBe('My phone · Peter');
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
  });

  describe('filters', () => {
    const sheet = () => document.querySelector<HTMLFormElement>('form.sheet');
    const sections = () => [...(sheet()?.querySelectorAll('h3') ?? [])].map((h) => h.textContent?.trim());
    const inSheet = (label: string) =>
      [...(sheet()?.querySelectorAll<HTMLButtonElement>('button') ?? [])].find((b) => b.getAttribute('aria-label') === label || b.firstChild?.textContent?.trim() === label || b.textContent?.trim() === label);
    const select = (label: string) => sheet()!.querySelector<HTMLSelectElement>(`select[aria-label="${label}"]`)!;
    /** The camera choices (name and count), and one of them by name. */
    const cameras = () => [...(sheet()?.querySelectorAll('.cams [role="radio"]') ?? [])].map((b) => b.textContent?.trim());
    const camera = (name: string) => [...sheet()!.querySelectorAll<HTMLButtonElement>('.cams [role="radio"]')].find((b) => b.querySelector('.name')?.textContent === name)!;
    const chips = () => [...document.querySelectorAll('.chips .chip .label')].map((c) => c.textContent?.trim());
    const filterButton = () => document.querySelector<HTMLButtonElement>('.topbar .filterbtn')!;
    function choose(sel: HTMLSelectElement, value: string) {
      sel.value = value;
      sel.dispatchEvent(new Event('change', { bubbles: true }));
      flushSync();
    }
    async function openSheet() {
      click(filterButton());
      await settle();
      expect(sheet()).not.toBeNull();
    }

    it('a visible Filter button opens a sheet with every section the share allows, from its facets', async () => {
      setup('libraries');
      await start('#/l/peter/');
      expect(filterButton().textContent?.trim()).toBe('Filter');
      await openSheet();
      expect(paths()).toContain('/api/l/peter/facets');
      // Camera right under Show: seen without scrolling the sheet.
      expect(sections()).toEqual(['Show', 'Camera', 'Dates', 'Rating at least', 'Location', 'Album', 'Tags', 'Size']);
      expect(cameras()).toEqual(['Any camera', 'Canon EOS R65', 'Pixel 72', 'Unknown camera1']);
      expect(camera('Any camera').getAttribute('aria-checked')).toBe('true');
      expect([...select('Album').options].map((o) => o.textContent)).toEqual(['Any album', 'Secret (1)', 'Summer (3)']);
      expect(inSheet('beach')?.textContent).toBe('beach2');
      // Years from the facets, with their counts.
      expect(inSheet('2024')).toBeDefined();
      click(sheet()!.querySelector('button[aria-label="Close"]')!);
      await settle();
      expect(sheet()).toBeNull();
    });

    it('applies through the route, shows removable chips and a count, and keeps them on reload', async () => {
      setup('libraries');
      await start('#/l/peter/');
      await openSheet();
      click(inSheet('Photos')!);
      click(inSheet('2024')!);
      click(camera('Canon EOS R6'));
      click(inSheet('beach')!);
      flushSync();
      click(inSheet('Apply')!);
      await settle();
      expect(sheet()).toBeNull();
      const want = '?type=image&from=2024-01-01&to=2025-01-01&camera=Canon+EOS+R6&tag=1';
      expect(location.hash).toBe(`#/l/peter/${want}`);
      expect(server.requests('/api/l/peter/timeline').at(-1)?.path).toBe(`/api/l/peter/timeline${want}`);
      expect(tiles().map((t) => t.getAttribute('href'))).toEqual([`#/l/peter/view/1${want}`, `#/l/peter/view/2${want}`]);
      expect(chips()).toEqual(['Photos', '2024', 'Canon EOS R6', '#beach']);
      expect(filterButton().getAttribute('aria-label')).toBe('Filter, 4 on');
      expect(filterButton().querySelector('.badge')?.textContent).toBe('4');
      // One chip's × removes that filter only.
      click(button('Remove filter #beach')!);
      await settle();
      expect(location.hash).toBe('#/l/peter/?type=image&from=2024-01-01&to=2025-01-01&camera=Canon+EOS+R6');
      // A reload keeps them; the sheet opens with them.
      unmountApp?.();
      document.body.innerHTML = '';
      await start(location.hash);
      expect(chips()).toEqual(['Photos', '2024', 'Canon EOS R6']);
      await openSheet();
      expect(inSheet('Photos')?.getAttribute('aria-pressed')).toBe('true');
      expect(camera('Canon EOS R6').getAttribute('aria-checked')).toBe('true');
      expect(camera('Any camera').getAttribute('aria-checked')).toBe('false');
      expect(select('From year').value).toBe('2024');
      expect(select('To month').value).toBe('12');
      click(inSheet('Clear all')!);
      click(inSheet('Apply')!);
      await settle();
      expect(location.hash).toBe('#/l/peter/');
      expect(document.querySelector('.chips')).toBeNull();
    });

    it('Clear all on the chips, and the empty state, clear every filter', async () => {
      setup('libraries');
      await start('#/l/peter/?type=video&rating=5');
      expect(chips()).toEqual(['Videos', '5★']);
      expect(text()).toContain('Nothing matches these filters');
      click(button('Clear filters')!);
      await settle();
      expect(location.hash).toBe('#/l/peter/');
      await go('#/l/peter/?type=video&months=7');
      click(document.querySelector<HTMLButtonElement>('.chips .clear')!);
      await settle();
      expect(location.hash).toBe('#/l/peter/');
    });

    it('cameras: the most used first, a search over maker and model, all on request, and the unknown camera', async () => {
      setup('libraries');
      // Six more cameras in peter, one photo each (in July 2024).
      const more = ['NIKON D750', 'iPhone 8', 'SM-G991B', 'ILCE-7M3', 'E-M10', 'X-T3'];
      more.forEach((cam, i) => {
        const id = 40 + i;
        server.items.push({ id, hash: id.toString(16).padStart(16, '0'), type: 'image', takenAt: '2024-07-01T09:00:00', w: 400, h: 300, thumb: 'ok', lib: 'peter', camera: cam });
      });
      await start('#/l/peter/');
      await openSheet();
      const search = sheet()!.querySelector<HTMLInputElement>('input[type="search"]')!;
      expect(search.getAttribute('aria-label')).toBe('Search cameras');
      // Any camera, the five most used, the unknown camera; the rest on request.
      expect(cameras()).toHaveLength(7);
      expect(cameras()[1]).toBe('Canon EOS R65');
      expect(cameras().at(-1)).toBe('Unknown camera1');
      click(inSheet('All 8 cameras')!);
      flushSync();
      expect(cameras()).toHaveLength(10);
      type(search, 'nikon');
      expect(cameras()).toEqual(['NIKON D7501']);
      type(search, 'zeiss');
      expect(cameras()).toEqual([]);
      expect(sheet()!.textContent).toContain('No camera matches “zeiss”.');
      type(search, 'unknown');
      click(camera('Unknown camera'));
      click(inSheet('Apply')!);
      await settle();
      expect(location.hash).toBe('#/l/peter/?nocamera=1');
      expect(server.requests('/api/l/peter/timeline').at(-1)?.path).toBe('/api/l/peter/timeline?nocamera=1');
      expect(chips()).toEqual(['Unknown camera']);
      // The one photo without a camera: the video.
      expect(tiles().map((t) => t.getAttribute('href'))).toEqual(['#/l/peter/view/3?nocamera=1']);
      // Picking a camera replaces the unknown camera.
      await openSheet();
      expect(camera('Unknown camera').getAttribute('aria-checked')).toBe('true');
      click(camera('Pixel 7'));
      click(inSheet('Apply')!);
      await settle();
      expect(location.hash).toBe('#/l/peter/?camera=Pixel+7');
    });

    it('every year: months and days like the admin', async () => {
      setup('libraries');
      await start('#/l/peter/');
      await openSheet();
      click(inSheet('Every year')!);
      flushSync();
      click(inSheet('June')!);
      click(inSheet('July')!);
      flushSync();
      click(inSheet('Apply')!);
      await settle();
      expect(location.hash).toBe('#/l/peter/?months=6,7');
      expect(chips()).toEqual(['Jun–Jul every year']);
      expect(tiles()).toHaveLength(8);
      await openSheet();
      click(inSheet('Christmas')!);
      flushSync();
      click(inSheet('Apply')!);
      await settle();
      // A day range replaces the months.
      expect(location.hash).toBe('#/l/peter/?recur=12-24..12-26');
      expect(text()).toContain('Nothing matches these filters');
    });

    it('the lightbox keeps the filters and turns pages within the filtered photos only', async () => {
      setup('libraries');
      await start('#/l/peter/?tag=1');
      expect(tiles()).toHaveLength(2);
      click(tiles()[0]!);
      await settle();
      expect(location.hash).toBe('#/l/peter/view/1?tag=1');
      click(button('Next')!);
      await settle();
      expect(location.hash).toBe('#/l/peter/view/2?tag=1');
      // Photo 3 is not tagged: there is no next page.
      expect(button('Next')).toBeUndefined();
      history.back();
      await settle();
      expect(location.hash).toBe('#/l/peter/?tag=1');
      expect(chips()).toEqual(['#beach']);
    });

    it('hides what the share may not use, and drops it from the route', async () => {
      setup('libraries', { capabilities: { rate: false, location: false } });
      await start('#/l/peter/?rating=3&gps=1&type=video');
      expect(server.requests('/api/l/peter/timeline').at(-1)?.path).toBe('/api/l/peter/timeline?type=video');
      expect(chips()).toEqual(['Videos']);
      await openSheet();
      expect(sections()).not.toContain('Rating at least');
      expect(sections()).not.toContain('Location');
      expect(server.calls.filter((c) => c.path.startsWith('/api/l/')).every((c) => !/[?&](rating|gps)=/.test(c.path))).toBe(true);
    });

    it('an album share filters within the album: facets of its photos only, no tags or albums', async () => {
      setup('album');
      await start('#/?tag=1&album=5&rating=3&gps=1&camera=Canon+EOS+R6');
      // Only what the album share may use reaches the server.
      expect(server.requests('/api/l/album/timeline').at(-1)?.path).toBe('/api/l/album/timeline?camera=Canon+EOS+R6');
      expect(chips()).toEqual(['Canon EOS R6']);
      expect(tiles()).toHaveLength(2);
      await openSheet();
      expect(paths()).toContain('/api/l/album/facets');
      expect(sections()).toEqual(['Show', 'Camera', 'Dates', 'Size']);
      // The Pixel took photos outside the album only: not offered. The album's video has no camera.
      expect(cameras()).toEqual(['Any camera', 'Canon EOS R62', 'Unknown camera1']);
      expect(text()).not.toContain('private');
      click(inSheet('Videos')!);
      click(camera('Any camera'));
      click(inSheet('Apply')!);
      await settle();
      expect(location.hash).toBe('#/?type=video');
      expect(tiles()).toHaveLength(1);
      click(tiles()[0]!);
      await settle();
      expect(location.hash).toBe('#/view/3?type=video');
      expect(paths().filter((p) => p.startsWith('/api/l/')).every((p) => p.startsWith('/api/l/album/'))).toBe(true);
      expect(paths().some((p) => /[?&](tag|album|rating|gps)=/.test(p))).toBe(false);
    });

    it('an album share with location offers the location filter', async () => {
      setup('album', { capabilities: { location: true } });
      await start('#/');
      await openSheet();
      expect(sections()).toContain('Location');
      expect(inSheet('Has location')?.textContent).toBe('Has location3');
      click(inSheet('Has location')!);
      flushSync();
      click(inSheet('Apply')!);
      await settle();
      expect(location.hash).toBe('#/?gps=1');
      expect(tiles()).toHaveLength(3);
    });
  });

  describe('capabilities changed on the computer', () => {
    const visible = () => {
      Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true });
      document.dispatchEvent(new Event('visibilitychange'));
    };
    const badges = () => document.querySelectorAll('a.tile .rating').length;

    it('drops the stars from tiles and lightbox when ratings are turned off, without a reload', async () => {
      setup('libraries');
      await start('#/l/peter/');
      expect(badges()).toBe(1);
      server.caps.rate = false;
      visible();
      await settle();
      expect(badges()).toBe(0);
      expect(document.querySelector('.topbar .filterbtn')).not.toBeNull();
      click(tiles()[0]!);
      await settle();
      expect(lightbox()!.querySelector('[aria-label="Rating"]')).toBeNull();
    });

    it('shows the home time, and the local time where it differs, in the details', async () => {
      setup('album');
      await start('#/view/1');
      const info = buttons().find((b) => b.textContent?.trim() === 'Info')!;
      click(info);
      await settle();
      const details = lightbox()!.querySelector('[aria-label="Details"]')!.textContent ?? '';
      expect(details).toMatch(/UTC\+02:00/);
      expect(details).toMatch(/\d\d:\d\d local \(UTC\+01:00\)/);
      expect(lightbox()!.querySelector('.title')?.textContent).toMatch(/UTC\+02:00$/);
    });

    it('shows what was turned on while the page is open', async () => {
      setup('album');
      await start('#/view/1');
      expect(lightbox()!.querySelector('[aria-label="Rating"]')).toBeNull();
      expect(badges()).toBe(0);
      server.caps.rate = true;
      server.caps.download = true;
      await vi.advanceTimersByTimeAsync(3 * 60_000);
      await settle();
      expect(lightbox()!.querySelectorAll('[aria-label="Rating"] button')).toHaveLength(5);
      expect([...lightbox()!.querySelectorAll('a')].some((a) => a.textContent?.trim() === 'Download')).toBe(true);
      expect(badges()).toBe(1);
    });

    it('a session read in the background that fails keeps the page; a refusal replaces it', async () => {
      setup('libraries');
      await start('#/l/peter/');
      vi.mocked(fetch).mockImplementationOnce(() => Promise.reject(new TypeError('Failed to fetch')));
      visible();
      await settle();
      expect(tiles()).toHaveLength(8);
      expect(text()).not.toContain('Cannot reach the computer');
      server.share.state = 'paused';
      visible();
      await settle();
      expect(text()).toContain('This share is paused');
    });

    it('says videos are no longer allowed when they were turned off during playback', async () => {
      setup('libraries');
      await start('#/l/peter/view/3');
      server.caps.video = false;
      const sessions = server.requests('/api/session').length;
      lightbox()!.querySelector('video')!.dispatchEvent(new Event('error'));
      await settle();
      expect(text()).not.toContain('cannot be played in this browser');
      expect(server.requests('/api/session').length).toBe(sessions + 1);
      expect(lightbox()!.querySelector('video')).toBeNull();
      expect(lightbox()!.textContent).toContain('Video not available in this share');
    });

    it('says when the computer cannot be reached or is busy, rather than blaming the browser', async () => {
      setup('libraries');
      await start('#/l/peter/view/3');
      server.mediaFail.times = 1;
      server.mediaFail.status = 503;
      lightbox()!.querySelector('video')!.dispatchEvent(new Event('error'));
      await settle();
      expect(lightbox()!.querySelector('.banner')?.textContent).toContain('The computer is busy. Try again in a moment.');
      click(button('Try again')!);
      await settle();
      expect(lightbox()!.querySelector('video')?.getAttribute('src')).toBe('/api/l/peter/file/3');
      vi.mocked(fetch).mockImplementationOnce(() => Promise.reject(new TypeError('Failed to fetch')));
      lightbox()!.querySelector('video')!.dispatchEvent(new Event('error'));
      await settle();
      expect(lightbox()!.querySelector('.banner')?.textContent).toContain('Cannot reach the computer');
    });

    it('falls back from an original that is no longer allowed', async () => {
      setup('libraries');
      await start('#/l/peter/view/1');
      lightbox()!.querySelector('.stage')!.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }));
      await settle();
      const img = lightbox()!.querySelector<HTMLImageElement>('.full img')!;
      expect(img.getAttribute('src')).toBe('/api/l/peter/file/1');
      server.caps.download = false;
      img.dispatchEvent(new Event('error'));
      await settle();
      expect(lightbox()!.querySelector('.note')?.textContent).toBe('Originals are no longer allowed in this share.');
      expect(lightbox()!.querySelector('.full img')?.getAttribute('src') ?? lightbox()!.querySelector('img.preview')?.getAttribute('src')).toContain('/preview/');
      expect([...lightbox()!.querySelectorAll('a')].some((a) => a.textContent?.trim() === 'Download')).toBe(false);
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
      // A plain disclosure (it holds text): no ARIA menu roles.
      const menu = document.getElementById('share-menu')!;
      expect(button('Menu')!.getAttribute('aria-controls')).toBe('share-menu');
      expect(button('Menu')!.getAttribute('aria-expanded')).toBe('true');
      expect(document.querySelector('[role="menu"], [role="menuitem"]')).toBeNull();
      expect(menu.textContent).toContain("Paired as Anna's phone");
      expect(menu.textContent).toContain('Available until 13 Nov');
      expect(menu.textContent).not.toContain('Libraries');
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', cancelable: true }));
      await settle();
      expect(document.getElementById('share-menu')).toBeNull();
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

    it('plays videos inline with a Download fallback', async () => {
      setup('libraries');
      await start('#/l/peter/view/3');
      const video = lightbox()!.querySelector('video')!;
      expect(video.hasAttribute('playsinline')).toBe(true);
      expect(video.getAttribute('src')).toBe('/api/l/peter/file/3');
      // Between the bars (layout.test.ts checks the inset), and a tap on it is the player's, not ours.
      expect(video.closest('.media')?.classList.contains('video')).toBe(true);
      touch(video, 'touchstart', 200, 300);
      touch(video, 'touchend', 200, 300);
      await settle();
      expect(lightbox()!.classList.contains('bare')).toBe(false);
      video.dispatchEvent(new Event('error'));
      await settle();
      // No conversion on this computer (no ffmpeg): the player says so, with Download.
      expect(lightbox()!.textContent).toContain("This browser can't play MP4 video files.");
      expect(lightbox()!.querySelector('.msgbar a')?.getAttribute('href')).toBe('/api/l/peter/file/3?download=1');
    });

    it('has a video this browser cannot decode converted by the computer', async () => {
      setup('libraries', { video: { video: { codec: 'hevc', profile: 'Main 10', bitDepth: 10, hdr: true }, canConvert: true, browserFormat: true } });
      await start('#/l/peter/view/3');
      expect(server.requests('/api/l/peter/stream/3/status').length).toBe(1);
      const video = lightbox()!.querySelector('video')!;
      expect(video.getAttribute('src')).toMatch(/^\/api\/l\/peter\/stream\/3\?mode=transcode/);
      // The conversion is still running: the player's own seek bar, between the bars.
      expect(lightbox()!.querySelector('.player input[type=range]')).not.toBeNull();
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

    it('keeps Tab inside the lightbox (no inert on Gecko 91)', async () => {
      setup('libraries');
      await start('#/l/peter/view/2');
      const lb = lightbox()!;
      const focusables = [...lb.querySelectorAll<HTMLElement>('a[href], button:not([disabled])')];
      const tab = (shift = false) => {
        const e = new KeyboardEvent('keydown', { key: 'Tab', shiftKey: shift, bubbles: true, cancelable: true });
        (document.activeElement ?? document.body).dispatchEvent(e);
        return e;
      };
      focusables.at(-1)!.focus();
      expect(tab().defaultPrevented).toBe(true);
      expect(document.activeElement).toBe(focusables[0]);
      expect(tab(true).defaultPrevented).toBe(true);
      expect(document.activeElement).toBe(focusables.at(-1));
      // From the page behind (focus escaped somehow): back into the lightbox.
      document.querySelector<HTMLElement>('.topbar button')!.focus();
      tab();
      expect(lb.contains(document.activeElement)).toBe(true);
      // In the middle, Tab is the browser's.
      focusables[0]!.focus();
      expect(tab().defaultPrevented).toBe(false);
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
