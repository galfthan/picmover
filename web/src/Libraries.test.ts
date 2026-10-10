// @vitest-environment jsdom
//
// The workspace start page against a fake server: cards in every state,
// Retry for a busy library, Rebuild and Record files in place, the New
// library dialog (focus trap, Escape, validation, create and open), the
// gate shown while a library opens, and the switcher menu.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fakeServer, FakeEventSource, go, settle, type FakeServer } from './testing/fakes';

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

const card = (name: string) => [...document.querySelectorAll<HTMLElement>('.libcard')].find((c) => c.querySelector('.name')?.textContent === name)!;
const button = (root: ParentNode, label: string) => [...root.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === label);

describe('Libraries page', () => {
  beforeEach(() => {
    vi.resetModules();
    server = fakeServer('workspace');
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

  it('lists libraries and folders with their states and actions', async () => {
    await start();
    expect(location.hash).toBe('#/');
    expect(document.querySelector('h1')?.textContent).toBe('Libraries');
    expect(document.querySelector('.root')?.textContent).toContain('/media');

    const peter = card('peter');
    expect(peter.querySelector('a.main')?.getAttribute('href')).toBe('#/l/peter/');
    expect(peter.textContent).toContain('/media/peter');
    expect(peter.textContent).toContain('8 photos · 0 videos · 2024');
    // A closed library's thumbnails cannot be served yet: a placeholder, not an <img>.
    expect(peter.querySelector('img')).toBeNull();

    const penny = card('penny');
    expect(penny.querySelector('.badge')?.textContent).toBe('In use');
    expect(penny.textContent).toMatch(/in use by picmover import \(pid 812\) since/);
    button(penny, 'Retry')!.click();
    await settle();
    expect(server.posts('libraries/penny/open')).toHaveLength(1);
    expect(card('penny').querySelector('.badge')?.textContent).toBe('In use');

    const broken = card('broken');
    expect(broken.querySelector('.badge')?.textContent).toBe('Needs rebuild');
    expect(broken.textContent).toContain('media.db needs a rebuild');

    const scans = card('scans');
    expect(scans.textContent).toContain('Not a library yet');
    button(scans, 'Record files in place')!.click();
    await settle();
    expect(server.posts('libraries/scans/jobs')[0]?.body).toEqual({ kind: 'rebuild' });
    expect(FakeEventSource.instances.at(-1)?.url).toBe('/api/jobs/1/events');
    expect(card('scans').querySelector('.progress')).not.toBeNull();
    // Other libraries cannot start a rebuild meanwhile.
    expect(button(card('broken'), 'Rebuild')?.disabled).toBe(true);

    // The rebuild finishes: the folder is a library now and opens.
    const done = { id: 1, kind: 'rebuild', state: 'done', library: 'scans', startedAt: '2026-10-10T12:00:00Z', finishedAt: '2026-10-10T12:01:00Z', stats: {}, error: null, exitCode: 0 };
    server.libs.push({ id: 'scans', name: 'scans', path: '/media/scans', state: 'closed' });
    FakeEventSource.instances.at(-1)!.emit('done', done);
    await settle();
    expect(location.hash).toBe('#/l/scans/');
  });

  it('creates a library from the dialog', async () => {
    await start();
    const opener = button(document, 'New library')!;
    opener.focus();
    opener.click();
    await settle();
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!;
    expect(dialog.getAttribute('aria-modal')).toBe('true');
    const input = dialog.querySelector<HTMLInputElement>('input')!;
    expect(document.activeElement).toBe(input);

    // Tab from the last control wraps to the first, Shift+Tab back.
    input.value = 'x';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();
    const create = button(dialog, 'Create')!;
    create.focus();
    create.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }));
    expect(document.activeElement).toBe(input);
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true }));
    expect(document.activeElement).toBe(create);

    // Escape closes and gives focus back.
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
    await settle();
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(document.activeElement).toBe(opener);

    // Validation, then a real create that opens the new library.
    opener.click();
    await settle();
    const input2 = document.querySelector<HTMLInputElement>('[role="dialog"] input')!;
    input2.value = 'CON';
    input2.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();
    expect(document.querySelector('[role="dialog"] [role="alert"]')?.textContent).toContain('reserved');
    expect(button(document.querySelector('[role="dialog"]')!, 'Create')?.disabled).toBe(true);
    input2.value = 'penny2';
    input2.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain('/media/penny2');
    document.querySelector<HTMLFormElement>('[role="dialog"] form')!.requestSubmit();
    await settle();
    expect(server.posts('libraries')[0]?.body).toEqual({ name: 'penny2' });
    expect(location.hash).toBe('#/l/penny2/');
    expect(server.posts('libraries/penny2/open')).toHaveLength(1);
  });

  it('opens a closed library, shows gates for the others, and switches', async () => {
    await start('#/l/peter/');
    expect(server.posts('libraries/peter/open')).toHaveLength(1);
    expect(document.querySelectorAll('a.tile').length).toBe(8);
    expect(document.querySelector('.libchip .name')?.textContent).toBe('peter');
    expect(document.querySelector('.libchip .root')?.textContent).toBe('/media/peter');
    expect(document.title).toBe('peter — picmover');
    // Trash count in the nav.
    expect([...document.querySelectorAll('nav a')].find((a) => a.textContent?.startsWith('Trash'))?.textContent).toBe('Trash1');

    document.querySelector<HTMLButtonElement>('.libchip')!.click();
    await settle();
    const menu = document.querySelector('[role="menu"]')!;
    const entries = [...menu.querySelectorAll('[role="menuitem"]')].map((a) => [a.textContent?.trim().replace(/\s+/g, ' '), a.getAttribute('href')]);
    expect(entries).toEqual([
      ['Library details', '#/l/peter/library'],
      ['penny 3', '#/l/penny/'],
      ['broken error', '#/l/broken/'],
      ['All libraries', '#/'],
    ]);

    await go('#/l/penny/');
    expect(document.body.textContent).toMatch(/This library is in use by picmover import \(pid 812\)/);
    expect(button(document, 'Retry')).toBeDefined();
    expect(document.querySelectorAll('nav a').length).toBe(0);

    await go('#/l/broken/');
    expect(document.body.textContent).toContain('The catalog cannot be used: media.db needs a rebuild');
    button(document, 'Rebuild')!.click();
    await settle();
    expect(server.posts('libraries/broken/jobs')).toHaveLength(1);
    expect(document.body.textContent).toContain('Rebuilding the catalog');

    await go('#/l/nothing/');
    expect(document.body.textContent).toContain('No library “nothing”');
  });
});
