import { describe, expect, it } from 'vitest';
import { filtersFromQuery, filtersKey, filtersToQuery, hasFilters } from './filters';

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
  });
});
