import { describe, expect, it } from 'vitest';
import { cleanFilters, countFilters, filterChips, filtersFromQuery, filtersQuery, fromMonth, monthEnd, monthStart, tapYear, toMonth, wholeYears, type FilterScope } from './filters';

const all: FilterScope = { rate: true, location: true, library: true };
const guest: FilterScope = { rate: false, location: false, library: false };
const q = (s: string) => new URLSearchParams(s);

describe('share filters', () => {
  it('reads every filter the share may use from the route', () => {
    const query = q('type=image_raw&from=2020-01-01&to=2021-01-01&recur=12-24..12-26&months=7,1,7&rating=3&album=2&camera=EOS+R5&gps=1&tag=5&tag=2&tag=5&minpx=100&maxpx=4000');
    expect(filtersFromQuery(query, all)).toEqual({
      type: 'image_raw',
      from: '2020-01-01',
      to: '2021-01-01',
      recur: '12-24..12-26',
      months: [1, 7],
      rating: 3,
      album: 2,
      camera: 'EOS R5',
      gps: true,
      tags: [2, 5],
      minPx: 100,
      maxPx: 4000,
    });
  });

  it('drops what the share may not use: rating without rate, gps without location, album and tags in an album share', () => {
    const query = q('type=video&rating=3&gps=1&album=2&tag=1&camera=x');
    expect(filtersFromQuery(query, guest)).toEqual({ type: 'video', camera: 'x' });
    expect(filtersFromQuery(query, { ...guest, rate: true })).toEqual({ type: 'video', camera: 'x', rating: 3 });
    expect(filtersFromQuery(query, { ...guest, location: true })).toEqual({ type: 'video', camera: 'x', gps: true });
  });

  it('ignores malformed values and keys the server refuses', () => {
    const query = q('type=raw&from=2023-02-29&to=2020&recur=13-01&months=0&rating=9&album=x&gps=2&tag=-1&minpx=20&maxpx=10&trashed=1&import=3&session=1');
    expect(filtersFromQuery(query, all)).toEqual({});
    expect(filtersFromQuery(q('camera=' + 'x'.repeat(201)), all)).toEqual({});
    expect(filtersFromQuery(q(Array.from({ length: 40 }, (_, i) => `tag=${i + 1}`).join('&')), all).tags).toHaveLength(32);
  });

  it('writes the query in a stable order, tags repeated', () => {
    expect(filtersQuery({ tags: [2, 5], type: 'image', months: [6, 7], gps: true, camera: 'EOS R5', minPx: 2000 }).toString()).toBe(
      'type=image&months=6%2C7&camera=EOS+R5&gps=1&tag=2&tag=5&minpx=2000',
    );
    expect(filtersQuery({}).toString()).toBe('');
    expect(cleanFilters({ type: undefined, gps: false, tags: [], camera: '', rating: 0, from: '2020-01-01' })).toEqual({ from: '2020-01-01' });
  });

  it('one chip per filter, each removing only itself', () => {
    const f = { type: 'video' as const, from: '2020-01-01', to: '2022-01-01', months: [7], rating: 4, camera: 'Pixel 7', gps: true, album: 3, tags: [1, 9], maxPx: 1600 };
    const chips = filterChips(f, { tags: new Map([[1, 'beach']]), albums: new Map([[3, 'Summer']]) });
    expect(chips.map((c) => c.label)).toEqual(['Videos', 'Jul every year, 2020 – 2021', '4★ or more', 'Pixel 7', 'Has location', 'Album: Summer', '#beach', '#9', '≤ 1600 px']);
    expect(chips.find((c) => c.id === 'dates')?.without).toEqual({ type: 'video', rating: 4, camera: 'Pixel 7', gps: true, album: 3, tags: [1, 9], maxPx: 1600 });
    expect(chips.find((c) => c.id === 'tag:1')?.without.tags).toEqual([9]);
    expect(chips.find((c) => c.id === 'tag:9')?.without.tags).toEqual([1]);
    expect(filterChips({ tags: [1] })[0]!.without).toEqual({});
    expect(countFilters(f)).toBe(9);
    expect(countFilters({})).toBe(0);
    expect(filterChips({ rating: 5 })[0]!.label).toBe('5★');
  });

  it('month ranges for the From and To pickers', () => {
    expect(fromMonth('2024-07-15')).toEqual({ year: 2024, month: 7 });
    expect(fromMonth(undefined)).toBeNull();
    expect(toMonth('2025-01-01')).toEqual({ year: 2024, month: 12 });
    expect(toMonth('2024-08-01')).toEqual({ year: 2024, month: 7 });
    expect(monthStart({ year: 2024, month: 7 })).toBe('2024-07-01');
    expect(monthEnd({ year: 2024, month: 7 })).toBe('2024-08-01');
    expect(monthEnd({ year: 2024, month: 12 })).toBe('2025-01-01');
  });

  it('year chips: one year, then a range, and the same year again clears', () => {
    expect(tapYear(undefined, undefined, 2020)).toEqual({ from: '2020-01-01', to: '2021-01-01' });
    expect(tapYear('2020-01-01', '2021-01-01', 2023)).toEqual({ from: '2020-01-01', to: '2024-01-01' });
    expect(tapYear('2020-01-01', '2021-01-01', 2018)).toEqual({ from: '2018-01-01', to: '2021-01-01' });
    expect(tapYear('2020-01-01', '2021-01-01', 2020)).toEqual({});
    expect(tapYear('2020-01-01', '2024-01-01', 2022)).toEqual({ from: '2022-01-01', to: '2023-01-01' });
    expect(tapYear('2020-03-01', undefined, 2022)).toEqual({ from: '2022-01-01', to: '2023-01-01' });
    expect(wholeYears('2020-01-01', '2024-01-01')).toEqual({ a: 2020, b: 2023 });
    expect(wholeYears('2020-02-01', '2024-01-01')).toBeNull();
  });
});
