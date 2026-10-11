// View memory (docs/design/management.md "View memory (web)"): per page
// path ("/l/peter/", "/l/peter/map", "/l/peter/albums/3", ...) the filter
// query last shown there and, for grid pages, the place in the timeline.
// The main nav links go to the remembered query, so filters survive a trip
// to Import and back; the grid scrolls back to its anchor when it mounts
// with the same query. Kept in memory and in sessionStorage
// ("picmover.view.<path>"): a reload keeps it, a new tab starts fresh.

import { parseHash } from './router';
import { libPath, type AppRoute } from './routes';
import { isNumber, loadSession, saveSession } from './storage';

/** The bucket at the top of the view and how many pixels into it. */
export type ViewAnchor = { year: number; month: number; offset: number } | { undated: true; offset: number };

export interface ViewEntry {
  /** Canonical filter query (filtersToQuery), "" for none. */
  query: string;
  anchor?: ViewAnchor;
  savedAt: number;
}

export const VIEW_KEY_PREFIX = 'picmover.view.';

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);
const isInt = (v: unknown): v is number => isNumber(v) && Number.isInteger(v);

export function isViewAnchor(v: unknown): v is ViewAnchor {
  if (!isObject(v) || !isNumber(v.offset) || v.offset < 0) return false;
  if (v.undated === true) return true;
  return isInt(v.year) && v.year > 0 && isInt(v.month) && v.month >= 1 && v.month <= 12;
}

export function isViewEntry(v: unknown): v is ViewEntry {
  if (!isObject(v) || typeof v.query !== 'string' || !isNumber(v.savedAt)) return false;
  return v.anchor === undefined || isViewAnchor(v.anchor);
}

/** Normalised memory key of a router path or hash: "/l/peter/", "/l/peter/map", "/l/peter/albums/3". */
export function viewKey(path: string): string {
  const segs = parseHash(path.startsWith('#') ? path : '#' + path).segments;
  const p = '/' + segs.map(encodeURIComponent).join('/');
  return segs.length === 2 && segs[0] === 'l' ? p + '/' : p;
}

/** The router path a library route's view is remembered under, or null for pages without filters. */
export function memoryPath(app: AppRoute): string | null {
  if (app.kind !== 'lib') return null;
  switch (app.page) {
    case 'grid':
      return libPath(app.lib, '');
    case 'map':
      return libPath(app.lib, 'map');
    case 'trash':
      return libPath(app.lib, 'trash');
    case 'album':
      return app.albumId !== null ? libPath(app.lib, `albums/${app.albumId}`) : null;
    case 'importrecord':
      return app.importId !== null ? libPath(app.lib, `imports/${app.importId}`) : null;
    default:
      return null;
  }
}

export class ViewMemory {
  /** Entries written in this page load; others are read from sessionStorage. */
  #entries = new Map<string, ViewEntry>();
  #version = $state(0);

  get(path: string): ViewEntry | null {
    void this.#version;
    const k = viewKey(path);
    return this.#entries.get(k) ?? loadSession(VIEW_KEY_PREFIX + k, isViewEntry);
  }

  #set(k: string, e: ViewEntry) {
    this.#entries.set(k, e);
    saveSession(VIEW_KEY_PREFIX + k, e);
    this.#version++;
  }

  /** The page at `path` shows `query`; a different query than before drops the anchor. */
  remember(path: string, query: string): void {
    const k = viewKey(path);
    const old = this.get(k);
    if (old && old.query === query) return;
    this.#set(k, { query, savedAt: Date.now() });
  }

  /** The grid's place for `query`; ignored when the page has moved on to another query. */
  saveAnchor(path: string, query: string, anchor: ViewAnchor): void {
    const k = viewKey(path);
    const old = this.get(k);
    if (old && old.query !== query) return;
    this.#set(k, { query, anchor, savedAt: Date.now() });
  }

  /** Forgets the page's query and place. */
  clear(path: string): void {
    this.#set(viewKey(path), { query: '', savedAt: Date.now() });
  }

  /** Hash href of `path` with its remembered query. */
  href(path: string): string {
    const k = viewKey(path);
    const q = this.get(k)?.query ?? '';
    return '#' + k + (q ? '?' + q : '');
  }

  /** `hash` unchanged when it carries a query, else its path with the remembered query. */
  withMemory(hash: string): string {
    const r = parseHash(hash);
    if ([...r.query.keys()].length > 0) return hash;
    const q = this.get(r.path)?.query ?? '';
    return q ? '#' + viewKey(r.path) + '?' + q : hash;
  }
}

export const viewMemory = new ViewMemory();
