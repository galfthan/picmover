// Texts and small pure helpers of the admin Share page and ShareDialog
// (docs/design/lan-share.md "Entry points"); see sharetext.test.ts.

import type { Share, ShareKind } from './api';
import { formatAgo } from './format';

/** "Album: Summer 2026 in peter, 48 photos" / "Libraries: peter, penny". */
export function scopeText(s: Share): string {
  if (s.kind === 'album') {
    const a = s.album;
    const lib = s.library?.name ?? '?';
    if (!a) return `Album in ${lib}`;
    return `Album: ${a.name || `#${a.id}`} in ${lib}, ${a.count} ${a.count === 1 ? 'photo' : 'photos'}`;
  }
  if (s.kind !== 'libraries') return `Unknown kind “${s.kind}”; update picmover`;
  return `${s.libraries.length === 1 ? 'Library' : 'Libraries'}: ${s.libraries.map((l) => l.name).join(', ') || 'none'}`;
}

/** Why a share does not work now, for its card; "" when it is active. */
export function stateText(s: Share): string {
  switch (s.state) {
    case 'active':
      return '';
    case 'paused':
      return 'Paused';
    case 'expired':
      return 'Expired';
    case 'gone':
      if (s.kind === 'album') return s.library?.missing ? 'Library missing' : 'Album deleted';
      return 'Library missing';
    case 'unavailable':
      return 'Not in this workspace';
    default:
      return s.state;
  }
}

/** "Never expires", "Expires in 12 days", "Ended 3 days ago". */
export function expiryText(expiresAt: string | null, now = Date.now()): string {
  if (!expiresAt) return 'Never expires';
  const t = Date.parse(expiresAt);
  if (!Number.isFinite(t)) return 'Never expires';
  return t <= now ? `Ended ${formatAgo(new Date(t), now)}` : `Expires ${formatAgo(new Date(t), now)}`;
}

export type ExpiryChoice = 'keep' | 'never' | '1d' | '7d' | '30d' | '90d' | 'date';

export const EXPIRY_CHOICES: readonly { value: Exclude<ExpiryChoice, 'keep'>; label: string }[] = [
  { value: '1d', label: '1 day' },
  { value: '7d', label: '7 days' },
  { value: '30d', label: '30 days' },
  { value: '90d', label: '90 days' },
  { value: 'date', label: 'Until a date' },
  { value: 'never', label: 'Never' },
];

const DAY = 86_400;

/** The dialog's default: 30 days for an album share, never for a library share (Q17). */
export function defaultExpiry(kind: ShareKind | string): ExpiryChoice {
  return kind === 'album' ? '30d' : 'never';
}

/**
 * The expiry as an absolute time (RFC 3339), null for never, undefined to
 * leave it as it is (`keep`, or a date that is not valid). `day` is
 * "YYYY-MM-DD"; the share then ends at the end of that local day.
 */
export function expiryAt(choice: ExpiryChoice, day: string, now = Date.now()): string | null | undefined {
  switch (choice) {
    case 'keep':
      return undefined;
    case 'never':
      return null;
    case 'date': {
      const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day);
      if (!m) return undefined;
      const end = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]) + 1);
      return Number.isNaN(end.getTime()) || end.getTime() <= now ? undefined : end.toISOString();
    }
    default:
      return new Date(now + Number(choice.slice(0, -1)) * DAY * 1000).toISOString();
  }
}

/** Seconds from now for the relative choices (the create body's `expiresIn`); null for the others. */
export function expirySeconds(choice: ExpiryChoice): number | null {
  return /^\d+d$/.test(choice) ? Number(choice.slice(0, -1)) * DAY : null;
}

/** The first `groups` byte groups of a fingerprint ("AB:CD:EF"), for "starts with". */
export function fingerprintStart(fp: string, groups = 3): string {
  return fp.split(':').slice(0, groups).join(':');
}

/** "Anna's iPhone" checks: 1–60 characters, no control characters; null when fine. */
export function deviceNameProblem(raw: string): string | null {
  const name = raw.trim();
  if (!name) return 'Enter a name.';
  if ([...name].length > 60) return 'At most 60 characters.';
  if (/[\u0000-\u001f\u007f]/.test(name)) return 'A name cannot contain control characters.';
  return null;
}

/** Share names: 1–60 characters, no control characters; null when fine. */
export const shareNameProblem = deviceNameProblem;
