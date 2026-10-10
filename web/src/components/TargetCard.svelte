<script lang="ts">
  // One backup target of the library: label, path (editable, Browse or a
  // typed path, UNC allowed), reachable state, last backup and scrub, the
  // set's size and the "behind" headline; Back up now, Scrub with a budget,
  // Prune (the plan's extras, confirmed by typing the count), Restore
  // missing (the files that would come back first), Forget; inline progress
  // while one of its jobs runs; the problems of its last runs.
  import { errorMessage, isBackupKind, type BackupPlan, type BackupTarget, type Job } from '../lib/api';
  import { backups } from '../lib/backups.svelte';
  import { jobs, isActive } from '../lib/jobs.svelte';
  import { formatBytes, formatCount, formatInstant, parseWhen } from '../lib/format';
  import { outcome } from '../lib/jobtext';
  import {
    agoText,
    behindCount,
    behindText,
    problemCount,
    runProblems,
    SCRUB_BUDGETS,
    targetPathProblem,
    type ProblemList,
  } from '../lib/backupinfo';
  import { toasts } from '../lib/toast.svelte';
  import BackupProgress from './BackupProgress.svelte';
  import ConfirmButton from './ConfirmButton.svelte';
  import Dialog from './Dialog.svelte';
  import FolderPicker from './FolderPicker.svelte';

  interface Props {
    target: BackupTarget;
  }
  let { target }: Props = $props();

  const id = $derived(target.id);
  const set = $derived(target.set);
  const behind = $derived(behindCount(set?.behind));
  const headline = $derived(behindText(target));
  const headTone = $derived(!target.reachable ? 'warn' : behind === null ? 'muted' : behind === 0 ? 'ok' : 'warn');

  /** The running job of this target. */
  const job = $derived(jobs.active && isBackupKind(jobs.active.kind) && backups.targetOf(jobs.active) === id ? jobs.active : null);
  /** The newest finished job of this target seen by this server process. */
  const lastJob = $derived<Job | null>(jobs.jobs.find((j) => isBackupKind(j.kind) && !isActive(j) && backups.targetOf(j) === id) ?? null);
  const lastOutcome = $derived(lastJob ? outcome(lastJob) : null);
  const busy = $derived(!!jobs.active);
  const canRun = $derived(target.reachable && !busy);

  const problems = $derived(runProblems(backups.runs[id] ?? []));
  const nProblems = $derived(problemCount(problems));

  let starting = $state(false);
  let actionError = $state('');

  async function run(fn: () => Promise<unknown>) {
    if (starting) return;
    starting = true;
    actionError = '';
    try {
      await fn();
    } catch (e) {
      actionError = errorMessage(e);
    } finally {
      starting = false;
    }
  }

  const backupNow = () => run(() => backups.start({ kind: 'backup', target: id }));

  let budget = $state('900');
  function scrub() {
    const b = SCRUB_BUDGETS.find((x) => String(x.seconds) === budget);
    const seconds = b ? b.seconds : 900;
    void run(() => backups.start(seconds === null ? { kind: 'scrub', target: id, repair: true } : { kind: 'scrub', target: id, budget: seconds, repair: true }));
  }

  function forget() {
    void run(async () => {
      await backups.forget(id);
      toasts.show(`Forgot ${target.label || 'the target'}; the files on it were not touched`);
    });
  }

  // ---- Edit (label and path) ----

  let editing = $state(false);
  let editLabel = $state('');
  let editPath = $state('');
  let editError = $state('');
  let saving = $state(false);
  let browsing = $state(false);
  const pathProblem = $derived(editPath.trim() ? targetPathProblem(editPath) : null);

  function startEdit() {
    editLabel = target.label;
    editPath = target.path;
    editError = '';
    editing = true;
  }

  async function saveEdit(e: SubmitEvent) {
    e.preventDefault();
    const problem = targetPathProblem(editPath);
    if (problem) {
      editError = problem;
      return;
    }
    const patch: { label?: string; path?: string } = {};
    if (editLabel.trim() && editLabel.trim() !== target.label) patch.label = editLabel.trim();
    if (editPath.trim() !== target.path) patch.path = editPath.trim();
    if (!patch.label && patch.path === undefined) {
      editing = false;
      return;
    }
    saving = true;
    editError = '';
    try {
      await backups.update(id, patch);
      editing = false;
    } catch (err) {
      editError = errorMessage(err);
    } finally {
      saving = false;
    }
  }

  // ---- Prune and restore dialogs (both show the plan's extras) ----

  let dialog = $state<'prune' | 'restore' | null>(null);
  let plan = $state<BackupPlan | null>(null);
  let planError = $state('');
  let planLoading = $state(false);
  let typed = $state('');
  let dialogError = $state('');
  let dialogBusy = $state(false);
  const extra = $derived(plan?.extra ?? null);

  async function openDialog(kind: 'prune' | 'restore') {
    dialog = kind;
    plan = null;
    planError = '';
    typed = '';
    dialogError = '';
    planLoading = true;
    try {
      // Prune must send the count of the plan the user saw: always a fresh one.
      plan = await backups.plan(id, true);
    } catch (e) {
      planError = errorMessage(e);
    } finally {
      planLoading = false;
    }
  }

  async function confirmPrune(e: SubmitEvent) {
    e.preventDefault();
    if (!extra || typed.trim() !== String(extra.count) || dialogBusy) return;
    dialogBusy = true;
    dialogError = '';
    try {
      await backups.start({ kind: 'prune', target: id, confirm: true, expected: extra.count });
      dialog = null;
    } catch (err) {
      dialogError = errorMessage(err);
    } finally {
      dialogBusy = false;
    }
  }

  async function confirmRestore() {
    if (dialogBusy) return;
    dialogBusy = true;
    dialogError = '';
    try {
      await backups.start({ kind: 'restore', target: id, mode: 'missing' });
      dialog = null;
    } catch (err) {
      dialogError = errorMessage(err);
    } finally {
      dialogBusy = false;
    }
  }

  const SHOWN = 100;
  const when = (v: number | string | null) => {
    const d = parseWhen(v);
    return d ? formatInstant(d.toISOString()) : '';
  };
</script>

{#snippet list(title: string, p: ProblemList, cls: string)}
  {#if p.count > 0}
    <div class="plist {cls}">
      <h4>{title} <span class="muted">({formatCount(p.count)})</span></h4>
      {#if p.items.length}
        <ul>
          {#each p.items.slice(0, SHOWN) as it, i (i)}
            <li>
              <span class="mono">{it.path}</span>
              {#if it.repaired === true}<span class="tag ok">repaired</span>{:else if it.repaired === false}<span class="tag bad">not repaired</span>{/if}
              {#if it.detail}<span class="muted"> — {it.detail}</span>{/if}
            </li>
          {/each}
        </ul>
        {#if p.count > Math.min(p.items.length, SHOWN)}<p class="muted small">and {formatCount(p.count - Math.min(p.items.length, SHOWN))} more; see the run log.</p>{/if}
      {/if}
    </div>
  {/if}
{/snippet}

{#snippet paths(list: string[], count: number)}
  <ul class="paths mono small">
    {#each list as p, i (i)}<li>{p}</li>{/each}
  </ul>
  {#if count > list.length}<p class="muted small more">and {formatCount(count - list.length)} more</p>{/if}
{/snippet}

<li class="card target" data-target={id} aria-label="Backup target {target.label}">
  <div class="top">
    <h2 class="label">{target.label || 'Unnamed target'}</h2>
    <span class="badge" class:ok={target.reachable} class:bad={!target.reachable}>{target.reachable ? 'Connected' : 'Not found'}</span>
    <span class="spacer"></span>
    {#if !editing}<button class="small" onclick={startEdit}>Edit</button>{/if}
  </div>

  {#if editing}
    <form class="edit" onsubmit={saveEdit}>
      <label>
        <span>Label</span>
        <input type="text" bind:value={editLabel} maxlength="200" autocomplete="off" />
      </label>
      <label>
        <span>Folder</span>
        <span class="pathfield">
          <input type="text" class="mono" bind:value={editPath} spellcheck="false" autocomplete="off" aria-invalid={!!pathProblem} aria-label="Target folder" />
          <button type="button" onclick={() => (browsing = true)}>Browse…</button>
        </span>
      </label>
      <p class="muted small">Point at the drive or share where this target is now; it must hold the target's marker file. Network shares can be typed (\\server\share).</p>
      {#if pathProblem || editError}<div class="error-box small" role="alert">{editError || pathProblem}</div>{/if}
      <div class="row end">
        <button type="button" onclick={() => (editing = false)}>Cancel</button>
        <button type="submit" class="primary" disabled={saving || !!pathProblem}>Save</button>
      </div>
    </form>
  {:else}
    <div class="path mono" title={target.path}>{target.path}</div>
  {/if}

  <p class="headline tone-{headTone}">{headline}</p>

  <dl class="facts small">
    <dt>Last backup</dt>
    <dd title={when(target.lastBackupAt)}>{agoText(target.lastBackupAt)}</dd>
    <dt>Last scrub</dt>
    <dd title={when(target.lastScrubAt)}>{agoText(target.lastScrubAt)}</dd>
    {#if set}
      <dt>On the target</dt>
      <dd>{formatCount(set.files)} files, {formatBytes(set.bytes)}</dd>
      {#if set.unverified}<dt>Not verified yet</dt><dd>{formatCount(set.unverified)} files</dd>{/if}
      {#if set.damaged}<dt>Damaged</dt><dd class="bad">{formatCount(set.damaged)} files</dd>{/if}
      {#if set.extras}<dt>Not in the library</dt><dd>{formatCount(set.extras)} files</dd>{/if}
    {/if}
  </dl>

  {#if job}
    <div class="progress"><BackupProgress {job} /></div>
  {:else}
    <div class="row actions">
      <button class="primary" disabled={!canRun || starting} onclick={backupNow}>Back up now</button>
      <span class="scrub">
        <select bind:value={budget} aria-label="Scrub budget" disabled={!canRun || starting}>
          {#each SCRUB_BUDGETS as b (b.label)}<option value={String(b.seconds)}>{b.label}</option>{/each}
        </select>
        <button disabled={!canRun || starting} onclick={scrub}>Scrub</button>
      </span>
      <button disabled={!canRun || starting} onclick={() => openDialog('prune')}>Prune…</button>
      <button disabled={!canRun || starting} onclick={() => openDialog('restore')}>Restore missing…</button>
      <span class="spacer"></span>
      <ConfirmButton
        label="Forget"
        question="Forget this target? The files on it are not touched."
        confirmLabel="Forget"
        cancelLabel="Keep"
        danger
        disabled={starting || !!job}
        onconfirm={forget}
      />
    </div>
    {#if !target.reachable}
      <p class="muted small">Connect the drive or share, or Edit the folder if it is mounted somewhere else now.</p>
    {:else if busy}
      <p class="muted small">Another job is running; wait until it has finished.</p>
    {/if}
  {/if}
  {#if actionError}<div class="error-box small" role="alert">{actionError}</div>{/if}
  {#if lastOutcome && !job}<p class="outcome small tone-{lastOutcome.tone}">{lastOutcome.text}</p>{/if}

  <details class="problems" open={nProblems > 0}>
    <summary>
      {#if nProblems > 0}Problems <span class="count">{formatCount(nProblems)}</span>{:else}No problems in the last runs{/if}
    </summary>
    {@render list('Damaged files in the last scrub', problems.damaged, 'damaged')}
    {@render list('Copy failures in the last backup', problems.failed, 'failed')}
    {@render list('Unknown files on the target', problems.unknown, 'unknown')}
    {@render list('Missing on the target', problems.missing, 'missing')}
    {#if problems.scrub}<p class="muted small">Last scrub {agoText(problems.scrub.finishedAt ?? problems.scrub.startedAt)}.</p>{/if}
  </details>
</li>

{#if browsing}
  <Dialog title="Choose the target folder" wide onclose={() => (browsing = false)}>
    <FolderPicker
      start={editPath}
      chooseLabel="Use this folder"
      countsNote=""
      rootsHint="Choose the drive or folder that holds the backup."
      onchoose={(p) => {
        editPath = p;
        browsing = false;
      }}
    />
    <div class="row end"><button onclick={() => (browsing = false)}>Cancel</button></div>
  </Dialog>
{/if}

{#if dialog === 'prune'}
  <Dialog title="Prune {target.label}" wide onclose={() => (dialog = null)}>
    {#if planLoading}
      <p class="muted">Comparing the target with the library…</p>
    {:else if planError}
      <div class="error-box small" role="alert">{planError}</div>
      <div class="row end"><button onclick={() => (dialog = null)}>Close</button></div>
    {:else if extra && extra.count === 0}
      <p>Nothing to prune: everything on the target is still in the library.</p>
      <div class="row end"><button onclick={() => (dialog = null)}>Close</button></div>
    {:else if extra}
      <form onsubmit={confirmPrune}>
        <p>
          {formatCount(extra.count)}
          {extra.count === 1 ? 'file' : 'files'}{extra.bytes ? ` (${formatBytes(extra.bytes)})` : ''} on the target {extra.count === 1 ? 'is' : 'are'} no longer
          in the library. Pruning deletes {extra.count === 1 ? 'it' : 'them'} from the target, together with earlier versions kept for more than 30 days.
          Nothing in the library is touched.
        </p>
        {@render paths(extra.paths, extra.count)}
        <label class="field">
          <span>Type <strong>{extra.count}</strong> to confirm</span>
          <input type="text" inputmode="numeric" bind:value={typed} autocomplete="off" />
        </label>
        {#if dialogError}<div class="error-box small" role="alert">{dialogError}</div>{/if}
        <div class="row end">
          <button type="button" onclick={() => (dialog = null)}>Cancel</button>
          <button type="submit" class="danger" disabled={typed.trim() !== String(extra.count) || dialogBusy || busy}>Prune {formatCount(extra.count)} files</button>
        </div>
      </form>
    {/if}
  </Dialog>
{/if}

{#if dialog === 'restore'}
  <Dialog title="Restore missing files from {target.label}" wide onclose={() => (dialog = null)}>
    {#if planLoading}
      <p class="muted">Comparing the target with the library…</p>
    {:else if planError}
      <div class="error-box small" role="alert">{planError}</div>
      <div class="row end"><button onclick={() => (dialog = null)}>Close</button></div>
    {:else if extra && extra.count === 0}
      <p>Nothing is missing: every file on the target is in the library.</p>
      <div class="row end"><button onclick={() => (dialog = null)}>Close</button></div>
    {:else if extra}
      <p>
        {formatCount(extra.count)}
        {extra.count === 1 ? 'file' : 'files'}{extra.bytes ? ` (${formatBytes(extra.bytes)})` : ''} on the target {extra.count === 1 ? 'is' : 'are'} missing
        from the library and would be copied back. Files in the library are never overwritten.
      </p>
      {@render paths(extra.paths, extra.count)}
      {#if dialogError}<div class="error-box small" role="alert">{dialogError}</div>{/if}
      <div class="row end">
        <button type="button" onclick={() => (dialog = null)}>Cancel</button>
        <button class="primary" disabled={dialogBusy || busy} onclick={confirmRestore}>Restore {formatCount(extra.count)} files</button>
      </div>
    {/if}
  </Dialog>
{/if}

<style>
  .target {
    display: flex;
    flex-direction: column;
    gap: 10px;
    margin: 0;
  }
  .top {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .label {
    margin: 0;
  }
  .badge {
    padding: 1px 8px;
    border-radius: 999px;
    background: var(--surface-3);
    font-size: 0.82em;
  }
  .badge.ok {
    color: var(--ok);
  }
  .badge.bad,
  .bad {
    color: var(--danger);
  }
  .path {
    color: var(--muted);
    word-break: break-all;
  }
  .headline {
    margin: 0;
    font-size: 1.15em;
    font-weight: 600;
  }
  .tone-ok {
    color: var(--ok);
  }
  .tone-warn {
    color: var(--warn);
  }
  .tone-error {
    color: var(--danger);
  }
  .tone-muted {
    color: var(--muted);
  }
  .facts {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    gap: 2px 14px;
    margin: 0;
  }
  .facts dt {
    color: var(--muted);
  }
  .facts dd {
    margin: 0;
  }
  .actions {
    flex-wrap: wrap;
    gap: 8px;
  }
  .scrub {
    display: inline-flex;
    gap: 4px;
  }
  .outcome {
    margin: 0;
  }
  .edit {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .edit label,
  .field {
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
  .problems summary {
    cursor: pointer;
    color: var(--muted);
  }
  .problems .count {
    padding: 0 6px;
    border-radius: 999px;
    background: var(--danger-bg);
    color: var(--danger);
  }
  .plist h4 {
    margin: 10px 0 4px;
    font-size: 0.95em;
  }
  .plist ul,
  .paths {
    margin: 0;
    padding-left: 18px;
    max-height: 220px;
    overflow: auto;
    word-break: break-all;
  }
  .tag {
    margin-left: 6px;
    padding: 0 6px;
    border-radius: 999px;
    font-size: 0.8em;
    background: var(--surface-3);
  }
  .tag.ok {
    color: var(--ok);
  }
  .tag.bad {
    color: var(--danger);
  }
  .more {
    margin: 4px 0 10px;
  }
</style>
