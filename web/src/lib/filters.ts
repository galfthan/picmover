// Filters live in the route query (#/l/peter/?type=video&gps=1) so they
// survive reloads and links like "Show imported" (?session=17) work. `tag`
// is repeatable. The trash page's `trashed`, the album page's `album` and
// the import record page's `import` come from the route, not from here,
// when the page implies them.

import type { FileType, Filters } from './api';
import { formatRecur, isDay, parseRecur } from './dates';

const TYPES: readonly FileType[] = ['image', 'image_raw', 'video'];

function positiveInt(s: string | null): number | null {
  if (!s || !/^\d+$/.test(s)) return null;
  const n = Number(s);
  return Number.isSafeInteger(n) && n > 0 ? n : null;
}

export function filtersFromQuery(q: URLSearchParams): Filters {
  const f: Filters = {};
  const t = q.get('type');
  if (t && (TYPES as readonly string[]).includes(t)) f.type = t as FileType;
  if (q.get('gps') === '1') f.gps = true;
  const cam = q.get('camera');
  if (cam) f.camera = cam;
  const s = positiveInt(q.get('session'));
  if (s !== null) f.session = s;
  const from = q.get('from');
  if (isDay(from)) f.from = from;
  const to = q.get('to');
  if (isDay(to)) f.to = to;
  const recur = parseRecur(q.get('recur'));
  if (recur) f.recur = formatRecur(recur);
  const rating = positiveInt(q.get('rating'));
  if (rating !== null && rating <= 5) f.rating = rating;
  const tags = [...new Set(q.getAll('tag').map(positiveInt).filter((n): n is number => n !== null))].sort((a, b) => a - b);
  if (tags.length) f.tags = tags;
  const album = positiveInt(q.get('album'));
  if (album !== null) f.album = album;
  const imp = positiveInt(q.get('import'));
  if (imp !== null) f.import = imp;
  return f;
}

/** Query values for filters; `tag` becomes repeated entries. `trashed` is never part of the URL. */
export function filtersToQuery(f: Filters): URLSearchParams {
  const q = new URLSearchParams();
  if (f.type) q.set('type', f.type);
  if (f.gps) q.set('gps', '1');
  if (f.camera) q.set('camera', f.camera);
  if (f.session) q.set('session', String(f.session));
  if (f.from) q.set('from', f.from);
  if (f.to) q.set('to', f.to);
  if (f.recur) q.set('recur', f.recur);
  if (f.rating) q.set('rating', String(f.rating));
  for (const t of [...(f.tags ?? [])].sort((a, b) => a - b)) q.append('tag', String(t));
  if (f.album) q.set('album', String(f.album));
  if (f.import) q.set('import', String(f.import));
  return q;
}

/** Stable key for a filter set (used to reset state when filters change). Includes `trashed`. */
export function filtersKey(f: Filters): string {
  const q = filtersToQuery(f);
  if (f.trashed) q.set('trashed', '1');
  return q.toString();
}

/** Whether the user has narrowed the view (the page's own `album`/`import`/`trashed` do not count). */
export function hasFilters(f: Filters, implied: { album?: boolean; import?: boolean } = {}): boolean {
  const g: Filters = { ...f, trashed: undefined };
  if (implied.album) g.album = undefined;
  if (implied.import) g.import = undefined;
  return filtersKey(g) !== '';
}
