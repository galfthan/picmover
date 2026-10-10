// Wording for job summaries, mirroring the CLI (cmd/import.go) so the UI and
// the terminal say the same thing.

import type { Job } from './api';

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
    default:
      return 'Import';
  }
}

/** One-line outcome, e.g. "12 files could not be imported; see /lib/.picmover/logs/x.log". */
export function outcome(j: Job): { tone: Tone; text: string } {
  const what = jobTitle(j);
  const log = j.logPath || 'the log';
  if (j.state === 'running' || j.state === 'queued') return { tone: 'muted', text: `${what} ${j.state}…` };
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

export const resultLabels: Record<string, string> = {
  imported: 'Imported',
  imported_existing: 'Already in place',
  duplicate: 'Duplicate',
  skipped_small: 'Skipped (small)',
  error: 'Error',
};
