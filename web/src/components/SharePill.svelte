<script lang="ts">
  // The "Share" item of the top bar (docs/design/lan-share.md "Entry
  // points"): a plain link to #/share while sharing is off, a green pill
  // "Sharing · 3 shares · 4 phones" while it is on, amber while paused.
  // Sharing must be visible whenever it is on, so this is on every page.
  // It reads GET /api/share/status (counts of the shares phones can use
  // now and their phones), which looks at no library.
  //
  // In a narrow top bar the pill shortens by whole parts (lib/sharetext.ts
  // pillVariants): it asks for the width of its longest text, measures
  // every variant, and shows the longest that fits the width it got. The
  // tooltip always has all of it.
  import { sharing } from '../lib/sharing.svelte';
  import { route } from '../lib/router';
  import { fittingVariant, pillVariants } from '../lib/sharetext';

  $effect(() => sharing.watchSummary());

  const st = $derived(sharing.summary);
  const active = $derived(route.current.segments[0] === 'share');

  /** "52 min left" / "3 h left" of the auto-off timer; "" without one. */
  const left = $derived.by(() => {
    const until = st?.autoOff.until ? Date.parse(st.autoOff.until) : NaN;
    if (!Number.isFinite(until)) return '';
    const min = Math.max(0, Math.round((until - Date.now()) / 60_000));
    return min < 90 ? `${min} min left` : `${Math.round(min / 60)} h left`;
  });

  const label = $derived(st && st.state !== 'off' && st.state !== '' ? pillVariants(st, left) : null);
  const variants = $derived(label?.variants ?? []);
  let pick = $state(0);
  const shown = $derived(variants[Math.min(pick, variants.length - 1)] ?? null);

  let fit: HTMLSpanElement | undefined = $state();
  let measures: HTMLSpanElement | undefined = $state();

  function measure() {
    if (!fit || !measures) return;
    const widths = [...measures.children].map((c) => (c as HTMLElement).offsetWidth);
    pick = fittingVariant(widths, fit.clientWidth);
  }
  // Again whenever the texts change or the room for them does.
  $effect(() => {
    void variants;
    measure();
  });
  $effect(() => {
    const el = fit;
    if (!el || typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(() => measure());
    ro.observe(el);
    return () => ro.disconnect();
  });
</script>

{#snippet body(v: { text: string; left: string })}<span class="text">{v.text}</span>{#if v.left}<span class="left">{v.left}</span>{/if}{/snippet}

{#if sharing.supported}
  {#if st && label && shown && (st.state === 'on' || st.state === 'paused' || st.state === 'error')}
    <a class="pill" class:on={st.state === 'on'} class:paused={st.state === 'paused'} class:error={st.state === 'error'} class:active href="#/share" title={label.title} aria-label={label.title}>
      <span class="dot" aria-hidden="true"></span>
      <span class="fit" bind:this={fit}>
        <!-- Asks for the room of the longest text; the flex row gives what it can. -->
        <span class="sizer" aria-hidden="true">{@render body(variants[0] ?? shown)}</span>
        <span class="shown">{@render body(shown)}</span>
        <span class="measures" aria-hidden="true" bind:this={measures}>
          {#each variants as v, i (i)}<span>{@render body(v)}</span>{/each}
        </span>
      </span>
    </a>
  {:else}
    <a class="plain" class:active href="#/share">Share</a>
  {/if}
{/if}

<style>
  .plain {
    padding: 6px 12px;
    border-radius: var(--radius-sm);
    color: var(--muted);
    white-space: nowrap;
  }
  .plain:hover,
  .plain.active {
    color: var(--text);
    background: var(--surface-2);
    text-decoration: none;
  }
  .pill {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    max-width: 360px;
    min-height: 30px;
    padding: 0 12px;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--surface-2);
    color: var(--text);
    white-space: nowrap;
  }
  .pill:hover {
    text-decoration: none;
    background: var(--surface-3);
  }
  .fit {
    position: relative;
    display: block;
    min-width: 0;
    overflow: hidden;
  }
  .sizer {
    display: block;
    height: 0;
    visibility: hidden;
  }
  .shown {
    display: block;
  }
  .measures {
    position: absolute;
    top: 0;
    left: 0;
    visibility: hidden;
    pointer-events: none;
  }
  .measures > span {
    position: absolute;
    top: 0;
    left: 0;
  }
  .left {
    margin-left: 8px;
    color: var(--muted);
    font-size: 0.9em;
  }
  .dot {
    flex: none;
    width: 8px;
    height: 8px;
    border-radius: 50%;
  }
  .on {
    border-color: var(--ok);
  }
  .on .dot {
    background: var(--ok);
  }
  .paused {
    border-color: var(--warn);
  }
  .paused .dot {
    background: var(--warn);
  }
  .error {
    border-color: var(--danger);
  }
  .error .dot {
    background: var(--danger);
  }
  @media (max-width: 900px) {
    .pill {
      max-width: 240px;
    }
  }
</style>
