<script lang="ts">
  // "Move to library" (docs/design/library-move.md "Frontend"): pick the
  // target among the workspace's other libraries (a closed one is opened
  // first; a busy one is shown with its holder and cannot be picked), read
  // the plan, confirm. While the move runs (moves.svelte.ts) the dialog
  // shows its progress, then the result with "Open in <target>". Closing it
  // never stops a move; the job pill brings it back.
  import { untrack } from 'svelte';
  import { errorMessage, libApi, type Lib, type TransferPlan } from '../lib/api';
  import { libraries, holderText } from '../lib/libraries.svelte';
  import { jobs } from '../lib/jobs.svelte';
  import { formatCount } from '../lib/format';
  import { moves, openHref, type MoveDialogState } from '../lib/moves.svelte';
  import { failedHeading, interruptedText, photos, planLines, reasons, refusedHeading, resultLines, spaceProblem } from '../lib/movetext';
  import { summaryFromLib } from '../lib/backupinfo';
  import Dialog from './Dialog.svelte';
  import BackupProgress from './BackupProgress.svelte';

  interface Props {
    state: MoveDialogState;
  }
  let { state: req }: Props = $props();

  const from = $derived(req.lib);
  const api = $derived(libApi(req.lib));
  const nameOf = (id: string) => libraries.find(id)?.name ?? id;

  type Step = 'pick' | 'opening' | 'planning' | 'plan';
  let step = $state<Step>('pick');
  let target = $state<string | null>(null);
  let pickError = $state('');
  let plan = $state.raw<TransferPlan | null>(null);
  let planError = $state('');
  let planProgress = $state<[number, number] | null>(null);
  let resumeError = $state('');
  let resuming = $state(false);

  /** The other libraries that can take photos: open, closed (opened first) or busy (shown, not pickable). */
  const candidates = $derived(libraries.libraries.filter((l) => l.id !== from && (l.state === 'open' || l.state === 'closed' || l.state === 'busy')));
  const targetName = $derived(target !== null ? nameOf(target) : '');
  const run = $derived(moves.run);
  const runJob = $derived(run?.jobId != null ? (jobs.jobs.find((j) => j.id === run.jobId) ?? null) : null);

  const refused = $derived(new Set(plan?.refused.map((f) => f.hash) ?? []));
  const send = $derived(plan ? req.hashes.filter((h) => !refused.has(h)) : []);
  const text = $derived(plan ? planLines(plan, targetName) : null);
  const noSpace = $derived(plan ? spaceProblem(plan, targetName) : null);
  const blocked = $derived(!!plan && plan.interrupted.length > 0);
  /** The targets of the interrupted moves, once each: "Finish moving" resumes there. */
  const resumeTargets = $derived([...new Set(plan?.interrupted.map((m) => m.to) ?? [])]);
  const backedUp = $derived.by(() => {
    const l = libraries.find(from);
    const b = l ? summaryFromLib(l) : null;
    return !!b && b.targets > 0;
  });
  const canConfirm = $derived(!!plan && send.length > 0 && !noSpace && !blocked && !jobs.active && !moves.running);

  function counts(l: Lib): string {
    return l.summary ? `${formatCount(l.summary.total)} items` : '';
  }

  async function choose(l: Lib) {
    if (l.state === 'busy') return;
    target = l.id;
    pickError = '';
    if (l.state !== 'open') {
      step = 'opening';
      await libraries.open(l.id);
      const now = libraries.find(l.id);
      if (now?.state !== 'open') {
        const why =
          now?.state === 'busy'
            ? holderText(now.holder)
            : now?.state === 'error'
              ? now.error || 'the catalog cannot be read'
              : (libraries.failures.get(l.id) ?? 'it could not be opened');
        pickError = `${l.name} cannot take photos now: ${why}.`;
        step = 'pick';
        target = null;
        return;
      }
    }
    await makePlan();
  }

  let planGen = 0;
  async function makePlan() {
    const to = target;
    if (to === null) return;
    const g = ++planGen;
    step = 'planning';
    plan = null;
    planError = '';
    planProgress = null;
    try {
      const p = await api.transferPlan(to, req.hashes, (done, total) => {
        if (g === planGen) planProgress = total > 2000 ? [done, total] : null;
      });
      if (g !== planGen) return;
      plan = p;
    } catch (e) {
      if (g === planGen) planError = errorMessage(e);
    } finally {
      if (g === planGen) step = 'plan';
    }
  }

  function back() {
    planGen++;
    step = 'pick';
    target = null;
    plan = null;
    planError = '';
  }

  function confirm() {
    const to = target;
    if (!canConfirm || to === null) return;
    void moves.start(from, to, send, req.origin);
  }

  async function finish(to: string) {
    resumeError = '';
    resuming = true;
    try {
      const r = await moves.resume(to, req.origin);
      if (r.state === 'done' && moves.dialog) {
        moves.dismissRun();
        await makePlan();
      }
    } catch (e) {
      resumeError = errorMessage(e);
    } finally {
      resuming = false;
    }
  }

  // Opened from the pill (no hashes): show the run; when it is gone, there is nothing to show.
  $effect(() => {
    if (!run && req.hashes.length === 0) untrack(() => moves.close());
  });
</script>

<Dialog title="Move to library" onclose={() => moves.close()} wide>
  <div class="move">
    {#if run}
      {@const to = nameOf(run.to)}
      {#if run.state === 'running'}
        <p class="headline">
          {#if run.resume}
            Finishing the interrupted move to {to}.
          {:else}
            Moving {photos(run.total)} to {to}.
          {/if}
          {#if run.parts > 1}<span class="muted small part">Part {run.part} of {run.parts}</span>{/if}
        </p>
        {#if runJob}
          <BackupProgress job={runJob} />
        {:else}
          <p class="muted">Starting…</p>
        {/if}
        <p class="muted small">The move goes on when you close this window.</p>
        <div class="row end">
          <button onclick={() => moves.close()}>Close</button>
        </div>
      {:else}
        <div class="result" role="status">
          {#if run.state === 'failed'}
            <div class="error-box">{run.resume ? 'Finishing the move' : 'The move'} failed: {run.error || 'unknown error'}</div>
          {:else if run.state === 'cancelled'}
            <p class="headline">The move was cancelled.</p>
          {:else if run.resume}
            <p class="headline">Finished the interrupted move to {to}.</p>
          {/if}
          {#if !run.resume || run.totals.moved + run.totals.existing + run.totals.restored > 0}
            {#each resultLines(run.totals, to) as line, i (i)}
              <p class:headline={i === 0 && run.state === 'done' && !run.resume}>{line}</p>
            {/each}
          {/if}
          {#if run.totals.failedCount > 0}
            <p class="failed-head">{failedHeading(run.totals.failedCount)}</p>
            <ul class="reasons">
              {#each reasons(run.totals.failed) as r (r.error)}
                <li><span class="reason">{r.error}</span>{#if r.count > 1}<span class="muted"> ({formatCount(r.count)})</span>{/if}</li>
              {/each}
              {#if run.totals.failedCount > run.totals.failed.length}
                <li class="muted">and {formatCount(run.totals.failedCount - run.totals.failed.length)} more; see the job log</li>
              {/if}
            </ul>
          {/if}
        </div>
        <div class="row end">
          <button onclick={() => moves.close()}>Close</button>
          {#if run.totals.moved + run.totals.existing + run.totals.restored > 0}
            <a class="btn primary" href={openHref(run)} onclick={() => moves.close()}>Open in {to}</a>
          {/if}
        </div>
      {/if}
    {:else if step === 'pick' || step === 'opening'}
      <p>Move {photos(req.hashes.length)} to:</p>
      {#if candidates.length === 0}
        <p class="muted">No other library can take photos now.</p>
      {:else}
        <ul class="targets" aria-label="Libraries">
          {#each candidates as l (l.id)}
            <li>
              <button class="target" disabled={l.state === 'busy' || step === 'opening'} onclick={() => choose(l)} aria-describedby="mv-{l.id}">
                <span class="name">{l.name}</span>
                <span class="mono path small">{l.path}</span>
                <span class="muted small" id="mv-{l.id}">
                  {#if l.state === 'busy'}
                    {holderText(l.holder)}
                  {:else if step === 'opening' && target === l.id}
                    Opening…
                  {:else}
                    {counts(l)}
                  {/if}
                </span>
              </button>
            </li>
          {/each}
        </ul>
      {/if}
      {#if pickError}<div class="error-box small" role="alert">{pickError}</div>{/if}
      <div class="row end">
        <button onclick={() => moves.close()}>Cancel</button>
      </div>
    {:else if step === 'planning'}
      <p class="muted" role="status">
        Checking {photos(req.hashes.length)} against {targetName}…{#if planProgress}&nbsp;{formatCount(planProgress[0])} / {formatCount(planProgress[1])}{/if}
      </p>
      <div class="row end">
        <button onclick={back}>Back</button>
        <button onclick={() => moves.close()}>Cancel</button>
      </div>
    {:else}
      {#if planError}
        <div class="error-box" role="alert">Cannot plan the move: {planError}</div>
        <div class="row end">
          <button onclick={back}>Back</button>
          <button onclick={makePlan}>Retry</button>
        </div>
      {:else if plan && text}
        {#if blocked}
          <div class="notice interrupted" role="alert">
            {#each plan.interrupted as m (`${m.from}/${m.to}/${m.id}`)}
              <p>{interruptedText(m, nameOf)}</p>
            {/each}
            <p>Finish it before moving more photos.</p>
            <div class="row">
              {#each resumeTargets as to (to)}
                <button class="primary small" disabled={resuming || !!jobs.active} onclick={() => finish(to)}>
                  Finish moving{resumeTargets.length > 1 ? ` to ${nameOf(to)}` : ''}
                </button>
              {/each}
            </div>
            {#if resumeError}<div class="error-box small">{resumeError}</div>{/if}
          </div>
        {/if}
        <p class="headline">{text.headline}</p>
        {#each text.lines as line (line)}
          <p>{line}</p>
        {/each}
        {#if noSpace}<div class="error-box" role="alert">{noSpace}</div>{/if}
        {#if plan.refused.length}
          <p class="failed-head">{refusedHeading(plan.refused.length)}</p>
          <ul class="reasons">
            {#each reasons(plan.refused) as r (r.error)}
              <li><span class="reason">{r.error}</span>{#if r.count > 1}<span class="muted"> ({formatCount(r.count)})</span>{/if}</li>
            {/each}
          </ul>
        {/if}
        {#if backedUp && send.length > 0}
          <p class="muted small">This library's backups keep the moved files until they are pruned.</p>
        {/if}
        {#if jobs.active && !blocked}
          <p class="muted small">A job is running; the move can start when it has finished.</p>
        {/if}
        <div class="row end">
          <button onclick={back}>Back</button>
          <button onclick={() => moves.close()}>Cancel</button>
          <button class="primary" disabled={!canConfirm} onclick={confirm}>Move</button>
        </div>
      {/if}
    {/if}
  </div>
</Dialog>

<style>
  .move {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .move p {
    margin: 0;
  }
  .headline {
    font-weight: 600;
  }
  .part {
    margin-left: 8px;
    font-weight: 400;
  }
  .targets {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .target {
    width: 100%;
    display: grid;
    grid-template-columns: 1fr;
    justify-items: start;
    gap: 2px;
    padding: 8px 12px;
    text-align: left;
  }
  .target .name {
    font-weight: 600;
  }
  .target .path {
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .reasons {
    margin: 0;
    padding-left: 20px;
  }
  .failed-head {
    color: var(--danger);
  }
  .interrupted {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .result {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
</style>
