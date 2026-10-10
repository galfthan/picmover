<script lang="ts">
  // What #/l/<id>/... shows while the library is not open: a one-line
  // "Opening peter…", or the state with its action (busy → Retry, error →
  // Rebuild, folder → Record files in place, rebuilding → progress).
  import { errorMessage } from '../lib/api';
  import { libraries, type Gate } from '../lib/libraries.svelte';
  import { jobs } from '../lib/jobs.svelte';
  import JobProgress from './JobProgress.svelte';

  interface Props {
    id: string;
    gate: Gate;
  }
  let { id, gate }: Props = $props();

  let actionError = $state('');
  let starting = $state(false);

  const name = $derived('lib' in gate && gate.lib ? gate.lib.name : 'folder' in gate && gate.folder ? gate.folder.name : id);
  const path = $derived('lib' in gate && gate.lib ? gate.lib.path : 'folder' in gate && gate.folder ? gate.folder.path : '');
  const job = $derived(jobs.active && jobs.active.library === id ? jobs.active : null);
  const workspace = $derived(libraries.mode === 'workspace');

  async function rebuild() {
    actionError = '';
    starting = true;
    try {
      await libraries.rebuild(id);
    } catch (e) {
      actionError = errorMessage(e);
    } finally {
      starting = false;
    }
  }
</script>

<div class="page">
  <div class="page-inner gate">
    {#if gate.kind === 'loading'}
      <p class="muted">Loading libraries…</p>
    {:else if gate.kind === 'failed'}
      <div class="error-box">
        Cannot reach the picmover server: {gate.message}
        <button class="small" onclick={() => libraries.load()}>Retry</button>
      </div>
    {:else if gate.kind === 'missing'}
      <h1>No library “{id}”</h1>
      <p class="muted">There is no library or folder with this name in the workspace{libraries.root ? ` ${libraries.root}` : ''}.</p>
      <a class="btn primary" href="#/">All libraries</a>
    {:else}
      <h1>{name}</h1>
      {#if path}<p class="mono muted path">{path}</p>{/if}
      {#if gate.kind === 'opening' || gate.kind === 'closed'}
        <p aria-live="polite">Opening {name}…</p>
      {:else if gate.kind === 'busy'}
        <div class="notice">
          This library is {gate.message}. Close the other program, then try again.
        </div>
        <div class="row"><button class="primary" onclick={() => libraries.open(id)}>Retry</button></div>
      {:else if gate.kind === 'openFailed'}
        <div class="error-box">Could not open the library: {gate.message}</div>
        <div class="row"><button class="primary" onclick={() => libraries.open(id)}>Retry</button></div>
      {:else if gate.kind === 'error'}
        <div class="error-box">The catalog cannot be used: {gate.message}</div>
        <p class="muted small">
          Rebuild records the files of the folder in a new catalog (the old one is kept as a backup). Ratings, tags, albums and
          the trash are kept: they live in user.db.
        </p>
        <div class="row"><button class="primary" disabled={starting || !!jobs.active} onclick={rebuild}>Rebuild</button></div>
      {:else if gate.kind === 'folder'}
        <p>This folder is not a picmover library yet.</p>
        <p class="muted small">
          Record files in place adds every photo and video under it to a new catalog without moving or copying anything.
        </p>
        <div class="row">
          <button class="primary" disabled={starting || !!jobs.active} onclick={rebuild}>Record files in place</button>
        </div>
      {:else if gate.kind === 'rebuilding'}
        <p aria-live="polite">Rebuilding the catalog; the library opens when it has finished.</p>
        {#if job}
          <section class="card"><JobProgress {job} /></section>
        {/if}
      {/if}
      {#if jobs.active && jobs.active.library !== id && (gate.kind === 'error' || gate.kind === 'folder')}
        <p class="muted small">Another job is running; start when it has finished.</p>
      {/if}
      {#if actionError}<div class="error-box">{actionError}</div>{/if}
      {#if workspace}
        <p><a href="#/">All libraries</a></p>
      {/if}
    {/if}
  </div>
</div>

<style>
  .gate {
    max-width: 720px;
  }
  .path {
    word-break: break-all;
    margin-top: -0.4em;
  }
</style>
