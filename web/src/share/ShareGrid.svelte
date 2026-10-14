<script lang="ts">
  // The phone app's grid (docs/design/lan-share.md "What is shown"): the
  // timeline with month headers and the scrubber, read only. It reuses the
  // admin's Tile, Scrubber and timeline layout, but not the admin Grid
  // page, which is bound to the selection, jobs, moves and view memory.
  // Columns follow the width (3 in portrait phones, 4–5 in landscape and
  // on tablets); tiles are capped at the 256 px thumb (the large only for
  // 1–2 columns), so scrolling never makes the computer derive renditions.
  // A tap opens the lightbox over the grid; the timeline reloads when the
  // page becomes visible again, so photos trashed or moved meanwhile drop out.
  import { untrack } from 'svelte';
  import { SvelteMap } from 'svelte/reactivity';
  import type { MediaItem, TimelineBucket } from '../lib/api';
  import { layoutTimeline } from '../lib/timeline';
  import { bucketLabel, formatCount, parseWallClock } from '../lib/format';
  import { href, navigate } from '../lib/router';
  import { aspectOf, pixelRatio, renditionFor, shareGridCap } from '../lib/rendition';
  import Tile, { type TileViewer } from '../components/Tile.svelte';
  import Scrubber from '../components/Scrubber.svelte';
  import ShareLightbox, { type Neighbours } from './ShareLightbox.svelte';
  import { errorText, ShareError, type ShareClient, type ShareFilters } from './shareapi';
  import { filtersKey } from './filters';

  interface Props {
    client: ShareClient;
    filters: ShareFilters;
    viewId: number | null;
    /** Router path of the grid ("l/peter", "l/peter/albums/3", "" for an album share). */
    basePath: string;
    /** The route query (filters), kept on the lightbox routes. */
    query: URLSearchParams;
    /** The share's kind ("album" says "this album"). */
    kind: string;
    /** Whether the route filters anything (for the empty state). */
    filtered: boolean;
    /** Library shares: album links in the lightbox's Info. */
    albumHref?: ((id: number) => string) | null;
    /** Text for an empty grid without filters. */
    emptyText?: string;
    onclearfilters?: () => void;
    /** A 403 without a state (a capability turned off): the shell reads the session again. */
    onforbidden?: () => void;
  }
  let { client, filters, viewId, basePath, query, kind, filtered, albumHref = null, emptyText = 'Nothing here yet', onclearfilters, onforbidden }: Props = $props();

  const GAP = 2;
  const HEADER = 40;
  const BUCKET_PAD = 8;
  const SCRUB = 28; // right gutter for the scrubber

  let width = $state(0);
  let height = $state(0);
  let scrollTop = $state(0);
  let scroller: HTMLDivElement | undefined = $state();

  let buckets = $state.raw<TimelineBucket[]>([]);
  let tlStatus = $state<'loading' | 'ready' | 'error'>('loading');
  let tlError = $state('');
  let busy = $state(false);
  const items = new SvelteMap<string, MediaItem[]>();
  const bucketErrors = new SvelteMap<string, string>();
  const inflight = new Map<string, Promise<MediaItem[] | null>>();
  let gen = 0;
  let epoch = $state(0);
  const bkey = (year: number, month: number) => `${year}-${month}`;

  /** 3 columns on a portrait phone, 4–5 in landscape and on tablets, more on big screens. */
  function columnsForWidth(w: number): number {
    if (w < 500) return 3;
    if (w < 800) return 4;
    if (w < 1100) return 5;
    return Math.floor(w / 220);
  }
  const contentWidth = $derived(Math.max(0, width - SCRUB - GAP));
  const columns = $derived(columnsForWidth(width));
  const timeline = $derived(
    layoutTimeline(buckets, {
      width: contentWidth,
      // Slightly under the exact size, so rounding never drops a column.
      tileSize: Math.max(40, (contentWidth - (columns - 1) * GAP) / columns - 0.01),
      gap: GAP,
      headerHeight: HEADER,
      bucketPadding: BUCKET_PAD,
    }),
  );
  const cap = $derived(shareGridCap(timeline.columns));
  const dpr = pixelRatio();
  const totalCount = $derived(buckets.reduce((n, b) => n + b.count, 0));

  // ---- Data ----

  function loadBucket(year: number, month: number): Promise<MediaItem[] | null> {
    const k = bkey(year, month);
    const have = items.get(k);
    if (have) return Promise.resolve(have);
    const pending = inflight.get(k);
    if (pending) return pending;
    const g = gen;
    const p = client
      .getBucket(filters, year, month)
      .then((list) => {
        if (g !== gen) return null;
        items.set(k, list);
        bucketErrors.delete(k);
        fixCount(year, month, list.length);
        return list;
      })
      .catch((e: unknown) => {
        if (g === gen) bucketErrors.set(k, errorText(e));
        return null;
      })
      .finally(() => {
        if (inflight.get(k) === p) inflight.delete(k);
      });
    inflight.set(k, p);
    return p;
  }

  /** The fetched list wins over the timeline's count (photos came or went meanwhile). */
  function fixCount(year: number, month: number, n: number) {
    const b = buckets.find((x) => x.year === year && x.month === month);
    if (!b || b.count === n) return;
    buckets = n > 0 ? buckets.map((x) => (x === b ? { ...x, count: n } : x)) : buckets.filter((x) => x !== b);
  }

  let ready: Promise<void> = Promise.resolve();

  function loadTimeline(resetScroll: boolean): Promise<void> {
    ready = fetchTimeline(resetScroll);
    return ready;
  }

  async function fetchTimeline(resetScroll: boolean) {
    const g = ++gen;
    inflight.clear();
    if (resetScroll || !buckets.length) tlStatus = 'loading';
    try {
      const b = await client.getTimeline(filters);
      if (g !== gen) return;
      items.clear();
      bucketErrors.clear();
      buckets = b;
      busy = false;
      epoch++;
      tlStatus = 'ready';
      if (resetScroll && scroller) {
        scroller.scrollTop = 0;
        scrollTop = 0;
      }
    } catch (e) {
      if (g !== gen) return;
      busy = e instanceof ShareError && e.status === 503;
      tlError = errorText(e);
      tlStatus = 'error';
    }
  }

  const fkey = $derived(`${client.id}|${basePath}|${filtersKey(filters)}`);
  $effect(() => {
    void fkey;
    untrack(() => void loadTimeline(true));
  });

  // Back on the page (the phone was locked, another app was in front): photos may have changed.
  $effect(() => {
    const onVisible = () => {
      if (document.visibilityState === 'visible') void loadTimeline(false);
    };
    document.addEventListener('visibilitychange', onVisible);
    return () => document.removeEventListener('visibilitychange', onVisible);
  });

  function patchRating(hash: string, rating: number) {
    for (const [k, list] of items) {
      if (!list.some((x) => x.hash === hash)) continue;
      items.set(
        k,
        list.map((x) => {
          if (x.hash !== hash) return x;
          const { rating: _old, ...rest } = x;
          return rating > 0 ? { ...rest, rating } : rest;
        }),
      );
    }
  }

  function dropItem(mid: number) {
    for (const [k, list] of items) {
      const next = list.filter((x) => x.id !== mid);
      if (next.length === list.length) continue;
      items.set(k, next);
      const [y, m] = k.split('-').map(Number) as [number, number];
      fixCount(y, m, next.length);
    }
  }

  // ---- Measuring and scrolling ----

  $effect(() => {
    const el = scroller;
    if (!el) return;
    const ro = new ResizeObserver((entries) => {
      for (const e of entries) {
        width = e.contentRect.width;
        height = e.contentRect.height;
      }
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
    const max = Math.max(0, timeline.totalHeight - height);
    const t = Math.max(0, Math.min(max, y));
    scroller.scrollTop = t;
    scrollTop = t;
  }

  // ---- Virtualisation (one screen of overscan: phones are often slow) ----

  const view = $derived.by(() => {
    const tl = timeline;
    const vh = height || 800;
    const [b0, b1] = tl.visibleBuckets(scrollTop, vh, vh);
    const out: { key: string; top: number; height: number; label: string; count: number; error: string | undefined; year: number; month: number; tiles: { key: string; item: MediaItem | undefined; left: number; top: number }[] }[] = [];
    for (let i = b0; i < b1; i++) {
      const b = tl.buckets[i];
      if (!b) continue;
      const k = bkey(b.year, b.month);
      const list = items.get(k);
      const [r0, r1] = tl.visibleRows(i, scrollTop, vh, vh);
      const tiles = [];
      const end = Math.min(b.count, r1 * tl.columns);
      for (let idx = r0 * tl.columns; idx < end; idx++) {
        const pos = tl.tilePosition(i, idx);
        const item = list?.[idx];
        tiles.push({ key: item ? `i${item.id}` : `p${k}-${idx}`, item, left: pos.left, top: pos.top - b.top });
      }
      out.push({ key: k, top: b.top, height: b.height, label: bucketLabel(b.year, b.month), count: b.count, error: bucketErrors.get(k), year: b.year, month: b.month, tiles });
    }
    return out;
  });

  const wanted = $derived(
    view
      .filter((v) => !items.has(v.key) && !v.error)
      .map((v) => `${v.year}:${v.month}`)
      .join(','),
  );
  $effect(() => {
    void epoch;
    if (!wanted) return;
    const list = wanted.split(',').map((s) => s.split(':').map(Number) as [number, number]);
    const t = setTimeout(() => {
      for (const [y, m] of list) void loadBucket(y, m);
    }, 60);
    return () => clearTimeout(t);
  });

  // ---- Lightbox ----

  const prefix = $derived(basePath ? `${basePath}/` : '');
  const viewHref = (mid: number) => href(`${prefix}view/${mid}`, query);
  /** The lightbox was opened from a tile here, so closing it goes back in history (the phone's Back does the same). */
  let openedHere = false;
  let hint = $state.raw<MediaItem | null>(null);

  function findItem(mid: number): MediaItem | null {
    for (const list of items.values()) {
      const x = list.find((i) => i.id === mid);
      if (x) return x;
    }
    return null;
  }

  const viewer: TileViewer = {
    href: (mid) => viewHref(mid),
    open(mid) {
      hint = findItem(mid);
      openedHere = true;
      navigate(viewHref(mid));
    },
  };

  function show(item: MediaItem) {
    hint = item;
    navigate(viewHref(item.id), { replace: true });
  }

  function close() {
    if (openedHere) {
      openedHere = false;
      history.back();
    } else navigate(href(prefix, query), { replace: true });
  }

  // The route left the lightbox by other means (the phone's Back button).
  $effect(() => {
    if (viewId === null) openedHere = false;
  });

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

  async function neighbours(item: MediaItem): Promise<Neighbours> {
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
  }

  function gone(mid: number) {
    dropItem(mid);
    void loadTimeline(false);
  }
</script>

<div class="share-grid">
  <div class="scroller" bind:this={scroller} onscroll={onScroll} inert={viewId !== null ? true : undefined}>
    {#if tlStatus === 'error'}
      <div class="empty" role="alert">
        {#if busy}
          <h2>Not available right now</h2>
          <p>This library is not available right now (the computer is busy with it). Try again later.</p>
        {:else}
          <h2>Could not load the photos</h2>
          <p>{tlError}</p>
        {/if}
        <button onclick={() => loadTimeline(false)}>Try again</button>
      </div>
    {:else if tlStatus === 'loading' && buckets.length === 0}
      <div class="empty">Loading…</div>
    {:else if tlStatus === 'ready' && buckets.length === 0}
      <div class="empty">
        {#if filtered}
          <h2>Nothing matches these filters</h2>
          {#if onclearfilters}<button onclick={onclearfilters}>Clear filters</button>{/if}
        {:else}
          <h2>{emptyText}</h2>
        {/if}
      </div>
    {:else}
      <p class="sr-only" aria-live="polite">{formatCount(totalCount)} items</p>
      <div class="canvas" style:height="{timeline.totalHeight}px">
        {#each view as v (v.key)}
          <section class="bucket" style:top="{v.top}px" style:height="{v.height}px" aria-label={v.label}>
            <h2 class="header">
              <span>{v.label}</span>
              <span class="n">{formatCount(v.count)}</span>
              {#if v.error}
                <span class="berr">
                  {v.error}
                  <button
                    class="small"
                    onclick={() => {
                      bucketErrors.delete(v.key);
                      void loadBucket(v.year, v.month);
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
                  api={client}
                  {viewer}
                  rendition={renditionFor(timeline.tile, dpr, aspectOf(t.item.w, t.item.h), cap)}
                />
              {:else}
                <div class="ph" style:left="{t.left}px" style:top="{t.top}px" style:width="{timeline.tile}px" style:height="{timeline.tile}px"></div>
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
  .share-grid {
    position: relative;
    height: 100%;
  }
  .scroller {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    overflow-y: auto;
    overflow-x: hidden;
    overscroll-behavior: contain;
    -webkit-overflow-scrolling: touch;
    scrollbar-width: none;
  }
  .scroller::-webkit-scrollbar {
    display: none;
  }
  .canvas {
    position: relative;
    margin-left: 2px;
    margin-right: 28px;
  }
  .bucket {
    position: absolute;
    left: 0;
    right: 0;
  }
  .header {
    position: -webkit-sticky;
    position: sticky;
    top: 0;
    z-index: 2;
    display: flex;
    align-items: center;
    gap: 8px;
    height: 40px;
    margin: 0;
    padding: 0 6px;
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
  /* The scrubber is narrower on a phone; the year labels still fit. */
  .share-grid :global(.scrubber) {
    width: 28px;
  }
  .share-grid :global(.scrubber .year) {
    right: 2px;
    font-size: 10px;
  }
  .share-grid :global(.scrubber .hover) {
    right: 32px;
  }
</style>
