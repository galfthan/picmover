// Month grids for the calendar range picker (components/Calendar.svelte).
// Pure; see calendar.test.ts. Days are "YYYY-MM-DD" wall-clock days, weeks
// start on Monday.

import { addDays } from './dates';

export interface DayCell {
  /** "YYYY-MM-DD" */
  day: string;
  year: number;
  /** 1..12 */
  month: number;
  /** 1..31 */
  date: number;
  /** 0 = Monday … 6 = Sunday */
  weekday: number;
  /** The day belongs to the month the grid shows (else it is a leading or trailing day). */
  inMonth: boolean;
}

const pad = (n: number, w = 2) => String(n).padStart(w, '0');

export function dayOf(year: number, month: number, date: number): string {
  return `${pad(year, 4)}-${pad(month)}-${pad(date)}`;
}

/** Days in a real month (28..31), leap years counted. */
export function monthLength(year: number, month: number): number {
  if (month === 2) return (year % 4 === 0 && year % 100 !== 0) || year % 400 === 0 ? 29 : 28;
  return [4, 6, 9, 11].includes(month) ? 30 : 31;
}

/** Monday-first weekday (0..6) of a day "YYYY-MM-DD". */
export function weekdayOf(day: string): number {
  const d = new Date(0);
  d.setUTCFullYear(Number(day.slice(0, 4)), Number(day.slice(5, 7)) - 1, Number(day.slice(8, 10)));
  return (d.getUTCDay() + 6) % 7;
}

/** The month `n` months after year/month (n may be negative). */
export function shiftMonth(year: number, month: number, n: number): { year: number; month: number } {
  const i = year * 12 + (month - 1) + n;
  return { year: Math.floor(i / 12), month: (((i % 12) + 12) % 12) + 1 };
}

/** The same day of the month `n` months away, clamped to that month's length (31/01 + 1 → 29/02). */
export function shiftDayByMonths(day: string, n: number): string {
  const m = shiftMonth(Number(day.slice(0, 4)), Number(day.slice(5, 7)), n);
  return dayOf(m.year, m.month, Math.min(Number(day.slice(8, 10)), monthLength(m.year, m.month)));
}

/**
 * The cells of one month view: 6 rows of 7 days, Monday first, starting
 * with the Monday on or before the 1st (leading days of the previous
 * month) and filled up with days of the next month.
 */
export function calendarMonths(viewYear: number, viewMonth: number): DayCell[][] {
  const first = dayOf(viewYear, viewMonth, 1);
  let day = addDays(first, -weekdayOf(first));
  const rows: DayCell[][] = [];
  for (let r = 0; r < 6; r++) {
    const row: DayCell[] = [];
    for (let c = 0; c < 7; c++) {
      const year = Number(day.slice(0, 4));
      const month = Number(day.slice(5, 7));
      row.push({ day, year, month, date: Number(day.slice(8, 10)), weekday: c, inMonth: year === viewYear && month === viewMonth });
      day = addDays(day, 1);
    }
    rows.push(row);
  }
  return rows;
}

/** The viewer's local today as "YYYY-MM-DD". */
export function localToday(now: Date = new Date()): string {
  return dayOf(now.getFullYear(), now.getMonth() + 1, now.getDate());
}

export interface RangePreset {
  id: 'last7' | 'last30' | 'month' | 'year' | 'lastyear';
  label: string;
  /** Inclusive first and last day. */
  from: string;
  to: string;
}

/** The range presets relative to `today` ("YYYY-MM-DD"). */
export function rangePresets(today: string): RangePreset[] {
  const y = Number(today.slice(0, 4));
  const m = Number(today.slice(5, 7));
  return [
    { id: 'last7', label: 'Last 7 days', from: addDays(today, -6), to: today },
    { id: 'last30', label: 'Last 30 days', from: addDays(today, -29), to: today },
    { id: 'month', label: 'This month', from: dayOf(y, m, 1), to: dayOf(y, m, monthLength(y, m)) },
    { id: 'year', label: 'This year', from: dayOf(y, 1, 1), to: dayOf(y, 12, 31) },
    { id: 'lastyear', label: 'Last year', from: dayOf(y - 1, 1, 1), to: dayOf(y - 1, 12, 31) },
  ];
}

/**
 * The years a year select offers, newest first: every year from the
 * earliest to the latest of `years` (the library's span) when given, else
 * 30 years either side of `around`; `around` itself is always included.
 */
export function yearOptions(years: readonly number[], around: number): number[] {
  const known = years.filter((y) => Number.isInteger(y));
  let lo = known.length ? Math.min(...known) : around - 30;
  let hi = known.length ? Math.max(...known) : around + 30;
  lo = Math.min(lo, around);
  hi = Math.max(hi, around);
  const out: number[] = [];
  for (let y = hi; y >= lo && out.length < 1000; y--) out.push(y);
  return out;
}
