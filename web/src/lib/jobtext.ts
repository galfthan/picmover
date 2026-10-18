// Wording for job summaries, mirroring the CLI (cmd/import.go) so the UI and
// the terminal say the same thing.

import { isBackupKind, toTransferResult, type Job } from './api';
import { phaseLabel, runSummary } from './backupinfo';
import { formatCount } from './format';

export type Tone = 'ok' | 'warn' | 'error' | 'muted';

/** The one status of an import whose source went away (importer.DisconnectedMessage). */
export const DISCONNECTED_MESSAGE =
  'Source disconnected — reconnect and run the import again; already imported files are kept and will be skipped';

/** Imported counts by kind. */
export interface KindCounts {
  importedPhotos: number;
  importedRaw: number;
  importedVideos: number;
}

const countOf = (n: number, one: string, many: string) => `${formatCount(n)} ${n === 1 ? one : many}`;

/** "3 photos, 2 RAW, 1 video": the imported split by kind, zero counts left out ("" when all are zero). */
export function kindSplit(s: KindCounts): string {
  const parts: string[] = [];
  if (s.importedPhotos) parts.push(countOf(s.importedPhotos, 'photo', 'photos'));
  if (s.importedRaw) parts.push(`${formatCount(s.importedRaw)} RAW`);
  if (s.importedVideos) parts.push(countOf(s.importedVideos, 'video', 'videos'));
  return parts.join(', ');
}

/** Thumbnails processed by an import (or thumbs) job. */
export function thumbsProcessed(s: { thumbsDone: number; thumbsUnsupported: number; thumbsFailed: number }): number {
  return s.thumbsDone + s.thumbsUnsupported + s.thumbsFailed;
}

/** The CLI's exit code for a finished job, derived from state when absent. */
export function exitCodeOf(j: Job): number {
  if (typeof j.exitCode === 'number') return j.exitCode;
  if (j.state === 'cancelled') return 130;
  if (j.state === 'failed') return 1;
  return 0;
}

export function jobTitle(j: Job): string {
  switch (j.kind) {
    case 'thumbs':
      return 'Thumbnails';
    case 'similar':
      return 'Duplicate search';
    case 'rebuild':
      return 'Rebuild';
    case 'backup':
      return 'Backup';
    case 'scrub':
      return 'Scrub';
    case 'prune':
      return 'Prune';
    case 'restore':
      return 'Restore';
    case 'transfer':
      return 'Move to library';
    default:
      return 'Import';
  }
}

/** One-line outcome, e.g. "12 files could not be imported; see /lib/.picmover/logs/x.log". */
export function outcome(j: Job): { tone: Tone; text: string } {
  const what = jobTitle(j);
  const log = j.logPath || 'the log';
  if (j.state === 'running' || j.state === 'queued') return { tone: 'muted', text: `${what} ${j.state}…` };
  if (isBackupKind(j.kind)) return backupOutcome(j, what, log);
  if (j.kind === 'transfer') return transferOutcome(j, what, log);
  const s = j.stats;
  // One status for the run, not an error per file left behind.
  if (j.kind === 'import' && s.sourceDisconnected) return { tone: 'warn', text: DISCONNECTED_MESSAGE };
  switch (exitCodeOf(j)) {
    case 0:
      return { tone: 'ok', text: `${what} finished.` };
    case 130:
      return { tone: 'warn', text: `${what} cancelled.` };
    case 2:
      if (s.errors > 0) return { tone: 'warn', text: `${s.errors} files could not be imported; see ${log}` };
      if (s.thumbsFailed > 0) return { tone: 'warn', text: `thumbnails failed for ${s.thumbsFailed} files; see ${log}` };
      return { tone: 'warn', text: j.error || `${what} finished with problems; see ${log}` };
    default:
      return { tone: 'error', text: `${what} failed: ${j.error || 'unknown error'}` };
  }
}

/**
 * Backup kinds use the backup CLI's exit codes (docs/design/backups.md):
 * 0 clean, 1 some files failed (scrub: damage found), 2 the target or the
 * library could not be used, 130 cancelled. The run's stats are in
 * `result` (`stats` stays the importer's zero counters); exit 1 carries
 * the server's message in `error`.
 */
function backupOutcome(j: Job, what: string, log: string): { tone: Tone; text: string } {
  const summary = runSummary(j.kind, j.result ?? {});
  const code = exitCodeOf(j);
  if (j.state === 'cancelled' || code === 130) return { tone: 'warn', text: `${what} cancelled${summary && j.result ? ` (${summary})` : ''}.` };
  // Exit 1 is a partial success whatever state the job ended in.
  if (j.exitCode === 1) {
    if (j.error) return { tone: 'warn', text: `${capitalize(j.error)}${j.logPath ? ` (${j.logPath})` : ''}` };
    const why = j.kind === 'scrub' ? 'found damaged files' : 'finished, but some files failed';
    return { tone: 'warn', text: `${what} ${why}${summary ? ` (${summary})` : ''}; see ${log}` };
  }
  if (j.state === 'failed' || code >= 2) return { tone: 'error', text: `${what} failed: ${j.error || 'unknown error'}` };
  return { tone: 'ok', text: `${what} finished${summary ? `: ${summary}` : ''}.` };
}

/** "300 moved, 12 already there, 1 restored, 2 failed" ("" before the job has a result). */
export function transferSummary(j: Job): string {
  const r = toTransferResult(j.result);
  if (!r) return '';
  const parts = [`${formatCount(r.moved)} moved`];
  if (r.existing) parts.push(`${formatCount(r.existing)} already there`);
  if (r.restored) parts.push(`${formatCount(r.restored)} restored`);
  if (r.failedCount) parts.push(`${formatCount(r.failedCount)} failed`);
  if (r.sourcePending) parts.push(`${formatCount(r.sourcePending)} still here too`);
  return parts.join(', ');
}

/** A move uses the import exit codes: 0, 2 some files failed, 130 cancelled, 1 failed. */
function transferOutcome(j: Job, what: string, log: string): { tone: Tone; text: string } {
  const summary = transferSummary(j);
  switch (exitCodeOf(j)) {
    case 0:
      return { tone: 'ok', text: `${what} finished${summary ? `: ${summary}` : ''}.` };
    case 130:
      return { tone: 'warn', text: `${what} cancelled${summary ? ` (${summary})` : ''}.` };
    case 2:
      // The server says what is not done (files that failed, originals
      // still here, interrupted moves it could not finish).
      if (j.error) return { tone: 'warn', text: `${what} finished with problems${summary ? ` (${summary})` : ''}: ${j.error}` };
      return { tone: 'warn', text: `${what} finished, but some files failed${summary ? ` (${summary})` : ''}; see ${log}` };
    default:
      return { tone: 'error', text: `${what} failed: ${j.error || 'unknown error'}` };
  }
}

/**
 * The short status of a running backup-kind job for a live region: it
 * changes with the phase only ("Backup: copying"), never with counters.
 */
export function backupStatusText(j: Job): string {
  return `${jobTitle(j)}: ${j.state === 'queued' ? 'queued' : phaseLabel(j.kind, j.phase?.phase).toLowerCase()}`;
}

const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

/** The run stats of a finished backup-kind job in one line ("" when there are none yet). */
export function backupJobSummary(j: Job): string {
  return j.result ? runSummary(j.kind, j.result) : '';
}

export const resultLabels: Record<string, string> = {
  imported: 'Imported',
  imported_existing: 'Already in place',
  duplicate: 'Duplicate',
  skipped_small: 'Skipped (small)',
  error: 'Error',
};

export const kindLabels: Record<string, string> = {
  image: 'Photo',
  image_raw: 'RAW',
  video: 'Video',
};
