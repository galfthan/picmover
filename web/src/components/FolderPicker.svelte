<script lang="ts">
  import { untrack } from 'svelte';
  import { errorMessage, isAbort, listDir, type FsListing } from '../lib/api';
  import { breadcrumbs } from '../lib/paths';
  import { formatCount } from '../lib/format';

  interface Props {
    /** Directory to start in ("" = the roots). */
    start?: string;
    onchoose: (path: string) => void;
    /** The choose button's label. */
    chooseLabel?: string;
    /** What the counts line ends with (the import says subfolders are imported too). */
    countsNote?: string;
    /** The hint on the roots listing. */
    rootsHint?: string;
    /** Disables the choose button (browsing still works). */
    disabled?: boolean;
  }
  let {
    start = '',
    onchoose,
    chooseLabel = 'Import from this folder',
    countsNote = 'subfolders are imported too.',
    rootsHint = 'Choose a drive or folder to import from.',
    disabled = false,
  }: Props = $props();

  let listing = $state<FsListing | null>(null);
  let error = $state('');
  let loading = $state(false);
  let typed = $state('');
  let ac: AbortController | null = null;

  async function go(path: string, fallbackToRoots = false) {
    ac?.abort();
    const ctl = new AbortController();
    ac = ctl;
    loading = true;
    error = '';
    try {
      const l = await listDir(path, ctl.signal);
      if (ctl.signal.aborted) return;
      listing = l;
      typed = l.path;
    } catch (e) {
      if (isAbort(e) || ctl.signal.aborted) return;
      if (fallbackToRoots && path !== '') {
        void go('');
        return;
      }
      error = errorMessage(e);
    } finally {
      if (ac === ctl) loading = false;
    }
  }

  $effect(() => {
    const s = start;
    untrack(() => void go(s, true));
    return () => ac?.abort();
  });

  const crumbs = $derived(listing ? breadcrumbs(listing.path) : []);
  const isRoots = $derived(!!listing && listing.path === '');

  function submitTyped(e: SubmitEvent) {
    e.preventDefault();
    const p = typed.trim();
    if (p) void go(p);
  }
</script>

<div class="picker">
  <form class="pathbar" onsubmit={submitTyped}>
    <button type="button" onclick={() => go('')} title="Drives and common locations">Roots</button>
    <input type="text" bind:value={typed} placeholder="Paste a folder path" aria-label="Folder path" spellcheck="false" autocomplete="off" />
    <button type="submit">Go</button>
  </form>

  {#if crumbs.length}
    <nav class="crumbs" aria-label="Current folder">
      {#each crumbs as c, i (c.path)}
        {#if i > 0}<span class="sep" aria-hidden="true">›</span>{/if}
        <button class="ghost small" onclick={() => go(c.path)} aria-current={i === crumbs.length - 1 ? 'location' : undefined}>{c.name}</button>
      {/each}
    </nav>
  {/if}

  {#if error}
    <div class="error-box">{error}</div>
  {/if}

  <div class="list" aria-busy={loading}>
    {#if listing}
      {#if listing.parent}
        <button class="entry" onclick={() => go(listing?.parent ?? '')}>
          <span class="icon" aria-hidden="true">↰</span><span>..</span>
        </button>
      {/if}
      {#each listing.dirs as d (d.path)}
        <button class="entry" onclick={() => go(d.path)} title={d.path}>
          <span class="icon" aria-hidden="true">📁</span><span class="name">{d.name}</span>
        </button>
      {:else}
        <p class="muted pad">{isRoots ? 'No locations found.' : 'No subfolders.'}</p>
      {/each}
    {:else if loading}
      <p class="muted pad">Loading…</p>
    {/if}
  </div>

  {#if listing && !isRoots}
    <div class="choose">
      <div class="counts">
        <strong>{formatCount(listing.mediaFiles ?? 0)}</strong> media files and
        <strong>{formatCount(listing.otherFiles ?? 0)}</strong> other files directly in this folder{countsNote ? `; ${countsNote}` : '.'}
      </div>
      <button class="primary" {disabled} onclick={() => listing && onchoose(listing.path)}>{chooseLabel}</button>
    </div>
  {:else if isRoots}
    <p class="muted small">{rootsHint}</p>
  {/if}
</div>

<style>
  .picker {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .pathbar {
    display: flex;
    gap: 8px;
  }
  .pathbar input {
    flex: 1;
    min-width: 0;
    font-family: var(--mono);
  }
  .crumbs {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 2px;
  }
  .crumbs button[aria-current] {
    font-weight: 600;
  }
  .sep {
    color: var(--faint);
  }
  .list {
    display: flex;
    flex-direction: column;
    height: 320px;
    overflow-y: auto;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--bg);
    padding: 4px;
  }
  .entry {
    justify-content: flex-start;
    gap: 10px;
    min-height: 30px;
    border: 0;
    background: transparent;
    text-align: left;
  }
  .entry .name {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .icon {
    width: 1.2em;
    text-align: center;
  }
  .pad {
    padding: 6px 10px;
    margin: 0;
  }
  .choose {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
  }
  .counts {
    flex: 1;
    min-width: 240px;
    color: var(--muted);
  }
  .counts strong {
    color: var(--text);
  }
</style>
