<script lang="ts">
  // The active filters above the grid, one removable chip each, so a
  // filtered grid never looks like the whole share. Tapping a chip opens
  // the sheet; its × removes just that filter.
  import type { Chip } from './filters';

  interface Props {
    chips: Chip[];
    onremove: (c: Chip) => void;
    onopen: () => void;
    onclear: () => void;
  }
  let { chips, onremove, onopen, onclear }: Props = $props();
</script>

<div class="chips" role="group" aria-label="Active filters">
  {#each chips as c (c.id)}
    <span class="chip">
      <button type="button" class="label" onclick={onopen}>{c.label}</button>
      <button type="button" class="x" aria-label="Remove filter {c.label}" onclick={() => onremove(c)}>×</button>
    </span>
  {/each}
  {#if chips.length > 1}
    <button type="button" class="ghost clear" onclick={onclear}>Clear all</button>
  {/if}
</div>

<style>
  .chips {
    flex: none;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
    padding: 6px calc(8px + env(safe-area-inset-right, 0px)) 6px calc(8px + env(safe-area-inset-left, 0px));
    border-bottom: 1px solid var(--border);
    background: var(--surface);
  }
  .chip {
    display: inline-flex;
    align-items: stretch;
    max-width: 100%;
    border-radius: 20px;
    background: var(--accent-strong);
    color: #fff;
  }
  .chip button {
    min-height: 40px;
    border: 0;
    background: none;
    color: #fff;
  }
  .chip button:hover:not(:disabled) {
    background: none;
  }
  .label {
    min-width: 0;
    max-width: 240px;
    padding: 0 4px 0 14px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    border-radius: 20px 0 0 20px;
  }
  .x {
    min-width: 40px;
    padding: 0;
    font-size: 20px;
    border-radius: 0 20px 20px 0;
  }
  .clear {
    min-height: 40px;
  }
</style>
