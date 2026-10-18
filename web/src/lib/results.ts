// The files of an import job in one list (the Import page's Recent files):
// the kept results and the kept errors merged, newest first, and the
// filters by outcome and by media kind. Pure; see results.test.ts.

import type { JobResult } from './api';

export type StatusFilter = 'all' | 'error' | 'imported' | 'imported_existing' | 'duplicate' | 'skipped_small';
export type KindFilter = 'all' | 'image' | 'image_raw' | 'video';

/**
 * One list, newest first (by seq): the jobs store keeps the last results
 * and, longer, the last errors; a result in both appears once.
 */
export function mergeResults(results: JobResult[], errors: JobResult[]): JobResult[] {
  const seen = new Set<number>();
  const out: JobResult[] = [];
  for (const r of [...results, ...errors]) {
    if (seen.has(r.seq)) continue;
    seen.add(r.seq);
    out.push(r);
  }
  return out.sort((a, b) => b.seq - a.seq);
}

export function filterResults(list: JobResult[], status: StatusFilter, kind: KindFilter): JobResult[] {
  return list.filter((r) => (status === 'all' || r.status === status) && (kind === 'all' || r.kind === kind));
}

/** Results per status. */
export function statusCounts(list: JobResult[]): Partial<Record<string, number>> {
  const out: Partial<Record<string, number>> = {};
  for (const r of list) out[r.status] = (out[r.status] ?? 0) + 1;
  return out;
}

/** Results per media kind (results without one are not counted). */
export function kindCounts(list: JobResult[]): Partial<Record<string, number>> {
  const out: Partial<Record<string, number>> = {};
  for (const r of list) if (r.kind) out[r.kind] = (out[r.kind] ?? 0) + 1;
  return out;
}
