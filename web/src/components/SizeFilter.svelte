<script lang="ts" module>
  export interface SizeValue {
    minPx?: number;
    maxPx?: number;
  }
</script>

<script lang="ts">
  // The Size filter (docs/design/renditions.md "Dimension filter"): a
  // button with a summary that opens a panel with min/max inputs on the
  // longer edge, presets and a Clear. Presets apply at once; typed values
  // apply on Enter, on blur and when the panel closes, never per
  // keystroke (the timeline reloads on every filter change).
  import { describeSize, MAX_PX } from '../lib/filters';

  interface Props {
    minPx?: number;
    maxPx?: number;
    onchange: (v: SizeValue) => void;
  }
  let { minPx, maxPx, onchange }: Props = $props();

  let open = $state(false);
  let minText = $state('');
  let maxText = $state('');
  let wrap: HTMLSpanElement | undefined = $state();
  const uid = `sf-${Math.random().toString(36).slice(2, 9)}`;

  const summary = $derived(describeSize(minPx, maxPx));
  const active = $derived(!!minPx || !!maxPx);

  const presets: { id: string; label: string; value: SizeValue }[] = [
    { id: 'thumbs', label: 'Thumbnails ≤ 640', value: { maxPx: 640 } },
    { id: 'small', label: 'Small ≤ 1600', value: { maxPx: 1600 } },
    { id: 'full', label: 'Full size ≥ 2000', value: { minPx: 2000 } },
  ];
  const isOn = (v: SizeValue) => (v.minPx ?? 0) === (minPx ?? 0) && (v.maxPx ?? 0) === (maxPx ?? 0);

  function load() {
    minText = minPx ? String(minPx) : '';
    maxText = maxPx ? String(maxPx) : '';
  }

  /** A typed bound: a whole number 1..MAX_PX, else none. */
  function parse(s: string): number | undefined {
    const t = s.trim();
    if (!/^\d+$/.test(t)) return undefined;
    const n = Number(t);
    return n >= 1 && n <= MAX_PX ? n : undefined;
  }

  function send(v: SizeValue) {
    if (isOn(v)) return;
    onchange(v);
  }

  /** Apply the typed values; min above max swaps them. */
  function apply() {
    let lo = parse(minText);
    let hi = parse(maxText);
    if (lo !== undefined && hi !== undefined && lo > hi) [lo, hi] = [hi, lo];
    minText = lo !== undefined ? String(lo) : '';
    maxText = hi !== undefined ? String(hi) : '';
    send({ minPx: lo, maxPx: hi });
  }

  function close() {
    if (!open) return;
    open = false;
    apply();
  }

  function toggle() {
    if (open) close();
    else {
      load();
      open = true;
    }
  }

  function pick(v: SizeValue) {
    minText = v.minPx ? String(v.minPx) : '';
    maxText = v.maxPx ? String(v.maxPx) : '';
    send(v);
  }

  $effect(() => {
    if (!open) return;
    const away = (e: PointerEvent) => {
      if (wrap && e.target instanceof Node && !wrap.contains(e.target)) close();
    };
    window.addEventListener('pointerdown', away);
    return () => window.removeEventListener('pointerdown', away);
  });

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && open) {
      e.preventDefault();
      e.stopPropagation();
      close();
    }
  }

  function onInputKeydown(e: KeyboardEvent) {
    if (e.key === 'Enter') {
      e.preventDefault();
      apply();
    }
  }

  /**
   * Apply when focus leaves the panel, not when it moves between Min and
   * Max or to a preset or Clear (a half-entered range would reload the
   * timeline). A null target while open means a click elsewhere or
   * Escape, which close (and apply) on their own; closing applies already,
   * so the inputs leaving the page must not apply twice.
   */
  function onBlur(e: FocusEvent) {
    if (!open) return;
    const to = e.relatedTarget;
    if (to === null || (to instanceof Node && wrap?.contains(to))) return;
    apply();
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<span class="sizefilter" bind:this={wrap} onkeydown={onKeydown}>
  <button type="button" class:set={active} title="Size of the longer edge" aria-expanded={open} aria-controls={uid} onclick={toggle}>
    {summary}
  </button>
  {#if open}
    <div class="panel" id={uid} role="group" aria-label="Size filter">
      <div class="presets">
        {#each presets as p (p.id)}
          <button type="button" class="small" class:on={isOn(p.value)} onclick={() => pick(p.value)}>{p.label}</button>
        {/each}
      </div>
      <div class="rows">
        <label>
          <span>Min</span>
          <input
            type="number"
            min="1"
            max={MAX_PX}
            step="1"
            inputmode="numeric"
            value={minText}
            oninput={(e) => (minText = e.currentTarget.value)}
            onkeydown={onInputKeydown}
            onblur={onBlur}
          />
        </label>
        <label>
          <span>Max</span>
          <input
            type="number"
            min="1"
            max={MAX_PX}
            step="1"
            inputmode="numeric"
            value={maxText}
            oninput={(e) => (maxText = e.currentTarget.value)}
            onkeydown={onInputKeydown}
            onblur={onBlur}
          />
        </label>
        <p class="muted small">longer edge, pixels</p>
      </div>
      {#if active || minText || maxText}
        <div class="foot">
          <button type="button" class="ghost small" onclick={() => pick({})}>Clear</button>
        </div>
      {/if}
    </div>
  {/if}
</span>

<style>
  .sizefilter {
    position: relative;
    display: inline-block;
  }
  button.set {
    border-color: var(--accent-strong);
  }
  .panel {
    position: absolute;
    top: calc(100% + 6px);
    left: 0;
    z-index: 40;
    width: 300px;
    max-width: 92vw;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    box-shadow: var(--shadow);
  }
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
  .rows label {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .rows label > span {
    width: 3.2em;
    color: var(--muted);
  }
  input[type='number'] {
    width: 9em;
    min-height: 32px;
    padding: 0 8px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface);
    color: var(--text);
    font: inherit;
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
