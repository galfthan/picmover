// Moving photos to another library of the workspace (docs/design/
// library-move.md "Frontend"). The move dialog is shown by the shell from
// `moves.dialog`; a run lives here, not in the dialog, so it goes on when
// the dialog is closed. A run is one `transfer` job per TRANSFER_CHUNK
// hashes, started one after another (one job runs at a time). After each
// job the moved items leave the source's pages (change `moved`); after
// the run the selection, the counts and the libraries list are refreshed.

import { chunks, errorMessage, resumeTransfer, toTransferResult, TRANSFER_CHUNK, type Job } from './api';
import { emitChange } from './changes';
import { jobs } from './jobs.svelte';
import { libraries } from './libraries.svelte';
import { library } from './library.svelte';
import { selection } from './selection.svelte';
import { toasts } from './toast.svelte';
import { navigate } from './router';
import { libHref } from './routes';
import { continues, emptyTotals, failedHeading, photos, sentence, type MoveTotals } from './movetext';

export type MoveOrigin = 'selection' | 'viewer';

export interface MoveDialogState {
  /** The source library. */
  lib: string;
  hashes: string[];
  origin: MoveOrigin;
}

export interface MoveRun {
  from: string;
  to: string;
  /** Finishing interrupted moves into `to` (no hashes). */
  resume: boolean;
  /** Hashes sent. */
  total: number;
  /** Jobs the run needs, and the one running now (1-based). */
  parts: number;
  part: number;
  jobId: number | null;
  state: 'running' | 'done' | 'cancelled' | 'failed';
  error: string;
  /** The server's messages of jobs that finished with problems (exit 2): what is not done and where to finish it. */
  notes: string[];
  totals: MoveTotals;
  origin: MoveOrigin;
}

const nameOf = (id: string) => libraries.find(id)?.name ?? id;

/** Where "Open in <target>" goes: the import record's grid (one job), the target's Imports page (several), else its grid. */
export function openHref(run: Pick<MoveRun, 'to' | 'totals'>): string {
  const r = run.totals.records;
  if (r.length === 1) return libHref(run.to, `imports/${r[0]}`);
  if (r.length > 1) return libHref(run.to, 'imports');
  return libHref(run.to, '');
}

/** Whether "Move to…" is offered: workspace mode with at least two libraries. */
export function canMove(): boolean {
  return libraries.mode === 'workspace' && libraries.libraries.length >= 2;
}

/** What the server answers to trash and user data changes on a library a move runs on (409). */
export const MOVING_TEXT = 'A move is running on this library; ratings, tags, albums and the trash can be changed when it has finished.';

/** Whether a move runs on library `lib` (as its source or target): its trash and user data cannot be changed meanwhile. */
export function movingOn(lib: string): boolean {
  const j = jobs.active;
  if (j && j.kind === 'transfer' && (j.state === 'running' || j.state === 'queued') && (j.library === lib || j.to === lib)) return true;
  const r = moves.run;
  return !!r && r.state === 'running' && (r.from === lib || r.to === lib);
}

/** Asks before leaving the page while a run still has jobs to start (they are started from this page). */
function warnBeforeLeaving(e: BeforeUnloadEvent): void {
  const r = moves.run;
  if (r && r.state === 'running' && r.part < r.parts) e.preventDefault();
}
if (typeof window !== 'undefined') window.addEventListener('beforeunload', warnBeforeLeaving);

class MoveStore {
  dialog = $state<MoveDialogState | null>(null);
  run = $state<MoveRun | null>(null);

  get running(): boolean {
    return this.run?.state === 'running';
  }

  /** Opens the dialog for `hashes` of library `lib`; a running move is shown instead. */
  open(lib: string, hashes: readonly string[], origin: MoveOrigin): void {
    if (this.run && !this.running) this.run = null;
    this.dialog = { lib, hashes: [...hashes], origin };
  }

  /** Shows the current run (the job pill). False when there is none. */
  show(): boolean {
    if (!this.run) return false;
    this.dialog = { lib: this.run.from, hashes: [], origin: this.run.origin };
    return true;
  }

  /** Closes the dialog; a finished run is forgotten, a running one goes on (the pill shows it). */
  close(): void {
    this.dialog = null;
    if (this.run && !this.running) this.run = null;
  }

  /** Forgets a finished run (the dialog goes back to its plan). */
  dismissRun(): void {
    if (!this.running) this.run = null;
  }

  /** Moves `hashes` from `from` to `to`, one job per TRANSFER_CHUNK, each after the previous has ended. */
  async start(from: string, to: string, hashes: readonly string[], origin: MoveOrigin): Promise<void> {
    if (this.running || hashes.length === 0) return;
    const parts = chunks(hashes, TRANSFER_CHUNK);
    this.run = { from, to, resume: false, total: hashes.length, parts: parts.length, part: 0, jobId: null, state: 'running', error: '', notes: [], totals: emptyTotals(), origin };
    const run = this.run;
    /** Hashes of the jobs that started: they leave the selection (failed ones too, as the result lists them). */
    const sent: string[] = [];
    for (const [i, part] of parts.entries()) {
      run.part = i + 1;
      run.jobId = null;
      let job: Job;
      try {
        job = await jobs.start(from, { kind: 'transfer', to, hashes: part });
      } catch (e) {
        run.state = 'failed';
        run.error = errorMessage(e);
        break;
      }
      run.jobId = job.id;
      sent.push(...part);
      const end = await jobs.waitFor(job.id);
      const res = toTransferResult(end.result);
      if (res) {
        const t = run.totals;
        t.moved += res.moved;
        t.existing += res.existing;
        t.restored += res.restored;
        t.failed = [...t.failed, ...res.failed];
        t.failedCount += res.failedCount;
        t.sourcePending += res.sourcePending;
        if (res.record !== null) t.records = [...t.records, res.record];
      }
      const ok = continues(end.state, end.exitCode);
      if (ok && end.exitCode === 2 && end.error) run.notes = [...run.notes, end.error];
      // The tiles go now when the job names every failure and every photo
      // left this library (an original that could not go to the trash is
      // still here); else the in-place reload after the job
      // (jobs.doneCount) has them right.
      if (ok && res && res.failedCount === res.failed.length && res.sourcePending === 0) {
        const bad = new Set(res.failed.map((f) => f.hash));
        const gone = part.filter((h) => !bad.has(h));
        if (gone.length) emitChange({ kind: 'moved', lib: from, hashes: gone });
      }
      if (!ok) {
        run.state = end.state === 'cancelled' || end.exitCode === 130 ? 'cancelled' : 'failed';
        run.error = end.error ?? '';
        break;
      }
    }
    if (run.state === 'running') run.state = 'done';
    selection.remove(sent);
    this.#after(run);
  }

  /** Finishes the interrupted moves into library `to`. Throws when the job cannot start. */
  async resume(to: string, origin: MoveOrigin = 'selection'): Promise<MoveRun> {
    if (this.running) throw new Error('Another move is running.');
    const job = jobs.adopt(await resumeTransfer(to), to);
    this.run = { from: job.library ?? to, to, resume: true, total: 0, parts: 1, part: 1, jobId: job.id, state: 'running', error: '', notes: [], totals: emptyTotals(), origin };
    const run = this.run;
    const end = await jobs.waitFor(job.id);
    const res = toTransferResult(end.result);
    if (res) {
      run.totals = {
        moved: res.moved,
        existing: res.existing,
        restored: res.restored,
        failed: res.failed,
        failedCount: res.failedCount,
        sourcePending: res.sourcePending,
        records: res.record !== null ? [res.record] : [],
      };
    }
    if (continues(end.state, end.exitCode)) {
      run.state = 'done';
      if (end.exitCode === 2 && end.error) run.notes = [end.error];
    } else {
      run.state = end.state === 'cancelled' || end.exitCode === 130 ? 'cancelled' : 'failed';
      run.error = end.error ?? '';
    }
    this.#after(run);
    return run;
  }

  /** Counts, the libraries list, and a toast when nobody is looking at the dialog. */
  #after(run: MoveRun): void {
    void library.load();
    void libraries.load();
    if (this.dialog) return;
    const to = nameOf(run.to);
    if (run.state === 'failed') {
      toasts.error(run.resume ? `Finishing the move to ${to} failed: ${run.error || 'unknown error'}` : `Moving to ${to} failed: ${run.error || 'unknown error'}`);
      return;
    }
    let text = run.resume
      ? run.notes.length
        ? `The interrupted move to ${to} is not finished.`
        : `Finished the interrupted move to ${to}.`
      : `Moved ${photos(run.totals.moved)} to ${to}.`;
    if (run.state === 'cancelled') text = `Move to ${to} cancelled; ${photos(run.totals.moved)} moved.`;
    if (run.totals.failedCount && !run.notes.length) text += ` ${failedHeading(run.totals.failedCount).replace(/:$/, '.')}`;
    for (const n of run.notes) text += ` ${sentence(n)}`;
    const href = openHref(run);
    const t = run.totals;
    const action =
      t.moved + t.existing + t.restored > 0
        ? { label: `Open in ${to}`, run: () => navigate(href) }
        : {
            label: 'Details',
            run: () => {
              this.show();
            },
          };
    toasts.show(text, { ms: 10000, tone: t.failedCount || run.notes.length ? 'error' : 'info', action });
  }
}

export const moves = new MoveStore();
