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
  | { kind: 'album'; lib: string; albumId: number; viewId: number | null };

const ID_RE = /^[1-9]\d{0,15}$/;
const id = (s: string | undefined): number | null => (s !== undefined && ID_RE.test(s) ? Number(s) : null);

/** The page for a route, or `redirect` (a hash to replace the current one with). */
export function resolveRoute(r: Route, share: ShareInfo | null): { page: SharePage } | { redirect: string } {
  const s = r.segments;
  if (s[0] === 'pair') return s.length === 1 ? { page: { kind: 'pair' } } : { redirect: '#/pair' };
  if (!share) return { redirect: '#/' };
  const home = { redirect: '#/' };
  if (share.kind === 'album') {
    // No library or album navigation at all in an album share.
    if (s.length === 0) return { page: { kind: 'albumshare', viewId: null } };
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
  if (rest[0] === 'albums') {
    if (rest.length === 1) return { page: { kind: 'albums', lib } };
    const aid = id(rest[1]);
    if (aid !== null && rest.length === 2) return { page: { kind: 'album', lib, albumId: aid, viewId: null } };
    if (aid !== null && rest.length === 4 && rest[2] === 'view' && id(rest[3]) !== null) {
      return { page: { kind: 'album', lib, albumId: aid, viewId: id(rest[3]) } };
    }
  }
  return { redirect: href(`l/${lib}/`) };
}

/** The router path of a page's grid, without the lightbox ("l/peter", "l/peter/albums/3", ""). */
export function gridPath(p: SharePage): string {
  switch (p.kind) {
    case 'grid':
      return `l/${p.lib}`;
    case 'album':
      return `l/${p.lib}/albums/${p.albumId}`;
    default:
      return '';
  }
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

/** A name for this phone from the browser ("iPhone", "Android phone", "Sailfish phone"). */
export function phoneName(ua: string): string {
  if (/iPhone/.test(ua)) return 'iPhone';
  if (/iPad/.test(ua)) return 'iPad';
  if (/Sailfish/i.test(ua)) return 'Sailfish phone';
  if (/Android/.test(ua)) return /Mobile/.test(ua) ? 'Android phone' : 'Android tablet';
  return 'Phone';
}
