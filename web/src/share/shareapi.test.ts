import { afterEach, describe, expect, it } from 'vitest';
import {
  backoffMs,
  checkUrl,
  errorText,
  isAbort,
  NetworkError,
  retrier,
  retryAfterMs,
  setRefusalHandler,
  shareClient,
  shareFilterParams,
  THUMB_RETRIES,
  toAlbums,
  toClusters,
  toCapabilities,
  toDetail,
  toFacets,
  toItem,
  toSession,
  toVideoInfo,
  toShareInfo,
  UNREACHABLE,
  type UrlCheck,
} from './shareapi';

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
    expect(
      shareFilterParams({ to: '2021-01-01', recur: '06-19..06-26', months: [1, 7], camera: 'Z 6', gps: true, tags: [3, 4], minPx: 10, maxPx: 20 }).toString(),
    ).toBe('to=2021-01-01&recur=06-19..06-26&months=1%2C7&camera=Z+6&gps=1&tag=3&tag=4&minpx=10&maxpx=20');
    expect(c.thumbUrl({ hash: '0000000000000001' })).toBe('/api/l/album/thumb/0000000000000001');
  });

  it('reads the facets, keeping gps only with location', () => {
    const raw = {
      total: 5,
      types: { image: 4, image_raw: 0, video: 1 },
      years: [{ year: 2024, count: 5 }, { year: 'x' }],
      undated: 0,
      cameras: [{ make: 'Canon', model: 'EOS R5', count: 4 }, { make: 'x' }],
      nocamera: 1,
      gps: 1,
      tags: [{ id: 3, name: 'beach', count: 2 }, { id: 0, name: 'bad' }],
      albums: [{ id: 2, name: 'Summer', count: 3 }],
    };
    expect(toFacets(raw, every)).toEqual({
      total: 5,
      types: { image: 4, image_raw: 0, video: 1 },
      years: [{ year: 2024, count: 5 }],
      undated: 0,
      cameras: [{ make: 'Canon', model: 'EOS R5', count: 4 }],
      nocamera: 1,
      gps: 1,
      tags: [{ id: 3, name: 'beach', count: 2 }],
      albums: [{ id: 2, name: 'Summer', count: 3 }],
    });
    expect(toFacets(raw, none).gps).toBeNull();
    // An album share's answer: no gps, tags or albums.
    expect(toFacets({ total: 0, types: {}, years: [], cameras: [] }, none)).toEqual({
      total: 0,
      types: { image: 0, image_raw: 0, video: 0 },
      years: [],
      undated: 0,
      cameras: [],
      nocamera: 0,
      gps: null,
      tags: [],
      albums: [],
    });
  });
});

describe('share client errors and failed media', () => {
  const realFetch = globalThis.fetch;
  afterEach(() => {
    globalThis.fetch = realFetch;
    setRefusalHandler(null);
  });
  const answer = (r: () => Promise<Response>, seen: RequestInit[] = []) =>
    (globalThis.fetch = ((_u: string, init: RequestInit) => (seen.push(init), r())) as unknown as typeof fetch);

  it('says "cannot reach" only when fetch itself failed', async () => {
    answer(() => Promise.reject(new TypeError('Failed to fetch')));
    const e = await shareClient('peter', none)
      .getAlbums()
      .catch((x: unknown) => x);
    expect(e).toBeInstanceOf(NetworkError);
    expect(errorText(e)).toBe(UNREACHABLE);
    // A TypeError from anywhere else (a bug) keeps its own message.
    expect(errorText(new TypeError("Cannot read properties of undefined (reading 'id')"))).toBe("Cannot read properties of undefined (reading 'id')");
    // An abort stays an abort.
    answer(() => Promise.reject(new DOMException('aborted', 'AbortError')));
    const ac = new AbortController();
    ac.abort();
    expect(isAbort(await shareClient('peter', none).getAlbums(ac.signal).catch((x: unknown) => x))).toBe(true);
  });

  it('reads Retry-After as seconds or a date', () => {
    const now = Date.parse('2026-10-14T12:00:00Z');
    expect(retryAfterMs('3', now)).toBe(3000);
    expect(retryAfterMs('Wed, 14 Oct 2026 12:00:10 GMT', now)).toBe(10_000);
    expect(retryAfterMs(null, now)).toBeNull();
    expect(retryAfterMs('soon', now)).toBeNull();
  });

  it('checks a failed URL with one byte, and reports refusals', async () => {
    const seen: RequestInit[] = [];
    const refusals: unknown[] = [];
    setRefusalHandler((...a) => void refusals.push(a));
    answer(() => Promise.resolve(new Response('{"error":"slow down"}', { status: 429, headers: { 'Retry-After': '2' } })), seen);
    expect(await checkUrl('/api/l/peter/file/3')).toEqual({ status: 429, retryAfter: 2000, state: '' });
    expect(new Headers(seen[0]?.headers).get('Range')).toBe('bytes=0-0');
    answer(() => Promise.resolve(new Response('{"error":"This share is paused.","state":"paused"}', { status: 403 })));
    expect(await checkUrl('/api/l/peter/thumb/x')).toEqual({ status: 403, retryAfter: null, state: 'paused' });
    expect(refusals).toEqual([[403, 'paused', 'This share is paused.']]);
    answer(() => Promise.resolve(new Response('{"error":"not allowed in this share"}', { status: 403 })));
    expect((await checkUrl('/api/l/peter/file/3')).status).toBe(403);
    expect(refusals).toHaveLength(1); // a capability, not a refusal of the phone
    answer(() => Promise.reject(new TypeError('Failed to fetch')));
    expect(await checkUrl('/api/l/peter/thumb/x')).toEqual({ status: 0, retryAfter: null, state: '' });
  });

  it('retries a failed thumbnail after Retry-After or a backoff, a bounded number of times, never a 404', async () => {
    let t = 0;
    const answers: UrlCheck[] = [];
    const checked: string[] = [];
    const check = async (url: string) => (checked.push(url), answers.shift() ?? { status: 200, retryAfter: null, state: '' });
    const delay = retrier(check, () => t, () => 0);
    answers.push({ status: 429, retryAfter: 5000, state: '' });
    expect(await delay('/a', 0)).toBe(5000);
    // While cooling down nothing is asked; the others wait it out.
    t = 1000;
    expect(await delay('/b', 0)).toBe(4000);
    expect(checked).toEqual(['/a']);
    t = 6000;
    answers.push({ status: 503, retryAfter: null, state: '' });
    expect(await delay('/a', 1)).toBe(2000); // backoff: 1, 2, 4, 8 s
    t = 9000;
    answers.push({ status: 0, retryAfter: null, state: '' });
    expect(await delay('/a', 2)).toBe(4000);
    t = 20_000;
    expect(await delay('/a', THUMB_RETRIES)).toBeNull();
    answers.push({ status: 404, retryAfter: null, state: '' });
    expect(await delay('/gone', 0)).toBeNull();
    answers.push({ status: 200, retryAfter: null, state: '' });
    expect(await delay('/fine-now', 0)).toBe(0);
    answers.push({ status: 403, retryAfter: null, state: 'paused' });
    expect(await delay('/refused', 0)).toBeNull();
    // Retry-After is capped at a minute.
    answers.push({ status: 429, retryAfter: 3_600_000, state: '' });
    expect(await delay('/a', 0)).toBe(60_000);
  });

  it('asks one URL at a time when many tiles fail together', async () => {
    let release: (r: UrlCheck) => void = () => {};
    let n = 0;
    const check = (_url: string) => (n++, new Promise<UrlCheck>((r) => (release = r)));
    const delay = retrier(check, () => 0, () => 0);
    const all = [delay('/1', 0), delay('/2', 0), delay('/3', 1)];
    release({ status: 429, retryAfter: 3000, state: '' });
    expect(await Promise.all(all)).toEqual([3000, 3000 + 1000, 3000 + 2000]);
    expect(n).toBe(1);
    expect(backoffMs(0, 0)).toBe(1000);
    expect(backoffMs(10, 1)).toBe(30_500);
  });

  it('reads the map clusters leniently', () => {
    const raw = {
      clusters: [
        { lat: 60.17, lon: 24.94, count: 3, hash: '0000000000000003', thumbAt: 1700000000, bounds: [24.9, 60.1, 25, 60.2], id: 9 },
        { lat: 48.85, lon: 2.35, count: 1, hash: '0000000000000007', id: 7 },
        { lat: 91, lon: 0, count: 1, hash: '0000000000000001', id: 1 },
        { lat: 0, lon: 0, count: 0, hash: '0000000000000001' },
        { lat: 0, lon: 0, count: 2, hash: 'nope' },
        { lat: 1, lon: 1, count: 2, hash: '0000000000000002', bounds: [0, 2, 1, 1] },
        'x',
      ],
    };
    expect(toClusters(raw)).toEqual([
      { lat: 60.17, lon: 24.94, count: 3, hash: '0000000000000003', thumbAt: 1700000000, id: null, bounds: { west: 24.9, south: 60.1, east: 25, north: 60.2 } },
      { lat: 48.85, lon: 2.35, count: 1, hash: '0000000000000007', id: 7, bounds: null },
      { lat: 1, lon: 1, count: 2, hash: '0000000000000002', id: null, bounds: null },
    ]);
    expect(toClusters({})).toEqual([]);
  });
});

describe('video playback on the phone', () => {
  const caps = { video: true, download: false, rate: false, location: false };
  it('parses the video fields of a detail leniently', () => {
    const d = toDetail({ id: 3, hash: '0000000000000003', type: 'video', video: { codec: 'hevc', bitDepth: 10, hdr: true, level: 'x', audio: '' }, canConvert: true, browserFormat: false }, caps);
    expect(d.video).toEqual({ codec: 'hevc', bitDepth: 10, hdr: true });
    expect(d.canConvert).toBe(true);
    expect(d.browserFormat).toBe(false);
    // Without video, canConvert means nothing; an older computer says nothing about the container.
    const old = toDetail({ id: 3, hash: '0000000000000003', type: 'video', canConvert: true }, { ...caps, video: false });
    expect(old.canConvert).toBe(false);
    expect(old.browserFormat).toBe(true);
    expect(old.video).toBeNull();
    expect(toVideoInfo({ codec: 7 })).toBeNull();
  });
  it('builds the stream URLs like the admin client', () => {
    const c = shareClient('peter', caps);
    expect(c.streamUrl(3, 'transcode', 720, 12.3456)).toBe('/api/l/peter/stream/3?mode=transcode&h=720&t=12.346');
    expect(c.streamUrl(3, 'remux', 720)).toBe('/api/l/peter/stream/3?mode=remux');
  });
});
