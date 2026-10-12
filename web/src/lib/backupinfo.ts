// Pure helpers for the backups UI (docs/design/backups.md "Frontend"):
// the "behind" headline, the one-line status of the Library and Libraries
// pages with the 30-day warning, run problems, transfer rate and ETA, and
// the target path check. See backupinfo.test.ts.

import { isRecord, type BackupBehind, type BackupRun, type BackupTarget, type Lib, type LibBackup } from './api';
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

/**
 * The card's headline: "1,204 files, 3.2 GiB not backed up yet", "Up to
 * date", "1 file to move", … A target the server reports an `error` for
 * (its set belongs to another library, …) shows that instead.
 */
export function behindText(t: BackupTarget): string {
  if (!t.reachable) return 'Not reachable';
  if (t.error) return t.error;
  const b = t.set?.behind ?? null;
  if (!t.set && !b) return 'Nothing backed up here yet';
  const n = behindCount(b);
  if (n === null || !b) return 'Not checked yet';
  if (n === 0) return 'Up to date';
  // Moves are renames on the target: they copy nothing, so they are not "not backed up".
  const copy = b.new + b.changed;
  const move = b.moved > 0 ? `${plural(b.moved, 'file')} to move` : '';
  if (copy === 0) return move;
  const head = `${plural(copy, 'file')}, ${formatBytes(b.bytes)} not backed up yet`;
  return move ? `${head}; ${formatCount(b.moved)} to move` : head;
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

/**
 * The status of a library card of the workspace listing. The server omits
 * `backup` for a library without targets (and for one in state error,
 * where nothing is known): the former reads as "No backup target yet".
 */
export function summaryFromLib(l: Pick<Lib, 'state' | 'backup'>): BackupSummary | null {
  if (l.state === 'error') return null;
  return summaryFromListing(l.backup) ?? { targets: 0, lastBackupAt: null, label: '', behind: null };
}

/** The listing's `backup` object, or null when the server sends none. */
export function summaryFromListing(b: LibBackup | null | undefined): BackupSummary | null {
  if (!isRecord(b)) return null;
  const targets = Array.isArray(b.targets) ? b.targets.length : typeof b.targets === 'number' ? b.targets : 0;
  const behind = isRecord(b.behind) ? behindCount(b.behind as unknown as BackupBehind) : behindCount(typeof b.behind === 'number' ? b.behind : null);
  const at = b.lastBackupAt;
  return { targets, lastBackupAt: parseWhen(typeof at === 'number' || typeof at === 'string' ? at : null), label: typeof b.label === 'string' ? b.label : '', behind };
}

export interface StatusLine {
  /** 'muted' for "No backup target yet": a warning, but not an alarm. */
  tone: 'ok' | 'warn' | 'muted';
  text: string;
  /** No target, never backed up, or the last backup is older than STALE_DAYS. */
  stale: boolean;
}

/** "Backed up 2 hours ago to Red WD drive; 0 behind", with the warnings of the contract. */
export function statusLine(s: BackupSummary, now: number = Date.now()): StatusLine {
  if (s.targets === 0) return { tone: 'muted', stale: true, text: 'No backup target yet' };
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
  /** Damaged and missing files: copied again from the library by the scrub. */
  repaired?: boolean;
}

export interface ProblemList {
  /** The count, also when the run recorded only a number or capped the list. */
  count: number;
  /** Items not repaired (the problems still open). */
  open: number;
  items: Problem[];
  /** The run capped its lists (at 500): `items` holds fewer than `count`. */
  truncated: boolean;
}

const EMPTY: ProblemList = { count: 0, open: 0, items: [], truncated: false };

/** Lists in run stats are capped at this many entries (docs/design/backups.md). */
export const LIST_CAP = 500;

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

const isNum = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v);

/**
 * A stats field that is a list of paths/objects, or only a count. `count`
 * is the run's own total (`failedCount`, …), preferred when it is a number:
 * the list is capped.
 */
export function problemList(v: unknown, count?: unknown, truncated = false): ProblemList {
  let out: ProblemList;
  if (isNum(v)) out = { count: v, open: v, items: [], truncated: false };
  else if (!Array.isArray(v)) out = { ...EMPTY, items: [] };
  else {
    const items = v.map(problem).filter((x): x is Problem => x !== null);
    const c = Math.max(items.length, v.length);
    out = { count: c, open: c, items, truncated: false };
  }
  if (isNum(count) && count >= 0) out.count = count;
  out.truncated = truncated && out.count > out.items.length;
  out.open = out.count;
  return out;
}

export interface RunProblems {
  /** Damaged files from the last scrub, with their repaired state. */
  damaged: ProblemList;
  /** Files on the target that backup.db does not know (last scrub). */
  unknown: ProblemList;
  /** Rows whose file is missing on the target (last scrub); repaired when the scrub copied them again. */
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
  const ss = scrub?.stats ?? {};
  const bs = backup?.stats ?? {};
  const damaged = problemList(ss.damaged, ss.damagedCount, ss.truncated === true);
  const missing = problemList(ss.missing, ss.missingCount, ss.truncated === true);
  // A run may list the repaired paths separately.
  const repairedList = ss.repaired;
  if (Array.isArray(repairedList)) {
    const fixed = new Set(problemList(repairedList).items.map((p) => p.path));
    for (const d of damaged.items) if (d.repaired === undefined) d.repaired = fixed.has(d.path);
    for (const m of missing.items) if (fixed.has(m.path)) m.repaired = true;
  }
  const damagedFixed = damaged.items.filter((d) => d.repaired === true).length;
  let missingFixed = missing.items.filter((m) => m.repaired === true).length;
  // The server counts every file it copied again in `repaired` (damaged and
  // missing ones alike) and marks only the damaged ones. When the number
  // covers the repaired damaged files and every missing one, the missing
  // ones were all copied again.
  if (isNum(repairedList) && missing.count > 0 && repairedList >= damagedFixed + missing.count) {
    for (const m of missing.items) m.repaired = true;
    missingFixed = missing.count;
  }
  damaged.open = Math.max(0, damaged.count - damagedFixed);
  missing.open = Math.max(0, missing.count - missingFixed);
  return {
    damaged,
    unknown: problemList(ss.unknown, ss.unknownCount, ss.truncated === true),
    missing,
    failed: problemList(bs.failed ?? bs.failures, bs.failedCount, bs.truncated === true),
    scrub,
    backup,
  };
}

/** Problems still open (repaired files are not counted). */
export function problemCount(p: RunProblems): number {
  return p.damaged.open + p.unknown.open + p.missing.open + p.failed.open;
}

/** Everything listed, repaired files included. */
export function problemTotal(p: RunProblems): number {
  return p.damaged.count + p.unknown.count + p.missing.count + p.failed.count;
}

const n = (v: unknown): number => (isNum(v) ? v : Array.isArray(v) ? v.length : 0);
/** `<field>Count` when the run sent it (lists are capped), else the field itself. */
const nc = (stats: Record<string, unknown>, field: string): number => {
  const c = stats[`${field}Count`];
  return isNum(c) ? c : n(stats[field]);
};

/** The note for capped lists. */
export const TRUNCATED_NOTE = `list truncated to ${formatCount(LIST_CAP)}`;

/** One line about a finished run from its stats: "copied 12 files (1.2 GiB), moved 3; 1 failed". */
export function runSummary(kind: string, stats: Record<string, unknown>): string {
  const parts: string[] = [];
  const add = (count: number, text: string) => {
    if (count > 0) parts.push(text);
  };
  const bytes = n(stats.bytes) ? ` (${formatBytes(n(stats.bytes))})` : '';
  let line: string;
  switch (kind) {
    case 'backup':
      add(n(stats.copied), `copied ${plural(n(stats.copied), 'file')}${bytes}`);
      add(n(stats.moved), `moved ${formatCount(n(stats.moved))}`);
      add(n(stats.changed), `${formatCount(n(stats.changed))} changed`);
      add(nc(stats, 'failed'), `${formatCount(nc(stats, 'failed'))} failed`);
      line = parts.length ? parts.join(', ') : 'nothing to copy';
      break;
    case 'scrub':
      parts.push(`checked ${plural(n(stats.checked), 'file')}${bytes}`);
      add(nc(stats, 'damaged'), `${formatCount(nc(stats, 'damaged'))} damaged`);
      add(nc(stats, 'missing'), `${formatCount(nc(stats, 'missing'))} missing`);
      add(n(stats.repaired), `${formatCount(n(stats.repaired))} repaired`);
      add(nc(stats, 'unknown'), `${formatCount(nc(stats, 'unknown'))} unknown`);
      line = parts.join(', ');
      break;
    case 'prune':
      parts.push(`removed ${plural(n(stats.removed), 'file')}${bytes}`);
      add(nc(stats, 'failed'), `${formatCount(nc(stats, 'failed'))} failed`);
      line = parts.join(', ');
      break;
    case 'restore':
      parts.push(`restored ${plural(n(stats.restored), 'file')}${bytes}`);
      add(nc(stats, 'failed'), `${formatCount(nc(stats, 'failed'))} failed`);
      line = parts.join(', ');
      break;
    default:
      return '';
  }
  return stats.truncated === true ? `${line}; ${TRUNCATED_NOTE}` : line;
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

/**
 * Rate and time left as the server computed them (`rate` bytes/s, `eta`
 * seconds, -1 unknown); `undefined` for a field the server did not send, so
 * the caller falls back to its own measurement.
 */
export function serverRate(p: { rate?: unknown } | null | undefined): number | null | undefined {
  if (!p || !isNum(p.rate)) return undefined;
  return p.rate > 0 ? p.rate : null;
}

export function serverEtaMs(p: { eta?: unknown } | null | undefined): number | null | undefined {
  if (!p || !isNum(p.eta)) return undefined;
  return p.eta >= 0 ? p.eta * 1000 : null;
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
    case 'move':
      return 'Moving';
    case 'recover':
      return 'Finishing an interrupted move';
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
