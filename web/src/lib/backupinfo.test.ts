// Backup wording and math: the "behind" headline, the status line with the
// 30-day warning (from targets and from the workspace listing), run
// problems, rate and ETA, the target path check, and backup job outcomes.

import { describe, expect, it } from 'vitest';
import type { BackupRun, BackupTarget, Job } from './api';
import {
  RateMeter,
  behindText,
  etaMs,
  etaText,
  formatRate,
  phaseLabel,
  problemCount,
  progressFraction,
  runProblems,
  runSummary,
  statusLine,
  summaryFromListing,
  summaryFromTargets,
  targetPathProblem,
} from './backupinfo';
import { outcome } from './jobtext';

const NOW = new Date('2026-10-10T12:00:00Z').getTime();
const sec = (iso: string) => Math.floor(new Date(iso).getTime() / 1000);

function target(p: Partial<BackupTarget> = {}): BackupTarget {
  return {
    id: 'a1b2c3d4e5f60718',
    label: 'Red WD drive',
    path: '/mnt/red',
    reachable: true,
    lastBackupAt: sec('2026-10-10T10:00:00Z'),
    lastScrubAt: null,
    set: { files: 10, bytes: 1000, behind: { new: 1000, changed: 200, moved: 4, bytes: 3.2 * 1024 ** 3 }, extras: 0, damaged: 0, unverified: 0 },
    ...p,
  };
}

describe('behind headline', () => {
  it('counts new, changed and moved files with the bytes', () => {
    expect(behindText(target())).toBe('1,204 files, 3.2 GiB not backed up yet');
  });
  it('says up to date, not checked, not reachable, nothing yet', () => {
    expect(behindText(target({ set: { files: 1, bytes: 1, behind: { new: 0, changed: 0, moved: 0, bytes: 0 }, extras: 0, damaged: 0, unverified: 0 } }))).toBe('Up to date');
    expect(behindText(target({ set: { files: 1, bytes: 1, behind: null, extras: 0, damaged: 0, unverified: 0 } }))).toBe('Not checked yet');
    expect(behindText(target({ reachable: false }))).toBe('Not reachable');
    expect(behindText(target({ set: null }))).toBe('Nothing backed up here yet');
    expect(behindText(target({ set: { files: 0, bytes: 0, behind: { new: 1, changed: 0, moved: 0, bytes: 10 }, extras: 0, damaged: 0, unverified: 0 } }))).toBe(
      '1 file, 10 B not backed up yet',
    );
  });
});

describe('status line', () => {
  it('names the newest backup, its target and how far behind it is', () => {
    const older = target({ id: '0f1e2d3c4b5a6978', label: 'NAS', lastBackupAt: sec('2026-09-01T10:00:00Z') });
    const s = summaryFromTargets([older, target()]);
    expect(s).toMatchObject({ targets: 2, label: 'Red WD drive', behind: 1204 });
    expect(statusLine(s, NOW)).toEqual({ tone: 'ok', stale: false, text: 'Backed up 2 hours ago to Red WD drive; 1,204 behind' });
  });
  it('warns without a target, before the first backup and after 30 days', () => {
    expect(statusLine(summaryFromTargets([]), NOW)).toEqual({ tone: 'warn', stale: true, text: 'No backup target yet' });
    expect(statusLine(summaryFromTargets([target({ lastBackupAt: null })]), NOW)).toEqual({ tone: 'warn', stale: true, text: 'Never backed up (1 target)' });
    const old = statusLine(summaryFromTargets([target({ lastBackupAt: sec('2026-09-09T11:00:00Z') })]), NOW);
    expect(old.stale).toBe(true);
    expect(old.tone).toBe('warn');
    expect(old.text).toBe('Backed up last month to Red WD drive; 1,204 behind (older than 30 days)');
    // 29 days is not stale yet.
    expect(statusLine(summaryFromTargets([target({ lastBackupAt: sec('2026-09-11T12:00:00Z') })]), NOW).stale).toBe(false);
  });
  it('reads every time form the server may send', () => {
    for (const at of [sec('2026-10-10T10:00:00Z'), '2026-10-10T10:00:00Z', '2026-10-10T13:00:00+03:00']) {
      expect(statusLine(summaryFromTargets([target({ lastBackupAt: at })]), NOW).text).toContain('2 hours ago');
    }
  });
  it('reads the listing summary and shows nothing without one', () => {
    expect(summaryFromListing(undefined)).toBeNull();
    expect(summaryFromListing(null)).toBeNull();
    const s = summaryFromListing({ lastBackupAt: sec('2026-10-10T11:00:00Z'), behind: 0, targets: 1 });
    expect(s && statusLine(s, NOW).text).toBe('Backed up 1 hour ago; 0 behind');
    const o = summaryFromListing({ lastBackupAt: '2026-10-10T11:00:00Z', behind: { new: 2, changed: 1, moved: 0, bytes: 9 }, targets: [{}, {}], label: 'NAS' });
    expect(o).toMatchObject({ targets: 2, behind: 3, label: 'NAS' });
    expect(statusLine(summaryFromListing({ targets: 0 })!, NOW).text).toBe('No backup target yet');
    expect(statusLine(summaryFromListing({ targets: 1, lastBackupAt: null })!, NOW).text).toBe('Never backed up (1 target)');
  });
});

describe('run problems', () => {
  const runs: BackupRun[] = [
    { id: 5, kind: 'prune', startedAt: 1, finishedAt: 2, stats: { removed: 3 } },
    {
      id: 4,
      kind: 'scrub',
      startedAt: 1,
      finishedAt: 2,
      stats: { damaged: [{ path: 'a.jpg' }, { path: 'b.jpg', error: 'no good copy' }], repaired: ['a.jpg'], unknown: ['x.txt', 'y.txt'], missing: 1 },
    },
    { id: 3, kind: 'backup', startedAt: 1, finishedAt: 2, stats: { failed: [{ path: 'c.jpg', error: 'EIO' }] } },
    { id: 2, kind: 'scrub', startedAt: 1, finishedAt: 2, stats: { damaged: [{ path: 'old.jpg' }] } },
  ];
  it('takes the newest scrub and backup', () => {
    const p = runProblems(runs);
    expect(p.damaged.items).toEqual([
      { path: 'a.jpg', detail: '', repaired: true },
      { path: 'b.jpg', detail: 'no good copy', repaired: false },
    ]);
    expect(p.unknown).toEqual({ count: 2, items: [{ path: 'x.txt', detail: '' }, { path: 'y.txt', detail: '' }] });
    expect(p.missing).toEqual({ count: 1, items: [] });
    expect(p.failed.items).toEqual([{ path: 'c.jpg', detail: 'EIO' }]);
    expect(problemCount(p)).toBe(6);
  });
  it('keeps a repaired flag sent with the item, and is empty without runs', () => {
    const p = runProblems([{ id: 1, kind: 'scrub', startedAt: 1, finishedAt: 2, stats: { damaged: [{ path: 'a', repaired: false }], repaired: ['a'] } }]);
    expect(p.damaged.items[0]?.repaired).toBe(false);
    expect(problemCount(runProblems([]))).toBe(0);
  });
  it('summarizes runs in words', () => {
    expect(runSummary('backup', { copied: 12, bytes: 2048, moved: 3, failed: [{}] })).toBe('copied 12 files (2.0 KiB), moved 3, 1 failed');
    expect(runSummary('backup', {})).toBe('nothing to copy');
    expect(runSummary('scrub', { checked: 1, damaged: 2, repaired: 1 })).toBe('checked 1 file, 2 damaged, 1 repaired');
    expect(runSummary('prune', { removed: 3 })).toBe('removed 3 files');
    expect(runSummary('restore', { restored: 2, failed: 1 })).toBe('restored 2 files, 1 failed');
  });
});

describe('rate and ETA', () => {
  it('measures over the samples and restarts when the count drops', () => {
    const m = new RateMeter(10_000);
    m.add(0, 0);
    expect(m.rate()).toBeNull(); // one sample
    m.add(500, 1000);
    expect(m.rate()).toBeNull(); // under a second
    m.add(2000, 4 * 1024 * 1024);
    expect(m.rate()).toBe(2 * 1024 * 1024);
    // The window slides: old samples drop out.
    m.add(20_000, 4 * 1024 * 1024 + 10 * 1024 * 1024);
    m.add(22_000, 4 * 1024 * 1024 + 14 * 1024 * 1024);
    expect(m.rate()).toBe(2 * 1024 * 1024);
    m.add(23_000, 10);
    expect(m.rate()).toBeNull();
  });
  it('turns the rate into time left', () => {
    expect(etaMs(5, 25, 2)).toBe(10_000);
    expect(etaMs(25, 25, 2)).toBe(0);
    expect(etaMs(5, 25, 0)).toBeNull();
    expect(etaMs(5, 0, 2)).toBeNull();
    expect(etaMs(5, 25, null)).toBeNull();
    expect(etaText(10_000)).toBe('about 10s left');
    expect(etaText(185_000)).toBe('about 3m 05s left');
    expect(etaText(0)).toBe('finishing');
    expect(etaText(null)).toBe('');
    expect(formatRate(2 * 1024 * 1024)).toBe('2.0 MiB/s');
  });
  it('tells the fraction by bytes, else by files', () => {
    expect(progressFraction({ bytes: 25, totalBytes: 100, files: 9, totalFiles: 10 })).toBe(0.25);
    expect(progressFraction({ files: 3, totalFiles: 4 })).toBe(0.75);
    expect(progressFraction({ files: 3 })).toBeNull();
    expect(progressFraction(null)).toBeNull();
    expect(phaseLabel('backup', 'copy')).toBe('Copying');
    expect(phaseLabel('scrub', 'scrub')).toBe('Checking files');
    expect(phaseLabel('backup', undefined)).toBe('Starting');
  });
});

describe('target paths', () => {
  it('accepts absolute POSIX, drive and UNC paths', () => {
    for (const p of ['/media/backup', 'E:\\Backup', 'e:/backup', '\\\\nas\\photos', '//nas/photos', '  /mnt/x  ']) expect(targetPathProblem(p)).toBeNull();
  });
  it('refuses empty, relative, device and NUL paths', () => {
    expect(targetPathProblem(' ')).toBe('Enter a folder path.');
    expect(targetPathProblem('backup')).toMatch(/absolute/);
    expect(targetPathProblem('E:backup')).toMatch(/absolute/);
    expect(targetPathProblem('\\\\nas')).toMatch(/absolute/);
    expect(targetPathProblem('\\\\?\\C:\\x')).toMatch(/Device/);
    expect(targetPathProblem('\\\\.\\PhysicalDrive0')).toMatch(/Device/);
    expect(targetPathProblem('/mnt/\u0000x')).toMatch(/NUL/);
  });
});

describe('backup job outcomes', () => {
  const base = { id: 1, startedAt: null, finishedAt: null, error: null, logPath: '/lib/.picmover/logs/backup.log' };
  const j = (p: Omit<Partial<Job>, 'stats'> & { stats?: Record<string, unknown> }): Job => ({ ...base, kind: 'backup', state: 'done', ...p, stats: (p.stats ?? {}) as unknown as Job['stats'] });
  it('uses the backup exit codes', () => {
    expect(outcome(j({ exitCode: 0, stats: { copied: 2 } }))).toEqual({ tone: 'ok', text: 'Backup finished: copied 2 files.' });
    expect(outcome(j({ exitCode: 1, state: 'failed', stats: { copied: 2, failed: 1 } }))).toEqual({
      tone: 'warn',
      text: 'Backup finished, but some files failed (copied 2 files, 1 failed); see /lib/.picmover/logs/backup.log',
    });
    expect(outcome(j({ kind: 'scrub', exitCode: 1, stats: { checked: 5, damaged: 1 } })).text).toMatch(/^Scrub found damaged files/);
    expect(outcome(j({ exitCode: 2, state: 'failed', error: 'the target is not reachable' }))).toEqual({ tone: 'error', text: 'Backup failed: the target is not reachable' });
    expect(outcome(j({ state: 'cancelled' }))).toEqual({ tone: 'warn', text: 'Backup cancelled.' });
  });
});
