<script lang="ts">
  // #/l/<id>/similar: near-duplicate review (docs/design/management.md
  // "Duplicates page"). Run starts the `similar` job (progress over the job
  // stream); each group offers a Keep radio per item, "Trash the others"
  // and "Ignore group". Keyboard: a group row is focusable, arrows move the
  // Keep choice, Enter trashes the others.
  import { untrack } from 'svelte';
  import { errorMessage, isAbort, type LibApi, type MediaDetail, type SimilarGroup, type SimilarPage } from '../lib/api';
  import { jobs, isActive } from '../lib/jobs.svelte';
  import { library } from '../lib/library.svelte';
  import { onChange } from '../lib/changes';
  import { toasts } from '../lib/toast.svelte';
  import { failureText, trashItems, undoTrash } from '../lib/actions';
  import { formatAgo, formatBytes, formatCount, formatInstant, formatWallDate, parseWhen } from '../lib/format';
  import { openViewer, setSequenceProvider, viewerHref } from '../lib/viewer';
  import { bestOf } from '../lib/similar';
  import JobProgress from '../components/JobProgress.svelte';
  import RatingStars from '../components/RatingStars.svelte';

  interface Props {
    api: LibApi;
  }
  let { api }: Props = $props();

  const LIMIT = 50;

  let offset = $state(0);
  let data = $state<SimilarPage | null>(null);
  let groups = $state<SimilarGroup[]>([]);
  let loading = $state(false);
  let error = $state('');
  /** Kept hash per group, by the group's first hash (stable while items leave). */
  let keep = $state<Record<string, string>>({});
  let busyGroup = $state<string | null>(null);
  let groupError = $state<Record<string, string>>({});

  const gkey = (g: SimilarGroup) => g.items.map((x) => x.hash).join(',');

  let ac: AbortController | null = null;
  async function load() {
    ac?.abort();
    const ctl = new AbortController();
    ac = ctl;
    loading = true;
    error = '';
    try {
      const r = await api.getSimilar(offset, LIMIT, ctl.signal);
      if (ctl.signal.aborted) return;
      if (r.groups.length === 0 && offset > 0 && r.total > 0) {
        offset = Math.max(0, Math.floor((r.total - 1) / LIMIT) * LIMIT);
        void load();
        return;
      }
      data = r;
      groups = r.groups;
      const k: Record<string, string> = {};
      for (const g of r.groups) k[gkey(g)] = keep[gkey(g)] ?? bestOf(g.items);
      keep = k;
      groupError = {};
    } catch (e) {
      if (!isAbort(e) && !ctl.signal.aborted) error = errorMessage(e);
    } finally {
      if (ac === ctl) loading = false;
    }
  }

  $effect(() => {
    void api.id;
    void offset;
    untrack(() => void load());
    return () => ac?.abort();
  });

  // A finished job (this run, or one elsewhere) changes the groups.
  let seenDone = untrack(() => jobs.doneCount);
  $effect(() => {
    const d = jobs.doneCount;
    if (d === seenDone) return;
    seenDone = d;
    untrack(() => void load());
  });
  $effect(() => jobs.watch());

  // Trashed items (here or in the lightbox) leave their groups at once; the
  // page is then fetched again, because the server's list has shifted: the
  // groups after this page move up into it (and the total is the server's).
  $effect(() =>
    onChange((c) => {
      if (c.kind === 'trashed' || c.kind === 'deleted') {
        dropHashes(c.hashes);
        untrack(() => void load());
      }
      if (c.kind === 'restored') untrack(() => void load());
      if (c.kind === 'rating') {
        const set = new Set(c.hashes);
        groups = groups.map((g) => ({ ...g, items: g.items.map((x) => (set.has(x.hash) ? { ...x, rating: c.rating || undefined } : x)) }));
      }
    }),
  );

  function dropHashes(hashes: readonly string[]) {
    const set = new Set(hashes);
    const before = groups.length;
    const next: SimilarGroup[] = [];
    const k: Record<string, string> = {};
    for (const g of groups) {
      const old = gkey(g);
      const items = g.items.filter((x) => !set.has(x.hash));
      if (items.length < 2) continue;
      const ng = { ...g, items };
      next.push(ng);
      const kept = keep[old];
      k[gkey(ng)] = kept && items.some((x) => x.hash === kept) ? kept : bestOf(items);
    }
    groups = next;
    keep = k;
    if (data) data = { ...data, total: Math.max(0, data.total - (before - next.length)) };
  }

  function removeGroup(g: SimilarGroup) {
    const key = gkey(g);
    groups = groups.filter((x) => gkey(x) !== key);
    if (data) data = { ...data, total: Math.max(0, data.total - 1) };
  }

  async function trashOthers(g: SimilarGroup) {
    const key = gkey(g);
    const kept = keep[key] ?? bestOf(g.items);
    const others = g.items.filter((x) => x.hash !== kept).map((x) => x.hash);
    if (!others.length || busyGroup) return;
    busyGroup = key;
    groupError = { ...groupError, [key]: '' };
    try {
      const r = await trashItems(api, others);
      if (r.failed.length) groupError = { ...groupError, [key]: failureText(r.failed, 'moved to the trash') };
      if (r.done.length) {
        const moved = r.done;
        toasts.show(`Moved ${formatCount(moved.length)} to the trash`, {
          ms: 8000,
          action: {
            label: 'Undo',
            run: () => undoTrash(api, moved),
          },
        });
      }
    } catch (e) {
      groupError = { ...groupError, [key]: errorMessage(e) };
    } finally {
      busyGroup = null;
    }
  }

  async function ignore(g: SimilarGroup) {
    const key = gkey(g);
    if (busyGroup) return;
    busyGroup = key;
    try {
      // Any hash names the group; the server ignores the whole current group (any size).
      const first = g.items[0];
      if (first) await api.ignoreSimilarGroup(first.hash);
      removeGroup(g);
      void load();
      void library.load();
    } catch (e) {
      groupError = { ...groupError, [key]: errorMessage(e) };
    } finally {
      busyGroup = null;
    }
  }

  function onGroupKey(e: KeyboardEvent, g: SimilarGroup) {
    const key = gkey(g);
    if (e.key === 'Enter') {
      // Only the row itself or a Keep radio: Enter on a thumbnail link opens the viewer.
      const onRadio = e.target instanceof HTMLInputElement && e.target.type === 'radio';
      if (e.target !== e.currentTarget && !onRadio) return;
      e.preventDefault();
      void trashOthers(g);
      return;
    }
    if (e.target !== e.currentTarget) return; // radios handle their own arrows
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight' && e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return;
    e.preventDefault();
    const i = g.items.findIndex((x) => x.hash === keep[key]);
    const step = e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : 1;
    const n = g.items.length;
    const next = g.items[(((i < 0 ? 0 : i) + step) % n + n) % n];
    if (next) keep = { ...keep, [key]: next.hash };
  }

  // ---- Run ----

  let distance = $state(untrack(() => library.info?.similar?.distance ?? 6));
  let distanceTouched = false;
  $effect(() => {
    const d = data?.distance ?? library.info?.similar?.distance;
    if (!distanceTouched && typeof d === 'number' && d >= 1 && d <= 7) distance = d;
  });
  let starting = $state(false);
  let runError = $state('');
  const job = $derived(jobs.current && jobs.current.kind === 'similar' && jobs.current.library === api.id && isActive(jobs.current) ? jobs.current : null);
  const otherActive = $derived(!!jobs.active && jobs.active !== job);

  async function run() {
    runError = '';
    starting = true;
    try {
      await jobs.start(api.id, { kind: 'similar', distance });
    } catch (e) {
      runError = errorMessage(e);
    } finally {
      starting = false;
    }
  }

  const lastRun = $derived(parseWhen(data?.at ?? library.info?.similar?.at ?? null));
  const total = $derived(data?.total ?? 0);

  // The lightbox steps through this page's items.
  $effect(() =>
    setSequenceProvider({
      async neighbours(item: MediaDetail) {
        const list = groups.flatMap((g) => g.items);
        const i = list.findIndex((x) => x.id === item.id);
        if (i < 0) return { prev: null, next: null };
        return { prev: list[i - 1] ?? null, next: list[i + 1] ?? null };
      },
    }),
  );

  function open(e: MouseEvent, id: number) {
    if (e.button !== 0 || e.ctrlKey || e.metaKey || e.shiftKey) return;
    e.preventDefault();
    openViewer(api.id, id);
  }
</script>

<div class="page">
  <div class="page-inner">
    <h1>Duplicates</h1>
    <p class="muted small where">Near-duplicate photos in <span class="mono">{library.info?.root ?? library.name}</span></p>

    <section class="card run">
      <div class="row">
        <span>
          {#if lastRun}
            Last run {formatAgo(lastRun)} <span class="muted small">({formatInstant(lastRun.toISOString())})</span> at distance
            {data?.distance ?? library.info?.similar?.distance ?? '?'}, {formatCount(total)}
            {total === 1 ? 'group' : 'groups'}
          {:else if data}
            Not run yet. Run the search to find visually similar photos.
          {/if}
        </span>
        <span class="spacer"></span>
        <label class="dist" title="How different two photos may look and still count as near-duplicates: 1 finds only almost identical photos, 7 also finds looser matches.">
          Distance
          <select
            value={String(distance)}
            onchange={(e) => {
              distanceTouched = true;
              distance = Number(e.currentTarget.value);
            }}
          >
            {#each [1, 2, 3, 4, 5, 6, 7] as d (d)}
              <option value={String(d)}>{d}{d === 6 ? ' (default)' : d === 1 ? ' (strict)' : d === 7 ? ' (loose)' : ''}</option>
            {/each}
          </select>
        </label>
        <button class="primary" disabled={starting || !!job || otherActive} onclick={run} title="Search the library for near-duplicate photos. Nothing is changed or deleted.">Run</button>
      </div>
      {#if otherActive}<p class="muted small">Another job is running; run the search when it has finished.</p>{/if}
      {#if runError}<div class="error-box">{runError}</div>{/if}
      {#if job}<div class="job"><JobProgress {job} /></div>{/if}
    </section>

    {#if error}
      <div class="error-box">{error} <button class="small" onclick={() => load()}>Retry</button></div>
    {/if}

    {#if data && total === 0 && !loading}
      <div class="empty">
        {#if lastRun}
          <h2>No near-duplicates left</h2>
          <p>Every group has been handled. Run the search again after importing more photos.</p>
        {:else}
          <h2>No search yet</h2>
        {/if}
      </div>
    {/if}

    {#if groups.length}
      <ol class="groups" start={offset + 1}>
        {#each groups as g (gkey(g))}
          {@const key = gkey(g)}
          <li class="group">
            <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
            <div
              class="items"
              role="radiogroup"
              tabindex="0"
              aria-label="Group of {g.items.length}, distance {g.maxDistance}. Arrows choose the photo to keep, Enter trashes the others."
              onkeydown={(e) => onGroupKey(e, g)}
            >
              {#each g.items as it (it.hash)}
                <div class="item" class:kept={keep[key] === it.hash}>
                  <a class="thumb" href={viewerHref(api.id, it.id)} onclick={(e) => open(e, it.id)} aria-label="View {formatWallDate(it.takenAt)}">
                    <img src={api.thumbUrl(it)} alt="" loading="lazy" decoding="async" />
                  </a>
                  <label class="keep" title="The photo to keep: Trash the others moves the rest of the group to the trash.">
                    <input
                      type="radio"
                      name="keep-{key}"
                      checked={keep[key] === it.hash}
                      onchange={() => (keep = { ...keep, [key]: it.hash })}
                    />
                    Keep
                  </label>
                  <div class="meta small">
                    <div>{it.width || it.w} × {it.height || it.h} · {formatBytes(it.size)}</div>
                    <div>{formatWallDate(it.takenAt)}</div>
                    <div class="mono path" title={it.relPath}>{it.relPath}</div>
                    {#if it.rating}<RatingStars value={it.rating} small />{/if}
                  </div>
                </div>
              {/each}
            </div>
            <div class="actions">
              <span class="muted small" title="The largest difference between two photos of this group (0 = look identical).">distance ≤ {g.maxDistance}</span>
              <span class="spacer"></span>
              <button
                class="danger small"
                disabled={busyGroup !== null}
                onclick={() => trashOthers(g)}
                title="Move every photo of this group except the one marked Keep to the trash. They can be restored from the Trash page until it is emptied."
                >Trash the others ({g.items.length - 1})</button
              >
              <button
                class="small"
                disabled={busyGroup !== null}
                onclick={() => ignore(g)}
                title="Stop showing these photos as duplicates of each other. Nothing is deleted. The app has no way to undo this."
              >Ignore group</button>
            </div>
            {#if groupError[key]}<div class="error-box small">{groupError[key]}</div>{/if}
          </li>
        {/each}
      </ol>
    {/if}

    {#if total > LIMIT}
      <nav class="row pager" aria-label="Pages">
        <button disabled={offset === 0 || loading} onclick={() => (offset = Math.max(0, offset - LIMIT))}>Previous</button>
        <span class="muted">Groups {formatCount(offset + 1)}–{formatCount(Math.min(total, offset + LIMIT))} of {formatCount(total)}</span>
        <button disabled={offset + LIMIT >= total || loading} onclick={() => (offset += LIMIT)}>Next</button>
      </nav>
    {/if}
  </div>
</div>

<style>
  .where {
    margin: -6px 0 16px;
    word-break: break-all;
  }
  .dist {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .job {
    margin-top: 12px;
  }
  .groups {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .group {
    margin-bottom: 14px;
    padding: 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
  }
  .items {
    display: flex;
    gap: 10px;
    overflow-x: auto;
    padding: 2px;
    border-radius: var(--radius-sm);
  }
  .item {
    flex: none;
    width: 180px;
    padding: 6px;
    border: 2px solid transparent;
    border-radius: var(--radius-sm);
  }
  .item.kept {
    border-color: var(--ok);
  }
  .item:not(.kept) img {
    opacity: 0.75;
  }
  .thumb {
    display: block;
    aspect-ratio: 1;
    overflow: hidden;
    border-radius: 4px;
    background: var(--placeholder);
  }
  .thumb img {
    display: block;
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .keep {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    margin: 6px 0 2px;
    cursor: pointer;
  }
  .meta {
    color: var(--muted);
  }
  .path {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .actions {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 8px;
  }
  .pager {
    justify-content: center;
  }
</style>
