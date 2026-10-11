<script lang="ts">
  import { tick, untrack, type Snippet } from 'svelte';
  import { SvelteMap } from 'svelte/reactivity';
  import { errorMessage, type Filters, type LibApi, type MediaDetail, type MediaItem, type TimelineBucket } from '../lib/api';
  import { layoutTimeline, type BucketLayout } from '../lib/timeline';
  import { bucketLabel, formatCount, parseWallClock } from '../lib/format';
  import { filtersKey, filtersToQuery, hasFilters } from '../lib/filters';
  import { href, navigate } from '../lib/router';
  import { libHref } from '../lib/routes';
  import { library } from '../lib/library.svelte';
  import { jobs } from '../lib/jobs.svelte';
  import { selection, type OrderSegment } from '../lib/selection.svelte';
  import { onChange, type Change } from '../lib/changes';
  import { setSequenceProvider, type Neighbours } from '../lib/viewer';
  import { isNumber, loadJSON, saveJSON } from '../lib/storage';
  import Tile from '../components/Tile.svelte';
  import Scrubber from '../components/Scrubber.svelte';
  import FiltersBar from '../components/Filters.svelte';
  import SelectionBar from '../components/SelectionBar.svelte';

  interface Props {
    api: LibApi;
    /** Filters from the URL query. */
    filters: Filters;
    /** Item open in the lightbox, null when closed. */
    viewId: number | null;
    /** grid: all photos; album: one album (albumId); import: one import record (importId); trash: trashed items. */
    mode?: 'grid' | 'album' | 'import' | 'trash';
    albumId?: number | null;
    importId?: number | null;
    /** Router path of this page, for filter links ("/l/peter/albums/3"). */
    basePath: string;
    /** Page header above the toolbar (album name, Empty trash). */
    header?: Snippet;
  }
  let { api, filters, viewId, mode = 'grid', albumId = null, importId = null, basePath, header }: Props = $props();

  /** The filters sent to the server: the URL's plus what the page implies. */
  const effective = $derived.by<Filters>(() => {
    if (mode === 'trash') return { ...filters, trashed: true };
    if (mode === 'album' && albumId !== null) return { ...filters, album: albumId };
    if (mode === 'import' && importId !== null) return { ...filters, import: importId };
    return filters;
  });

  const GAP = 4;
  const HEADER = 44;
  const BUCKET_PAD = 12;
  const SIDE = 16; // left padding of the content
  const SCRUB = 56; // right gutter for the scrubber
  const MIN_TILE = 100;
  const MAX_TILE = 320;
  const TILE_KEY = 'picmover.grid.tileSize';

  const clampTile = (n: number) => Math.round(Math.min(MAX_TILE, Math.max(MIN_TILE, n)));

  let tileSize = $state(clampTile(loadJSON(TILE_KEY, isNumber) ?? 160));
  let width = $state(0);
  let height = $state(0);
  let scrollTop = $state(0);
  let scroller: HTMLDivElement | undefined = $state();

  let buckets = $state.raw<TimelineBucket[]>([]);
  let tlStatus = $state<'loading' | 'ready' | 'error'>('loading');
  let tlError = $state('');
  const items = new SvelteMap<string, MediaItem[]>();
  // Lists from before an in-place reload (a job finished), shown until the
  // fresh list arrives so tiles do not flash.
  const stale = new SvelteMap<string, MediaItem[]>();
  const bucketErrors = new SvelteMap<string, string>();
  const inflight = new Map<string, Promise<MediaItem[] | null>>();
  let gen = 0;
  // Bumped whenever a timeline is committed. The fetch effect below reads
  // it, so buckets are re-requested even when the visible key set did not
  // change (a fetch dropped as stale by a filter change would otherwise
  // leave them as placeholders for good).
  let epoch = $state(0);

  const bkey = (year: number, month: number) => `${year}-${month}`;

  const contentWidth = $derived(Math.max(0, width - SIDE - SCRUB));
  const timeline = $derived(
    layoutTimeline(buckets, { width: contentWidth, tileSize, gap: GAP, headerHeight: HEADER, bucketPadding: BUCKET_PAD }),
  );
  /** Height of the selection bar while shown; the canvas grows by it (plus its margin) so the last row can scroll clear of it. */
  let barHeight = $state(0);
  const BAR_MARGIN = 24;
  const padBottom = $derived(barHeight > 0 ? barHeight + BAR_MARGIN : 0);
  const totalCount = $derived(buckets.reduce((n, b) => n + b.count, 0));

  // ---- Data ----

  function loadBucket(year: number, month: number): Promise<MediaItem[] | null> {
    const k = bkey(year, month);
    const have = items.get(k);
    if (have) return Promise.resolve(have);
    const pending = inflight.get(k);
    if (pending) return pending;
    const g = gen;
    const f = effective;
    const p = api.getBucket(f, year, month)
      .then((list) => {
        if (g !== gen) return null;
        items.set(k, list);
        bucketErrors.delete(k);
        fixCount(year, month, list.length);
        return list;
      })
      .catch((e: unknown) => {
        if (g === gen) bucketErrors.set(k, errorMessage(e));
        return null;
      })
      .finally(() => {
        if (inflight.get(k) === p) inflight.delete(k);
      });
    inflight.set(k, p);
    return p;
  }

  /**
   * The timeline's count for a bucket can be out of date (a job is still
   * running, or files changed outside the app). The fetched list wins: the
   * bucket is re-laid out to its real length so there are neither
   * placeholders that never fill nor items that cannot be seen.
   */
  function fixCount(year: number, month: number, n: number) {
    const b = buckets.find((x) => x.year === year && x.month === month);
    if (!b || b.count === n) return;
    const restore = captureAnchor();
    buckets =
      n > 0
        ? buckets.map((x) => (x === b ? { ...x, count: n } : x))
        : buckets.filter((x) => x !== b);
    restore?.();
  }

  /** Settles when the latest timeline request has. */
  let ready: Promise<void> = Promise.resolve();

  function loadTimeline(resetScroll: boolean): Promise<void> {
    ready = fetchTimeline(resetScroll);
    return ready;
  }

  async function fetchTimeline(resetScroll: boolean) {
    const g = ++gen;
    inflight.clear();
    tlStatus = buckets.length && !resetScroll ? tlStatus : 'loading';
    try {
      const b = await api.getTimeline(effective);
      if (g !== gen) return;
      stale.clear();
      if (!resetScroll) for (const [k, v] of items) stale.set(k, v);
      items.clear();
      bucketErrors.clear();
      buckets = b;
      epoch++;
      tlStatus = 'ready';
      if (resetScroll && scroller) {
        scroller.scrollTop = 0;
        scrollTop = 0;
      }
    } catch (e) {
      if (g !== gen) return;
      tlError = errorMessage(e);
      tlStatus = 'error';
    }
  }

  // Filters changed (or first mount): reload and go to the top, and start
  // a new selection.
  const fkey = $derived(`${api.id}|${mode}|${albumId ?? ''}|${importId ?? ''}|${filtersKey(effective)}`);
  $effect(() => {
    const k = fkey;
    untrack(() => {
      selection.setScope(k);
      void loadTimeline(true);
    });
  });

  // ---- In-place updates after actions (selection bar, lightbox) ----

  /** Drops items from the loaded buckets and shrinks the layout to match. */
  function removeHashes(hashes: readonly string[]) {
    if (!hashes.length) return;
    const gone = new Set(hashes);
    for (const map of [items, stale]) {
      for (const [k, list] of map) {
        const next = list.filter((x) => !gone.has(x.hash));
        if (next.length === list.length) continue;
        map.set(k, next);
        if (map === items) {
          const [y, m] = k.split('-').map(Number) as [number, number];
          fixCount(y, m, next.length);
        }
      }
    }
    selection.remove(hashes);
  }

  function patchRating(hashes: readonly string[], rating: number) {
    const set = new Set(hashes);
    for (const map of [items, stale]) {
      for (const [k, list] of map) {
        if (!list.some((x) => set.has(x.hash))) continue;
        map.set(
          k,
          list.map((x) => {
            if (!set.has(x.hash)) return x;
            const { rating: _old, ...rest } = x;
            return rating > 0 ? { ...rest, rating } : rest;
          }),
        );
      }
    }
  }

  function applyChange(c: Change) {
    switch (c.kind) {
      case 'rating':
        patchRating(c.hashes, c.rating);
        break;
      case 'trashed':
        if (mode === 'trash') void loadTimeline(false);
        else removeHashes(c.hashes);
        break;
      case 'restored':
        if (mode === 'trash') removeHashes(c.hashes);
        else void loadTimeline(false);
        break;
      case 'deleted':
        if (mode === 'trash' && c.hashes.length) removeHashes(c.hashes);
        else if (mode === 'trash') {
          // Empty trash (no hashes): everything listed here is gone, the selection too.
          selection.clear();
          void loadTimeline(false);
        }
        break;
      case 'album':
        if (mode !== 'album' || c.albumId !== albumId) break;
        removeHashes(c.removed);
        if (c.added.length) void loadTimeline(false);
        break;
      case 'tags':
        if (effective.tags?.length) void loadTimeline(false);
        break;
    }
  }
  $effect(() => onChange((c) => untrack(() => applyChange(c))));

  // ---- Selection ----

  /** The loaded months in display order, for Shift-click ranges. */
  function segments(): OrderSegment[] {
    return buckets.map((b) => {
      const list = items.get(bkey(b.year, b.month));
      return { count: b.count, hashes: list ? list.map((x) => x.hash) : null };
    });
  }

  function onSelect(item: MediaItem, range: boolean) {
    if (range) selection.selectRange(segments(), item.hash);
    else selection.toggle(item.hash);
  }

  /** The rating every selected loaded item shares, else 0. */
  const commonRating = $derived.by(() => {
    if (selection.size === 0) return 0;
    let r: number | null = null;
    for (const list of items.values()) {
      for (const x of list) {
        if (!selection.peek(x.hash)) continue;
        const v = x.rating ?? 0;
        if (r === null) r = v;
        else if (r !== v) return 0;
      }
    }
    return r ?? 0;
  });

  function onKeydown(e: KeyboardEvent) {
    if (e.key !== 'Escape' || viewId !== null || selection.size === 0 || e.defaultPrevented) return;
    const t = e.target;
    if (t instanceof HTMLElement && (t.isContentEditable || ['INPUT', 'SELECT', 'TEXTAREA'].includes(t.tagName)) && !(t instanceof HTMLInputElement && t.type === 'checkbox')) return;
    e.preventDefault();
    selection.clear();
  }

  // The trash page says "deleted 3 minutes ago"; keep it current.
  let now = $state(Date.now());
  $effect(() => {
    if (mode !== 'trash') return;
    const t = setInterval(() => (now = Date.now()), 60_000);
    return () => clearInterval(t);
  });

  // A job finished: counts changed; reload in place.
  let seenDone = untrack(() => jobs.doneCount);
  $effect(() => {
    const d = jobs.doneCount;
    if (d === seenDone) return;
    seenDone = d;
    untrack(() => void loadTimeline(false));
  });

  // ---- Measuring and scrolling ----

  $effect(() => {
    const el = scroller;
    if (!el) return;
    const ro = new ResizeObserver((entries) => {
      // A width change can change the column count: keep the same items in view.
      const restore = width > 0 ? captureAnchor() : null;
      for (const e of entries) {
        width = e.contentRect.width;
        height = e.contentRect.height;
      }
      restore?.();
    });
    ro.observe(el);
    return () => ro.disconnect();
  });

  let raf = 0;
  function onScroll() {
    if (raf) return;
    raf = requestAnimationFrame(() => {
      raf = 0;
      if (scroller) scrollTop = scroller.scrollTop;
    });
  }
  $effect(() => () => {
    if (raf) cancelAnimationFrame(raf);
  });

  function scrollToY(y: number) {
    if (!scroller) return;
    const max = Math.max(0, timeline.totalHeight + padBottom - height);
    const t = Math.max(0, Math.min(max, y));
    scroller.scrollTop = t;
    scrollTop = t;
  }

  /**
   * Remembers which item row is at the top of the view; the returned
   * function scrolls back to it after the layout changed (tile size,
   * column count, a bucket's count). Null when there is nothing to anchor.
   */
  function captureAnchor(): (() => void) | null {
    const tl = timeline;
    const i = tl.indexAtOffset(scrollTop);
    const b = tl.buckets[i];
    if (!b) return null;
    const key = bkey(b.year, b.month);
    const y = scrollTop - b.rowsTop;
    // In the header: keep the offset into the bucket. In the rows: keep the
    // first item of the top row and how far into that row we are.
    let idx = -1;
    let frac = 0;
    const headerOff = scrollTop - b.top;
    if (y >= 0 && b.rows > 0) {
      const row = Math.min(b.rows - 1, Math.floor(y / tl.rowPitch));
      idx = row * tl.columns;
      frac = Math.min(1, Math.max(0, (y - row * tl.rowPitch) / tl.rowPitch));
    }
    const before = scrollTop;
    return () => {
      const nt = timeline;
      const nb = nt.buckets.find((x) => bkey(x.year, x.month) === key);
      if (!nb) return;
      const target =
        idx < 0
          ? nb.top + Math.min(headerOff, nb.height)
          : nb.rowsTop + Math.floor(Math.min(idx, Math.max(0, nb.count - 1)) / nt.columns) * nt.rowPitch + frac * nt.rowPitch;
      if (Math.abs(target - before) < 0.5) return;
      scrollTop = target; // render the right rows now; the DOM follows after the update
      void tick().then(() => scrollToY(target));
    };
  }

  /** Changes the tile size keeping the same place in the timeline in view. */
  function setTileSize(n: number) {
    const next = clampTile(n);
    if (next === tileSize) return;
    const restore = captureAnchor();
    tileSize = next;
    saveJSON(TILE_KEY, next);
    restore?.();
  }

  // Ctrl+wheel zooms the grid; needs a non-passive listener. Trackpad
  // pinches arrive as many tiny deltas, so the unrounded size is kept here
  // and only the rounded one is applied; it is resynced when the size was
  // changed by other means (the slider).
  let zoomRaw: number | null = null;
  $effect(() => {
    const el = scroller;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      if (!e.ctrlKey) return;
      e.preventDefault();
      const cur = untrack(() => tileSize);
      if (zoomRaw === null || Math.abs(zoomRaw - cur) >= 1) zoomRaw = cur;
      const scale = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? 400 : 1; // lines / pages
      zoomRaw = Math.min(MAX_TILE, Math.max(MIN_TILE, zoomRaw * Math.exp((-e.deltaY * scale) / 400)));
      setTileSize(zoomRaw);
    };
    el.addEventListener('wheel', onWheel, { passive: false });
    return () => el.removeEventListener('wheel', onWheel);
  });

  // ---- Virtualisation ----

  interface TileView {
    key: string;
    item: MediaItem | undefined;
    left: number;
    top: number;
  }
  interface BucketView {
    b: BucketLayout;
    key: string;
    label: string;
    tiles: TileView[];
    error: string | undefined;
  }

  const view = $derived.by((): BucketView[] => {
    const tl = timeline;
    const overscan = Math.max(height, 400);
    const [b0, b1] = tl.visibleBuckets(scrollTop, height || 800, overscan);
    const out: BucketView[] = [];
    for (let i = b0; i < b1; i++) {
      const b = tl.buckets[i];
      if (!b) continue;
      const k = bkey(b.year, b.month);
      const list = items.get(k) ?? stale.get(k);
      const [r0, r1] = tl.visibleRows(i, scrollTop, height || 800, overscan);
      const tiles: TileView[] = [];
      const end = Math.min(b.count, r1 * tl.columns);
      for (let idx = r0 * tl.columns; idx < end; idx++) {
        const pos = tl.tilePosition(i, idx);
        const item = list?.[idx];
        tiles.push({
          key: item ? `i${item.id}` : `p${k}-${idx}`,
          item,
          left: pos.left,
          top: pos.top - b.top,
        });
      }
      out.push({ b, key: k, label: bucketLabel(b.year, b.month), tiles, error: bucketErrors.get(k) });
    }
    return out;
  });

  // Fetch the visible buckets that are not loaded yet. Keyed on the set of
  // keys so plain scrolling inside a bucket does not re-run this; a short
  // delay skips buckets that only flash by while dragging the scrubber.
  const wanted = $derived(
    view
      .filter((v) => !items.has(v.key) && !v.error)
      .map((v) => `${v.b.year}:${v.b.month}`)
      .join(','),
  );
  $effect(() => {
    void epoch; // re-run after every committed timeline, see `epoch`
    if (!wanted) return;
    const list = wanted.split(',').map((s) => s.split(':').map(Number) as [number, number]);
    const t = setTimeout(() => {
      for (const [y, m] of list) void loadBucket(y, m);
    }, 60);
    return () => clearTimeout(t);
  });

  // ---- Lightbox order ----

  function bucketIndexFor(takenAt: string | null): number {
    const d = parseWallClock(takenAt);
    const y = d ? d.getUTCFullYear() : 0;
    const m = d ? d.getUTCMonth() + 1 : 0;
    return buckets.findIndex((b) => b.year === y && b.month === m);
  }

  async function firstOrLast(from: number, step: 1 | -1): Promise<MediaItem | null> {
    for (let i = from; i >= 0 && i < buckets.length; i += step) {
      const b = buckets[i];
      if (!b || b.count === 0) continue;
      const list = await loadBucket(b.year, b.month);
      if (list && list.length) return step === 1 ? (list[0] ?? null) : (list[list.length - 1] ?? null);
    }
    return null;
  }

  let reveal: MediaDetail | null = null;

  $effect(() =>
    setSequenceProvider({
      async neighbours(item: MediaDetail): Promise<Neighbours> {
        await ready;
        const bi = bucketIndexFor(item.takenAt);
        const b = buckets[bi];
        if (!b) return { prev: null, next: null };
        const list = await loadBucket(b.year, b.month);
        const idx = list ? list.findIndex((x) => x.id === item.id) : -1;
        if (!list || idx < 0) return { prev: null, next: null };
        const prev = idx > 0 ? (list[idx - 1] ?? null) : await firstOrLast(bi - 1, -1);
        const next = idx < list.length - 1 ? (list[idx + 1] ?? null) : await firstOrLast(bi + 1, 1);
        return { prev, next };
      },
      shown(item: MediaDetail) {
        reveal = item;
      },
    }),
  );

  // When the lightbox closes, make sure the last viewed item is on screen.
  $effect(() => {
    if (viewId !== null) return;
    const item = reveal;
    reveal = null;
    if (!item) return;
    untrack(() => {
      const bi = bucketIndexFor(item.takenAt);
      const b = buckets[bi];
      const list = b ? items.get(bkey(b.year, b.month)) : undefined;
      const idx = list ? list.findIndex((x) => x.id === item.id) : -1;
      if (!b || idx < 0) return;
      const pos = timeline.tilePosition(bi, idx);
      if (pos.top < scrollTop + HEADER || pos.top + pos.size > scrollTop + height) {
        scrollToY(pos.top - Math.max(HEADER, (height - pos.size) / 2));
      }
    });
  });

  function applyFilters(f: Filters) {
    navigate(href(basePath, filtersToQuery(f)));
  }

  const libraryEmpty = $derived(library.info !== null && library.info.summary.total === 0);
  const filtered = $derived(hasFilters(filters, { album: mode === 'album', import: mode === 'import' }));
</script>

<svelte:window onkeydown={onKeydown} />

<div class="grid-page">
  {@render header?.()}
  <div class="toolbar">
    <FiltersBar {filters} onchange={applyFilters} hideAlbum={mode === 'album'} hideImport={mode === 'import'} />
    <div class="spacer"></div>
    {#if tlStatus === 'ready'}
      <span class="muted small count">{formatCount(totalCount)} items</span>
    {/if}
    <label class="size" title="Tile size (Ctrl + mouse wheel)">
      <span class="sr-only">Tile size</span>
      <span aria-hidden="true" class="muted">▫</span>
      <input
        type="range"
        min={MIN_TILE}
        max={MAX_TILE}
        step="10"
        value={tileSize}
        oninput={(e) => setTileSize(Number(e.currentTarget.value))}
      />
      <span aria-hidden="true" class="muted big">◻</span>
    </label>
  </div>

  <div class="viewport">
    <div class="scroller" bind:this={scroller} onscroll={onScroll} tabindex="-1" data-focus-return>
      {#if tlStatus === 'error'}
        <div class="empty">
          <h2>Could not load the timeline</h2>
          <p>{tlError}</p>
          <button onclick={() => loadTimeline(false)}>Retry</button>
        </div>
      {:else if tlStatus === 'loading' && buckets.length === 0}
        <div class="empty">Loading…</div>
      {:else if tlStatus === 'ready' && buckets.length === 0}
        <div class="empty">
          {#if mode === 'trash' && !filtered}
            <h2>The trash is empty</h2>
            <p>Items you delete wait here until you empty the trash.</p>
          {:else if mode === 'album' && !filtered}
            <h2>This album is empty</h2>
            <p>Select photos in Photos and add them with the Album button.</p>
            <a class="btn" href={libHref(api.id, '')}>Go to Photos</a>
          {:else if mode === 'import' && !filtered}
            <h2>Nothing from this import is in the library</h2>
            <p>Its photos and videos were removed or are in the trash.</p>
          {:else if libraryEmpty && !filtered}
            <h2>Your library is empty</h2>
            <p>Import photos and videos from a camera card, phone backup or folder.</p>
            <a class="btn primary" href={libHref(api.id, 'import')}>Import media</a>
          {:else}
            <h2>Nothing matches these filters</h2>
            <button onclick={() => applyFilters({})}>Clear filters</button>
          {/if}
        </div>
      {:else}
        <div class="canvas" style:height="{timeline.totalHeight + padBottom}px">
          {#each view as v (v.key)}
            <section class="bucket" style:top="{v.b.top}px" style:height="{v.b.height}px" aria-label={v.label}>
              <h2 class="header">
                <span>{v.label}</span>
                <span class="n">{formatCount(v.b.count)}</span>
                {#if v.error}
                  <span class="berr">
                    {v.error}
                    <button
                      class="small"
                      onclick={() => {
                        bucketErrors.delete(v.key);
                        void loadBucket(v.b.year, v.b.month);
                      }}>Retry</button
                    >
                  </span>
                {/if}
              </h2>
              {#each v.tiles as t (t.key)}
                {#if t.item}
                  <Tile
                    item={t.item}
                    left={t.left}
                    top={t.top}
                    size={timeline.tile}
                    {api}
                    selected={selection.has(t.item.hash)}
                    selecting={selection.size > 0}
                    onselect={onSelect}
                    trash={mode === 'trash'}
                    {now}
                  />
                {:else}
                  <div
                    class="ph"
                    style:left="{t.left}px"
                    style:top="{t.top}px"
                    style:width="{timeline.tile}px"
                    style:height="{timeline.tile}px"
                  ></div>
                {/if}
              {/each}
            </section>
          {/each}
        </div>
      {/if}
    </div>
    {#if tlStatus === 'ready' && buckets.length > 0}
      <Scrubber {timeline} {scrollTop} viewportHeight={height} onjump={scrollToY} />
    {/if}
    <SelectionBar {api} mode={mode === 'import' ? 'grid' : mode} {albumId} currentRating={commonRating} bind:height={barHeight} />
  </div>
</div>

<style>
  .grid-page {
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
    background: var(--bg);
  }
  .count {
    white-space: nowrap;
  }
  .size {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .size input {
    width: 110px;
  }
  .size .big {
    font-size: 1.3em;
  }
  .viewport {
    position: relative;
    flex: 1;
    min-height: 0;
  }
  .scroller {
    position: absolute;
    inset: 0;
    overflow-y: auto;
    overflow-x: hidden;
    scrollbar-width: none;
  }
  .scroller:focus {
    outline: none; /* focus only lands here when the viewer closes */
  }
  .scroller::-webkit-scrollbar {
    display: none;
  }
  .canvas {
    position: relative;
    margin-left: 16px;
    margin-right: 56px;
  }
  .bucket {
    position: absolute;
    left: 0;
    right: 0;
  }
  .header {
    position: sticky;
    top: 0;
    z-index: 2;
    display: flex;
    align-items: center;
    gap: 10px;
    height: 44px;
    margin: 0;
    padding: 0 2px;
    font-size: 15px;
    font-weight: 600;
    background: var(--bg);
  }
  .header .n {
    color: var(--muted);
    font-weight: 400;
    font-size: 13px;
  }
  .berr {
    color: var(--danger);
    font-size: 13px;
    font-weight: 400;
    display: inline-flex;
    align-items: center;
    gap: 8px;
  }
  .ph {
    position: absolute;
    background: var(--placeholder);
    border-radius: 2px;
  }
</style>
