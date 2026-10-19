// @vitest-environment jsdom
//
// The safety tooltips (issue #23) through the whole app against the fake
// server: Trash, Delete permanently, Empty trash, Move to… and the import's
// Copy and Move say what they do to the files and whether it can be undone.
// The wording is checked here, so a change to it is a deliberate one.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fakeServer, go, settle, type FakeServer } from './testing/fakes';
import type { Lib } from './lib/api';

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

const kids: Lib = {
  id: 'kids',
  name: 'kids',
  path: '/media/kids',
  state: 'closed',
  summary: { total: 2, images: 2, raw: 0, videos: 0, withGps: 0, deleted: 0, bytes: 1, earliest: null, latest: null },
};

const text = (el: Element | null | undefined) => (el?.textContent ?? '').replace(/\s+/g, ' ').trim();
const click = (el: Element) => el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
const button = (root: ParentNode | null, label: string) => [...(root ?? document).querySelectorAll<HTMLButtonElement>('button')].find((b) => text(b) === label);
const bar = () => document.querySelector<HTMLElement>('.selbar');
const check = (id: number) => document.querySelector<HTMLInputElement>(`a.tile[href="#/l/peter/view/${id}"] + input.check`)!;
const title = (el: Element | null | undefined) => el?.getAttribute('title') ?? '';

describe('safety tooltips', () => {
  beforeEach(() => {
    vi.resetModules();
    server = fakeServer('workspace');
    server.libs.push({ ...kids });
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

  it('says that Trash can be restored and Move deletes nothing', async () => {
    await start('#/l/peter/');
    click(check(1));
    await settle();
    expect(title(button(bar(), 'Trash'))).toBe('Move to the trash. It can be restored from the Trash page until the trash is emptied.');
    expect(title(button(bar(), 'Move to…'))).toBe(
      'Move to another library, with ratings, tags and albums. Nothing is deleted: when the target is on another drive, the originals go to this library’s trash.',
    );
    expect(title(button(bar(), 'Album'))).toBe('Add the selected items to an album, or to a new one.');
  });

  it('says the same in the viewer, with the keys', async () => {
    await start('#/l/peter/view/3');
    const lb = document.querySelector('.lightbox')!;
    expect(title(button(lb, 'Trash'))).toBe('Move to the trash. It can be restored from the Trash page until the trash is emptied. Key: Delete.');
    expect(title(button(lb.querySelector('aside.info'), 'Move to…'))).toMatch(/^Move to another library, .*Nothing is deleted.* Key: M\.$/);
    expect(title(lb.querySelector('dt[title]'))).toBe('Kept in the library’s user.db. The file itself is not changed.');
  });

  it('says that Delete permanently and Empty trash cannot be undone', async () => {
    await start('#/l/peter/trash');
    expect(title(button(document, 'Empty trash'))).toBe(
      'Delete every file in the trash from the disk. This cannot be undone; you type the number of files to confirm.',
    );
    click(check(9));
    await settle();
    expect(title(button(bar(), 'Delete permanently'))).toBe('Delete from the disk. This cannot be undone.');
    expect(title(button(bar(), 'Restore'))).toBe('Put back in the library where it was, with its rating, tags and albums.');

    // The viewer's own Delete permanently says the same.
    await go('#/l/peter/view/9');
    const info = document.querySelector('.lightbox aside.info');
    expect(title(button(info, 'Delete permanently'))).toBe('Delete from the disk. This cannot be undone.');
  });

  it('says whether an import changes or deletes the source', async () => {
    await start('#/l/peter/import');
    click([...document.querySelectorAll<HTMLButtonElement>('.entry')].find((b) => b.textContent?.includes('/'))!);
    await settle();
    click(button(document, 'Import from this folder')!);
    await settle();
    const copy = document.querySelector<HTMLInputElement>('input[name="mode"][value="copy"]')!.closest('label');
    const move = document.querySelector<HTMLInputElement>('input[name="mode"][value="move"]')!.closest('label');
    expect(title(copy)).toBe('Copy each file into the library. Nothing on the source (card, phone or folder) is changed or deleted.');
    expect(title(move)).toBe(
      'Delete each source file (on the card, phone or folder) once it is in the library. Duplicates, skipped files and zip archives stay on the source. The deletion cannot be undone.',
    );
    const submit = () => document.querySelector<HTMLButtonElement>('form.options button[type="submit"]');
    expect(title(submit())).toBe('Start the import. The source is not changed.');
    move!.querySelector('input')!.click();
    await settle();
    expect(title(submit())).toBe('Start the import. Each source file is deleted once it is in the library; this cannot be undone.');
  });
});
