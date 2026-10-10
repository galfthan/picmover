<script lang="ts">
  import { errorMessage, type Job } from '../lib/api';
  import { jobs, isActive, processed } from '../lib/jobs.svelte';
  import { formatBytes, formatCount, formatElapsed } from '../lib/format';
  import { jobTitle } from '../lib/jobtext';
  import ConfirmButton from './ConfirmButton.svelte';

  interface Props {
    job: Job;
  }
  let { job }: Props = $props();

  let now = $state(Date.now());
  $effect(() => {
    if (!isActive(job)) return;
    const t = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(t);
  });

  const started = $derived(job.startedAt ? Date.parse(job.startedAt) : NaN);
  const elapsed = $derived.by(() => {
    if (Number.isNaN(started)) return 0;
    const end = job.finishedAt ? Date.parse(job.finishedAt) : now;
    return Math.max(0, end - started);
  });

  const s = $derived(job.stats);
  const thumbsStarted = $derived(s.thumbsDone + s.thumbsUnsupported + s.thumbsFailed > 0);
  const fraction = $derived((job.kind === 'import' || job.kind === 'rebuild') && s.scanned > 0 ? Math.min(1, processed(s) / s.scanned) : null);

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

<div class="progress">
  <div class="row">
    <h2>
      {jobTitle(job)}
      {#if job.state === 'queued'}queued{:else}running{/if}
    </h2>
    <span class="muted">{formatElapsed(elapsed)}</span>
    <span class="spacer"></span>
    {#if cancelling}
      <span class="muted">Cancelling…</span>
    {:else}
      <ConfirmButton
        label="Cancel"
        question={job.kind === 'import'
          ? 'Stop the import? Files already imported stay in the library.'
          : job.kind === 'similar'
            ? 'Stop the duplicate search?'
            : job.kind === 'rebuild'
              ? 'Stop recording? The catalog stays incomplete until a rebuild finishes.'
              : 'Stop generating thumbnails?'}
        confirmLabel="Stop"
        cancelLabel="Keep going"
        danger
        onconfirm={cancel}
      />
    {/if}
  </div>
  {#if job.source}
    <p class="muted small mono src">{job.move ? 'Moving' : 'Copying'} from {job.source}</p>
  {/if}
  {#if cancelError}<div class="error-box">{cancelError}</div>{/if}
  {#if jobs.reconnecting}<div class="notice">Connection lost; reconnecting…</div>{/if}

  {#if fraction !== null}
    <div class="bar" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={Math.round(fraction * 100)}>
      <span class="fill" style:width="{fraction * 100}%"></span>
    </div>
  {/if}

  {#if job.kind === 'import'}
    <div class="stats">
      <div class="stat"><span class="v">{formatCount(s.scanned)}</span><span class="k">Scanned</span></div>
      <div class="stat"><span class="v">{formatCount(s.imported)}</span><span class="k">Imported</span></div>
      <div class="stat"><span class="v">{formatCount(s.existing)}</span><span class="k">Already in place</span></div>
      <div class="stat"><span class="v">{formatCount(s.duplicates)}</span><span class="k">Duplicates</span></div>
      <div class="stat"><span class="v">{formatCount(s.skippedSmall)}</span><span class="k">Skipped (small)</span></div>
      <div class="stat" class:bad={s.errors > 0}><span class="v">{formatCount(s.errors)}</span><span class="k">Errors</span></div>
      <div class="stat"><span class="v">{formatBytes(s.bytesCopied)}</span><span class="k">Copied</span></div>
    </div>
  {/if}
  {#if job.kind === 'rebuild'}
    <div class="stats">
      <div class="stat"><span class="v">{formatCount(s.scanned)}</span><span class="k">Scanned</span></div>
      <div class="stat"><span class="v">{formatCount(s.imported + s.existing)}</span><span class="k">Recorded</span></div>
      <div class="stat" class:bad={s.errors > 0}><span class="v">{formatCount(s.errors)}</span><span class="k">Errors</span></div>
    </div>
  {/if}
  {#if job.kind === 'similar'}
    <p class="muted">
      {#if job.phase?.phase === 'load'}Loading {formatCount(job.phase.items ?? 0)} images…
      {:else if job.phase?.phase === 'compare'}Comparing{job.phase.bands ? ` (${formatCount(job.phase.bands)} bands)` : ''}…
      {:else if job.phase?.phase === 'done'}Found {formatCount(job.phase.pairs ?? 0)} similar pairs.
      {:else}Starting…{/if}
    </p>
  {/if}
  {#if thumbsStarted || job.kind === 'thumbs'}
    <h3 class="sub">Thumbnails</h3>
    <div class="stats">
      <div class="stat"><span class="v">{formatCount(s.thumbsDone)}</span><span class="k">Done</span></div>
      <div class="stat"><span class="v">{formatCount(s.thumbsUnsupported)}</span><span class="k">Unsupported</span></div>
      <div class="stat" class:bad={s.thumbsFailed > 0}><span class="v">{formatCount(s.thumbsFailed)}</span><span class="k">Failed</span></div>
    </div>
  {/if}
</div>

<style>
  h2 {
    margin: 0;
  }
  .src {
    margin: 4px 0 12px;
    word-break: break-all;
  }
  .bar {
    height: 6px;
    border-radius: 3px;
    background: var(--surface-3);
    overflow: hidden;
    margin: 8px 0 14px;
  }
  .fill {
    display: block;
    height: 100%;
    background: var(--accent);
    transition: width 0.25s;
  }
  .sub {
    margin: 16px 0 8px;
    color: var(--muted);
    font-weight: 500;
  }
</style>
