// Wording for import records (docs/design/imports.md "Web"): titles, mode
// and outcome labels, the counts line and the list date. Pure; see
// imports.test.ts.

import type { ImportRecord, ImportRef } from './api';
import { formatAgo, formatCount } from './format';

/** "Summer trip", else the source's base name, else "Import #12". */
export function importTitle(r: Pick<ImportRecord, 'id' | 'name'> & { sourceName?: string }): string {
  return r.name.trim() || r.sourceName?.trim() || `Import #${r.id}`;
}

const MODES: Record<string, string> = { copy: 'Copy', move: 'Move', rebuild: 'Rebuild', record: 'Record' };

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

/** "12 new, 3 already there, 1 error"; zero counts are left out. */
export function importCounts(r: Pick<ImportRecord, 'imported' | 'existing' | 'duplicates' | 'skipped' | 'errors'>): string {
  const parts: string[] = [];
  if (r.imported) parts.push(`${formatCount(r.imported)} new`);
  if (r.existing) parts.push(`${formatCount(r.existing)} already there`);
  if (r.duplicates) parts.push(plural(r.duplicates, 'duplicate', 'duplicates'));
  if (r.skipped) parts.push(`${formatCount(r.skipped)} skipped`);
  if (r.errors) parts.push(plural(r.errors, 'error', 'errors'));
  return parts.length ? parts.join(', ') : 'Nothing imported';
}

const WEEK = 7 * 86400 * 1000;
const dateFormats = new Map<string, Intl.DateTimeFormat>();

/** Relative under a week ("3 hours ago", "yesterday"), else the local date; "" when unknown. */
export function formatRecent(rfc3339: string | null | undefined, now: number = Date.now(), locale?: string): string {
  if (!rfc3339) return '';
  const d = new Date(rfc3339);
  if (Number.isNaN(d.getTime())) return '';
  if (Math.abs(now - d.getTime()) < WEEK) return formatAgo(d, now, locale);
  let f = dateFormats.get(locale ?? '');
  if (!f) {
    f = new Intl.DateTimeFormat(locale, { dateStyle: 'medium' });
    dateFormats.set(locale ?? '', f);
  }
  return f.format(d);
}

/**
 * The lightbox line: "Imported 10 Oct 2026, 12:00 from DCIM", plus
 * " · <name>" when the record is named; a rebuild says "Recorded by a
 * rebuild". The source name is left out when the server does not send it.
 */
export function importLine(r: ImportRef, when: string): string {
  const verb = r.mode === 'rebuild' ? 'Recorded by a rebuild' : 'Imported';
  const from = r.sourceName ? ` from ${r.sourceName}` : '';
  const name = r.name.trim() ? ` · ${r.name.trim()}` : '';
  return `${verb}${when ? ` ${when}` : ''}${from}${name}`;
}
