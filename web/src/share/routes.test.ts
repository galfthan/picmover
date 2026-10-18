import { describe, expect, it } from 'vitest';
import { parseHash } from '../lib/router';
import { codeProblem, gridPath, mapPath, mapQuery, mapViewFromQuery, normaliseCode, pairToken, phoneName, resolveRoute } from './routes';
import { toShareInfo, type ShareInfo } from './shareapi';

const libShare = (ids: string[]): ShareInfo => toShareInfo({ name: 'My phone', kind: 'libraries', libraries: ids.map((id) => ({ id, name: id })) });
const albumShare = toShareInfo({ name: 'Summer guests', kind: 'album', album: { name: 'Summer', count: 3 } });
const at = (hash: string, share: ShareInfo | null) => resolveRoute(parseHash(hash), share);

describe('share routes', () => {
  it('library share: the libraries, or straight into the only one', () => {
    expect(at('#/', libShare(['peter', 'penny']))).toEqual({ page: { kind: 'libraries' } });
    expect(at('#/', libShare(['peter']))).toEqual({ redirect: '#/l/peter/' });
    expect(at('#/l/peter/', libShare(['peter']))).toEqual({ page: { kind: 'grid', lib: 'peter', viewId: null } });
    expect(at('#/l/peter/view/12', libShare(['peter']))).toEqual({ page: { kind: 'grid', lib: 'peter', viewId: 12 } });
    expect(at('#/l/peter/albums', libShare(['peter']))).toEqual({ page: { kind: 'albums', lib: 'peter' } });
    expect(at('#/l/peter/albums/3', libShare(['peter']))).toEqual({ page: { kind: 'album', lib: 'peter', albumId: 3, viewId: null } });
    expect(at('#/l/peter/albums/3/view/9', libShare(['peter']))).toEqual({ page: { kind: 'album', lib: 'peter', albumId: 3, viewId: 9 } });
  });

  it('library share: anything else goes home or to the library', () => {
    const s = libShare(['peter', 'penny']);
    expect(at('#/l/secret/', s)).toEqual({ redirect: '#/' });
    expect(at('#/view/3', s)).toEqual({ redirect: '#/' });
    expect(at('#/map', s)).toEqual({ redirect: '#/' });
    expect(at('#/l/peter/trash', s)).toEqual({ redirect: '#/l/peter/' });
    expect(at('#/l/peter/albums/x', s)).toEqual({ redirect: '#/l/peter/' });
    expect(at('#/l/peter/view/0', s)).toEqual({ redirect: '#/l/peter/' });
  });

  it('album share: the album grid at #/, and no library or album routes', () => {
    expect(at('#/', albumShare)).toEqual({ page: { kind: 'albumshare', viewId: null } });
    expect(at('#/view/7', albumShare)).toEqual({ page: { kind: 'albumshare', viewId: 7 } });
    for (const h of ['#/l/peter/', '#/l/album/', '#/l/peter/albums', '#/l/peter/albums/2', '#/view/x', '#/import']) {
      expect(at(h, albumShare)).toEqual({ redirect: '#/' });
    }
  });

  it('pairing works without a session; the token form is cut down to #/pair', () => {
    expect(at('#/pair', null)).toEqual({ page: { kind: 'pair' } });
    expect(at('#/pair/abc', null)).toEqual({ redirect: '#/pair' });
    expect(at('#/', null)).toEqual({ redirect: '#/' });
    expect(pairToken('#/pair/' + 'a'.repeat(43))).toBe('a'.repeat(43));
    expect(pairToken('#/pair/short')).toBeNull();
    expect(pairToken('#/pair')).toBeNull();
    expect(pairToken('#/pair/a b c d e f')).toBeNull();
  });

  it('map routes exist only with location, for a library, an album page and an album share', () => {
    const withLoc = (s: ShareInfo): ShareInfo => ({ ...s, capabilities: { ...s.capabilities, location: true } });
    const lib = withLoc(libShare(['peter']));
    expect(at('#/l/peter/map', lib)).toEqual({ page: { kind: 'map', lib: 'peter', albumId: null, albumShare: false, viewId: null } });
    expect(at('#/l/peter/map/view/4', lib)).toEqual({ page: { kind: 'map', lib: 'peter', albumId: null, albumShare: false, viewId: 4 } });
    expect(at('#/l/peter/albums/3/map', lib)).toEqual({ page: { kind: 'map', lib: 'peter', albumId: 3, albumShare: false, viewId: null } });
    expect(at('#/l/peter/albums/3/map/view/9', lib)).toEqual({ page: { kind: 'map', lib: 'peter', albumId: 3, albumShare: false, viewId: 9 } });
    expect(at('#/l/peter/map/view/x', lib)).toEqual({ redirect: '#/l/peter/' });
    expect(at('#/map', lib)).toEqual({ redirect: '#/' });
    const album = withLoc(albumShare);
    expect(at('#/map', album)).toEqual({ page: { kind: 'map', lib: 'album', albumId: null, albumShare: true, viewId: null } });
    expect(at('#/map/view/7', album)).toEqual({ page: { kind: 'map', lib: 'album', albumId: null, albumShare: true, viewId: 7 } });
    expect(at('#/map/x', album)).toEqual({ redirect: '#/' });
    // Without location: to the grid.
    expect(at('#/l/peter/map', libShare(['peter']))).toEqual({ redirect: '#/l/peter/' });
    expect(at('#/l/peter/albums/3/map/view/9', libShare(['peter']))).toEqual({ redirect: '#/l/peter/albums/3' });
    expect(at('#/map', albumShare)).toEqual({ redirect: '#/' });
    expect(at('#/map/view/7', albumShare)).toEqual({ redirect: '#/' });
  });

  it('map paths and the position in the query', () => {
    const map = { kind: 'map', lib: 'peter', albumId: null, albumShare: false, viewId: 2 } as const;
    expect(gridPath(map)).toBe('l/peter');
    expect(mapPath(map)).toBe('l/peter/map');
    expect(mapPath({ kind: 'album', lib: 'peter', albumId: 3, viewId: null })).toBe('l/peter/albums/3/map');
    expect(gridPath({ ...map, albumId: 3 })).toBe('l/peter/albums/3');
    expect(mapPath({ kind: 'albumshare', viewId: null })).toBe('map');
    expect(gridPath({ kind: 'map', lib: 'album', albumId: null, albumShare: true, viewId: null })).toBe('');
    // A library called "album" in a library share is still a library.
    expect(gridPath({ kind: 'map', lib: 'album', albumId: null, albumShare: false, viewId: null })).toBe('l/album');
    expect(mapPath({ kind: 'albums', lib: 'peter' })).toBeNull();
    const q = (s: string) => new URLSearchParams(s);
    expect(mapViewFromQuery(q('at=60.17,24.94&z=12'))).toEqual({ lat: 60.17, lon: 24.94, zoom: 12 });
    expect(mapViewFromQuery(q('at=-33.87,151.21&z=0'))).toEqual({ lat: -33.87, lon: 151.21, zoom: 0 });
    for (const bad of ['at=60.17,24.94', 'z=3', 'at=91,0&z=3', 'at=0,181&z=3', 'at=0,0&z=20', 'at=0,0&z=-1', 'at=x,y&z=3', 'at=1,2,3&z=3', 'at=1e3,2&z=3']) {
      expect(mapViewFromQuery(q(bad))).toBeNull();
    }
    expect(mapQuery(q('type=video&at=1,2&z=3'), { lat: 60.123456789, lon: -0.5, zoom: 7.4 }).toString()).toBe('type=video&at=60.12346%2C-0.50000&z=7');
    expect(mapQuery(q('type=video&at=1,2&z=3'), null).toString()).toBe('type=video');
  });

  it('grid paths for the lightbox routes', () => {
    expect(gridPath({ kind: 'grid', lib: 'peter', viewId: 1 })).toBe('l/peter');
    expect(gridPath({ kind: 'album', lib: 'peter', albumId: 3, viewId: null })).toBe('l/peter/albums/3');
    expect(gridPath({ kind: 'albumshare', viewId: null })).toBe('');
  });

  it('codes are case- and dash-insensitive, from the 32-letter alphabet', () => {
    expect(normaliseCode('k7qm-3xrd')).toBe('K7QM3XRD');
    expect(normaliseCode(' K7QM 3XRD ')).toBe('K7QM3XRD');
    expect(normaliseCode('K7QM-3XR')).toBeNull();
    expect(normaliseCode('K7QM-3XR0')).toBeNull(); // no 0, O, 1, I, L
  });

  it('says what is wrong with a code while it is typed', () => {
    for (const ok of ['', 'k7', 'K7QM-', 'k7qm 3xrd', 'K7QM-3XRD']) expect(codeProblem(ok)).toBe('');
    for (const misread of ['K7QM-3XR0', 'k7qo', '1', 'kil']) expect(codeProblem(misread)).toBe('Codes don’t use 0, 1, I, L or O. Check the code on the computer.');
    expect(codeProblem('K7QM_3XRD')).toBe('Codes have only letters and digits.');
    expect(codeProblem('K7QM3XRDA')).toBe('The code has 8 characters.');
    // The hint agrees with what is accepted: every character it lets through is in the alphabet.
    for (const ch of '23456789ABCDEFGHJKMNPQRSTUVWXYZ') expect(normaliseCode(ch.repeat(8))).not.toBeNull();
  });

  it('names the phone from the browser', () => {
    expect(phoneName('Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)')).toBe('iPhone');
    expect(phoneName('Mozilla/5.0 (Linux; Android 14; Pixel 7) Mobile Safari/537.36')).toBe('Android phone');
    expect(phoneName('Mozilla/5.0 (Linux; Sailfish 5.1; Mobile; rv:91.0) Gecko/91.0 Firefox/91.0')).toBe('Sailfish phone');
    expect(phoneName('')).toBe('Phone');
  });
});
