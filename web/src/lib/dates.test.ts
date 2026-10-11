import { describe, expect, it } from 'vitest';
import {
  addDays,
  describeDates,
  describeRecur,
  exclusiveEnd,
  formatRecur,
  fromYear,
  inclusiveEnd,
  isDay,
  parseRecur,
  recurPresets,
  toYear,
  withYear,
} from './dates';

describe('recur', () => {
  it('parses single days and ranges', () => {
    expect(parseRecur('07-14')).toEqual({ from: { month: 7, day: 14 }, to: { month: 7, day: 14 } });
    expect(parseRecur('06-01..08-31')).toEqual({ from: { month: 6, day: 1 }, to: { month: 8, day: 31 } });
  });

  it('allows 02-29 but no impossible days', () => {
    expect(parseRecur('02-29')).not.toBeNull();
    expect(parseRecur('02-30')).toBeNull();
    expect(parseRecur('04-31')).toBeNull();
    expect(parseRecur('13-01')).toBeNull();
    expect(parseRecur('00-10')).toBeNull();
    expect(parseRecur('7-4')).toBeNull();
    expect(parseRecur('01-01..02-02..03-03')).toBeNull();
    expect(parseRecur('01-01..')).toBeNull();
    expect(parseRecur('')).toBeNull();
    expect(parseRecur(null)).toBeNull();
  });

  it('serialises back to the canonical form', () => {
    for (const s of ['07-14', '12-24..12-26', '12-30..01-02', '02-29']) expect(formatRecur(parseRecur(s)!)).toBe(s);
    expect(formatRecur(parseRecur('07-14..07-14')!)).toBe('07-14');
  });

  it('keeps a range over the year end', () => {
    const r = parseRecur('12-30..01-02')!;
    expect(r).toEqual({ from: { month: 12, day: 30 }, to: { month: 1, day: 2 } });
    expect(formatRecur(r)).toBe('12-30..01-02');
  });

  it('describes ranges', () => {
    expect(describeRecur(parseRecur('12-24..12-26')!, 'en-GB')).toBe('24 Dec – 26 Dec every year');
    expect(describeRecur(parseRecur('07-14')!, 'en-GB')).toBe('14 Jul every year');
  });
});

describe('presets', () => {
  it('has the listed fixed ranges', () => {
    const p = Object.fromEntries(recurPresets(new Date(2026, 9, 10)).map((x) => [x.id, formatRecur(x.recur)]));
    expect(p).toEqual({
      today: '10-10',
      christmas: '12-24..12-26',
      newyear: '12-31..01-01',
      midsummer: '06-19..06-26',
      summer: '06-01..08-31',
      month: '10-01..10-31',
    });
  });
  it('includes 29 February in "This month" and follows the local date for Today', () => {
    const p = recurPresets(new Date(2027, 1, 3));
    expect(formatRecur(p.find((x) => x.id === 'month')!.recur)).toBe('02-01..02-29');
    expect(formatRecur(p.find((x) => x.id === 'today')!.recur)).toBe('02-03');
  });
});

describe('day ranges', () => {
  it('validates real days', () => {
    expect(isDay('2024-02-29')).toBe(true);
    expect(isDay('2023-02-29')).toBe(false);
    expect(isDay('2024-13-01')).toBe(false);
    expect(isDay('0099-01-01')).toBe(true);
    expect(isDay('2024-1-1')).toBe(false);
  });
  it('converts between the inclusive last day and the exclusive to', () => {
    expect(exclusiveEnd('2024-12-31')).toBe('2025-01-01');
    expect(exclusiveEnd('2024-02-28')).toBe('2024-02-29');
    expect(inclusiveEnd('2025-01-01')).toBe('2024-12-31');
    expect(inclusiveEnd('2024-03-01')).toBe('2024-02-29');
    expect(inclusiveEnd(undefined)).toBe('');
    expect(exclusiveEnd('')).toBeUndefined();
    expect(addDays('0099-12-31', 1)).toBe('0100-01-01');
  });
  it('maps whole years', () => {
    expect(withYear(undefined, undefined, 'from', 2010)).toEqual({ from: '2010-01-01', to: undefined });
    expect(withYear('2010-01-01', undefined, 'to', 2012)).toEqual({ from: '2010-01-01', to: '2013-01-01' });
    expect(withYear('2010-01-01', '2013-01-01', 'from', null)).toEqual({ from: undefined, to: '2013-01-01' });
  });
  it('changes one year limit without dropping a day bound on the other side', () => {
    expect(withYear(undefined, '2024-07-16', 'from', 2020)).toEqual({ from: '2020-01-01', to: '2024-07-16' });
    expect(withYear('2020-03-05', undefined, 'to', 2024)).toEqual({ from: '2020-03-05', to: '2025-01-01' });
  });
  it('never lets the year limits cross', () => {
    // From after To: To moves to the end of the same year, and the other way round.
    expect(withYear('2010-01-01', '2013-01-01', 'from', 2015)).toEqual({ from: '2015-01-01', to: '2016-01-01' });
    expect(withYear('2015-01-01', '2016-01-01', 'to', 2010)).toEqual({ from: '2010-01-01', to: '2011-01-01' });
    expect(withYear('2015-01-01', '2016-01-01', 'to', 2015)).toEqual({ from: '2015-01-01', to: '2016-01-01' });
    expect(withYear(undefined, '2020-03-01', 'from', 2020)).toEqual({ from: '2020-01-01', to: '2020-03-01' });
    expect(fromYear('2010-01-01')).toBe(2010);
    expect(fromYear('2010-03-01')).toBeNull();
    expect(toYear('2013-01-01')).toBe(2012);
  });
  it('summarises the filter', () => {
    expect(describeDates('2010-01-01', '2013-01-01', undefined)).toBe('2010 – 2012');
    expect(describeDates('2012-01-01', '2013-01-01', undefined)).toBe('2012');
    expect(describeDates('2024-12-20', undefined, undefined)).toBe('20/12/2024 –');
    expect(describeDates(undefined, '2025-01-01', undefined)).toBe('– 2024');
    expect(describeDates('2024-05-03', '2024-05-10', undefined)).toBe('03/05/2024 – 09/05/2024');
    expect(describeDates(undefined, undefined, '12-24..12-26', 'en-GB')).toBe('24 Dec – 26 Dec every year');
    expect(describeDates('2010-01-01', undefined, '06-01..08-31', 'en-GB')).toBe('1 Jun – 31 Aug every year, 2010 –');
    expect(describeDates(undefined, undefined, undefined)).toBe('');
    // A day on one side shows days on both, not "2020 – 2020-02-29".
    expect(describeDates('2020-01-01', '2020-03-01', undefined)).toBe('01/01/2020 – 29/02/2020');
    expect(describeDates('2020-03-05', '2021-01-01', undefined)).toBe('05/03/2020 – 31/12/2020');
  });
});
