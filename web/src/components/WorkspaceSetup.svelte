<script lang="ts">
  // The workspace setup card of the desktop app (docs/design/desktop-shell.md
  // "Web"): the Libraries page shows it while no workspace is set (mode
  // "unset") and after the header's Change link (#/?setup=1). Choosing a
  // folder posts /api/app/workspace; errors stay inline, under the picker.
  import { app } from '../lib/app.svelte';
  import FolderPicker from './FolderPicker.svelte';
  import CopyButton from './CopyButton.svelte';

  interface Props {
    /** The current workspace folder ("" when none is set). */
    current: string;
    /** Called after a successful change. */
    ondone?: () => void;
    /** Shown as a Cancel button when given (a workspace is set already). */
    oncancel?: () => void;
  }
  let { current, ondone, oncancel }: Props = $props();

  let error = $state('');
  let busy = $state(false);
  const enabled = $derived(app.isApp);
  // Only a server known not to be the app (GET /api/app answered 404) gets
  // the restart notice; while /api/app is loading or failing the button
  // just stays disabled.
  const serveOnly = $derived(app.loaded && !app.isApp);

  async function choose(path: string) {
    if (!enabled || busy) return;
    busy = true;
    error = '';
    const problem = await app.setWorkspace(path);
    busy = false;
    if (problem !== null) error = problem;
    else ondone?.();
  }
</script>

<section class="card setup" aria-labelledby="setup-title">
  <h2 id="setup-title">Choose the folder that holds your libraries</h2>
  <p class="muted">A workspace folder contains one subfolder per library; an empty folder is fine.</p>
  {#if current}
    <div class="current">
      <span class="muted">Current workspace</span>
      <span class="mono">{current}</span>
      <CopyButton text={current} />
    </div>
  {/if}
  {#if serveOnly}
    <div class="notice">
      This server has no workspace folder and cannot change it. Restart it with one: <span class="mono">picmover serve &lt;folder&gt;</span>.
    </div>
  {/if}
  <FolderPicker
    start={current}
    onchoose={choose}
    chooseLabel="Use this folder"
    countsNote=""
    rootsHint="Choose the drive or folder that holds your libraries."
    disabled={!enabled || busy}
  />
  {#if error}<div class="error-box" role="alert">{error}</div>{/if}
  {#if oncancel}
    <div class="row end"><button onclick={oncancel}>Cancel</button></div>
  {/if}
</section>

<style>
  .setup {
    display: flex;
    flex-direction: column;
    gap: 10px;
    margin-bottom: 16px;
  }
  .setup h2 {
    margin: 0;
  }
  .setup p {
    margin: 0;
  }
  .current {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    word-break: break-all;
  }
  .end {
    justify-content: flex-end;
  }
</style>
