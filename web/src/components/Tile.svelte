<script lang="ts">
  import { untrack } from 'svelte';
  import type { LibApi, MediaItem } from '../lib/api';
  import { formatAgo, formatDuration, formatWallDate, parseLocalWallClock } from '../lib/format';
  import { openViewer, viewerHref } from '../lib/viewer';

  interface Props {
    item: MediaItem;
    left: number;
    top: number;
    size: number;
    api: LibApi;
    selected?: boolean;
    /** A selection exists: checkboxes stay visible. */
    selecting?: boolean;
    /** Checkbox click, Ctrl/Cmd-click (toggle) or Shift-click (range). */
    onselect?: (item: MediaItem, range: boolean) => void;
    /** Trash page: show when the item was deleted. */
    trash?: boolean;
    /** Current time for "deleted … ago" (the grid ticks it). */
    now?: number;
  }
  let { item, left, top, size, api, selected = false, selecting = false, onselect, trash = false, now = 0 }: Props = $props();

  // Rows whose thumbnails cannot exist are not requested at all.
  const requestable = $derived(item.thumb !== 'unsupported' && item.thumb !== 'error');
  // The URL carries thumbAt, so a regenerated thumbnail is a new URL. A
  // failure is remembered per URL: when the grid refetches the bucket after
  // a job (new thumbAt), the tile simply tries the new URL.
  const src = $derived(api.thumbUrl(item));
  let failedSrc = $state<string | null>(null);
  const failed = $derived(failedSrc !== null && failedSrc === src);
  let loaded = $state(false);
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
    openViewer(api.id, item.id);
  }
</script>

<div
  class="cell"
  class:selected
  class:selecting
  style:left="{left}px"
  style:top="{top}px"
  style:width="{size}px"
  style:height="{size}px"
>
  <a
    class="tile"
    class:loaded
    href={viewerHref(api.id, item.id)}
    onclick={open}
    aria-label={label}
  >
    {#if requestable && !failed}
      <img
        {src}
        alt=""
        loading="lazy"
        decoding="async"
        draggable="false"
        onload={() => (loaded = true)}
        onerror={() => (failedSrc = src)}
      />
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
  .cell:hover .rating {
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
  @media (prefers-reduced-motion: reduce) {
    img,
    .date {
      transition: none;
    }
  }
</style>
