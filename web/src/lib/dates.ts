// Date filter values (docs/design/management.md "Date filters"). Pure; see
// dates.test.ts.
//
// - Range: `from` and `to` are wall-clock days "YYYY-MM-DD"; `to` is
//   exclusive in the API, while the UI shows and edits the inclusive last
//   day, so the conversion lives here.
// - Every year: `recur` is "MM-DD" or "MM-DD..MM-DD", inclusive, possibly
//   wrapping the year end ("12-30..01-02"); "02-29" is allowed.

import { monthName, parseWallClock } from './format';

export interface MonthDay {
  /** 1..12 */
  month: number;
  /** 1..31 */
  day: number;
}

export interface Recur {
  from: MonthDay;
  to: MonthDay;
}

/** Days of each month in a leap year: every month-day that can occur. */
const DAYS = [31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];

/** Days in month 1..12, counting 29 February. */
export function daysIn(month: number): number {
  return DAYS[month - 1] ?? 0;
}

const MD_RE = /^(\d{2})-(\d{2})$/;

export function parseMonthDay(s: string): MonthDay | null {
  const m = MD_RE.exec(s);
  if (!m) return null;
  const month = Number(m[1]);
  const day = Number(m[2]);
  if (month < 1 || month > 12 || day < 1 || day > daysIn(month)) return null;
  return { month, day };
}

const pad2 = (n: number) => String(n).padStart(2, '0');

export function formatMonthDay(md: MonthDay): string {
  return `${pad2(md.month)}-${pad2(md.day)}`;
}

/** Parses a `recur` value; null when absent or malformed. */
export function parseRecur(s: string | null | undefined): Recur | null {
  if (!s) return null;
  const parts = s.split('..');
  if (parts.length > 2) return null;
  const from = parseMonthDay(parts[0] ?? '');
  const to = parts.length === 2 ? parseMonthDay(parts[1] ?? '') : from;
  if (!from || !to) return null;
  return { from, to };
}

export function sameMonthDay(a: MonthDay, b: MonthDay): boolean {
  return a.month === b.month && a.day === b.day;
}

/** "MM-DD" for a single day, else "MM-DD..MM-DD". */
export function formatRecur(r: Recur): string {
  return sameMonthDay(r.from, r.to) ? formatMonthDay(r.from) : `${formatMonthDay(r.from)}..${formatMonthDay(r.to)}`;
}

function mdLabel(md: MonthDay, locale?: string): string {
  return `${md.day} ${monthName(md.month, 'short', locale)}`;
}

/** "24 Dec – 26 Dec every year", "14 Jul every year". */
export function describeRecur(r: Recur, locale?: string): string {
  const span = sameMonthDay(r.from, r.to) ? mdLabel(r.from, locale) : `${mdLabel(r.from, locale)} – ${mdLabel(r.to, locale)}`;
  return `${span} every year`;
}

export interface Preset {
  id: 'today' | 'christmas' | 'newyear' | 'midsummer' | 'summer' | 'month';
  label: string;
  recur: Recur;
}

const md = (month: number, day: number): MonthDay => ({ month, day });

/** The "every year" presets; Today and This month follow `today` (the viewer's local date). */
export function recurPresets(today: Date): Preset[] {
  const m = today.getMonth() + 1;
  const d = today.getDate();
  return [
    { id: 'today', label: 'Today', recur: { from: md(m, d), to: md(m, d) } },
    { id: 'christmas', label: 'Christmas', recur: { from: md(12, 24), to: md(12, 26) } },
    { id: 'newyear', label: 'New Year', recur: { from: md(12, 31), to: md(1, 1) } },
    { id: 'midsummer', label: 'Midsummer', recur: { from: md(6, 19), to: md(6, 26) } },
    { id: 'summer', label: 'Summer', recur: { from: md(6, 1), to: md(8, 31) } },
    { id: 'month', label: 'This month', recur: { from: md(m, 1), to: md(m, daysIn(m)) } },
  ];
}

// ---- Day ranges ----

const DAY_RE = /^\d{4}-\d{2}-\d{2}$/;

/** A real calendar day "YYYY-MM-DD" (not 2023-02-29). */
export function isDay(s: string | null | undefined): s is string {
  if (!s || !DAY_RE.test(s)) return false;
  const d = parseWallClock(s);
  return !!d && dayString(d) === s;
}

function dayString(d: Date): string {
  return `${String(d.getUTCFullYear()).padStart(4, '0')}-${pad2(d.getUTCMonth() + 1)}-${pad2(d.getUTCDate())}`;
}

/** Day arithmetic on "YYYY-MM-DD" (calendar days, no zones involved). */
export function addDays(day: string, n: number): string {
  const d = parseWallClock(day);
  if (!d) return day;
  d.setUTCDate(d.getUTCDate() + n);
  return dayString(d);
}

/** The last included day for an exclusive `to` ("" when open). */
export function inclusiveEnd(toExclusive: string | undefined): string {
  return toExclusive && isDay(toExclusive) ? addDays(toExclusive, -1) : '';
}

/** The exclusive `to` for an inclusive last day (undefined when empty). */
export function exclusiveEnd(lastDay: string): string | undefined {
  return isDay(lastDay) ? addDays(lastDay, 1) : undefined;
}

const yearDay = (y: number) => `${String(y).padStart(4, '0')}-01-01`;

/**
 * Sets one whole-year limit (null: open) and keeps the other bound as it
 * is, whatever it is (a day or a year). When the two would cross, the other
 * bound moves to the same year, so the range never matches nothing.
 */
export function withYear(from: string | undefined, to: string | undefined, which: 'from' | 'to', year: number | null): { from?: string; to?: string } {
  if (which === 'from') {
    const f = year !== null ? yearDay(year) : undefined;
    // Days "YYYY-MM-DD" with four-digit years compare as strings.
    const t = f && to && to <= f && year !== null ? yearDay(year + 1) : to;
    return { from: f, to: t };
  }
  const t = year !== null ? yearDay(year + 1) : undefined;
  const f = t && from && from >= t && year !== null ? yearDay(year) : from;
  return { from: f, to: t };
}

/** The year of `from` when it is a Jan 1 (null otherwise or absent). */
export function fromYear(from: string | undefined): number | null {
  return from && /^\d{4}-01-01$/.test(from) ? Number(from.slice(0, 4)) : null;
}

/** The last whole year of an exclusive `to` that is a Jan 1 (null otherwise or absent). */
export function toYear(to: string | undefined): number | null {
  return to && /^\d{4}-01-01$/.test(to) ? Number(to.slice(0, 4)) - 1 : null;
}

/**
 * Short text for the filter button: "2020 – 2024", "24 Dec – 26 Dec every
 * year, 2010 –", "". Years only when every given bound is a whole year,
 * else days on both sides ("2020-01-01 – 2020-02-29").
 */
export function describeDates(from: string | undefined, to: string | undefined, recur: string | undefined, locale?: string): string {
  const parts: string[] = [];
  const r = parseRecur(recur);
  if (r) parts.push(describeRecur(r, locale));
  if (from || to) {
    const fy = fromYear(from);
    const ty = toYear(to);
    const years = (!from || fy !== null) && (!to || ty !== null);
    const a = from ? (years ? String(fy) : from) : '';
    const b = to ? (years ? String(ty) : inclusiveEnd(to)) : '';
    if (a && b && a === b) parts.push(a);
    else parts.push(`${a}${a || b ? ' – ' : ''}${b}`.trim());
  }
  return parts.join(', ');
}
