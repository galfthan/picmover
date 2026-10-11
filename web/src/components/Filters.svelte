<script lang="ts">
  import type { FileType, Filters } from '../lib/api';
  import { library } from '../lib/library.svelte';
  import { untrack } from 'svelte';
  import { hasFilters } from '../lib/filters';
  import { importTitle } from '../lib/imports';
  import { parseWallClock } from '../lib/format';
  import DateFilter, { type DateValue } from './DateFilter.svelte';
  import TagInput, { type Pick } from './TagInput.svelte';

  interface Props {
    filters: Filters;
    onchange: (f: Filters) => void;
    /** Hide the "has location" toggle (the map implies it). */
    hideGps?: boolean;
    /** Hide the album select (the album page implies it). */
    hideAlbum?: boolean;
    /** Hide the import chip (the import record page implies it). */
    hideImport?: boolean;
  }
  let { filters, onchange, hideGps = false, hideAlbum = false, hideImport = false }: Props = $props();

  const types: { value: FileType | ''; label: string }[] = [
    { value: '', label: 'All types' },
    { value: 'image', label: 'Photos' },
    { value: 'image_raw', label: 'RAW' },
    { value: 'video', label: 'Videos' },
  ];

  const cameras = $derived(library.info?.cameras ?? []);
  const tags = $derived(library.tags);
  const albums = $derived(library.albums);

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

  function set(patch: Partial<Filters>) {
    const f: Filters = { ...filters, ...patch };
    for (const k of Object.keys(f) as (keyof Filters)[]) {
      const v = f[k];
      if (v === undefined || v === '' || v === false || (Array.isArray(v) && v.length === 0)) delete f[k];
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
  function onRating(e: Event) {
    const v = Number((e.currentTarget as HTMLSelectElement).value);
    set({ rating: v > 0 ? v : undefined });
  }
  function onAlbum(e: Event) {
    const v = Number((e.currentTarget as HTMLSelectElement).value);
    set({ album: v > 0 ? v : undefined });
  }
  function onDates(v: DateValue) {
    set({ from: v.from, to: v.to, recur: v.recur });
  }
  function addTag(p: Pick) {
    if ('create' in p) return;
    const cur = filters.tags ?? [];
    if (!cur.includes(p.item.id)) set({ tags: [...cur, p.item.id].sort((a, b) => a - b) });
  }
  function removeTag(id: number) {
    set({ tags: (filters.tags ?? []).filter((t) => t !== id) });
  }
  const tagName = (id: number) => tags.find((t) => t.id === id)?.name ?? `#${id}`;

  // The import chip names the record; the list is loaded on demand, once
  // per record id the store does not know yet.
  const importRec = $derived(filters.import ? (library.imports.find((r) => r.id === filters.import) ?? null) : null);
  const importName = $derived(importRec ? importTitle(importRec) : `#${filters.import ?? ''}`);
  let askedImport = 0;
  $effect(() => {
    const iid = hideImport || library.id === null ? undefined : filters.import;
    if (!iid || importRec || iid === askedImport) return;
    askedImport = iid;
    untrack(() => void library.loadImports());
  });
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

  <DateFilter from={filters.from} to={filters.to} recur={filters.recur} {years} onchange={onDates} />

  <select aria-label="Minimum rating" value={String(filters.rating ?? 0)} onchange={onRating}>
    <option value="0">Any rating</option>
    {#each [1, 2, 3, 4, 5] as n (n)}
      <option value={String(n)}>{'★'.repeat(n)}{n < 5 ? ' or more' : ''}</option>
    {/each}
  </select>

  {#if tags.length || filters.tags?.length}
    <span class="tags">
      {#each filters.tags ?? [] as id (id)}
        <span class="chip">
          {tagName(id)}
          <button class="ghost small" aria-label="Remove tag filter {tagName(id)}" onclick={() => removeTag(id)}>×</button>
        </span>
      {/each}
      <TagInput options={tags} exclude={filters.tags ?? []} allowCreate={false} placeholder="Tag…" label="Filter by tag" onpick={addTag} />
    </span>
  {/if}

  {#if !hideAlbum && (albums.length || filters.album)}
    <select aria-label="Album" value={String(filters.album ?? 0)} onchange={onAlbum}>
      <option value="0">All albums</option>
      {#each albums as a (a.id)}
        <option value={String(a.id)}>{a.name}</option>
      {/each}
      {#if filters.album && !albums.some((a) => a.id === filters.album)}
        <option value={String(filters.album)}>Album #{filters.album}</option>
      {/if}
    </select>
  {/if}

  {#if filters.session}
    <span class="chip">
      This import (#{filters.session})
      <button class="ghost small" aria-label="Remove import session filter" onclick={() => set({ session: undefined })}>×</button>
    </span>
  {/if}

  {#if filters.import && !hideImport}
    <span class="chip">
      Import: {importName}
      <button class="ghost small" aria-label="Remove import filter {importName}" onclick={() => set({ import: undefined })}>×</button>
    </span>
  {/if}

  {#if hasFilters(filters, { album: hideAlbum, import: hideImport })}
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
  .tags {
    display: inline-flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 4px;
  }
  .tags :global(input) {
    min-width: 90px;
    width: 110px;
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
