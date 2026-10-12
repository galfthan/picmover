<script lang="ts">
  // Inline progress of a backup, scrub, prune, restore or transfer (move to library) job: phase, files
  // and bytes done of total, rate, ETA, the current file and Cancel. Fed by
  // the job's SSE progress events through jobs.svelte.ts (job.phase).
  // Rate and ETA come from the server when it sends them. A scrub with a
  // budget stops when the budget is used, so its bar shows the time used of
  // the budget and no ETA (the server's totals are the whole pass). Only a
  // small status line (phase changes) is a live region.
  import { errorMessage, type Job } from '../lib/api';
  import { jobs, isActive } from '../lib/jobs.svelte';
  import { formatBytes, formatCount, formatElapsed } from '../lib/format';
  import { backupStatusText, jobTitle } from '../lib/jobtext';
  import { RateMeter, etaMs, etaText, formatRate, phaseLabel, progressFraction, serverEtaMs, serverRate, SCRUB_BUDGETS } from '../lib/backupinfo';
  import ConfirmButton from './ConfirmButton.svelte';

  interface Props {
    job: Job;
    /** Seconds of a budgeted scrub, when known. */
    budget?: number | null;
    /** Own live status line; false when the parent keeps one that outlives this block (TargetCard). */
    live?: boolean;
  }
  let { job, budget = null, live = true }: Props = $props();

  const p = $derived(job.phase ?? null);
  const files = $derived(p?.files ?? 0);
  const bytes = $derived(p?.bytes ?? 0);
  const totalFiles = $derived(p?.totalFiles ?? 0);
  const totalBytes = $derived(p?.totalBytes ?? 0);
  const failed = $derived(p?.failed ?? 0);
  const snapshot = $derived(p?.phase === 'snapshot');

  // Rate over the last 20 s of byte counts when the server sends none; restarts when a phase counts from zero.
  const meter = new RateMeter();
  let localRate = $state<number | null>(null);
  let lastPhase = '';
  $effect(() => {
    const phase = p?.phase ?? '';
    const b = bytes;
    if (phase !== lastPhase) {
      meter.reset();
      lastPhase = phase;
    }
    meter.add(Date.now(), b);
    localRate = meter.rate();
  });
  const sRate = $derived(serverRate(p));
  const sEta = $derived(serverEtaMs(p));
  const rate = $derived(sRate !== undefined ? sRate : localRate);

  let now = $state(Date.now());
  $effect(() => {
    if (!isActive(job)) return;
    const t = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(t);
  });
  const elapsed = $derived.by(() => {
    const s = job.startedAt ? Date.parse(job.startedAt) : NaN;
    return Number.isNaN(s) ? 0 : Math.max(0, now - s);
  });

  const budgeted = $derived(job.kind === 'scrub' && !!budget && budget > 0);
  const budgetLabel = $derived(budget ? (SCRUB_BUDGETS.find((b) => b.seconds === budget)?.label ?? formatElapsed(budget * 1000)) : '');
  const fraction = $derived(budgeted && budget ? Math.min(1, elapsed / (budget * 1000)) : snapshot ? null : progressFraction(p));
  const eta = $derived(budgeted || snapshot ? null : sEta !== undefined ? sEta : etaMs(bytes, totalBytes, rate));
  const valueText = $derived(
    budgeted ? `${formatElapsed(elapsed)} of ${budgetLabel}, ${formatCount(files)} files checked` : `${formatCount(files)}${totalFiles ? ` of ${formatCount(totalFiles)}` : ''} files`,
  );
  /** What the live region says: changes with the phase only, never with the counters. */
  const status = $derived(backupStatusText(job));

  const question = $derived(
    job.kind === 'backup'
      ? 'Stop the backup? Files already copied stay on the target; the next run continues.'
      : job.kind === 'scrub'
        ? 'Stop the scrub? The next scrub continues where this one stopped.'
        : job.kind === 'restore'
          ? 'Stop restoring? Files already copied back stay in the library.'
          : job.kind === 'transfer'
            ? 'Stop moving? Files already moved stay in the other library.'
            : 'Stop pruning?',
  );

  let cancelling = $state(false);
  let cancelError = $state('');
  async function cancel() {
    cancelling = true;
    cancelError = '';
    try {
      await jobs.cancel(job.id);
    } catch (e) {
      cancelError = errorMessage(e);
      cancelling = false;
    }
  }
</script>

<div class="bprogress">
  {#if live}<p class="sr-only live" aria-live="polite">{status}</p>{/if}
  <div class="row head">
    <strong>{jobTitle(job)}</strong>
    <span class="phase">{job.state === 'queued' ? 'Queued' : snapshot ? 'Saving catalog snapshots…' : phaseLabel(job.kind, p?.phase)}</span>
    <span class="muted small">{formatElapsed(elapsed)}</span>
    <span class="spacer"></span>
    {#if cancelling}
      <span class="muted">Cancelling…</span>
    {:else}
      <ConfirmButton label="Cancel" {question} confirmLabel="Stop" cancelLabel="Keep going" danger onconfirm={cancel} />
    {/if}
  </div>

  {#if fraction !== null}
    <div
      class="bar"
      class:budget={budgeted}
      role="progressbar"
      aria-label="{jobTitle(job)} progress"
      aria-valuemin="0"
      aria-valuemax="100"
      aria-valuenow={Math.round(fraction * 100)}
      aria-valuetext={valueText}
    >
      <span class="fill" style:width="{fraction * 100}%"></span>
    </div>
  {/if}

  {#if budgeted}
    <div class="nums small">
      <span class="files">{formatCount(files)} {files === 1 ? 'file' : 'files'} checked</span>
      <span class="bytes">{formatBytes(bytes)}</span>
      {#if rate !== null}<span class="rate">{formatRate(rate)}</span>{/if}
      <span class="budget-left">stops after {budgetLabel}</span>
      {#if failed > 0}<span class="failed">{formatCount(failed)} failed</span>{/if}
    </div>
  {:else if !snapshot}
    <div class="nums small">
      <span class="files">{formatCount(files)}{totalFiles ? ` of ${formatCount(totalFiles)}` : ''} files</span>
      <span class="bytes">{formatBytes(bytes)}{totalBytes ? ` of ${formatBytes(totalBytes)}` : ''}</span>
      {#if rate !== null}<span class="rate">{formatRate(rate)}</span>{/if}
      {#if etaText(eta)}<span class="eta">{etaText(eta)}</span>{/if}
      {#if failed > 0}<span class="failed">{formatCount(failed)} failed</span>{/if}
    </div>
  {/if}
  {#if p?.current && !snapshot}
    <div class="current mono small" title={p.current}>{p.current}</div>
  {/if}
  {#if cancelError}<div class="error-box small">{cancelError}</div>{/if}
  {#if jobs.reconnecting}<div class="notice small">Connection lost; reconnecting…</div>{/if}
</div>

<style>
  .bprogress {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .head {
    gap: 10px;
  }
  .phase {
    color: var(--muted);
  }
  .bar {
    height: 6px;
    border-radius: 3px;
    background: var(--surface-3);
    overflow: hidden;
  }
  .fill {
    display: block;
    height: 100%;
    background: var(--accent);
    transition: width 0.25s;
  }
  .nums {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 14px;
    font-variant-numeric: tabular-nums;
  }
  .failed {
    color: var(--danger);
  }
  .current {
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
    color: var(--muted);
  }
</style>
