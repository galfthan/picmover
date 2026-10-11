// @vitest-environment jsdom
//
// Step 4 flows through the whole app against the fake server: selecting in
// the grid (Ctrl/Cmd and Shift ranges, Escape), in-place updates after
// rating and trashing, the lightbox keys, the trash page with Empty trash,
// album pages, the duplicates page and the new filters.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fakeServer, go, hashOf, settle, type FakeServer } from './testing/fakes';

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

const tiles = () => [...document.querySelectorAll<HTMLAnchorElement>('a.tile')];
const tile = (id: number) => document.querySelector<HTMLAnchorElement>(`a.tile[href="#/l/peter/view/${id}"]`)!;
const click = (el: Element, init: MouseEventInit = {}) => el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0, ...init }));
const key = (k: string, target: EventTarget = window) => target.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true }));
const button = (root: ParentNode, label: string) => [...root.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === label);
const toastText = () => [...document.querySelectorAll('.toast .text')].map((t) => t.textContent);
const navTrash = () => [...document.querySelectorAll('nav a')].find((a) => a.textContent?.startsWith('Trash'))?.textContent;
const check = (id: number) => document.querySelector<HTMLInputElement>(`a.tile[href="#/l/peter/view/${id}"] + input.check`)!;
const toast = (text: string) => [...document.querySelectorAll<HTMLElement>('.toast')].find((t) => t.querySelector('.text')?.textContent === text)!;

describe('management flows', () => {
  beforeEach(() => {
    vi.resetModules();
    server = fakeServer('single');
    server.install();
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

  it('selects tiles, rates and trashes them in place', async () => {
    await start();
    expect(location.hash).toBe('#/l/peter/');
    expect(tiles()).toHaveLength(8);

    // Ctrl-click toggles, Shift-click selects the range in display order.
    const canvasHeight = () => parseFloat(document.querySelector<HTMLElement>('.canvas')!.style.height);
    const plain = canvasHeight();
    click(tile(2), { ctrlKey: true });
    await settle();
    // While the bar is shown the grid grows by its height (48 px in jsdom) plus its margin.
    expect(canvasHeight()).toBe(plain + 48 + 24);
    expect(location.hash).toBe('#/l/peter/');
    expect(document.querySelector('.selbar')?.textContent).toContain('1 selected');
    expect(tile(2).parentElement?.classList.contains('selected')).toBe(true);
    expect(document.querySelectorAll('.cell.selecting').length).toBe(8);
    click(tile(7), { shiftKey: true });
    await settle();
    expect(document.querySelector('.selbar')?.textContent).toContain('6 selected');
    // The checkbox toggles too.
    click(document.querySelector<HTMLInputElement>(`a.tile[href="#/l/peter/view/7"] + input.check`)!);
    await settle();
    expect(document.querySelector('.selbar')?.textContent).toContain('5 selected');

    // Rate: tiles update in place, no timeline reload.
    const timelines = () => server.calls.filter((c) => c.path.startsWith('/api/l/peter/timeline')).length;
    const before = timelines();
    document.querySelector<HTMLButtonElement>('.selbar [aria-label="Rate 3 stars"]')!.click();
    await settle();
    expect(tile(2).querySelector('.rating')?.textContent).toBe('★ 3');
    expect(tile(6).querySelector('.rating')?.textContent).toBe('★ 3');
    expect(tile(1).querySelector('.rating')).toBeNull();
    expect(timelines()).toBe(before);
    // The bar now shows the shared rating.
    expect(document.querySelector('.selbar [aria-label^="Clear rating (3"]')).not.toBeNull();

    // Trash: the tiles leave the grid, the nav count follows, Undo brings them back.
    button(document.querySelector('.selbar')!, 'Trash')!.click();
    await settle();
    expect(tiles().map((t) => t.getAttribute('href'))).toEqual(['#/l/peter/view/1', '#/l/peter/view/7', '#/l/peter/view/8']);
    expect(document.querySelector('.selbar')).toBeNull();
    expect([...document.querySelectorAll('nav a')].find((a) => a.textContent?.startsWith('Trash'))?.textContent).toBe('Trash6');
    expect(toastText()).toContain('Moved 5 items to the trash');
    button(toast('Moved 5 items to the trash'), 'Undo')!.click();
    await settle();
    expect(server.posts('l/peter/trash/restore')[0]?.body).toEqual({ hashes: [2, 3, 4, 5, 6].map(hashOf) });
    expect(tiles()).toHaveLength(8);

    // Escape clears; a filter change clears too.
    click(tile(1), { metaKey: true });
    await settle();
    key('Escape');
    await settle();
    expect(document.querySelector('.selbar')).toBeNull();
    expect(canvasHeight()).toBe(plain);
    click(tile(1), { metaKey: true });
    await settle();
    await go('#/l/peter/?rating=3');
    expect(document.querySelector('.selbar')).toBeNull();
    expect(tiles()).toHaveLength(5);
  });

  it('shows a Shift-clicked checkbox of a selected tile as checked', async () => {
    await start();
    click(tile(2), { ctrlKey: true });
    click(tile(4), { ctrlKey: true });
    await settle();
    // Shift on tile 2's box selects 2..4; tile 2 stays selected although the browser flipped its box.
    click(check(2), { shiftKey: true });
    await settle();
    expect(document.querySelector('.selbar')?.textContent).toContain('3 selected');
    expect(tile(2).parentElement?.classList.contains('selected')).toBe(true);
    expect(check(2).checked).toBe(true);
  });

  it('forgets the selection bar message with the selection', async () => {
    await start();
    click(tile(1), { ctrlKey: true });
    await settle();
    server.items[0]!.deletedAt = '2026-10-10T11:00:00'; // trashed in another tab
    button(document.querySelector('.selbar')!, 'Trash')!.click();
    await settle();
    expect(document.querySelector('.selbar .err')?.textContent).toContain('could not be moved to the trash: already in the trash');
    button(document.querySelector('.selbar')!, 'Clear')!.click();
    await settle();
    click(tile(2), { ctrlKey: true });
    await settle();
    expect(document.querySelector('.selbar')?.textContent).toContain('1 selected');
    expect(document.querySelector('.selbar .err')).toBeNull();
  });

  it('reloads the library counts after a write made while a load was in flight', async () => {
    await start();
    expect(navTrash()).toBe('Trash1');
    const { library } = await import('./lib/library.svelte');
    server.items[0]!.deletedAt = '2026-10-10T11:00:00';
    const first = library.load(); // answered with two items in the trash
    server.items[1]!.deletedAt = '2026-10-10T11:00:00';
    const second = library.load(); // shares the first, which then loads again
    await settle();
    await Promise.all([first, second]);
    expect(navTrash()).toBe('Trash3');
  });

  it('says so when Undo cannot restore', async () => {
    await start();
    tile(1).click();
    await settle();
    key('Delete');
    await settle();
    expect(toastText()).toContain('Moved to the trash');
    // The file left the trash meanwhile: the server answers 200 with a failure.
    server.items.splice(0, 1);
    button(toast('Moved to the trash'), 'Undo')!.click();
    await settle();
    expect(server.posts('l/peter/trash/restore')).toHaveLength(1);
    expect(toastText()).toContain('1 item could not be restored: not in the catalog');
    expect(document.querySelector('.toast.error')).not.toBeNull();
  });

  it('rates, tags and trashes in the lightbox', async () => {
    await start();
    tile(1).click();
    await settle();
    expect(location.hash).toBe('#/l/peter/view/1');
    const lb = document.querySelector<HTMLElement>('.lightbox')!;
    expect(lb.textContent).toContain('Summer'); // album chip
    key('4');
    await settle();
    expect(server.posts('l/peter/ratings')[0]?.body).toEqual({ hashes: [hashOf(1)], rating: 4 });
    expect(lb.querySelectorAll('.info .star.on').length).toBe(4);

    key('t');
    await settle();
    const input = lb.querySelector<HTMLInputElement>('input[aria-label="Add tag (T)"]')!;
    expect(document.activeElement).toBe(input);
    input.value = 'Sunset';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();
    key('Enter', input);
    await settle();
    expect(server.posts('l/peter/media/tags')[0]?.body).toEqual({ hashes: [hashOf(1)], add: [], remove: [], create: ['Sunset'] });
    expect([...lb.querySelectorAll('.chips .chip a')].map((a) => a.textContent)).toEqual(['Sunset', 'Summer']);
    // Keys typed into the input are not shortcuts.
    input.blur();

    key('b');
    await settle();
    expect(lb.querySelector('.pop input')).not.toBeNull();
    key('Escape', lb.querySelector('.pop input')!);
    await settle();
    expect(lb.querySelector('.pop')).toBeNull();
    expect(location.hash).toBe('#/l/peter/view/1');

    // Delete trashes and moves on; the toast offers Undo.
    key('Delete');
    await settle();
    expect(server.posts('l/peter/trash')[0]?.body).toEqual({ hashes: [hashOf(1)] });
    expect(location.hash).toBe('#/l/peter/view/2');
    expect(toastText()).toContain('Moved to the trash');
    button(toast('Moved to the trash'), 'Undo')!.click();
    await settle();
    expect(server.posts('l/peter/trash/restore')).toHaveLength(1);
  });

  it('shows the trash with restore keys and Empty trash', async () => {
    // user.db knows no trash row for the item (put in .picmover/trash by hand and
    // recorded by a rebuild): the counts follow the catalog, which the page lists.
    server.userdb.trashed = 0;
    await start('#/l/peter/trash');
    expect(document.querySelector('.thead')?.textContent).toContain('1 item');
    expect(navTrash()).toBe('Trash1');
    expect(button(document, 'Empty trash')?.disabled).toBe(false);
    expect(tiles()).toHaveLength(1);
    expect(tile(9).textContent).toContain('deleted yesterday');
    tile(9).click();
    await settle();
    const lb = document.querySelector('.lightbox')!;
    expect(lb.textContent).toContain('In the trash');
    expect(lb.querySelector('input[aria-label="Add tag (T)"]')).toBeNull();
    key('Delete'); // not in the trash
    key('r');
    await settle();
    expect(server.posts('l/peter/trash')).toHaveLength(0);
    expect(server.posts('l/peter/trash/restore')[0]?.body).toEqual({ hashes: [hashOf(9)] });
    for (let i = 0; i < 20 && location.hash.includes('/view/'); i++) await vi.advanceTimersByTimeAsync(10);
    await settle();
    expect(location.hash).toBe('#/l/peter/trash');
    expect(tiles()).toHaveLength(0);
    expect(document.body.textContent).toContain('The trash is empty');

    // Put two items in the trash and empty it.
    server.items[0]!.deletedAt = '2026-10-10T11:00:00';
    server.items[1]!.deletedAt = '2026-10-10T11:00:00';
    await go('#/l/peter/');
    await go('#/l/peter/trash');
    click(tile(1), { ctrlKey: true });
    await settle();
    expect(document.querySelector('.selbar')?.textContent).toContain('1 selected');
    button(document, 'Empty trash')!.click();
    await settle();
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!;
    expect(dialog.textContent).toContain('Type 2 to confirm');
    const del = button(dialog, 'Delete permanently')!;
    expect(del.disabled).toBe(true);
    const input = dialog.querySelector<HTMLInputElement>('input')!;
    input.value = '3';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();
    expect(del.disabled).toBe(true);
    input.value = '2';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();
    expect(del.disabled).toBe(false);
    dialog.querySelector('form')!.requestSubmit();
    await settle();
    expect(server.posts('l/peter/trash/delete')[0]?.body).toEqual({ all: true, confirm: true });
    expect(tiles()).toHaveLength(0);
    expect(toastText()).toContain('Deleted 2 files permanently');
    // The selected item is gone with the rest: no bar offering Restore of nothing.
    expect(document.querySelector('.selbar')).toBeNull();
  });

  it('lists albums and removes items from one', async () => {
    await start('#/l/peter/albums');
    const card = document.querySelector<HTMLAnchorElement>('.albumcard a.main')!;
    expect(card.getAttribute('href')).toBe('#/l/peter/albums/2');
    expect(card.textContent).toContain('Summer');
    expect(card.textContent).toContain('2 items');
    expect(card.querySelector('img')?.getAttribute('src')).toBe(`/api/l/peter/thumb/${hashOf(1)}`);

    // Rename through the card menu.
    button(document, '⋯')!.click();
    await settle();
    button(document, 'Rename')!.click();
    await settle();
    const input = document.querySelector<HTMLInputElement>('[role="dialog"] input')!;
    expect(input.value).toBe('Summer');
    input.value = 'Summer 24';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    document.querySelector<HTMLFormElement>('[role="dialog"] form')!.requestSubmit();
    await settle();
    expect(server.posts('l/peter/albums/2')[0]?.body).toEqual({ name: 'Summer 24' });
    expect(document.querySelector('.albumcard')?.textContent).toContain('Summer 24');

    await go('#/l/peter/albums/2');
    expect(document.querySelector('.ahead h1')?.textContent).toBe('Summer 24');
    expect(server.calls.some((c) => c.path.startsWith('/api/l/peter/timeline?album=2'))).toBe(true);
    expect(tiles()).toHaveLength(2);
    click(tile(2), { ctrlKey: true });
    await settle();
    button(document, 'Set cover from selection')!.click();
    await settle();
    expect(server.posts('l/peter/albums/2')[1]?.body).toEqual({ cover: hashOf(2) });
    button(document.querySelector('.selbar')!, 'Remove from album')!.click();
    await settle();
    expect(tiles().map((t) => t.getAttribute('href'))).toEqual(['#/l/peter/view/1']);
    expect(document.querySelector('.ahead')?.textContent).toContain('1 item');
  });

  it('reviews near-duplicates', async () => {
    await start('#/l/peter/similar');
    expect(document.querySelector('.run')?.textContent?.replace(/\s+/g, ' ')).toMatch(/Last run last year \(.*\) at distance 6, 1 group /);
    const group = document.querySelector<HTMLElement>('.group .items')!;
    const radios = [...group.querySelectorAll<HTMLInputElement>('input[type="radio"]')];
    expect(radios.map((r) => r.checked)).toEqual([false, true]); // the larger file is kept
    group.focus();
    key('ArrowLeft', group);
    await settle();
    expect(radios.map((r) => r.checked)).toEqual([true, false]);
    // A click on a thumbnail opens the viewer over the page.
    click(group.querySelector('a.thumb')!);
    await settle();
    expect(location.hash).toBe('#/l/peter/view/7');
    expect(document.querySelector('.lightbox .nav.next')).not.toBeNull();
    key('Escape');
    for (let i = 0; i < 20 && location.hash.includes('/view/'); i++) await vi.advanceTimersByTimeAsync(10);
    await settle();
    expect(location.hash).toBe('#/l/peter/similar');
    // Enter on a thumbnail link is the link's (it bubbles to the row): it must not trash.
    key('Enter', document.querySelector('.group a.thumb')!);
    await settle();
    expect(server.posts('l/peter/trash')).toHaveLength(0);
    expect(document.querySelectorAll('.group').length).toBe(1);
    key('Enter', document.querySelector('.group .items')!);
    await settle();
    expect(server.posts('l/peter/trash')[0]?.body).toEqual({ hashes: [hashOf(8)] });
    expect(document.querySelectorAll('.group').length).toBe(0);
    expect(document.body.textContent).toContain('No near-duplicates left');

    // Run at another distance.
    const select = document.querySelector<HTMLSelectElement>('.dist select')!;
    select.value = '4';
    select.dispatchEvent(new Event('change', { bubbles: true }));
    button(document, 'Run')!.click();
    await settle();
    expect(server.posts('l/peter/jobs')[0]?.body).toEqual({ kind: 'similar', distance: 4 });
  });

  it('pages through duplicates with the server: handled groups make room, ignore by group', async () => {
    // 120 groups of two (ids 100..339), after the fixture's own group.
    const groups: string[][] = [];
    for (let i = 0; i < 120; i++) {
      const a = 100 + 2 * i;
      for (const id of [a, a + 1]) server.items.push({ id, hash: hashOf(id), type: 'image', takenAt: '2020-01-01T10:00:00', w: 100, h: 100, thumb: 'ok', tagIds: [] });
      groups.push([hashOf(a), hashOf(a + 1)]);
    }
    server.similar.groups = groups;
    await start('#/l/peter/similar');
    const pager = () => document.querySelector('.pager')?.textContent?.replace(/\s+/g, ' ').trim();
    const firstIds = () => [...document.querySelectorAll('.group')].map((g) => g.querySelector('a.thumb')?.getAttribute('href'));
    expect(document.querySelectorAll('.group').length).toBe(50);
    expect(pager()).toContain('Groups 1–50 of 120');

    // Trash the others of group 1: the page is fetched again and group 51 moves up.
    key('Enter', document.querySelector('.group .items')!);
    await settle();
    expect(server.posts('l/peter/trash')[0]?.body).toEqual({ hashes: [hashOf(100)] }); // the larger file (101) is kept
    expect(document.querySelectorAll('.group').length).toBe(50);
    expect(firstIds()[0]).toBe('#/l/peter/view/102');
    expect(firstIds()[49]).toBe(`#/l/peter/view/${100 + 2 * 50}`);
    expect(pager()).toContain('Groups 1–50 of 119');

    // Ignore sends one hash of the group; the server ignores the whole group.
    button(document.querySelector('.group')!, 'Ignore group')!.click();
    await settle();
    expect(server.posts('l/peter/similar/ignore')).toHaveLength(1);
    expect(server.posts('l/peter/similar/ignore')[0]?.body).toEqual({ group: hashOf(102) });
    expect(firstIds()[0]).toBe('#/l/peter/view/104');
    expect(document.querySelectorAll('.group').length).toBe(50);
    expect(pager()).toContain('Groups 1–50 of 118');

    // The last page empties (its items trashed elsewhere): no "nothing left" while groups
    // remain; the page moves back to the last one that has groups.
    button(document.querySelector('.pager')!, 'Next')!.click();
    await settle();
    button(document.querySelector('.pager')!, 'Next')!.click();
    await settle();
    expect(pager()).toContain('Groups 101–118 of 118');
    const hashes = [...document.querySelectorAll('.group a.thumb')].map((a) => hashOf(Number(a.getAttribute('href')!.split('/').pop())));
    for (const x of server.items) if (hashes.includes(x.hash)) x.deletedAt = '2026-10-10T11:00:00';
    const { emitChange } = await import('./lib/changes');
    emitChange({ kind: 'trashed', hashes });
    await settle();
    expect(document.body.textContent).not.toContain('No near-duplicates left');
    expect(pager()).toContain('Groups 51–100 of 100');
    expect(document.querySelectorAll('.group').length).toBe(50);
  });

  it('keeps an existing day bound when a year limit changes, and summarises in days', async () => {
    await start('#/l/peter/?to=2024-07-16');
    const dates = [...document.querySelectorAll<HTMLButtonElement>('.datefilter > button')][0]!;
    expect(dates.textContent).toContain('– 15/07/2024');
    dates.click();
    await settle();
    button(document.querySelector('.datefilter .panel')!, 'Every year')!.click();
    await settle();
    const toYear = document.querySelector<HTMLSelectElement>('select[aria-label="To year"]')!;
    // The day bound is shown as such, not as "Any".
    expect(toYear.value).toBe('day');
    expect(toYear.selectedOptions[0]?.textContent).toBe('15/07/2024');
    const fromYear = document.querySelector<HTMLSelectElement>('select[aria-label="From year"]')!;
    fromYear.value = '2024';
    fromYear.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();
    expect(location.hash).toBe('#/l/peter/?from=2024-01-01&to=2024-07-16');
    expect(dates.textContent).toContain('01/01/2024 – 15/07/2024');
  });

  it('picks months every year in the month grid', async () => {
    await start();
    const dates = [...document.querySelectorAll<HTMLButtonElement>('.datefilter > button')][0]!;
    dates.click();
    await settle();
    const panel = () => document.querySelector('.datefilter .panel')!;
    button(panel(), 'Every year')!.click();
    await settle();
    const month = (name: string) => panel().querySelector<HTMLButtonElement>(`.mgrid button[aria-label="${name}"]`)!;
    const timelineMonths = () =>
      server.calls.filter((c) => c.path.startsWith('/api/l/peter/timeline')).map((c) => new URLSearchParams(c.path.split('?')[1] ?? '').get('months'));
    for (const m of ['January', 'February', 'March']) {
      month(m).click();
      await settle();
    }
    expect(location.hash).toBe('#/l/peter/?months=1,2,3');
    expect(timelineMonths()).toContain('1,2,3');
    expect(dates.textContent).toContain('Jan–Mar every year');
    expect(month('February').getAttribute('aria-pressed')).toBe('true');
    expect(month('April').getAttribute('aria-pressed')).toBe('false');
    // Toggling off, then a day preset: the preset clears the months.
    month('February').click();
    await settle();
    expect(location.hash).toBe('#/l/peter/?months=1,3');
    expect(dates.textContent).toContain('Jan, Mar every year');
    button(panel(), 'Christmas')!.click();
    await settle();
    expect(location.hash).toBe('#/l/peter/?recur=12-24..12-26');
    expect(month('January').getAttribute('aria-pressed')).toBe('false');
    // Picking a month clears the day range; Shift-click selects the run from the last clicked month.
    month('July').click();
    await settle();
    expect(location.hash).toBe('#/l/peter/?months=7');
    month('October').dispatchEvent(new MouseEvent('click', { bubbles: true, shiftKey: true }));
    await settle();
    expect(location.hash).toBe('#/l/peter/?months=7,8,9,10');
    expect(dates.textContent).toContain('Jul–Oct every year');
    // The fake library is July and June 2024: July only.
    expect(timelineMonths()).toContain('7,8,9,10');
    // Keyboard: arrows move the focus, Space/Enter toggle (native buttons).
    month('October').focus();
    key('ArrowRight', month('October'));
    expect(document.activeElement).toBe(month('November'));
    // All, then None.
    button(panel(), 'All')!.click();
    await settle();
    expect(location.hash).toBe('#/l/peter/?months=1,2,3,4,5,6,7,8,9,10,11,12');
    expect(dates.textContent).toContain('Jan–Dec every year');
    button(panel(), 'None')!.click();
    await settle();
    expect(location.hash).toBe('#/l/peter/');
    expect(dates.textContent).not.toContain('every year');
  });

  it('puts the new filters in the URL and the requests', async () => {
    await start();
    // Dates → Every year → Christmas preset.
    const dates = [...document.querySelectorAll<HTMLButtonElement>('.datefilter > button')][0]!;
    dates.click();
    await settle();
    button(document.querySelector('.datefilter .panel')!, 'Every year')!.click();
    await settle();
    button(document.querySelector('.datefilter .panel')!, 'Christmas')!.click();
    await settle();
    expect(location.hash).toBe('#/l/peter/?recur=12-24..12-26');
    expect(server.calls.some((c) => c.path === '/api/l/peter/timeline?recur=12-24..12-26')).toBe(true);
    expect(dates.textContent).toContain('24 Dec – 26 Dec every year');

    // Range mode: an inclusive last day becomes an exclusive `to`. Typed
    // dd/mm/yyyy days apply on Enter; calendar clicks apply at once.
    button(document.querySelector('.datefilter .panel')!, 'Range')!.click();
    await settle();
    const typeDay = async (label: string, text: string) => {
      const input = document.querySelector<HTMLInputElement>(`.datefilter input[aria-label="${label}"]`)!;
      input.value = text;
      input.dispatchEvent(new Event('input', { bubbles: true }));
      key('Enter', input);
      await settle();
    };
    await typeDay('End day', '15/07/2024');
    expect(location.hash).toBe('#/l/peter/?to=2024-07-16');
    await typeDay('Start day', '01/07/2024');
    expect(location.hash).toBe('#/l/peter/?from=2024-07-01&to=2024-07-16');
    expect(dates.textContent).toContain('01/07/2024 – 15/07/2024');
    // The calendar shows July 2024; a click starts a new range, the next ends it.
    const day = (d: string) => document.querySelector<HTMLElement>(`.datefilter [role="gridcell"][data-day="${d}"][data-in="1"]`)!;
    expect(day('2024-07-10').getAttribute('aria-selected')).toBe('true');
    day('2024-07-03').click();
    await settle();
    expect(location.hash).toBe('#/l/peter/?from=2024-07-03&to=2024-07-04');
    day('2024-07-10').click();
    await settle();
    expect(location.hash).toBe('#/l/peter/?from=2024-07-03&to=2024-07-11');
    expect(dates.textContent).toContain('03/07/2024 – 10/07/2024');
    key('Escape', document.querySelector('.datefilter .panel')!);
    await settle();
    expect(document.querySelector('.datefilter .panel')).toBeNull();

    // Rating, tag and album filters.
    const rating = document.querySelector<HTMLSelectElement>('select[aria-label="Minimum rating"]')!;
    rating.value = '2';
    rating.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();
    const tagInput = document.querySelector<HTMLInputElement>('input[aria-label="Filter by tag"]')!;
    tagInput.focus();
    tagInput.value = 'beach';
    tagInput.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();
    key('Enter', tagInput);
    await settle();
    const album = document.querySelector<HTMLSelectElement>('select[aria-label="Album"]')!;
    album.value = '2';
    album.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();
    expect(location.hash).toBe('#/l/peter/?from=2024-07-03&to=2024-07-11&rating=2&tag=1&album=2');
    expect(server.calls.some((c) => c.path === '/api/l/peter/timeline?from=2024-07-03&to=2024-07-11&rating=2&tag=1&album=2')).toBe(true);
    expect(document.querySelector('.filters .chip')?.textContent).toContain('beach');
    button(document.querySelector('.filters')!, 'Clear')!.click();
    await settle();
    expect(location.hash).toBe('#/l/peter/');
  });
});
