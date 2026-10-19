<script lang="ts">
  // One backup target of the library: label, path (editable, Browse or a
  // typed path, UNC allowed), reachable state, last backup and scrub, the
  // set's size and the "behind" headline; Back up now, Scrub with a budget,
  // Prune (the plan's extras, confirmed by typing the count), Restore
  // missing (the files that would come back first), Forget; inline progress
  // while one of its jobs runs; the problems of its last runs. A target the
  // server reports an `error` for (its set belongs to another library) shows
  // the error and offers only Edit and Forget, plus Take over when the set
  // was last written by another copy of this library (`foreign`).
  import { tick } from 'svelte';
  import { ApiError, errorMessage, type BackupPlan, type BackupTarget, type Job } from '../lib/api';
  import { backups } from '../lib/backups.svelte';
  import { jobs, isActive } from '../lib/jobs.svelte';
  import { formatBytes, formatCount, formatInstant, parseWhen } from '../lib/format';
  import { backupStatusText, outcome } from '../lib/jobtext';
  import {
    agoText,
    behindCount,
    behindText,
    incompleteFiles,
    incompleteText,
    liveLeft,
    problemCount,
    problemTotal,
    runProblems,
    TRUNCATED_NOTE,
    SCRUB_BUDGETS,
    stoppedText,
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

  /** The running job of this target in this library (another library's job on the same drive is not ours). */
  const job = $derived(jobs.active && backups.isFor(jobs.active, id) ? jobs.active : null);
  /** A backup of this target runs: the headline counts down from its progress, not the plan made before it. */
  const backingUp = $derived(!!job && job.kind === 'backup');
  const live = $derived(backingUp ? liveLeft(job?.phase) : null);
  const headline = $derived(behindText(target, live));
  const headTone = $derived(!target.reachable || target.error ? 'warn' : live ? 'muted' : behind === null ? 'muted' : behind === 0 ? 'ok' : 'warn');
  /** Files on the target from a backup that never finished ("Incomplete: …"). */
  const incomplete = $derived(incompleteText(target, backingUp));
  const neverFinished = $derived(incompleteFiles(target) > 0);
  /** The newest finished job of this target and library seen by this server process. */
  const lastJob = $derived<Job | null>(jobs.jobs.find((j) => !isActive(j) && backups.isFor(j, id)) ?? null);
  const lastOutcome = $derived(lastJob ? outcome(lastJob) : null);
  const busy = $derived(!!jobs.active);
  const canRun = $derived(target.reachable && !target.error && !busy);
  /** The set belongs to another copy of this library: it can be taken over by a backup. */
  const canTakeOver = $derived(target.reachable && !!target.foreign && !!target.error);
  /** What the live region says: the running job's phase, then its outcome. */
  const announce = $derived(job ? backupStatusText(job) : (lastOutcome?.text ?? ''));

  const problems = $derived(runProblems(backups.runs[id] ?? []));
  /** Why the newest backup stopped before it was done (from this server's job, user.db or backup.db); null when it finished. */
  const stopped = $derived(job ? null : stoppedText(target, backups.runs[id] ?? [], lastJob));
  const nProblems = $derived(problemCount(problems, stopped));
  const nListed = $derived(problemTotal(problems, stopped));

  // ---- Focus: buttons that disappear must not leave focus on <body> ----

  let cardEl: HTMLElement | undefined = $state();
  let editBtn: HTMLButtonElement | undefined = $state();
  let backupBtn: HTMLButtonElement | undefined = $state();
  let labelInput: HTMLInputElement | undefined = $state();

  const focusLost = () => !document.activeElement || document.activeElement === document.body || !document.activeElement.isConnected;

  /** After a job starts: focus its Cancel button (the actions row is gone). */
  async function focusProgress() {
    await tick();
    // Focus that moved elsewhere on purpose stays there.
    if (!focusLost() && !cardEl?.contains(document.activeElement)) return;
    cardEl?.querySelector<HTMLButtonElement>('.bprogress button')?.focus();
  }

  // When the job ends its progress block (and Cancel) goes away: focus Back up now.
  let hadJob = false;
  $effect(() => {
    const has = !!job;
    if (hadJob && !has) {
      void tick().then(() => {
        if (focusLost()) backupBtn?.focus();
      });
    }
    hadJob = has;
  });

  let starting = $state(false);
  let actionError = $state('');

  async function run(fn: () => Promise<unknown>, after?: () => Promise<void>) {
    if (starting) return;
    starting = true;
    actionError = '';
    try {
      await fn();
      if (after) await after();
    } catch (e) {
      actionError = errorMessage(e);
    } finally {
      starting = false;
    }
  }

  const backupNow = () => run(() => backups.start({ kind: 'backup', target: id }), focusProgress);

  let budget = $state('900');
  function scrub() {
    const b = SCRUB_BUDGETS.find((x) => String(x.seconds) === budget);
    const seconds = b ? b.seconds : 900;
    void run(() => backups.start(seconds === null ? { kind: 'scrub', target: id, repair: true } : { kind: 'scrub', target: id, budget: seconds, repair: true }), focusProgress);
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

  async function startEdit() {
    editLabel = target.label;
    editPath = target.path;
    editError = '';
    editing = true;
    await tick();
    labelInput?.focus();
  }

  async function endEdit() {
    editing = false;
    await tick();
    if (focusLost()) editBtn?.focus();
  }

  /** The directory browser cannot open network shares: start it at the roots then. */
  const browseStart = (p: string) => (/^[\\/]{2}/.test(p.trim()) ? '' : p);

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
      await endEdit();
      return;
    }
    saving = true;
    editError = '';
    try {
      await backups.update(id, patch);
      await endEdit();
    } catch (err) {
      editError = errorMessage(err);
    } finally {
      saving = false;
    }
  }

  // ---- Prune and restore dialogs (both show the plan's extras) ----

  let dialog = $state<'prune' | 'restore' | 'takeover' | null>(null);
  let plan = $state<BackupPlan | null>(null);
  let planError = $state('');
  let planLoading = $state(false);
  let typed = $state('');
  let dialogError = $state('');
  let dialogBusy = $state(false);
  const extra = $derived(plan?.extra ?? null);
  /** What a restore copies back: the plan's `restore` class; null when the server does not send one. */
  const restoreClass = $derived(plan?.restore ?? null);
  let typedInput: HTMLInputElement | undefined = $state();
  /** The typed count without digit grouping ("1,204", "1 204" and "1204" all match 1204). */
  const typedCount = $derived(typed.replace(/[\s,.'_\u00a0\u202f]/g, ''));
  const typedOk = $derived(!!extra && typedCount === String(extra.count));

  /** Focus the typed-count field once the plan is shown. */
  async function focusTyped() {
    await tick();
    typedInput?.focus();
  }

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
    if (kind === 'prune' && plan) await focusTyped();
  }

  async function confirmPrune(e: SubmitEvent) {
    e.preventDefault();
    if (!extra || !typedOk || dialogBusy) return;
    dialogBusy = true;
    dialogError = '';
    try {
      await backups.start({ kind: 'prune', target: id, confirm: true, expected: extra.count });
      dialog = null;
      await focusProgress();
    } catch (err) {
      dialogError = errorMessage(err);
      // The files to remove changed: show the new list and ask for its count.
      if (err instanceof ApiError && err.status === 409) {
        typed = '';
        try {
          plan = await backups.plan(id, true);
          await focusTyped();
        } catch (e) {
          planError = errorMessage(e);
        }
      }
    } finally {
      dialogBusy = false;
    }
  }

  async function confirmTakeOver() {
    if (dialogBusy) return;
    dialogBusy = true;
    dialogError = '';
    try {
      await backups.start({ kind: 'backup', target: id, takeOver: true });
      dialog = null;
      await focusProgress();
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
      await focusProgress();
    } catch (err) {
      dialogError = errorMessage(err);
    } finally {
      dialogBusy = false;
    }
  }

  const SHOWN = 100;
  const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);
  const files = (k: number) => `${formatCount(k)} ${k === 1 ? 'file' : 'files'}`;
  const when = (v: number | string | null) => {
    const d = parseWhen(v);
    return d ? formatInstant(d.toISOString()) : '';
  };
</script>

{#snippet list(title: string, p: ProblemList, cls: string)}
  {#if p.count > 0}
    <div class="plist {cls}">
      <h4>{title} <span class="muted">({formatCount(p.count)}{p.open < p.count ? `, ${formatCount(p.count - p.open)} repaired` : ''})</span></h4>
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
      {#if p.truncated}<p class="muted small truncated">The run's {TRUNCATED_NOTE}; the run log has every file.</p>{/if}
    </div>
  {/if}
{/snippet}

{#snippet paths(list: string[], count: number)}
  <ul class="paths mono small">
    {#each list as p, i (i)}<li>{p}</li>{/each}
  </ul>
  {#if count > list.length}<p class="muted small more">and {formatCount(count - list.length)} more</p>{/if}
{/snippet}

<li class="card target" data-target={id} aria-label="Backup target {target.label}" bind:this={cardEl}>
  <div class="top">
    <h2 class="label">{target.label || 'Unnamed target'}</h2>
    <span class="badge" class:ok={target.reachable} class:bad={!target.reachable}>{target.reachable ? 'Connected' : 'Not found'}</span>
    <span class="spacer"></span>
    {#if !editing}<button class="small" bind:this={editBtn} onclick={startEdit} title="Rename the target, or point it at the folder where the drive or share is mounted now.">Edit</button>{/if}
  </div>

  {#if editing}
    <form class="edit" onsubmit={saveEdit}>
      <label>
        <span>Label</span>
        <input type="text" bind:this={labelInput} bind:value={editLabel} maxlength="64" autocomplete="off" />
      </label>
      <label>
        <span>Folder</span>
        <span class="pathfield">
          <input type="text" class="mono" bind:value={editPath} oninput={() => (editError = '')} spellcheck="false" autocomplete="off" aria-invalid={!!pathProblem} aria-label="Target folder" />
          <button type="button" onclick={() => (browsing = true)}>Browse…</button>
        </span>
      </label>
      <p class="muted small">Point at the drive or share where this target is now; it must hold the target's marker file. Network shares can be typed (\\server\share).</p>
      {#if pathProblem || editError}<div class="error-box small" role="alert">{editError || pathProblem}</div>{/if}
      <div class="row end">
        <button type="button" onclick={endEdit}>Cancel</button>
        <button type="submit" class="primary" disabled={saving || !!pathProblem}>Save</button>
      </div>
    </form>
  {:else}
    <div class="path mono" title={target.path}>{target.path}</div>
  {/if}

  <p class="headline tone-{headTone}">{headline}</p>
  {#if incomplete}<p class="incomplete small tone-warn">{incomplete}</p>{/if}
  {#if target.busy && !job}<p class="muted small busy">{capitalize(target.busy)}; the numbers are updated when it has finished.</p>{/if}

  <dl class="facts small">
    <dt title="When a backup to this target last finished.">Last backup</dt>
    <dd title={when(target.lastBackupAt)}>{neverFinished && !target.lastBackupAt ? 'never finished' : agoText(target.lastBackupAt)}</dd>
    <dt title="When a scrub last re-read the backup on this target.">Last scrub</dt>
    <dd title={when(target.lastScrubAt)}>{agoText(target.lastScrubAt)}</dd>
    {#if set}
      <dt title="The files of this library in the backup on the target, as the target's backup.db records them{backingUp ? '; updated while the backup runs' : ''}.">On the target</dt>
      <dd>{files(set.files)}, {formatBytes(set.bytes)}</dd>
      {#if set.unverified}
        <dt title="Every copy was checked by reading it back when it was written. These files have not been re-read by a scrub since; Scrub checks them for damage on the drive.">Not scrubbed yet</dt>
        <dd>{files(set.unverified)}</dd>
      {/if}
      {#if set.damaged}
        <dt>Damaged</dt>
        <dd class="bad">{files(set.damaged)}</dd>
      {/if}
      {#if set.extras}
        <dt>Not in the library</dt>
        <dd>{files(set.extras)}</dd>
      {/if}
    {/if}
  </dl>

  <p class="sr-only" aria-live="polite">{announce}</p>
  {#if job}
    <div class="progress"><BackupProgress {job} budget={backups.budgetOf(job)} live={false} /></div>
  {:else}
    <div class="row actions">
      <button
        class="primary"
        bind:this={backupBtn}
        disabled={!canRun || starting}
        onclick={backupNow}
        title="Copy new and changed files from the library to this target and check each copy by reading it back. Nothing on the target is deleted; replaced versions are kept in its .picmover/previous folder.">Back up now</button
      >
      <span class="scrub">
        <select
          bind:value={budget}
          aria-label="Scrub budget"
          disabled={!canRun || starting}
          title="How long the scrub may read. A scrub that runs out of time continues where it stopped the next time; All reads the whole backup.">
          {#each SCRUB_BUDGETS as b (b.label)}<option value={String(b.seconds)}>{b.label}</option>{/each}
        </select>
        <button
          disabled={!canRun || starting}
          onclick={scrub}
          title="Re-read the backup and compare each file with its checksum, to find damaged or missing copies. Those are copied again from the library when it still has them.">Scrub</button
        >
      </span>
      <button
        disabled={!canRun || starting}
        onclick={() => openDialog('prune')}
        title="List the files on the target that the library no longer has, and earlier versions kept for more than 30 days. Nothing is deleted until you confirm the count.">Prune…</button
      >
      <button
        disabled={!canRun || starting}
        onclick={() => openDialog('restore')}
        title="List the files of this backup that are missing from the library, and copy them back after you confirm. Files in the library are never overwritten.">Restore missing…</button
      >
      {#if canTakeOver}
        <button
          title="Make this library the writer of this backup set and back up now."
          disabled={busy || starting}
          onclick={() => {
            dialogError = '';
            dialog = 'takeover';
          }}>Take over…</button
        >
      {/if}
      <span class="spacer"></span>
      <ConfirmButton
        label="Forget"
        title="Remove this target from the library's list. Nothing on the drive is deleted; add the same folder again to continue."
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
    {:else if target.error}
      <p class="muted small">This library cannot use the target as it is: Edit the folder, or Forget the target (the files on it are not touched).</p>
    {:else if busy}
      <p class="muted small">Another job is running; wait until it has finished.</p>
    {/if}
  {/if}
  {#if actionError}<div class="error-box small" role="alert">{actionError}</div>{/if}
  {#if lastOutcome && !job}<p class="outcome small tone-{lastOutcome.tone}">{lastOutcome.text}</p>{/if}

  <details class="problems" open={nProblems > 0}>
    <summary>
      {#if nProblems > 0}Problems <span class="count">{formatCount(nProblems)}</span>{:else if nListed > 0}No open problems in the last runs{:else}No problems in the last runs{/if}
    </summary>
    {#if stopped}
      <div class="plist stopped">
        <h4>The last backup did not finish</h4>
        <p class="small">{stopped}</p>
      </div>
    {/if}
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
      start={browseStart(editPath)}
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
          <span>Type <strong>{formatCount(extra.count)}</strong> to confirm</span>
          <input type="text" inputmode="numeric" bind:this={typedInput} bind:value={typed} autocomplete="off" />
        </label>
        {#if dialogError}<div class="error-box small" role="alert">{dialogError}</div>{/if}
        <div class="row end">
          <button type="button" onclick={() => (dialog = null)}>Cancel</button>
          <button type="submit" class="danger" disabled={!typedOk || dialogBusy || busy}>Prune {files(extra.count)}</button>
        </div>
      </form>
    {/if}
  </Dialog>
{/if}

{#if dialog === 'takeover'}
  <Dialog title="Take over the backup on {target.label}" wide onclose={() => (dialog = null)}>
    <p>{target.error}</p>
    <p>
      If you take it over, this library becomes the writer of that backup set: a backup starts now and brings the set in line with this library, and
      the other copy's backups there are treated as this library's from then on. Back up the other copy to a different target (or set) if you keep using
      it.
    </p>
    {#if dialogError}<div class="error-box small" role="alert">{dialogError}</div>{/if}
    <div class="row end">
      <button type="button" onclick={() => (dialog = null)}>Cancel</button>
      <button class="primary" disabled={dialogBusy || busy} onclick={confirmTakeOver}>Take over and back up</button>
    </div>
  </Dialog>
{/if}

{#if dialog === 'restore'}
  <Dialog title="Restore missing files from {target.label}" wide onclose={() => (dialog = null)}>
    {#if planLoading}
      <p class="muted">Comparing the target with the library…</p>
    {:else if planError}
      <div class="error-box small" role="alert">{planError}</div>
      <div class="row end"><button onclick={() => (dialog = null)}>Close</button></div>
    {:else if restoreClass && restoreClass.count === 0}
      <p>Nothing is missing: every file of the backup is in the library.</p>
      <div class="row end"><button onclick={() => (dialog = null)}>Close</button></div>
    {:else if plan}
      {#if restoreClass}
        <p>
          {files(restoreClass.count)}{restoreClass.bytes ? ` (${formatBytes(restoreClass.bytes)})` : ''} of the backup {restoreClass.count === 1 ? 'is' : 'are'} missing
          from the library and would be copied back. Files in the library are never overwritten.
        </p>
        {@render paths(restoreClass.paths, restoreClass.count)}
      {:else}
        <p>
          The files of the backup that are missing from the library are copied back. Files in the library are never overwritten; the job's summary
          says how many were restored.
        </p>
      {/if}
      {#if dialogError}<div class="error-box small" role="alert">{dialogError}</div>{/if}
      <div class="row end">
        <button type="button" onclick={() => (dialog = null)}>Cancel</button>
        <button class="primary" disabled={dialogBusy || busy} onclick={confirmRestore}>{restoreClass ? `Restore ${files(restoreClass.count)}` : 'Restore missing files'}</button>
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
