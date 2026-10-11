// Backup state of the library the UI is in (docs/design/backups.md
// "Frontend"): its targets with their set summaries, a plan cache per
// target (the prune and restore dialogs need a fresh one), the recent runs
// per target (the problems panel), and which job runs for which target.
// The Backups page and the Library page's status line read it; pages call
// `refresh()` after a job ends.

import { errorMessage, isBackupKind, libApi, type BackupJobKindRequest, type BackupPlan, type BackupRun, type BackupTarget, type Job } from './api';
import { jobs } from './jobs.svelte';
import { statusLine, summaryFromTargets, type StatusLine } from './backupinfo';

/** A plan younger than this is reused (the server recomputes `behind` at most every 60 s too). */
export const PLAN_TTL_MS = 60_000;

interface CachedPlan {
  plan: BackupPlan;
  at: number;
}

class BackupsStore {
  /** Library the state belongs to; null when none is selected. */
  lib = $state<string | null>(null);
  targets = $state<BackupTarget[]>([]);
  loaded = $state(false);
  loading = $state(false);
  error = $state<string | null>(null);
  plans = $state<Record<string, CachedPlan>>({});
  planErrors = $state<Record<string, string>>({});
  runs = $state<Record<string, BackupRun[]>>({});
  /** Job id → target id, for jobs started from this page (the job object may not name its target). */
  jobTargets = $state<Record<number, string>>({});
  /** Job id → scrub budget in seconds, for budgeted scrubs started from this page (the job object does not carry it). */
  jobBudgets = $state<Record<number, number>>({});
  /** Bumped by select(); answers for an older library are dropped. */
  #gen = 0;

  /** Switches to library `lib` (or none). Does not load; call `load()`. */
  select(lib: string | null): void {
    if (lib === this.lib) return;
    this.#gen++;
    this.lib = lib;
    this.targets = [];
    this.loaded = false;
    this.loading = false;
    this.error = null;
    this.plans = {};
    this.planErrors = {};
    this.runs = {};
    this.jobTargets = {};
    this.jobBudgets = {};
  }

  /** The status line of the Library page ("Backed up 2 hours ago to …"); null until loaded. */
  get status(): StatusLine | null {
    return this.loaded ? statusLine(summaryFromTargets(this.targets)) : null;
  }

  find(id: string): BackupTarget | undefined {
    return this.targets.find((t) => t.id === id);
  }

  /** Loads the targets; `plan` asks the server for fresh "behind" numbers. `runs` also loads every target's runs. */
  async load(opts: { plan?: boolean; runs?: boolean } = {}): Promise<void> {
    const lib = this.lib;
    if (lib === null) return;
    const g = this.#gen;
    this.loading = true;
    try {
      const list = await libApi(lib).getBackups(!!opts.plan);
      if (g !== this.#gen) return;
      this.targets = list;
      this.error = null;
      this.loaded = true;
      if (opts.runs) await Promise.all(list.map((t) => this.loadRuns(t.id)));
    } catch (e) {
      if (g === this.#gen) this.error = errorMessage(e);
    } finally {
      if (g === this.#gen) this.loading = false;
    }
  }

  /** After a job: fresh "behind" numbers, runs, and no cached plans. */
  async refresh(): Promise<void> {
    this.plans = {};
    await this.load({ plan: true, runs: true });
  }

  async loadRuns(id: string): Promise<void> {
    const lib = this.lib;
    if (lib === null) return;
    const g = this.#gen;
    try {
      const list = await libApi(lib).getRuns(id);
      if (g === this.#gen) this.runs = { ...this.runs, [id]: list };
    } catch {
      // The problems panel is informational; keep what was there.
    }
  }

  /** The plan for target `id`, from the cache when younger than PLAN_TTL_MS unless `fresh`. Throws on failure. */
  async plan(id: string, fresh = false): Promise<BackupPlan> {
    const lib = this.lib;
    if (lib === null) throw new Error('no library');
    const hit = this.plans[id];
    if (hit && !fresh && Date.now() - hit.at < PLAN_TTL_MS) return hit.plan;
    const g = this.#gen;
    const plan = await libApi(lib).getPlan(id);
    if (g === this.#gen) this.plans = { ...this.plans, [id]: { plan, at: Date.now() } };
    return plan;
  }

  /** Adds a target and lists it. */
  async add(path: string, label: string): Promise<BackupTarget> {
    const lib = this.lib;
    if (lib === null) throw new Error('no library');
    const t = await libApi(lib).addTarget(path.trim(), label.trim());
    await this.load({ plan: true });
    void this.loadRuns(t.id);
    return t;
  }

  /** Renames or re-points a target. */
  async update(id: string, patch: { label?: string; path?: string }): Promise<BackupTarget> {
    const lib = this.lib;
    if (lib === null) throw new Error('no library');
    const t = await libApi(lib).updateTarget(id, patch);
    const i = this.targets.findIndex((x) => x.id === id);
    if (i >= 0 && t.id) this.targets[i] = t;
    // Re-pointing can make a target reachable: ask for its numbers.
    if (patch.path !== undefined) await this.load({ plan: true });
    return t;
  }

  /** Detaches a target (the files on it stay). */
  async forget(id: string): Promise<void> {
    const lib = this.lib;
    if (lib === null) throw new Error('no library');
    await libApi(lib).forgetTarget(id);
    this.targets = this.targets.filter((t) => t.id !== id);
    const { [id]: _p, ...plans } = this.plans;
    const { [id]: _r, ...runs } = this.runs;
    this.plans = plans;
    this.runs = runs;
  }

  /** Starts a backup, scrub, prune or restore job for a target and remembers which target it is for. */
  async start(req: BackupJobKindRequest): Promise<Job> {
    const lib = this.lib;
    if (lib === null) throw new Error('no library');
    const job = await jobs.start(lib, req);
    this.jobTargets = { ...this.jobTargets, [job.id]: req.target };
    if (req.kind === 'scrub' && req.budget) this.jobBudgets = { ...this.jobBudgets, [job.id]: req.budget };
    // A prune or restore changes what the plan says.
    const { [req.target]: _p, ...plans } = this.plans;
    this.plans = plans;
    return job;
  }

  /** The target a job runs for: the job's own `target`, else the one it was started for here. */
  targetOf(job: Job | null | undefined): string | null {
    if (!job) return null;
    if (typeof job.target === 'string' && job.target) return job.target;
    return this.jobTargets[job.id] ?? null;
  }

  /**
   * Whether `job` is a backup-kind job of this library for target `id`.
   * Marker ids are shared by every library backed up to the same drive, so
   * the library must match too.
   */
  isFor(job: Job | null | undefined, id: string): boolean {
    if (!job || !isBackupKind(job.kind)) return false;
    if (job.library !== this.lib) return false;
    return this.targetOf(job) === id;
  }

  /** The budget (seconds) of a budgeted scrub started from this page, else null. */
  budgetOf(job: Job | null | undefined): number | null {
    if (!job || job.kind !== 'scrub') return null;
    return this.jobBudgets[job.id] ?? null;
  }
}

export const backups = new BackupsStore();
