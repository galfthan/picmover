// @vitest-environment jsdom
//
// Step 5 backups through the whole app against the fake server: the
// Backups page (cards, headline, problems, Back up now with inline
// progress, rate, ETA and Cancel, scrub budgets, prune with the typed
// count, restore preview, Add target with the folder picker and typed UNC
// paths, edit and forget), and the status lines of the Library and
// Libraries pages. The fake answers as the real step 5 server does (job
// objects with `target`, `result` and zero `stats`, list-shaped run stats
// with counts, `rate`/`eta`, target `error`), see testing/fakes.ts.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { backupJobJSON, fakeServer, FakeEventSource, go, progress, runResult, settle, T1, T2, unix, type FakeServer } from './testing/fakes';

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

  it('shows the targets with their headline, facts, status and problems', async () => {
    await start('#/l/peter/backups');
    expect(document.querySelector('nav a[aria-current="page"]')?.textContent).toBe('Backups');
    expect(document.querySelector('h1')?.textContent).toBe('Backups');
    // The library being backed up is always named.
    expect(text(document.querySelector('.dest'))).toContain('/media/peter');
    // The plain GET has the behind numbers (the server plans on a cache miss): no ?plan=1.
    expect(gets('l/peter/backups')).toHaveLength(1);
    expect(gets('l/peter/backups?plan=1')).toHaveLength(0);
    expect(gets(`l/peter/backups/targets/${T1}/runs`)).toHaveLength(1);
    expect(gets(`l/peter/backups/targets/${T2}/runs`)).toHaveLength(1);

    const red = card('Red WD drive');
    expect(red.querySelector('.badge')?.textContent).toBe('Connected');
    expect(red.querySelector('.path')?.textContent).toBe('/mnt/red');
    expect(red.querySelector('.headline')?.textContent).toBe('2 files, 2.0 KiB not backed up yet; 1 to move');
    const facts = text(red.querySelector('.facts'));
    expect(facts).toContain('Last backup 2 hours ago');
    expect(facts).toContain('Last scrub last week');
    expect(facts).toContain('On the target 8 files, 7.8 KiB');
    expect(facts).toContain('Not scrubbed yet 2 files');
    expect(facts).toContain('Damaged 1 file');
    expect(facts).toContain('Not in the library 3 files');

    // Problems of the last scrub and the last backup; repaired files are listed but not counted.
    const problems = red.querySelector<HTMLDetailsElement>('details.problems')!;
    expect(problems.open).toBe(true);
    expect(text(problems.querySelector('summary'))).toBe('Problems 3');
    const damaged = [...problems.querySelectorAll('.damaged li')].map(text);
    expect(damaged).toEqual(['image/2024/06/7.jpg repaired', 'image/2024/06/8.jpg not repaired — the library copy differs too']);
    expect(text(problems.querySelector('.damaged h4'))).toBe('Damaged files in the last scrub (2, 1 repaired)');
    expect(text(problems.querySelector('.failed'))).toContain('image/2024/07/3.jpg — read: input/output error');
    expect(text(problems.querySelector('.unknown'))).toContain('notes/stray.txt');
    // The scrub copied the missing file again (`repaired` 2 = 1 damaged + 1 missing).
    expect([...problems.querySelectorAll('.missing li')].map(text)).toEqual(['image/2024/07/5.jpg repaired']);
    expect(problems.querySelector('.truncated')).toBeNull();

    const nas = card('NAS');
    expect(nas.querySelector('.badge')?.textContent).toBe('Not found');
    expect(nas.querySelector('.path')?.textContent).toBe('\\\\nas\\photos');
    expect(nas.querySelector('.headline')?.textContent).toBe('Not reachable');
    expect(button(nas, 'Back up now')?.disabled).toBe(true);
    expect(button(nas, 'Prune…')?.disabled).toBe(true);
    expect(text(nas.querySelector('details.problems summary'))).toBe('No problems in the last runs');

    expect(document.querySelector('.dest .status')?.textContent).toBe('Backed up 2 hours ago to Red WD drive; 3 behind');
  });

  it('asks for a plan once when the targets come without behind numbers', async () => {
    server.backup.planOnGet = false;
    await start('#/l/peter/backups');
    expect(gets('l/peter/backups')).toHaveLength(1);
    expect(gets('l/peter/backups?plan=1')).toHaveLength(1);
    expect(card('Red WD drive').querySelector('.headline')?.textContent).toBe('2 files, 2.0 KiB not backed up yet; 1 to move');
  });

  it("does not show another library's job on a shared target (H2)", async () => {
    // penny backs up to the same drive (same marker id) while peter's page is open.
    server.backup.jobs.push(backupJobJSON({ id: 5, kind: 'backup', target: T1, library: 'penny' }));
    await start('#/l/peter/backups');
    const es = FakeEventSource.instances.find((e) => e.url === '/api/jobs/5/events')!;
    expect(es).toBeDefined();
    es.emit('progress', progress({ files: 1, bytes: MiB, totalFiles: 4, totalBytes: 4 * MiB, current: 'image/x.jpg', rate: MiB, eta: 3 }));
    await micro();
    const red = card('Red WD drive');
    expect(red.querySelector('.bprogress')).toBeNull();
    expect(button(red, 'Cancel')).toBeUndefined();
    // Not an orphan of this library either.
    expect(document.querySelector('section.card .bprogress')).toBeNull();
    // One job per process: peter's actions wait.
    expect(button(red, 'Back up now')?.disabled).toBe(true);
    expect(text(red)).toContain('Another job is running');

    server.finish(5, { result: { new: 4, copied: 4, bytes: 4 * MiB } });
    await settle();
    expect(card('Red WD drive').querySelector('.outcome')).toBeNull();
    expect(button(card('Red WD drive'), 'Back up now')?.disabled).toBe(false);
  });

  it('shows a target error as the headline and offers only Edit and Forget (M1)', async () => {
    const msg = 'the target already holds a backup of another library under this name';
    server.backup.targets[0]!.error = msg;
    await start('#/l/peter/backups');
    const red = card('Red WD drive');
    expect(red.querySelector('.headline')?.textContent).toBe(msg);
    expect(red.querySelector('.headline')?.classList.contains('tone-warn')).toBe(true);
    for (const b of ['Back up now', 'Scrub', 'Prune…', 'Restore missing…']) expect(button(red, b)?.disabled).toBe(true);
    expect(red.querySelector<HTMLSelectElement>('select')?.disabled).toBe(true);
    expect(button(red, 'Edit')?.disabled).toBe(false);
    expect(button(red, 'Forget')?.disabled).toBe(false);
    expect(text(red)).toContain('Edit the folder, or Forget the target');
    expect(red.querySelector('.facts')?.textContent).not.toContain('On the target');
  });

  it('offers Take over for a set last written by another copy of this library', async () => {
    const msg = 'the backup set was last written by another copy of this library (/home/peter/Pictures on laptop)';
    server.backup.targets[0]!.error = msg;
    server.backup.targets[0]!.foreign = true;
    await start('#/l/peter/backups');
    const red = card('Red WD drive');
    expect(red.querySelector('.headline')?.textContent).toBe(msg);
    expect(button(red, 'Back up now')?.disabled).toBe(true);
    const take = button(red, 'Take over…')!;
    expect(take.disabled).toBe(false);
    take.click();
    await settle();
    const d = dialog()!;
    expect(text(d.querySelector('h2'))).toBe('Take over the backup on Red WD drive');
    expect(text(d)).toContain(msg);
    expect(text(d)).toContain("this library becomes the writer of that backup set");
    expect(text(d)).toContain("the other copy's backups there are treated as this library's");
    // Cancel sends nothing.
    button(d, 'Cancel')!.click();
    await settle();
    expect(jobBodies()).toEqual([]);
    button(card('Red WD drive'), 'Take over…')!.click();
    await settle();
    button(dialog()!, 'Take over and back up')!.click();
    await settle();
    expect(jobBodies()).toEqual([{ kind: 'backup', target: T1, takeOver: true }]);
    expect(dialog()).toBeNull();
    expect(card('Red WD drive').querySelector('.bprogress')).not.toBeNull();
  });

  it('has no Take over for an error target that is not foreign', async () => {
    server.backup.targets[0]!.error = 'the target already holds a backup of another library under this name';
    await start('#/l/peter/backups');
    expect(button(card('Red WD drive'), 'Take over…')).toBeUndefined();
  });

  it('shows the run counts of capped lists and says they were truncated (M2)', async () => {
    const failed = Array.from({ length: 500 }, (_, i) => ({ path: `image/f${i}.jpg`, error: 'read: input/output error' }));
    server.backup.runs[T1] = [
      { id: 9, kind: 'backup', startedAt: unix('2026-10-10T09:00:00'), finishedAt: unix('2026-10-10T10:00:00'), stats: runResult('backup', { new: 3100, copied: 100, failed, failedCount: 3000, truncated: true }) },
    ];
    await start('#/l/peter/backups');
    const problems = card('Red WD drive').querySelector('details.problems')!;
    expect(text(problems.querySelector('summary'))).toBe('Problems 3,000');
    expect(text(problems.querySelector('.failed h4'))).toBe('Copy failures in the last backup (3,000)');
    expect(problems.querySelectorAll('.failed li')).toHaveLength(100);
    expect(text(problems.querySelector('.failed'))).toContain('and 2,900 more; see the run log.');
    expect(text(problems.querySelector('.failed .truncated'))).toBe("The run's list truncated to 500; the run log has every file.");
  });

  it('backs up with inline progress, rate, ETA and Cancel, then reloads', async () => {
    await start('#/l/peter/backups');
    const backupBtn = button(card('Red WD drive'), 'Back up now')!;
    backupBtn.focus();
    backupBtn.click();
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
    // The actions row went away: focus is on Cancel, not lost (L6).
    expect(document.activeElement).toBe(button(p, 'Cancel'));
    // Only the short status line is live (M6): not the counters.
    expect(p.closest('[aria-live]')).toBeNull();
    expect(p.querySelector('[aria-live]')).toBeNull();
    const live = card('Red WD drive').querySelector('[aria-live="polite"]')!;
    expect(live.textContent).toBe('Backup: copying');
    expect(p.querySelector('[role="progressbar"]')?.getAttribute('aria-valuetext')).toBe('1 of 4 files');

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
    // As the server: the run stats are in `result`, `stats` stays the importer's zeros (H1).
    server.finish(1, { result: { new: 4, copied: 4, bytes: 25 * MiB } });
    await settle();
    expect(card('Red WD drive').querySelector('.bprogress')).toBeNull();
    expect(gets('l/peter/backups?plan=1').length).toBe(before + 1);
    expect(gets(`l/peter/backups/targets/${T1}/runs`).length).toBe(2);
    expect(text(card('Red WD drive').querySelector('.outcome'))).toBe('Backup finished: copied 4 files (25.0 MiB).');
    expect(card('Red WD drive').querySelector('[aria-live="polite"]')?.textContent).toBe('Backup finished: copied 4 files (25.0 MiB).');
    // Cancel went away with the progress: focus goes back to Back up now.
    expect(document.activeElement).toBe(button(card('Red WD drive'), 'Back up now'));
  });

  it("uses the server's rate and ETA, and shows the snapshot phase without counters (L1, L7)", async () => {
    await start('#/l/peter/backups');
    button(card('Red WD drive'), 'Back up now')!.click();
    await settle();
    const es = FakeEventSource.instances.at(-1)!;
    // One event is enough: the server measured the rate.
    es.emit('progress', progress({ files: 1, bytes: MiB, totalFiles: 4, totalBytes: 25 * MiB, current: 'image/2024/07/1.jpg', rate: 2 * MiB, eta: 12 }));
    await micro();
    let p = card('Red WD drive').querySelector('.bprogress')!;
    expect(text(p.querySelector('.rate'))).toBe('2.0 MiB/s');
    expect(text(p.querySelector('.eta'))).toBe('about 12s left');
    // -1: unknown, nothing is made up.
    es.emit('progress', progress({ files: 1, bytes: MiB, totalFiles: 4, totalBytes: 25 * MiB, rate: 0, eta: -1 }));
    await micro();
    p = card('Red WD drive').querySelector('.bprogress')!;
    expect(p.querySelector('.rate')).toBeNull();
    expect(p.querySelector('.eta')).toBeNull();

    // The real snapshot event after the copy: counters of the snapshot files, not of the backup.
    es.emit('progress', progress({ phase: 'snapshot', files: 0, bytes: 0, totalFiles: 2, totalBytes: 169 * 1024 }));
    await micro();
    p = card('Red WD drive').querySelector('.bprogress')!;
    expect(text(p.querySelector('.phase'))).toBe('Saving catalog snapshots…');
    expect(p.querySelector('.files')).toBeNull();
    expect(p.querySelector('[role="progressbar"]')).toBeNull();
    expect(text(document.querySelector('.pill .text'))).toBe('Saving catalog snapshots…');
    expect(document.querySelector('.pill .pct')).toBeNull();
    expect(card('Red WD drive').querySelector('[aria-live="polite"]')?.textContent).toBe('Backup: saving catalog snapshots');
  });

  it('scrubs with the chosen budget', async () => {
    await start('#/l/peter/backups');
    const red = () => card('Red WD drive');
    expect([...red().querySelectorAll('select option')].map((o) => o.textContent)).toEqual(['15 min', '1 h', 'All']);
    const finish = async (id: number) => {
      server.finish(id, { result: { checked: 8, ok: 8, bytes: 8000 } });
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
    // A whole-set scrub shows files of the pass and an ETA.
    FakeEventSource.instances.at(-1)!.emit('progress', progress({ phase: 'scrub', files: 2, bytes: 2000, totalFiles: 8, totalBytes: 8000, rate: 1000, eta: 6 }));
    await micro();
    const p = red().querySelector('.bprogress')!;
    expect(text(p.querySelector('.files'))).toBe('2 of 8 files');
    expect(text(p.querySelector('.eta'))).toBe('about 6s left');
  });

  it('shows a budgeted scrub as time used of the budget, without an ETA (M5)', async () => {
    await start('#/l/peter/backups');
    button(card('Red WD drive'), 'Scrub')!.click();
    await settle();
    expect(jobBodies()).toEqual([{ kind: 'scrub', target: T1, budget: 900, repair: true }]);
    // The server's totals are the whole pass, not what 15 minutes will read.
    const es = FakeEventSource.instances.at(-1)!;
    await vi.advanceTimersByTimeAsync(90_000);
    es.emit('progress', progress({ phase: 'scrub', files: 12, bytes: 12 * MiB, totalFiles: 5000, totalBytes: 5000 * MiB, current: 'image/a.jpg', rate: MiB, eta: 4988 }));
    await micro();
    const p = card('Red WD drive').querySelector('.bprogress')!;
    expect(text(p.querySelector('.files'))).toBe('12 files checked');
    expect(p.querySelector('.eta')).toBeNull();
    expect(text(p.querySelector('.budget-left'))).toBe('stops after 15 min');
    const bar = p.querySelector('[role="progressbar"]')!;
    expect(bar.getAttribute('aria-valuenow')).toBe('10');
    expect(bar.getAttribute('aria-valuetext')).toMatch(/^1m 3\ds of 15 min, 12 files checked$/);
    expect(text(document.querySelector('.pill .text'))).toBe('Scrubbing: 12 files checked…');
    expect(document.querySelector('.pill .pct')).toBeNull();
  });

  it('shows the server message of a scrub that found damage, and the run stats of every kind (H1)', async () => {
    await start('#/l/peter/backups');
    button(card('Red WD drive'), 'Scrub')!.click();
    await settle();
    server.finish(1, {
      exitCode: 1,
      error: 'the scrub found 1 damaged and 1 missing files (2 repaired); see the job log',
      result: { checked: 5, bytes: 31991, ok: 4, damaged: [{ path: 'image/a.jpg', repaired: true }], damagedCount: 1, repaired: 2, missing: ['image/b.jpg'], missingCount: 1, unknown: ['unknown.txt'], unknownCount: 1 },
    });
    await settle();
    const o = card('Red WD drive').querySelector('.outcome')!;
    expect(o.classList.contains('tone-warn')).toBe(true);
    expect(text(o)).toBe('The scrub found 1 damaged and 1 missing files (2 repaired); see the job log (/media/peter/.picmover/logs/scrub_2026-10-10_12-00-00.log)');
  });

  it('prunes only after the count of a fresh plan is typed', async () => {
    await start('#/l/peter/backups');
    button(card('Red WD drive'), 'Prune…')!.click();
    await settle();
    expect(gets(`l/peter/backups/targets/${T1}/plan`)).toHaveLength(1);
    let d = dialog()!;
    expect(text(d.querySelector('h2'))).toBe('Prune Red WD drive');
    expect(text(d)).toContain('3 files (2.9 KiB) on the target are no longer in the library');
    expect([...d.querySelectorAll('.paths li')].map(text)).toEqual(['image/2023/01/x.jpg', 'image/2023/01/y.jpg', '.picmover/previous/image/2024/06/8.jpg.1757000000']);
    expect(d.querySelector('.more')).toBeNull();
    const input = () => dialog()!.querySelector<HTMLInputElement>('input')!;
    // The plan arrived: the typed-count field has focus (L6).
    expect(document.activeElement).toBe(input());
    const submit = () => button(dialog()!, 'Prune 3 files')!;
    expect(submit().disabled).toBe(true);
    type(input(), '2');
    await micro();
    expect(submit().disabled).toBe(true);

    // The target changed after the plan was shown: the server refuses the stale count.
    server.backup.plans[T1]!.extra = { count: 1204, bytes: 1204 * 1000, paths: ['image/2022/z.jpg'] };
    type(input(), '3');
    await micro();
    expect(submit().disabled).toBe(false);
    submit().click();
    await settle();
    expect(jobBodies()).toEqual([{ kind: 'prune', target: T1, confirm: true, expected: 3 }]);
    // The new plan is shown at once with the server's message above it, and the old count is gone (L2).
    d = dialog()!;
    expect(d.querySelector('[role="alert"]')?.textContent).toBe('the files to remove changed: the plan now lists 1204, not 3; review them again');
    expect(gets(`l/peter/backups/targets/${T1}/plan`)).toHaveLength(2);
    expect(text(d)).toContain('1,204 files (1.1 MiB) on the target are no longer in the library');
    expect(input().value).toBe('');
    expect(document.activeElement).toBe(input());
    // The prompt uses the same digit grouping as the text, and either form is accepted (L3).
    expect(text(d.querySelector('label.field span'))).toBe('Type 1,204 to confirm');
    type(input(), '1,204');
    await micro();
    button(d, 'Prune 1,204 files')!.click();
    await settle();
    expect(jobBodies()[1]).toEqual({ kind: 'prune', target: T1, confirm: true, expected: 1204 });
    expect(dialog()).toBeNull();
    // The job runs: focus is on its Cancel (L6).
    expect(document.activeElement).toBe(button(card('Red WD drive').querySelector('.bprogress')!, 'Cancel'));
    server.finish(1, { result: { removed: 1204, bytes: 1204 * 1000 } });
    await settle();
    expect(text(card('Red WD drive').querySelector('.outcome'))).toBe('Prune finished: removed 1,204 files (1.1 MiB).');
  });

  it('accepts the count typed without grouping too', async () => {
    server.backup.plans[T1]!.extra = { count: 1204, bytes: 0, paths: [] };
    await start('#/l/peter/backups');
    button(card('Red WD drive'), 'Prune…')!.click();
    await settle();
    type(dialog()!.querySelector<HTMLInputElement>('input')!, '1204');
    await micro();
    expect(button(dialog()!, 'Prune 1,204 files')!.disabled).toBe(false);
  });

  it('shows what a restore would bring back first', async () => {
    await start('#/l/peter/backups');
    button(card('Red WD drive'), 'Restore missing…')!.click();
    await settle();
    const d = dialog()!;
    // The plan's restore class: set rows missing from the library, not the previous/ versions of `extra` (M4).
    expect(text(d)).toContain('2 files (2.0 KiB) of the backup are missing from the library and would be copied back');
    expect([...d.querySelectorAll('.paths li')].map(text)).toEqual(['image/2023/01/x.jpg', 'image/2023/01/y.jpg']);
    button(d, 'Restore 2 files')!.click();
    await settle();
    expect(jobBodies()).toEqual([{ kind: 'restore', target: T1, mode: 'missing' }]);
    expect(dialog()).toBeNull();
    server.finish(1, { result: { restored: 2, bytes: 2000, present: 6 } });
    await settle();
    expect(text(card('Red WD drive').querySelector('.outcome'))).toBe('Restore finished: restored 2 files (2.0 KiB).');

    // Nothing missing: no Restore button.
    server.backup.plans[T1]!.restore = { count: 0, bytes: 0, paths: [] };
    button(card('Red WD drive'), 'Restore missing…')!.click();
    await settle();
    expect(text(dialog())).toContain('Nothing is missing');
    expect(dialog()!.querySelectorAll('button.primary')).toHaveLength(0);
    button(dialog()!, 'Close')!.click();
    await settle();

    // A server without the restore class: the preview has no count rather than the extras' one.
    server.backup.planRestore = false;
    button(card('Red WD drive'), 'Restore missing…')!.click();
    await settle();
    expect(text(dialog())).toContain('The files of the backup that are missing from the library are copied back');
    expect(text(dialog())).not.toContain('3 files');
    expect(dialog()!.querySelector('.paths')).toBeNull();
    button(dialog()!, 'Restore missing files')!.click();
    await settle();
    expect(jobBodies()[1]).toEqual({ kind: 'restore', target: T1, mode: 'missing' });
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
    // As the server: a new target has an empty set and a plan.
    expect(card('Spare').querySelector('.headline')?.textContent).toBe('8 files, 7.8 KiB not backed up yet');
    expect(text(card('Spare').querySelector('.facts'))).toContain('On the target 0 files, 0 B');
    expect([...document.querySelectorAll('.toast .text')].map((t) => t.textContent)).toContain('Added Spare');

    // A typed network share is sent as is; a conflict is shown in the dialog.
    button(document.querySelector('.head')!, 'Add target')!.click();
    await settle();
    type(path(), '/mnt/taken');
    type(dialog()!.querySelectorAll<HTMLInputElement>('input')[1]!, 'Taken');
    await micro();
    submit().click();
    await settle();
    expect(dialog()!.querySelector('[role="alert"]')?.textContent).toBe('the target already holds a backup of another library under this name; choose another set name');
    // The server takes 1-64 characters (L4).
    expect(dialog()!.querySelectorAll<HTMLInputElement>('input')[1]!.maxLength).toBe(64);
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
    // The Edit button is gone: focus is on the first field (L6), which takes 1-64 characters (L4).
    const labelField = form.querySelector<HTMLInputElement>('input')!;
    expect(document.activeElement).toBe(labelField);
    expect(labelField.maxLength).toBe(64);
    const folder = form.querySelector<HTMLInputElement>('input[aria-label="Target folder"]')!;
    expect(folder.value).toBe('\\\\nas\\photos');
    // Browse cannot open a share: the picker starts at the roots (L9).
    button(form, 'Browse…')!.click();
    await settle();
    expect(server.calls.filter((c) => c.path.startsWith('/api/fs')).map((c) => c.path)).toEqual(['/api/fs?path=']);
    button(dialog()!, 'Cancel')!.click();
    await settle();
    type(folder, '/mnt/wrong');
    await micro();
    button(form, 'Save')!.click();
    await settle();
    expect(form.querySelector('[role="alert"]')?.textContent).toBe('the folder is another backup target (its marker has another id)');
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
    // The form closed: focus is back on Edit (L6).
    expect(document.activeElement).toBe(button(card('Basement NAS'), 'Edit'));

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
    expect(document.querySelector('.dest .status')?.classList.contains('tone-muted')).toBe(true);
  });

  it('shows the backup status line on the Library page, with the 30-day warning', async () => {
    await start('#/l/peter/library');
    const line = () => document.querySelector('p.backup');
    expect(text(line())).toBe('Backed up 2 hours ago to Red WD drive; 3 behind · Backups');
    expect(line()?.querySelector('a')?.getAttribute('href')).toBe('#/l/peter/backups');
    expect(line()?.querySelector('.tone-ok')).not.toBeNull();

    for (const t of server.backup.targets) t.lastBackupAt = unix('2026-09-01T10:00:00');
    await go('#/l/peter/');
    await go('#/l/peter/library');
    expect(text(line())).toBe('⚠ Backed up last month to Red WD drive; 3 behind (older than 30 days) · Backups');
    expect(line()?.querySelector('.tone-warn')).not.toBeNull();

    server.backup.targets.length = 0;
    await go('#/l/peter/');
    await go('#/l/peter/library');
    expect(text(line())).toBe('⚠ No backup target yet · Add a backup target');
    // A muted warning (L5).
    expect(line()?.querySelector('.tone-muted')).not.toBeNull();
  });

  it('shows the run stats of backup jobs in the Library page job table (M7, H1)', async () => {
    server.backup.jobs.push(
      backupJobJSON({ id: 2, kind: 'scrub', target: T1, state: 'failed', exitCode: 1, error: 'the scrub found 1 damaged files (1 repaired); see the job log', result: runResult('scrub', { checked: 5, damaged: [{ path: 'a.jpg', repaired: true }], damagedCount: 1, repaired: 1 }) }),
      backupJobJSON({ id: 1, kind: 'backup', target: T1, state: 'done', result: runResult('backup', { new: 6, copied: 6, bytes: 32127 }), progress: progress({ phase: 'snapshot', totalFiles: 2 }) }),
    );
    await start('#/l/peter/library');
    const rows = [...document.querySelectorAll('table.data tbody tr')].map((r) => [...r.querySelectorAll('td')].map(text));
    expect(rows).toHaveLength(2);
    expect(rows[0]?.[1]).toBe('Scrub');
    expect(rows[0]?.[4]).toBe('checked 5 files, 1 damaged, 1 repaired');
    expect(rows[0]?.[5]).toBe('The scrub found 1 damaged files (1 repaired); see the job log (/media/peter/.picmover/logs/scrub_2026-10-10_12-00-00.log)');
    expect(rows[1]?.[1]).toBe('Backup');
    expect(rows[1]?.[4]).toBe('copied 6 files (31.4 KiB)');
    expect(rows[1]?.[5]).toBe('Backup finished: copied 6 files (31.4 KiB).');
    expect(text(document.querySelector('table.data'))).not.toContain('imported');
  });

  it('counts down while a backup runs and reads the set counts again (issue #22)', async () => {
    await start('#/l/peter/backups');
    expect(card('Red WD drive').querySelector('.headline')?.textContent).toBe('2 files, 2.0 KiB not backed up yet; 1 to move');
    button(card('Red WD drive'), 'Back up now')!.click();
    await settle();
    const es = FakeEventSource.instances.at(-1)!;
    es.emit('progress', { phase: 'copy', files: 1, bytes: 1 * MiB, totalFiles: 4, totalBytes: 25 * MiB, current: 'image/2024/07/1.jpg', failed: 0 });
    await micro();
    expect(card('Red WD drive').querySelector('.headline')?.textContent).toBe('3 files, 24.0 MiB not backed up yet');
    es.emit('progress', { phase: 'copy', files: 3, bytes: 20 * MiB, totalFiles: 4, totalBytes: 25 * MiB, current: 'image/2024/07/2.jpg', failed: 0 });
    await micro();
    expect(card('Red WD drive').querySelector('.headline')?.textContent).toBe('1 file, 5.0 MiB not backed up yet');
    // The set's counts are read again while the job runs (the server does not plan meanwhile).
    const loads = gets('l/peter/backups').length;
    server.backup.targets[0]!.set!.files = 11;
    await vi.advanceTimersByTimeAsync(15_000);
    await micro();
    expect(gets('l/peter/backups').length).toBeGreaterThan(loads);
    expect(gets('l/peter/backups?plan=1')).toHaveLength(0);
    expect(text(card('Red WD drive').querySelector('.facts'))).toContain('On the target 11 files');
    server.finish(1, { result: { new: 4, copied: 4, bytes: 25 * MiB } });
    await settle();
    expect(card('Red WD drive').querySelector('.headline')?.textContent).toBe('2 files, 2.0 KiB not backed up yet; 1 to move');
    // No more reloads once it has finished.
    const after = gets('l/peter/backups').length + gets('l/peter/backups?plan=1').length;
    await vi.advanceTimersByTimeAsync(30_000);
    expect(gets('l/peter/backups').length + gets('l/peter/backups?plan=1').length).toBe(after);
  });

  it('shows a set without a finished backup as incomplete, with the real cause of the stop (issue #22)', async () => {
    const busy = 'the backup database on the target stayed busy (another program or page kept it open for reading); the files copied so far are kept, and Back up now continues from there';
    server.backup.targets.splice(1, 1);
    const red = server.backup.targets[0]!;
    red.lastBackupAt = null;
    red.set = { name: 'peter', files: 98543, bytes: 546 * 1024 ** 3, extras: 0, damaged: 0, unverified: 98543, libraryFiles: 193158 };
    red.lastAttempt = { at: unix('2026-10-10T07:26:00'), error: busy, setFiles: 98543, libraryFiles: 193158 };
    // The run could not be recorded on the target: backup.db has none.
    server.backup.runs[T1] = [];
    await start('#/l/peter/backups');
    const c = card('Red WD drive');
    expect(text(c.querySelector('.incomplete'))).toBe('Incomplete: 98,543 of 193,158 files backed up. Continue with Back up now.');
    expect(text(c.querySelector('.facts'))).toContain('Last backup never finished');
    expect(text(c.querySelector('.facts'))).not.toContain('Never backed up');
    const problems = c.querySelector<HTMLDetailsElement>('details.problems')!;
    expect(problems.open).toBe(true);
    expect(text(problems.querySelector('summary'))).toBe('Problems 1');
    expect(text(problems.querySelector('.stopped'))).toBe(`The last backup did not finish The last backup stopped: ${busy}.`);
    expect(document.querySelector('.dest .status')?.textContent).toBe('Incomplete: 98,543 of 193,158 files backed up to Red WD drive; the last backup to Red WD drive failed');
  });

  it('lists a backup that just failed under problems, never "No problems" (issue #22)', async () => {
    server.backup.runs[T1] = [];
    await start('#/l/peter/backups');
    expect(text(card('Red WD drive').querySelector('details.problems summary'))).toBe('No problems in the last runs');
    button(card('Red WD drive'), 'Back up now')!.click();
    await settle();
    const msg = 'the backup database on the target stayed busy (another program or page kept it open for reading)';
    server.finish(1, { exitCode: 2, error: msg });
    await settle();
    const c = card('Red WD drive');
    expect(text(c.querySelector('.outcome'))).toBe(`Backup failed: ${msg}`);
    expect(text(c.querySelector('details.problems summary'))).toBe('Problems 1');
    expect(text(c.querySelector('details.problems .stopped p'))).toBe(`The last backup stopped: ${msg}.`);
    expect(text(c)).not.toContain('No problems');
  });

  it('reports a backup killed after the last recorded attempt (issue #22 review)', async () => {
    const red = server.backup.targets[0]!;
    red.lastAttempt = { at: unix('2026-10-10T10:00:00'), setFiles: 8, libraryFiles: 8 };
    // The next backup was killed: no attempt, and a runs row without an end.
    server.backup.runs[T1] = [{ id: 4, kind: 'backup', startedAt: unix('2026-10-10T11:00:00'), finishedAt: null, stats: runResult('backup', {}) }];
    await start('#/l/peter/backups');
    const c = card('Red WD drive');
    expect(text(c.querySelector('details.problems summary'))).toBe('Problems 1');
    expect(text(c.querySelector('details.problems .stopped p'))).toContain('The last backup was interrupted');
    expect(text(c)).not.toContain('No problems');
  });

  it('explains the controls and the facts in tooltips', async () => {
    await start('#/l/peter/backups');
    const c = card('Red WD drive');
    const tip = (label: string) => button(c, label)?.title ?? '';
    expect(tip('Forget')).toBe("Remove this target from the library's list. Nothing on the drive is deleted; add the same folder again to continue.");
    expect(tip('Scrub')).toContain('Re-read the backup and compare each file with its checksum, to find damaged or missing copies.');
    for (const b of ['Back up now', 'Prune…', 'Restore missing…', 'Edit']) expect(tip(b)).not.toBe('');
    expect(c.querySelector<HTMLSelectElement>('select[aria-label="Scrub budget"]')?.title).toContain('continues where it stopped');
    const dt = (name: string) => [...c.querySelectorAll<HTMLElement>('.facts dt')].find((d) => d.textContent === name);
    expect(dt('Not scrubbed yet')?.title).toContain('checked by reading it back when it was written');
    expect(dt('On the target')?.title).toContain("backup.db");
    const head = document.querySelector('.head')!;
    expect(button(head, 'Check now')?.title).toContain('Compare the library with each target again');
    expect(button(head, 'Add target')?.title).toContain('Add a folder');
  });

  it("links the Import page's notice about a running backup to Backups (L10)", async () => {
    server.backup.jobs.push(backupJobJSON({ id: 3, kind: 'scrub', target: T1 }));
    await start('#/l/peter/import');
    const notice = document.querySelector('.notice')!;
    expect(text(notice)).toContain('A scrub job is running');
    expect(notice.querySelector('a')?.getAttribute('href')).toBe('#/l/peter/backups');
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

  it('shows the listing summary per card, "No backup target yet" without one, nothing in state error', async () => {
    // As the server: no `backup` for a library without targets.
    delete server.libs.find((l) => l.id === 'penny')!.backup;
    server.libs.push({ id: 'family', name: 'family', path: '/media/family', state: 'closed', summary: { total: 1, images: 1, raw: 0, videos: 0, withGps: 0, deleted: 0, bytes: 1, earliest: null, latest: null } });
    await start('#/');
    const libcard = (name: string) => [...document.querySelectorAll<HTMLElement>('.libcard')].find((c) => c.querySelector('.name')?.textContent === name)!;
    const peter = libcard('peter').querySelector('.backup');
    expect(text(peter)).toBe('Backed up 2 hours ago to Red WD drive; 3 behind');
    expect(peter?.classList.contains('tone-ok')).toBe(true);
    const family = libcard('family').querySelector('.backup');
    expect(text(family)).toBe('⚠ No backup target yet');
    expect(family?.classList.contains('tone-muted')).toBe(true);
    expect(text(libcard('penny').querySelector('.backup'))).toBe('⚠ No backup target yet');
    expect(libcard('broken').querySelector('.backup')).toBeNull();
    expect(libcard('scans').querySelector('.backup')).toBeNull();
    // The listing is enough: no library-scoped backup requests from this page.
    expect(server.calls.some((c) => c.path.includes('/backups'))).toBe(false);
  });

  it('shows an incomplete backup in the listing, not "Never backed up" (issue #22)', async () => {
    server.libs.find((l) => l.id === 'peter')!.backup = {
      lastBackupAt: null,
      targets: 1,
      incomplete: { label: 'Red WD drive', files: 98543, libraryFiles: 193158 },
      failure: { label: 'Red WD drive', at: unix('2026-10-10T07:26:00'), error: 'the backup database on the target stayed busy' },
    };
    await start('#/');
    const peter = [...document.querySelectorAll<HTMLElement>('.libcard')].find((c) => c.querySelector('.name')?.textContent === 'peter')!.querySelector('.backup');
    expect(text(peter)).toBe('⚠ Incomplete: 98,543 of 193,158 files backed up to Red WD drive; the last backup to Red WD drive failed');
    expect(peter?.classList.contains('tone-warn')).toBe(true);
  });

  it('warns about a stale backup in the listing', async () => {
    await start('#/');
    const penny = [...document.querySelectorAll<HTMLElement>('.libcard')].find((c) => c.querySelector('.name')?.textContent === 'penny')!.querySelector('.backup');
    expect(text(penny)).toBe('⚠ Backed up 2 months ago; 5 behind (older than 30 days)');
    expect(penny?.classList.contains('tone-warn')).toBe(true);
    expect(server.calls.some((c) => c.path.includes('/backups'))).toBe(false);
  });
});
