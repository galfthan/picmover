<script lang="ts">
  import { tick, untrack } from 'svelte';
  import { errorMessage, isAbort, type LibApi, type MediaDetail, type MediaItem } from '../lib/api';
  import { formatAgo, formatBytes, formatCoord, formatDuration, formatInstant, formatTakenAt, formatWallDate, parseLocalWallClock } from '../lib/format';
  import { closeViewer, sequenceProvider, showInViewer, viewerHref, type Neighbours } from '../lib/viewer';
  import { libHref } from '../lib/routes';
  import { importLine } from '../lib/imports';
  import { isBoolean, loadJSON, saveJSON } from '../lib/storage';
  import { library } from '../lib/library.svelte';
  import { toasts } from '../lib/toast.svelte';
  import { addToAlbum, deleteItems, failureText, rateItems, removeFromAlbum, restoreItems, tagItems, trashItems, undoTrash } from '../lib/actions';
  import { canMove, moves } from '../lib/moves.svelte';
  import { onChange } from '../lib/changes';
  import { libraries } from '../lib/libraries.svelte';
  import CopyButton from './CopyButton.svelte';
  import RatingStars from './RatingStars.svelte';
  import TagInput, { type Pick } from './TagInput.svelte';
  import AlbumPicker from './AlbumPicker.svelte';
  import ConfirmButton from './ConfirmButton.svelte';

  interface Props {
    id: number;
    api: LibApi;
    /** Hash of the page underneath, used when closing a deep-linked viewer. */
    baseHash: string;
  }
  let { id, api, baseHash }: Props = $props();

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
    api.getMedia(cur, ac.signal)
      .then((md) => {
        if (ac.signal.aborted) return;
        detail = md;
        const p = sequenceProvider();
        p?.shown?.(md);
        return p?.neighbours(md).then((n) => {
          if (ac.signal.aborted) return;
          nb = n;
          for (const x of [n.next, n.prev]) {
            if (x) new Image().src = api.previewUrl(x);
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
    // Props must not be read once the component is gone: take the library now.
    const lib = untrack(() => api.id);
    untrack(() => dialog?.focus({ preventScroll: true }));
    return () => {
      const shown = lastId;
      void tick().then(() =>
        setTimeout(() => {
          const ae = document.activeElement;
          if (ae && ae !== document.body && ae.isConnected) return; // something else took focus
          const target =
            document.querySelector<HTMLElement>(`a[href="${viewerHref(lib, shown)}"]`) ??
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
    showInViewer(api.id, target.id);
  }

  // ---- Editing (rating, tags, albums, trash) ----

  let tagInput: HTMLInputElement | undefined = $state();
  let albumOpen = $state(false);
  let working = $state(false);
  let editError = $state('');

  const trashed = $derived(!!d?.deletedAt);
  const rating = $derived(d?.rating ?? 0);
  const itemTags = $derived(d?.tags ?? []);
  const itemAlbums = $derived(d?.albums ?? []);

  /** Runs an edit on the shown item; `apply` patches the detail when it is still shown. */
  async function edit(fn: (cur: MediaDetail) => Promise<((x: MediaDetail) => MediaDetail) | void>) {
    const cur = d;
    if (!cur || working) return;
    working = true;
    editError = '';
    try {
      const apply = await fn(cur);
      if (apply && detail && detail.id === cur.id) detail = apply(detail);
    } catch (e) {
      editError = errorMessage(e);
    } finally {
      working = false;
    }
  }

  function rate(v: number) {
    void edit(async (cur) => {
      await rateItems(api, [cur.hash], v);
      return (x) => ({ ...x, rating: v });
    });
  }

  function addTag(p: Pick) {
    void edit(async (cur) => {
      const tags = await tagItems(api, [cur.hash], 'create' in p ? { create: [p.create] } : { add: [p.item.id] });
      const added = tags.length ? tags : 'create' in p ? [] : [{ id: p.item.id, name: p.item.name }];
      return (x) => {
        const have = x.tags ?? [];
        const more = added.filter((t) => !have.some((h) => h.id === t.id)).map((t) => ({ id: t.id, name: t.name }));
        return { ...x, tags: [...have, ...more].sort((a, b) => a.name.localeCompare(b.name)) };
      };
    });
  }

  function removeTag(tid: number) {
    void edit(async (cur) => {
      await tagItems(api, [cur.hash], { remove: [tid] });
      return (x) => ({ ...x, tags: (x.tags ?? []).filter((t) => t.id !== tid) });
    });
  }

  function addAlbum(p: Pick) {
    void edit(async (cur) => {
      const a = await addToAlbum(api, [cur.hash], 'create' in p ? { create: p.create } : { album: { id: p.item.id, name: p.item.name } });
      return (x) => ({ ...x, albums: [...(x.albums ?? []).filter((y) => y.id !== a.id), a] });
    });
  }

  function removeAlbum(aid: number) {
    void edit(async (cur) => {
      await removeFromAlbum(api, aid, [cur.hash]);
      return (x) => ({ ...x, albums: (x.albums ?? []).filter((y) => y.id !== aid) });
    });
  }

  /** After the shown item left this view: the next one, else the previous, else close. */
  function leave() {
    const after = nb.next ?? nb.prev;
    if (after) go(after);
    else close();
  }

  function trash() {
    void edit(async (cur) => {
      const target = nb;
      const r = await trashItems(api, [cur.hash]);
      if (r.failed.length) throw new Error(failureText(r.failed, 'moved to the trash'));
      toasts.show('Moved to the trash', {
        ms: 8000,
        action: {
          label: 'Undo',
          run: () => undoTrash(api, [cur.hash]),
        },
      });
      nb = target;
      leave();
    });
  }

  function restore() {
    void edit(async (cur) => {
      const target = nb;
      const r = await restoreItems(api, [cur.hash]);
      if (r.failed.length) throw new Error(failureText(r.failed, 'restored'));
      toasts.show('Restored');
      nb = target;
      leave();
    });
  }

  function deleteForever() {
    void edit(async (cur) => {
      const target = nb;
      const r = await deleteItems(api, [cur.hash]);
      if (r.failed.length) throw new Error(failureText(r.failed, 'deleted'));
      toasts.show('Deleted permanently');
      nb = target;
      leave();
    });
  }

  const movable = $derived(!!d && !trashed && canMove());

  function move() {
    if (!d || trashed || !canMove()) return;
    moves.open(api.id, [d.hash], 'viewer');
  }

  // The shown item was moved to another library (here or from the selection bar): go on to the next.
  $effect(() =>
    onChange((c) => {
      if (c.kind !== 'moved' || c.lib !== untrack(() => api.id)) return;
      const cur = untrack(() => d);
      if (cur && c.hashes.includes(cur.hash)) leave();
    }),
  );

  async function focusTags() {
    if (!showInfo) toggleInfo();
    await tick();
    tagInput?.focus();
  }

  async function openAlbums() {
    if (!showInfo) toggleInfo();
    await tick();
    albumOpen = true;
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
    // The move dialog is on top: its keys are not the viewer's.
    if (moves.dialog) return;
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
      case '0':
      case '1':
      case '2':
      case '3':
      case '4':
      case '5':
        if (!d || trashed) return;
        e.preventDefault();
        rate(Number(e.key));
        break;
      case 't':
        if (!d || trashed) return;
        e.preventDefault();
        void focusTags();
        break;
      case 'b':
        if (!d || trashed) return;
        e.preventDefault();
        void openAlbums();
        break;
      case 'Delete':
      case 'Backspace':
        if (!d || trashed) return;
        e.preventDefault();
        trash();
        break;
      case 'r':
        if (!d || !trashed) return;
        e.preventDefault();
        restore();
        break;
      case 'm':
        if (!movable) return;
        e.preventDefault();
        move();
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
      {#if d && !trashed}
        <button class="ghost" onclick={trash} disabled={working} title="Move to the trash (Delete)">Trash</button>
      {:else if d}
        <button class="ghost" onclick={restore} disabled={working} title="Restore (R)">Restore</button>
      {/if}
      {#if d}
        <a class="btn ghost" href={api.fileUrl(d.id, true)} download title="Download the original">Download</a>
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
            <video bind:this={videoEl} controls preload="metadata" src={api.fileUrl(d.id)} poster={api.previewUrl(d)}></video>
          {:else if full && d}
            <div class="full">
              <img src={api.fileUrl(d.id)} alt="" />
            </div>
          {:else}
            {#if thumbRequestable && !thumbFailed && !previewLoaded}
              <img class="fit blur" src={api.thumbUrl(item)} alt="" onerror={() => (thumbFailed = true)} />
            {/if}
            {#if !previewFailed}
              <img
                class="fit preview"
                class:loaded={previewLoaded}
                src={api.previewUrl(item)}
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
                <a class="btn small" href={api.fileUrl(d.id, true)} download>Download original</a>
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
        {#if trashed}
          <div class="trashinfo">
            <strong>In the trash</strong>
            <span class="muted small">
              Deleted {formatAgo(parseLocalWallClock(d.deletedAt))}{#if d.deletedAt}&nbsp;({formatWallDate(d.deletedAt)}){/if}
            </span>
            <div class="row">
              <button class="primary small" disabled={working} onclick={restore}>Restore</button>
              <ConfirmButton
                label="Delete permanently"
                question="Delete this file permanently?"
                confirmLabel="Delete"
                cancelLabel="Cancel"
                danger
                disabled={working}
                onconfirm={deleteForever}
              />
            </div>
          </div>
        {/if}
        {#if editError}<div class="error-box small" role="alert">{editError}</div>{/if}
        <dl>
          {#if !trashed}
            <dt>Rating</dt>
            <dd><RatingStars value={rating} onchange={rate} disabled={working} /></dd>

            <dt>Tags</dt>
            <dd class="chips">
              {#each itemTags as t (t.id)}
                <span class="chip">
                  <a href={libHref(api.id, '', { tag: t.id })}>{t.name}</a>
                  <button class="ghost small" aria-label="Remove tag {t.name}" disabled={working} onclick={() => removeTag(t.id)}>×</button>
                </span>
              {/each}
              <TagInput
                bind:inputEl={tagInput}
                options={library.tags}
                exclude={itemTags.map((t) => t.id)}
                label="Add tag (T)"
                placeholder="Add tag… (T)"
                disabled={working}
                onpick={addTag}
              />
            </dd>

            <dt>Albums</dt>
            <dd class="chips">
              {#each itemAlbums as a (a.id)}
                <span class="chip">
                  <a href={libHref(api.id, `albums/${a.id}`)}>{a.name}</a>
                  <button class="ghost small" aria-label="Remove from album {a.name}" disabled={working} onclick={() => removeAlbum(a.id)}>×</button>
                </span>
              {/each}
              <AlbumPicker
                albums={library.albums}
                exclude={itemAlbums.map((a) => a.id)}
                label="Add to album (B)"
                placement="down"
                disabled={working}
                bind:open={albumOpen}
                onpick={addAlbum}
              />
            </dd>

            {#if movable}
              <dt>Library</dt>
              <dd class="chips">
                <span>{libraries.find(api.id)?.name ?? api.id}</span>
                <button class="small" disabled={working || moves.running} onclick={move} title="Move to another library (M)">Move to…</button>
              </dd>
            {/if}
          {/if}

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
              <a href={libHref(api.id, 'map', { at: `${d.lat},${d.lon}`, z: 15 })}>Show on map</a>
            </dd>
          {/if}

          <dt>Imported</dt>
          <dd>
            {#if d.imports?.length}
              <ul class="imports">
                {#each d.imports as r (r.id)}
                  <li><a href={libHref(api.id, `imports/${r.id}`)}>{importLine(r, formatInstant(r.startedAt))}</a></li>
                {/each}
              </ul>
            {:else}
              {formatInstant(d.importedAt)}
              {#if d.sessionId}
                <br /><a href={libHref(api.id, '', { session: d.sessionId })}>Session #{d.sessionId}</a>
              {/if}
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
  .imports {
    margin: 0;
    padding: 0;
    list-style: none;
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
  .chips {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
    margin-top: 4px;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    padding-left: 9px;
    border-radius: 999px;
    background: var(--surface-3);
    font-size: 0.92em;
  }
  .chip a {
    color: var(--text);
  }
  .chip button {
    min-height: 22px;
    border-radius: 999px;
  }
  .trashinfo {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin-bottom: 14px;
    padding: 10px 12px;
    border: 1px solid var(--danger);
    border-radius: var(--radius-sm);
    background: var(--danger-bg);
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
