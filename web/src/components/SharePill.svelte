<script lang="ts">
  // The "Share" item of the top bar (docs/design/lan-share.md "Entry
  // points"): a plain link to #/share while sharing is off, a green pill
  // "Sharing · 3 shares · 4 phones" while it is on, amber while paused.
  // Sharing must be visible whenever it is on, so this is on every page.
  import { sharing } from '../lib/sharing.svelte';
  import { route } from '../lib/router';

  $effect(() => sharing.watch());

  const st = $derived(sharing.status);
  const active = $derived(route.current.segments[0] === 'share');
  const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

  /** "52 min left" / "3 h left" of the auto-off timer; "" without one. */
  const left = $derived.by(() => {
    const until = st?.autoOff.until ? Date.parse(st.autoOff.until) : NaN;
    if (!Number.isFinite(until)) return '';
    const min = Math.max(0, Math.round((until - Date.now()) / 60_000));
    return min < 90 ? `${min} min left` : `${Math.round(min / 60)} h left`;
  });
</script>

{#if sharing.supported}
  {#if st && st.state === 'on'}
    <a class="pill on" class:active href="#/share" title={st.url ? `Sharing on Wi-Fi at ${st.url}` : 'Sharing on Wi-Fi'}>
      <span class="dot" aria-hidden="true"></span>
      <span class="text">Sharing · {plural(st.shares.length, 'share', 'shares')} · {plural(sharing.phones, 'phone', 'phones')}</span>
      {#if left}<span class="left">{left}</span>{/if}
    </a>
  {:else if st && st.state === 'paused'}
    <a class="pill paused" class:active href="#/share" title={st.reason || 'Sharing is paused'}>
      <span class="dot" aria-hidden="true"></span>
      <span class="text">Sharing paused{st.reason ? `: ${st.reason}` : ''}</span>
    </a>
  {:else if st && st.state === 'error'}
    <a class="pill error" class:active href="#/share" title={st.reason || 'Sharing failed'}>
      <span class="dot" aria-hidden="true"></span>
      <span class="text">Sharing failed</span>
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
  .text {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .left {
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
      max-width: 200px;
    }
    .left {
      display: none;
    }
  }
</style>
