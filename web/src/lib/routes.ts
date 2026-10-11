// App routes on top of the hash router (docs/design/management.md
// "Routes"). Every library page lives under #/l/<id>/; #/ is the
// Libraries page (workspace) or a redirect to the one library (single
// mode). Pure; see routes.test.ts.

import { href, type Route } from './router';

export type LibPage = 'grid' | 'map' | 'import' | 'library' | 'albums' | 'album' | 'trash' | 'similar' | 'backups' | 'view';

export type AppRoute =
  | { kind: 'libraries' }
  /** A step 3 path without the library prefix ("#/map", "#/view/3", "#/?session=4"). */
  | { kind: 'legacy'; rest: string }
  | { kind: 'lib'; lib: string; page: LibPage; albumId: number | null; viewId: number | null }
  | { kind: 'notfound' };

const SIMPLE: Record<string, LibPage> = {
  '': 'grid',
  map: 'map',
  import: 'import',
  library: 'library',
  albums: 'albums',
  trash: 'trash',
  similar: 'similar',
  backups: 'backups',
};

const LEGACY = new Set(['map', 'import', 'library', 'view', 'albums', 'trash', 'similar']);

function positiveInt(s: string | undefined): number | null {
  if (!s || !/^\d+$/.test(s)) return null;
  const n = Number(s);
  return Number.isSafeInteger(n) && n > 0 ? n : null;
}

export function parseAppRoute(r: Route): AppRoute {
  const s = r.segments;
  const first = s[0] ?? '';
  if (first === '') return { kind: 'libraries' };
  if (first !== 'l') return LEGACY.has(first) ? { kind: 'legacy', rest: s.join('/') } : { kind: 'notfound' };
  const lib = s[1];
  if (!lib) return { kind: 'notfound' };
  const sub = s[2] ?? '';
  if (sub === 'view') {
    if (s.length > 4) return { kind: 'notfound' };
    return { kind: 'lib', lib, page: 'view', albumId: null, viewId: positiveInt(s[3]) };
  }
  if (sub === 'albums' && s.length === 4) {
    const albumId = positiveInt(s[3]);
    return albumId === null ? { kind: 'notfound' } : { kind: 'lib', lib, page: 'album', albumId, viewId: null };
  }
  const page = SIMPLE[sub];
  if (page === undefined || s.length > 3) return { kind: 'notfound' };
  return { kind: 'lib', lib, page, albumId: null, viewId: null };
}

/** Router path of a library page: libPath('peter', 'albums/3') → "/l/peter/albums/3". */
export function libPath(lib: string, sub = ''): string {
  return `/l/${encodeURIComponent(lib)}/${sub.replace(/^\/+/, '')}`;
}

/** Hash href of a library page, with an optional query. */
export function libHref(lib: string, sub = '', query?: Parameters<typeof href>[1]): string {
  return href(libPath(lib, sub), query);
}

export interface WorkspaceMode {
  /** null until the listing has loaded. */
  mode: 'workspace' | 'single' | null;
  /** The library of single mode. */
  singleId: string | null;
}

/**
 * Where the shell must send a route instead of rendering it, or null.
 * Single mode sends #/ (and the step 3 paths) to its one library; workspace
 * mode sends step 3 paths and unknown routes to the Libraries page.
 */
export function redirectFor(app: AppRoute, r: Route, ws: WorkspaceMode): string | null {
  if (ws.mode === null) return null;
  const single = ws.mode === 'single' && ws.singleId !== null ? ws.singleId : null;
  switch (app.kind) {
    case 'libraries':
      return single !== null ? libHref(single, '', r.query) : null;
    case 'legacy':
      return single !== null ? libHref(single, app.rest, r.query) : '#/';
    case 'notfound':
      return single !== null ? libHref(single, '') : '#/';
    default:
      return null;
  }
}
