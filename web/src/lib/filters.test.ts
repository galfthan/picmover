import { describe, expect, it } from 'vitest';
import { describeSize, filtersFromQuery, filtersKey, filtersToQuery, hasFilters } from './filters';

const q = (s: string) => new URLSearchParams(s);

describe('filters in the URL', () => {
  it('read the new parameters', () => {
    expect(filtersFromQuery(q('recur=12-24..12-26&rating=4&tag=9&tag=3&tag=9&album=2&from=2010-01-01&to=2013-01-01'))).toEqual({
      recur: '12-24..12-26',
      rating: 4,
      tags: [3, 9],
      album: 2,
      from: '2010-01-01',
      to: '2013-01-01',
    });
  });
  it('drop malformed values', () => {
    expect(filtersFromQuery(q('recur=02-30&rating=6&tag=x&tag=-1&album=0&from=2023-02-29&to=2024-1-1'))).toEqual({});
    expect(filtersFromQuery(q('rating=0'))).toEqual({});
  });
  it('round-trip, with tag repeated and trashed never in the URL', () => {
    const f = { recur: '06-01..08-31', rating: 2, tags: [5, 1], album: 3, type: 'video' as const, trashed: true };
    const s = filtersToQuery(f).toString();
    expect(s).toBe('type=video&recur=06-01..08-31&rating=2&tag=1&tag=5&album=3');
    expect(filtersFromQuery(q(s))).toEqual({ recur: '06-01..08-31', rating: 2, tags: [1, 5], album: 3, type: 'video' });
  });
  it('read and write months: sorted, unique, malformed dropped', () => {
    expect(filtersFromQuery(q('months=7,1,7,12'))).toEqual({ months: [1, 7, 12] });
    expect(filtersToQuery({ months: [12, 1, 7, 7] }).get('months')).toBe('1,7,12');
    const f = { months: [1, 2, 3], recur: '07-14' };
    expect(filtersFromQuery(filtersToQuery(f))).toEqual(f);
    for (const bad of ['0', '13', 'a', '1,,2', '1,', ',1', '1.5', '-1', '1,99']) {
      expect(filtersFromQuery(q(`months=${bad}&rating=2`))).toEqual({ rating: 2 });
    }
    expect(filtersToQuery({ months: [] }).toString()).toBe('');
    expect(hasFilters({ months: [7] })).toBe(true);
    expect(hasFilters({ months: [] })).toBe(false);
  });
  it('key every filter, including trashed', () => {
    expect(filtersKey({ tags: [2, 1] })).toBe('tag=1&tag=2');
    expect(filtersKey({ recur: '07-14' })).not.toBe(filtersKey({ recur: '07-15' }));
    expect(filtersKey({ trashed: true })).toBe('trashed=1');
    expect(filtersKey({ album: 1 })).not.toBe(filtersKey({ album: 2 }));
  });
  it('do not count what the page implies', () => {
    expect(hasFilters({})).toBe(false);
    expect(hasFilters({ trashed: true })).toBe(false);
    expect(hasFilters({ album: 3 })).toBe(true);
    expect(hasFilters({ album: 3 }, { album: true })).toBe(false);
    expect(hasFilters({ album: 3, rating: 1 }, { album: true })).toBe(true);
    expect(hasFilters({ import: 4 })).toBe(true);
    expect(hasFilters({ import: 4 }, { import: true })).toBe(false);
    expect(hasFilters({ import: 4, album: 3 }, { import: true })).toBe(true);
  });
  it('read and write the import record filter', () => {
    expect(filtersFromQuery(q('import=12&album=3'))).toEqual({ import: 12, album: 3 });
    expect(filtersFromQuery(q('import=0'))).toEqual({});
    expect(filtersFromQuery(q('import=x'))).toEqual({});
    expect(filtersToQuery({ import: 12, session: 4 }).toString()).toBe('session=4&import=12');
    expect(filtersKey({ import: 1 })).not.toBe(filtersKey({ import: 2 }));
  });
});

describe('size filter in the URL', () => {
  it('round-trips minpx and maxpx', () => {
    const s = filtersToQuery({ minPx: 640, maxPx: 2000, type: 'image' }).toString();
    expect(s).toBe('type=image&minpx=640&maxpx=2000');
    expect(filtersFromQuery(q(s))).toEqual({ type: 'image', minPx: 640, maxPx: 2000 });
    expect(filtersFromQuery(q('minpx=2000'))).toEqual({ minPx: 2000 });
    expect(filtersFromQuery(q('maxpx=640'))).toEqual({ maxPx: 640 });
    expect(filtersFromQuery(q('minpx=500&maxpx=500'))).toEqual({ minPx: 500, maxPx: 500 });
  });
  it('drops malformed bounds, and both when min > max', () => {
    expect(filtersFromQuery(q('minpx=5000&maxpx=100'))).toEqual({});
    expect(filtersFromQuery(q('minpx=0&maxpx=1000001'))).toEqual({});
    expect(filtersFromQuery(q('minpx=abc&maxpx=-5'))).toEqual({});
    expect(filtersFromQuery(q('minpx=1.5&maxpx=1000000'))).toEqual({ maxPx: 1000000 });
  });
  it('count as filters and key distinctly', () => {
    expect(hasFilters({ minPx: 2000 })).toBe(true);
    expect(hasFilters({ maxPx: 640 })).toBe(true);
    expect(filtersKey({ minPx: 640 })).not.toBe(filtersKey({ maxPx: 640 }));
  });
  it('describe the bounds', () => {
    expect(describeSize()).toBe('Any size');
    expect(describeSize(2000)).toBe('≥ 2000 px');
    expect(describeSize(undefined, 640)).toBe('≤ 640 px');
    expect(describeSize(640, 2000)).toBe('640–2000 px');
  });
});
