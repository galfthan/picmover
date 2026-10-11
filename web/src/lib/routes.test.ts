import { describe, expect, it } from 'vitest';
import { parseHash } from './router';
import { importedHref, libHref, libPath, parseAppRoute, redirectFor, type AppRoute } from './routes';

const app = (h: string) => parseAppRoute(parseHash(h));
const lib = (lib: string, page: string, extra: Partial<Extract<AppRoute, { kind: 'lib' }>> = {}) => ({
  kind: 'lib',
  lib,
  page,
  albumId: null,
  importId: null,
  viewId: null,
  ...extra,
});

describe('app routes', () => {
  it('parse every library page under the prefix', () => {
    expect(app('#/')).toEqual({ kind: 'libraries' });
    expect(app('')).toEqual({ kind: 'libraries' });
    expect(app('#/l/peter/')).toEqual(lib('peter', 'grid'));
    expect(app('#/l/peter')).toEqual(lib('peter', 'grid'));
    expect(app('#/l/peter/?type=video')).toEqual(lib('peter', 'grid'));
    for (const p of ['map', 'import', 'library', 'albums', 'imports', 'trash', 'similar']) expect(app(`#/l/peter/${p}`)).toEqual(lib('peter', p));
    expect(app('#/l/peter/albums/12')).toEqual(lib('peter', 'album', { albumId: 12 }));
    expect(app('#/l/peter/imports/5')).toEqual(lib('peter', 'importrecord', { importId: 5 }));
    expect(app('#/l/peter/imports/5?type=video')).toEqual(lib('peter', 'importrecord', { importId: 5 }));
    expect(app('#/l/peter/view/7')).toEqual(lib('peter', 'view', { viewId: 7 }));
    expect(app('#/l/peter/view/x')).toEqual(lib('peter', 'view', { viewId: null }));
  });

  it('decode the library id', () => {
    expect(app('#/l/my%20photos%231/map')).toEqual(lib('my photos#1', 'map'));
  });

  it('reject unknown or malformed paths', () => {
    expect(app('#/l/')).toEqual({ kind: 'notfound' });
    expect(app('#/l/peter/nope')).toEqual({ kind: 'notfound' });
    expect(app('#/l/peter/albums/0')).toEqual({ kind: 'notfound' });
    expect(app('#/l/peter/albums/3/x')).toEqual({ kind: 'notfound' });
    expect(app('#/l/peter/imports/0')).toEqual({ kind: 'notfound' });
    expect(app('#/l/peter/imports/x')).toEqual({ kind: 'notfound' });
    expect(app('#/l/peter/imports/3/x')).toEqual({ kind: 'notfound' });
    expect(app('#/imports')).toEqual({ kind: 'notfound' }); // not a step 3 path
    expect(app('#/whatever')).toEqual({ kind: 'notfound' });
  });

  it('recognise step 3 paths', () => {
    expect(app('#/map')).toEqual({ kind: 'legacy', rest: 'map' });
    expect(app('#/view/3')).toEqual({ kind: 'legacy', rest: 'view/3' });
  });

  it('build prefixed hrefs with encoded ids', () => {
    expect(libPath('peter')).toBe('/l/peter/');
    expect(libPath('peter', '/albums/3')).toBe('/l/peter/albums/3');
    expect(libHref('a b', 'map', { at: '1,2', z: 15 })).toBe('#/l/a%20b/map?at=1%2C2&z=15');
    expect(libHref('peter', '', { session: 17 })).toBe('#/l/peter/?session=17');
    expect(app(libHref('a/b?c', 'trash'))).toEqual(lib('a/b?c', 'trash')); // the encoding round-trips
    expect(app(libHref('a?c#d', 'trash'))).toEqual(lib('a?c#d', 'trash'));
  });
});

describe('Show imported', () => {
  it('prefers the import record, falls back to the catalog session', () => {
    expect(importedHref('peter', { importId: 12, sessionId: 4 })).toBe('#/l/peter/?import=12');
    expect(importedHref('peter', { importId: null, sessionId: 4 })).toBe('#/l/peter/?session=4');
    expect(importedHref('peter', { sessionId: 4 })).toBe('#/l/peter/?session=4');
    expect(importedHref('peter', { importId: 0, sessionId: 4 })).toBe('#/l/peter/?session=4');
    expect(importedHref('a b', { importId: 3 })).toBe('#/l/a%20b/?import=3');
    expect(importedHref('peter', { importId: null, sessionId: null })).toBeNull();
    expect(importedHref('peter', {})).toBeNull();
  });
});

describe('redirects', () => {
  const single = { mode: 'single' as const, singleId: 'Photos' };
  const ws = { mode: 'workspace' as const, singleId: null };
  const r = (h: string, m: Parameters<typeof redirectFor>[2]) => redirectFor(app(h), parseHash(h), m);

  it('send #/ to the one library in single mode, keeping the query', () => {
    expect(r('#/', single)).toBe('#/l/Photos/');
    expect(r('#/?session=17', single)).toBe('#/l/Photos/?session=17');
    expect(r('#/', ws)).toBeNull();
  });

  it('prefix step 3 links in single mode and send them home in a workspace', () => {
    expect(r('#/map?at=1,2&z=3', single)).toBe('#/l/Photos/map?at=1%2C2&z=3');
    expect(r('#/view/9', single)).toBe('#/l/Photos/view/9');
    expect(r('#/map', ws)).toBe('#/');
    expect(r('#/nope', ws)).toBe('#/');
    expect(r('#/nope', single)).toBe('#/l/Photos/');
  });

  it('wait for the listing and leave library routes alone', () => {
    expect(r('#/', { mode: null, singleId: null })).toBeNull();
    expect(r('#/l/peter/map', ws)).toBeNull();
    expect(r('#/l/peter/map', single)).toBeNull();
  });
});
