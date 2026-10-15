// @vitest-environment jsdom
//
// The phone's map (docs/design/lan-share.md "Map on the phone") against
// the fake share listener, with the real Leaflet: the Map/Grid switch only
// with `location`, the position in the route, the filters on the map,
// a cluster's bottom sheet and the lightbox over it, a single photo, an
// album share, `location` turned off meanwhile, the plain background
// when the tiles fail, and the phone's location never asked for.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fakeShareServer, go, settle, type FakeShareOptions, type FakeShareServer } from './testing/fakes';

let server: FakeShareServer;
let unmountApp: (() => void) | null = null;
const errors: unknown[] = [];

function setup(kind: 'libraries' | 'album' = 'libraries', opts: FakeShareOptions = {}) {
  server = fakeShareServer(kind, opts);
  server.install();
}

async function start(hash: string) {
  history.replaceState(null, '', '/' + hash);
  const svelte = await import('svelte');
  const ShareApp = (await import('./share/ShareApp.svelte')).default;
  const app = svelte.mount(ShareApp, { target: document.body });
  unmountApp = () => void svelte.unmount(app);
  await settle();
}

const click = (el: Element) => el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
const text = () => document.body.textContent ?? '';
const control = (label: string) =>
  [...document.querySelectorAll<HTMLElement>('button, a')].find((b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label);
const paths = () => server.calls.map((c) => c.path);
const geoCalls = () => paths().filter((p) => /^\/api\/l\/[^/]+\/geo/.test(p)).map(decodeURIComponent);
const markers = () => [...document.querySelectorAll<HTMLElement>('.sm-marker')];
const lightbox = () => document.querySelector<HTMLElement>('.lightbox');
const hashQuery = () => new URLSearchParams(location.hash.split('?')[1] ?? '');

/**
 * Waits for the map: the first test transforms Leaflet and the map's
 * module, which takes real time that the fake timers do not advance.
 */
async function mapReady() {
  await vi.waitFor(() => expect(document.querySelector('.leaflet-container')).not.toBeNull(), { timeout: 15_000, interval: 20 });
  await settle();
}

describe('phone map', () => {
  const geolocation = { getCurrentPosition: vi.fn(), watchPosition: vi.fn() };

  beforeEach(() => {
    vi.resetModules();
    errors.length = 0;
    vi.spyOn(console, 'error').mockImplementation((...a: unknown[]) => void errors.push(a));
    vi.spyOn(console, 'warn').mockImplementation((...a: unknown[]) => void errors.push(a));
    // A phone-sized map: Leaflet measures its container.
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(390);
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(700);
    geolocation.getCurrentPosition.mockClear();
    geolocation.watchPosition.mockClear();
    Object.defineProperty(navigator, 'geolocation', { value: geolocation, configurable: true });
  });

  afterEach(() => {
    unmountApp?.();
    unmountApp = null;
    document.body.innerHTML = '';
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    // The map never asks where the phone is.
    expect(geolocation.getCurrentPosition).not.toHaveBeenCalled();
    expect(geolocation.watchPosition).not.toHaveBeenCalled();
    expect(errors).toEqual([]);
  });

  it('library share: the Map switch opens the map fitted to every photo, with the position in the route', async () => {
    setup('libraries');
    await start('#/l/peter/');
    const sw = control('Map');
    expect(sw?.getAttribute('href')).toBe('#/l/peter/map');
    click(sw!);
    await go('#/l/peter/map');
    await mapReady();
    expect(document.querySelector('.leaflet-container')).not.toBeNull();
    // The extent: the world at zoom 0, then the clusters of the view.
    expect(geoCalls()[0]).toBe('/api/l/peter/geo?bbox=-180,-90,180,90&zoom=0');
    expect(location.hash).toMatch(/^#\/l\/peter\/map\?at=-?\d+\.\d{5},-?\d+\.\d{5}&z=\d+$/);
    expect(markers().length).toBeGreaterThan(0);
    // Every photo with a location is on the map (Helsinki and Paris).
    const counts = markers().map((m) => (m.classList.contains('sm-cluster') ? Number(m.querySelector('.n')?.textContent) : 1));
    expect(counts.reduce((a, b) => a + b, 0)).toBe(4);
    // The markers are thumbnails of the scope's photos.
    for (const img of document.querySelectorAll<HTMLImageElement>('.sm-marker img')) expect(img.getAttribute('src')).toMatch(/^\/api\/l\/peter\/thumb\/[0-9a-f]{16}/);
    // The Grid switch goes back, keeping the filters.
    expect(control('Grid')?.getAttribute('href')).toBe('#/l/peter/');
    expect(document.querySelector('.tabs')).toBeNull();
  });

  it('without location there is no map: no switch, and the map route goes to the grid', async () => {
    setup('libraries', { capabilities: { location: false } });
    await start('#/l/peter/');
    expect(control('Map')).toBeUndefined();
    await go('#/l/peter/map?at=60.17,24.94&z=12');
    expect(location.hash).toBe('#/l/peter/');
    await go('#/l/peter/albums/2/map');
    expect(location.hash).toBe('#/l/peter/albums/2');
    expect(geoCalls()).toEqual([]);
    expect(document.querySelector('.leaflet-container')).toBeNull();
  });

  it('opens at the position in the route; a cluster opens the sheet, a thumbnail the lightbox over the sheet', async () => {
    setup('libraries');
    await start('#/l/peter/map?at=60.17,24.94&z=12');
    await mapReady();
    expect(hashQuery().get('at')).toBe('60.17000,24.94000');
    expect(hashQuery().get('z')).toBe('12');
    // No fit: the first clusters are of the view around Helsinki.
    const view = geoCalls().find((p) => p.includes('zoom=12'))!;
    const box = new URLSearchParams(view.split('?')[1]).get('bbox')!.split(',').map(Number);
    expect(box[0]! < 24.94 && box[2]! > 24.94 && box[1]! < 60.17 && box[3]! > 60.17).toBe(true);
    const cluster = document.querySelector<HTMLElement>('.sm-cluster')!;
    expect(cluster.querySelector('.n')?.textContent).toBe('3');
    click(cluster);
    await settle();
    expect(text()).toContain('3 photos here');
    const strip = [...document.querySelectorAll<HTMLAnchorElement>('.sheet .strip a')];
    expect(strip).toHaveLength(3);
    // Newest first: 1 (Jul 19), 2 (Jul 18), 3 (Jul 17).
    expect(strip.map((a) => a.getAttribute('href'))).toEqual([1, 2, 3].map((id) => `#/l/peter/map/view/${id}?at=60.17000,24.94000&z=12`));
    expect(geoCalls()).toContain('/api/l/peter/geo/items?bbox=24.94,60.17,24.941,60.171&limit=200');
    expect(control('Zoom in here')).toBeDefined();
    click(strip[1]!);
    await settle();
    expect(location.hash).toBe('#/l/peter/map/view/2?at=60.17000,24.94000&z=12');
    expect(lightbox()).not.toBeNull();
    // The lightbox turns pages within the sheet's photos.
    click(control('Next')!);
    await settle();
    expect(location.hash).toBe('#/l/peter/map/view/3?at=60.17000,24.94000&z=12');
    // 3 is the sheet's last photo.
    expect(control('Next')).toBeUndefined();
    click(lightbox()!.querySelector('button[aria-label="Close"]')!);
    await settle();
    expect(lightbox()).toBeNull();
    expect(location.hash).toBe('#/l/peter/map?at=60.17000,24.94000&z=12');
    // The sheet is still there; Zoom in fits the cluster and closes it.
    expect(text()).toContain('3 photos here');
    click(control('Zoom in here')!);
    await settle();
    expect(document.querySelector('.sheet')).toBeNull();
    expect(Number(hashQuery().get('z'))).toBeGreaterThan(12);
  });

  it('a single photo opens the lightbox; Back and reload keep the map', async () => {
    setup('libraries');
    await start('#/l/peter/map?at=48.85,2.35&z=10');
    await mapReady();
    const single = markers().find((m) => !m.classList.contains('sm-cluster'))!;
    expect(single.querySelector('img')?.getAttribute('src')).toBe('/api/l/peter/thumb/0000000000000007');
    click(single);
    await settle();
    expect(location.hash).toBe('#/l/peter/map/view/7?at=48.85000,2.35000&z=10');
    expect(lightbox()).not.toBeNull();
    history.back();
    await settle();
    expect(location.hash).toBe('#/l/peter/map?at=48.85000,2.35000&z=10');
    expect(lightbox()).toBeNull();
  });

  it('the filters apply to the map, and Apply keeps the position', async () => {
    setup('libraries');
    await start('#/l/peter/map?type=video&at=60.17,24.94&z=12');
    await mapReady();
    expect(geoCalls().length).toBeGreaterThan(0);
    expect(geoCalls().every((p) => p.includes('type=video'))).toBe(true);
    expect(markers()).toHaveLength(1);
    expect(markers()[0]!.querySelector('img')?.getAttribute('src')).toContain('/thumb/0000000000000003');
    // The chip clears it; the map stays where it is.
    click(control('Filter, 1 on')!);
    await settle();
    const sheet = document.querySelector<HTMLElement>('form.sheet')!;
    click([...sheet.querySelectorAll<HTMLElement>('button')].find((b) => b.firstChild?.textContent?.trim() === 'Photos')!);
    await settle();
    click([...sheet.querySelectorAll<HTMLElement>('button')].find((b) => b.textContent?.trim() === 'Apply')!);
    await settle();
    expect(location.hash).toBe('#/l/peter/map?type=image&at=60.17000,24.94000&z=12');
    expect(geoCalls().at(-1)).toContain('type=image');
    expect(markers().length).toBeGreaterThan(0);
  });

  it('says so when no photo has a location, with Clear filters when filtered', async () => {
    setup('libraries');
    await start('#/l/peter/map?months=6&type=video');
    await mapReady();
    expect(text()).toContain('Nothing matches these filters');
    click(control('Clear filters')!);
    await settle();
    // Still the map, without filters, fitted to the photos now matching.
    expect(location.hash).toMatch(/^#\/l\/peter\/map\?at=/);
    expect(text()).not.toContain('Nothing matches');
    expect(markers().length).toBeGreaterThan(0);
  });

  it('album share: the map of the album only, never other albums or tags', async () => {
    setup('album', { capabilities: { location: true } });
    await start('#/');
    expect(control('Map')?.getAttribute('href')).toBe('#/map');
    await go('#/map');
    await mapReady();
    expect(geoCalls().length).toBeGreaterThan(0);
    expect(geoCalls().every((p) => p.startsWith('/api/l/album/geo') && !/[?&](album|tag)=/.test(p))).toBe(true);
    const counts = markers().map((m) => (m.classList.contains('sm-cluster') ? Number(m.querySelector('.n')?.textContent) : 1));
    expect(counts.reduce((a, b) => a + b, 0)).toBe(3);
    expect(document.querySelector('img[src*="0000000000000007"]')).toBeNull();
    expect(control('Grid')?.getAttribute('href')).toBe('#/');
    // Lightbox routes of the album share's map.
    click(document.querySelector('.sm-cluster')!);
    await settle();
    const first = document.querySelector<HTMLAnchorElement>('.sheet .strip a')!;
    expect(first.getAttribute('href')).toMatch(/^#\/map\/view\/1\?at=/);
  });

  it('album share without location: no switch, and #/map goes to the album', async () => {
    setup('album');
    await start('#/');
    expect(control('Map')).toBeUndefined();
    await go('#/map');
    expect(location.hash).toBe('#/');
    await go('#/map/view/1');
    expect(location.hash).toBe('#/');
    expect(geoCalls()).toEqual([]);
  });

  it('location turned off meanwhile: the 403 reads the session again and the map goes away', async () => {
    setup('libraries');
    await start('#/l/peter/map?at=60.17,24.94&z=12');
    await mapReady();
    expect(markers().length).toBeGreaterThan(0);
    server.caps.location = false;
    click(control('Show all photos')!);
    await settle();
    await vi.advanceTimersByTimeAsync(6_000);
    await settle();
    expect(location.hash).toBe('#/l/peter/');
    expect(document.querySelector('.leaflet-container')).toBeNull();
    expect(control('Map')).toBeUndefined();
  });

  it('without tiles the map stays usable on a plain background', async () => {
    setup('libraries');
    await start('#/l/peter/map?at=60.17,24.94&z=12');
    await mapReady();
    const tile = document.querySelector<HTMLImageElement>('img.leaflet-tile')!;
    expect(tile.getAttribute('src')).toMatch(/^https:\/\/tile\.openstreetmap\.org\/12\/\d+\/\d+\.png$/);
    expect(tile.referrerPolicy).toBe('strict-origin-when-cross-origin');
    tile.dispatchEvent(new Event('error'));
    await settle();
    expect(text()).toContain('No map background: the map tiles need the internet.');
    expect(markers().length).toBeGreaterThan(0);
  });
});
