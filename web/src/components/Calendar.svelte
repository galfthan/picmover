<script lang="ts">
  // A calendar range picker (docs/design/management.md "Dates and the
  // calendar picker (web)"). Monday-first month grids, two side by side from
  // 640 px, one below; month and year selects so decades are one step away.
  // First click picks the start, the second the end (an earlier day starts
  // over); hovering previews the end. Typed dd/mm/yyyy days apply on Enter
  // or blur. Every pick calls `onchange` with inclusive days at once.
  import { tick } from 'svelte';
  import { addDays } from '../lib/dates';
  import { calendarMonths, localToday, rangePresets, shiftDayByMonths, shiftMonth, weekdayOf, yearOptions, type DayCell } from '../lib/calendar';
  import { formatDay, monthName, parseDay } from '../lib/format';

  interface Props {
    /** Inclusive first day "YYYY-MM-DD" (absent: open). */
    from?: string;
    /** Inclusive last day "YYYY-MM-DD" (absent: open). */
    to?: string;
    /** The library's years (any order); the year select spans them. */
    years: readonly number[];
    onchange: (from: string | undefined, to: string | undefined) => void;
    min?: string;
    max?: string;
  }
  let { from, to, years, onchange, min, max }: Props = $props();

  const WEEKDAYS = ['Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday'];
  const MONTHS = Array.from({ length: 12 }, (_, i) => i + 1);
  const uid = `cal-${Math.random().toString(36).slice(2, 9)}`;

  const today = localToday();
  const presets = rangePresets(today);

  const ym = (day: string) => ({ year: Number(day.slice(0, 4)), month: Number(day.slice(5, 7)) });
  const index = (v: { year: number; month: number }) => v.year * 12 + v.month - 1;

  // Two months from 640 px of viewport width.
  let wide = $state(false);
  $effect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return;
    const mq = window.matchMedia('(min-width: 640px)');
    wide = mq.matches;
    const update = () => (wide = mq.matches);
    mq.addEventListener('change', update);
    return () => mq.removeEventListener('change', update);
  });
  const count = $derived(wide ? 2 : 1);

  // The first shown month; starts at the selection (or today).
  // svelte-ignore state_referenced_locally
  let view = $state(ym(from ?? to ?? today));
  const shown = $derived(Array.from({ length: count }, (_, i) => shiftMonth(view.year, view.month, i)));
  const grids = $derived(shown.map((m) => ({ ...m, rows: calendarMonths(m.year, m.month) })));
  const yearList = $derived(yearOptions(years, view.year));

  function isShown(day: string): boolean {
    const i = index(ym(day)) - index(view);
    return i >= 0 && i < count;
  }

  /** Scrolls the view the least so that `day`'s month is shown. */
  function reveal(day: string, at: 'start' | 'end' = 'start') {
    if (isShown(day)) return;
    const m = ym(day);
    view = at === 'end' ? shiftMonth(m.year, m.month, -(count - 1)) : m;
  }

  // ---- Selection ----

  // A start picked by a first click, waiting for the end. It only counts
  // while the value is still the single day it set; any other change (a
  // preset, typed text, Clear) ends it.
  let anchor = $state<string | null>(null);
  const pending = $derived(anchor !== null && from === anchor && to === anchor ? anchor : null);
  let hover = $state<string | null>(null);

  const lo = $derived(from);
  const hi = $derived(pending && hover && hover >= pending ? hover : to);
  const previewing = $derived(!!pending && !!hover && hover > pending);

  const outOfBounds = (day: string) => (!!min && day < min) || (!!max && day > max);

  function pick(day: string) {
    if (outOfBounds(day)) return;
    focusDay = day;
    const start = pending;
    if (start && day >= start) {
      anchor = null;
      hover = null;
      onchange(start, day);
    } else {
      anchor = day;
      onchange(day, day);
    }
  }

  function applyPreset(f: string, t: string) {
    anchor = null;
    onchange(f, t);
    focusDay = t;
    reveal(t, 'end');
  }

  // ---- Focus and keys (roving tabindex) ----

  // svelte-ignore state_referenced_locally
  let focusDay = $state(from ?? to ?? today);
  /** The one day that takes Tab: the focused day when shown, else the 1st of the first month. */
  const tabDay = $derived(isShown(focusDay) ? focusDay : `${String(view.year).padStart(4, '0')}-${String(view.month).padStart(2, '0')}-01`);
  let root: HTMLDivElement | undefined = $state();

  async function moveFocus(day: string) {
    focusDay = day;
    if (pending) hover = day;
    if (!isShown(day)) reveal(day, index(ym(day)) < index(view) ? 'start' : 'end');
    await tick();
    const cells = root?.querySelectorAll<HTMLElement>(`[data-day="${day}"]`) ?? [];
    ([...cells].find((c) => c.dataset.in === '1') ?? cells[0])?.focus();
  }

  function onCellKey(e: KeyboardEvent, cell: DayCell) {
    const d = cell.day;
    let next: string | null = null;
    switch (e.key) {
      case 'ArrowLeft':
        next = addDays(d, -1);
        break;
      case 'ArrowRight':
        next = addDays(d, 1);
        break;
      case 'ArrowUp':
        next = addDays(d, -7);
        break;
      case 'ArrowDown':
        next = addDays(d, 7);
        break;
      case 'PageUp':
        next = shiftDayByMonths(d, e.shiftKey ? -12 : -1);
        break;
      case 'PageDown':
        next = shiftDayByMonths(d, e.shiftKey ? 12 : 1);
        break;
      case 'Home':
        next = addDays(d, -weekdayOf(d));
        break;
      case 'End':
        next = addDays(d, 6 - weekdayOf(d));
        break;
      case 'Enter':
      case ' ':
        e.preventDefault();
        pick(d);
        return;
      default:
        return;
    }
    e.preventDefault();
    void moveFocus(next);
  }

  function step(n: number) {
    view = shiftMonth(view.year, view.month, n);
  }

  // ---- Typed days ----

  // The text follows the value until edited; a bad entry stays shown, marked.
  let fromText = $derived(formatDay(from));
  let toText = $derived(formatDay(to));
  let fromBad = $derived.by(() => (void from, false));
  let toBad = $derived.by(() => (void to, false));

  function commit(which: 'from' | 'to') {
    const text = (which === 'from' ? fromText : toText).trim();
    const current = which === 'from' ? from : to;
    if (text === '') {
      if (current === undefined) return;
      anchor = null;
      if (which === 'from') onchange(undefined, to);
      else onchange(from, undefined);
      return;
    }
    const day = parseDay(text);
    const other = which === 'from' ? to : from;
    const crossed = !!day && !!other && (which === 'from' ? day > other : day < other);
    if (!day || outOfBounds(day) || crossed) {
      if (which === 'from') fromBad = true;
      else toBad = true;
      return;
    }
    if (which === 'from') {
      fromBad = false;
      fromText = formatDay(day);
    } else {
      toBad = false;
      toText = formatDay(day);
    }
    if (day === current) return;
    anchor = null;
    focusDay = day;
    reveal(day, which === 'from' ? 'start' : 'end');
    if (which === 'from') onchange(day, to);
    else onchange(from, day);
  }

  function onTextKey(e: KeyboardEvent, which: 'from' | 'to') {
    if (e.key === 'Enter') {
      e.preventDefault();
      commit(which);
    }
  }

  // ---- Text ----

  const fullDate = (c: DayCell) => `${WEEKDAYS[c.weekday]} ${c.date} ${monthName(c.month)} ${c.year}`;
  const activePreset = $derived(presets.find((p) => p.from === from && p.to === to)?.id ?? null);
  const status = $derived(
    pending
      ? `Start ${formatDay(pending)}. Choose the end day.`
      : from && to
        ? `Selected ${formatDay(from)} to ${formatDay(to)}`
        : from
          ? `Selected from ${formatDay(from)}`
          : to
            ? `Selected up to ${formatDay(to)}`
            : '',
  );
</script>

<div class="calendar" bind:this={root}>
  <div class="typed">
    <label>
      <span>Start</span>
      <input
        type="text"
        inputmode="numeric"
        placeholder="dd/mm/yyyy"
        autocomplete="off"
        aria-label="Start day"
        aria-invalid={fromBad}
        class:bad={fromBad}
        bind:value={fromText}
        oninput={() => (fromBad = false)}
        onkeydown={(e) => onTextKey(e, 'from')}
        onblur={() => commit('from')}
      />
    </label>
    <span class="dash" aria-hidden="true">–</span>
    <label>
      <span>End</span>
      <input
        type="text"
        inputmode="numeric"
        placeholder="dd/mm/yyyy"
        autocomplete="off"
        aria-label="End day"
        aria-invalid={toBad}
        class:bad={toBad}
        bind:value={toText}
        oninput={() => (toBad = false)}
        onkeydown={(e) => onTextKey(e, 'to')}
        onblur={() => commit('to')}
      />
    </label>
  </div>
  {#if fromBad || toBad}
    <p class="hint bad-text small">Type a day as dd/mm/yyyy; the start cannot come after the end.</p>
  {/if}

  <div class="presets" role="group" aria-label="Quick ranges">
    {#each presets as p (p.id)}
      <button type="button" class="small" class:on={activePreset === p.id} aria-pressed={activePreset === p.id} onclick={() => applyPreset(p.from, p.to)}
        >{p.label}</button
      >
    {/each}
  </div>

  <div class="months" class:two={count === 2}>
    {#each grids as g, gi (`${g.year}-${g.month}`)}
      <div class="month">
        <div class="head">
          {#if gi === 0}
            <button type="button" class="ghost nav" aria-label="Previous month" onclick={() => step(-1)}>
              <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="M10 3 5 8l5 5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
            </button>
            <select aria-label="Month" value={String(view.month)} onchange={(e) => (view = { year: view.year, month: Number(e.currentTarget.value) })}>
              {#each MONTHS as m (m)}<option value={String(m)}>{monthName(m)}</option>{/each}
            </select>
            <select aria-label="Year" class="year" value={String(view.year)} onchange={(e) => (view = { year: Number(e.currentTarget.value), month: view.month })}>
              {#each yearList as y (y)}<option value={String(y)}>{y}</option>{/each}
            </select>
          {:else}
            <span class="caption">{monthName(g.month)} {g.year}</span>
          {/if}
          {#if gi === grids.length - 1}
            <button type="button" class="ghost nav next" aria-label="Next month" onclick={() => step(1)}>
              <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="m6 3 5 5-5 5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
            </button>
          {/if}
        </div>

        <div class="grid" role="grid" aria-label="{monthName(g.month)} {g.year}">
          <div class="week" role="row">
            {#each WEEKDAYS as w (w)}
              <span class="wd" role="columnheader" aria-label={w}><abbr title={w}>{w[0]}</abbr></span>
            {/each}
          </div>
          {#each g.rows as row, ri (ri)}
            <div class="week" role="row">
              {#each row as c (c.day)}
                {@const isStart = !!lo && c.day === lo}
                {@const isEnd = !!hi && c.day === hi}
                {@const inside = !!lo && !!hi && c.day > lo && c.day < hi}
                {@const selected = (!!from && !!to && c.day >= from && c.day <= to) || c.day === from || c.day === to}
                {@const off = outOfBounds(c.day)}
                {#if !c.inMonth && isShown(c.day)}
                  <!-- Shown in the other month: left blank here so a day appears once. -->
                  <div class="cell blank" role="gridcell" aria-hidden="true"></div>
                {:else}
                  <div
                    class="cell"
                    class:out={!c.inMonth}
                    class:start={isStart}
                    class:end={isEnd}
                    class:span={isStart && !!hi && hi > c.day}
                    class:spanend={isEnd && !!lo && lo < c.day}
                    class:inside
                    class:preview={previewing && (inside || isEnd)}
                    class:today={c.day === today}
                    class:off
                    role="gridcell"
                    tabindex={c.inMonth && c.day === tabDay ? 0 : -1}
                    aria-selected={selected}
                    aria-disabled={off || undefined}
                    aria-current={c.day === today ? 'date' : undefined}
                    aria-label={fullDate(c)}
                    data-day={c.day}
                    data-in={c.inMonth ? '1' : '0'}
                    onclick={() => pick(c.day)}
                    onkeydown={(e) => onCellKey(e, c)}
                    onfocus={() => (focusDay = c.day)}
                    onmouseenter={() => (hover = c.day)}
                    onmouseleave={() => (hover = null)}
                  >
                    <span class="num">{c.date}</span>
                  </div>
                {/if}
              {/each}
            </div>
          {/each}
        </div>
      </div>
    {/each}
  </div>

  <p class="sr-only" aria-live="polite" id="{uid}-status">{status}</p>
</div>

<style>
  .calendar {
    --cell: 36px;
    --band: color-mix(in srgb, var(--accent) 20%, transparent);
    --band-preview: color-mix(in srgb, var(--accent) 11%, transparent);
    display: flex;
    flex-direction: column;
    gap: 10px;
    font-variant-numeric: tabular-nums;
  }

  /* Typed days */
  .typed {
    display: flex;
    align-items: flex-end;
    gap: 8px;
  }
  .typed label {
    display: flex;
    flex-direction: column;
    gap: 3px;
    flex: 1;
    min-width: 0;
  }
  .typed label > span {
    color: var(--muted);
    font-size: 0.88em;
  }
  .typed input {
    width: 100%;
    font-variant-numeric: tabular-nums;
    letter-spacing: 0.02em;
  }
  .typed input.bad {
    border-color: var(--danger);
    background: var(--danger-bg);
  }
  .dash {
    color: var(--faint);
    padding-bottom: 6px;
  }
  .hint {
    margin: -4px 0 0;
  }
  .bad-text {
    color: var(--danger);
  }

  /* Presets */
  .presets {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .presets .on {
    background: var(--surface-3);
    border-color: var(--accent-strong);
  }

  /* Months */
  .months {
    display: flex;
    gap: 20px;
  }
  .month {
    width: calc(var(--cell) * 7);
  }
  .head {
    display: flex;
    align-items: center;
    gap: 4px;
    height: 34px;
    margin-bottom: 4px;
  }
  .head select {
    min-height: 30px;
    padding: 0 4px;
    border-color: transparent;
    background: transparent;
    font-weight: 600;
    cursor: pointer;
  }
  .head select:hover {
    background: var(--surface-2);
    border-color: var(--border);
  }
  .head select.year {
    font-weight: 400;
    color: var(--muted);
  }
  .caption {
    flex: 1;
    text-align: center;
    font-weight: 600;
  }
  .two .caption {
    padding-left: 36px;
  }
  .nav {
    width: 30px;
    min-height: 30px;
    padding: 0;
    color: var(--muted);
    flex: none;
  }
  .nav:hover {
    color: var(--text);
  }
  .nav.next {
    margin-left: auto;
  }

  /* Grid */
  .week {
    display: grid;
    grid-template-columns: repeat(7, var(--cell));
  }
  .wd {
    height: 24px;
    display: grid;
    place-items: center;
    color: var(--faint);
    font-size: 0.8em;
  }
  .wd abbr {
    text-decoration: none;
  }
  .cell {
    position: relative;
    height: var(--cell);
    display: grid;
    place-items: center;
    cursor: pointer;
    outline: none;
    user-select: none;
  }
  .num {
    position: relative;
    z-index: 1;
    width: 32px;
    height: 32px;
    display: grid;
    place-items: center;
    border-radius: 50%;
    color: var(--text);
    transition: background-color 80ms ease;
  }
  .cell:hover .num {
    background: var(--surface-3);
  }
  .cell.out .num {
    color: var(--faint);
  }
  .cell.off,
  .cell.blank {
    cursor: default;
  }
  .cell.off .num {
    color: var(--faint);
    opacity: 0.5;
    text-decoration: line-through;
  }
  .cell.off:hover .num {
    background: none;
  }
  .cell.today .num {
    box-shadow: inset 0 0 0 1.5px var(--muted);
  }
  .cell:focus-visible .num {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }

  /* The range: one continuous band behind the days, the ends as solid discs. */
  .cell.inside::before,
  .cell.span::before,
  .cell.spanend::before {
    content: '';
    position: absolute;
    top: 2px;
    bottom: 2px;
    left: 0;
    right: 0;
    background: var(--band);
  }
  .cell.span::before {
    left: 50%;
  }
  .cell.spanend::before {
    right: 50%;
  }
  .cell.preview::before {
    background: var(--band-preview);
  }
  .cell.inside:first-child::before {
    border-radius: 16px 0 0 16px;
  }
  .cell.inside:last-child::before {
    border-radius: 0 16px 16px 0;
  }
  .cell.inside.out .num {
    color: var(--muted);
  }
  .cell.start .num,
  .cell.end:not(.preview) .num {
    background: var(--accent);
    color: var(--accent-text);
    font-weight: 600;
  }
  .cell.end.preview .num {
    box-shadow: inset 0 0 0 1.5px var(--accent);
  }
  .cell.start.out .num,
  .cell.end.out .num {
    opacity: 0.7;
  }

  @media (prefers-reduced-motion: reduce) {
    .num {
      transition: none;
    }
  }
</style>
