// @vitest-environment jsdom
//
// Import history in the web UI (docs/design/imports.md "Web") against the
// fake server: the Imports page, the record's grid with Rename and Delete
// record, the import filter chip, the lightbox line and "Show imported".

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { backupJobJSON, fakeServer, go, hashOf, settle, ZERO_STATS, type FakeServer } from './testing/fakes';
import type { Job } from './lib/api';

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

const tiles = () => [...document.querySelectorAll<HTMLAnchorElement>('a.tile')].map((a) => a.getAttribute('href'));
const button = (root: ParentNode, label: string) => [...root.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === label);
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]');
const rows = () => [...document.querySelectorAll<HTMLAnchorElement>('ul[aria-label="Imports"] a.rec')];
const timelines = () => server.calls.filter((c) => c.path.startsWith('/api/l/peter/timeline')).map((c) => c.path);

async function typeName(value: string) {
  const input = dialog()!.querySelector<HTMLInputElement>('input')!;
  input.value = value;
  input.dispatchEvent(new Event('input', { bubbles: true }));
  dialog()!.querySelector('form')!.requestSubmit();
  await settle();
}

describe('import history', () => {
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

  it('lists the records newest first, next to Albums in the nav', async () => {
    await start('#/l/peter/imports');
    const nav = [...document.querySelectorAll('nav a')];
    const i = nav.findIndex((a) => a.textContent === 'Imports');
    expect(nav[i - 1]?.textContent).toBe('Albums');
    expect(nav[i]?.getAttribute('aria-current')).toBe('page');
    expect(nav[i]?.getAttribute('href')).toBe('#/l/peter/imports');
    // The page names the library it lists.
    expect(document.querySelector('.where')?.textContent).toBe('In /media/peter');

    const [a, b] = rows();
    expect(rows()).toHaveLength(2);
    expect(a!.getAttribute('href')).toBe('#/l/peter/imports/5');
    expect(a!.querySelector('.name')?.textContent).toBe('Phone dump');
    expect(a!.querySelector('.mode')?.textContent).toBe('Copy');
    expect(a!.querySelector('.outcome')).toBeNull();
    expect(a!.textContent).toContain('from Camera');
    expect(a!.textContent).toContain('3 new, 1 already there, 2 duplicates, 1 error');
    expect(a!.textContent).toContain('3.0 MiB');
    expect(a!.querySelector('time')?.textContent).toMatch(/ago$/); // under a week: relative
    // The cover is the newest item by capture time, through the usual thumb URL.
    expect(a!.querySelector('img')?.getAttribute('src')).toBe(`/api/l/peter/thumb/${hashOf(1)}`);

    // Unnamed: the source name; an old one shows its date; a cancelled one says so.
    expect(b!.getAttribute('href')).toBe('#/l/peter/imports/2');
    expect(b!.querySelector('.name')?.textContent).toBe('DCIM');
    expect(b!.querySelector('.mode')?.textContent).toBe('Move');
    expect(b!.querySelector('.outcome')?.textContent).toBe('Cancelled');
    expect(b!.querySelector('time')?.textContent).toContain('2026');
    expect(b!.querySelector('.src')?.textContent).toBe('/media/card/DCIM');
    expect(b!.textContent).toContain('2 new');
  });

  it('says so when there are no imports', async () => {
    server.imports.length = 0;
    await start('#/l/peter/imports');
    expect(document.querySelector('.empty h2')?.textContent).toBe('No imports yet');
  });

  it('shows one record in the grid and renames it', async () => {
    await start('#/l/peter/imports');
    expect(rows()[0]!.getAttribute('href')).toBe('#/l/peter/imports/5');
    await go('#/l/peter/imports/5');
    expect(document.querySelector('.ihead h1')?.textContent).toBe('Import: Phone dump');
    expect(document.querySelector('.ihead .facts')?.textContent).toContain('/mnt/phone/DCIM/Camera');
    expect(timelines().at(-1)).toBe('/api/l/peter/timeline?import=5');
    expect(tiles()).toEqual(['#/l/peter/view/1', '#/l/peter/view/2', '#/l/peter/view/3', '#/l/peter/view/4']);
    // The page implies the filter: no chip, and the nav stays on Imports.
    expect(document.querySelector('[aria-label^="Remove import filter"]')).toBeNull();
    expect(document.querySelector('nav a[aria-current="page"]')?.textContent).toBe('Imports');

    // Other filters narrow within the import, in the record's URL.
    const rating = document.querySelector<HTMLSelectElement>('select[aria-label="Minimum rating"]')!;
    rating.value = '3';
    rating.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();
    expect(location.hash).toBe('#/l/peter/imports/5?rating=3');
    expect(timelines().at(-1)).toBe('/api/l/peter/timeline?rating=3&import=5');
    await go('#/l/peter/imports/5');

    button(document.querySelector('.ihead')!, 'Rename')!.click();
    await settle();
    expect(dialog()!.querySelector('input')!.value).toBe('Phone dump');
    await typeName('  Pixel 8  ');
    expect(server.posts('l/peter/imports/5').map((c) => c.body)).toEqual([{ name: 'Pixel 8' }]);
    expect(dialog()).toBeNull();
    expect(document.querySelector('.ihead h1')?.textContent).toBe('Import: Pixel 8');

    // An empty name clears it: the title falls back to the source name.
    button(document.querySelector('.ihead')!, 'Rename')!.click();
    await settle();
    await typeName('   ');
    expect(server.posts('l/peter/imports/5')[1]?.body).toEqual({ name: '' });
    expect(document.querySelector('.ihead h1')?.textContent).toBe('Import: Camera');

    // Too long: refused before sending.
    button(document.querySelector('.ihead')!, 'Rename')!.click();
    await settle();
    await typeName('x'.repeat(201));
    expect(dialog()?.querySelector('[role="alert"]')?.textContent).toBe('At most 200 characters.');
    expect(server.posts('l/peter/imports/5')).toHaveLength(2);
    button(dialog()!, 'Cancel')!.click();
    await settle();

    await go('#/l/peter/imports');
    expect(rows()[0]?.querySelector('.name')?.textContent).toBe('Camera');
  });

  it('deletes the record only, after a confirm that says the files stay', async () => {
    await start('#/l/peter/imports/5');
    const before = server.items.length;
    button(document.querySelector('.ihead')!, 'Delete record')!.click();
    await settle();
    expect(dialog()!.textContent).toContain('Delete the record of “Phone dump”?');
    expect(dialog()!.textContent).toContain('The 4 photos and videos it brought in stay in the library; no file is touched.');
    // Cancel sends nothing.
    button(dialog()!, 'Cancel')!.click();
    await settle();
    expect(server.posts('l/peter/imports/5/delete')).toEqual([]);

    button(document.querySelector('.ihead')!, 'Delete record')!.click();
    await settle();
    dialog()!.querySelector('form')!.requestSubmit();
    await settle();
    expect(server.posts('l/peter/imports/5/delete').map((c) => c.body)).toEqual([{}]);
    expect(server.calls.filter((c) => c.method === 'POST' && /trash|delete/.test(c.path) && !c.path.includes('/imports/'))).toEqual([]);
    expect(server.items).toHaveLength(before);
    expect(location.hash).toBe('#/l/peter/imports');
    expect([...document.querySelectorAll('.toast .text')].map((t) => t.textContent)).toContain('Import record deleted');
    expect(rows().map((r) => r.getAttribute('href'))).toEqual(['#/l/peter/imports/2']);
  });

  it('shows the server refusing a name with control characters', async () => {
    await start('#/l/peter/imports/5');
    button(document.querySelector('.ihead')!, 'Rename')!.click();
    await settle();
    await typeName('a\u0007b');
    expect(server.posts('l/peter/imports/5').map((c) => c.body)).toEqual([{ name: 'a\u0007b' }]);
    expect(dialog()?.querySelector('[role="alert"]')?.textContent).toContain('control characters are not allowed');
    expect(document.querySelector('.ihead h1')?.textContent).toBe('Import: Phone dump');
  });

  it('shows a rebuild record without a grid of its own', async () => {
    server.imports.unshift({
      id: 9,
      name: '',
      startedAt: '2026-10-11T08:00:00Z',
      finishedAt: '2026-10-11T08:01:00Z',
      source: '/media/peter',
      sourceName: 'peter',
      mode: 'record',
      host: '',
      outcome: 'done',
      imported: 0,
      existing: 8,
      duplicates: 0,
      skipped: 0,
      errors: 0,
      bytes: 0,
      hashes: [],
    });
    await start('#/l/peter/imports');
    const a = rows()[0]!;
    expect(a.getAttribute('href')).toBe('#/l/peter/imports/9');
    expect(a.querySelector('.mode')?.textContent).toBe('Rebuild');
    expect(a.textContent).toContain('8 files recorded');
    expect(a.querySelector('img')).toBeNull();

    await go('#/l/peter/imports/9');
    expect(document.querySelector('.ihead h1')?.textContent).toBe('Rebuild: peter');
    expect(document.body.textContent).toContain('keeps no list of pictures of its own');
    const whole = [...document.querySelectorAll<HTMLAnchorElement>('a')].find((x) => x.textContent === 'Show the whole library');
    expect(whole?.getAttribute('href')).toBe('#/l/peter/');
    expect(timelines().filter((p) => p.includes('import=9'))).toEqual([]);
    expect(tiles()).toEqual([]);
    button(document.querySelector('.ihead')!, 'Delete record')!.click();
    await settle();
    expect(dialog()!.textContent).toContain("The library's photos and videos stay as they are; no file is touched.");
  });

  it('answers like the server (the fake)', async () => {
    await start('#/l/peter/imports');
    const status = async (p: string) => (await fetch(p)).status;
    expect(await status('/api/l/peter/timeline?import=abc')).toBe(400);
    expect(await status('/api/l/peter/timeline?import=0')).toBe(400);
    expect(await status('/api/l/peter/timeline?import=99')).toBe(404);
    expect(await status('/api/l/peter/imports/abc')).toBe(404);
    expect(await status('/api/l/peter/imports/99')).toBe(404);
    // The list carries the stored summary; one record the live one.
    server.items.find((x) => x.hash === hashOf(1))!.deletedAt = '2026-10-11T00:00:00';
    const list = (await (await fetch('/api/l/peter/imports')).json()) as { imports: { id: number; items: number; cover: string }[] };
    expect(list.imports[0]).toMatchObject({ id: 5, items: 4, cover: hashOf(1) });
    const one = (await (await fetch('/api/l/peter/imports/5')).json()) as { items: number; cover: string };
    expect(one).toMatchObject({ items: 3, cover: hashOf(2) });
    const d = (await (await fetch('/api/l/peter/media/2')).json()) as { imports: unknown[] };
    expect(d.imports).toEqual([{ id: 5, name: 'Phone dump', startedAt: '2026-10-10T09:00:00Z', mode: 'copy', sourceName: 'Camera' }]);
  });

  it('shows a missing record as such', async () => {
    await start('#/l/peter/imports/99');
    expect(document.body.textContent).toContain('There is no such import record in this library.');
    expect(document.querySelector('.back')?.getAttribute('href')).toBe('#/l/peter/imports');
  });

  it('filters the grid by an import with a named chip', async () => {
    await start('#/l/peter/?import=2&rating=0');
    expect(timelines().at(-1)).toBe('/api/l/peter/timeline?import=2');
    expect(tiles()).toEqual(['#/l/peter/view/7', '#/l/peter/view/8']);
    const remove = document.querySelector<HTMLButtonElement>('[aria-label="Remove import filter DCIM"]')!;
    expect(remove.parentElement?.textContent).toContain('Import: DCIM');
    remove.click();
    await settle();
    expect(location.hash).toBe('#/l/peter/');
    expect(tiles()).toHaveLength(8);
  });

  it('links the lightbox to the imports that brought an item in', async () => {
    await start('#/l/peter/view/2');
    const link = [...document.querySelectorAll<HTMLAnchorElement>('.imports a')];
    expect(link).toHaveLength(1);
    expect(link[0]!.getAttribute('href')).toBe('#/l/peter/imports/5');
    expect(link[0]!.textContent).toMatch(/^Imported .+ from Camera · Phone dump$/);
  });

  it('points "Show imported" at the record when the job has one', async () => {
    const job = (id: number, p: Partial<Job>): Job =>
      backupJobJSON({ id, kind: 'import', target: '', state: 'done', source: '/mnt/phone', stats: { ...ZERO_STATS, imported: 3 }, ...p });
    server.backup.jobs.push(job(7, { importId: 5, sessionId: 9 }), job(6, { sessionId: 8 }), job(5, { importId: null, sessionId: null }));
    await start('#/l/peter/library');
    const links = [...document.querySelectorAll<HTMLAnchorElement>('a')].filter((a) => a.textContent === 'Show imported');
    expect(links.map((a) => a.getAttribute('href'))).toEqual(['#/l/peter/?import=5', '#/l/peter/?session=8']);
  });
});
