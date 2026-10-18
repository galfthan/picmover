<script lang="ts">
  // The timeline scrubber beside a grid (docs/design/lan-share.md
  // "Timeline scrolling"). The track maps proportionally onto the whole
  // timeline: the layout's heights come from the months' counts, so a
  // point of the track is a fixed place among all the photos, never a
  // year bucket. The year labels are marks, not buttons: a press anywhere
  // on the track (labels included) jumps there and drags. On a phone a
  // finger covers several labels, and the browser's touch adjustment sent
  // the press to the nearest year button, which jumped to that year's
  // start instead of dragging (the "snaps to every year" complaint).
  //
  // `touch` (the phone): a thumb at least 44 px tall, kept off the very
  // edge where the system takes swipes, and a large month bubble while
  // dragging. Grabbing the thumb keeps the finger's offset on it, so a
  // press does not move the view. Touch is handled with touch events and
  // non-passive listeners (the Sailfish Browser, Gecko 91, has no
  // `touch-action`, and would pan the page and cancel the pointer); the
  // mouse and pen use pointer events.
  //
  // Keyboard (role slider): Up/Down a month, Page Up/Down a year,
  // Home/End the ends; the value's text is the month at the top.
  import { scrubScale, yearMarks, type Timeline } from '../lib/timeline';
  import { bucketLabel } from '../lib/format';

  interface Props {
    timeline: Timeline;
    scrollTop: number;
    viewportHeight: number;
    /** Scroll the grid to a timeline offset. */
    onjump: (y: number) => void;
    /** The phone's scrubber: a large thumb and bubble (see above). */
    touch?: boolean;
    /** Called when a drag starts and ends. */
    ondrag?: (dragging: boolean) => void;
  }
  let { timeline, scrollTop, viewportHeight, onjump, touch = false, ondrag }: Props = $props();

  const PAD = 8; // track inset top and bottom
  const LABEL_GAP = 16; // minimum px between year labels
  /** The thumb's height on a phone (a 44 px target and then some). */
  const THUMB = 48;

  const thumb = $derived(touch ? THUMB : 0);
  const scale = $derived(scrubScale(timeline.totalHeight, viewportHeight, PAD, thumb));
  const track = $derived(Math.max(0, viewportHeight - 2 * PAD));
  const maxScroll = $derived(scale.maxScroll);
  const toTrack = (y: number) => scale.toTrack(y);
  const toTimeline = (t: number) => scale.toTimeline(t);

  const allMarks = $derived(yearMarks(timeline.buckets));
  const marks = $derived.by(() => {
    const out: { year: number; y: number }[] = [];
    let lastY = -Infinity;
    for (const m of allMarks) {
      const y = toTrack(m.top);
      if (y - lastY < LABEL_GAP) continue;
      out.push({ year: m.year, y });
      lastY = y;
    }
    return out;
  });

  let hoverY = $state<number | null>(null);
  let dragging = $state(false);
  /** Where on the thumb it was grabbed (finger minus thumb centre). */
  let grab = 0;
  let el: HTMLDivElement | undefined = $state();

  const thumbY = $derived(toTrack(scrollTop));
  const labelAt = (y: number) => {
    const b = timeline.buckets[timeline.indexAtOffset(y)];
    return b ? bucketLabel(b.year, b.month) : '';
  };
  const currentLabel = $derived(labelAt(scrollTop));
  const hoverLabel = $derived(hoverY === null ? '' : labelAt(toTimeline(hoverY - grab)));

  function localY(clientY: number): number {
    const r = el?.getBoundingClientRect();
    return r ? clientY - r.top : 0;
  }

  function start(y: number) {
    dragging = true;
    // On the thumb: keep the offset (the view stays). Elsewhere: the thumb's centre comes to the finger.
    grab = thumb > 0 && Math.abs(y - thumbY) <= thumb / 2 ? y - thumbY : 0;
    hoverY = y;
    ondrag?.(true);
    onjump(toTimeline(y - grab));
  }
  function drag(y: number) {
    hoverY = y;
    if (dragging) onjump(toTimeline(y - grab));
  }
  function end() {
    if (!dragging) return;
    dragging = false;
    grab = 0;
    hoverY = null;
    ondrag?.(false);
  }

  // ---- Mouse and pen (pointer events); touch is below ----

  function down(e: PointerEvent) {
    if (e.pointerType === 'touch' || e.button !== 0) return;
    e.preventDefault(); // no text selection, no focus jump
    el?.setPointerCapture?.(e.pointerId);
    start(localY(e.clientY));
  }
  function move(e: PointerEvent) {
    if (e.pointerType === 'touch') return;
    drag(localY(e.clientY));
  }
  function up(e: PointerEvent) {
    if (e.pointerType === 'touch') return;
    if (el?.hasPointerCapture?.(e.pointerId)) el.releasePointerCapture(e.pointerId);
    end();
  }
  function leave(e: PointerEvent) {
    if (e.pointerType !== 'touch' && !dragging) hoverY = null;
  }

  // ---- Touch: non-passive listeners, so the page never pans under the finger ----

  $effect(() => {
    const node = el;
    if (!node) return;
    let id: number | null = null;
    const find = (list: TouchList) => {
      for (let i = 0; i < list.length; i++) {
        const t = list[i];
        if (t && t.identifier === id) return t;
      }
      return null;
    };
    const onStart = (e: TouchEvent) => {
      if (e.cancelable) e.preventDefault();
      if (id !== null) return;
      const t = e.changedTouches[0];
      if (!t) return;
      id = t.identifier;
      start(localY(t.clientY));
    };
    const onMove = (e: TouchEvent) => {
      if (e.cancelable) e.preventDefault();
      const t = find(e.changedTouches);
      if (t) drag(localY(t.clientY));
    };
    const onEnd = (e: TouchEvent) => {
      if (id === null || !find(e.changedTouches)) return;
      id = null;
      end();
    };
    node.addEventListener('touchstart', onStart, { passive: false });
    node.addEventListener('touchmove', onMove, { passive: false });
    node.addEventListener('touchend', onEnd);
    node.addEventListener('touchcancel', onEnd);
    return () => {
      node.removeEventListener('touchstart', onStart);
      node.removeEventListener('touchmove', onMove);
      node.removeEventListener('touchend', onEnd);
      node.removeEventListener('touchcancel', onEnd);
    };
  });

  // ---- Keyboard ----

  /** The top of the month before or after the one at the top of the view. */
  function monthStep(dir: 1 | -1): number {
    const bs = timeline.buckets;
    const i = timeline.indexAtOffset(scrollTop);
    const b = bs[i];
    if (!b) return 0;
    if (dir === -1) return scrollTop > b.top + 1 ? b.top : (bs[Math.max(0, i - 1)]?.top ?? 0);
    return bs[i + 1]?.top ?? maxScroll;
  }
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
        y = monthStep(-1);
        break;
      case 'ArrowDown':
        y = monthStep(1);
        break;
      case 'PageUp':
        y = yearStep(-1);
        break;
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
    if (y !== null) onjump(Math.min(maxScroll, y));
  }
</script>

{#if timeline.totalHeight > viewportHeight}
  <div
    class="scrubber"
    class:touch
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
    aria-label="Timeline (Up/Down: month, Page Up/Down: year, Home/End)"
    aria-orientation="vertical"
    aria-valuemin={0}
    aria-valuemax={Math.round(maxScroll)}
    aria-valuenow={Math.round(Math.min(maxScroll, Math.max(0, scrollTop)))}
    aria-valuetext={currentLabel}
  >
    {#each marks as m (m.year)}
      <span class="year" style:top="{m.y}px" aria-hidden="true">{m.year === 0 ? '—' : m.year}</span>
    {/each}
    {#if touch}
      <span class="thumb" style:top="{thumbY}px" aria-hidden="true"><span class="grip"></span></span>
    {:else}
      <span class="pos" style:top="{thumbY}px" aria-hidden="true"></span>
    {/if}
    {#if hoverY !== null && hoverLabel}
      <span class="hover" class:bubble={dragging && touch} style:top="{touch && dragging ? thumbY : Math.min(track + PAD, Math.max(PAD, hoverY))}px" aria-hidden="true"
        >{hoverLabel}</span
      >
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
    -webkit-user-select: none;
    touch-action: none;
    -webkit-tap-highlight-color: transparent;
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
    padding: 0 4px;
    color: var(--muted);
    font-size: 11px;
    font-variant-numeric: tabular-nums;
    line-height: 14px;
    pointer-events: none;
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
  /* The phone's thumb: 48 px tall, off the screen's edge. */
  .thumb {
    position: absolute;
    right: 8px;
    width: 26px;
    height: 48px;
    margin-top: -24px;
    border-radius: 13px;
    background: var(--accent-strong, var(--accent));
    box-shadow: 0 1px 4px rgba(0, 0, 0, 0.35);
    pointer-events: none;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .grip {
    width: 10px;
    height: 12px;
    border-top: 2px solid rgba(255, 255, 255, 0.85);
    border-bottom: 2px solid rgba(255, 255, 255, 0.85);
    box-sizing: border-box;
  }
  .grip::before {
    content: '';
    display: block;
    margin-top: 3px;
    height: 2px;
    background: rgba(255, 255, 255, 0.85);
  }
  .dragging .thumb {
    box-shadow: 0 2px 10px rgba(0, 0, 0, 0.45);
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
  /* The month under the finger, large enough to read past it. */
  .hover.bubble {
    right: 44px;
    padding: 10px 16px;
    border-radius: 22px;
    font-size: 20px;
    font-weight: 600;
    background: var(--surface);
    border: 1px solid var(--border);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.3);
  }
</style>
