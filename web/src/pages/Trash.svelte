<script lang="ts">
  // #/l/<id>/trash: the grid in trash mode. Tiles say when they were
  // deleted; the selection bar has Restore and Delete permanently; Empty
  // trash asks for the number of files to be typed.
  import { untrack } from 'svelte';
  import { errorMessage, type Filters, type LibApi } from '../lib/api';
  import { library } from '../lib/library.svelte';
  import { toasts } from '../lib/toast.svelte';
  import { emptyTrash, failureText } from '../lib/actions';
  import { formatCount } from '../lib/format';
  import { libPath } from '../lib/routes';
  import { joinPath } from '../lib/paths';
  import Grid from './Grid.svelte';
  import Dialog from '../components/Dialog.svelte';

  interface Props {
    api: LibApi;
    filters: Filters;
    viewId: number | null;
  }
  let { api, filters, viewId }: Props = $props();

  // The count comes from the library summary; make it current on arrival.
  $effect(() => {
    void api.id;
    untrack(() => void library.load());
  });
  const count = $derived(library.trashed);
  let confirming = $state(false);
  let typed = $state('');
  let working = $state(false);
  let error = $state('');

  function ask() {
    typed = '';
    error = '';
    confirming = true;
  }

  async function empty(e: SubmitEvent) {
    e.preventDefault();
    if (typed.trim() !== String(count) || working) return;
    working = true;
    error = '';
    try {
      const r = await emptyTrash(api);
      confirming = false;
      toasts.show(`Deleted ${formatCount(r.deleted)} ${r.deleted === 1 ? 'file' : 'files'} permanently`);
      if (r.failed.length) toasts.error(failureText(r.failed, 'deleted'));
    } catch (err) {
      error = errorMessage(err);
    } finally {
      working = false;
    }
  }
</script>

<Grid {api} {filters} {viewId} mode="trash" basePath={libPath(api.id, 'trash')}>
  {#snippet header()}
    <div class="thead">
      <h1>Trash</h1>
      <span class="muted">{formatCount(count)} {count === 1 ? 'item' : 'items'}</span>
      <span class="muted small where">
        Files wait in <span class="mono">{library.info ? joinPath(library.info.root, '.picmover', 'trash') : '.picmover/trash'}</span> until
        the trash is emptied.
      </span>
      <span class="spacer"></span>
      <button class="danger" disabled={count === 0} onclick={ask}>Empty trash</button>
    </div>
  {/snippet}
</Grid>

{#if confirming}
  <Dialog title="Empty the trash?" onclose={() => (confirming = false)}>
    <form onsubmit={empty}>
      <p>
        This deletes {formatCount(count)}
        {count === 1 ? 'file' : 'files'} from the disk permanently. It cannot be undone.
      </p>
      <label class="field">
        <span>Type <strong>{count}</strong> to confirm</span>
        <input type="text" inputmode="numeric" bind:value={typed} autocomplete="off" />
      </label>
      {#if error}<div class="error-box small" role="alert">{error}</div>{/if}
      <div class="row end">
        <button type="button" onclick={() => (confirming = false)}>Cancel</button>
        <button type="submit" class="danger" disabled={typed.trim() !== String(count) || working}>Delete permanently</button>
      </div>
    </form>
  </Dialog>
{/if}

<style>
  .thead {
    flex: none;
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: 12px;
    padding: 12px 16px 4px;
  }
  .thead h1 {
    margin: 0;
  }
  .where {
    word-break: break-all;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .end {
    justify-content: flex-end;
    margin-top: 12px;
  }
</style>
