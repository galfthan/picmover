import { describe, expect, it } from 'vitest';
import type { JobResult } from './api';
import { filterResults, kindCounts, mergeResults, statusCounts } from './results';
import { DISCONNECTED_MESSAGE, kindSplit, outcome } from './jobtext';
import { importCounts, outcomeLabel } from './imports';
import { ZERO_STATS } from '../testing/fakes';

const r = (seq: number, status: string, kind?: string): JobResult => ({ seq, status, source: `/s/${seq}`, kind });

describe('results', () => {
  it('merges the kept results and errors newest first, once each', () => {
    const merged = mergeResults([r(1, 'imported'), r(3, 'duplicate')], [r(2, 'error'), r(3, 'duplicate'), r(4, 'error')]);
    expect(merged.map((x) => x.seq)).toEqual([4, 3, 2, 1]);
  });

  it('filters and counts by outcome and kind', () => {
    const list = [r(1, 'imported', 'image'), r(2, 'error', 'video'), r(3, 'imported', 'video'), r(4, 'error')];
    expect(filterResults(list, 'error', 'all').map((x) => x.seq)).toEqual([2, 4]);
    expect(filterResults(list, 'all', 'video').map((x) => x.seq)).toEqual([2, 3]);
    expect(filterResults(list, 'imported', 'video').map((x) => x.seq)).toEqual([3]);
    expect(statusCounts(list)).toEqual({ imported: 2, error: 2 });
    expect(kindCounts(list)).toEqual({ image: 1, video: 2 });
  });
});

describe('import counts by kind', () => {
  it('words the split', () => {
    expect(kindSplit({ importedPhotos: 1000, importedRaw: 2, importedVideos: 1 })).toBe('1,000 photos, 2 RAW, 1 video');
    expect(kindSplit({ importedPhotos: 1, importedRaw: 0, importedVideos: 0 })).toBe('1 photo');
    expect(kindSplit({ importedPhotos: 0, importedRaw: 0, importedVideos: 0 })).toBe('');
  });

  it('shows the split of a record that has one, the total otherwise', () => {
    const base = { imported: 12, existing: 3, duplicates: 0, skipped: 0, errors: 1 };
    expect(importCounts({ ...base, importedPhotos: 10, importedRaw: 0, importedVideos: 2 })).toBe('12 new (10 photos, 2 videos), 3 already there, 1 error');
    expect(importCounts({ ...base, importedPhotos: null, importedRaw: null, importedVideos: null })).toBe('12 new, 3 already there, 1 error');
    expect(importCounts(base)).toBe('12 new, 3 already there, 1 error');
  });

  it('labels a disconnected record and job', () => {
    expect(outcomeLabel('disconnected')).toBe('Source disconnected');
    const job = {
      id: 1,
      kind: 'import',
      state: 'done',
      startedAt: null,
      finishedAt: null,
      stats: { ...ZERO_STATS, imported: 4, errors: 0, sourceDisconnected: true },
      error: DISCONNECTED_MESSAGE,
      exitCode: 2,
    };
    expect(outcome(job)).toEqual({ tone: 'warn', text: DISCONNECTED_MESSAGE });
  });
});
