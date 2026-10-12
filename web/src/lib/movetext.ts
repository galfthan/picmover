// Wording of moving photos to another library (docs/design/library-move.md
// "Frontend"): the plan's lines, the free-space refusal, failure reasons
// and the result. Pure; see movetext.test.ts.

import type { Failure, InterruptedMove, TransferPlan } from './api';
import { formatBytes, formatCount } from './format';

/** Space the server keeps free on the target's volume (the run is refused within it). */
export const SPACE_MARGIN = 1024 * 1024 * 1024;

export const photos = (n: number) => `${formatCount(n)} ${n === 1 ? 'photo' : 'photos'}`;

/** What the selection minus the refused items amounts to: those leave this library. */
export function movable(p: TransferPlan): number {
  return p.move.count + p.existing.count + p.restore.count;
}

/** "Not enough space in Penny: 4.1 GiB needed, 2.0 GiB free."; null when the copy fits (or is a rename). */
export function spaceProblem(p: TransferPlan, target: string): string | null {
  if (p.sameVolume || p.free === null || p.move.count === 0) return null;
  if (p.move.bytes + SPACE_MARGIN <= p.free) return null;
  const need = formatBytes(p.move.bytes);
  const free = formatBytes(p.free);
  if (p.move.bytes <= p.free) return `Not enough space in ${target}: ${need} needed plus ${formatBytes(SPACE_MARGIN)} to spare, ${free} free.`;
  return `Not enough space in ${target}: ${need} needed, ${free} free.`;
}

/**
 * The plan in sentences: the headline ("Move 312 photos (4.1 GiB) to
 * Penny."), then what happens to duplicates and to items in the target's
 * trash, then how the files travel.
 */
export function planLines(p: TransferPlan, target: string): { headline: string; lines: string[] } {
  const n = movable(p);
  if (n === 0) return { headline: 'Nothing here can be moved.', lines: [] };
  const size = p.move.bytes > 0 ? ` (${formatBytes(p.move.bytes)})` : '';
  const lines: string[] = [];
  const e = p.existing.count;
  if (e) lines.push(e === 1 ? '1 is already there; its copy here goes to the trash.' : `${formatCount(e)} are already there; their copies here go to the trash.`);
  const r = p.restore.count;
  if (r) {
    lines.push(
      r === 1
        ? `1 is in ${target}'s trash; it comes back there, and its copy here goes to the trash.`
        : `${formatCount(r)} are in ${target}'s trash; they come back there, and their copies here go to the trash.`,
    );
  }
  if (p.move.count > 0) {
    lines.push(p.sameVolume ? 'Same drive: instant.' : `Copies ${formatBytes(p.move.bytes)} across drives, then puts the originals in this library's trash.`);
  }
  return { headline: `Move ${photos(n)}${size} to ${target}.`, lines };
}

/** Failures grouped by reason, most frequent first ("" reads as "unknown error"). */
export function reasons(failed: readonly Failure[]): { error: string; count: number }[] {
  const m = new Map<string, number>();
  for (const f of failed) {
    const k = f.error || 'unknown error';
    m.set(k, (m.get(k) ?? 0) + 1);
  }
  return [...m.entries()].map(([error, count]) => ({ error, count })).sort((a, b) => b.count - a.count || a.error.localeCompare(b.error));
}

/** "3 cannot be moved:" over a refused list. */
export function refusedHeading(n: number): string {
  return `${formatCount(n)} ${n === 1 ? 'photo' : 'photos'} cannot be moved:`;
}

/** "A move from peter to penny was interrupted." with names when known. */
export function interruptedText(m: InterruptedMove, name: (id: string) => string): string {
  return `A move from ${name(m.from)} to ${name(m.to)} was interrupted.`;
}

/** The banner text of library `lib` for one of its interrupted moves. */
export function interruptedFor(lib: string, m: InterruptedMove, name: (id: string) => string): string {
  if (m.to === lib) return `A move from ${name(m.from)} was interrupted.`;
  if (m.from === lib) return `A move to ${name(m.to)} was interrupted.`;
  return interruptedText(m, name);
}

export interface MoveTotals {
  moved: number;
  existing: number;
  restored: number;
  failed: Failure[];
  failedCount: number;
  /** The target's import records, one per job. */
  records: number[];
}

export function emptyTotals(): MoveTotals {
  return { moved: 0, existing: 0, restored: 0, failed: [], failedCount: 0, records: [] };
}

/** The finished run in sentences: what was moved, then duplicates and restores. */
export function resultLines(t: MoveTotals, target: string): string[] {
  const out = [`Moved ${photos(t.moved)} to ${target}.`];
  if (t.existing) {
    out.push(
      t.existing === 1
        ? '1 was already there; its copy here is in the trash.'
        : `${formatCount(t.existing)} were already there; their copies here are in the trash.`,
    );
  }
  if (t.restored) out.push(`${formatCount(t.restored)} came back from ${target}'s trash.`);
  return out;
}

/** "2 could not be moved:" (failures listed below it). */
export function failedHeading(n: number): string {
  return `${formatCount(n)} ${n === 1 ? 'photo' : 'photos'} could not be moved:`;
}

/** Whether a finished job of a run lets the next one start: done, or done with some files failed (exit 2). */
export function continues(state: string, exitCode: number | null | undefined): boolean {
  return state === 'done' || exitCode === 2;
}
