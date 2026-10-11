import { describe, expect, it } from 'vitest';
import { calendarMonths, monthLength, rangePresets, shiftDayByMonths, shiftMonth, weekdayOf, yearOptions } from './calendar';

const days = (rows: ReturnType<typeof calendarMonths>) => rows.flat();

describe('calendarMonths', () => {
  it('lays out February 2024 (leap year) Monday first', () => {
    const rows = calendarMonths(2024, 2);
    expect(rows).toHaveLength(6);
    for (const r of rows) expect(r).toHaveLength(7);
    // 1 Feb 2024 is a Thursday: Mon 29 Jan .. Wed 31 Jan lead in.
    expect(rows[0]!.map((c) => c.day)).toEqual(['2024-01-29', '2024-01-30', '2024-01-31', '2024-02-01', '2024-02-02', '2024-02-03', '2024-02-04']);
    expect(rows[0]!.map((c) => c.inMonth)).toEqual([false, false, false, true, true, true, true]);
    const inMonth = days(rows).filter((c) => c.inMonth);
    expect(inMonth).toHaveLength(29);
    expect(inMonth.at(-1)!.day).toBe('2024-02-29');
    expect(days(rows).at(-1)!.day).toBe('2024-03-10');
    expect(rows.every((r) => r[0]!.weekday === 0 && weekdayOf(r[0]!.day) === 0)).toBe(true);
  });

  it('lays out February 2023 (28 days)', () => {
    const rows = calendarMonths(2023, 2);
    // 1 Feb 2023 is a Wednesday.
    expect(rows[0]![0]!.day).toBe('2023-01-30');
    expect(rows[0]![2]!.day).toBe('2023-02-01');
    const inMonth = days(rows).filter((c) => c.inMonth);
    expect(inMonth).toHaveLength(28);
    expect(inMonth.some((c) => c.day === '2023-02-29')).toBe(false);
    expect(days(rows).at(-1)!.day).toBe('2023-03-12');
  });

  it('starts on the 1st when the month starts on a Monday', () => {
    // 1 Jan 2024 is a Monday.
    const rows = calendarMonths(2024, 1);
    expect(rows[0]![0]).toMatchObject({ day: '2024-01-01', inMonth: true, date: 1, month: 1, year: 2024 });
  });

  it('crosses the year end', () => {
    const dec = days(calendarMonths(2024, 12));
    expect(dec[0]!.day).toBe('2024-11-25');
    const tail = dec.filter((c) => !c.inMonth && c.year === 2025);
    expect(tail[0]).toMatchObject({ day: '2025-01-01', month: 1, year: 2025, inMonth: false });
    const jan = days(calendarMonths(2025, 1));
    // 1 Jan 2025 is a Wednesday: the 30th and 31st of December lead in.
    expect(jan.slice(0, 3).map((c) => c.day)).toEqual(['2024-12-30', '2024-12-31', '2025-01-01']);
  });
});

describe('month arithmetic', () => {
  it('shifts months over year ends', () => {
    expect(shiftMonth(2024, 12, 1)).toEqual({ year: 2025, month: 1 });
    expect(shiftMonth(2024, 1, -1)).toEqual({ year: 2023, month: 12 });
    expect(shiftMonth(2024, 5, -17)).toEqual({ year: 2022, month: 12 });
  });
  it('clamps the day when moving by months', () => {
    expect(shiftDayByMonths('2024-01-31', 1)).toBe('2024-02-29');
    expect(shiftDayByMonths('2023-03-31', -1)).toBe('2023-02-28');
    expect(shiftDayByMonths('2024-02-29', 12)).toBe('2025-02-28');
  });
  it('knows month lengths', () => {
    expect(monthLength(2024, 2)).toBe(29);
    expect(monthLength(1900, 2)).toBe(28);
    expect(monthLength(2000, 2)).toBe(29);
    expect(monthLength(2024, 4)).toBe(30);
  });
});

describe('presets and years', () => {
  it('computes the range presets from today', () => {
    const p = Object.fromEntries(rangePresets('2024-03-05').map((x) => [x.id, [x.from, x.to]]));
    expect(p.last7).toEqual(['2024-02-28', '2024-03-05']);
    expect(p.last30).toEqual(['2024-02-05', '2024-03-05']);
    expect(p.month).toEqual(['2024-03-01', '2024-03-31']);
    expect(p.year).toEqual(['2024-01-01', '2024-12-31']);
    expect(p.lastyear).toEqual(['2023-01-01', '2023-12-31']);
  });
  it('spans the library years, else 30 years around', () => {
    expect(yearOptions([2021, 2019], 2020)).toEqual([2021, 2020, 2019]);
    expect(yearOptions([2010, 2012], 2026)).toEqual([2026, 2025, 2024, 2023, 2022, 2021, 2020, 2019, 2018, 2017, 2016, 2015, 2014, 2013, 2012, 2011, 2010]);
    const around = yearOptions([], 2000);
    expect(around[0]).toBe(2030);
    expect(around.at(-1)).toBe(1970);
  });
});
