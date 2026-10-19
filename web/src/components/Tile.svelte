<script lang="ts" module>
  import type { LibApi } from '../lib/api';

  /**
   * What a tile needs of a library client: the admin LibApi, or the phone
   * app's share client. `retryDelay` (the phone's): when to load a failed
   * image again (ms), or null to give up; the admin grid has none and
   * retries only when a job versions the URL.
   */
  export type TileApi = Pick<LibApi, 'id' | 'thumbUrl' | 'largeUrl' | 'previewUrl'> & {
    retryDelay?: (url: string, attempt: number) => Promise<number | null>;
  };

  /** Where a tile opens its item; the admin viewer (#/l/<id>/view/<mid>) when not given. */
  export interface TileViewer {
    href(id: number): string;
    open(id: number): void;
  }
</script>

<script lang="ts">
  import { untrack } from 'svelte';
  import type { MediaItem } from '../lib/api';
  import { formatAgo, formatDuration, formatWallDate, parseLocalWallClock } from '../lib/format';
  import { openViewer, viewerHref } from '../lib/viewer';
  import type { Rendition } from '../lib/rendition';

  interface Props {
    item: MediaItem;
    left: number;
    top: number;
    size: number;
    api: TileApi;
    selected?: boolean;
    /** A selection exists: checkboxes stay visible. */
    selecting?: boolean;
    /** Checkbox click, Ctrl/Cmd-click (toggle) or Shift-click (range). */
    onselect?: (item: MediaItem, range: boolean) => void;
    /** Trash page: show when the item was deleted. */
    trash?: boolean;
    /** Current time for "deleted … ago" (the grid ticks it). */
    now?: number;
    /** Which stored rendition to show (the grid picks it from the tile size and pixel ratio). */
    rendition?: Rendition;
    /** Read-only grids (the phone app) open the item elsewhere; without `onselect` there is no check box. */
    viewer?: TileViewer;
  }
  let { item, left, top, size, api, selected = false, selecting = false, onselect, trash = false, now = 0, rendition = 'thumb', viewer }: Props = $props();

  // Rows whose thumbnails cannot exist are not requested at all.
  const requestable = $derived(item.thumb !== 'unsupported' && item.thumb !== 'error');
  // The URL carries thumbAt, so a regenerated thumbnail is a new URL. A
  // failure is remembered per URL: when the grid refetches the bucket after
  // a job (new thumbAt), the tile simply tries the new URL.
  const thumbSrc = $derived(api.thumbUrl(item));
  const bigSrc = $derived(rendition === 'large' ? api.largeUrl(item) : rendition === 'preview' ? api.previewUrl(item) : null);
  // A large or preview that fails (no preview to derive it from) falls back to the thumb.
  let bigFailedSrc = $state<string | null>(null);
  const src = $derived(bigSrc !== null && bigFailedSrc !== bigSrc ? bigSrc : thumbSrc);
  let failedSrc = $state<string | null>(null);
  const failed = $derived(failedSrc !== null && failedSrc === src);
  let loaded = $state(false);
  // The last URL that finished loading. While a new URL loads (zoom crossed
  // a rendition threshold, or a job versioned the URL), that image stays
  // underneath so the tile never goes blank. A tile that has loaded nothing
  // yet and shows the large or the preview gets the thumb underneath (the
  // browser has usually cached it; it is small either way), so a large
  // being derived on demand never leaves the tile empty. The extra <img>
  // exists only until the shown URL has loaded.
  let loadedSrc = $state<string | null>(null);
  const under = $derived.by(() => {
    if (failed || loadedSrc === src) return null;
    if (loadedSrc !== null) return loadedSrc;
    return src !== thumbSrc ? thumbSrc : null;
  });

  function onLoad(e: Event) {
    loaded = true;
    loadedSrc = (e.currentTarget as HTMLImageElement).getAttribute('src');
  }
  function onError(e: Event) {
    const s = (e.currentTarget as HTMLImageElement).getAttribute('src');
    if (s === bigSrc && s !== thumbSrc) bigFailedSrc = s;
    else {
      failedSrc = s;
      if (s) scheduleRetry(s);
    }
  }

  // Bounded retries where the client offers them (the phone: a 429 from
  // the computer's rate limit, or a moment without Wi-Fi, must not leave
  // grey tiles for good). The <img> is made anew for each try.
  let attempts = 0;
  let tries = $state(0);
  let retryTimer: ReturnType<typeof setTimeout> | null = null;
  let asking = false;
  let destroyed = false;
  function scheduleRetry(s: string) {
    const ask = api.retryDelay;
    if (!ask || retryTimer || asking) return;
    asking = true;
    void ask(s, attempts++).then((ms) => {
      asking = false;
      if (ms === null || destroyed || failedSrc !== s) return;
      retryTimer = setTimeout(() => {
        retryTimer = null;
        if (failedSrc !== s) return;
        tries++;
        failedSrc = null;
      }, ms);
    });
  }
  $effect(() => () => {
    destroyed = true;
    if (retryTimer) clearTimeout(retryTimer);
  });
  // One more try at an unchanged URL when a fresh copy of the item arrives
  // (the grid refetches buckets after a job), for a server that does not
  // send thumbAt.
  let retried = false;
  let lastItem = untrack(() => item);
  $effect(() => {
    const it = item;
    if (it === lastItem) return;
    lastItem = it;
    untrack(() => {
      if (failed && !retried) {
        retried = true;
        failedSrc = null;
      }
    });
  });

  const isVideo = $derived(item.type === 'video');
  const isRaw = $derived(item.type === 'image_raw');

  const deleted = $derived(trash && item.deletedAt ? formatAgo(parseLocalWallClock(item.deletedAt), now || Date.now()) : '');
  const label = $derived(`${isVideo ? 'Video' : 'Photo'} ${formatWallDate(item.takenAt)}`);

  function open(e: MouseEvent) {
    if (e.button !== 0 || e.altKey) return;
    if (onselect && (e.ctrlKey || e.metaKey || e.shiftKey)) {
      e.preventDefault();
      onselect(item, e.shiftKey);
      return;
    }
    if (e.ctrlKey || e.metaKey || e.shiftKey) return; // new tab / window
    e.preventDefault();
    if (viewer) viewer.open(item.id);
    else openViewer(api.id, item.id);
  }

  // Shift+mousedown would extend the page's text selection to the tile.
  function noTextSelection(e: MouseEvent) {
    if (onselect && e.shiftKey) e.preventDefault();
  }
</script>

<div
  class="cell"
  class:selected
  class:selecting
  class:checkable={!!onselect}
  style:left="{left}px"
  style:top="{top}px"
  style:width="{size}px"
  style:height="{size}px"
>
  <a
    class="tile"
    class:loaded
    href={viewer ? viewer.href(item.id) : viewerHref(api.id, item.id)}
    onclick={open}
    onmousedown={noTextSelection}
    aria-label={label}
  >
    {#if requestable && !failed}
      {#if under}
        <img class="under" src={under} alt="" draggable="false" />
      {/if}
      {#key tries}
        <img {src} alt="" loading="lazy" decoding="async" draggable="false" onload={onLoad} onerror={onError} />
      {/key}
    {:else}
      <span class="ph" aria-hidden="true">{isVideo ? '▶' : ''}</span>
    {/if}
    {#if isVideo}
      <span class="badge">▶ {item.dur ? formatDuration(item.dur) : ''}</span>
    {:else if isRaw}
      <span class="badge">RAW</span>
    {/if}
    {#if item.rating}
      <span class="rating" aria-label="{item.rating} stars">★ {item.rating}</span>
    {/if}
    {#if deleted}
      <span class="deleted">deleted {deleted}</span>
    {:else}
      <span class="date">{formatWallDate(item.takenAt)}</span>
    {/if}
  </a>
  {#if onselect}
    <input
      class="check"
      type="checkbox"
      checked={selected}
      aria-label="Select {label}"
      title="Select. Shift-click selects every item between this one and the last one clicked."
      onmousedown={noTextSelection}
      onclick={(e) => {
        const box = e.currentTarget;
        onselect(item, e.shiftKey);
        // The browser flipped the box; a Shift range can leave this tile selected, so show the real state.
        box.checked = selected;
      }}
    />
  {/if}
</div>

<style>
  .cell {
    position: absolute;
  }
  .tile {
    position: absolute;
    inset: 0;
    display: block;
    overflow: hidden;
    background: var(--placeholder);
    border-radius: 2px;
    color: #fff;
  }
  .tile:focus-visible {
    outline-offset: -2px;
    z-index: 1;
  }
  img {
    position: relative;
    display: block;
    width: 100%;
    height: 100%;
    object-fit: cover;
    opacity: 0;
    transition:
      opacity 0.15s,
      transform 0.12s;
  }
  .loaded img {
    opacity: 1;
  }
  img.under {
    position: absolute;
    inset: 0;
    opacity: 1;
  }
  .ph {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 100%;
    height: 100%;
    color: var(--faint);
    font-size: 22px;
  }
  .badge {
    position: absolute;
    top: 6px;
    right: 6px;
    padding: 1px 6px;
    border-radius: 4px;
    background: rgba(0, 0, 0, 0.6);
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.02em;
  }
  .date {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    padding: 14px 8px 6px;
    background: linear-gradient(transparent, rgba(0, 0, 0, 0.65));
    font-size: 12px;
    opacity: 0;
    transition: opacity 0.12s;
    pointer-events: none;
  }
  .tile:hover .date,
  .tile:focus-visible .date {
    opacity: 1;
  }
  .tile:hover img {
    filter: brightness(1.06);
  }
  .rating {
    position: absolute;
    left: 6px;
    top: 6px;
    padding: 0 5px;
    border-radius: 4px;
    background: rgba(0, 0, 0, 0.55);
    color: #ffd25e;
    font-size: 11px;
    font-weight: 600;
  }
  .selecting .rating,
  .checkable:hover .rating {
    left: 32px;
  }
  .deleted {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    padding: 12px 8px 5px;
    background: linear-gradient(transparent, rgba(0, 0, 0, 0.7));
    font-size: 11.5px;
    pointer-events: none;
  }
  .check {
    position: absolute;
    left: 6px;
    top: 6px;
    z-index: 2;
    width: 20px;
    height: 20px;
    margin: 0;
    opacity: 0;
    cursor: pointer;
  }
  .cell:hover .check,
  .check:focus-visible,
  .selecting .check {
    opacity: 1;
  }
  .selected .tile {
    outline: 3px solid var(--accent);
    outline-offset: -3px;
  }
  .selected img {
    transform: scale(0.92);
  }
  /* Touch screens have no hover: the date stays visible. */
  @media (hover: none) {
    .date {
      opacity: 1;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    img,
    .date {
      transition: none;
    }
  }
</style>
