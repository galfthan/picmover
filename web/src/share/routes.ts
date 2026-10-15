// Routes of the phone app (docs/design/lan-share.md "Phone UI", "Routes"),
// on top of the shared hash router. Pure; see routes.test.ts.
//
//   #/pair/<token>, #/pair                  pairing (the token is taken out of the URL at once)
//   #/                                      library share: its libraries (one: redirect into it)
//                                           album share: the album grid
//   #/view/<mid>                            album share: the lightbox over the album
//   #/l/<id>/                               library share: the library's photos
//   #/l/<id>/view/<mid>                     … with the lightbox
//   #/l/<id>/albums                         library share: the albums
//   #/l/<id>/albums/<aid>                   library share: one album
//   #/l/<id>/albums/<aid>/view/<mid>        … with the lightbox
//   #/l/<id>/map, #/l/<id>/albums/<aid>/map the map of a library or an album page (with `location` only)
//   #/map                                   album share: the album's map (with `location` only)
//   …/map/view/<mid>                        … with the lightbox
//
// The map's position is in the query (`at=<lat>,<lon>&z=<zoom>`) beside
// the filters; without `location` a map route goes to its grid.
//
// The lightbox route keeps the grid it was opened from in its path, so a
// reload or a shared link reopens it over the same grid. Filters live in
// the query (share/filters.ts) and are kept on view routes.

import { href, type Route } from '../lib/router';
import type { ShareInfo } from './shareapi';

export type SharePage =
  | { kind: 'pair' }
  | { kind: 'libraries' }
  | { kind: 'albumshare'; viewId: number | null }
  | { kind: 'grid'; lib: string; viewId: number | null }
  | { kind: 'albums'; lib: string }
  | { kind: 'album'; lib: string; albumId: number; viewId: number | null }
  /** The map of a grid: a library (albumId null), an album page, or an album share (lib "album"). */
  | { kind: 'map'; lib: string; albumId: number | null; albumShare: boolean; viewId: number | null };

const ID_RE = /^[1-9]\d{0,15}$/;
const id = (s: string | undefined): number | null => (s !== undefined && ID_RE.test(s) ? Number(s) : null);

/** The page for a route, or `redirect` (a hash to replace the current one with). */
export function resolveRoute(r: Route, share: ShareInfo | null): { page: SharePage } | { redirect: string } {
  const s = r.segments;
  if (s[0] === 'pair') return s.length === 1 ? { page: { kind: 'pair' } } : { redirect: '#/pair' };
  if (!share) return { redirect: '#/' };
  const home = { redirect: '#/' };
  const maps = share.capabilities.location;
  if (share.kind === 'album') {
    // No library or album navigation at all in an album share.
    if (s.length === 0) return { page: { kind: 'albumshare', viewId: null } };
    if (s[0] === 'map') {
      if (!maps) return home;
      if (s.length === 1) return { page: { kind: 'map', lib: 'album', albumId: null, albumShare: true, viewId: null } };
      const mv = s[1] === 'view' && s.length === 3 ? id(s[2]) : null;
      return mv !== null ? { page: { kind: 'map', lib: 'album', albumId: null, albumShare: true, viewId: mv } } : home;
    }
    const v = s[0] === 'view' && s.length === 2 ? id(s[1]) : null;
    return v !== null ? { page: { kind: 'albumshare', viewId: v } } : home;
  }
  if (share.kind !== 'libraries') return s.length === 0 ? { page: { kind: 'libraries' } } : home;
  const libs = share.libraries;
  if (s.length === 0) {
    const only = libs.length === 1 ? libs[0] : undefined;
    return only ? { redirect: href(`l/${only.id}/`) } : { page: { kind: 'libraries' } };
  }
  if (s[0] !== 'l' || s.length < 2) return home;
  const lib = s[1] ?? '';
  if (!libs.some((l) => l.id === lib)) return home;
  const rest = s.slice(2);
  if (rest.length === 0) return { page: { kind: 'grid', lib, viewId: null } };
  if (rest[0] === 'view' && rest.length === 2 && id(rest[1]) !== null) return { page: { kind: 'grid', lib, viewId: id(rest[1]) } };
  if (rest[0] === 'map') {
    const m = mapPage(lib, null, rest.slice(1), maps);
    if (m) return m;
  }
  if (rest[0] === 'albums') {
    if (rest.length === 1) return { page: { kind: 'albums', lib } };
    const aid = id(rest[1]);
    if (aid !== null && rest.length === 2) return { page: { kind: 'album', lib, albumId: aid, viewId: null } };
    if (aid !== null && rest[2] === 'map') {
      const m = mapPage(lib, aid, rest.slice(3), maps);
      if (m) return m;
    }
    if (aid !== null && rest.length === 4 && rest[2] === 'view' && id(rest[3]) !== null) {
      return { page: { kind: 'album', lib, albumId: aid, viewId: id(rest[3]) } };
    }
  }
  return { redirect: href(`l/${lib}/`) };
}

/** A map route's page from what follows "map" (`[]` or `['view', mid]`); without `location`, its grid. */
function mapPage(lib: string, albumId: number | null, rest: string[], maps: boolean): { page: SharePage } | { redirect: string } | null {
  const grid = albumId === null ? `l/${lib}/` : `l/${lib}/albums/${albumId}`;
  if (!maps) return { redirect: href(grid) };
  if (rest.length === 0) return { page: { kind: 'map', lib, albumId, albumShare: false, viewId: null } };
  const v = rest[0] === 'view' && rest.length === 2 ? id(rest[1]) : null;
  return v !== null ? { page: { kind: 'map', lib, albumId, albumShare: false, viewId: v } } : null;
}

/** The router path of a page's grid, without the lightbox ("l/peter", "l/peter/albums/3", ""); a map's is its grid's. */
export function gridPath(p: SharePage): string {
  switch (p.kind) {
    case 'grid':
      return `l/${p.lib}`;
    case 'album':
      return `l/${p.lib}/albums/${p.albumId}`;
    case 'map':
      if (p.albumShare) return '';
      return p.albumId === null ? `l/${p.lib}` : `l/${p.lib}/albums/${p.albumId}`;
    default:
      return '';
  }
}

/** The router path of a grid page's map ("l/peter/map", "l/peter/albums/3/map", "map"); null for pages without one. */
export function mapPath(p: SharePage): string | null {
  switch (p.kind) {
    case 'grid':
    case 'album':
    case 'albumshare':
    case 'map': {
      const g = gridPath(p);
      return g ? `${g}/map` : 'map';
    }
    default:
      return null;
  }
}

/** A map position from the route query (`at=<lat>,<lon>&z=<zoom>`), or null when absent or malformed. */
export interface MapView {
  lat: number;
  lon: number;
  zoom: number;
}

export function mapViewFromQuery(q: URLSearchParams): MapView | null {
  const at = q.get('at');
  const z = q.get('z');
  if (!at || z === null || !/^\d{1,2}$/.test(z)) return null;
  const m = /^(-?\d{1,3}(?:\.\d{1,8})?),(-?\d{1,3}(?:\.\d{1,8})?)$/.exec(at);
  if (!m) return null;
  const lat = Number(m[1]);
  const lon = Number(m[2]);
  const zoom = Number(z);
  if (Math.abs(lat) > 90 || Math.abs(lon) > 180 || zoom > 19) return null;
  return { lat, lon, zoom };
}

/** The route query of a map: the filters' plus the position (5 decimals, about a metre). */
export function mapQuery(filters: URLSearchParams, view: MapView | null): URLSearchParams {
  const q = new URLSearchParams(filters);
  q.delete('at');
  q.delete('z');
  if (view) {
    q.set('at', `${view.lat.toFixed(5)},${view.lon.toFixed(5)}`);
    q.set('z', String(Math.round(view.zoom)));
  }
  return q;
}

/** The pairing token of a "#/pair/<token>" hash, else null. */
export function pairToken(hash: string): string | null {
  const m = /^#\/pair\/([A-Za-z0-9_-]{8,200})\/?$/.exec(hash);
  return m ? (m[1] ?? null) : null;
}

/**
 * Takes the pairing token out of the address bar and the history at once
 * (docs/design/lan-share.md "Redeem (phone)", step 6) and returns it.
 */
export function takePairToken(): string | null {
  const t = pairToken(location.hash);
  if (t === null) return null;
  history.replaceState(null, '', location.pathname + location.search + '#/pair');
  window.dispatchEvent(new HashChangeEvent('hashchange'));
  return t;
}

/** "K7QM3XRD" from what was typed ("k7qm-3xrd", spaces); null unless 8 characters of the code alphabet. */
export function normaliseCode(s: string): string | null {
  const c = s.toUpperCase().replace(/[\s-]/g, '');
  return /^[23456789ABCDEFGHJKMNPQRSTUVWXYZ]{8}$/.test(c) ? c : null;
}

/**
 * What is wrong with a code being typed, for a hint under the field; ""
 * while it can still become a code. The alphabet leaves out 0, 1, I, L
 * and O; since both halves of each look-alike pair are out, a misread
 * cannot be mapped to the intended character, only pointed out.
 */
export function codeProblem(s: string): string {
  const c = s.toUpperCase().replace(/[\s-]/g, '');
  if (/[01ILO]/.test(c)) return 'Codes don’t use 0, 1, I, L or O. Check the code on the computer.';
  if (/[^0-9A-Z]/.test(c)) return 'Codes have only letters and digits.';
  if (c.length > 8) return 'The code has 8 characters.';
  return '';
}

/** A name for this phone from the browser ("iPhone", "Android phone", "Sailfish phone"). */
export function phoneName(ua: string): string {
  if (/iPhone/.test(ua)) return 'iPhone';
  if (/iPad/.test(ua)) return 'iPad';
  if (/Sailfish/i.test(ua)) return 'Sailfish phone';
  if (/Android/.test(ua)) return /Mobile/.test(ua) ? 'Android phone' : 'Android tablet';
  return 'Phone';
}
