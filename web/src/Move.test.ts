// @vitest-environment jsdom
//
// Moving photos to another library (docs/design/library-move.md
// "Frontend") through the whole app against the fake server: where "Move
// to…" is offered, the target list, the plan's states (same drive, across
// drives, not enough space, refused, duplicates, interrupted with Finish
// moving), the run with progress, the result with "Open in", the grid,
// selection and counts afterwards, jobs one after another for more than
// 2000 hashes, the lightbox's `m`, the pill and the interrupted-move
// banner.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fakeServer, FakeEventSource, go, hashOf, settle, type FakeServer } from './testing/fakes';
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
const click = (el: Element, init: MouseEventInit = {}) => el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0, ...init }));
const key = (k: string, target: EventTarget = window) => target.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true }));
const button = (root: ParentNode | null, label: string) => [...(root ?? document).querySelectorAll<HTMLButtonElement>('button')].find((b) => text(b) === label);
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"][aria-modal="true"]:not(.lightbox)');
const bar = () => document.querySelector<HTMLElement>('.selbar');
const tiles = () => [...document.querySelectorAll<HTMLAnchorElement>('a.tile')];
const tile = (id: number) => document.querySelector<HTMLAnchorElement>(`a.tile[href="#/l/peter/view/${id}"]`);
const check = (id: number) => document.querySelector<HTMLInputElement>(`a.tile[href="#/l/peter/view/${id}"] + input.check`)!;
const targetButton = (name: string) => [...(dialog()?.querySelectorAll<HTMLButtonElement>('button.target') ?? [])].find((b) => b.querySelector('.name')?.textContent === name);
const plans = () => server.posts('l/peter/transfer/plan').map((c) => c.body);
const jobBodies = () => server.posts('l/peter/jobs').map((c) => c.body);
const gets = (path: string) => server.calls.filter((c) => c.method === 'GET' && c.path === `/api/${path}`).length;
const lastJobId = () => server.backup.jobs[0]!.id;

async function select(...ids: number[]) {
  for (const id of ids) click(check(id));
  await settle();
}

/** Opens the dialog from the selection bar and picks `name`. */
async function moveTo(name: string) {
  click(button(bar(), 'Move to…')!);
  await settle();
  click(targetButton(name)!);
  await settle();
}

describe('move to library', () => {
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

  it('offers Move to… in the grid and album pages of a workspace, not in the trash', async () => {
    await start('#/l/peter/');
    await select(1);
    expect(button(bar(), 'Move to…')).toBeDefined();

    await go('#/l/peter/albums/2');
    await select(1);
    expect(button(bar(), 'Move to…')).toBeDefined();

    await go('#/l/peter/trash');
    click(check(9));
    await settle();
    expect(bar()).not.toBeNull();
    expect(button(bar(), 'Move to…')).toBeUndefined();
  });

  it('lists the other libraries: closed ones pickable, busy ones with the holder, broken ones not at all', async () => {
    await start('#/l/peter/');
    await select(1, 2);
    click(button(bar(), 'Move to…')!);
    await settle();
    const d = dialog()!;
    expect(d.querySelector('h2')?.textContent).toBe('Move to library');
    expect(text(d)).toContain('Move 2 photos to:');
    const names = [...d.querySelectorAll('button.target .name')].map((n) => n.textContent);
    expect(names).toEqual(['penny', 'kids']);
    expect(targetButton('penny')!.disabled).toBe(true);
    expect(text(targetButton('penny'))).toContain('in use by picmover import (pid 812)');
    expect(targetButton('kids')!.disabled).toBe(false);

    // Escape closes it.
    key('Escape', d);
    await settle();
    expect(dialog()).toBeNull();
  });

  it('opens a closed target first, then shows a same-drive plan and moves', async () => {
    await start('#/l/peter/');
    await select(1, 2);
    await moveTo('kids');
    expect(server.posts('libraries/kids/open')).toHaveLength(1);
    expect(plans()).toEqual([{ to: 'kids', hashes: [hashOf(1), hashOf(2)] }]);
    const d = dialog()!;
    expect(text(d)).toContain('Move 2 photos (2.0 KiB) to kids.');
    expect(text(d)).toContain('Same drive: instant.');
    expect(text(d)).toContain("This library's backups keep the moved files until they are pruned.");

    const librariesBefore = gets('libraries');
    const libraryBefore = gets('l/peter/library');
    click(button(d, 'Move')!);
    await settle();
    expect(jobBodies()).toEqual([{ kind: 'transfer', to: 'kids', hashes: [hashOf(1), hashOf(2)] }]);
    expect(text(dialog())).toContain('Moving 2 photos to kids.');
    // The pill says where to.
    expect(text(document.querySelector('.pill'))).toContain('Moving to kids…');

    // Progress as the server sends it.
    const id = lastJobId();
    const es = FakeEventSource.instances.filter((e) => e.url === `/api/jobs/${id}/events`).at(-1)!;
    es.emit('progress', { phase: 'move', files: 1, totalFiles: 2, bytes: 1000, totalBytes: 2000, current: 'image/2024/07/1.jpg' });
    await settle();
    expect(text(dialog())).toContain('1 of 2 files');
    expect(text(dialog())).toContain('image/2024/07/1.jpg');
    expect(text(document.querySelector('.pill'))).toContain('Moving to kids: 1 of 2 files…');

    server.finishTransfer(id);
    await settle();
    const r = dialog()!;
    expect(text(r)).toContain('Moved 2 photos to kids.');
    const open = [...r.querySelectorAll<HTMLAnchorElement>('a')].find((a) => text(a) === 'Open in kids')!;
    expect(open.getAttribute('href')).toBe('#/l/kids/imports/50');

    // The source grid lost the tiles, the selection is gone, counts and the libraries list were reloaded.
    expect(tile(1)).toBeNull();
    expect(tile(2)).toBeNull();
    expect(tiles()).toHaveLength(6);
    expect(bar()).toBeNull();
    expect(gets('libraries')).toBeGreaterThan(librariesBefore);
    expect(gets('l/peter/library')).toBeGreaterThan(libraryBefore);

    click(button(r, 'Close')!);
    await settle();
    expect(dialog()).toBeNull();
  });

  it('says a copy across drives puts the originals in the trash, and refuses one that does not fit', async () => {
    server.transfer.sameVolume = false;
    server.transfer.free = 100 * 1024 * 1024 * 1024;
    await start('#/l/peter/');
    await select(1, 2);
    await moveTo('kids');
    expect(text(dialog())).toContain("Copies 2.0 KiB across drives, then puts the originals in this library's trash.");
    expect(button(dialog(), 'Move')!.disabled).toBe(false);

    // Back, less space, plan again.
    click(button(dialog(), 'Back')!);
    await settle();
    server.transfer.free = 500;
    server.transfer.size = 4.1 * 1024 * 1024 * 1024;
    click(targetButton('kids')!);
    await settle();
    expect(text(dialog())).toContain('Not enough space in kids: 8.2 GiB needed, 500 B free.');
    expect(button(dialog(), 'Move')!.disabled).toBe(true);
    expect(jobBodies()).toEqual([]);
  });

  it('lists refused items and duplicates, and sends only what can move', async () => {
    server.transfer.refused.set(hashOf(2), 'the file is missing');
    server.transfer.existing.add(hashOf(3));
    await start('#/l/peter/');
    await select(1, 2, 3);
    await moveTo('kids');
    const d = dialog()!;
    expect(text(d)).toContain('Move 2 photos (1000 B) to kids.');
    expect(text(d)).toContain('1 is already there; its copy here goes to the trash.');
    expect(text(d)).toContain('1 photo cannot be moved: the file is missing');
    click(button(d, 'Move')!);
    await settle();
    expect(jobBodies()).toEqual([{ kind: 'transfer', to: 'kids', hashes: [hashOf(1), hashOf(3)] }]);
    server.finishTransfer(lastJobId());
    await settle();
    expect(text(dialog())).toContain('1 was already there; its copy here is in the trash.');
    expect(tile(1)).toBeNull();
    expect(tile(3)).toBeNull();
    expect(tile(2)).not.toBeNull();
  });

  it('shows failures with reasons and keeps their tiles', async () => {
    server.transfer.fail.set(hashOf(2), 'the file does not match the catalog');
    await start('#/l/peter/');
    await select(1, 2);
    await moveTo('kids');
    click(button(dialog(), 'Move')!);
    await settle();
    server.finishTransfer(lastJobId());
    await settle();
    const d = dialog()!;
    expect(text(d)).toContain('Moved 1 photo to kids.');
    expect(text(d)).toContain('1 photo could not be moved: the file does not match the catalog');
    expect(tile(1)).toBeNull();
    expect(tile(2)).not.toBeNull();
  });

  it('keeps the selection when the job cannot start', async () => {
    await start('#/l/peter/');
    await select(1);
    await moveTo('kids');
    server.libs.find((l) => l.id === 'kids')!.state = 'busy';
    click(button(dialog(), 'Move')!);
    await settle();
    expect(text(dialog())).toContain('The move failed: kids is in use by another picmover process');
    expect(text(bar())).toContain('1 selected');
    expect(tile(1)).not.toBeNull();
  });

  it('blocks on an interrupted move until Finish moving has run', async () => {
    server.transfer.interrupted = [{ from: 'peter', to: 'kids', id: 7 }];
    await start('#/l/peter/');
    await select(1);
    await moveTo('kids');
    const d = dialog()!;
    expect(text(d)).toContain('A move from peter to kids was interrupted.');
    expect(button(d, 'Move')!.disabled).toBe(true);
    click(button(d, 'Finish moving')!);
    await settle();
    expect(server.posts('libraries/kids/transfer/resume')).toHaveLength(1);
    expect(text(dialog())).toContain('Finishing the interrupted move to kids.');
    expect(text(document.querySelector('.pill'))).toContain('Finishing the move to kids…');

    server.finishTransfer(lastJobId());
    await settle();
    // Planned again, now without the block.
    expect(plans()).toHaveLength(2);
    expect(text(dialog())).not.toContain('was interrupted');
    expect(button(dialog(), 'Move')!.disabled).toBe(false);
  });

  it('runs more than 2000 hashes as consecutive jobs of 2000', async () => {
    await start('#/l/peter/');
    const { moves } = await import('./lib/moves.svelte');
    const hashes = Array.from({ length: 4100 }, (_, i) => hashOf(i + 100));
    const done = moves.start('peter', 'kids', hashes, 'selection');
    await settle();
    expect(jobBodies().map((b) => (b?.hashes as string[]).length)).toEqual([2000]);
    server.finishTransfer(lastJobId());
    await settle();
    expect(jobBodies().map((b) => (b?.hashes as string[]).length)).toEqual([2000, 2000]);
    expect(jobBodies()[1]?.hashes).toEqual(hashes.slice(2000, 4000));
    server.finishTransfer(lastJobId());
    await settle();
    expect(jobBodies().map((b) => (b?.hashes as string[]).length)).toEqual([2000, 2000, 100]);
    server.finishTransfer(lastJobId());
    await done;
    expect(moves.run).toMatchObject({ state: 'done', parts: 3, part: 3 });
    expect(moves.run!.totals.records).toEqual([50, 51, 52]);
    // Several records: "Open in" goes to the target's imports.
    const { openHref } = await import('./lib/moves.svelte');
    expect(openHref(moves.run!)).toBe('#/l/kids/imports');
  });

  it('stops after a cancelled job', async () => {
    await start('#/l/peter/');
    const { moves } = await import('./lib/moves.svelte');
    const done = moves.start('peter', 'kids', Array.from({ length: 2500 }, (_, i) => hashOf(i + 100)), 'selection');
    await settle();
    server.finishTransfer(lastJobId(), { exitCode: 130 });
    await done;
    expect(jobBodies()).toHaveLength(1);
    expect(moves.run?.state).toBe('cancelled');
  });

  it('moves the shown item with m in the viewer and goes on to the next', async () => {
    await start('#/l/peter/view/3');
    expect(document.querySelector('.lightbox')).not.toBeNull();
    const info = document.querySelector('aside.info')!;
    expect(button(info, 'Move to…')).toBeDefined();
    key('m');
    await settle();
    expect(text(dialog())).toContain('Move 1 photo to:');
    // Keys in the dialog are not the viewer's.
    key('ArrowRight', dialog()!);
    await settle();
    expect(location.hash).toBe('#/l/peter/view/3');
    click(targetButton('kids')!);
    await settle();
    click(button(dialog(), 'Move')!);
    await settle();
    expect(jobBodies()).toEqual([{ kind: 'transfer', to: 'kids', hashes: [hashOf(3)] }]);
    server.finishTransfer(lastJobId());
    await settle();
    expect(location.hash).toBe('#/l/peter/view/4');
  });

  it('has no Move to… in single mode', async () => {
    unmountApp = null;
    server = fakeServer('single');
    server.install();
    await start('#/l/peter/view/3');
    expect(button(document.querySelector('aside.info'), 'Move to…')).toBeUndefined();
    key('m');
    await settle();
    expect(dialog()).toBeNull();
    key('Escape');
    await settle();
    await select(1);
    expect(bar()).not.toBeNull();
    expect(button(bar(), 'Move to…')).toBeUndefined();
  });

  it('shows an interrupted move on both libraries with Finish moving', async () => {
    const m = { from: 'peter', to: 'kids', id: 7 };
    server.transfer.interrupted = [m];
    server.libs.find((l) => l.id === 'peter')!.interruptedMoves = [m];
    server.libs.find((l) => l.id === 'kids')!.interruptedMoves = [m];
    await start('#/');
    const cards = [...document.querySelectorAll<HTMLElement>('li.libcard')];
    const card = (name: string) => cards.find((c) => c.querySelector('.name')?.textContent === name)!;
    expect(text(card('peter'))).toContain('A move to kids was interrupted.');
    expect(text(card('kids'))).toContain('A move from peter was interrupted.');
    click(button(card('kids'), 'Finish moving')!);
    await settle();
    expect(server.posts('libraries/kids/transfer/resume')).toHaveLength(1);
    server.finishTransfer(lastJobId());
    await settle();
    expect(document.body.textContent).not.toContain('was interrupted');
    expect(text(document.querySelector('.toast'))).toContain('Finished the interrupted move to kids.');
  });
});
