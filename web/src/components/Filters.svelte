<script lang="ts">
  import type { FileType, Filters } from '../lib/api';
  import { library } from '../lib/library.svelte';
  import { hasFilters } from '../lib/filters';
  import { parseWallClock } from '../lib/format';

  interface Props {
    filters: Filters;
    onchange: (f: Filters) => void;
    /** Hide the "has location" toggle (the map implies it). */
    hideGps?: boolean;
  }
  let { filters, onchange, hideGps = false }: Props = $props();

  const types: { value: FileType | ''; label: string }[] = [
    { value: '', label: 'All types' },
    { value: 'image', label: 'Photos' },
    { value: 'image_raw', label: 'RAW' },
    { value: 'video', label: 'Videos' },
  ];

  const cameras = $derived(library.info?.cameras ?? []);

  const years = $derived.by(() => {
    const s = library.info?.summary;
    const a = parseWallClock(s?.earliest)?.getUTCFullYear();
    const b = parseWallClock(s?.latest)?.getUTCFullYear();
    const out: number[] = [];
    if (a !== undefined && b !== undefined && b >= a) for (let y = b; y >= a && out.length < 300; y--) out.push(y);
    // Keep a filtered year selectable even if the summary does not cover it.
    for (const v of [filters.from, filters.to]) {
      const y = v ? Number(v.slice(0, 4)) - (v === filters.to ? 1 : 0) : NaN;
      if (Number.isInteger(y) && !out.includes(y)) out.push(y);
    }
    return out.sort((x, y) => y - x);
  });

  const fromYear = $derived(filters.from ? filters.from.slice(0, 4) : '');
  // `to` is exclusive: to=2025-01-01 means "through 2024".
  const toYear = $derived(filters.to ? String(Number(filters.to.slice(0, 4)) - 1) : '');

  function set(patch: Partial<Filters>) {
    const f: Filters = { ...filters, ...patch };
    for (const k of Object.keys(f) as (keyof Filters)[]) {
      if (f[k] === undefined || f[k] === '' || f[k] === false) delete f[k];
    }
    onchange(f);
  }

  function onType(e: Event) {
    const v = (e.currentTarget as HTMLSelectElement).value;
    set({ type: v === '' ? undefined : (v as FileType) });
  }
  function onCamera(e: Event) {
    const v = (e.currentTarget as HTMLSelectElement).value;
    set({ camera: v || undefined });
  }
  function onFrom(e: Event) {
    const v = (e.currentTarget as HTMLSelectElement).value;
    set({ from: v ? `${v}-01-01` : undefined });
  }
  function onTo(e: Event) {
    const v = (e.currentTarget as HTMLSelectElement).value;
    set({ to: v ? `${Number(v) + 1}-01-01` : undefined });
  }
</script>

<div class="filters" role="search" aria-label="Filters">
  <select aria-label="Type" value={filters.type ?? ''} onchange={onType} data-filter-focus>
    {#each types as t (t.value)}
      <option value={t.value}>{t.label}</option>
    {/each}
  </select>

  {#if !hideGps}
    <label class="check">
      <input type="checkbox" checked={!!filters.gps} onchange={(e) => set({ gps: e.currentTarget.checked || undefined })} />
      Has location
    </label>
  {/if}

  <select aria-label="Camera" value={filters.camera ?? ''} onchange={onCamera}>
    <option value="">All cameras</option>
    {#each cameras as c (c.make + '\u0000' + c.model)}
      <option value={c.model}>{c.model} ({c.count})</option>
    {/each}
    {#if filters.camera && !cameras.some((c) => c.model === filters.camera)}
      <option value={filters.camera}>{filters.camera}</option>
    {/if}
  </select>

  <span class="years">
    <select aria-label="From year" value={fromYear} onchange={onFrom}>
      <option value="">From</option>
      {#each years as y (y)}<option value={String(y)}>{y}</option>{/each}
    </select>
    <span class="muted">–</span>
    <select aria-label="To year" value={toYear} onchange={onTo}>
      <option value="">To</option>
      {#each years as y (y)}<option value={String(y)}>{y}</option>{/each}
    </select>
  </span>

  {#if filters.session}
    <span class="chip">
      This import (#{filters.session})
      <button class="ghost small" aria-label="Remove import filter" onclick={() => set({ session: undefined })}>×</button>
    </span>
  {/if}

  {#if hasFilters(filters)}
    <button class="ghost small" onclick={() => onchange({})}>Clear</button>
  {/if}
</div>

<style>
  .filters {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    min-width: 0;
  }
  select {
    max-width: 220px;
  }
  .check {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    white-space: nowrap;
    cursor: pointer;
  }
  .years {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    padding-left: 10px;
    border-radius: 999px;
    background: var(--accent-strong);
    color: #fff;
    font-size: 0.9em;
  }
  .chip button {
    color: #fff;
    border-radius: 999px;
  }
</style>
