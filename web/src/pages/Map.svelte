<script lang="ts">
  import 'leaflet/dist/leaflet.css';
  import type * as Leaflet from 'leaflet';
  import { onMount, untrack } from 'svelte';
  import {
    errorMessage,
    getGeoClusters,
    getGeoItems,
    isAbort,
    isHash,
    thumbUrl,
    type BBox,
    type Filters,
    type GeoCluster,
    type MediaDetail,
    type MediaItem,
  } from '../lib/api';
  import { filtersKey, filtersToQuery } from '../lib/filters';
  import { formatCount, formatWallDate } from '../lib/format';
  import { href, navigate } from '../lib/router';
  import { isNumber, loadJSON, saveJSON } from '../lib/storage';
  import { openViewer, setSequenceProvider } from '../lib/viewer';
  import { jobs } from '../lib/jobs.svelte';
  import FiltersBar from '../components/Filters.svelte';

  interface Props {
    filters: Filters;
    query: URLSearchParams;
  }
  let { filters, query }: Props = $props();

  const VIEW_KEY = 'picmover.map.view';
  const PANEL_LIMIT = 200;
  const DEBOUNCE_MS = 150;

  interface SavedView {
    lat: number;
    lon: number;
    zoom: number;
  }
  function isSavedView(v: unknown): v is SavedView {
    if (typeof v !== 'object' || v === null) return false;
    const r = v as Record<string, unknown>;
    return isNumber(r.lat) && isNumber(r.lon) && isNumber(r.zoom);
  }

  /** ?at=lat,lon&z= from a "Show on map" link. */
  function viewFromQuery(q: URLSearchParams): SavedView | null {
    const at = q.get('at');
    if (!at) return null;
    const [a, b] = at.split(',').map(Number);
    if (!isNumber(a) || !isNumber(b) || Math.abs(a) > 90 || Math.abs(b) > 180) return null;
    const z = Number(q.get('z'));
    return { lat: a, lon: b, zoom: Number.isFinite(z) && z >= 0 && z <= 19 ? z : 15 };
  }

  let el: HTMLDivElement | undefined = $state();
  let tilesFailed = $state(false);
  let error = $state('');
  let loading = $state(false);
  let panelItems = $state.raw<MediaItem[]>([]);
  let panelTotal = $state(0);
  let ready = $state(false);

  // Leaflet objects are not reactive state.
  let L: typeof Leaflet | null = null;
  let map: Leaflet.Map | null = null;
  let markers: Leaflet.LayerGroup | null = null;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let ac: AbortController | null = null;

  /** The map bounds as an API bbox, wrapped to [-180, 180]. */
  function currentBBox(m: Leaflet.Map): BBox {
    const b = m.getBounds();
    const south = Math.max(-90, b.getSouth());
    const north = Math.min(90, b.getNorth());
    let west = b.getWest();
    let east = b.getEast();
    if (east - west >= 360) {
      west = -180;
      east = 180;
    } else {
      const wrap = (x: number) => ((((x + 180) % 360) + 360) % 360) - 180;
      west = wrap(west);
      east = wrap(east);
      if (east === -180) east = 180;
      // west > east now means the box crosses the antimeridian; the API handles it.
    }
    return { west, south, east, north };
  }

  function clusterIcon(c: GeoCluster): Leaflet.DivIcon {
    const Lm = L as typeof Leaflet;
    if (c.count === 1 && typeof c.id === 'number' && isHash(c.hash)) {
      return Lm.divIcon({
        className: 'pm-single',
        html: `<img src="${thumbUrl({ hash: c.hash, thumbAt: c.thumbAt })}" alt="" loading="lazy" decoding="async">`,
        iconSize: [40, 40],
        iconAnchor: [20, 20],
      });
    }
    const size = Math.round(28 + Math.min(26, 7 * Math.log10(Math.max(1, c.count))));
    return Lm.divIcon({
      className: 'pm-cluster',
      html: `<span>${formatCount(Math.trunc(c.count))}</span>`,
      iconSize: [size, size],
      iconAnchor: [size / 2, size / 2],
    });
  }

  function render(clusters: GeoCluster[]) {
    if (!L || !map || !markers) return;
    markers.clearLayers();
    const centerLng = map.getCenter().lng;
    for (const c of clusters) {
      // Show the marker in the world copy nearest the view centre.
      const lon = c.lon + 360 * Math.round((centerLng - c.lon) / 360);
      const m = L.marker([c.lat, lon], { icon: clusterIcon(c), keyboard: true, title: c.count === 1 ? 'Open' : `${c.count} items` });
      const id = c.id;
      if (c.count === 1 && typeof id === 'number') {
        m.on('click', () => openViewer(id));
      } else {
        m.on('click', () => map?.setView([c.lat, lon], Math.min(map.getMaxZoom(), map.getZoom() + 2)));
      }
      markers.addLayer(m);
    }
  }

  async function refresh() {
    if (!map) return;
    const bbox = currentBBox(map);
    const zoom = map.getZoom();
    const f = filters;
    ac?.abort();
    const ctl = new AbortController();
    ac = ctl;
    loading = true;
    try {
      const [clusters, items] = await Promise.all([
        getGeoClusters(f, bbox, zoom, ctl.signal),
        getGeoItems(f, bbox, PANEL_LIMIT, ctl.signal),
      ]);
      if (ctl.signal.aborted) return;
      error = '';
      render(clusters);
      panelItems = items.items;
      panelTotal = items.total;
    } catch (e) {
      if (!isAbort(e) && !ctl.signal.aborted) error = errorMessage(e);
    } finally {
      if (ac === ctl) loading = false;
    }
  }

  function schedule() {
    clearTimeout(timer);
    timer = setTimeout(() => void refresh(), DEBOUNCE_MS);
  }

  function saveView() {
    if (!map) return;
    const c = map.getCenter();
    saveJSON(VIEW_KEY, { lat: c.lat, lon: c.wrap().lng, zoom: map.getZoom() } satisfies SavedView);
  }

  onMount(() => {
    let disposed = false;
    let ro: ResizeObserver | null = null;
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
        error = `Could not load the map: ${errorMessage(e)}`;
        return;
      }
      if (disposed || !el) return;
      const initial = viewFromQuery(untrack(() => query)) ?? loadJSON(VIEW_KEY, isSavedView) ?? { lat: 30, lon: 10, zoom: 2 };
      map = L.map(el, { center: [initial.lat, initial.lon], zoom: initial.zoom, worldCopyJump: true, minZoom: 1 });
      const tiles = L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
        subdomains: 'abc',
        maxZoom: 19,
        attribution: '&copy; <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener">OpenStreetMap</a> contributors',
        // The app sends no-referrer by default; OSM's tile policy wants one.
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
      ro = new ResizeObserver(() => map?.invalidateSize());
      ro.observe(el);
      ready = true; // the filters effect below does the first fetch
    })();
    return () => {
      disposed = true;
      clearTimeout(timer);
      ac?.abort();
      ro?.disconnect();
      map?.remove();
      map = null;
      markers = null;
    };
  });

  // Filters changed, or a job finished: refetch.
  const fkey = $derived(filtersKey(filters));
  $effect(() => {
    void fkey;
    void jobs.doneCount;
    if (ready) untrack(() => void refresh());
  });

  // A "Show on map" link while the map is already open.
  let lastAt = untrack(() => query.get('at') ?? '');
  $effect(() => {
    const at = query.get('at') ?? '';
    if (at === lastAt) return;
    lastAt = at;
    const v = viewFromQuery(query);
    if (v && map) map.setView([v.lat, v.lon], v.zoom);
  });

  // The lightbox steps through the side panel's order.
  $effect(() =>
    setSequenceProvider({
      async neighbours(item: MediaDetail) {
        const list = panelItems;
        const i = list.findIndex((x) => x.id === item.id);
        if (i < 0) return { prev: null, next: null };
        return { prev: list[i - 1] ?? null, next: list[i + 1] ?? null };
      },
    }),
  );

  function applyFilters(f: Filters) {
    navigate(href('/map', filtersToQuery(f)));
  }
  function open(e: MouseEvent, id: number) {
    if (e.button !== 0 || e.ctrlKey || e.metaKey || e.shiftKey) return;
    e.preventDefault();
    openViewer(id);
  }
</script>

<div class="map-page">
  <div class="toolbar">
    <FiltersBar {filters} onchange={applyFilters} hideGps />
    <div class="spacer"></div>
    {#if loading}<span class="muted small">Loading…</span>{/if}
  </div>
  <div class="body">
    <div class="map-wrap">
      <div class="map" bind:this={el}></div>
      {#if tilesFailed}
        <div class="note">Map tiles need an internet connection. Markers still work.</div>
      {/if}
      {#if error}
        <div class="note err">{error}</div>
      {/if}
    </div>
    <aside class="panel" aria-label="Items in view">
      <div class="panel-head">
        {#if panelTotal > panelItems.length}
          {formatCount(panelItems.length)} of {formatCount(panelTotal)} in view
        {:else}
          {formatCount(panelTotal)} in view
        {/if}
      </div>
      {#if panelItems.length === 0 && !loading}
        <p class="muted small pad">No photos with a location in this area. Zoom out or move the map.</p>
      {/if}
      <div class="thumbs">
        {#each panelItems as it (it.id)}
          <a href="#/view/{it.id}" onclick={(e) => open(e, it.id)} title={formatWallDate(it.takenAt)}>
            <img src={thumbUrl(it)} alt="" loading="lazy" decoding="async" />
          </a>
        {/each}
      </div>
    </aside>
  </div>
</div>

<style>
  .map-page {
    display: flex;
    flex-direction: column;
    height: 100%;
  }
  .toolbar {
    flex: none;
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 10px;
    padding: 8px 16px;
    border-bottom: 1px solid var(--border);
  }
  .body {
    flex: 1;
    min-height: 0;
    display: flex;
  }
  .map-wrap {
    position: relative;
    flex: 1;
    min-width: 0;
  }
  .map {
    position: absolute;
    inset: 0;
    background: var(--surface-2);
  }
  .note {
    position: absolute;
    left: 50%;
    top: 12px;
    transform: translateX(-50%);
    z-index: 1000;
    padding: 6px 12px;
    border-radius: var(--radius-sm);
    background: var(--surface);
    border: 1px solid var(--border);
    box-shadow: var(--shadow);
    font-size: 13px;
  }
  .note.err {
    top: 52px;
    border-color: var(--danger);
  }
  .panel {
    flex: none;
    width: 300px;
    display: flex;
    flex-direction: column;
    border-left: 1px solid var(--border);
    background: var(--surface);
  }
  .panel-head {
    flex: none;
    padding: 10px 12px;
    font-weight: 600;
    border-bottom: 1px solid var(--border);
  }
  .pad {
    padding: 0 12px;
  }
  .thumbs {
    flex: 1;
    overflow-y: auto;
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    align-content: start;
    gap: 3px;
    padding: 3px;
  }
  .thumbs a {
    display: block;
    aspect-ratio: 1;
    background: var(--placeholder);
    overflow: hidden;
  }
  .thumbs img {
    display: block;
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .map:global(.leaflet-container) {
    font: inherit;
  }
  @media (prefers-color-scheme: dark) {
    .map :global(.leaflet-tile-pane) {
      filter: brightness(0.8) contrast(1.08) saturate(0.85);
    }
  }
  @media (max-width: 900px) {
    .panel {
      width: 210px;
    }
    .thumbs {
      grid-template-columns: repeat(2, 1fr);
    }
  }
</style>
