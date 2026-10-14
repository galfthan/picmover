<script lang="ts">
  import { yearMarks, type Timeline } from '../lib/timeline';
  import { bucketLabel } from '../lib/format';

  interface Props {
    timeline: Timeline;
    scrollTop: number;
    viewportHeight: number;
    /** Scroll the grid to a timeline offset. */
    onjump: (y: number) => void;
  }
  let { timeline, scrollTop, viewportHeight, onjump }: Props = $props();

  const PAD = 8; // track inset top and bottom
  const LABEL_GAP = 16; // minimum px between year labels

  const track = $derived(Math.max(0, viewportHeight - 2 * PAD));
  const maxScroll = $derived(Math.max(1, timeline.totalHeight - viewportHeight));

  function toTrack(y: number): number {
    return PAD + Math.min(1, Math.max(0, y / maxScroll)) * track;
  }
  function toTimeline(trackY: number): number {
    return (Math.min(track, Math.max(0, trackY - PAD)) / Math.max(1, track)) * maxScroll;
  }

  const marks = $derived.by(() => {
    const out: { year: number; y: number; top: number }[] = [];
    let lastY = -Infinity;
    for (const m of yearMarks(timeline.buckets)) {
      const y = toTrack(m.top);
      if (y - lastY < LABEL_GAP) continue;
      out.push({ year: m.year, y, top: m.top });
      lastY = y;
    }
    return out;
  });

  let hoverY = $state<number | null>(null);
  let dragging = $state(false);
  let el: HTMLDivElement | undefined = $state();

  const hoverLabel = $derived.by(() => {
    if (hoverY === null) return '';
    const i = timeline.indexAtOffset(toTimeline(hoverY));
    const b = timeline.buckets[i];
    return b ? bucketLabel(b.year, b.month) : '';
  });

  function localY(e: PointerEvent): number {
    const r = el?.getBoundingClientRect();
    return r ? e.clientY - r.top : 0;
  }
  function down(e: PointerEvent) {
    if (e.button !== 0) return;
    dragging = true;
    el?.setPointerCapture(e.pointerId);
    hoverY = localY(e);
    onjump(toTimeline(hoverY));
  }
  function move(e: PointerEvent) {
    hoverY = localY(e);
    if (dragging) onjump(toTimeline(hoverY));
  }
  function up(e: PointerEvent) {
    dragging = false;
    if (el?.hasPointerCapture(e.pointerId)) el.releasePointerCapture(e.pointerId);
  }
  function leave() {
    if (!dragging) hoverY = null;
  }

  // Browsers without `touch-action` (Sailfish Browser, Gecko 91) would pan
  // the page under a dragging finger and cancel the pointer: a non-passive
  // touchmove keeps the drag here.
  $effect(() => {
    const node = el;
    if (!node) return;
    const hold = (e: TouchEvent) => {
      if (e.cancelable) e.preventDefault();
    };
    node.addEventListener('touchmove', hold, { passive: false });
    return () => node.removeEventListener('touchmove', hold);
  });

  // Keyboard: Up/Down and PageUp/PageDown jump a year, Home/End to the ends.
  const allMarks = $derived(yearMarks(timeline.buckets));
  const currentLabel = $derived.by(() => {
    const b = timeline.buckets[timeline.indexAtOffset(scrollTop)];
    return b ? bucketLabel(b.year, b.month) : '';
  });
  function yearStep(dir: 1 | -1): number | null {
    const ms = allMarks;
    if (ms.length === 0) return null;
    // Index of the year the view is in (last mark at or above the top).
    let cur = -1;
    for (let i = 0; i < ms.length; i++) if ((ms[i]?.top ?? Infinity) <= scrollTop + 1) cur = i;
    if (dir === -1) {
      const m = ms[cur];
      // Inside a year: go to its start first.
      if (m && scrollTop > m.top + 1) return m.top;
      return ms[Math.max(0, cur - 1)]?.top ?? 0;
    }
    const next = ms[cur + 1];
    return next ? next.top : maxScroll;
  }
  function key(e: KeyboardEvent) {
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    let y: number | null;
    switch (e.key) {
      case 'ArrowUp':
      case 'PageUp':
        y = yearStep(-1);
        break;
      case 'ArrowDown':
      case 'PageDown':
        y = yearStep(1);
        break;
      case 'Home':
        y = 0;
        break;
      case 'End':
        y = maxScroll;
        break;
      default:
        return;
    }
    e.preventDefault();
    if (y !== null) onjump(y);
  }
</script>

{#if timeline.totalHeight > viewportHeight}
  <div
    class="scrubber"
    class:dragging
    bind:this={el}
    onpointerdown={down}
    onpointermove={move}
    onpointerup={up}
    onpointercancel={up}
    onpointerleave={leave}
    onkeydown={key}
    role="slider"
    tabindex="0"
    aria-label="Timeline (Up/Down: year, Home/End)"
    aria-orientation="vertical"
    aria-valuemin={0}
    aria-valuemax={Math.round(maxScroll)}
    aria-valuenow={Math.round(Math.min(maxScroll, Math.max(0, scrollTop)))}
    aria-valuetext={currentLabel}
  >
    {#each marks as m (m.year)}
      <button
        class="year"
        style:top="{m.y}px"
        tabindex="-1"
        onpointerdown={(e) => e.stopPropagation()}
        onclick={() => onjump(m.top)}>{m.year === 0 ? '—' : m.year}</button
      >
    {/each}
    <span class="pos" style:top="{toTrack(scrollTop)}px" aria-hidden="true"></span>
    {#if hoverY !== null && hoverLabel}
      <span class="hover" style:top="{Math.min(track + PAD, Math.max(PAD, hoverY))}px">{hoverLabel}</span>
    {/if}
  </div>
{/if}

<style>
  .scrubber {
    position: absolute;
    top: 0;
    right: 0;
    bottom: 0;
    width: 52px;
    cursor: row-resize;
    user-select: none;
    touch-action: none;
    z-index: 3;
  }
  .scrubber:focus {
    outline: none;
  }
  .scrubber:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: -2px;
  }
  .year {
    position: absolute;
    right: 8px;
    transform: translateY(-50%);
    min-height: 0;
    padding: 0 4px;
    border: 0;
    background: transparent;
    color: var(--muted);
    font-size: 11px;
    font-variant-numeric: tabular-nums;
    line-height: 14px;
  }
  .year:hover:not(:disabled) {
    color: var(--text);
    background: transparent;
  }
  .pos {
    position: absolute;
    right: 2px;
    width: 18px;
    height: 2px;
    background: var(--accent);
    transform: translateY(-50%);
    pointer-events: none;
  }
  .hover {
    position: absolute;
    right: 56px;
    transform: translateY(-50%);
    padding: 3px 8px;
    border-radius: 4px;
    background: var(--surface-3);
    color: var(--text);
    font-size: 12px;
    white-space: nowrap;
    box-shadow: var(--shadow);
    pointer-events: none;
  }
</style>
