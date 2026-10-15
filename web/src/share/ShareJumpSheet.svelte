<script lang="ts">
  // "Jump to date" (docs/design/lan-share.md "Timeline scrolling"): the
  // accessible way through a long timeline, beside the scrubber. A bottom
  // sheet: pick a year (with its number of photos), then a month of it;
  // months without photos are disabled. Built from the grid's timeline,
  // so it offers only months that are there. Buttons only (44 px), no
  // hover, Escape and the scrim close it.
  import { monthName, formatCount } from '../lib/format';
  import type { TimelineBucket } from '../lib/api';

  interface Props {
    buckets: readonly TimelineBucket[];
    /** The month at the top of the grid (the starting year). */
    current: { year: number; month: number } | null;
    onpick: (year: number, month: number) => void;
    onclose: () => void;
  }
  let { buckets, current, onpick, onclose }: Props = $props();

  /** Years newest first, with their counts; the undated bucket (0) last. */
  const years = $derived.by(() => {
    const m = new Map<number, number>();
    for (const b of buckets) m.set(b.year, (m.get(b.year) ?? 0) + b.count);
    return [...m].map(([year, count]) => ({ year, count })).sort((a, b) => (a.year === 0 ? 1 : b.year === 0 ? -1 : b.year - a.year));
  });
  /** The year whose months are shown; null: the years (the current one marked). */
  let year = $state<number | null>(null);
  const months = $derived.by(() => {
    const counts = new Map<number, number>();
    for (const b of buckets) if (b.year === year) counts.set(b.month, b.count);
    return Array.from({ length: 12 }, (_, i) => ({ month: i + 1, count: counts.get(i + 1) ?? 0 }));
  });

  function pickYear(y: number) {
    if (y === 0) return onpick(0, 0);
    year = y;
    void focusFirst();
  }

  let sheet: HTMLDivElement | undefined = $state();
  async function focusFirst() {
    await Promise.resolve();
    sheet?.querySelector<HTMLButtonElement>('.grid button:not(:disabled)')?.focus();
  }
  $effect(() => {
    const before = document.activeElement as HTMLElement | null;
    void focusFirst();
    return () => before?.focus?.();
  });

  function key(e: KeyboardEvent) {
    if (e.key !== 'Escape') return;
    e.preventDefault();
    e.stopPropagation();
    if (year !== null) year = null;
    else onclose();
  }
</script>

<button class="scrim" aria-label="Close" tabindex="-1" onclick={onclose}></button>
<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div class="sheet" role="dialog" aria-modal="true" aria-labelledby="jump-title" tabindex="-1" bind:this={sheet} onkeydown={key}>
  <div class="head">
    {#if year !== null}
      <button type="button" class="ghost back" aria-label="Back to the years" onclick={() => ((year = null), void focusFirst())}>‹</button>
      <h2 id="jump-title">{year}</h2>
    {:else}
      <h2 id="jump-title">Jump to a date</h2>
    {/if}
    <button type="button" class="ghost close" aria-label="Close" onclick={onclose}>×</button>
  </div>
  {#if year === null}
    <div class="grid years" role="group" aria-label="Years">
      {#each years as y (y.year)}
        <button type="button" class:on={current?.year === y.year} aria-current={current?.year === y.year ? 'true' : undefined} onclick={() => pickYear(y.year)}>
          <span class="big">{y.year === 0 ? 'Undated' : y.year}</span>
          <span class="count">{formatCount(y.count)}</span>
        </button>
      {/each}
    </div>
  {:else}
    <div class="grid months" role="group" aria-label="Months of {year}">
      {#each months as m (m.month)}
        <button
          type="button"
          disabled={m.count === 0}
          class:on={current?.year === year && current?.month === m.month}
          aria-label="{monthName(m.month)} {year}{m.count ? `, ${formatCount(m.count)}` : ', no photos'}"
          onclick={() => onpick(year ?? 0, m.month)}
        >
          <span class="big">{monthName(m.month, 'short')}</span>
          <span class="count">{m.count ? formatCount(m.count) : '–'}</span>
        </button>
      {/each}
    </div>
  {/if}
</div>

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
    max-height: 80vh;
    max-height: calc(var(--app-h, 100vh) * 0.8);
    overflow-y: auto;
    -webkit-overflow-scrolling: touch;
    overscroll-behavior: contain;
    padding: 0 16px calc(16px + env(safe-area-inset-bottom, 0px));
    border-radius: 12px 12px 0 0;
    background: var(--surface);
    box-shadow: var(--shadow);
  }
  .sheet:focus {
    outline: none;
  }
  .head {
    position: -webkit-sticky;
    position: sticky;
    top: 0;
    z-index: 1;
    display: flex;
    align-items: center;
    gap: 4px;
    min-height: 52px;
    background: var(--surface);
  }
  h2 {
    flex: 1;
    margin: 0;
    font-size: 17px;
  }
  .close,
  .back {
    min-width: 44px;
    min-height: 44px;
    padding: 0;
    font-size: 24px;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 8px;
  }
  .grid.months {
    grid-template-columns: repeat(4, 1fr);
  }
  .grid button {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    min-height: 56px;
    padding: 4px;
  }
  .grid button.on {
    background: var(--accent-strong);
    border-color: var(--accent-strong);
    color: #fff;
  }
  .big {
    font-size: 17px;
    font-weight: 600;
  }
  .count {
    font-size: 12px;
    opacity: 0.75;
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
