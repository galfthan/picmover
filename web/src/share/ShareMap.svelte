<script lang="ts" module>
  /**
   * The map's tiles: OpenStreetMap's standard layer from its one host.
   * The share listener's CSP allows images from exactly this origin
   * (internal/server/share_routes.go shareTileOrigin); change both together.
   */
  export const TILE_URL = 'https://tile.openstreetmap.org/{z}/{x}/{y}.png';
  /** The deepest zoom of the tiles (and of the map). */
  export const MAX_ZOOM = 19;
  /** Photos the bottom sheet and the "in view" set show at most. */
  export const SET_LIMIT = 200;
</script>

<script lang="ts">
  // The phone's map (docs/design/lan-share.md "Map on the phone"): the
  // share's photos with a position, clustered by the server for the box
  // and zoom in view, over OpenStreetMap tiles; only with `location`. A
  // marker is its cluster's newest photo with the count as a badge. Tap a
  // photo: the lightbox, turning pages within the photos in view. Tap a
  // cluster: a bottom sheet with its photos and Zoom in. The position
  // lives in the route query (at=lat,lon&z=zoom), replaced while the map
  // moves, so Back and reload keep it; without one the map fits all the
  // photos. Without tiles (no internet) the markers stay on a plain
  // background. The phone's own location is never asked for.
  import 'leaflet/dist/leaflet.css';
  import type * as Leaflet from 'leaflet';
  import { onMount, untrack } from 'svelte';
  import type { MediaItem } from '../lib/api';
  import { formatCount } from '../lib/format';
  import { href, navigate } from '../lib/router';
  import ShareLightbox, { type Neighbours } from './ShareLightbox.svelte';
  import { mapQuery, type MapView } from './routes';
  import { errorText, isAbort, ShareError, type GeoBox, type ShareClient, type ShareCluster, type ShareFilters } from './shareapi';
  import { filtersKey } from './filters';

  interface Props {
    client: ShareClient;
    filters: ShareFilters;
    /** The route's map position (null: fit all the photos). */
    view: MapView | null;
    viewId: number | null;
    /** Router path of the map ("l/peter/map", "map"). */
    basePath: string;
    /** The filters' route query (the position is added to it). */
    query: URLSearchParams;
    /** The share's kind ("album" says "this album"). */
    kind: string;
    filtered: boolean;
    albumHref?: ((id: number) => string) | null;
    onclearfilters?: () => void;
    /** A 403 without a state (`location` turned off): the shell reads the session again. */
    onforbidden?: () => void;
  }
  let { client, filters, view, viewId, basePath, query, kind, filtered, albumHref = null, onclearfilters, onforbidden }: Props = $props();

  const DEBOUNCE_MS = 150;
  const WORLD: GeoBox = { west: -180, south: -90, east: 180, north: 90 };

  let el: HTMLDivElement | undefined = $state();
  let ready = $state(false);
  let loading = $state(false);
  let error = $state('');
  let tilesFailed = $state(false);
  /** Every photo of the filters, from the world at zoom 0: its extent, or null when there is none. */
  let all = $state.raw<{ key: string; box: GeoBox | null } | null>(null);

  interface Sheet {
    cluster: ShareCluster;
    items: MediaItem[];
    total: number;
    loading: boolean;
    error: string;
    canZoom: boolean;
  }
  let sheet = $state.raw<Sheet | null>(null);
  /** The photos in view (the lightbox's pages for a photo opened from a marker), for one box. */
  let inView: { key: string; items: Promise<MediaItem[]> } | null = null;
  let hint = $state.raw<MediaItem | null>(null);

  // Leaflet objects are not reactive state.
  let L: typeof Leaflet | null = null;
  let map: Leaflet.Map | null = null;
  let markers: Leaflet.LayerGroup | null = null;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let ac: AbortController | null = null;
  let sheetAc: AbortController | null = null;

  const fkey = $derived(`${client.id}|${filtersKey(filters)}`);

  /** The map's position now (longitude wrapped into [-180, 180]). */
  function currentView(): MapView | null {
    if (!map) return null;
    const c = map.getCenter();
    return { lat: c.lat, lon: c.wrap().lng, zoom: map.getZoom() };
  }
  const viewQuery = () => mapQuery(query, currentView() ?? view);
  const viewHref = (mid: number) => href(`${basePath}/view/${mid}`, viewQuery());

  /** The map's bounds as a box, wrapped to [-180, 180] (west > east crosses the antimeridian). */
  function currentBox(m: Leaflet.Map): GeoBox {
    const b = m.getBounds();
    const south = Math.max(-90, b.getSouth());
    const north = Math.min(90, b.getNorth());
    let west = b.getWest();
    let east = b.getEast();
    if (east - west >= 360) return { west: -180, south, east: 180, north };
    const wrap = (x: number) => ((((x + 180) % 360) + 360) % 360) - 180;
    if (west < -180 || west >= 180) west = wrap(west);
    if (east < -180 || east > 180) east = wrap(east);
    if (east === -180) east = 180;
    return { west, south, east, north };
  }

  /** Answers that change what is shown: 403 without a state is `location` turned off. */
  function failed(e: unknown): string {
    if (e instanceof ShareError && e.status === 403 && !e.state) onforbidden?.();
    return errorText(e);
  }

  // ---- Markers ----

  function esc(s: string): string {
    return s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
  }

  function icon(c: ShareCluster): Leaflet.DivIcon {
    const Lm = L as typeof Leaflet;
    const src = esc(client.thumbUrl({ hash: c.hash, thumbAt: c.thumbAt }));
    const badge = c.count > 1 ? `<span class="n">${esc(formatCount(c.count))}</span>` : '';
    return Lm.divIcon({
      className: c.count > 1 ? 'sm-marker sm-cluster' : 'sm-marker',
      html: `<img src="${src}" alt="" decoding="async">${badge}`,
      iconSize: [48, 48],
      iconAnchor: [24, 24],
    });
  }

  function render(list: ShareCluster[]) {
    if (!L || !map || !markers) return;
    markers.clearLayers();
    const centerLng = map.getCenter().lng;
    // The biggest clusters last, so they sit on top.
    for (const c of [...list].reverse()) {
      // In the world copy nearest the view's centre.
      const lon = c.lon + 360 * Math.round((centerLng - c.lon) / 360);
      const label = c.count > 1 ? `${formatCount(c.count)} photos` : 'Open the photo';
      const m = L.marker([c.lat, lon], { icon: icon(c), keyboard: true, title: label, alt: label });
      m.on('click', () => tapMarker(c));
      markers.addLayer(m);
    }
  }

  function tapMarker(c: ShareCluster) {
    if (c.count === 1 && c.id !== null) {
      openPhoto(c.id, null);
      return;
    }
    openSheet(c);
  }

  // ---- Loading ----

  async function refresh() {
    if (!map) return;
    const box = currentBox(map);
    const zoom = map.getZoom();
    ac?.abort();
    const ctl = new AbortController();
    ac = ctl;
    loading = true;
    try {
      const list = await client.getGeo(filters, box, zoom, ctl.signal);
      if (ctl.signal.aborted) return;
      error = '';
      render(list);
    } catch (e) {
      if (!isAbort(e) && !ctl.signal.aborted) error = failed(e);
    } finally {
      if (ac === ctl) loading = false;
    }
  }

  function schedule() {
    clearTimeout(timer);
    timer = setTimeout(() => void refresh(), DEBOUNCE_MS);
  }

  /** The extent of every photo of the filters: one request for the world at zoom 0, whose clusters together hold them all. */
  async function loadAll(key: string): Promise<GeoBox | null> {
    const list = await client.getGeo(filters, WORLD, 0);
    let box: GeoBox | null = null;
    for (const c of list) {
      const b = c.bounds ?? { west: c.lon, south: c.lat, east: c.lon, north: c.lat };
      box = box
        ? { west: Math.min(box.west, b.west), south: Math.min(box.south, b.south), east: Math.max(box.east, b.east), north: Math.max(box.north, b.north) }
        : b;
    }
    if (key === fkey) all = { key, box };
    return box;
  }

  /** Shows a box: fitted to the screen, at most zoom 15 for a single spot. */
  function fit(box: GeoBox) {
    if (!map || !L) return;
    const bounds = L.latLngBounds([box.south, box.west], [box.north, box.east]);
    const size = map.getSize();
    if (size.x < 50 || size.y < 50) {
      // Not laid out (or a tiny window): no measure to fit by.
      map.setView(bounds.getCenter(), 3);
      return;
    }
    map.fitBounds(bounds, { padding: [40, 40], maxZoom: 15 });
  }

  async function fitAll() {
    const key = fkey;
    try {
      const box = all?.key === key ? all.box : await loadAll(key);
      if (box) fit(box);
    } catch (e) {
      if (!isAbort(e)) error = failed(e);
    }
  }

  // ---- Sheet and lightbox ----

  function boundsOf(c: ShareCluster): GeoBox {
    return c.bounds ?? { west: c.lon, south: c.lat, east: c.lon, north: c.lat };
  }

  function openSheet(c: ShareCluster) {
    sheetAc?.abort();
    const ctl = new AbortController();
    sheetAc = ctl;
    const b = boundsOf(c);
    const spread = b.west !== b.east || b.south !== b.north;
    sheet = { cluster: c, items: [], total: c.count, loading: true, error: '', canZoom: spread && (map?.getZoom() ?? MAX_ZOOM) < MAX_ZOOM };
    client
      .getGeoItems(filters, b, SET_LIMIT, ctl.signal)
      .then((r) => {
        if (sheet?.cluster === c) sheet = { ...sheet, items: r.items, total: r.total, loading: false };
      })
      .catch((e: unknown) => {
        if (!isAbort(e) && sheet?.cluster === c) sheet = { ...sheet, loading: false, error: failed(e) };
      });
  }

  function closeSheet() {
    sheetAc?.abort();
    sheet = null;
  }

  function zoomIn() {
    if (!sheet || !map || !L) return;
    const b = boundsOf(sheet.cluster);
    closeSheet();
    map.fitBounds(L.latLngBounds([b.south, b.west], [b.north, b.east]), { padding: [48, 48], maxZoom: MAX_ZOOM });
  }

  /** The photos in view, newest first (the pages of a photo opened from a marker); one request per box. */
  function inViewItems(): Promise<MediaItem[]> {
    if (!map) return Promise.resolve([]);
    const box = currentBox(map);
    const key = `${fkey}|${box.west},${box.south},${box.east},${box.north}`;
    if (inView?.key !== key) {
      const p = client
        .getGeoItems(filters, box, SET_LIMIT)
        .then((r) => r.items)
        .catch(() => [] as MediaItem[]);
      inView = { key, items: p };
    }
    return inView.items;
  }

  let openedHere = false;
  function openPhoto(mid: number, item: MediaItem | null) {
    hint = item;
    openedHere = true;
    navigate(viewHref(mid));
  }

  function onSheetTap(e: MouseEvent, it: MediaItem) {
    if (e.button !== 0 || e.ctrlKey || e.metaKey || e.shiftKey) return;
    e.preventDefault();
    openPhoto(it.id, it);
  }

  /** Pages within the sheet when the photo is in it, else within the photos in view. */
  async function neighbours(item: MediaItem): Promise<Neighbours> {
    const fromSheet = sheet?.items ?? [];
    const list = fromSheet.some((x) => x.id === item.id) ? fromSheet : await inViewItems();
    const i = list.findIndex((x) => x.id === item.id);
    if (i < 0) return { prev: null, next: null };
    return { prev: list[i - 1] ?? null, next: list[i + 1] ?? null };
  }

  function show(item: MediaItem) {
    hint = item;
    navigate(viewHref(item.id), { replace: true });
  }

  function close() {
    if (openedHere) {
      openedHere = false;
      history.back();
    } else navigate(href(basePath, viewQuery()), { replace: true });
  }

  $effect(() => {
    if (viewId === null) openedHere = false;
  });

  function gone(mid: number) {
    if (sheet) sheet = { ...sheet, items: sheet.items.filter((x) => x.id !== mid), total: Math.max(0, sheet.total - 1) };
    inView = null;
    void refresh();
  }

  function patchRating(hash: string, rating: number) {
    if (!sheet) return;
    sheet = {
      ...sheet,
      items: sheet.items.map((x) => {
        if (x.hash !== hash) return x;
        const { rating: _old, ...rest } = x;
        return rating > 0 ? { ...rest, rating } : rest;
      }),
    };
  }

  // ---- The map ----

  /** The position into the route (replaced, so Back leaves the map in one step). */
  function saveView() {
    if (viewId !== null) return;
    navigate(href(basePath, viewQuery()), { replace: true });
  }

  onMount(() => {
    let disposed = false;
    void (async () => {
      try {
        // Leaflet is CommonJS; depending on the bundler interop its API is
        // on the namespace or on `default`.
        const mod: unknown = await import('leaflet');
        const ns = mod as Record<string, unknown>;
        const api = typeof ns.map === 'function' ? ns : (ns.default as Record<string, unknown> | undefined);
        if (!api || typeof api.map !== 'function') throw new Error('unexpected leaflet module');
        L = api as unknown as typeof Leaflet;
      } catch (e) {
        error = `Could not load the map: ${errorText(e)}`;
        return;
      }
      if (disposed || !el) return;
      const start = untrack(() => view);
      map = L.map(el, {
        center: start ? [start.lat, start.lon] : [30, 10],
        zoom: start ? start.zoom : 2,
        minZoom: 1,
        maxZoom: MAX_ZOOM,
        worldCopyJump: true,
        zoomControl: true,
      });
      map.attributionControl.setPrefix(false);
      const tiles = L.tileLayer(TILE_URL, {
        maxZoom: MAX_ZOOM,
        attribution: '&copy; <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener">OpenStreetMap</a>',
        // The share sends no-referrer; OSM's tile policy wants one. This
        // sends the share's origin (a LAN address), never a path.
        referrerPolicy: 'strict-origin-when-cross-origin',
      });
      tiles.on('tileerror', () => (tilesFailed = true));
      tiles.on('tileload', () => (tilesFailed = false));
      tiles.addTo(map);
      markers = L.layerGroup().addTo(map);
      map.on('moveend', () => {
        saveView();
        schedule();
      });
      ready = true;
      // The extent of every photo: the first view without a position, and "nothing here".
      if (!start) await fitAll();
      else void loadAll(untrack(() => fkey)).catch(() => {});
      if (disposed) return;
      saveView();
      void refresh();
    })();
    const onResize = () => map?.invalidateSize();
    window.addEventListener('resize', onResize);
    return () => {
      disposed = true;
      clearTimeout(timer);
      ac?.abort();
      sheetAc?.abort();
      window.removeEventListener('resize', onResize);
      map?.remove();
      map = null;
      markers = null;
    };
  });

  // Filters changed: the sheet closes, the extent is read again (and the
  // map refits when nothing of the new set is in view), the clusters too.
  let lastKey = untrack(() => fkey);
  $effect(() => {
    const key = fkey;
    if (!ready || key === lastKey) return;
    lastKey = key;
    untrack(() => {
      closeSheet();
      inView = null;
      void (async () => {
        try {
          const box = await loadAll(key);
          if (box && map && !map.getBounds().intersects(L!.latLngBounds([box.south, box.west], [box.north, box.east]))) fit(box);
        } catch (e) {
          if (!isAbort(e)) error = failed(e);
        }
        void refresh();
      })();
    });
  });

  // Back, forward or a link moved the position while the map is open.
  $effect(() => {
    const v = view;
    untrack(() => {
      if (!map || !v) return;
      const c = map.getCenter();
      if (Math.abs(c.lat - v.lat) > 1e-4 || Math.abs(c.wrap().lng - v.lon) > 1e-4 || map.getZoom() !== v.zoom) map.setView([v.lat, v.lon], v.zoom);
    });
  });

  const empty = $derived(all !== null && all.key === fkey && all.box === null);
  const sheetTitle = $derived(sheet ? `${formatCount(sheet.total)} ${sheet.total === 1 ? 'photo' : 'photos'} here` : '');
</script>

<div class="share-map" inert={viewId !== null ? true : undefined}>
  <div class="map" bind:this={el}></div>
  <div class="tools">
    <button class="tool" aria-label="Show all photos" title="Show all photos" onclick={() => void fitAll()} disabled={!ready || empty}>
      <svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"
        ><path d="M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg
      >
    </button>
  </div>
  {#if tilesFailed}
    <div class="note" role="status">No map background: the map tiles need the internet.</div>
  {/if}
  {#if error}
    <div class="note err" role="alert">{error} <button class="small" onclick={() => void refresh()}>Try again</button></div>
  {:else if loading}
    <div class="note busy" aria-live="polite">Loading…</div>
  {/if}
  {#if empty}
    <div class="empty-note" role="status">
      <h2>{filtered ? 'Nothing matches these filters' : 'No photos with a location'}</h2>
      {#if filtered}
        <p>No photo with a location matches them.</p>
        {#if onclearfilters}<button onclick={onclearfilters}>Clear filters</button>{/if}
      {:else}
        <p>{kind === 'album' ? 'None of the photos in this album has a location.' : 'None of these photos has a location.'}</p>
      {/if}
    </div>
  {/if}

  {#if sheet}
    <div class="sheet" role="dialog" aria-label={sheetTitle}>
      <div class="sheet-head">
        <strong>{sheetTitle}</strong>
        {#if sheet.canZoom}<button class="small" onclick={zoomIn}>Zoom in here</button>{/if}
        <button class="ghost icon" aria-label="Close" onclick={closeSheet}><span aria-hidden="true">×</span></button>
      </div>
      {#if sheet.error}
        <p class="muted small pad" role="alert">{sheet.error}</p>
      {:else if sheet.loading}
        <p class="muted small pad">Loading…</p>
      {:else}
        <div class="strip">
          {#each sheet.items as it (it.id)}
            <a href={viewHref(it.id)} onclick={(e) => onSheetTap(e, it)} aria-label="Open the photo">
              <img src={client.thumbUrl(it)} alt="" loading="lazy" decoding="async" />
              {#if it.type === 'video'}<span class="vid" aria-hidden="true">▶</span>{/if}
            </a>
          {/each}
        </div>
        {#if sheet.total > sheet.items.length}
          <p class="muted small pad">The newest {formatCount(sheet.items.length)} of {formatCount(sheet.total)}. Zoom in to see the others.</p>
        {/if}
      {/if}
    </div>
  {/if}
</div>

{#if viewId !== null}
  <ShareLightbox
    {client}
    id={viewId}
    hint={hint && hint.id === viewId ? hint : null}
    {kind}
    {albumHref}
    {neighbours}
    onnavigate={show}
    onclose={close}
    ongone={gone}
    onrated={patchRating}
    {onforbidden}
  />
{/if}

<style>
  .share-map {
    position: relative;
    height: 100%;
    /* A stacking context: Leaflet's panes and controls (z-index up to
       1000) stay under the app's menus, sheets and the lightbox. */
    z-index: 0;
    overflow: hidden;
  }
  .map {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    background: var(--surface-2);
  }
  .map:global(.leaflet-container) {
    font: inherit;
  }
  .map :global(.leaflet-control-zoom a) {
    width: 40px;
    height: 40px;
    line-height: 40px;
  }
  .map :global(.sm-marker) {
    border-radius: 8px;
    border: 2px solid #fff;
    background: var(--placeholder);
    box-shadow: 0 1px 4px rgba(0, 0, 0, 0.45);
  }
  .map :global(.sm-marker img) {
    display: block;
    width: 100%;
    height: 100%;
    object-fit: cover;
    border-radius: 6px;
  }
  .map :global(.sm-marker .n) {
    position: absolute;
    top: -9px;
    right: -11px;
    min-width: 22px;
    height: 22px;
    padding: 0 6px;
    border-radius: 11px;
    background: var(--accent-strong);
    color: #fff;
    font-size: 12px;
    font-weight: 600;
    line-height: 22px;
    text-align: center;
    box-shadow: 0 1px 3px rgba(0, 0, 0, 0.4);
  }
  .tools {
    position: absolute;
    top: 10px;
    right: calc(10px + env(safe-area-inset-right, 0px));
    z-index: 1001;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .tool {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 44px;
    height: 44px;
    padding: 0;
    border-radius: var(--radius-sm);
    background: var(--surface);
    box-shadow: var(--shadow);
  }
  .note {
    position: absolute;
    left: 50%;
    bottom: calc(28px + env(safe-area-inset-bottom, 0px));
    transform: translateX(-50%);
    z-index: 1001;
    max-width: calc(100% - 24px);
    padding: 6px 12px;
    border-radius: var(--radius-sm);
    background: var(--surface);
    border: 1px solid var(--border);
    box-shadow: var(--shadow);
    font-size: 13px;
    text-align: center;
  }
  .note.err {
    bottom: calc(72px + env(safe-area-inset-bottom, 0px));
    border-color: var(--danger);
  }
  .note.busy {
    top: 10px;
    bottom: auto;
  }
  .empty-note {
    position: absolute;
    left: 50%;
    top: 30%;
    transform: translateX(-50%);
    z-index: 1001;
    width: min(320px, calc(100% - 32px));
    padding: 12px 16px;
    border-radius: var(--radius);
    background: var(--surface);
    box-shadow: var(--shadow);
    text-align: center;
  }
  .empty-note h2 {
    margin: 4px 0;
    font-size: 17px;
  }
  .sheet {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    z-index: 1002;
    padding-bottom: env(safe-area-inset-bottom, 0px);
    border-top: 1px solid var(--border);
    border-radius: var(--radius) var(--radius) 0 0;
    background: var(--surface);
    box-shadow: 0 -4px 16px rgba(0, 0, 0, 0.25);
  }
  .sheet-head {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 48px;
    padding: 0 4px 0 calc(14px + env(safe-area-inset-left, 0px));
  }
  .sheet-head strong {
    flex: 1;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .icon {
    min-width: 44px;
    min-height: 44px;
    padding: 0;
    font-size: 22px;
  }
  .strip {
    display: flex;
    gap: 4px;
    overflow-x: auto;
    overflow-y: hidden;
    padding: 0 8px 10px;
    -webkit-overflow-scrolling: touch;
    overscroll-behavior: contain;
  }
  .strip a {
    position: relative;
    flex: none;
    display: block;
    width: 96px;
    height: 96px;
    border-radius: 6px;
    overflow: hidden;
    background: var(--placeholder);
  }
  .strip img {
    display: block;
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .vid {
    position: absolute;
    right: 6px;
    bottom: 4px;
    color: #fff;
    font-size: 14px;
    text-shadow: 0 0 3px rgba(0, 0, 0, 0.8);
  }
  .pad {
    margin: 0;
    padding: 0 14px 12px;
  }
</style>
