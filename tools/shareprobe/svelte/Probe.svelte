<script>
  // A tiny Svelte 5 component that exercises what the phone app would:
  // runes ($state, $derived, $effect, $props), an each block, an event
  // handler, a class directive, and a transition-free conditional.
  let { onready } = $props();
  let count = $state(0);
  let items = $state([{ id: 1, n: 'one' }, { id: 2, n: 'two' }]);
  let doubled = $derived(count * 2);
  let last = $derived(items[items.length - 1].n);

  $effect(() => {
    onready?.({ count, doubled, last });
  });

  export function bump() {
    count += 1;
    items.push({ id: items.length + 1, n: 'item ' + (items.length + 1) });
  }
</script>

<div class="svelte-probe">
  <p class:ok={count > 0} data-testid="state">count {count}, doubled {doubled}, last {last}</p>
  <ul>
    {#each items as it (it.id)}
      <li>{it.n}</li>
    {/each}
  </ul>
  {#if count > 0}<p class="ok">Svelte 5 mounted and updated</p>{/if}
  <button type="button" onclick={bump}>Tap to update</button>
</div>
