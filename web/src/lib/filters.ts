// Filters live in the route query (#/?type=video&gps=1) so they survive
// reloads and links like "Show imported" (#/?session=17) work.

import type { FileType, Filters } from './api';

const TYPES: readonly FileType[] = ['image', 'image_raw', 'video'];
const DATE_RE = /^\d{4}-\d{2}-\d{2}$/;

export function filtersFromQuery(q: URLSearchParams): Filters {
  const f: Filters = {};
  const t = q.get('type');
  if (t && (TYPES as readonly string[]).includes(t)) f.type = t as FileType;
  if (q.get('gps') === '1') f.gps = true;
  const cam = q.get('camera');
  if (cam) f.camera = cam;
  const s = Number(q.get('session'));
  if (Number.isInteger(s) && s > 0) f.session = s;
  const from = q.get('from');
  if (from && DATE_RE.test(from)) f.from = from;
  const to = q.get('to');
  if (to && DATE_RE.test(to)) f.to = to;
  return f;
}

export function filtersToQuery(f: Filters): Record<string, string> {
  const q: Record<string, string> = {};
  if (f.type) q.type = f.type;
  if (f.gps) q.gps = '1';
  if (f.camera) q.camera = f.camera;
  if (f.session) q.session = String(f.session);
  if (f.from) q.from = f.from;
  if (f.to) q.to = f.to;
  return q;
}

/** Stable key for a filter set (used to reset state when filters change). */
export function filtersKey(f: Filters): string {
  return new URLSearchParams(filtersToQuery(f)).toString();
}

export function hasFilters(f: Filters): boolean {
  return filtersKey(f) !== '';
}
