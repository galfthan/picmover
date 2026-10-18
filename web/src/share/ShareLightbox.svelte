<script lang="ts" module>
  import type { MediaItem } from '../lib/api';

  export interface Neighbours {
    prev: MediaItem | null;
    next: MediaItem | null;
  }
</script>

<script lang="ts">
  // The phone app's lightbox (docs/design/lan-share.md "Lightbox"). Read
  // only except for the stars, and every control only with its capability:
  // stars with `rate`, Download with `download`, the 1:1 original with
  // `download` (else double-tap zooms the preview), the player with
  // `video`, the location with `location`. Info is always there.
  //
  // Touch: swipe left and right turns the page, swipe down closes, a tap
  // toggles the bars, a double tap toggles 1:1. Only swipes that start
  // away from the screen edges count (the system owns edge swipes on
  // Android and Sailfish), and without `touch-action` on Gecko 91 the
  // listeners are non-passive and cancel the browser's panning themselves.
  // Two fingers are left to the browser (pinch zoom).
  //
  // A video or original that fails to load is asked what it answers
  // (shareapi checkUrl): a 403 means the capability was turned off on the
  // computer (the session is read again and the control goes away), no
  // answer means the computer cannot be reached; only a URL that loads
  // fine is "cannot be played in this browser". The lightbox follows the
  // client it is given, which changes with the capabilities.
  import { untrack } from 'svelte';
  import { formatBytes, formatCapture, formatCoord, formatDuration, formatTakenAt } from '../lib/format';
  import RatingStars from '../components/RatingStars.svelte';
  import { classify, dragAxis, startsAwayFromEdges, DOUBLE_TAP_MS, type Point } from './gestures';
  import { errorText, isAbort, ShareError, UNREACHABLE, type ShareClient, type ShareDetail, type UrlCheck } from './shareapi';

  interface Props {
    client: ShareClient;
    id: number;
    /** The tile's item, shown until the detail arrives. */
    hint: MediaItem | null;
    /** The share's kind: an album share says "this album" where a library share says "the library". */
    kind: string;
    /** Library shares: the router path of an album, for the album links in Info; null: no links. */
    albumHref?: ((id: number) => string) | null;
    neighbours: (item: MediaItem) => Promise<Neighbours>;
    /** Show another item (replaces the route's view id). */
    onnavigate: (item: MediaItem) => void;
    onclose: () => void;
    /** The item is no longer visible (404): the grid reloads. */
    ongone?: (id: number) => void;
    /** A rating was set (or rolled back): the grid patches its tile. */
    onrated?: (hash: string, rating: number) => void;
    /** A 403 without a state: a capability was turned off meanwhile; the session is read again. */
    onforbidden?: () => void;
  }
  let { client, id, hint, kind, albumHref = null, neighbours, onnavigate, onclose, ongone, onrated, onforbidden }: Props = $props();

  const caps = $derived(client.caps);
  let detail = $state<ShareDetail | null>(null);
  let loadError = $state('');
  let nb = $state.raw<Neighbours>({ prev: null, next: null });
  let previewLoaded = $state(false);
  let previewFailed = $state(false);
  let videoFailed = $state(false);
  /** Why the video or the original did not load ("" while asking, or when it loads fine but cannot be played). */
  let mediaNote = $state('');
  let videoTry = $state(0);
  let fullFailed = $state(false);
  let full = $state(false);
  let chrome = $state(true);
  let info = $state(false);
  let note = $state('');
  let stage: HTMLDivElement | undefined = $state();
  let dialog: HTMLDivElement | undefined = $state();

  const d = $derived(detail && detail.id === id ? detail : null);
  /** The home time, and the local time when it differs (docs/design/home-time.md). */
  const capture = $derived(d ? formatCapture(d) : null);
  const item = $derived<MediaItem | null>(d ?? (hint && hint.id === id ? hint : null));
  const isVideo = $derived(item?.type === 'video');
  const playable = $derived(isVideo && caps.video);
  const thumbRequestable = $derived(!!item && item.thumb !== 'unsupported' && item.thumb !== 'error');
  const goneText = $derived(kind === 'album' ? 'This photo is no longer in this album.' : 'This photo is no longer in the library.');

  $effect(() => {
    const cur = id;
    const ac = new AbortController();
    untrack(() => {
      full = false;
      previewLoaded = false;
      previewFailed = false;
      videoFailed = false;
      fullFailed = false;
      mediaNote = '';
      loadError = '';
      nb = { prev: null, next: null };
    });
    client
      .getMedia(cur, ac.signal)
      .then((md) => {
        if (ac.signal.aborted) return;
        detail = md;
        return neighbours(md).then((n) => {
          if (ac.signal.aborted) return;
          nb = n;
          for (const x of [n.next, n.prev]) if (x) new Image().src = client.previewUrl(x);
        });
      })
      .catch((e: unknown) => {
        if (isAbort(e) || ac.signal.aborted) return;
        if (e instanceof ShareError && e.status === 404) {
          loadError = goneText;
          ongone?.(cur);
        } else loadError = errorText(e);
      });
    return () => ac.abort();
  });

  $effect(() => {
    untrack(() => dialog?.focus({ preventScroll: true }));
  });

  // Notes (a rating that did not stick) go away by themselves.
  $effect(() => {
    if (!note) return;
    const t = setTimeout(() => (note = ''), 6000);
    return () => clearTimeout(t);
  });

  function go(target: MediaItem | null) {
    if (target) onnavigate(target);
  }

  // ---- Rating (optimistic, rolled back on error) ----

  let rating = $state(false);
  async function rate(v: number) {
    const cur = d;
    if (!cur || rating || !caps.rate) return;
    const old = cur.rating ?? 0;
    rating = true;
    note = '';
    detail = { ...cur, rating: v };
    onrated?.(cur.hash, v);
    try {
      await client.setRating(cur.hash, v);
    } catch (e) {
      if (detail && detail.id === cur.id) detail = { ...detail, rating: old };
      onrated?.(cur.hash, old);
      if (e instanceof ShareError && e.status === 409) note = 'Ratings are paused while photos are being moved on the computer. Try again later.';
      else if (e instanceof ShareError && e.status === 404) {
        note = goneText;
        ongone?.(cur.id);
      } else if (e instanceof ShareError && e.status === 403 && !e.state) {
        note = 'Ratings are no longer allowed in this share.';
        onforbidden?.();
      } else note = errorText(e);
    } finally {
      rating = false;
    }
  }

  // ---- A video or an original that did not load ----

  /** What to say about a media URL's answer; "" when it loads fine (the browser could not use it). */
  function problem(r: UrlCheck, what: 'video' | 'original'): string {
    if (r.status === 0) return UNREACHABLE;
    if (r.status === 404) return goneText;
    if (r.status === 403 && !r.state) return what === 'video' ? 'Videos are no longer allowed in this share.' : 'Originals are no longer allowed in this share.';
    if (r.status === 401 || r.status === 403) return ''; // the shell shows "removed" or "paused"
    if (r.status === 429 || r.status >= 500) return 'The computer is busy. Try again in a moment.';
    if (r.status >= 400) return `The computer answered ${r.status}.`;
    return '';
  }

  async function failed(url: string, what: 'video' | 'original') {
    const cur = id;
    let r: UrlCheck;
    try {
      r = await client.check(url);
    } catch {
      return;
    }
    if (cur !== id) return;
    mediaNote = problem(r, what);
    if (r.status === 403 && !r.state) onforbidden?.();
    if (r.status === 404) ongone?.(cur);
  }

  function onVideoError() {
    if (!d) return;
    videoFailed = true;
    mediaNote = '';
    void failed(client.fileUrl(d.id), 'video');
  }
  function retryVideo() {
    videoFailed = false;
    mediaNote = '';
    videoTry++;
  }
  function onFullError() {
    if (!d || fullFailed) return;
    fullFailed = true;
    void failed(client.fileUrl(d.id), 'original').then(() => {
      if (mediaNote) note = mediaNote;
    });
  }

  // ---- Zoom and bars ----

  function toggleFull() {
    if (!item || (isVideo && playable)) return;
    full = !full;
  }

  /**
   * Tab cycles inside the lightbox. The grid behind is `inert`, but Gecko
   * 91 (the Sailfish Browser) has no inert, so without this Tab would walk
   * into the page underneath.
   */
  function trapTab(e: KeyboardEvent) {
    const root = dialog;
    if (!root) return;
    const els = [...root.querySelectorAll<HTMLElement>('a[href], button:not([disabled]), input:not([disabled]), video[controls], [tabindex]:not([tabindex="-1"])')];
    const first = els[0];
    const last = els[els.length - 1];
    const a = document.activeElement;
    const inside = a instanceof Node && root.contains(a);
    if (!first || !last) {
      e.preventDefault();
      root.focus();
    } else if (e.shiftKey && (!inside || a === first || a === root)) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && (!inside || a === last)) {
      e.preventDefault();
      first.focus();
    }
  }

  function onKey(e: KeyboardEvent) {
    if (e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey) return;
    switch (e.key) {
      case 'Tab':
        trapTab(e);
        break;
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
        if (info) info = false;
        else if (full) full = false;
        else onclose();
        break;
    }
  }

  // ---- Touch ----

  let start: Point | null = null;
  let onVideo = false;
  let axis: 'x' | 'down' | null = null;
  let dragX = $state(0);
  let dragY = $state(0);
  let lastTap: Point | null = null;
  let tapTimer: ReturnType<typeof setTimeout> | null = null;

  /** The page is pinch-zoomed: swipes would fight the browser's panning. */
  function zoomed(): boolean {
    const vv = window.visualViewport;
    return !!vv && vv.scale > 1.01;
  }

  const pointOf = (t: Touch): Point => ({ x: t.clientX, y: t.clientY, t: Date.now() });

  function resetDrag() {
    start = null;
    axis = null;
    dragX = 0;
    dragY = 0;
  }

  function touchStart(e: TouchEvent) {
    const p = e.touches[0];
    const target = e.target instanceof Element ? e.target : null;
    if (e.touches.length !== 1 || !p || info || target?.closest('button, a, input')) {
      resetDrag();
      return;
    }
    // A tap may land anywhere; a swipe only away from the edges.
    if (!startsAwayFromEdges(p.clientX, window.innerWidth) && !full) {
      resetDrag();
      return;
    }
    onVideo = target instanceof HTMLVideoElement;
    // The player's own controls (the seek bar) sit at its bottom.
    if (onVideo && target && p.clientY > target.getBoundingClientRect().bottom - 72) {
      resetDrag();
      return;
    }
    start = pointOf(p);
    axis = null;
  }

  function touchMove(e: TouchEvent) {
    if (!start) return;
    const p = e.touches[0];
    if (e.touches.length !== 1 || !p) {
      resetDrag(); // a second finger: the browser's pinch
      return;
    }
    if (full || zoomed()) return; // the 1:1 view (or the zoomed page) pans natively
    const cur = pointOf(p);
    axis ??= dragAxis(start, cur);
    if (!axis) return;
    e.preventDefault(); // no page scroll or overscroll while we drag
    if (axis === 'x') dragX = cur.x - start.x;
    else dragY = Math.max(0, cur.y - start.y);
  }

  function touchEnd(e: TouchEvent) {
    const s = start;
    const p = e.changedTouches[0];
    resetDrag();
    if (!s || !p) return;
    const end = pointOf(p);
    const kind = classify(s, end);
    if (kind === 'tap') {
      if (onVideo) return; // the player shows its controls
      e.preventDefault(); // no synthetic click on top
      if (lastTap && end.t - lastTap.t < DOUBLE_TAP_MS && Math.abs(end.x - lastTap.x) < 40 && Math.abs(end.y - lastTap.y) < 40) {
        if (tapTimer) clearTimeout(tapTimer);
        tapTimer = null;
        lastTap = null;
        toggleFull();
      } else {
        lastTap = end;
        if (tapTimer) clearTimeout(tapTimer);
        tapTimer = setTimeout(() => {
          tapTimer = null;
          lastTap = null;
          chrome = !chrome;
        }, DOUBLE_TAP_MS);
      }
      return;
    }
    if (full || zoomed()) return;
    if (kind === 'next') go(nb.next);
    else if (kind === 'prev') go(nb.prev);
    else if (kind === 'close') onclose();
  }

  $effect(() => {
    const el = stage;
    if (!el) return;
    const opts: AddEventListenerOptions = { passive: false };
    el.addEventListener('touchstart', touchStart, opts);
    el.addEventListener('touchmove', touchMove, opts);
    el.addEventListener('touchend', touchEnd, opts);
    el.addEventListener('touchcancel', resetDrag);
    return () => {
      el.removeEventListener('touchstart', touchStart, opts);
      el.removeEventListener('touchmove', touchMove, opts);
      el.removeEventListener('touchend', touchEnd, opts);
      el.removeEventListener('touchcancel', resetDrag);
      if (tapTimer) clearTimeout(tapTimer);
    };
  });

  // Mouse (a tablet with a mouse, a desktop browser): click toggles the bars, double-click 1:1.
  function onStageClick(e: MouseEvent) {
    if (e.target instanceof Element && e.target.closest('button, a, video')) return;
    chrome = !chrome;
  }
  function onStageDblclick(e: MouseEvent) {
    if (e.target instanceof Element && e.target.closest('button, a, video')) return;
    toggleFull();
  }

  // ---- Info ----

  const camera = $derived.by(() => {
    if (!d) return '';
    const make = d.cameraMake.trim();
    const model = d.cameraModel.trim();
    if (!make) return model;
    if (!model) return make;
    return model.toLowerCase().startsWith(make.toLowerCase()) ? model : `${make} ${model}`;
  });
  const typeLabel = $derived.by(() => {
    if (!item) return '';
    const t = item.type === 'image_raw' ? 'RAW image' : item.type === 'video' ? 'Video' : 'Image';
    return d?.ext ? `${t} · ${d.ext.replace(/^\./, '').toUpperCase()}` : t;
  });
  const dragStyle = $derived(dragX || dragY ? `translate(${dragX}px, ${dragY}px)` : '');
</script>

<svelte:window onkeydown={onKey} />

<div class="lightbox" class:bare={!chrome} role="dialog" aria-modal="true" aria-label="Viewer" tabindex="-1" bind:this={dialog}>
  <!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
  <div class="stage" bind:this={stage} onclick={onStageClick} ondblclick={onStageDblclick}>
    {#if loadError}
      <div class="msg" role="alert">
        <p>{loadError}</p>
        <button onclick={onclose}>Close</button>
      </div>
    {:else if item}
      {#key item.id}
        <div class="media" class:video={playable && !!d && !videoFailed} style:transform={dragStyle} style:opacity={dragY ? Math.max(0.3, 1 - dragY / 400) : null}>
          {#if playable && d && !videoFailed}
            {#key videoTry}
              <!-- svelte-ignore a11y_media_has_caption -->
              <video controls playsinline preload="metadata" src={client.fileUrl(d.id)} poster={client.previewUrl(d)} onerror={onVideoError}></video>
            {/key}
          {:else if full && d}
            <div class="full">
              <img src={d.displayable && caps.download && !fullFailed ? client.fileUrl(d.id) : client.previewUrl(d)} alt="" onerror={onFullError} />
            </div>
          {:else}
            {#if thumbRequestable && !previewLoaded}
              <img class="fit" src={client.thumbUrl(item)} alt="" />
            {/if}
            {#if !previewFailed}
              <img
                class="fit preview"
                class:loaded={previewLoaded}
                src={client.previewUrl(item)}
                alt=""
                onload={() => (previewLoaded = true)}
                onerror={() => (previewFailed = true)}
              />
            {:else if !thumbRequestable}
              <div class="msg"><p>No preview available.</p></div>
            {/if}
          {/if}
        </div>
        {#if isVideo && !caps.video}
          <div class="banner">Video not available in this share</div>
        {:else if videoFailed && d}
          <div class="banner" role="alert">
            {#if mediaNote}
              <span>{mediaNote}</span>
              {#if mediaNote !== goneText}<button class="ghost" onclick={retryVideo}>Try again</button>{/if}
            {:else}
              <span>This video cannot be played in this browser.</span>
              {#if caps.download}<a class="btn" href={client.fileUrl(d.id, true)} download>Download</a>{/if}
            {/if}
          </div>
        {/if}
      {/key}
    {/if}

    {#if chrome && nb.prev}
      <button class="nav prev" onclick={() => go(nb.prev)} aria-label="Previous">‹</button>
    {/if}
    {#if chrome && nb.next}
      <button class="nav next" onclick={() => go(nb.next)} aria-label="Next">›</button>
    {/if}
  </div>

  <div class="bar top">
    <button class="ghost icon" onclick={onclose} aria-label="Close">✕</button>
    <span class="title">{capture ? capture.home : item ? formatTakenAt(item.takenAt, null) : ''}</span>
  </div>

  {#if note}<div class="note" role="status">{note}</div>{/if}

  <div class="bar bottom">
    {#if caps.rate && d}
      <RatingStars value={d.rating ?? 0} onchange={rate} disabled={rating} />
    {/if}
    <span class="spacer"></span>
    {#if caps.download && d}
      <a class="btn ghost" href={client.fileUrl(d.id, true)} download>Download</a>
    {/if}
    <button class="ghost" class:on={info} aria-pressed={info} onclick={() => (info = !info)}>Info</button>
  </div>

  {#if info}
    <aside class="sheet" aria-label="Details">
      <div class="sheet-head">
        <strong>Details</strong>
        <button class="ghost icon" aria-label="Close details" onclick={() => (info = false)}>✕</button>
      </div>
      {#if d}
        <dl>
          <dt>Date</dt>
          <dd>
            {capture?.home ?? ''}
            {#if capture?.local}<span class="local-time" title={capture.note ?? undefined}>{capture.local}</span>{/if}
            {#if capture?.note}<span class="local-time">{capture.note}</span>{/if}
          </dd>
          {#if camera}
            <dt>Camera</dt>
            <dd>{camera}</dd>
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
          {#if d.size}
            <dt>Size</dt>
            <dd>{formatBytes(d.size)}</dd>
          {/if}
          <dt>Type</dt>
          <dd>{typeLabel}</dd>
          {#if d.fileName}
            <dt>File</dt>
            <dd class="wrap">{d.fileName}</dd>
          {/if}
          {#if d.albums.length}
            <dt>Albums</dt>
            <dd>
              {#each d.albums as a, i (a.id)}{#if i > 0}, {/if}{#if albumHref}<a href={albumHref(a.id)}>{a.name}</a>{:else}{a.name}{/if}{/each}
            </dd>
          {/if}
          {#if caps.location && d.lat !== null && d.lon !== null}
            <dt>Location</dt>
            <dd class="mono">{formatCoord(d.lat)}, {formatCoord(d.lon)}</dd>
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
    --bar-h: 52px;
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    z-index: 1000;
    background: #000;
    color: #eee;
    overflow: hidden;
  }
  .lightbox:focus {
    outline: none;
  }
  .stage {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    user-select: none;
    -webkit-user-select: none;
  }
  .media {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
  }
  .fit {
    position: absolute;
    top: 0;
    left: 0;
    width: 100%;
    height: 100%;
    object-fit: contain;
    touch-action: pinch-zoom;
  }
  .preview {
    opacity: 0;
    transition: opacity 0.2s;
  }
  .preview.loaded {
    opacity: 1;
  }
  /* A playing video sits between the bars, never under them: the
     player's own controls (seek bar, time, play) stay reachable, and a
     tap meant for them cannot land on a star. The bars keep their own
     height (see .bar); the inset matches it. */
  .media.video {
    top: calc(var(--bar-h) + env(safe-area-inset-top, 0px));
    bottom: calc(var(--bar-h) + env(safe-area-inset-bottom, 0px));
  }
  video {
    position: absolute;
    top: 0;
    left: 0;
    width: 100%;
    height: 100%;
    background: #000;
  }
  .full {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    display: flex;
    overflow: auto;
    -webkit-overflow-scrolling: touch;
  }
  .full img {
    flex: none;
    margin: auto;
    max-width: none;
  }
  .msg {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 8px;
    padding: 24px;
    text-align: center;
    color: #bbb;
  }
  .banner {
    position: absolute;
    left: 12px;
    right: 12px;
    bottom: calc(76px + env(safe-area-inset-bottom, 0px));
    display: flex;
    align-items: center;
    justify-content: center;
    flex-wrap: wrap;
    gap: 10px;
    padding: 8px 12px;
    border-radius: var(--radius);
    background: rgba(0, 0, 0, 0.75);
    font-size: 14px;
    text-align: center;
  }
  .bar {
    position: absolute;
    left: 0;
    right: 0;
    display: flex;
    align-items: center;
    gap: 4px;
    min-height: var(--bar-h);
    padding-left: calc(6px + env(safe-area-inset-left, 0px));
    padding-right: calc(6px + env(safe-area-inset-right, 0px));
    background: rgba(0, 0, 0, 0.55);
    transition: opacity 0.15s;
  }
  .bar.top {
    top: 0;
    padding-top: env(safe-area-inset-top, 0px);
  }
  .bar.bottom {
    bottom: 0;
    padding-bottom: env(safe-area-inset-bottom, 0px);
  }
  .bare .bar {
    opacity: 0;
    pointer-events: none;
  }
  .bar button,
  .bar .btn {
    min-height: 44px;
    min-width: 44px;
    color: #eee;
    font-size: 15px;
  }
  .bar .on {
    background: rgba(255, 255, 255, 0.16);
  }
  .icon {
    font-size: 18px;
  }
  .local-time {
    display: block;
    opacity: 0.7;
    font-size: 0.9em;
  }
  .title {
    margin-left: 4px;
    font-weight: 500;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .note {
    position: absolute;
    left: 12px;
    right: 12px;
    bottom: calc(60px + env(safe-area-inset-bottom, 0px));
    padding: 10px 12px;
    border-radius: var(--radius);
    background: rgba(30, 30, 34, 0.95);
    border: 1px solid #444;
    font-size: 14px;
  }
  .nav {
    position: absolute;
    top: 50%;
    margin-top: -36px;
    width: 44px;
    height: 72px;
    border: 0;
    border-radius: var(--radius);
    background: rgba(0, 0, 0, 0.35);
    color: #fff;
    font-size: 34px;
    line-height: 1;
    opacity: 0.6;
  }
  .prev {
    left: calc(4px + env(safe-area-inset-left, 0px));
  }
  .next {
    right: calc(4px + env(safe-area-inset-right, 0px));
  }
  .sheet {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    max-height: 70%;
    overflow-y: auto;
    padding: 4px 16px calc(16px + env(safe-area-inset-bottom, 0px));
    border-radius: 12px 12px 0 0;
    background: var(--surface);
    color: var(--text);
    box-shadow: var(--shadow);
  }
  .sheet-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    min-height: 48px;
  }
  .sheet-head button {
    min-width: 44px;
    min-height: 44px;
  }
  dl {
    margin: 0;
  }
  dt {
    margin-top: 10px;
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
  .wrap {
    word-break: break-all;
  }
  @media (min-width: 900px) {
    .sheet {
      left: auto;
      top: 52px;
      width: 340px;
      max-height: none;
      border-radius: 0;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .preview,
    .bar {
      transition: none;
    }
  }
</style>
