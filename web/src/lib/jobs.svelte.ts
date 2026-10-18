// Job state shared by the shell (job pill), the Import page and the Library
// page. Keeps exactly one SSE connection open while a job is active and
// reconnects with backoff after errors. When a job ends it bumps `doneCount`
// and refetches /api/library; pages watch `doneCount` to refresh themselves.

import {
  ApiError,
  cancelJob,
  errorMessage,
  getJob,
  isRecord,
  libApi,
  listJobs,
  startWorkspaceJob,
  subscribeJob,
  type Job,
  type JobRequest,
  type JobResult,
  type JobStats,
  type RebuildJobRequest,
} from './api';
import { library } from './library.svelte';

const MAX_RESULTS = 200;
const MAX_ERRORS = 500;
/** How often an idle page looks for jobs started elsewhere (another tab, the CLI). */
export const POLL_MS = 10_000;

export function isActive(j: Job | null | undefined): boolean {
  return !!j && (j.state === 'running' || j.state === 'queued');
}

/** Files the importer has finished with (any outcome). */
export function processed(s: JobStats): number {
  return s.imported + s.existing + s.duplicates + s.skippedSmall + s.nonMedia + s.errors;
}

export function emptyStats(): JobStats {
  return {
    scanned: 0,
    imported: 0,
    importedPhotos: 0,
    importedRaw: 0,
    importedVideos: 0,
    existing: 0,
    duplicates: 0,
    skippedSmall: 0,
    nonMedia: 0,
    errors: 0,
    bytesCopied: 0,
    rate: 0,
    thumbsDone: 0,
    thumbsUnsupported: 0,
    thumbsFailed: 0,
    largeDone: 0,
    sourceDisconnected: false,
  };
}

function normalize(j: Job): Job {
  const out: Job = { ...j, stats: { ...emptyStats(), ...(j.stats ?? {}) } };
  // A similar job carries its phase as `progress` in the job object (listings,
  // state events): the pill and the progress panel read `phase`.
  const p = j.progress;
  if (out.phase === undefined && isRecord(p) && typeof p.phase === 'string') out.phase = { ...p };
  return out;
}

/**
 * Job ids restart at 1 with the server, so an id alone does not name a job.
 * Two records are the same job when their ids match and their start times
 * do not contradict each other (a queued job has no start time yet).
 */
function sameStart(a: string | null | undefined, b: string | null | undefined): boolean {
  return !a || !b || a === b;
}

class JobStore {
  /** Jobs of this server process, newest first. */
  jobs = $state<Job[]>([]);
  /** The job the UI follows: the active one, else the newest. */
  currentId = $state<number | null>(null);
  /** Last MAX_RESULTS non-error results of the followed job, oldest first. */
  results = $state.raw<JobResult[]>([]);
  /** Error results of the followed job (pinned in the UI). */
  errors = $state.raw<JobResult[]>([]);
  /** Incremented whenever a job ends. */
  doneCount = $state(0);
  loaded = $state(false);
  loadError = $state<string | null>(null);
  /** True while the SSE stream is (re)connecting after an error. */
  reconnecting = $state(false);

  #close: (() => void) | null = null;
  #subId: number | null = null;
  /** Start time of the followed job, to tell it from a later job with the same id. */
  #followStarted: string | null = null;
  /** Bumped by every (un)subscribe; async work started for an older value is dropped. */
  #token = 0;
  /** Bumped by every init(); only the latest listing is applied. */
  #initGen = 0;
  #lastSeq = -1;
  #retryTimer: ReturnType<typeof setTimeout> | null = null;
  #backoff = 1000;
  /** Callers of waitFor, per job id. */
  #waiters = new Map<number, ((j: Job) => void)[]>();

  get current(): Job | null {
    return this.jobs.find((j) => j.id === this.currentId) ?? null;
  }

  get active(): Job | null {
    return this.jobs.find((j) => isActive(j)) ?? null;
  }

  /**
   * Loads the job list and follows the active job (else the newest). Safe
   * to call again at any time: a job that is already followed is left
   * alone, so polling does not reopen streams.
   */
  async init(): Promise<void> {
    const g = ++this.#initGen;
    try {
      const list = (await listJobs()).map(normalize);
      if (g !== this.#initGen) return;
      this.jobs = list;
      this.loadError = null;
      for (const j of list) if (!isActive(j)) this.#settle(j);
      const follow = list.find((j) => isActive(j)) ?? list[0] ?? null;
      if (!follow) {
        this.#unsubscribe();
        this.#reset(null, null);
        this.reconnecting = false;
      } else if (!this.#isFollowing(follow) || (isActive(follow) && this.#subId !== follow.id)) {
        this.#follow(follow.id);
      }
    } catch (e) {
      if (g === this.#initGen) this.loadError = errorMessage(e);
    } finally {
      if (g === this.#initGen) this.loaded = true;
    }
  }

  /**
   * Starts a job in library `lib`. A rebuild of a folder or of an unreadable
   * catalog goes through the workspace route (`via: 'workspace'`).
   */
  async start(lib: string, req: JobRequest, via: 'library' | 'workspace' = 'library'): Promise<Job> {
    let started: Job;
    try {
      started = via === 'workspace' && req.kind === 'rebuild' ? await startWorkspaceJob(lib, req as RebuildJobRequest) : await libApi(lib).startJob(req);
      if (!started.library) started = { ...started, library: lib };
    } catch (e) {
      // 409: another job is running that this page does not know about
      // (started in another tab or by the CLI): show it right away.
      if (e instanceof ApiError && e.status === 409) void this.init();
      throw e;
    }
    return this.adopt(started);
  }

  /** Follows a job started through another route (e.g. resuming a move); `lib` fills a missing `library`. */
  adopt(started: Job, lib?: string): Job {
    const job = normalize(!started.library && lib !== undefined ? { ...started, library: lib } : started);
    this.#upsert(job);
    this.#follow(job.id);
    return job;
  }

  /**
   * While a page that shows jobs is visible, polls the job list when idle
   * so jobs started elsewhere are discovered. Returns the stop function
   * (use as the return value of an $effect).
   */
  watch(): () => void {
    const t = setInterval(() => {
      if (this.active) return; // the SSE stream keeps an active job current
      if (typeof document !== 'undefined' && document.hidden) return;
      void this.init();
    }, POLL_MS);
    return () => clearInterval(t);
  }

  async cancel(id: number): Promise<void> {
    await cancelJob(id);
  }

  /**
   * Resolves with job `id` once it has ended (at once when it already has).
   * A job the server lost resolves as failed. Used to run jobs one after
   * another (one job runs at a time).
   */
  waitFor(id: number): Promise<Job> {
    const j = this.jobs.find((x) => x.id === id);
    if (j && !isActive(j)) return Promise.resolve(j);
    return new Promise((resolve) => this.#waiters.set(id, [...(this.#waiters.get(id) ?? []), resolve]));
  }

  #settle(j: Job): void {
    const list = this.#waiters.get(j.id);
    if (!list) return;
    this.#waiters.delete(j.id);
    for (const resolve of list) resolve(j);
  }

  #upsert(job: Job): void {
    const j = normalize(job);
    const i = this.jobs.findIndex((x) => x.id === j.id);
    const prev = i >= 0 ? this.jobs[i] : undefined;
    if (prev && j.phase === undefined && prev.phase && sameStart(prev.startedAt, j.startedAt)) j.phase = prev.phase;
    if (i >= 0) this.jobs[i] = j;
    else this.jobs = [j, ...this.jobs].sort((a, b) => b.id - a.id);
    if (!isActive(j)) this.#settle(j);
    if (j.id === this.currentId) {
      if (!sameStart(this.#followStarted, j.startedAt)) this.#reset(j.id, j.startedAt);
      else if (j.startedAt) this.#followStarted = j.startedAt;
    }
  }

  #isFollowing(j: Job): boolean {
    return this.currentId === j.id && sameStart(this.#followStarted, j.startedAt);
  }

  /** Forgets the followed job's results (a different job is followed now). */
  #reset(id: number | null, startedAt: string | null): void {
    if (this.#flushTimer) clearTimeout(this.#flushTimer);
    this.#flushTimer = null;
    this.#pending = [];
    this.currentId = id;
    this.#followStarted = startedAt;
    this.#backoff = 1000;
    this.results = [];
    this.errors = [];
    this.#lastSeq = -1;
  }

  /** Makes id the followed job and subscribes when it is active. */
  #follow(id: number): void {
    const job = this.jobs.find((j) => j.id === id);
    if (job ? !this.#isFollowing(job) : this.currentId !== id) this.#reset(id, job?.startedAt ?? null);
    else if (job?.startedAt) this.#followStarted = job.startedAt;
    // Finished jobs still get one subscription: the server replays its
    // result ring, which the summary page shows.
    this.#subscribe(id, !!job && !isActive(job));
  }

  #subscribe(id: number, replayOnly: boolean): void {
    this.#unsubscribe();
    const token = ++this.#token;
    const live = () => token === this.#token;
    this.#subId = id;
    this.#close = subscribeJob(id, {
      onState: (job) => {
        if (!live()) return;
        this.reconnecting = false;
        this.#backoff = 1000;
        this.#upsert(job);
      },
      onProgress: (stats) => {
        if (!live()) return;
        const j = this.jobs.find((x) => x.id === id);
        if (j) j.stats = { ...emptyStats(), ...stats };
      },
      onPhase: (p) => {
        if (!live()) return;
        const j = this.jobs.find((x) => x.id === id);
        if (j) j.phase = { ...p };
      },
      onResult: (r) => {
        if (live()) this.#addResult(r);
      },
      onDone: (job) => {
        if (!live()) return;
        this.#close = null;
        this.#subId = null;
        const before = this.jobs.find((x) => x.id === id);
        const wasActive = !!before && (isActive(before) || !replayOnly);
        this.#upsert(job);
        if (wasActive) this.#finished();
      },
      onError: () => {
        if (!live()) return;
        this.#close = null;
        this.#scheduleReconnect(id);
      },
    });
  }

  #unsubscribe(): void {
    this.#token++;
    if (this.#retryTimer) {
      clearTimeout(this.#retryTimer);
      this.#retryTimer = null;
    }
    this.#close?.();
    this.#close = null;
    this.#subId = null;
  }

  /**
   * Retries a dropped stream for job id with backoff. Every step checks
   * that the subscription it was started for is still the current one, so
   * a late answer never clobbers a newer subscription.
   */
  #scheduleReconnect(id: number): void {
    const token = this.#token;
    this.reconnecting = true;
    const delay = this.#backoff;
    this.#backoff = Math.min(this.#backoff * 2, 15000);
    this.#retryTimer = setTimeout(() => {
      this.#retryTimer = null;
      if (token !== this.#token) return;
      void (async () => {
        let fresh: Job;
        try {
          fresh = normalize(await getJob(id));
        } catch (e) {
          if (token !== this.#token) return;
          // The server no longer knows the job (it was restarted): stop
          // retrying and take the server's current state instead.
          if (e instanceof ApiError && e.status === 404) void this.#lost(id);
          else this.#scheduleReconnect(id);
          return;
        }
        if (token !== this.#token) return;
        const before = this.jobs.find((x) => x.id === id);
        if (before && !sameStart(before.startedAt, fresh.startedAt)) {
          // Same id, different job: the server restarted and reused the id.
          void this.#lost(id);
          return;
        }
        this.#upsert(fresh);
        if (isActive(fresh)) {
          this.#subscribe(id, false);
        } else {
          this.reconnecting = false;
          this.#subId = null;
          this.#backoff = 1000;
          if (before && isActive(before)) this.#finished();
        }
      })();
    }, delay);
  }

  /** The followed job vanished from the server: drop it and reload the list. */
  async #lost(id: number): Promise<void> {
    this.#unsubscribe();
    this.reconnecting = false;
    this.#backoff = 1000;
    const before = this.jobs.find((x) => x.id === id);
    this.jobs = this.jobs.filter((x) => x !== before);
    if (before) this.#settle({ ...before, state: 'failed', error: before.error ?? 'the server no longer knows this job' });
    // It may have changed the library before it was lost.
    if (before && isActive(before)) this.#finished();
    await this.init();
  }

  // Results can arrive thousands per second; they are buffered and applied
  // at most every 200 ms so the list re-renders in batches.
  #pending: JobResult[] = [];
  #flushTimer: ReturnType<typeof setTimeout> | null = null;

  #addResult(r: JobResult): void {
    if (typeof r.seq === 'number') {
      if (r.seq <= this.#lastSeq) return; // replayed after a reconnect
      this.#lastSeq = r.seq;
    }
    this.#pending.push(r);
    this.#flushTimer ??= setTimeout(() => this.#flush(), 200);
  }

  #flush(): void {
    if (this.#flushTimer) clearTimeout(this.#flushTimer);
    this.#flushTimer = null;
    if (this.#pending.length === 0) return;
    const batch = this.#pending;
    this.#pending = [];
    const errs = batch.filter((r) => r.status === 'error');
    const rest = batch.filter((r) => r.status !== 'error');
    if (errs.length) this.errors = [...this.errors, ...errs].slice(-MAX_ERRORS);
    if (rest.length) this.results = [...this.results, ...rest].slice(-MAX_RESULTS);
  }

  #finished(): void {
    this.#flush();
    this.reconnecting = false;
    this.doneCount++;
    void library.load();
  }
}

export const jobs = new JobStore();
