<script lang="ts">
  import type { JobResult } from '../lib/api';
  import { resultLabels } from '../lib/jobtext';
  import { formatCount } from '../lib/format';

  interface Props {
    results: JobResult[];
    errors: JobResult[];
  }
  let { results, errors }: Props = $props();

  // Newest first.
  const recent = $derived(results.slice().reverse());
</script>

<div class="results">
  {#if errors.length}
    <div class="pinned" aria-label="Errors">
      <div class="head">{formatCount(errors.length)} {errors.length === 1 ? 'error' : 'errors'}</div>
      {#each errors as r (r.seq)}
        <div class="row error">
          <span class="st">Error</span>
          <span class="path mono" title={r.source}>{r.source}</span>
          {#if r.message}<span class="msg">{r.message}</span>{/if}
        </div>
      {/each}
    </div>
  {/if}
  <div class="recent" aria-label="Recent results" aria-live="off">
    {#each recent as r (r.seq)}
      <div class="row s-{r.status}">
        <span class="st">{resultLabels[r.status] ?? r.status}</span>
        <span class="path mono" title={r.dest ? `${r.source} → ${r.dest}` : r.source}>
          {r.source}{#if r.dest}<span class="dest"> → {r.dest}</span>{/if}
        </span>
        {#if r.message && r.status !== 'imported'}<span class="msg">{r.message}</span>{/if}
      </div>
    {:else}
      <p class="muted small empty-list">No results yet.</p>
    {/each}
  </div>
</div>

<style>
  .results {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--bg);
    max-height: 420px;
    overflow-y: auto;
    font-size: 13px;
  }
  .pinned {
    position: sticky;
    top: 0;
    z-index: 1;
    max-height: 180px;
    overflow-y: auto;
    background: var(--surface);
    border-bottom: 1px solid var(--danger);
  }
  .head {
    padding: 6px 10px;
    color: var(--danger);
    font-weight: 600;
  }
  .row {
    display: grid;
    grid-template-columns: 120px 1fr;
    column-gap: 10px;
    padding: 3px 10px;
    border-bottom: 1px solid color-mix(in srgb, var(--border) 50%, transparent);
  }
  .st {
    font-weight: 500;
  }
  .path {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dest {
    color: var(--muted);
  }
  .msg {
    grid-column: 2;
    color: var(--muted);
    font-size: 12px;
    word-break: break-word;
  }
  .error .st,
  .error .msg {
    color: var(--danger);
  }
  .s-imported .st {
    color: var(--ok);
  }
  .s-imported_existing .st {
    color: var(--muted);
  }
  .s-duplicate .st {
    color: var(--warn);
  }
  .s-skipped_small .st {
    color: var(--faint);
  }
  .empty-list {
    padding: 8px 10px;
    margin: 0;
  }
</style>
