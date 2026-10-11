<script lang="ts" module>
  export interface DateValue {
    from?: string;
    to?: string;
    recur?: string;
  }
</script>

<script lang="ts">
  // The Dates filter (docs/design/management.md "Filters"): a button with a
  // summary that opens a panel with two modes. Range: the calendar picker
  // (the last day is inclusive here, exclusive in the URL), either side open.
  // Every year: month/day from and to (wrapping over New Year is fine),
  // presets, and whole-year limits. Every change applies at once.
  import {
    daysIn,
    describeDates,
    exclusiveEnd,
    formatRecur,
    fromYear,
    inclusiveEnd,
    parseRecur,
    recurPresets,
    toYear,
    withYear,
    type MonthDay,
    type Recur,
  } from '../lib/dates';
  import { formatDay, monthName } from '../lib/format';
  import Calendar from './Calendar.svelte';

  interface Props {
    from?: string;
    to?: string;
    recur?: string;
    /** Years offered by the year limits, newest first. */
    years: readonly number[];
    onchange: (v: DateValue) => void;
  }
  let { from, to, recur, years, onchange }: Props = $props();

  let open = $state(false);
  let tab = $state<'range' | 'year'>('range');
  let wrap: HTMLSpanElement | undefined = $state();
  const uid = `df-${Math.random().toString(36).slice(2, 9)}`;

  const summary = $derived(describeDates(from, to, recur));
  const parsed = $derived(parseRecur(recur));
  const presets = recurPresets(new Date());

  // Every-year selects: the current recur, else today's date until changed.
  const today = presets[0]?.recur as Recur;
  const cur = $derived<Recur>(parsed ?? today);
  const ends = ['from', 'to'] as const;
  const months = Array.from({ length: 12 }, (_, i) => i + 1);
  const dayList = (m: number) => Array.from({ length: daysIn(m) }, (_, i) => i + 1);

  function toggle() {
    if (!open) tab = parsed ? 'year' : 'range';
    open = !open;
  }

  $effect(() => {
    if (!open) return;
    const away = (e: PointerEvent) => {
      if (wrap && e.target instanceof Node && !wrap.contains(e.target)) open = false;
    };
    window.addEventListener('pointerdown', away);
    return () => window.removeEventListener('pointerdown', away);
  });

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && open) {
      e.preventDefault();
      e.stopPropagation();
      open = false;
    }
  }

  function setRange(f: string | undefined, lastDay: string | undefined) {
    onchange({ from: f || undefined, to: lastDay ? exclusiveEnd(lastDay) : undefined, recur: undefined });
  }

  function setRecur(r: Recur) {
    onchange({ from, to, recur: formatRecur(r) });
  }

  function setPart(which: 'from' | 'to', part: keyof MonthDay, v: number) {
    const md = { ...cur[which], [part]: v };
    if (md.day > daysIn(md.month)) md.day = daysIn(md.month);
    setRecur({ ...cur, [which]: md });
  }

  /** One year limit changed; the other bound stays (a day bound too). "day" is the shown day bound itself. */
  function setYear(which: 'from' | 'to', v: string) {
    if (v === 'day') return;
    const r = withYear(from, to, which, v ? Number(v) : null);
    onchange({ from: r.from, to: r.to, recur });
  }

  const yFrom = $derived(fromYear(from));
  const yTo = $derived(toYear(to));
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<span class="datefilter" bind:this={wrap} onkeydown={onKeydown}>
  <button type="button" class:set={!!summary} aria-expanded={open} aria-controls={uid} onclick={toggle}>
    Dates{#if summary}: <span class="sum">{summary}</span>{/if}
  </button>
  {#if open}
    <div class="panel" class:wide={tab === 'range'} id={uid} role="group" aria-label="Date filter">
      <div class="tabs" role="tablist">
        <button type="button" role="tab" aria-selected={tab === 'range'} class:on={tab === 'range'} onclick={() => (tab = 'range')}
          >Range</button
        >
        <button type="button" role="tab" aria-selected={tab === 'year'} class:on={tab === 'year'} onclick={() => (tab = 'year')}
          >Every year</button
        >
      </div>

      {#if tab === 'range'}
        <Calendar from={from} to={inclusiveEnd(to) || undefined} {years} onchange={setRange} />
        <p class="muted small note">Either side can stay empty. Both days are included.</p>
      {:else}
        <div class="presets">
          {#each presets as p (p.id)}
            <button
              type="button"
              class="small"
              class:on={!!recur && recur === formatRecur(p.recur)}
              onclick={() => setRecur(p.recur)}>{p.label}</button
            >
          {/each}
        </div>
        <div class="rows">
          {#each ends as which (which)}
            <div class="md">
              <span>{which === 'from' ? 'From' : 'To'}</span>
              <select aria-label="{which === 'from' ? 'From' : 'To'} day" value={String(cur[which].day)} onchange={(e) => setPart(which, 'day', Number(e.currentTarget.value))}>
                {#each dayList(cur[which].month) as d (d)}<option value={String(d)}>{d}</option>{/each}
              </select>
              <select aria-label="{which === 'from' ? 'From' : 'To'} month" value={String(cur[which].month)} onchange={(e) => setPart(which, 'month', Number(e.currentTarget.value))}>
                {#each months as m (m)}<option value={String(m)}>{monthName(m)}</option>{/each}
              </select>
            </div>
          {/each}
          <div class="md">
            <span>Years</span>
            <select aria-label="From year" value={yFrom !== null ? String(yFrom) : from ? 'day' : ''} onchange={(e) => setYear('from', e.currentTarget.value)}>
              <option value="">Any</option>
              {#if from && yFrom === null}<option value="day">{formatDay(from)}</option>{/if}
              {#each years as y (y)}<option value={String(y)}>{y}</option>{/each}
            </select>
            <span class="muted">–</span>
            <select aria-label="To year" value={yTo !== null ? String(yTo) : to ? 'day' : ''} onchange={(e) => setYear('to', e.currentTarget.value)}>
              <option value="">Any</option>
              {#if to && yTo === null}<option value="day">{formatDay(inclusiveEnd(to) || to)}</option>{/if}
              {#each years as y (y)}<option value={String(y)}>{y}</option>{/each}
            </select>
          </div>
          {#if !parsed}
            <p class="muted small">Pick a preset or a day to filter every year.</p>
          {/if}
        </div>
      {/if}

      {#if from || to || recur}
        <div class="foot">
          <button type="button" class="ghost small" onclick={() => onchange({})}>Clear dates</button>
        </div>
      {/if}
    </div>
  {/if}
</span>

<style>
  .datefilter {
    position: relative;
    display: inline-block;
  }
  button.set {
    border-color: var(--accent-strong);
  }
  .sum {
    max-width: 260px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .panel {
    position: absolute;
    top: calc(100% + 6px);
    left: 0;
    z-index: 40;
    width: 340px;
    max-width: 92vw;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    box-shadow: var(--shadow);
  }
  .tabs {
    display: flex;
    gap: 4px;
    margin-bottom: 10px;
  }
  .tabs .on,
  .presets .on {
    background: var(--surface-3);
    border-color: var(--accent-strong);
  }
  .presets {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    margin-bottom: 10px;
  }
  .rows {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .md {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .md > span:first-child {
    width: 3.2em;
    color: var(--muted);
  }
  .panel.wide {
    width: max-content;
    max-width: calc(100vw - 16px);
  }
  .note {
    margin: 8px 0 0;
  }
  .rows p {
    margin: 0;
  }
  .foot {
    margin-top: 10px;
    display: flex;
    justify-content: flex-end;
  }
</style>
