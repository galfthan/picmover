import { describe, expect, it } from 'vitest';
import { formatRecent, importCounts, importLine, importTitle, modeLabel, outcomeLabel } from './imports';

describe('import record wording', () => {
  it('titles a record by name, else source name, else id', () => {
    expect(importTitle({ id: 3, name: ' Summer ', sourceName: 'DCIM' })).toBe('Summer');
    expect(importTitle({ id: 3, name: '', sourceName: 'DCIM' })).toBe('DCIM');
    expect(importTitle({ id: 3, name: '  ', sourceName: '' })).toBe('Import #3');
    expect(importTitle({ id: 3, name: '' })).toBe('Import #3');
  });

  it('labels modes and only the outcomes worth showing', () => {
    expect(modeLabel('copy')).toBe('Copy');
    expect(modeLabel('rebuild')).toBe('Rebuild');
    expect(modeLabel('other')).toBe('other');
    expect(modeLabel('')).toBe('Import');
    expect(outcomeLabel('done')).toBeNull();
    expect(outcomeLabel('cancelled')).toBe('Cancelled');
    expect(outcomeLabel('running')).toBe('Running');
  });

  it('summarises the counts, leaving zeros out', () => {
    expect(importCounts({ imported: 12, existing: 3, duplicates: 0, skipped: 0, errors: 1 })).toBe('12 new, 3 already there, 1 error');
    expect(importCounts({ imported: 1200, existing: 0, duplicates: 1, skipped: 4, errors: 2 })).toBe('1,200 new, 1 duplicate, 4 skipped, 2 errors');
    expect(importCounts({ imported: 0, existing: 0, duplicates: 0, skipped: 0, errors: 0 })).toBe('Nothing imported');
  });

  it('dates relative under a week, else the local date', () => {
    const now = Date.parse('2026-10-10T12:00:00Z');
    expect(formatRecent('2026-10-10T09:00:00Z', now, 'en')).toBe('3 hours ago');
    expect(formatRecent('2026-10-09T12:00:00Z', now, 'en')).toBe('yesterday');
    expect(formatRecent('2026-09-01T12:00:00Z', now, 'en')).toBe('Sep 1, 2026');
    expect(formatRecent(null, now)).toBe('');
    expect(formatRecent('garbage', now)).toBe('');
  });

  it('writes the detail line', () => {
    expect(importLine({ id: 1, name: '', startedAt: null, mode: 'copy', sourceName: 'DCIM' }, '9 Oct 2026')).toBe('Imported 9 Oct 2026 from DCIM');
    expect(importLine({ id: 1, name: 'Trip', startedAt: null, mode: 'move' }, '9 Oct 2026')).toBe('Imported 9 Oct 2026 · Trip');
    expect(importLine({ id: 1, name: '', startedAt: null, mode: 'rebuild' }, '')).toBe('Recorded by a rebuild');
  });
});
