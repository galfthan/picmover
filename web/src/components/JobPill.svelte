<script lang="ts">
  import { jobs, processed } from '../lib/jobs.svelte';
  import { formatCount } from '../lib/format';
  import { navigate } from '../lib/router';
  import { libPath } from '../lib/routes';
  import { libraries } from '../lib/libraries.svelte';
  import { library } from '../lib/library.svelte';

  const job = $derived(jobs.active);
  const libName = $derived(job?.library ? (libraries.find(job.library)?.name ?? job.library) : '');

  const label = $derived.by(() => {
    if (!job) return '';
    const s = job.stats;
    if (job.state === 'queued') return 'Job queued…';
    if (job.kind === 'thumbs') {
      return `Thumbnails: ${formatCount(s.thumbsDone + s.thumbsUnsupported + s.thumbsFailed)} done…`;
    }
    if (job.kind === 'similar') {
      const p = job.phase;
      if (p?.phase === 'compare') return 'Finding duplicates: comparing…';
      if (p?.phase === 'load' && p.items) return `Finding duplicates in ${formatCount(p.items)} images…`;
      return 'Finding duplicates…';
    }
    if (job.kind === 'rebuild') {
      return s.scanned > 0 ? `Recording ${formatCount(s.scanned)} files…` : 'Recording files in place…';
    }
    const done = processed(s);
    if (s.scanned > 0 && done >= s.scanned && s.thumbsDone + s.thumbsUnsupported + s.thumbsFailed > 0) {
      return `Thumbnails for ${formatCount(s.imported + s.existing)} files…`;
    }
    return `Importing ${formatCount(s.scanned)} files…`;
  });

  function open() {
    const j = job;
    if (!j) return;
    const lib = j.library ?? library.id;
    if (lib === null) return;
    if (j.kind === 'rebuild') navigate(libraries.mode === 'workspace' ? '/' : libPath(lib, 'library'));
    else navigate(libPath(lib, j.kind === 'import' ? 'import' : j.kind === 'similar' ? 'similar' : 'library'));
  }

  const fraction = $derived.by(() => {
    if (!job || job.kind !== 'import') return null;
    const s = job.stats;
    if (s.scanned <= 0) return null;
    return Math.min(1, processed(s) / s.scanned);
  });
</script>

{#if job}
  <button class="pill" onclick={open} title="Show progress{libName ? ` (library ${libName})` : ''}">
    <span class="dot" aria-hidden="true"></span>
    {#if libName}<span class="lib">{libName}</span>{/if}
    <span class="text">{label}</span>
    {#if fraction !== null}
      <span class="bar" aria-hidden="true"><span class="fill" style:width="{Math.round(fraction * 100)}%"></span></span>
      <span class="pct">{Math.round(fraction * 100)}%</span>
    {/if}
    {#if jobs.reconnecting}
      <span class="muted small">reconnecting…</span>
    {/if}
  </button>
{/if}

<style>
  .pill {
    gap: 8px;
    border-radius: 999px;
    min-height: 30px;
    padding: 0 12px;
    max-width: 420px;
    min-width: 0;
    background: var(--surface-2);
  }
  .lib {
    flex: none;
    max-width: 120px;
    overflow: hidden;
    text-overflow: ellipsis;
    font-weight: 600;
  }
  .text {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .dot {
    flex: none;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--accent);
    animation: pulse 1.4s ease-in-out infinite;
  }
  .bar {
    flex: none;
    width: 70px;
    height: 4px;
    border-radius: 2px;
    background: var(--surface-3);
    overflow: hidden;
  }
  .fill {
    display: block;
    height: 100%;
    background: var(--accent);
    transition: width 0.25s;
  }
  .pct {
    font-variant-numeric: tabular-nums;
    color: var(--muted);
    font-size: 0.9em;
  }
  @media (max-width: 900px) {
    .pill {
      max-width: 260px;
    }
    .bar {
      display: none;
    }
  }
  @keyframes pulse {
    50% {
      opacity: 0.35;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .dot {
      animation: none;
    }
  }
</style>
