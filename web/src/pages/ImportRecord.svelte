<script lang="ts">
  // #/l/<id>/imports/<iid>: the grid filtered to one import record, with
  // the record's facts and Rename / Delete record in the header (like the
  // Album page). Deleting the record never touches a file.
  import { untrack } from 'svelte';
  import { ApiError, errorMessage, type Filters, type ImportRecord, type LibApi } from '../lib/api';
  import { library } from '../lib/library.svelte';
  import { toasts } from '../lib/toast.svelte';
  import { formatBytes, formatCount, formatInstant } from '../lib/format';
  import { importCounts, importTitle, modeLabel, outcomeLabel } from '../lib/imports';
  import { navigate } from '../lib/router';
  import { libHref, libPath } from '../lib/routes';
  import Dialog from '../components/Dialog.svelte';
  import Grid from './Grid.svelte';

  interface Props {
    api: LibApi;
    importId: number;
    filters: Filters;
    viewId: number | null;
  }
  let { api, importId, filters, viewId }: Props = $props();

  /** Same rule as the server: trimmed, at most 200 characters, may be empty. */
  const MAX_NAME = 200;

  let rec = $state<ImportRecord | null>(null);
  let loadError = $state('');
  let missing = $state(false);

  async function load() {
    const iid = importId;
    try {
      const r = await api.getImport(iid);
      if (iid !== importId) return;
      rec = r;
      loadError = '';
      missing = false;
    } catch (e) {
      if (iid !== importId) return;
      missing = e instanceof ApiError && e.status === 404;
      loadError = missing ? 'There is no such import record in this library.' : errorMessage(e);
    }
  }
  $effect(() => {
    void importId;
    untrack(() => void load());
  });

  const title = $derived(rec ? importTitle(rec) : `#${importId}`);
  const outcome = $derived(rec ? outcomeLabel(rec.outcome) : null);

  type DialogState = 'rename' | 'delete' | null;
  let dialog = $state<DialogState>(null);
  let name = $state('');
  let working = $state(false);
  let dialogError = $state('');

  function show(d: NonNullable<DialogState>) {
    dialogError = '';
    name = rec?.name ?? '';
    dialog = d;
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    const d = dialog;
    if (!d || working) return;
    const n = name.trim();
    if (d === 'rename' && [...n].length > MAX_NAME) {
      dialogError = `At most ${MAX_NAME} characters.`;
      return;
    }
    working = true;
    dialogError = '';
    try {
      if (d === 'rename') {
        await api.renameImport(importId, n);
        dialog = null;
        await load();
        void library.loadImports();
      } else {
        await api.deleteImport(importId);
        dialog = null;
        void library.loadImports();
        toasts.show('Import record deleted');
        navigate(libPath(api.id, 'imports'));
      }
    } catch (err) {
      dialogError = errorMessage(err);
    } finally {
      working = false;
    }
  }
</script>

{#if missing}
  <div class="page">
    <div class="page-inner">
      <a class="back" href={libHref(api.id, 'imports')}>← Imports</a>
      <div class="error-box">{loadError}</div>
    </div>
  </div>
{:else}
  <Grid {api} {filters} {viewId} mode="import" {importId} basePath={libPath(api.id, `imports/${importId}`)}>
    {#snippet header()}
      <div class="ihead">
        <div class="line">
          <a class="back" href={libHref(api.id, 'imports')}>← Imports</a>
          <h1>Import: {title}</h1>
          {#if rec}
            <span class="chip">{modeLabel(rec.mode)}</span>
            {#if outcome}<span class="chip">{outcome}</span>{/if}
          {/if}
          <span class="spacer"></span>
          <button class="small" disabled={!rec} onclick={() => show('rename')}>Rename</button>
          <button class="small" disabled={!rec} onclick={() => show('delete')}>Delete record</button>
        </div>
        {#if rec}
          <p class="facts muted small">
            {formatInstant(rec.startedAt)}
            {#if rec.source}· from <span class="mono">{rec.source}</span>{/if}
            {#if rec.host}· on {rec.host}{/if}
            · {importCounts(rec)}{#if rec.bytes > 0}, {formatBytes(rec.bytes)}{/if}
          </p>
        {:else if loadError}
          <div class="error-box small">{loadError} <button class="small" onclick={load}>Retry</button></div>
        {/if}
      </div>
    {/snippet}
  </Grid>
{/if}

{#if dialog}
  <Dialog title={dialog === 'rename' ? 'Rename import' : `Delete the record of “${title}”?`} onclose={() => (dialog = null)}>
    <form onsubmit={submit}>
      {#if dialog === 'delete'}
        <p>The import record is removed from the history.</p>
        <p>The {formatCount(rec?.items ?? 0)} photos and videos it brought in stay in the library; no file is touched.</p>
      {:else}
        <label class="field">
          <span>Name</span>
          <input type="text" bind:value={name} maxlength={MAX_NAME} autocomplete="off" placeholder={rec?.sourceName || `Import #${importId}`} />
        </label>
        <p class="muted small">Leave empty to show the source folder's name.</p>
      {/if}
      {#if dialogError}<div class="error-box small" role="alert">{dialogError}</div>{/if}
      <div class="row end">
        <button type="button" onclick={() => (dialog = null)}>Cancel</button>
        <button type="submit" class={dialog === 'delete' ? 'danger' : 'primary'} disabled={working}>
          {dialog === 'delete' ? 'Delete record' : 'Rename'}
        </button>
      </div>
    </form>
  </Dialog>
{/if}

<style>
  .ihead {
    flex: none;
    padding: 12px 16px 4px;
  }
  .line {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: 12px;
  }
  .line h1 {
    margin: 0;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .facts {
    margin: 4px 0 0;
    overflow-wrap: anywhere;
  }
  .back {
    font-size: 0.9em;
  }
  .chip {
    padding: 0 8px;
    border-radius: 999px;
    background: var(--surface-3);
    color: var(--muted);
    font-size: 0.8em;
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
