<script lang="ts">
  // #/l/<id>/backups: the library's backup targets as cards, Add target
  // (folder picker or a typed path, UNC allowed, plus a label), and the
  // overall status line. Jobs run through jobs.svelte.ts; when one ends the
  // targets, plans and runs are loaded again.
  import { untrack } from 'svelte';
  import { errorMessage, isBackupKind } from '../lib/api';
  import { backups } from '../lib/backups.svelte';
  import { jobs } from '../lib/jobs.svelte';
  import { library } from '../lib/library.svelte';
  import { targetPathProblem } from '../lib/backupinfo';
  import { baseName } from '../lib/paths';
  import { toasts } from '../lib/toast.svelte';
  import BackupProgress from '../components/BackupProgress.svelte';
  import CopyButton from '../components/CopyButton.svelte';
  import Dialog from '../components/Dialog.svelte';
  import FolderPicker from '../components/FolderPicker.svelte';
  import TargetCard from '../components/TargetCard.svelte';

  /** How often the targets are read again while a backup-kind job runs. */
  const LIVE_RELOAD_MS = 15_000;

  interface Props {
    lib: string;
  }
  let { lib }: Props = $props();

  let checkedPlan = false;
  $effect(() => {
    const id = lib;
    untrack(() => {
      backups.select(id);
      checkedPlan = false;
      void backups.load({ runs: true }).then(() => {
        // Targets without "behind" numbers yet: ask the server to plan once.
        if (checkedPlan || backups.lib !== id) return;
        checkedPlan = true;
        if (backups.targets.some((t) => t.reachable && !t.set?.behind)) void backups.load({ plan: true });
      });
    });
  });

  // Discover jobs started elsewhere while idle; reload after any job ends.
  $effect(() => jobs.watch());
  let seenDone = untrack(() => jobs.doneCount);
  $effect(() => {
    const d = jobs.doneCount;
    if (d === seenDone) return;
    seenDone = d;
    untrack(() => void backups.refresh());
  });

  // While a backup-kind job of this library runs, the targets are read
  // again now and then: the set's counts ("On the target") come from
  // backup.db, which the server reads in one short query (it does not plan
  // while the job holds the set).
  const running = $derived(!!jobs.active && isBackupKind(jobs.active.kind) && (!jobs.active.library || jobs.active.library === lib));
  $effect(() => {
    if (!running) return;
    const t = setInterval(() => void backups.load(), LIVE_RELOAD_MS);
    return () => clearInterval(t);
  });

  const root = $derived(library.info?.root ?? '');
  const status = $derived(backups.status);
  /** A backup-kind job of this library whose target this page does not know (started elsewhere). */
  const orphan = $derived.by(() => {
    const a = jobs.active;
    if (!a || !isBackupKind(a.kind) || (a.library && a.library !== lib)) return null;
    const t = backups.targetOf(a);
    return t && backups.find(t) ? null : a;
  });

  // ---- Add target ----

  let adding = $state(false);
  let browsing = $state(false);
  let newPath = $state('');
  let newLabel = $state('');
  let addError = $state('');
  let addBusy = $state(false);
  const pathProblem = $derived(newPath.trim() ? targetPathProblem(newPath) : null);

  function openAdd() {
    newPath = '';
    newLabel = '';
    addError = '';
    browsing = false;
    adding = true;
  }

  function chosen(p: string) {
    newPath = p;
    if (!newLabel.trim()) newLabel = baseName(p);
    browsing = false;
  }

  async function add(e: SubmitEvent) {
    e.preventDefault();
    const problem = targetPathProblem(newPath);
    if (problem) {
      addError = problem;
      return;
    }
    if (!newLabel.trim()) {
      addError = 'Give the target a label, for example "Red WD drive".';
      return;
    }
    addBusy = true;
    addError = '';
    try {
      const t = await backups.add(newPath, newLabel);
      adding = false;
      toasts.show(`Added ${t.label || newLabel.trim()}`);
    } catch (err) {
      addError = errorMessage(err);
    } finally {
      addBusy = false;
    }
  }
</script>

<div class="page">
  <div class="page-inner">
    <div class="head">
      <h1>Backups</h1>
      <span class="spacer"></span>
      {#if backups.targets.length}
        <button
          disabled={backups.loading}
          onclick={() => backups.refresh()}
          title="Compare the library with each target again and refresh the numbers and the last runs. Nothing is copied.">Check now</button
        >
      {/if}
      <button class="primary" onclick={openAdd} title="Add a folder on another drive or a network share to back this library up to.">Add target</button>
    </div>

    <section class="dest" aria-label="Library being backed up">
      <div class="eyebrow">Library</div>
      {#if root}
        <div class="dest-root">
          <span class="mono path">{root}</span>
          <CopyButton text={root} />
        </div>
      {/if}
      {#if status}<p class="status tone-{status.tone}">{status.text}</p>{/if}
      <p class="muted small">
        A backup copies every file of the library, its user data (<span class="mono">user.db</span>) and a catalog snapshot to the target, verifies each
        copy and never deletes anything there; Prune removes what the library no longer has after you confirm the count.
      </p>
    </section>

    {#if backups.error}
      <div class="error-box">
        Could not load the backup targets: {backups.error}
        <button class="small" onclick={() => backups.load({ runs: true })}>Retry</button>
      </div>
    {/if}
    {#if jobs.loadError}
      <div class="error-box">
        Could not load jobs: {jobs.loadError}
        <button class="small" onclick={() => jobs.init()}>Retry</button>
      </div>
    {/if}

    {#if orphan}
      <section class="card"><BackupProgress job={orphan} budget={backups.budgetOf(orphan)} /></section>
    {/if}

    {#if !backups.loaded}
      {#if backups.loading}<p class="muted">Loading…</p>{/if}
    {:else if backups.targets.length === 0}
      <div class="empty">
        <h2>No backup targets yet</h2>
        <p>Add a folder on another drive or a network share. picmover marks it as a target, so it is recognized wherever it is mounted.</p>
        <button class="primary" onclick={openAdd} title="Add a folder on another drive or a network share to back this library up to.">Add target</button>
      </div>
    {:else}
      <ul class="targets" aria-label="Backup targets">
        {#each backups.targets as t (t.id)}
          <TargetCard target={t} />
        {/each}
      </ul>
    {/if}
  </div>
</div>

{#if adding}
  <Dialog title="Add backup target" wide onclose={() => (adding = false)}>
    {#if browsing}
      <FolderPicker
        start={newPath && !newPath.startsWith('\\\\') ? newPath : ''}
        chooseLabel="Use this folder"
        countsNote=""
        rootsHint="Choose a drive or folder for the backup: an empty folder, or one that already is a target."
        onchoose={chosen}
      />
      <div class="row end"><button type="button" onclick={() => (browsing = false)}>Back</button></div>
    {:else}
      <form class="add" onsubmit={add}>
        <label>
          <span>Folder</span>
          <span class="pathfield">
            <input type="text" class="mono" bind:value={newPath} oninput={() => (addError = '')} spellcheck="false" autocomplete="off" aria-invalid={!!pathProblem} placeholder="/media/backup, E:\Backup or \\nas\photos" />
            <button type="button" onclick={() => (browsing = true)}>Browse…</button>
          </span>
        </label>
        <p class="muted small">
          An empty folder becomes a new target; a folder that already is one is attached. It must be outside every library. Network shares can be typed.
        </p>
        <label>
          <span>Label</span>
          <input type="text" bind:value={newLabel} oninput={() => (addError = '')} maxlength="64" autocomplete="off" placeholder="Red WD drive" />
        </label>
        {#if root}<p class="muted small">Backs up <span class="mono">{root}</span>.</p>{/if}
        {#if pathProblem || addError}<div class="error-box small" role="alert">{addError || pathProblem}</div>{/if}
        <div class="row end">
          <button type="button" onclick={() => (adding = false)}>Cancel</button>
          <button type="submit" class="primary" disabled={addBusy || !newPath.trim() || !!pathProblem}>Add target</button>
        </div>
      </form>
    {/if}
  </Dialog>
{/if}

<style>
  .head {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 12px;
  }
  .head h1 {
    margin: 0;
  }
  .dest {
    margin-bottom: 16px;
  }
  .eyebrow {
    font-size: 0.75em;
    font-weight: 600;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--muted);
    margin: 0 0 4px;
  }
  .dest-root {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .path {
    word-break: break-all;
  }
  .status {
    margin: 6px 0;
    font-weight: 600;
  }
  .tone-ok {
    color: var(--ok);
  }
  .tone-warn {
    color: var(--warn);
  }
  .tone-muted {
    color: var(--muted);
  }
  .targets {
    display: flex;
    flex-direction: column;
    gap: 14px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .add {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .add label {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .pathfield {
    display: flex;
    gap: 6px;
  }
  .pathfield input {
    flex: 1;
    min-width: 0;
  }
  .end {
    justify-content: flex-end;
    margin-top: 8px;
  }
</style>
