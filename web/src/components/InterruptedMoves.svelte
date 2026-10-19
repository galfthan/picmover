<script lang="ts">
  // "A move from peter was interrupted." with Finish moving, for a library
  // whose listing entry carries `interruptedMoves` (docs/design/
  // library-move.md "The journal and recovery"). Shown on both libraries
  // of the move: the Libraries page card and the library's photo pages.
  import { errorMessage, toInterrupted } from '../lib/api';
  import { libraries } from '../lib/libraries.svelte';
  import { jobs } from '../lib/jobs.svelte';
  import { moves } from '../lib/moves.svelte';
  import { interruptedFor, sentence } from '../lib/movetext';

  interface Props {
    lib: string;
    /** One line for a card instead of a banner. */
    compact?: boolean;
  }
  let { lib, compact = false }: Props = $props();

  const list = $derived(toInterrupted(libraries.find(lib)?.interruptedMoves));
  const nameOf = (id: string) => libraries.find(id)?.name ?? id;
  let error = $state('');
  let busy = $state(false);

  async function finish(to: string) {
    error = '';
    busy = true;
    try {
      const r = await moves.resume(to);
      if (r.state === 'failed') error = r.error || 'Finishing the move failed.';
      else if (r.notes.length) error = r.notes.map(sentence).join(' ');
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

{#if list.length}
  <div class="notice moved" class:compact role="status">
    {#each list as m (`${m.from}/${m.to}/${m.id}`)}
      <span class="line">
        <span>{interruptedFor(lib, m, nameOf)}</span>
        {#if !m.problem}
          <button class="small" disabled={busy || moves.running || !!jobs.active} onclick={() => finish(m.to)}
            title="Finish the interrupted move from its journal. Each photo ends up in one of the two libraries; none is lost.">Finish moving</button
          >
        {/if}
      </span>
    {/each}
    {#if error}<span class="err small">{error}</span>{/if}
  </div>
{/if}

<style>
  .moved {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin: 8px 16px 0;
  }
  .moved.compact {
    margin: 0;
    padding: 4px 8px;
    font-size: 0.9em;
  }
  .line {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
  }
  .err {
    color: var(--danger);
  }
</style>
