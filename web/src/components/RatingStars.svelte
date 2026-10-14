<script lang="ts">
  // Five stars. Clicking a star sets that rating; clicking the current
  // value clears it (0). Read-only when there is no onchange.
  interface Props {
    value: number;
    onchange?: (v: number) => void;
    label?: string;
    small?: boolean;
    disabled?: boolean;
  }
  let { value, onchange, label = 'Rating', small = false, disabled = false }: Props = $props();
  const stars = [1, 2, 3, 4, 5];
</script>

{#if onchange}
  <span class="stars" class:small role="group" aria-label={label}>
    {#each stars as n (n)}
      <button
        type="button"
        class="star"
        class:on={n <= value}
        aria-pressed={n <= value}
        aria-label={n === value ? `Clear rating (${n} ${n === 1 ? 'star' : 'stars'})` : `Rate ${n} ${n === 1 ? 'star' : 'stars'}`}
        title={n === value ? 'Clear rating' : `${n} ${n === 1 ? 'star' : 'stars'}`}
        {disabled}
        onclick={() => onchange(n === value ? 0 : n)}>★</button
      >
    {/each}
  </span>
{:else}
  <span class="stars ro" class:small role="img" aria-label={value ? `${value} of 5 stars` : 'Not rated'}>
    {#each stars as n (n)}<span class="star" class:on={n <= value} aria-hidden="true">★</span>{/each}
  </span>
{/if}

<style>
  .stars {
    display: inline-flex;
    align-items: center;
    gap: 0;
  }
  .star {
    min-height: 28px;
    padding: 0 3px;
    border: 0;
    background: transparent;
    color: var(--faint);
    font-size: 18px;
    line-height: 1;
  }
  button.star:hover:not(:disabled) {
    background: transparent;
    color: var(--warn);
  }
  .star.on {
    color: var(--warn);
  }
  .small .star {
    min-height: 0;
    padding: 0 1px;
    font-size: 13px;
  }
  .ro .star {
    padding: 0;
  }
  /* Touch: 44 px hit targets. */
  @media (pointer: coarse) {
    button.star {
      min-width: 44px;
      min-height: 44px;
      font-size: 24px;
    }
    .small button.star {
      font-size: 18px;
    }
  }
</style>
