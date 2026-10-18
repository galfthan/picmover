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
  problemTotal,
  progressFraction,
  serverEtaMs,
  serverRate,
  runProblems,
  runSummary,
  statusLine,
  summaryFromLib,
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
    set: { name: 'peter', files: 10, bytes: 1000, behind: { new: 1000, changed: 200, moved: 4, bytes: 3.2 * 1024 ** 3 }, extras: 0, damaged: 0, unverified: 0 },
    ...p,
  };
}

describe('behind headline', () => {
  it('counts new and changed files with the bytes, and moves apart', () => {
    expect(behindText(target())).toBe('1,200 files, 3.2 GiB not backed up yet; 4 to move');
  });
  it('says up to date, not checked, not reachable, nothing yet', () => {
    expect(behindText(target({ set: { name: 'peter', files: 1, bytes: 1, behind: { new: 0, changed: 0, moved: 0, bytes: 0 }, extras: 0, damaged: 0, unverified: 0 } }))).toBe('Up to date');
    expect(behindText(target({ set: { name: 'peter', files: 1, bytes: 1, behind: null, extras: 0, damaged: 0, unverified: 0 } }))).toBe('Not checked yet');
    expect(behindText(target({ reachable: false }))).toBe('Not reachable');
    expect(behindText(target({ set: null }))).toBe('Nothing backed up here yet');
    expect(behindText(target({ set: { name: 'peter', files: 0, bytes: 0, behind: { new: 1, changed: 0, moved: 0, bytes: 10 }, extras: 0, damaged: 0, unverified: 0 } }))).toBe(
      '1 file, 10 B not backed up yet',
    );
  });
  it('says a pure rename is a move, not bytes not backed up', () => {
    const b = (behind: { new: number; changed: number; moved: number; bytes: number }) => target({ set: { name: 'peter', files: 5, bytes: 50, behind, extras: 0, damaged: 0, unverified: 0 } });
    expect(behindText(b({ new: 0, changed: 0, moved: 1, bytes: 0 }))).toBe('1 file to move');
    expect(behindText(b({ new: 0, changed: 0, moved: 1204, bytes: 0 }))).toBe('1,204 files to move');
    expect(behindText(b({ new: 2, changed: 0, moved: 1, bytes: 2048 }))).toBe('2 files, 2.0 KiB not backed up yet; 1 to move');
  });
  it('shows the target error as the headline', () => {
    const msg = 'the target already holds a backup of another library under this name';
    expect(behindText(target({ set: null, error: msg }))).toBe(msg);
    // Unreachable wins: the error is about a set that cannot be read then.
    expect(behindText(target({ reachable: false, set: null, error: msg }))).toBe('Not reachable');
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
    expect(statusLine(summaryFromTargets([]), NOW)).toEqual({ tone: 'muted', stale: true, text: 'No backup target yet' });
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
  it('reads a library without `backup` as having no target, except in state error', () => {
    expect(statusLine(summaryFromLib({ state: 'closed' })!, NOW)).toEqual({ tone: 'muted', stale: true, text: 'No backup target yet' });
    expect(statusLine(summaryFromLib({ state: 'busy', backup: null })!, NOW).text).toBe('No backup target yet');
    expect(summaryFromLib({ state: 'error' })).toBeNull();
    expect(summaryFromLib({ state: 'open', backup: { targets: 1, lastBackupAt: sec('2026-10-10T11:00:00Z'), behind: 0, label: 'Red' } })).toMatchObject({ targets: 1, label: 'Red' });
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
    expect(p.unknown).toEqual({ count: 2, open: 2, truncated: false, items: [{ path: 'x.txt', detail: '' }, { path: 'y.txt', detail: '' }] });
    expect(p.missing).toEqual({ count: 1, open: 1, truncated: false, items: [] });
    expect(p.failed.items).toEqual([{ path: 'c.jpg', detail: 'EIO' }]);
    // a.jpg was repaired: not an open problem.
    expect(problemCount(p)).toBe(5);
    expect(problemTotal(p)).toBe(6);
  });
  it('keeps a repaired flag sent with the item, and is empty without runs', () => {
    const p = runProblems([{ id: 1, kind: 'scrub', startedAt: 1, finishedAt: 2, stats: { damaged: [{ path: 'a', repaired: false }], repaired: ['a'] } }]);
    expect(p.damaged.items[0]?.repaired).toBe(false);
    expect(problemCount(runProblems([]))).toBe(0);
  });
  it('counts missing files the scrub copied again as repaired (the real scrub stats)', () => {
    // As the server sends them: 1 damaged and 1 missing, both copied again (`repaired` 2).
    const real = {
      checked: 5, bytes: 31991, ok: 4,
      damaged: [{ path: 'image/2018/06/tiny.jpg', repaired: true }], damagedCount: 1, repaired: 2,
      missing: ['image/2018/06/scan.png'], missingCount: 1, unknown: ['unknown.txt'], unknownCount: 1,
      passComplete: true, remaining: 0, cancelled: false, seconds: 0,
    };
    const p = runProblems([{ id: 2, kind: 'scrub', startedAt: 1, finishedAt: 2, stats: real }]);
    expect(p.missing.items).toEqual([{ path: 'image/2018/06/scan.png', detail: '', repaired: true }]);
    expect(p.missing.open).toBe(0);
    expect(p.damaged.open).toBe(0);
    // Only the unknown file is still a problem.
    expect(problemCount(p)).toBe(1);
    // Repaired only the damaged one (no repair for the missing file): it stays open.
    const q = runProblems([{ id: 2, kind: 'scrub', startedAt: 1, finishedAt: 2, stats: { ...real, repaired: 1 } }]);
    expect(q.missing.items[0]?.repaired).toBeUndefined();
    expect(problemCount(q)).toBe(2);
    // Nothing repaired (scrub without repair).
    const r = runProblems([{ id: 2, kind: 'scrub', startedAt: 1, finishedAt: 2, stats: { ...real, damaged: [{ path: 'image/2018/06/tiny.jpg', repaired: false }], repaired: 0 } }]);
    expect(problemCount(r)).toBe(3);
  });
  it('prefers the run counts over capped lists and says when a list was truncated', () => {
    const failed = Array.from({ length: 500 }, (_, i) => ({ path: `f${i}.jpg`, error: 'EIO' }));
    const p = runProblems([{ id: 1, kind: 'backup', startedAt: 1, finishedAt: 2, stats: { copied: 10, failed, failedCount: 3000, truncated: true } }]);
    expect(p.failed.count).toBe(3000);
    expect(p.failed.items).toHaveLength(500);
    expect(p.failed.truncated).toBe(true);
    expect(problemCount(p)).toBe(3000);
    expect(runSummary('backup', { copied: 10, failed, failedCount: 3000, truncated: true })).toBe('copied 10 files, 3,000 failed; list truncated to 500');
    const s = runProblems([{ id: 1, kind: 'scrub', startedAt: 1, finishedAt: 2, stats: { damaged: [], damagedCount: 0, unknown: ['u'], unknownCount: 700, truncated: true } }]);
    expect(s.unknown).toMatchObject({ count: 700, open: 700, truncated: true });
    expect(s.damaged).toMatchObject({ count: 0, truncated: false });
  });
  it('summarizes runs in words', () => {
    expect(runSummary('backup', { copied: 12, bytes: 2048, moved: 3, failed: [{}] })).toBe('copied 12 files (2.0 KiB), moved 3, 1 failed');
    expect(runSummary('backup', {})).toBe('nothing to copy');
    expect(runSummary('scrub', { checked: 1, damaged: 2, repaired: 1 })).toBe('checked 1 file, 2 damaged, 1 repaired');
    expect(runSummary('prune', { removed: 3 })).toBe('removed 3 files');
    expect(runSummary('restore', { restored: 2, failed: 1 })).toBe('restored 2 files, 1 failed');
    // The real result shapes: lists with counts.
    expect(runSummary('scrub', { checked: 5, bytes: 31991, damaged: [{ path: 'a', repaired: true }], damagedCount: 1, repaired: 2, missing: ['b'], missingCount: 1, unknown: ['c'], unknownCount: 1 })).toBe(
      'checked 5 files (31.2 KiB), 1 damaged, 1 missing, 2 repaired, 1 unknown',
    );
    expect(runSummary('restore', { restored: 1, bytes: 10, failed: [], failedCount: 0 })).toBe('restored 1 file (10 B)');
    expect(runSummary('prune', { removed: 2, bytes: 0, failed: [{ path: 'x', error: 'EACCES' }] })).toBe('removed 2 files, 1 failed');
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
  it('takes rate and ETA from the server when sent; -1 is unknown', () => {
    expect(serverRate({ rate: 62983.4 })).toBe(62983.4);
    expect(serverRate({ rate: 0 })).toBeNull();
    expect(serverRate({})).toBeUndefined();
    expect(serverRate(null)).toBeUndefined();
    expect(serverEtaMs({ eta: 12 })).toBe(12_000);
    expect(serverEtaMs({ eta: 0 })).toBe(0);
    expect(serverEtaMs({ eta: -1 })).toBeNull();
    expect(serverEtaMs({})).toBeUndefined();
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
  // As the server sends them: `stats` the importer's zero counters, the run stats in `result`.
  const zero = { scanned: 0, imported: 0, importedPhotos: 0, importedRaw: 0, importedVideos: 0, existing: 0, duplicates: 0, skippedSmall: 0, nonMedia: 0, errors: 0, bytesCopied: 0, rate: 0, thumbsDone: 0, thumbsUnsupported: 0, thumbsFailed: 0, largeDone: 0, sourceDisconnected: false };
  const base = { id: 1, startedAt: null, finishedAt: null, error: null, logPath: '/lib/.picmover/logs/backup.log', stats: zero, library: 'peter', target: 'a1b2c3d4e5f60718' };
  const j = (p: Partial<Job>): Job => ({ ...base, kind: 'backup', state: 'done', ...p });
  it('reads the run stats from `result`, not the importer counters', () => {
    const result = { current: 0, changed: 0, new: 6, extra: 0, copied: 6, moved: 0, adopted: 0, refreshed: 0, failedCount: 0, bytes: 32127, failed: [], cancelled: false, seconds: 0 };
    expect(outcome(j({ exitCode: 0, result }))).toEqual({ tone: 'ok', text: 'Backup finished: copied 6 files (31.4 KiB).' });
    expect(outcome(j({ kind: 'prune', exitCode: 0, result: { removed: 2, bytes: 2048, failed: [], seconds: 0 } })).text).toBe('Prune finished: removed 2 files (2.0 KiB).');
    expect(outcome(j({ kind: 'restore', exitCode: 0, result: { restored: 1, bytes: 10, present: 6, elsewhere: 0, failedCount: 0, failed: [] } })).text).toBe('Restore finished: restored 1 file (10 B).');
    // No result yet (an old server or a replay without it): no made-up numbers.
    expect(outcome(j({ exitCode: 0 })).text).toBe('Backup finished: nothing to copy.');
  });
  it('uses the backup exit codes and the server message for exit 1', () => {
    expect(outcome(j({ kind: 'scrub', exitCode: 1, state: 'failed', error: 'the scrub found 1 damaged and 1 missing files (2 repaired); see the job log', result: { checked: 5, damagedCount: 1, missingCount: 1, repaired: 2 } }))).toEqual({
      tone: 'warn',
      text: 'The scrub found 1 damaged and 1 missing files (2 repaired); see the job log (/lib/.picmover/logs/backup.log)',
    });
    expect(outcome(j({ exitCode: 1, state: 'failed', result: { copied: 2, failed: [{ path: 'a', error: 'EIO' }], failedCount: 1 } }))).toEqual({
      tone: 'warn',
      text: 'Backup finished, but some files failed (copied 2 files, 1 failed); see /lib/.picmover/logs/backup.log',
    });
    expect(outcome(j({ exitCode: 2, state: 'failed', error: 'the target is not reachable' }))).toEqual({ tone: 'error', text: 'Backup failed: the target is not reachable' });
    expect(outcome(j({ state: 'cancelled', exitCode: 130 }))).toEqual({ tone: 'warn', text: 'Backup cancelled.' });
    expect(outcome(j({ state: 'cancelled', exitCode: 130, result: { copied: 3, bytes: 0 } })).text).toBe('Backup cancelled (copied 3 files).');
  });
});
