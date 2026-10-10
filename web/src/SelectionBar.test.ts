// @vitest-environment jsdom
//
// The selection bar on its own, in each mode, against the fake server:
// what it shows, the requests its actions send (chunked), and how the
// selection and the toasts follow.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fakeServer, hashOf, settle, type FakeServer } from './testing/fakes';

let server: FakeServer;
let cleanup: (() => void) | null = null;
const errors: unknown[] = [];

type Mods = {
  selection: (typeof import('./lib/selection.svelte'))['selection'];
  toasts: (typeof import('./lib/toast.svelte'))['toasts'];
};

async function mountBar(mode: 'grid' | 'album' | 'trash', hashes: string[], extra: { albumId?: number; currentRating?: number } = {}): Promise<Mods> {
  const svelte = await import('svelte');
  const { selection } = await import('./lib/selection.svelte');
  const { library } = await import('./lib/library.svelte');
  const { toasts } = await import('./lib/toast.svelte');
  const { libApi } = await import('./lib/api');
  const SelectionBar = (await import('./components/SelectionBar.svelte')).default;
  library.select('peter');
  await settle();
  selection.add(hashes);
  const target = document.createElement('div');
  document.body.appendChild(target);
  const c = svelte.mount(SelectionBar, { target, props: { api: libApi('peter'), mode, ...extra } });
  cleanup = () => void svelte.unmount(c);
  await settle();
  return { selection, toasts };
}

const bar = () => document.querySelector<HTMLElement>('.selbar');
const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>('.selbar button')].find((b) => b.textContent?.trim() === label);

async function type(input: HTMLInputElement, text: string, key = 'Enter') {
  input.focus();
  input.value = text;
  input.dispatchEvent(new Event('input', { bubbles: true }));
  await settle();
  input.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }));
  await settle();
}

describe('SelectionBar', () => {
  beforeEach(() => {
    vi.resetModules();
    server = fakeServer('single');
    server.install();
    errors.length = 0;
    vi.spyOn(console, 'error').mockImplementation((...a: unknown[]) => void errors.push(a));
    vi.spyOn(console, 'warn').mockImplementation((...a: unknown[]) => void errors.push(a));
  });

  afterEach(() => {
    cleanup?.();
    cleanup = null;
    document.body.innerHTML = '';
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    expect(errors).toEqual([]);
  });

  it('stays hidden without a selection', async () => {
    await mountBar('grid', []);
    expect(bar()).toBeNull();
  });

  it('rates, tags and adds to albums in grid mode', async () => {
    const sel = [hashOf(1), hashOf(2)];
    const { toasts } = await mountBar('grid', sel, { currentRating: 4 });
    expect(bar()?.textContent).toContain('2 selected');
    expect(button('Remove from album')).toBeUndefined();
    expect(button('Restore')).toBeUndefined();

    // Clicking the current value clears it; another star sets it.
    document.querySelector<HTMLButtonElement>('.selbar [aria-label^="Clear rating"]')!.click();
    await settle();
    document.querySelector<HTMLButtonElement>('.selbar [aria-label="Rate 2 stars"]')!.click();
    await settle();
    expect(server.posts('l/peter/ratings').map((c) => c.body)).toEqual([
      { hashes: sel, rating: 0 },
      { hashes: sel, rating: 2 },
    ]);
    expect(toasts.list.at(-1)?.text).toBe('Rated 2 items 2 ★');

    // Tag: autocomplete picks the existing tag, Enter on a new name creates it.
    const tagInput = document.querySelector<HTMLInputElement>('.selbar input[aria-label="Tag selected"]')!;
    tagInput.focus();
    tagInput.value = 'bea';
    tagInput.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();
    const option = document.querySelector<HTMLElement>('.selbar [role="option"]');
    expect(option?.textContent).toContain('beach');
    expect(tagInput.getAttribute('aria-expanded')).toBe('true');
    tagInput.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
    await settle();
    await type(tagInput, 'sunset');
    expect(server.posts('l/peter/media/tags').map((c) => [c.body?.add, c.body?.create])).toEqual([
      [[1], []],
      [[], ['sunset']],
    ]);
    expect(toasts.list.at(-1)?.text).toBe('Tagged 2 items “sunset”');

    // Album: a new one, then an existing one.
    button('Album')!.click();
    await settle();
    const albumInput = document.querySelector<HTMLInputElement>('.selbar .pop input')!;
    expect(document.activeElement).toBe(albumInput);
    expect(document.querySelector('.selbar .pop [role="option"]')?.textContent).toContain('Summer');
    await type(albumInput, 'Trip');
    expect(server.posts('l/peter/albums')[0]?.body).toEqual({ name: 'Trip', hashes: sel });
    expect(document.querySelector('.selbar .pop')).toBeNull();
    button('Album')!.click();
    await settle();
    await type(document.querySelector<HTMLInputElement>('.selbar .pop input')!, 'Summer');
    expect(server.posts('l/peter/albums/2/items')[0]?.body).toEqual({ add: sel, remove: [] });
    expect(toasts.list.at(-1)?.text).toBe('Added 2 items to “Summer”');
  });

  it('trashes with Undo and leaves the selection', async () => {
    const sel = [hashOf(1), hashOf(9)]; // 9 is already in the trash
    const { selection, toasts } = await mountBar('grid', sel);
    button('Trash')!.click();
    await settle();
    expect(server.posts('l/peter/trash')[0]?.body).toEqual({ hashes: sel });
    // The one that failed stays selected and the bar says why.
    expect(selection.list()).toEqual([hashOf(9)]);
    expect(bar()?.querySelector('[role="alert"]')?.textContent).toBe('1 item could not be moved to the trash: already in the trash');
    const t = toasts.list.at(-1)!;
    expect(t.text).toBe('Moved 1 item to the trash');
    await t.action!.run();
    await settle();
    expect(server.posts('l/peter/trash/restore')[0]?.body).toEqual({ hashes: [hashOf(1)] });
    button('Clear')!.click();
    await settle();
    expect(bar()).toBeNull();
  });

  it('sends large selections in chunks of 1000', async () => {
    const many = Array.from({ length: 2500 }, (_, i) => hashOf(1000 + i));
    await mountBar('grid', many);
    expect(bar()?.textContent).toContain('2,500 selected');
    document.querySelector<HTMLButtonElement>('.selbar [aria-label="Rate 5 stars"]')!.click();
    await settle();
    expect(server.posts('l/peter/ratings').map((c) => (c.body?.hashes as unknown[]).length)).toEqual([1000, 1000, 500]);
  });

  it('removes from the album in album mode', async () => {
    const { selection } = await mountBar('album', [hashOf(1)], { albumId: 2 });
    button('Remove from album')!.click();
    await settle();
    expect(server.posts('l/peter/albums/2/items')[0]?.body).toEqual({ add: [], remove: [hashOf(1)] });
    expect(selection.size).toBe(0);
  });

  it('restores and deletes permanently in trash mode', async () => {
    const { selection } = await mountBar('trash', [hashOf(9)]);
    expect(button('Trash')).toBeUndefined();
    expect(document.querySelector('.selbar input[aria-label="Tag selected"]')).toBeNull();
    button('Delete permanently')!.click();
    await settle();
    expect(bar()?.textContent).toContain('Delete 1 item permanently? This cannot be undone.');
    expect(server.posts('l/peter/trash/delete')).toHaveLength(0);
    button('Delete')!.click();
    await settle();
    expect(server.posts('l/peter/trash/delete')[0]?.body).toEqual({ hashes: [hashOf(9)], confirm: true });
    expect(selection.size).toBe(0);

    selection.add([hashOf(1)]);
    await settle();
    button('Restore')!.click();
    await settle();
    expect(server.posts('l/peter/trash/restore')[0]?.body).toEqual({ hashes: [hashOf(1)] });
  });
});
