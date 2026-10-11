<script lang="ts">
  // #/l/<id>/imports: the library's import records (user.db), newest first.
  // Each row links to the record's grid (#/l/<id>/imports/<iid>), where it
  // can be renamed or deleted. docs/design/imports.md "Web".
  import { untrack } from 'svelte';
  import type { LibApi } from '../lib/api';
  import { library } from '../lib/library.svelte';
  import { formatBytes, formatInstant } from '../lib/format';
  import { formatRecent, importCounts, importTitle, modeLabel, outcomeLabel } from '../lib/imports';
  import { libHref } from '../lib/routes';

  interface Props {
    api: LibApi;
  }
  let { api }: Props = $props();

  let loaded = $state(false);
  let loadError = $state('');
  async function reload() {
    const id = api.id;
    const err = await library.loadImports();
    if (api.id !== id) return;
    loadError = err ?? '';
    loaded = true;
  }
  // The shell selects the library in the store after this page mounts; load once it has.
  $effect(() => {
    if (library.id !== api.id) return;
    untrack(() => void reload());
  });

  const records = $derived(library.imports);
</script>

<div class="page">
  <div class="page-inner">
    <div class="row head">
      <h1>Imports</h1>
      <span class="spacer"></span>
      <a class="btn primary" href={libHref(api.id, 'import')}>Import media</a>
    </div>
    <p class="muted small where">In <span class="mono">{library.info?.root ?? library.name}</span></p>

    {#if loadError}
      <div class="error-box">{loadError} <button class="small" onclick={reload}>Retry</button></div>
    {/if}

    {#if records.length === 0}
      {#if loaded && !loadError}
        <div class="empty">
          <h2>No imports yet</h2>
          <p>Every import into this library is listed here, with the photos and videos it brought in.</p>
        </div>
      {:else if !loaded}
        <p class="muted">Loading…</p>
      {/if}
    {:else}
      <ul class="list" aria-label="Imports">
        {#each records as r (r.id)}
          {@const outcome = outcomeLabel(r.outcome)}
          <li>
            <a class="rec" href={libHref(api.id, `imports/${r.id}`)}>
              <span class="cover">
                {#if r.cover}
                  <img src={api.thumbUrl({ hash: r.cover })} alt="" loading="lazy" decoding="async" />
                {/if}
              </span>
              <span class="body">
                <span class="title">
                  <span class="name">{importTitle(r)}</span>
                  <span class="chip mode">{modeLabel(r.mode)}</span>
                  {#if outcome}<span class="chip outcome tone-{r.outcome}">{outcome}</span>{/if}
                </span>
                <span class="meta muted small">
                  <time datetime={r.startedAt ?? undefined} title={formatInstant(r.startedAt)}>{formatRecent(r.startedAt)}</time>
                  {#if r.name.trim() && r.sourceName}<span class="src" title={r.source}>from {r.sourceName}</span>
                  {:else if r.source}<span class="src mono" title={r.source}>{r.source}</span>{/if}
                </span>
                <span class="counts small">
                  <span class:bad={r.errors > 0}>{importCounts(r)}</span>
                  {#if r.bytes > 0}<span class="muted"> · {formatBytes(r.bytes)}</span>{/if}
                </span>
              </span>
            </a>
          </li>
        {/each}
      </ul>
    {/if}
  </div>
</div>

<style>
  .head h1 {
    margin: 0;
  }
  .where {
    margin: 4px 0 16px;
    word-break: break-all;
  }
  .list {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .rec {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 8px;
    border-radius: var(--radius);
    color: inherit;
  }
  .rec:hover {
    text-decoration: none;
    background: var(--surface-2);
  }
  .rec:hover .name {
    color: var(--accent);
  }
  .cover {
    flex: none;
    width: 72px;
    height: 72px;
    border-radius: var(--radius-sm);
    overflow: hidden;
    background: var(--placeholder);
  }
  .cover img {
    display: block;
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: 3px;
    min-width: 0;
  }
  .title {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: 8px;
  }
  .name {
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: 10px;
    min-width: 0;
  }
  .src {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .chip {
    padding: 0 8px;
    border-radius: 999px;
    background: var(--surface-3);
    color: var(--muted);
    font-size: 0.8em;
  }
  .tone-failed {
    color: var(--danger);
  }
  .bad {
    color: var(--danger);
  }
</style>
