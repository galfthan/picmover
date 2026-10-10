<script lang="ts">
  // "Add to album": a button opening a small popover with the album list
  // (filter by typing) and a "New album" entry.
  import type { Album } from '../lib/api';
  import TagInput, { type Pick } from './TagInput.svelte';

  interface Props {
    albums: readonly Album[];
    onpick: (p: Pick) => void;
    /** Albums the item is already in (not offered). */
    exclude?: readonly number[];
    label?: string;
    disabled?: boolean;
    open?: boolean;
    /** Where the popover opens: above the button (the selection bar) or below it. */
    placement?: 'up' | 'down';
  }
  let { albums, onpick, exclude = [], label = 'Album', disabled = false, open = $bindable(false), placement = 'up' }: Props = $props();

  let wrap: HTMLSpanElement | undefined = $state();
  let input: HTMLInputElement | undefined = $state();
  const options = $derived(albums.map((a) => ({ id: a.id, name: a.name, count: a.count })));

  $effect(() => {
    if (!open) return;
    input?.focus();
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
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<span class="picker" class:down={placement === 'down'} bind:this={wrap} onkeydown={onKeydown}>
  <button type="button" aria-haspopup="true" aria-expanded={open} {disabled} onclick={() => (open = !open)}>{label}</button>
  {#if open}
    <span class="pop">
      <TagInput
        bind:inputEl={input}
        options={options}
        {exclude}
        listOnFocus
        up={placement === 'up'}
        createLabel="New album"
        placeholder="Album name…"
        label="Add to album"
        onpick={(p) => {
          open = false;
          onpick(p);
        }}
      />
    </span>
  {/if}
</span>

<style>
  .picker {
    position: relative;
    display: inline-block;
  }
  .pop {
    position: absolute;
    bottom: calc(100% + 6px);
    left: 0;
    z-index: 60;
    padding: 8px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface);
    box-shadow: var(--shadow);
  }
  .down .pop {
    bottom: auto;
    top: calc(100% + 6px);
  }
</style>
