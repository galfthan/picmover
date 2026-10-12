// Wording for import records (docs/design/imports.md "Web"): titles, mode
// and outcome labels, the counts line and the list date. Pure; see
// imports.test.ts.

import type { ImportRecord, ImportRef } from './api';
import { formatAgo, formatCount, formatLocalDay } from './format';

/** "Summer trip", else the source's base name, else "Import #12". */
export function importTitle(r: Pick<ImportRecord, 'id' | 'name'> & { sourceName?: string }): string {
  return r.name.trim() || r.sourceName?.trim() || `Import #${r.id}`;
}

// "record" is a rebuild session of an earlier version, backfilled from the catalog: a rebuild too.
const MODES: Record<string, string> = { copy: 'Copy', move: 'Move', rebuild: 'Rebuild', record: 'Rebuild' };

/**
 * A rebuild (mode rebuild, or record: one backfilled from the catalog)
 * records the whole library in place; its record keeps no items and has
 * no picture grid of its own.
 */
export function isRebuild(mode: string): boolean {
  return mode === 'rebuild' || mode === 'record';
}

export function modeLabel(mode: string): string {
  return MODES[mode] ?? (mode || 'Import');
}

const OUTCOMES: Record<string, string> = { running: 'Running', cancelled: 'Cancelled', failed: 'Failed', done: 'Done' };

/** Label of an outcome worth showing; null for a plain finished import. */
export function outcomeLabel(outcome: string): string | null {
  if (outcome === 'done' || outcome === '') return null;
  return OUTCOMES[outcome] ?? outcome;
}

const plural = (n: number, one: string, many: string) => `${formatCount(n)} ${n === 1 ? one : many}`;

/**
 * "12 new, 3 already there, 1 error"; zero counts are left out. A rebuild
 * (isRebuild) found every file in place: "1,234 files recorded".
 */
export function importCounts(r: Pick<ImportRecord, 'imported' | 'existing' | 'duplicates' | 'skipped' | 'errors'> & { mode?: string }): string {
  const parts: string[] = [];
  if (r.mode !== undefined && isRebuild(r.mode)) {
    const n = r.imported + r.existing;
    if (n) parts.push(plural(n, 'file recorded', 'files recorded'));
  } else {
    if (r.imported) parts.push(`${formatCount(r.imported)} new`);
    if (r.existing) parts.push(`${formatCount(r.existing)} already there`);
  }
  if (r.duplicates) parts.push(plural(r.duplicates, 'duplicate', 'duplicates'));
  if (r.skipped) parts.push(`${formatCount(r.skipped)} skipped`);
  if (r.errors) parts.push(plural(r.errors, 'error', 'errors'));
  return parts.length ? parts.join(', ') : r.mode !== undefined && isRebuild(r.mode) ? 'Nothing recorded' : 'Nothing imported';
}

const WEEK = 7 * 86400 * 1000;
/**
 * Relative under a week ("3 hours ago", "yesterday"), else the local date
 * ("01/09/2026"); "" when unknown. `locale` only picks the relative wording.
 */
export function formatRecent(rfc3339: string | null | undefined, now: number = Date.now(), locale?: string): string {
  if (!rfc3339) return '';
  const d = new Date(rfc3339);
  if (Number.isNaN(d.getTime())) return '';
  if (Math.abs(now - d.getTime()) < WEEK) return formatAgo(d, now, locale);
  return formatLocalDay(d);
}

/**
 * The lightbox line: "Imported 10/10/2026 12:00 from DCIM", plus
 * " · <name>" when the record is named; a rebuild (isRebuild) says
 * "Recorded 10/10/2026 12:00" (nothing came from anywhere). The source
 * name is left out when the server does not send it.
 */
export function importLine(r: ImportRef, when: string): string {
  const rebuild = isRebuild(r.mode);
  const verb = rebuild ? 'Recorded' : 'Imported';
  const from = r.sourceName && !rebuild ? ` from ${r.sourceName}` : '';
  const name = r.name.trim() ? ` · ${r.name.trim()}` : '';
  return `${verb}${when ? ` ${when}` : ''}${from}${name}`;
}
