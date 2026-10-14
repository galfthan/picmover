import { describe, expect, it } from 'vitest';
import { shareClient, shareFilterParams, toAlbums, toCapabilities, toDetail, toItem, toSession, toShareInfo } from './shareapi';

const none = { video: false, download: false, rate: false, location: false };
const every = { video: true, download: true, rate: true, location: true };

describe('share client parsing', () => {
  it('grants a capability only on an explicit true', () => {
    expect(toCapabilities(undefined)).toEqual(none);
    expect(toCapabilities({ video: 1, download: 'yes', rate: true })).toEqual({ ...none, rate: true });
  });

  it('reads both share kinds leniently', () => {
    expect(toShareInfo({ name: 'My phone', kind: 'libraries', capabilities: every, libraries: [{ id: 'peter', name: 'Peter', cover: '0000000000000001', total: 8 }, { id: '' }, 'x'] })).toEqual({
      name: 'My phone',
      kind: 'libraries',
      capabilities: every,
      expiresAt: null,
      libraries: [{ id: 'peter', name: 'Peter', cover: '0000000000000001', total: 8 }],
      album: null,
    });
    // An album share without a kind (the album says it), a cover that is not a hash.
    expect(toShareInfo({ name: 'Guests', album: { name: 'Summer', count: 3, cover: '/etc/passwd' }, expiresAt: '2026-11-13T19:00:00+02:00' })).toEqual({
      name: 'Guests',
      kind: 'album',
      capabilities: none,
      expiresAt: '2026-11-13T19:00:00+02:00',
      libraries: [],
      album: { name: 'Summer', count: 3, cover: null },
    });
  });

  it('reads the session and the pairing answer', () => {
    const s = toSession({ device: { id: 'd_1', name: 'Anna' }, computer: 'laptop', share: { name: 'x', kind: 'album', album: {} } });
    expect(s.device).toEqual({ id: 'd_1', name: 'Anna' });
    expect(s.computer).toBe('laptop');
    expect(s.share.kind).toBe('album');
    expect(toSession(null).share.capabilities).toEqual(none);
  });

  it('drops ratings and GPS the share does not grant, and malformed items', () => {
    const raw = { id: 3, hash: '0000000000000003', type: 'video', takenAt: '2024-07-01T10:00:00', w: 1, h: 2, dur: 7000, thumb: 'ok', rating: 4, gps: true, relPath: 'x' };
    expect(toItem(raw, none)).toEqual({ id: 3, hash: '0000000000000003', type: 'video', takenAt: '2024-07-01T10:00:00', w: 1, h: 2, dur: 7000, thumb: 'ok' });
    expect(toItem(raw, every)).toMatchObject({ rating: 4, gps: true });
    expect(toItem({ ...raw, id: 0 }, every)).toBeNull();
    expect(toItem({ ...raw, hash: 'nope' }, every)).toBeNull();
  });

  it('reads the redacted detail, never showing what a capability hides', () => {
    const raw = { id: 1, hash: '0000000000000001', type: 'image', w: 4, h: 3, fileName: 'C:\\x\\IMG_1.jpg', displayable: true, lat: 60, lon: 24, rating: 2, albums: [{ id: 2, name: 'Summer' }, { id: 'x' }], relPath: 'image/x.jpg' };
    const d = toDetail(raw, none);
    expect(d.displayable).toBe(false);
    expect(d.lat).toBeNull();
    expect(d.rating).toBeUndefined();
    expect(d.fileName).toBe('IMG_1.jpg');
    expect(d.albums).toEqual([{ id: 2, name: 'Summer' }]);
    expect('relPath' in d).toBe(false);
    expect(toDetail(raw, every)).toMatchObject({ displayable: true, lat: 60, lon: 24, rating: 2 });
  });

  it('reads albums', () => {
    expect(toAlbums({ albums: [{ id: 2, name: 'Summer', count: 3, cover: '0000000000000001' }, { id: -1 }] })).toEqual([{ id: 2, name: 'Summer', count: 3, cover: '0000000000000001' }]);
    expect(toAlbums(null)).toEqual([]);
  });

  it('builds the share listener URLs', () => {
    const c = shareClient('album', none);
    expect(c.thumbUrl({ hash: '0000000000000001', thumbAt: 5 })).toBe('/api/l/album/thumb/0000000000000001?v=5');
    expect(c.previewUrl({ hash: '0000000000000001' })).toBe('/api/l/album/preview/0000000000000001');
    expect(c.fileUrl(3, true)).toBe('/api/l/album/file/3?download=1');
    expect(shareFilterParams({ type: 'image', from: '2020-01-01', rating: 2, album: 3 }, { year: 2024, month: 7 }).toString()).toBe('type=image&from=2020-01-01&rating=2&album=3&year=2024&month=7');
  });
});
