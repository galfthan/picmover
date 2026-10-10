// Wording for job summaries, mirroring the CLI (cmd/import.go) so the UI and
// the terminal say the same thing.

import { isBackupKind, type Job } from './api';
import { runSummary } from './backupinfo';

export type Tone = 'ok' | 'warn' | 'error' | 'muted';

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
  const s = j.stats;
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
 * library could not be used, 130 cancelled.
 */
function backupOutcome(j: Job, what: string, log: string): { tone: Tone; text: string } {
  const stats = j.stats as unknown as Record<string, unknown>;
  const summary = runSummary(j.kind, stats);
  const code = exitCodeOf(j);
  if (j.state === 'cancelled' || code === 130) return { tone: 'warn', text: `${what} cancelled.` };
  // Exit 1 is a partial success whatever state the job ended in.
  if (j.exitCode === 1) {
    const why = j.kind === 'scrub' ? 'found damaged files' : 'finished, but some files failed';
    return { tone: 'warn', text: `${what} ${why}${summary ? ` (${summary})` : ''}; see ${log}` };
  }
  if (j.state === 'failed' || code >= 2) return { tone: 'error', text: `${what} failed: ${j.error || 'unknown error'}` };
  return { tone: 'ok', text: `${what} finished${summary ? `: ${summary}` : ''}.` };
}

export const resultLabels: Record<string, string> = {
  imported: 'Imported',
  imported_existing: 'Already in place',
  duplicate: 'Duplicate',
  skipped_small: 'Skipped (small)',
  error: 'Error',
};
