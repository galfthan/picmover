<script lang="ts">
  import { tick, untrack } from 'svelte';
  import {
    errorMessage,
    fileUrl,
    getMedia,
    isAbort,
    previewUrl,
    thumbUrl,
    type MediaDetail,
    type MediaItem,
  } from '../lib/api';
  import { formatBytes, formatCoord, formatDuration, formatInstant, formatTakenAt } from '../lib/format';
  import { closeViewer, sequenceProvider, showInViewer, type Neighbours } from '../lib/viewer';
  import { href } from '../lib/router';
  import { isBoolean, loadJSON, saveJSON } from '../lib/storage';
  import CopyButton from './CopyButton.svelte';

  interface Props {
    id: number;
    /** Hash of the page underneath, used when closing a deep-linked viewer. */
    baseHash: string;
  }
  let { id, baseHash }: Props = $props();

  const INFO_KEY = 'picmover.viewer.info';

  let detail = $state<MediaDetail | null>(null);
  /** The compact item we navigated from, shown until the detail arrives. */
  let hint = $state<MediaItem | null>(null);
  let loadError = $state('');
  let nb = $state.raw<Neighbours>({ prev: null, next: null });
  let previewLoaded = $state(false);
  let previewFailed = $state(false);
  let thumbFailed = $state(false);
  let full = $state(false);
  let showInfo = $state(loadJSON(INFO_KEY, isBoolean) ?? true);
  let videoEl: HTMLVideoElement | undefined = $state();
  let dialog: HTMLDivElement | undefined = $state();
  // Captured as early as possible: once the page behind is made inert the
  // browser may already have moved focus away from it.
  const opener = document.activeElement instanceof HTMLElement && document.activeElement !== document.body ? document.activeElement : null;

  const item = $derived<MediaItem | null>(detail && detail.id === id ? detail : hint && hint.id === id ? hint : null);
  const d = $derived(detail && detail.id === id ? detail : null);
  const isVideo = $derived(item?.type === 'video');
  const canFull = $derived(!!d && d.displayable && !isVideo);
  const playable = $derived(!!d && d.displayable && isVideo);
  // Rows whose thumbnails cannot exist are not requested (as in Tile).
  const thumbRequestable = $derived(!!item && item.thumb !== 'unsupported' && item.thumb !== 'error');

  // Load the detail and the neighbours whenever the id changes.
  // The id prop is already null while the viewer is torn down; remember it.
  let lastId = untrack(() => id);
  $effect(() => {
    const cur = id;
    lastId = cur;
    const ac = new AbortController();
    untrack(() => {
      full = false;
      previewLoaded = false;
      previewFailed = false;
      thumbFailed = false;
      loadError = '';
      nb = { prev: null, next: null };
    });
    getMedia(cur, ac.signal)
      .then((md) => {
        if (ac.signal.aborted) return;
        detail = md;
        const p = sequenceProvider();
        p?.shown?.(md);
        return p?.neighbours(md).then((n) => {
          if (ac.signal.aborted) return;
          nb = n;
          for (const x of [n.next, n.prev]) {
            if (x) new Image().src = previewUrl(x);
          }
        });
      })
      .catch((e: unknown) => {
        if (!isAbort(e) && !ac.signal.aborted) loadError = errorMessage(e);
      });
    return () => ac.abort();
  });

  // Focus the dialog itself on open (not the Close button, which Space or
  // Enter would activate). On close, focus the tile of the item shown last
  // (the grid scrolls it into view), else the element that opened the
  // viewer, else the page's main scroll area; never leave it on <body>.
  $effect(() => {
    untrack(() => dialog?.focus({ preventScroll: true }));
    return () => {
      const shown = lastId;
      void tick().then(() =>
        setTimeout(() => {
          const ae = document.activeElement;
          if (ae && ae !== document.body && ae.isConnected) return; // something else took focus
          const target =
            document.querySelector<HTMLElement>(`a[href="#/view/${shown}"]`) ??
            (opener?.isConnected ? opener : null) ??
            document.querySelector<HTMLElement>('[data-focus-return], .leaflet-container');
          target?.focus({ preventScroll: true });
        }, 0),
      );
    };
  });

  function go(target: MediaItem | null) {
    if (!target) return;
    hint = target;
    showInViewer(target.id);
  }
  const close = () => closeViewer(baseHash);

  function toggleInfo() {
    showInfo = !showInfo;
    saveJSON(INFO_KEY, showInfo);
  }
  function toggleFull() {
    if (canFull) full = !full;
  }
  function onStageDblclick(e: MouseEvent) {
    // Double-clicking prev/next (or the video controls) must not zoom.
    if (e.target instanceof Element && e.target.closest('button, a, video')) return;
    toggleFull();
  }
  function togglePlay() {
    if (!videoEl) return;
    if (videoEl.paused) void videoEl.play().catch(() => {});
    else videoEl.pause();
  }

  function onKey(e: KeyboardEvent) {
    if (e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey) return;
    const t = e.target;
    if (t instanceof HTMLElement && (t.isContentEditable || ['INPUT', 'SELECT', 'TEXTAREA'].includes(t.tagName))) return;
    switch (e.key) {
      case 'ArrowLeft':
        e.preventDefault();
        go(nb.prev);
        break;
      case 'ArrowRight':
        e.preventDefault();
        go(nb.next);
        break;
      case 'Escape':
        e.preventDefault();
        if (full) full = false;
        else close();
        break;
      case 'i':
        toggleInfo();
        break;
      case 'f':
        toggleFull();
        break;
      case ' ':
        if (t === videoEl) return; // the video handles its own space
        if (playable) {
          e.preventDefault();
          togglePlay();
        }
        break;
    }
  }

  // Touch: horizontal swipe navigates.
  let touchX: number | null = null;
  let touchY = 0;
  function touchStart(e: TouchEvent) {
    const p = e.touches[0];
    if (e.touches.length !== 1 || !p || full) {
      touchX = null;
      return;
    }
    touchX = p.clientX;
    touchY = p.clientY;
  }
  function touchEnd(e: TouchEvent) {
    const p = e.changedTouches[0];
    if (touchX === null || !p) return;
    const dx = p.clientX - touchX;
    const dy = p.clientY - touchY;
    touchX = null;
    if (Math.abs(dx) > 60 && Math.abs(dx) > 2 * Math.abs(dy)) go(dx < 0 ? nb.next : nb.prev);
  }

  const camera = $derived.by(() => {
    if (!d) return '';
    const make = d.cameraMake.trim();
    const model = d.cameraModel.trim();
    if (!make) return model;
    if (!model) return make;
    return model.toLowerCase().startsWith(make.toLowerCase()) ? model : `${make} ${model}`;
  });
  const hasGps = $derived(!!d && typeof d.lat === 'number' && typeof d.lon === 'number');
  const typeLabel = $derived.by(() => {
    if (!item) return '';
    const t = item.type === 'image_raw' ? 'RAW image' : item.type === 'video' ? 'Video' : 'Image';
    return d?.ext ? `${t} · ${d.ext.replace(/^\./, '').toUpperCase()}` : t;
  });
</script>

<svelte:window onkeydown={onKey} />

<div class="lightbox" role="dialog" aria-modal="true" aria-label="Viewer" tabindex="-1" bind:this={dialog}>
  <div class="main">
    <div class="bar">
      <button class="ghost" onclick={close} aria-label="Close (Esc)" title="Close (Esc)">✕</button>
      <span class="title">{item ? formatTakenAt(item.takenAt, d?.offset ?? null) : ''}</span>
      <span class="spacer"></span>
      {#if canFull}
        <button class="ghost" class:on={full} onclick={toggleFull} title="Original size (F, or double-click)">1:1</button>
      {/if}
      {#if d}
        <a class="btn ghost" href={fileUrl(d.id, true)} download title="Download the original">Download</a>
      {/if}
      <button class="ghost" class:on={showInfo} onclick={toggleInfo} title="Info (I)" aria-pressed={showInfo}>Info</button>
    </div>

    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="stage" ontouchstart={touchStart} ontouchend={touchEnd} ondblclick={onStageDblclick}>
      {#if loadError}
        <div class="msg">
          <p>{loadError}</p>
          <button onclick={close}>Close</button>
        </div>
      {:else if item}
        {#key item.id}
          {#if playable && d}
            <!-- svelte-ignore a11y_media_has_caption -->
            <video bind:this={videoEl} controls preload="metadata" src={fileUrl(d.id)} poster={previewUrl(d)}></video>
          {:else if full && d}
            <div class="full">
              <img src={fileUrl(d.id)} alt="" />
            </div>
          {:else}
            {#if thumbRequestable && !thumbFailed && !previewLoaded}
              <img class="fit blur" src={thumbUrl(item)} alt="" onerror={() => (thumbFailed = true)} />
            {/if}
            {#if !previewFailed}
              <img
                class="fit preview"
                class:loaded={previewLoaded}
                src={previewUrl(item)}
                alt=""
                onload={() => (previewLoaded = true)}
                onerror={() => (previewFailed = true)}
              />
            {/if}
            {#if previewFailed && (thumbFailed || !thumbRequestable)}
              <div class="msg">
                <p>No preview available{d && d.thumbState !== 'ok' ? ` (${d.thumbState})` : ''}.</p>
              </div>
            {/if}
            {#if d && !d.displayable}
              <div class="undisplayable">
                <span>This browser cannot show the original {d.ext.replace(/^\./, '').toUpperCase()} file.</span>
                <a class="btn small" href={fileUrl(d.id, true)} download>Download original</a>
              </div>
            {/if}
          {/if}
        {/key}
      {/if}

      {#if nb.prev}
        <button class="nav prev" onclick={() => go(nb.prev)} aria-label="Previous (←)" title="Previous (←)">‹</button>
      {/if}
      {#if nb.next}
        <button class="nav next" onclick={() => go(nb.next)} aria-label="Next (→)" title="Next (→)">›</button>
      {/if}
    </div>
  </div>

  {#if showInfo}
    <aside class="info" aria-label="Details">
      {#if d}
        <dl>
          <dt>Date</dt>
          <dd>
            {formatTakenAt(d.takenAt, d.offset)}
            {#if d.takenAtSource}<span class="src">{d.takenAtSource}</span>{/if}
          </dd>

          {#if camera}
            <dt>Camera</dt>
            <dd>{camera}{#if d.cameraType}<span class="src">{d.cameraType}</span>{/if}</dd>
          {/if}
          {#if d.lens}
            <dt>Lens</dt>
            <dd>{d.lens}</dd>
          {/if}

          {#if d.w > 0 && d.h > 0}
            <dt>Dimensions</dt>
            <dd>{d.w} × {d.h} <span class="muted">({((d.w * d.h) / 1e6).toFixed(1)} MP)</span></dd>
          {/if}
          {#if d.dur}
            <dt>Duration</dt>
            <dd>{formatDuration(d.dur)}</dd>
          {/if}
          <dt>Size</dt>
          <dd>{formatBytes(d.size)}</dd>
          <dt>Type</dt>
          <dd>{typeLabel}</dd>

          <dt>Path in library</dt>
          <dd class="path">
            <span class="mono">{d.relPath}</span>
            <CopyButton text={d.relPath} />
          </dd>

          {#if hasGps && typeof d.lat === 'number' && typeof d.lon === 'number'}
            <dt>Location</dt>
            <dd>
              <span class="mono">{formatCoord(d.lat)}, {formatCoord(d.lon)}</span>
              {#if typeof d.altitude === 'number'}<span class="muted"> · {Math.round(d.altitude)} m</span>{/if}
              <br />
              <a href={href('/map', { at: `${d.lat},${d.lon}`, z: 15 })}>Show on map</a>
            </dd>
          {/if}

          <dt>Imported</dt>
          <dd>
            {formatInstant(d.importedAt)}
            {#if d.sessionId}
              <br /><a href={href('/', { session: d.sessionId })}>Session #{d.sessionId}</a>
            {/if}
          </dd>

          {#if d.originalPath}
            <dt>Imported from</dt>
            <dd class="mono small wrap">{d.originalPath}</dd>
          {/if}

          {#if d.thumbState !== 'ok'}
            <dt>Thumbnail</dt>
            <dd>{d.thumbState}{#if d.thumbError}<span class="src">{d.thumbError}</span>{/if}</dd>
          {/if}
        </dl>
      {:else if !loadError}
        <p class="muted">Loading…</p>
      {/if}
    </aside>
  {/if}
</div>

<style>
  .lightbox {
    position: fixed;
    inset: 0;
    z-index: 1000;
    display: flex;
    background: var(--overlay);
    color: #eee;
  }
  .lightbox:focus {
    outline: none;
  }
  .main {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .bar {
    flex: none;
    display: flex;
    align-items: center;
    gap: 6px;
    height: 48px;
    padding: 0 8px;
  }
  .bar button,
  .bar .btn {
    color: #eee;
  }
  .bar .on {
    background: rgba(255, 255, 255, 0.14);
  }
  .bar button:hover:not(:disabled),
  .bar .btn:hover {
    background: rgba(255, 255, 255, 0.1);
  }
  .title {
    margin-left: 6px;
    font-weight: 500;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .spacer {
    flex: 1;
  }
  .stage {
    position: relative;
    flex: 1;
    min-height: 0;
    margin: 0 8px 8px;
    user-select: none;
  }
  .fit {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    object-fit: contain;
  }
  .blur {
    filter: blur(14px);
    transform: scale(1.01);
  }
  .preview {
    opacity: 0;
    transition: opacity 0.2s;
  }
  .preview.loaded {
    opacity: 1;
  }
  video {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    background: #000;
  }
  .full {
    position: absolute;
    inset: 0;
    display: flex;
    overflow: auto;
    cursor: zoom-out;
  }
  .full img {
    flex: none;
    margin: auto;
    max-width: none;
  }
  .msg {
    position: absolute;
    inset: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 8px;
    color: #bbb;
  }
  .undisplayable {
    position: absolute;
    left: 50%;
    bottom: 12px;
    transform: translateX(-50%);
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 6px 8px 6px 12px;
    border-radius: var(--radius);
    background: rgba(0, 0, 0, 0.7);
    font-size: 13px;
    white-space: nowrap;
  }
  .nav {
    position: absolute;
    top: 50%;
    transform: translateY(-50%);
    width: 44px;
    height: 72px;
    border: 0;
    border-radius: var(--radius);
    background: rgba(0, 0, 0, 0.35);
    color: #fff;
    font-size: 34px;
    line-height: 1;
    opacity: 0.55;
  }
  .nav:hover:not(:disabled),
  .nav:focus-visible {
    opacity: 1;
    background: rgba(0, 0, 0, 0.55);
  }
  .prev {
    left: 4px;
  }
  .next {
    right: 4px;
  }
  .info {
    flex: none;
    width: 320px;
    overflow-y: auto;
    padding: 16px 18px;
    background: var(--surface);
    color: var(--text);
    border-left: 1px solid var(--border);
  }
  dl {
    margin: 0;
  }
  dt {
    margin-top: 12px;
    color: var(--muted);
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  dt:first-child {
    margin-top: 0;
  }
  dd {
    margin: 2px 0 0;
    word-break: break-word;
  }
  .src {
    display: block;
    color: var(--faint);
    font-size: 12px;
  }
  .path {
    display: flex;
    align-items: flex-start;
    gap: 8px;
  }
  .path .mono {
    flex: 1;
    word-break: break-all;
  }
  .wrap {
    word-break: break-all;
  }
  @media (max-width: 900px) {
    .info {
      position: absolute;
      right: 0;
      top: 48px;
      bottom: 0;
      width: min(320px, 85vw);
      box-shadow: var(--shadow);
    }
  }
</style>
