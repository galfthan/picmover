<script lang="ts">
  import { untrack } from 'svelte';
  import { thumbUrl, type MediaItem } from '../lib/api';
  import { formatDuration, formatWallDate } from '../lib/format';
  import { openViewer } from '../lib/viewer';

  interface Props {
    item: MediaItem;
    left: number;
    top: number;
    size: number;
  }
  let { item, left, top, size }: Props = $props();

  // Rows whose thumbnails cannot exist are not requested at all.
  const requestable = $derived(item.thumb !== 'unsupported' && item.thumb !== 'error');
  // The URL carries thumbAt, so a regenerated thumbnail is a new URL. A
  // failure is remembered per URL: when the grid refetches the bucket after
  // a job (new thumbAt), the tile simply tries the new URL.
  const src = $derived(thumbUrl(item));
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

  function open(e: MouseEvent) {
    if (e.button !== 0 || e.ctrlKey || e.metaKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    openViewer(item.id);
  }
</script>

<a
  class="tile"
  class:loaded
  href="#/view/{item.id}"
  onclick={open}
  style:left="{left}px"
  style:top="{top}px"
  style:width="{size}px"
  style:height="{size}px"
  aria-label="{isVideo ? 'Video' : 'Photo'} {formatWallDate(item.takenAt)}"
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
  <span class="date">{formatWallDate(item.takenAt)}</span>
</a>

<style>
  .tile {
    position: absolute;
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
    transition: opacity 0.15s;
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
  @media (prefers-reduced-motion: reduce) {
    img,
    .date {
      transition: none;
    }
  }
</style>
