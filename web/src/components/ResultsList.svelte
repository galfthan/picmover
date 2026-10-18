<script lang="ts">
  import type { JobResult } from '../lib/api';
  import { resultLabels, kindLabels } from '../lib/jobtext';
  import { formatCount } from '../lib/format';
  import { filterResults, mergeResults, statusCounts, kindCounts, type KindFilter, type StatusFilter } from '../lib/results';
  import CopyButton from './CopyButton.svelte';

  interface Props {
    results: JobResult[];
    errors: JobResult[];
    /** The job's error count (stats.errors), which may exceed the errors kept. */
    errorCount?: number;
  }
  let { results, errors, errorCount = 0 }: Props = $props();

  let status = $state<StatusFilter>('all');
  let kind = $state<KindFilter>('all');

  // One list, newest first: the errors are kept longer than the others,
  // so they stay in it after many successes.
  const all = $derived(mergeResults(results, errors));
  const counts = $derived(statusCounts(all));
  const kinds = $derived(kindCounts(all));
  const shown = $derived(filterResults(all, status, kind));
  const nErrors = $derived(Math.max(errorCount, counts.error ?? 0));

  const STATUSES: [StatusFilter, string][] = [
    ['imported', 'Imported'],
    ['imported_existing', 'Already in place'],
    ['duplicate', 'Duplicates'],
    ['skipped_small', 'Skipped'],
  ];
  const KINDS: [KindFilter, string][] = [
    ['image', 'Photos'],
    ['image_raw', 'RAW'],
    ['video', 'Videos'],
  ];

  const errorLine = (r: JobResult) => (r.message ? `${r.source}: ${r.message}` : r.source);
  const allErrorsText = $derived(
    all
      .filter((r) => r.status === 'error')
      .map(errorLine)
      .join('\n'),
  );
</script>

<div class="filters" role="group" aria-label="Filter files">
  <div class="chips" role="radiogroup" aria-label="Outcome">
    <button class="chip" role="radio" aria-checked={status === 'all'} onclick={() => (status = 'all')}>All</button>
    <button class="chip err" class:has={nErrors > 0} role="radio" aria-checked={status === 'error'} onclick={() => (status = 'error')}>
      Errors <span class="n">{formatCount(nErrors)}</span>
    </button>
    {#each STATUSES as [s, label] (s)}
      {#if counts[s]}
        <button class="chip" role="radio" aria-checked={status === s} onclick={() => (status = s)}>
          {label} <span class="n">{formatCount(counts[s] ?? 0)}</span>
        </button>
      {/if}
    {/each}
  </div>
  <div class="chips" role="radiogroup" aria-label="Type">
    <button class="chip" role="radio" aria-checked={kind === 'all'} onclick={() => (kind = 'all')}>All types</button>
    {#each KINDS as [k, label] (k)}
      {#if kinds[k]}
        <button class="chip" role="radio" aria-checked={kind === k} onclick={() => (kind = k)}>
          {label} <span class="n">{formatCount(kinds[k] ?? 0)}</span>
        </button>
      {/if}
    {/each}
  </div>
  {#if status === 'error' && allErrorsText}
    <CopyButton text={allErrorsText} label="Copy all errors" />
  {/if}
</div>

<div class="results" aria-label="Files" aria-live="off">
  {#if nErrors > errors.length && status === 'error'}
    <p class="muted small note">The last {formatCount(errors.length)} of {formatCount(nErrors)} errors are shown; the job log has all of them.</p>
  {/if}
  {#each shown as r (r.seq)}
    {#if r.status === 'error'}
      <div class="row error" data-status="error">
        <span class="st">✕ Error</span>
        <span class="path mono" title={r.source}>{r.source}</span>
        <span class="msg">{r.message || 'unknown error'}</span>
        <span class="copy"><CopyButton text={errorLine(r)} /></span>
      </div>
    {:else}
      <div class="row s-{r.status}" data-status={r.status}>
        <span class="st">{resultLabels[r.status] ?? r.status}{#if r.kind && kindLabels[r.kind]}<span class="kind"> · {kindLabels[r.kind]}</span>{/if}</span>
        <span class="path mono" title={r.dest ? `${r.source} → ${r.dest}` : r.source}>
          {r.source}{#if r.dest}<span class="dest"> → {r.dest}</span>{/if}
        </span>
        {#if r.message && r.status !== 'imported'}<span class="msg">{r.message}</span>{/if}
      </div>
    {/if}
  {:else}
    <p class="muted small empty-list">{all.length ? 'No files match the filter.' : 'No results yet.'}</p>
  {/each}
</div>

<style>
  .filters {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 14px;
    margin-bottom: 8px;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .chip {
    padding: 2px 10px;
    border-radius: 999px;
    font-size: 12px;
    background: var(--surface-3);
    border: 1px solid transparent;
    color: var(--text);
  }
  .chip[aria-checked='true'] {
    border-color: var(--accent);
    background: color-mix(in srgb, var(--accent) 15%, var(--surface));
  }
  .chip .n {
    color: var(--muted);
  }
  .chip.err.has,
  .chip.err.has .n {
    color: var(--danger);
    font-weight: 600;
  }
  .results {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--bg);
    max-height: 420px;
    overflow-y: auto;
    font-size: 13px;
  }
  .note {
    margin: 0;
    padding: 6px 10px;
  }
  .row {
    display: grid;
    grid-template-columns: 140px 1fr auto;
    column-gap: 10px;
    padding: 3px 10px;
    border-bottom: 1px solid color-mix(in srgb, var(--border) 50%, transparent);
  }
  .st {
    font-weight: 500;
  }
  .kind {
    color: var(--faint);
    font-weight: 400;
  }
  .path {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    grid-column: 2 / 4;
  }
  .dest {
    color: var(--muted);
  }
  .msg {
    grid-column: 2;
    color: var(--muted);
    font-size: 12px;
    word-break: break-word;
    user-select: text;
  }
  .copy {
    grid-column: 3;
    grid-row: 2;
    align-self: start;
  }
  .error {
    border-left: 3px solid var(--danger);
    background: color-mix(in srgb, var(--danger) 6%, transparent);
  }
  .error .path {
    user-select: text;
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
  @media (max-width: 560px) {
    .row {
      grid-template-columns: 1fr auto;
    }
    .path,
    .msg {
      grid-column: 1 / 3;
    }
    .copy {
      grid-column: 2;
      grid-row: 1;
    }
  }
</style>
