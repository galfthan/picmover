import { describe, expect, it } from 'vitest';
import { parseHash } from '../lib/router';
import { codeProblem, gridPath, normaliseCode, pairToken, phoneName, resolveRoute } from './routes';
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
