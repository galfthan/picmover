// The phone's filters (docs/design/lan-share.md "Filters on the phone"):
// the route query, the chips above the grid and the date helpers of the
// filter sheet. Pure; see filters.test.ts.
//
// Filters live in the route query (#/l/peter/?type=video&tag=3) like the
// admin's, so Back, reload and the lightbox keep them. What the share may
// not use never gets into the filters, whatever the URL says: `rating`
// needs `rate`, `gps` needs `location`, `album` and `tag` exist only in a
// library share (the server answers 400 to each otherwise).

import { daysIn, describeDates, formatRecur, inclusiveEnd, isDay, parseMonths, parseRecur } from '../lib/dates';
import { describeSize, MAX_PX } from '../lib/filters';
import { shareFilterParams, type ShareFilters } from './shareapi';

/** What the share allows: its capabilities and whether it covers whole libraries. */
export interface FilterScope {
  rate: boolean;
  location: boolean;
  /** A library share (album and tag filters). */
  library: boolean;
}

const TYPES = ['image', 'image_raw', 'video'] as const;
/** The server's bounds: at most 32 tags, a camera name of at most 200 bytes. */
export const MAX_TAGS = 32;
const MAX_CAMERA = 200;

function positiveInt(s: string | null): number | null {
  if (!s || !/^\d{1,15}$/.test(s)) return null;
  const n = Number(s);
  return n > 0 ? n : null;
}

/** The share's filters from the route query: valid values the share may use, nothing else. */
export function filtersFromQuery(q: URLSearchParams, scope: FilterScope): ShareFilters {
  const f: ShareFilters = {};
  const t = q.get('type');
  if (t && (TYPES as readonly string[]).includes(t)) f.type = t as ShareFilters['type'];
  const from = q.get('from');
  if (isDay(from)) f.from = from;
  const to = q.get('to');
  if (isDay(to)) f.to = to;
  const recur = parseRecur(q.get('recur'));
  if (recur) f.recur = formatRecur(recur);
  const months = parseMonths(q.get('months'));
  if (months?.length) f.months = months;
  const r = q.get('rating');
  if (scope.rate && r && /^[1-5]$/.test(r)) f.rating = Number(r);
  const cam = q.get('camera');
  if (cam && new TextEncoder().encode(cam).length <= MAX_CAMERA) f.camera = cam;
  else if (q.get('nocamera') === '1') f.nocamera = true;
  if (scope.location && q.get('gps') === '1') f.gps = true;
  if (scope.library) {
    const album = positiveInt(q.get('album'));
    if (album !== null) f.album = album;
    const tags = [...new Set(q.getAll('tag').map(positiveInt).filter((n): n is number => n !== null))].sort((a, b) => a - b);
    if (tags.length) f.tags = tags.slice(0, MAX_TAGS);
  }
  const px = (k: string) => {
    const n = positiveInt(q.get(k));
    return n !== null && n <= MAX_PX ? n : null;
  };
  const minPx = px('minpx');
  const maxPx = px('maxpx');
  if (minPx === null || maxPx === null || minPx <= maxPx) {
    if (minPx !== null) f.minPx = minPx;
    if (maxPx !== null) f.maxPx = maxPx;
  }
  return f;
}

/** The route query of the filters (also the API's: the same keys). */
export function filtersQuery(f: ShareFilters): URLSearchParams {
  return shareFilterParams(f);
}

/** A stable key for a filter set (reloads the grid when it changes). */
export function filtersKey(f: ShareFilters): string {
  return filtersQuery(f).toString();
}

export function hasFilters(f: ShareFilters): boolean {
  return filtersKey(f) !== '';
}

/** Drops empty values (undefined, '', false, 0, []). */
export function cleanFilters(f: ShareFilters): ShareFilters {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(f)) {
    if (v === undefined || v === '' || v === false || v === 0 || (Array.isArray(v) && v.length === 0)) continue;
    out[k] = v;
  }
  return out as ShareFilters;
}

// ---- Chips ----

/** Names for ids in the filters (from the facets); missing ones read "#id". */
export interface FilterNames {
  tags: ReadonlyMap<number, string>;
  albums: ReadonlyMap<number, string>;
}

export const NO_NAMES: FilterNames = { tags: new Map(), albums: new Map() };

export interface Chip {
  /** Stable key ("type", "dates", "tag:3", …). */
  id: string;
  label: string;
  /** The filters without this chip. */
  without: ShareFilters;
}

export const TYPE_LABELS: Record<NonNullable<ShareFilters['type']>, string> = { image: 'Photos', image_raw: 'RAW', video: 'Videos' };

export function ratingLabel(n: number): string {
  return n >= 5 ? '5★' : `${n}★ or more`;
}

/** One chip per active filter, in the sheet's order; each tag is a chip of its own. */
export function filterChips(f: ShareFilters, names: FilterNames = NO_NAMES): Chip[] {
  const out: Chip[] = [];
  const drop = (patch: Partial<ShareFilters>) => cleanFilters({ ...f, ...patch });
  if (f.type) out.push({ id: 'type', label: TYPE_LABELS[f.type], without: drop({ type: undefined }) });
  const dates = describeDates(f.from, f.to, f.recur, f.months);
  if (dates) out.push({ id: 'dates', label: dates, without: drop({ from: undefined, to: undefined, recur: undefined, months: undefined }) });
  if (f.rating) out.push({ id: 'rating', label: ratingLabel(f.rating), without: drop({ rating: undefined }) });
  if (f.camera) out.push({ id: 'camera', label: f.camera, without: drop({ camera: undefined }) });
  else if (f.nocamera) out.push({ id: 'camera', label: UNKNOWN_CAMERA, without: drop({ nocamera: undefined }) });
  if (f.gps) out.push({ id: 'gps', label: 'Has location', without: drop({ gps: undefined }) });
  if (f.album) out.push({ id: 'album', label: `Album: ${names.albums.get(f.album) ?? `#${f.album}`}`, without: drop({ album: undefined }) });
  for (const t of f.tags ?? []) {
    out.push({ id: `tag:${t}`, label: `#${names.tags.get(t) ?? t}`, without: drop({ tags: (f.tags ?? []).filter((x) => x !== t) }) });
  }
  if (f.minPx || f.maxPx) out.push({ id: 'size', label: describeSize(f.minPx, f.maxPx), without: drop({ minPx: undefined, maxPx: undefined }) });
  return out;
}

/** The number on the Filter button. */
export function countFilters(f: ShareFilters): number {
  return filterChips(f).length;
}

// ---- Cameras ----

export const UNKNOWN_CAMERA = 'Unknown camera';

/** Maker suffixes that only make a camera's name longer. */
const MAKER_SUFFIX = /[\s,]+(corporation|corp\.?|co\.?,?\s*ltd\.?|ltd\.?|inc\.?|imaging|optical|electronics|company)\b\.?/gi;

/**
 * A camera's name for people: the maker and the model ("Apple iPhone 12",
 * "Samsung SM-G991B"), the model alone when it already starts with the
 * maker ("Canon EOS R6", "NIKON D750" from "NIKON CORPORATION"), without
 * the maker's company suffixes; a maker in lower case is capitalised.
 */
export function cameraLabel(make: string, model: string): string {
  let mk = make.replace(MAKER_SUFFIX, '').trim();
  if (mk && mk === mk.toLowerCase()) mk = mk.charAt(0).toUpperCase() + mk.slice(1);
  const md = model.trim();
  if (!mk) return md;
  const first = mk.split(/\s+/)[0]!.toLowerCase();
  if (md.toLowerCase().startsWith(first)) return md;
  return `${mk} ${md}`;
}

/**
 * The cameras matching a search: every word of it in the camera's name
 * (maker and model, any case), in the given order (most used first).
 */
export function searchCameras<T extends { make: string; model: string }>(cameras: readonly T[], query: string): T[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) return [...cameras];
  return cameras.filter((c) => {
    const hay = `${c.make} ${c.model} ${cameraLabel(c.make, c.model)}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  });
}

// ---- Date range by month (the sheet's From and To) ----

export interface MonthYear {
  month: number;
  year: number;
}

const pad = (n: number, w = 2) => String(n).padStart(w, '0');

/** The month and year of `from` (its first day's month); null when open. */
export function fromMonth(from: string | undefined): MonthYear | null {
  if (!isDay(from)) return null;
  return { year: Number(from.slice(0, 4)), month: Number(from.slice(5, 7)) };
}

/** The month and year of the last day an exclusive `to` includes; null when open. */
export function toMonth(to: string | undefined): MonthYear | null {
  const last = inclusiveEnd(to);
  if (!last) return null;
  return { year: Number(last.slice(0, 4)), month: Number(last.slice(5, 7)) };
}

/** `from` for the first day of a month. */
export function monthStart(m: MonthYear): string {
  return `${pad(m.year, 4)}-${pad(m.month)}-01`;
}

/** The exclusive `to` after the last day of a month. */
export function monthEnd(m: MonthYear): string {
  return m.month === 12 ? `${pad(m.year + 1, 4)}-01-01` : `${pad(m.year, 4)}-${pad(m.month + 1)}-01`;
}

/** Whether from/to are exactly the whole years a..b. */
export function wholeYears(from: string | undefined, to: string | undefined): { a: number; b: number } | null {
  if (!from || !to || !/^\d{4}-01-01$/.test(from) || !/^\d{4}-01-01$/.test(to)) return null;
  const a = Number(from.slice(0, 4));
  const b = Number(to.slice(0, 4)) - 1;
  return b >= a ? { a, b } : null;
}

/**
 * A tap on a year chip: the year alone; a second year makes a range from
 * the first; the only selected year again clears the range.
 */
export function tapYear(from: string | undefined, to: string | undefined, year: number): { from?: string; to?: string } {
  const w = wholeYears(from, to);
  const span = (a: number, b: number) => ({ from: `${pad(Math.min(a, b), 4)}-01-01`, to: `${pad(Math.max(a, b) + 1, 4)}-01-01` });
  if (w && w.a === w.b) return w.a === year ? {} : span(w.a, year);
  return span(year, year);
}

/** The days 1..n of a month (counting 29 February). */
export function dayList(month: number): number[] {
  return Array.from({ length: daysIn(month) }, (_, i) => i + 1);
}
