// Pure helpers for the backups UI (docs/design/backups.md "Frontend"):
// the "behind" headline, the one-line status of the Library and Libraries
// pages with the 30-day warning, run problems, transfer rate and ETA, and
// the target path check. See backupinfo.test.ts.

import { isRecord, type BackupBehind, type BackupRun, type BackupTarget, type LibBackup } from './api';
import { formatAgo, formatBytes, formatCount, formatElapsed, parseWhen } from './format';

/** A last backup older than this is a warning. */
export const STALE_DAYS = 30;
const DAY_MS = 86_400_000;

/** Scrub budgets offered in the UI, in seconds; null scrubs the whole target. */
export const SCRUB_BUDGETS: readonly { label: string; seconds: number | null }[] = [
  { label: '15 min', seconds: 15 * 60 },
  { label: '1 h', seconds: 3600 },
  { label: 'All', seconds: null },
];

const plural = (n: number, one: string, many = `${one}s`) => `${formatCount(n)} ${n === 1 ? one : many}`;

/** Files the set lacks (new + changed + moved), or null when unknown. */
export function behindCount(b: BackupBehind | number | null | undefined): number | null {
  if (typeof b === 'number') return Number.isFinite(b) ? b : null;
  if (!b) return null;
  return b.new + b.changed + b.moved;
}

/** The card's headline: "1,204 files, 3.2 GiB not backed up yet", "Up to date", … */
export function behindText(t: BackupTarget): string {
  if (!t.reachable) return 'Not reachable';
  const b = t.set?.behind ?? null;
  if (!t.set && !b) return 'Nothing backed up here yet';
  const n = behindCount(b);
  if (n === null || !b) return 'Not checked yet';
  if (n === 0) return 'Up to date';
  return `${plural(n, 'file')}, ${formatBytes(b.bytes)} not backed up yet`;
}

/** What the status line needs; built from the targets or the workspace listing. */
export interface BackupSummary {
  targets: number;
  /** Newest backup over all targets. */
  lastBackupAt: Date | null;
  /** Label of the target of the newest backup ("" when unknown). */
  label: string;
  /** Files behind on that target; null when unknown. */
  behind: number | null;
}

export function summaryFromTargets(ts: readonly BackupTarget[]): BackupSummary {
  let best: BackupTarget | null = null;
  let bestAt: Date | null = null;
  for (const t of ts) {
    const at = parseWhen(t.lastBackupAt);
    if (at && (!bestAt || at.getTime() > bestAt.getTime())) {
      best = t;
      bestAt = at;
    }
  }
  return { targets: ts.length, lastBackupAt: bestAt, label: best?.label ?? '', behind: best ? behindCount(best.set?.behind) : null };
}

/** The listing's `backup` object, or null when the server sends none (nothing is shown then). */
export function summaryFromListing(b: LibBackup | null | undefined): BackupSummary | null {
  if (!isRecord(b)) return null;
  const targets = Array.isArray(b.targets) ? b.targets.length : typeof b.targets === 'number' ? b.targets : 0;
  const behind = isRecord(b.behind) ? behindCount(b.behind as unknown as BackupBehind) : behindCount(typeof b.behind === 'number' ? b.behind : null);
  const at = b.lastBackupAt;
  return { targets, lastBackupAt: parseWhen(typeof at === 'number' || typeof at === 'string' ? at : null), label: typeof b.label === 'string' ? b.label : '', behind };
}

export interface StatusLine {
  tone: 'ok' | 'warn';
  text: string;
  /** No target, never backed up, or the last backup is older than STALE_DAYS. */
  stale: boolean;
}

/** "Backed up 2 hours ago to Red WD drive; 0 behind", with the warnings of the contract. */
export function statusLine(s: BackupSummary, now: number = Date.now()): StatusLine {
  if (s.targets === 0) return { tone: 'warn', stale: true, text: 'No backup target yet' };
  if (!s.lastBackupAt) return { tone: 'warn', stale: true, text: `Never backed up (${plural(s.targets, 'target')})` };
  let text = `Backed up ${formatAgo(s.lastBackupAt, now)}`;
  if (s.label) text += ` to ${s.label}`;
  if (s.behind !== null) text += `; ${formatCount(s.behind)} behind`;
  const stale = now - s.lastBackupAt.getTime() > STALE_DAYS * DAY_MS;
  if (stale) text += ` (older than ${STALE_DAYS} days)`;
  return { tone: stale ? 'warn' : 'ok', stale, text };
}

/** "3 hours ago", or "never". */
export function agoText(v: number | string | null | undefined, now: number = Date.now()): string {
  const d = parseWhen(v);
  return d ? formatAgo(d, now) : 'never';
}

// ---- Run problems ----

export interface Problem {
  path: string;
  /** The error or note, "" when none. */
  detail: string;
  /** Damaged files: repaired from the library. */
  repaired?: boolean;
}

export interface ProblemList {
  /** The count, also when the run recorded only a number. */
  count: number;
  items: Problem[];
}

const EMPTY: ProblemList = { count: 0, items: [] };

function problem(v: unknown): Problem | null {
  if (typeof v === 'string') return { path: v, detail: '' };
  if (!isRecord(v)) return null;
  const path = typeof v.path === 'string' ? v.path : typeof v.relPath === 'string' ? v.relPath : '';
  if (!path) return null;
  const detail = typeof v.error === 'string' ? v.error : typeof v.message === 'string' ? v.message : '';
  const p: Problem = { path, detail };
  if (typeof v.repaired === 'boolean') p.repaired = v.repaired;
  return p;
}

/** A stats field that is a list of paths/objects, or only a count. */
export function problemList(v: unknown): ProblemList {
  if (typeof v === 'number' && Number.isFinite(v)) return { count: v, items: [] };
  if (!Array.isArray(v)) return EMPTY;
  const items = v.map(problem).filter((x): x is Problem => x !== null);
  return { count: Math.max(items.length, v.length), items };
}

export interface RunProblems {
  /** Damaged files from the last scrub, with their repaired state. */
  damaged: ProblemList;
  /** Files on the target that backup.db does not know (last scrub). */
  unknown: ProblemList;
  /** Rows whose file is missing on the target (last scrub). */
  missing: ProblemList;
  /** Copy failures of the last backup run. */
  failed: ProblemList;
  scrub: BackupRun | null;
  backup: BackupRun | null;
}

/** Problems from the newest scrub and the newest backup run (runs newest first, as the server sends them). */
export function runProblems(runs: readonly BackupRun[]): RunProblems {
  const scrub = runs.find((r) => r.kind === 'scrub') ?? null;
  const backup = runs.find((r) => r.kind === 'backup') ?? null;
  const damaged = problemList(scrub?.stats.damaged);
  // A run may list the repaired paths separately.
  const repairedList = scrub?.stats.repaired;
  if (Array.isArray(repairedList)) {
    const fixed = new Set(problemList(repairedList).items.map((p) => p.path));
    for (const d of damaged.items) if (d.repaired === undefined) d.repaired = fixed.has(d.path);
  }
  return {
    damaged,
    unknown: problemList(scrub?.stats.unknown),
    missing: problemList(scrub?.stats.missing),
    failed: problemList(backup?.stats.failed ?? backup?.stats.failures),
    scrub,
    backup,
  };
}

export function problemCount(p: RunProblems): number {
  return p.damaged.count + p.unknown.count + p.missing.count + p.failed.count;
}

const n = (v: unknown): number => (typeof v === 'number' && Number.isFinite(v) ? v : Array.isArray(v) ? v.length : 0);

/** One line about a finished run from its stats: "copied 12 files (1.2 GiB), moved 3; 1 failed". */
export function runSummary(kind: string, stats: Record<string, unknown>): string {
  const parts: string[] = [];
  const add = (count: number, text: string) => {
    if (count > 0) parts.push(text);
  };
  switch (kind) {
    case 'backup':
      add(n(stats.copied), `copied ${plural(n(stats.copied), 'file')}${n(stats.bytes) ? ` (${formatBytes(n(stats.bytes))})` : ''}`);
      add(n(stats.moved), `moved ${formatCount(n(stats.moved))}`);
      add(n(stats.changed), `${formatCount(n(stats.changed))} changed`);
      add(n(stats.failed), `${formatCount(n(stats.failed))} failed`);
      return parts.length ? parts.join(', ') : 'nothing to copy';
    case 'scrub':
      parts.push(`checked ${plural(n(stats.checked), 'file')}${n(stats.bytes) ? ` (${formatBytes(n(stats.bytes))})` : ''}`);
      add(n(stats.damaged), `${formatCount(n(stats.damaged))} damaged`);
      add(n(stats.repaired), `${formatCount(n(stats.repaired))} repaired`);
      add(n(stats.unknown), `${formatCount(n(stats.unknown))} unknown`);
      add(n(stats.missing), `${formatCount(n(stats.missing))} missing`);
      return parts.join(', ');
    case 'prune':
      return `removed ${plural(n(stats.removed), 'file')}${n(stats.bytes) ? ` (${formatBytes(n(stats.bytes))})` : ''}`;
    case 'restore':
      parts.push(`restored ${plural(n(stats.restored), 'file')}${n(stats.bytes) ? ` (${formatBytes(n(stats.bytes))})` : ''}`);
      add(n(stats.failed), `${formatCount(n(stats.failed))} failed`);
      return parts.join(', ');
    default:
      return '';
  }
}

export function runTitle(kind: string): string {
  switch (kind) {
    case 'backup':
      return 'Backup';
    case 'scrub':
      return 'Scrub';
    case 'prune':
      return 'Prune';
    case 'restore':
      return 'Restore';
    default:
      return kind;
  }
}

// ---- Progress: rate and ETA ----

/**
 * Transfer rate over a sliding window of (time, bytes) samples. A value
 * that goes down (a new phase starts counting from zero) starts over.
 */
export class RateMeter {
  #samples: { t: number; v: number }[] = [];
  readonly windowMs: number;

  constructor(windowMs = 20_000) {
    this.windowMs = windowMs;
  }

  add(t: number, v: number): void {
    const last = this.#samples[this.#samples.length - 1];
    if (last && v < last.v) this.#samples = [];
    else if (last && t <= last.t) {
      last.v = v;
      return;
    }
    this.#samples.push({ t, v });
    const from = t - this.windowMs;
    while (this.#samples.length > 2 && (this.#samples[0] as { t: number }).t < from) this.#samples.shift();
  }

  /** Units per second, or null until the samples span at least a second. */
  rate(): number | null {
    const a = this.#samples[0];
    const b = this.#samples[this.#samples.length - 1];
    if (!a || !b || b.t - a.t < 1000) return null;
    return Math.max(0, (b.v - a.v) / ((b.t - a.t) / 1000));
  }

  reset(): void {
    this.#samples = [];
  }
}

/** Milliseconds until `total` at `rate` per second; null when it cannot be told. */
export function etaMs(done: number, total: number, rate: number | null): number | null {
  if (!total || total <= 0 || rate === null || !Number.isFinite(rate)) return null;
  if (done >= total) return 0;
  if (rate <= 0) return null;
  return ((total - done) / rate) * 1000;
}

/** "42 MiB/s". */
export function formatRate(bytesPerSec: number | null): string {
  return bytesPerSec === null ? '' : `${formatBytes(bytesPerSec)}/s`;
}

/** "about 3m 05s left", "" when unknown. */
export function etaText(ms: number | null): string {
  if (ms === null) return '';
  if (ms <= 0) return 'finishing';
  return `about ${formatElapsed(ms)} left`;
}

/** 0..1 done, by bytes when the total is known, else by files; null when neither total is known. */
export function progressFraction(p: { files?: number; bytes?: number; totalFiles?: number; totalBytes?: number } | null | undefined): number | null {
  if (!p) return null;
  if (p.totalBytes && p.totalBytes > 0) return Math.min(1, Math.max(0, (p.bytes ?? 0) / p.totalBytes));
  if (p.totalFiles && p.totalFiles > 0) return Math.min(1, Math.max(0, (p.files ?? 0) / p.totalFiles));
  return null;
}

export function phaseLabel(kind: string, phase: string | undefined): string {
  switch (phase) {
    case 'plan':
      return 'Planning';
    case 'copy':
      return kind === 'restore' ? 'Copying back' : 'Copying';
    case 'snapshot':
      return 'Saving catalog snapshots';
    case 'scrub':
      return 'Checking files';
    case 'prune':
      return 'Removing files';
    case 'restore':
      return 'Restoring';
    default:
      return 'Starting';
  }
}

// ---- Target paths ----

/**
 * Why a typed target path cannot be sent, or null. Absolute paths only
 * (POSIX, drive letters, UNC shares); no NUL, no device paths. The server
 * checks again.
 */
export function targetPathProblem(p: string): string | null {
  const s = p.trim();
  if (!s) return 'Enter a folder path.';
  if (s.includes('\u0000')) return 'The path contains a NUL character.';
  if (/^[\\/]{2}[?.][\\/]/.test(s) || s.startsWith('\\??\\')) return 'Device paths are not allowed.';
  if (s.startsWith('/') && !s.startsWith('//')) return null;
  if (/^[A-Za-z]:[\\/]/.test(s)) return null;
  if (/^[\\/]{2}[^\\/]+[\\/][^\\/]+/.test(s)) return null; // \\server\share
  return 'Enter an absolute path (for example /media/backup, E:\\Backup or \\\\nas\\photos).';
}
