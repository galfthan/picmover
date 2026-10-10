// @vitest-environment jsdom
//
// Step 5 backups through the whole app against the fake server: the
// Backups page (cards, headline, problems, Back up now with inline
// progress, rate, ETA and Cancel, scrub budgets, prune with the typed
// count, restore preview, Add target with the folder picker and typed UNC
// paths, edit and forget), and the status lines of the Library and
// Libraries pages.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fakeServer, FakeEventSource, go, settle, T1, T2, unix, type FakeServer } from './testing/fakes';

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

/** Lets effects run without moving the clock. */
async function micro() {
  for (let i = 0; i < 10; i++) await Promise.resolve();
}

const MiB = 1024 * 1024;
const card = (label: string) => [...document.querySelectorAll<HTMLElement>('li.target')].find((c) => c.querySelector('h2.label')?.textContent === label)!;
const button = (root: ParentNode, label: string) => [...root.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === label);
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]');
const gets = (path: string) => server.calls.filter((c) => c.method === 'GET' && c.path === `/api/${path}`);
const jobBodies = () => server.posts('l/peter/jobs').map((c) => c.body);
const text = (el: Element | null | undefined) => (el?.textContent ?? '').replace(/\s+/g, ' ').trim();

function type(input: HTMLInputElement | HTMLSelectElement, value: string) {
  input.value = value;
  input.dispatchEvent(new Event(input instanceof HTMLSelectElement ? 'change' : 'input', { bubbles: true }));
}

describe('backups', () => {
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

  it('shows the targets with their headline, facts, status and problems', async () => {
    await start('#/l/peter/backups');
    expect(document.querySelector('nav a[aria-current="page"]')?.textContent).toBe('Backups');
    expect(document.querySelector('h1')?.textContent).toBe('Backups');
    // The library being backed up is always named.
    expect(text(document.querySelector('.dest'))).toContain('/media/peter');
    // No behind numbers yet: the page asks for a plan once.
    expect(gets('l/peter/backups')).toHaveLength(1);
    expect(gets('l/peter/backups?plan=1')).toHaveLength(1);
    expect(gets(`l/peter/backups/targets/${T1}/runs`)).toHaveLength(1);
    expect(gets(`l/peter/backups/targets/${T2}/runs`)).toHaveLength(1);

    const red = card('Red WD drive');
    expect(red.querySelector('.badge')?.textContent).toBe('Connected');
    expect(red.querySelector('.path')?.textContent).toBe('/mnt/red');
    expect(red.querySelector('.headline')?.textContent).toBe('3 files, 2.0 KiB not backed up yet');
    const facts = text(red.querySelector('.facts'));
    expect(facts).toContain('Last backup 2 hours ago');
    expect(facts).toContain('Last scrub last week');
    expect(facts).toContain('On the target 8 files, 7.8 KiB');
    expect(facts).toContain('Not verified yet 2 files');
    expect(facts).toContain('Damaged 1 file');
    expect(facts).toContain('Not in the library 3 files');

    // Problems of the last scrub and the last backup.
    const problems = red.querySelector<HTMLDetailsElement>('details.problems')!;
    expect(problems.open).toBe(true);
    expect(text(problems.querySelector('summary'))).toBe('Problems 4');
    const damaged = [...problems.querySelectorAll('.damaged li')].map(text);
    expect(damaged).toEqual(['image/2024/06/7.jpg repaired', 'image/2024/06/8.jpg not repaired — the library copy differs too']);
    expect(text(problems.querySelector('.failed'))).toContain('image/2024/07/3.jpg — read: input/output error');
    expect(text(problems.querySelector('.unknown'))).toContain('notes/stray.txt');
    expect(problems.querySelector('.missing')).toBeNull();

    const nas = card('NAS');
    expect(nas.querySelector('.badge')?.textContent).toBe('Not found');
    expect(nas.querySelector('.path')?.textContent).toBe('\\\\nas\\photos');
    expect(nas.querySelector('.headline')?.textContent).toBe('Not reachable');
    expect(button(nas, 'Back up now')?.disabled).toBe(true);
    expect(button(nas, 'Prune…')?.disabled).toBe(true);
    expect(text(nas.querySelector('details.problems summary'))).toBe('No problems in the last runs');

    expect(document.querySelector('.dest .status')?.textContent).toBe('Backed up 2 hours ago to Red WD drive; 3 behind');
  });

  it('backs up with inline progress, rate, ETA and Cancel, then reloads', async () => {
    await start('#/l/peter/backups');
    button(card('Red WD drive'), 'Back up now')!.click();
    await settle();
    expect(jobBodies()).toEqual([{ kind: 'backup', target: T1 }]);
    const es = FakeEventSource.instances.at(-1)!;
    expect(es.url).toBe('/api/jobs/1/events');
    // Every action waits while a job runs.
    expect(button(card('NAS'), 'Forget')).toBeDefined();

    es.emit('progress', { phase: 'copy', files: 1, bytes: 1 * MiB, totalFiles: 4, totalBytes: 25 * MiB, current: 'image/2024/07/1.jpg', failed: 0 });
    await micro();
    let p = card('Red WD drive').querySelector('.bprogress')!;
    expect(p).not.toBeNull();
    expect(text(p.querySelector('.phase'))).toBe('Copying');
    expect(text(p.querySelector('.files'))).toBe('1 of 4 files');
    expect(text(p.querySelector('.bytes'))).toBe('1.0 MiB of 25.0 MiB');
    expect(p.querySelector('.current')?.textContent).toBe('image/2024/07/1.jpg');
    expect(p.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('4');
    expect(p.querySelector('.rate')).toBeNull(); // one sample: no rate yet
    expect(document.querySelector('.pill')?.textContent).toContain('Backing up 1 of 4 files');

    await vi.advanceTimersByTimeAsync(2000);
    es.emit('progress', { phase: 'copy', files: 2, bytes: 5 * MiB, totalFiles: 4, totalBytes: 25 * MiB, current: 'image/2024/07/2.jpg', failed: 1 });
    await micro();
    p = card('Red WD drive').querySelector('.bprogress')!;
    expect(text(p.querySelector('.rate'))).toBe('2.0 MiB/s');
    expect(text(p.querySelector('.eta'))).toBe('about 10s left');
    expect(text(p.querySelector('.failed'))).toBe('1 failed');
    expect(p.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('20');
    // The other cards cannot start anything meanwhile.
    expect(button(card('NAS'), 'Back up now')?.disabled).toBe(true);

    // Cancel asks first, inline.
    button(p, 'Cancel')!.click();
    await micro();
    button(p, 'Stop')!.click();
    await settle();
    expect(server.posts('jobs/1/cancel')).toHaveLength(1);

    const before = gets('l/peter/backups?plan=1').length;
    es.emit('done', { id: 1, kind: 'backup', state: 'done', library: 'peter', startedAt: '2026-10-10T12:00:00Z', finishedAt: '2026-10-10T12:00:05Z', stats: { copied: 4, bytes: 25 * MiB }, error: null, exitCode: 0 });
    await settle();
    expect(card('Red WD drive').querySelector('.bprogress')).toBeNull();
    expect(gets('l/peter/backups?plan=1').length).toBe(before + 1);
    expect(gets(`l/peter/backups/targets/${T1}/runs`).length).toBe(2);
    expect(text(card('Red WD drive').querySelector('.outcome'))).toBe('Backup finished: copied 4 files (25.0 MiB).');
  });

  it('scrubs with the chosen budget', async () => {
    await start('#/l/peter/backups');
    const red = () => card('Red WD drive');
    expect([...red().querySelectorAll('select option')].map((o) => o.textContent)).toEqual(['15 min', '1 h', 'All']);
    const finish = async (id: number) => {
      FakeEventSource.instances.at(-1)!.emit('done', { id, kind: 'scrub', state: 'done', library: 'peter', startedAt: '2026-10-10T12:00:00Z', finishedAt: '2026-10-10T12:00:01Z', stats: {}, error: null, exitCode: 0 });
      await settle();
    };
    button(red(), 'Scrub')!.click();
    await settle();
    await finish(1);
    type(red().querySelector<HTMLSelectElement>('select[aria-label="Scrub budget"]')!, '3600');
    button(red(), 'Scrub')!.click();
    await settle();
    await finish(2);
    type(red().querySelector<HTMLSelectElement>('select[aria-label="Scrub budget"]')!, 'null');
    button(red(), 'Scrub')!.click();
    await settle();
    expect(jobBodies()).toEqual([
      { kind: 'scrub', target: T1, budget: 900, repair: true },
      { kind: 'scrub', target: T1, budget: 3600, repair: true },
      { kind: 'scrub', target: T1, repair: true },
    ]);
  });

  it('prunes only after the count of a fresh plan is typed', async () => {
    await start('#/l/peter/backups');
    button(card('Red WD drive'), 'Prune…')!.click();
    await settle();
    expect(gets(`l/peter/backups/targets/${T1}/plan`)).toHaveLength(1);
    let d = dialog()!;
    expect(text(d.querySelector('h2'))).toBe('Prune Red WD drive');
    expect(text(d)).toContain('3 files (2.9 KiB) on the target are no longer in the library');
    expect([...d.querySelectorAll('.paths li')].map(text)).toEqual(['image/2023/01/x.jpg', 'image/2023/01/y.jpg']);
    expect(text(d.querySelector('.more'))).toBe('and 1 more');
    const input = d.querySelector<HTMLInputElement>('input')!;
    const submit = () => button(dialog()!, 'Prune 3 files')!;
    expect(submit().disabled).toBe(true);
    type(input, '2');
    await micro();
    expect(submit().disabled).toBe(true);

    // The target changed after the plan was shown: the server refuses the stale count.
    server.backup.plans[T1]!.extra!.count = 4;
    type(input, '3');
    await micro();
    expect(submit().disabled).toBe(false);
    submit().click();
    await settle();
    expect(jobBodies()).toEqual([{ kind: 'prune', target: T1, confirm: true, expected: 3 }]);
    expect(dialog()?.querySelector('[role="alert"]')?.textContent).toBe('the plan changed: 4 files would be removed');

    // Opened again, the dialog shows the new count, which is what is sent.
    button(dialog()!, 'Cancel')!.click();
    await settle();
    expect(dialog()).toBeNull();
    button(card('Red WD drive'), 'Prune…')!.click();
    await settle();
    d = dialog()!;
    expect(text(d)).toContain('4 files');
    type(d.querySelector<HTMLInputElement>('input')!, ' 4 ');
    await micro();
    button(d, 'Prune 4 files')!.click();
    await settle();
    expect(jobBodies()[1]).toEqual({ kind: 'prune', target: T1, confirm: true, expected: 4 });
    expect(dialog()).toBeNull();
  });

  it('shows what a restore would bring back first', async () => {
    await start('#/l/peter/backups');
    button(card('Red WD drive'), 'Restore missing…')!.click();
    await settle();
    const d = dialog()!;
    expect(text(d)).toContain('3 files (2.9 KiB) on the target are missing from the library and would be copied back');
    expect([...d.querySelectorAll('.paths li')].map(text)).toEqual(['image/2023/01/x.jpg', 'image/2023/01/y.jpg']);
    button(d, 'Restore 3 files')!.click();
    await settle();
    expect(jobBodies()).toEqual([{ kind: 'restore', target: T1, mode: 'missing' }]);
    expect(dialog()).toBeNull();
    FakeEventSource.instances.at(-1)!.emit('done', { id: 1, kind: 'restore', state: 'done', library: 'peter', startedAt: '2026-10-10T12:00:00Z', finishedAt: '2026-10-10T12:00:01Z', stats: {}, error: null, exitCode: 0 });
    await settle();

    // Nothing missing: no Restore button.
    server.backup.plans[T1]!.extra = { count: 0, bytes: 0, paths: [] };
    button(card('Red WD drive'), 'Restore missing…')!.click();
    await settle();
    expect(text(dialog())).toContain('Nothing is missing');
    expect(button(dialog()!, 'Restore 0 files')).toBeUndefined();
  });

  it('adds a target with the folder picker or a typed UNC path', async () => {
    await start('#/l/peter/backups');
    const opener = button(document.querySelector('.head')!, 'Add target')!;
    opener.focus();
    opener.click();
    await settle();
    let d = dialog()!;
    expect(text(d.querySelector('h2'))).toBe('Add backup target');
    expect(text(d)).toContain('Backs up /media/peter.');
    const path = () => dialog()!.querySelector<HTMLInputElement>('input.mono')!;
    expect(document.activeElement).toBe(path());
    const submit = () => [...dialog()!.querySelectorAll<HTMLButtonElement>('button[type="submit"]')][0]!;

    type(path(), 'backup');
    await micro();
    expect(dialog()!.querySelector('[role="alert"]')?.textContent).toMatch(/absolute path/);
    expect(submit().disabled).toBe(true);

    // Browse: the directory browser fills the path and a default label.
    button(dialog()!, 'Browse…')!.click();
    await settle();
    expect(server.calls.some((c) => c.path === '/api/fs?path=')).toBe(true);
    const mnt = [...dialog()!.querySelectorAll<HTMLButtonElement>('button.entry')].find((b) => b.querySelector('.name')?.textContent === 'mnt')!;
    mnt.click();
    await settle();
    button(dialog()!, 'Use this folder')!.click();
    await settle();
    expect(path().value).toBe('/mnt');
    const label = dialog()!.querySelectorAll<HTMLInputElement>('input')[1]!;
    expect(label.value).toBe('mnt');
    type(label, 'Spare');
    await micro();
    submit().click();
    await settle();
    expect(server.posts('l/peter/backups/targets').map((c) => c.body)).toEqual([{ path: '/mnt', label: 'Spare' }]);
    expect(dialog()).toBeNull();
    expect(card('Spare').querySelector('.headline')?.textContent).toBe('Nothing backed up here yet');
    expect([...document.querySelectorAll('.toast .text')].map((t) => t.textContent)).toContain('Added Spare');

    // A typed network share is sent as is; a conflict is shown in the dialog.
    button(document.querySelector('.head')!, 'Add target')!.click();
    await settle();
    type(path(), '/mnt/taken');
    type(dialog()!.querySelectorAll<HTMLInputElement>('input')[1]!, 'Taken');
    await micro();
    submit().click();
    await settle();
    expect(dialog()!.querySelector('[role="alert"]')?.textContent).toBe("the folder holds another library's backup set named peter");
    type(path(), '\\\\nas2\\share');
    type(dialog()!.querySelectorAll<HTMLInputElement>('input')[1]!, 'NAS 2');
    await micro();
    expect(dialog()!.querySelector('[role="alert"]')).toBeNull();
    submit().click();
    await settle();
    expect(server.posts('l/peter/backups/targets').at(-1)?.body).toEqual({ path: '\\\\nas2\\share', label: 'NAS 2' });
    expect(card('NAS 2').querySelector('.path')?.textContent).toBe('\\\\nas2\\share');

    // Escape closes and focus goes back to the opener.
    const again = button(document.querySelector('.head')!, 'Add target')!;
    again.focus();
    again.click();
    await settle();
    d = dialog()!;
    d.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
    await settle();
    expect(dialog()).toBeNull();
    expect(document.activeElement).toBe(again);
  });

  it('re-points, renames and forgets a target', async () => {
    await start('#/l/peter/backups');
    button(card('NAS'), 'Edit')!.click();
    await settle();
    const form = card('NAS').querySelector('form.edit')!;
    const folder = form.querySelector<HTMLInputElement>('input[aria-label="Target folder"]')!;
    expect(folder.value).toBe('\\\\nas\\photos');
    type(folder, '/mnt/wrong');
    await micro();
    button(form, 'Save')!.click();
    await settle();
    expect(form.querySelector('[role="alert"]')?.textContent).toBe('the marker in that folder belongs to another target');
    type(folder, '/media/nas');
    await micro();
    button(form, 'Save')!.click();
    await settle();
    expect(server.posts(`l/peter/backups/targets/${T2}`).map((c) => c.body)).toEqual([{ path: '/mnt/wrong' }, { path: '/media/nas' }]);
    expect(card('NAS').querySelector('.badge')?.textContent).toBe('Connected');
    expect(card('NAS').querySelector('.path')?.textContent).toBe('/media/nas');

    // Rename only sends the label.
    button(card('NAS'), 'Edit')!.click();
    await settle();
    type(card('NAS').querySelector<HTMLInputElement>('form.edit input')!, 'Basement NAS');
    await micro();
    button(card('NAS').querySelector('form.edit')!, 'Save')!.click();
    await settle();
    expect(server.posts(`l/peter/backups/targets/${T2}`).at(-1)?.body).toEqual({ label: 'Basement NAS' });

    const nas = card('Basement NAS');
    button(nas, 'Forget')!.click();
    await micro();
    expect(text(nas.querySelector('.confirm'))).toContain('The files on it are not touched.');
    button(nas.querySelector('.confirm')!, 'Forget')!.click();
    await settle();
    expect(server.posts(`l/peter/backups/targets/${T2}/forget`)).toHaveLength(1);
    expect(card('Basement NAS')).toBeUndefined();
    expect(document.querySelectorAll('li.target')).toHaveLength(1);
  });

  it('says so when there is no target', async () => {
    server.backup.targets.length = 0;
    await start('#/l/peter/backups');
    expect(text(document.querySelector('.empty h2'))).toBe('No backup targets yet');
    expect(document.querySelector('.dest .status')?.textContent).toBe('No backup target yet');
    expect(document.querySelector('.dest .status')?.classList.contains('tone-warn')).toBe(true);
  });

  it('shows the backup status line on the Library page, with the 30-day warning', async () => {
    await start('#/l/peter/library');
    const line = () => document.querySelector('p.backup');
    expect(text(line())).toBe('Backed up 2 hours ago to Red WD drive · Backups');
    expect(line()?.querySelector('a')?.getAttribute('href')).toBe('#/l/peter/backups');
    expect(line()?.querySelector('.tone-ok')).not.toBeNull();

    for (const t of server.backup.targets) t.lastBackupAt = unix('2026-09-01T10:00:00');
    await go('#/l/peter/');
    await go('#/l/peter/library');
    expect(text(line())).toBe('⚠ Backed up last month to Red WD drive (older than 30 days) · Backups');
    expect(line()?.querySelector('.tone-warn')).not.toBeNull();

    server.backup.targets.length = 0;
    await go('#/l/peter/');
    await go('#/l/peter/library');
    expect(text(line())).toBe('⚠ No backup target yet · Add a backup target');
  });
});

describe('Libraries page backup lines', () => {
  beforeEach(() => {
    vi.resetModules();
    server = fakeServer('workspace');
    server.install();
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

  it('shows the listing summary per card, and nothing without one', async () => {
    await start('#/');
    const libcard = (name: string) => [...document.querySelectorAll<HTMLElement>('.libcard')].find((c) => c.querySelector('.name')?.textContent === name)!;
    const peter = libcard('peter').querySelector('.backup');
    expect(text(peter)).toBe('Backed up 2 hours ago to Red WD drive; 3 behind');
    expect(peter?.classList.contains('tone-ok')).toBe(true);
    const penny = libcard('penny').querySelector('.backup');
    expect(text(penny)).toBe('⚠ Backed up 2 months ago; 5 behind (older than 30 days)');
    expect(penny?.classList.contains('tone-warn')).toBe(true);
    expect(libcard('broken').querySelector('.backup')).toBeNull();
    expect(libcard('scans').querySelector('.backup')).toBeNull();
    // The listing is enough: no library-scoped backup requests from this page.
    expect(server.calls.some((c) => c.path.includes('/backups'))).toBe(false);
  });
});
