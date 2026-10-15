<script lang="ts">
  // The phone's filter sheet (docs/design/lan-share.md "Filters on the
  // phone"): a bottom sheet with one section per filter, edited as a draft
  // and put in the route by Apply. The choices come from the share's facets
  // (GET /api/l/{lib}/facets), which the server computes inside the share's
  // scope; a section the share may not use (rating without `rate`, location
  // without `location`, albums and tags in an album share) is not shown.
  // Native selects for long lists and dates: every phone browser has a
  // picker for them, the Sailfish Browser included. No hover, 44 px targets.
  import { describeMonths, describeRecur, formatRecur, normalizeMonths, parseRecur, recurPresets, type MonthDay, type Recur } from '../lib/dates';
  import { describeSize } from '../lib/filters';
  import { monthName } from '../lib/format';
  import { cameraLabel, cleanFilters, dayList, fromMonth, MAX_TAGS, monthEnd, monthStart, ratingLabel, searchCameras, tapYear, toMonth, UNKNOWN_CAMERA, wholeYears, type FilterScope, type MonthYear } from './filters';
  import type { ShareFacets, ShareFilters } from './shareapi';

  interface Props {
    filters: ShareFilters;
    facets: ShareFacets | null;
    /** Why the facets could not be read ("" when they could or are loading). */
    facetsError?: string;
    scope: FilterScope;
    /** The album page of a library share: the album is the page, not a filter. */
    albumImplied?: boolean;
    onapply: (f: ShareFilters) => void;
    onclose: () => void;
  }
  let { filters, facets, facetsError = '', scope, albumImplied = false, onapply, onclose }: Props = $props();

  const initial = () => ({ ...filters, months: filters.months ? [...filters.months] : undefined, tags: filters.tags ? [...filters.tags] : undefined });
  let draft = $state<ShareFilters>(initial());
  let dateTab = $state<'range' | 'year'>(initial().recur || initial().months?.length ? 'year' : 'range');

  function set(patch: Partial<ShareFilters>) {
    draft = cleanFilters({ ...draft, ...patch });
  }

  // ---- Type ----

  const types = $derived.by(() => {
    const t = facets?.types;
    const out: { value: ShareFilters['type'] | ''; label: string; n: number | null }[] = [
      { value: '', label: 'All', n: facets ? facets.total : null },
      { value: 'image', label: 'Photos', n: t ? t.image : null },
      { value: 'video', label: 'Videos', n: t ? t.video : null },
    ];
    if ((t && t.image_raw > 0) || draft.type === 'image_raw') out.push({ value: 'image_raw', label: 'RAW', n: t ? t.image_raw : null });
    return out;
  });

  // ---- Dates ----

  const MONTHS = Array.from({ length: 12 }, (_, i) => i + 1);
  const ENDS = ['from', 'to'] as const;
  const years = $derived.by(() => {
    const ys = new Set((facets?.years ?? []).map((y) => y.year));
    for (const m of [fromMonth(draft.from), toMonth(draft.to)]) if (m) ys.add(m.year);
    return [...ys].sort((a, b) => b - a);
  });
  const yearCounts = $derived(new Map((facets?.years ?? []).map((y) => [y.year, y.count])));
  const span = $derived(wholeYears(draft.from, draft.to));
  const inSpan = (y: number) => !!span && y >= span.a && y <= span.b;
  const fromM = $derived(fromMonth(draft.from));
  const toM = $derived(toMonth(draft.to));

  function setFrom(part: 'month' | 'year', v: string) {
    if (part === 'year' && v === '') return set({ from: undefined });
    const cur: MonthYear = fromM ?? { month: 1, year: years[years.length - 1] ?? new Date().getFullYear() };
    const next = { ...cur, [part]: Number(v) };
    set({ from: monthStart(next) });
  }
  function setTo(part: 'month' | 'year', v: string) {
    if (part === 'year' && v === '') return set({ to: undefined });
    const cur: MonthYear = toM ?? { month: 12, year: years[0] ?? new Date().getFullYear() };
    const next = { ...cur, [part]: Number(v) };
    set({ to: monthEnd(next) });
  }
  function onYear(y: number) {
    const r = tapYear(draft.from, draft.to, y);
    set({ from: r.from, to: r.to });
  }

  const chosen = $derived(new Set(draft.months ?? []));
  function toggleMonth(m: number) {
    const ms = chosen.has(m) ? [...chosen].filter((x) => x !== m) : [...chosen, m];
    const out = normalizeMonths(ms);
    set({ months: out, recur: out.length ? undefined : draft.recur });
  }
  const presets = recurPresets(new Date());
  const parsed = $derived(parseRecur(draft.recur));
  const today = presets[0]?.recur as Recur;
  const cur = $derived<Recur>(parsed ?? today);
  function setRecur(r: Recur | null) {
    set({ recur: r ? formatRecur(r) : undefined, months: r ? undefined : draft.months });
  }
  function setPart(which: 'from' | 'to', part: keyof MonthDay, v: number) {
    const md = { ...cur[which], [part]: v };
    const days = dayList(md.month).length;
    if (md.day > days) md.day = days;
    setRecur({ ...cur, [which]: md });
  }
  const hasDates = $derived(!!(draft.from || draft.to || draft.recur || draft.months?.length));
  const everyYear = $derived([describeMonths(draft.months ?? []), parsed ? describeRecur(parsed) : ''].filter(Boolean).join(', '));

  // ---- Rating, camera, location, album, tags, size ----

  // Camera: right under Show, so it is seen without scrolling the sheet.
  // The most used cameras first, a search over maker and model when there
  // are many, all of them on request, and "Unknown camera" for the photos
  // without one (videos often, scans, edited copies).
  const cameras = $derived(facets?.cameras ?? []);
  const CAMERAS_SHOWN = 5;
  let camQuery = $state('');
  let camAll = $state(false);
  const camMatches = $derived(searchCameras(cameras, camQuery));
  const camList = $derived.by(() => {
    if (camQuery || camAll) return camMatches;
    const top = cameras.slice(0, CAMERAS_SHOWN);
    const sel = cameras.find((c) => c.model === draft.camera);
    return sel && !top.includes(sel) ? [...top, sel] : top;
  });
  const noCamera = $derived(facets?.nocamera ?? 0);
  const showCameras = $derived(cameras.length > 0 || noCamera > 0 || !!draft.camera || !!draft.nocamera);
  function setCamera(model: string | null, unknown = false) {
    set({ camera: model ?? undefined, nocamera: unknown || undefined });
  }
  const showLocation = $derived(scope.location);
  const albums = $derived(scope.library && !albumImplied ? (facets?.albums ?? []) : []);
  const tags = $derived(scope.library ? (facets?.tags ?? []) : []);
  const tagName = (id: number) => tags.find((t) => t.id === id)?.name ?? `#${id}`;
  const draftTags = $derived(new Set(draft.tags ?? []));
  function toggleTag(id: number) {
    const next = draftTags.has(id) ? [...draftTags].filter((t) => t !== id) : [...draftTags, id];
    set({ tags: next.sort((a, b) => a - b).slice(0, MAX_TAGS) });
  }
  // Tags that are filtered on but no longer offered (no live photos) stay removable.
  const orphanTags = $derived((draft.tags ?? []).filter((id) => !tags.some((t) => t.id === id)));

  const sizes: { label: string; minPx?: number; maxPx?: number }[] = [
    { label: 'Any' },
    { label: 'Small ≤ 1600', maxPx: 1600 },
    { label: 'Full size ≥ 2000', minPx: 2000 },
    { label: 'Large ≥ 4000', minPx: 4000 },
  ];
  const sizeOn = (s: { minPx?: number; maxPx?: number }) => (s.minPx ?? 0) === (draft.minPx ?? 0) && (s.maxPx ?? 0) === (draft.maxPx ?? 0);
  const customSize = $derived(!sizes.some(sizeOn));

  function apply(e: SubmitEvent) {
    e.preventDefault();
    const f = cleanFilters(draft);
    if (!scope.rate) delete f.rating;
    if (!scope.location) delete f.gps;
    if (!scope.library) {
      delete f.album;
      delete f.tags;
    }
    if (albumImplied) delete f.album;
    onapply(f);
  }
  function clearAll() {
    draft = {};
  }
  const n = (v: number | null | undefined) => (typeof v === 'number' ? v.toLocaleString() : '');
</script>

<button class="scrim" aria-label="Close the filters" onclick={onclose}></button>
<form class="sheet" aria-label="Filters" onsubmit={apply}>
  <div class="head">
    <h2>Filter</h2>
    <button type="button" class="ghost close" aria-label="Close" onclick={onclose}>×</button>
  </div>
  {#if facetsError}
    <p class="muted small note" role="status">Could not read the choices: {facetsError}</p>
  {/if}

  <section aria-labelledby="fs-show">
    <h3 id="fs-show">Show</h3>
    <div class="seg">
      {#each types as t (t.value)}
        <button type="button" class:on={(draft.type ?? '') === t.value} aria-pressed={(draft.type ?? '') === t.value} onclick={() => set({ type: t.value || undefined })}
          >{t.label}{#if t.n !== null}<span class="count">{n(t.n)}</span>{/if}</button
        >
      {/each}
    </div>
  </section>

  {#if showCameras}
    <section aria-labelledby="fs-camera">
      <h3 id="fs-camera">Camera</h3>
      {#if cameras.length > CAMERAS_SHOWN}
        <input class="search" type="search" placeholder="Search {cameras.length} cameras" aria-label="Search cameras" autocomplete="off" bind:value={camQuery} />
      {/if}
      <div class="cams" class:all={camAll || !!camQuery} role="radiogroup" aria-labelledby="fs-camera">
        {#if !camQuery}
          <button type="button" role="radio" aria-checked={!draft.camera && !draft.nocamera} class:on={!draft.camera && !draft.nocamera} onclick={() => setCamera(null)}>
            <span class="name">Any camera</span>
          </button>
        {/if}
        {#each camList as c (c.make + '\u0000' + c.model)}
          <button type="button" role="radio" aria-checked={draft.camera === c.model} class:on={draft.camera === c.model} onclick={() => setCamera(c.model)}>
            <span class="name">{cameraLabel(c.make, c.model)}</span><span class="count">{n(c.count)}</span>
          </button>
        {/each}
        {#if draft.camera && !cameras.some((c) => c.model === draft.camera)}
          <button type="button" role="radio" aria-checked="true" class="on"><span class="name">{draft.camera}</span></button>
        {/if}
        {#if (noCamera > 0 || draft.nocamera) && (!camQuery || 'unknown camera'.includes(camQuery.trim().toLowerCase()))}
          <button type="button" role="radio" aria-checked={!!draft.nocamera} class:on={!!draft.nocamera} onclick={() => setCamera(null, true)}>
            <span class="name">{UNKNOWN_CAMERA}</span>{#if noCamera > 0}<span class="count">{n(noCamera)}</span>{/if}
          </button>
        {/if}
        {#if camQuery && camMatches.length === 0}
          <p class="hint">No camera matches “{camQuery}”.</p>
        {/if}
      </div>
      {#if !camQuery && !camAll && cameras.length > CAMERAS_SHOWN}
        <button type="button" class="ghost wide" onclick={() => (camAll = true)}>All {cameras.length} cameras</button>
      {/if}
    </section>
  {/if}

  <section aria-labelledby="fs-dates">
    <h3 id="fs-dates">Dates</h3>
    <div class="seg tabs" role="tablist">
      <button type="button" role="tab" aria-selected={dateTab === 'range'} class:on={dateTab === 'range'} onclick={() => (dateTab = 'range')}>Range</button>
      <button type="button" role="tab" aria-selected={dateTab === 'year'} class:on={dateTab === 'year'} onclick={() => (dateTab = 'year')}>Every year</button>
    </div>
    {#if dateTab === 'range'}
      {#if years.length}
        <p class="hint">Tap a year, or two for a range.</p>
        <div class="seg years" role="group" aria-label="Years">
          {#each years as y (y)}
            <button type="button" class:on={inSpan(y)} aria-pressed={inSpan(y)} onclick={() => onYear(y)}
              >{y}{#if yearCounts.has(y)}<span class="count">{n(yearCounts.get(y))}</span>{/if}</button
            >
          {/each}
        </div>
      {/if}
      <div class="rows">
        <div class="row">
          <span class="lbl">From</span>
          <select aria-label="From month" value={fromM ? String(fromM.month) : '1'} disabled={!fromM} onchange={(e) => setFrom('month', e.currentTarget.value)}>
            {#each MONTHS as m (m)}<option value={String(m)}>{monthName(m)}</option>{/each}
          </select>
          <select aria-label="From year" value={fromM ? String(fromM.year) : ''} onchange={(e) => setFrom('year', e.currentTarget.value)}>
            <option value="">Any</option>
            {#each years as y (y)}<option value={String(y)}>{y}</option>{/each}
          </select>
        </div>
        <div class="row">
          <span class="lbl">To</span>
          <select aria-label="To month" value={toM ? String(toM.month) : '12'} disabled={!toM} onchange={(e) => setTo('month', e.currentTarget.value)}>
            {#each MONTHS as m (m)}<option value={String(m)}>{monthName(m)}</option>{/each}
          </select>
          <select aria-label="To year" value={toM ? String(toM.year) : ''} onchange={(e) => setTo('year', e.currentTarget.value)}>
            <option value="">Any</option>
            {#each years as y (y)}<option value={String(y)}>{y}</option>{/each}
          </select>
        </div>
      </div>
    {:else}
      <div class="mgrid" role="group" aria-label="Months every year">
        {#each MONTHS as m (m)}
          <button type="button" class:on={chosen.has(m)} aria-pressed={chosen.has(m)} aria-label={monthName(m)} onclick={() => toggleMonth(m)}>{monthName(m, 'short')}</button>
        {/each}
      </div>
      <p class="hint">Or days every year</p>
      <div class="seg">
        {#each presets as p (p.id)}
          <button type="button" class:on={draft.recur === formatRecur(p.recur)} aria-pressed={draft.recur === formatRecur(p.recur)} onclick={() => setRecur(p.recur)}>{p.label}</button>
        {/each}
      </div>
      <div class="rows">
        {#each ENDS as which (which)}
          <div class="row">
            <span class="lbl">{which === 'from' ? 'From' : 'To'}</span>
            <select aria-label="{which === 'from' ? 'From' : 'To'} day" value={String(cur[which].day)} onchange={(e) => setPart(which, 'day', Number(e.currentTarget.value))}>
              {#each dayList(cur[which].month) as d (d)}<option value={String(d)}>{d}</option>{/each}
            </select>
            <select aria-label="{which === 'from' ? 'From' : 'To'} day's month" value={String(cur[which].month)} onchange={(e) => setPart(which, 'month', Number(e.currentTarget.value))}>
              {#each MONTHS as m (m)}<option value={String(m)}>{monthName(m)}</option>{/each}
            </select>
          </div>
        {/each}
      </div>
      {#if everyYear}<p class="hint">{everyYear}</p>{/if}
    {/if}
    {#if hasDates}
      <button type="button" class="ghost wide" onclick={() => set({ from: undefined, to: undefined, recur: undefined, months: undefined })}>Any time</button>
    {/if}
  </section>

  {#if scope.rate}
    <section aria-labelledby="fs-rating">
      <h3 id="fs-rating">Rating at least</h3>
      <div class="seg">
        {#each [0, 1, 2, 3, 4, 5] as r (r)}
          <button type="button" class:on={(draft.rating ?? 0) === r} aria-pressed={(draft.rating ?? 0) === r} aria-label={r === 0 ? 'Any rating' : ratingLabel(r)} onclick={() => set({ rating: r || undefined })}
            >{r === 0 ? 'Any' : `${r}★`}</button
          >
        {/each}
      </div>
    </section>
  {/if}

  {#if showLocation}
    <section aria-labelledby="fs-location">
      <h3 id="fs-location">Location</h3>
      <div class="seg">
        <button type="button" class:on={!draft.gps} aria-pressed={!draft.gps} onclick={() => set({ gps: undefined })}>Any</button>
        <button type="button" class:on={!!draft.gps} aria-pressed={!!draft.gps} disabled={!draft.gps && facets?.gps === 0} onclick={() => set({ gps: true })}
          >Has location{#if facets && facets.gps !== null}<span class="count">{n(facets.gps)}</span>{/if}</button
        >
      </div>
    </section>
  {/if}

  {#if albums.length || (draft.album && !albumImplied && scope.library)}
    <section aria-labelledby="fs-album">
      <h3 id="fs-album">Album</h3>
      <select class="full" aria-label="Album" value={String(draft.album ?? 0)} onchange={(e) => set({ album: Number(e.currentTarget.value) || undefined })}>
        <option value="0">Any album</option>
        {#each albums as a (a.id)}
          <option value={String(a.id)}>{a.name} ({n(a.count)})</option>
        {/each}
        {#if draft.album && !albums.some((a) => a.id === draft.album)}
          <option value={String(draft.album)}>Album #{draft.album}</option>
        {/if}
      </select>
    </section>
  {/if}

  {#if tags.length || orphanTags.length}
    <section aria-labelledby="fs-tags">
      <h3 id="fs-tags">Tags</h3>
      <p class="hint">Photos with every tag you pick.</p>
      <div class="seg tags" role="group" aria-label="Tags">
        {#each tags as t (t.id)}
          <button type="button" class:on={draftTags.has(t.id)} aria-pressed={draftTags.has(t.id)} onclick={() => toggleTag(t.id)}>{t.name}<span class="count">{n(t.count)}</span></button>
        {/each}
        {#each orphanTags as id (id)}
          <button type="button" class="on" aria-pressed="true" onclick={() => toggleTag(id)}>{tagName(id)}</button>
        {/each}
      </div>
    </section>
  {/if}

  <section aria-labelledby="fs-size">
    <h3 id="fs-size">Size</h3>
    <div class="seg">
      {#each sizes as s (s.label)}
        <button type="button" class:on={sizeOn(s)} aria-pressed={sizeOn(s)} onclick={() => set({ minPx: s.minPx, maxPx: s.maxPx })}>{s.label}</button>
      {/each}
      {#if customSize}
        <button type="button" class="on" aria-pressed="true">{describeSize(draft.minPx, draft.maxPx)}</button>
      {/if}
    </div>
  </section>

  <div class="foot">
    <button type="button" onclick={clearAll}>Clear all</button>
    <button type="submit" class="primary">Apply</button>
  </div>
</form>

<style>
  .scrim {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    z-index: 900;
    border: 0;
    border-radius: 0;
    background: rgba(0, 0, 0, 0.4);
  }
  .scrim:hover:not(:disabled) {
    background: rgba(0, 0, 0, 0.4);
  }
  .sheet {
    position: fixed;
    left: 0;
    right: 0;
    bottom: 0;
    z-index: 901;
    display: flex;
    flex-direction: column;
    max-height: 88vh;
    max-height: calc(var(--app-h, 100vh) * 0.88);
    overflow-y: auto;
    -webkit-overflow-scrolling: touch;
    overscroll-behavior: contain;
    padding: 0 16px;
    border-radius: 12px 12px 0 0;
    background: var(--surface);
    box-shadow: var(--shadow);
  }
  .head {
    position: -webkit-sticky;
    position: sticky;
    top: 0;
    z-index: 1;
    display: flex;
    align-items: center;
    justify-content: space-between;
    min-height: 52px;
    background: var(--surface);
  }
  h2 {
    margin: 0;
    font-size: 17px;
  }
  .close {
    min-width: 44px;
    min-height: 44px;
    padding: 0;
    font-size: 24px;
  }
  section {
    padding: 10px 0 12px;
    border-top: 1px solid var(--border);
  }
  h3 {
    margin: 0 0 8px;
    color: var(--muted);
    font-size: 13px;
    font-weight: 600;
  }
  .hint {
    margin: 8px 0 6px;
    color: var(--muted);
    font-size: 13px;
  }
  .note {
    margin: 0 0 8px;
  }
  .seg {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .seg button {
    min-height: 44px;
    min-width: 44px;
    padding: 0 12px;
  }
  .seg button.on {
    background: var(--accent-strong);
    border-color: var(--accent-strong);
    color: #fff;
  }
  .seg.tabs {
    margin-bottom: 8px;
  }
  .seg.tabs button {
    flex: 1;
  }
  .seg.tags {
    max-height: 220px;
    overflow-y: auto;
  }
  .count {
    margin-left: 6px;
    font-size: 12px;
    opacity: 0.75;
  }
  .mgrid {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 6px;
  }
  .mgrid button {
    min-height: 44px;
    padding: 0;
  }
  .mgrid button.on {
    background: var(--accent-strong);
    border-color: var(--accent-strong);
    color: #fff;
  }
  .rows {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin-top: 10px;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .lbl {
    flex: none;
    width: 3em;
    color: var(--muted);
  }
  .row select {
    flex: 1;
    min-width: 0;
    min-height: 44px;
    font-size: 16px;
  }
  select.full {
    width: 100%;
    min-height: 44px;
    font-size: 16px;
  }
  .search {
    width: 100%;
    box-sizing: border-box;
    min-height: 44px;
    margin-bottom: 6px;
    font-size: 16px;
  }
  .cams {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .cams.all {
    max-height: 264px;
    overflow-y: auto;
    -webkit-overflow-scrolling: touch;
    overscroll-behavior: contain;
  }
  .cams button {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    min-height: 44px;
    padding: 0 12px;
    text-align: left;
  }
  .cams button.on {
    background: var(--accent-strong);
    border-color: var(--accent-strong);
    color: #fff;
  }
  .cams .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .cams .count {
    flex: none;
  }
  .wide {
    width: 100%;
    margin-top: 8px;
    min-height: 44px;
  }
  .foot {
    position: -webkit-sticky;
    position: sticky;
    bottom: 0;
    display: flex;
    gap: 8px;
    margin: 0 -16px;
    padding: 10px 16px calc(10px + env(safe-area-inset-bottom, 0px));
    border-top: 1px solid var(--border);
    background: var(--surface);
  }
  .foot button {
    flex: 1;
    min-height: 48px;
  }
  @media (min-width: 700px) {
    .sheet {
      left: 50%;
      right: auto;
      width: 520px;
      margin-left: -260px;
    }
  }
</style>
