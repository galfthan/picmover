// Formatting helpers. Capture times arrive as wall-clock strings without a
// zone ("2024-07-14T13:05:22") plus a separate UTC offset in seconds. They
// must be shown exactly as recorded, so they are parsed into a UTC instant
// and always rendered with timeZone 'UTC' — never in the viewer's zone.

const formatters = new Map<string, Intl.DateTimeFormat>();

/** Intl.DateTimeFormat is costly to construct; tiles format thousands of dates. */
function dtf(locale: string | undefined, opts: Intl.DateTimeFormatOptions): Intl.DateTimeFormat {
  const key = `${locale ?? ''}|${JSON.stringify(opts)}`;
  let f = formatters.get(key);
  if (!f) {
    f = new Intl.DateTimeFormat(locale, opts);
    formatters.set(key, f);
  }
  return f;
}

const WALL_RE = /^(\d{4})-(\d{2})-(\d{2})(?:[T ](\d{2}):(\d{2})(?::(\d{2})(?:\.\d+)?)?)?$/;

/** Parses a wall-clock string into a Date whose UTC fields equal it. */
export function parseWallClock(s: string | null | undefined): Date | null {
  if (!s) return null;
  const m = WALL_RE.exec(s);
  if (!m) return null;
  const [, y, mo, d, h, mi, se] = m;
  // Not Date.UTC(y, ...): it maps years 0-99 to 1900-1999.
  const out = new Date(0);
  out.setUTCFullYear(Number(y), Number(mo) - 1, Number(d));
  out.setUTCHours(Number(h ?? 0), Number(mi ?? 0), Number(se ?? 0), 0);
  return Number.isNaN(out.getTime()) ? null : out;
}

/** "UTC+03:00" for an offset in seconds east of UTC. */
export function formatOffset(offset: number): string {
  const sign = offset < 0 ? '-' : '+';
  const abs = Math.abs(Math.trunc(offset));
  const h = Math.floor(abs / 3600);
  const m = Math.floor((abs % 3600) / 60);
  return `UTC${sign}${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`;
}

// Dates are always shown day first, "14/07/2024", whatever the browser's
// locale (docs/design/management.md "Dates and the calendar picker (web)").
// Times use the 24-hour clock. Month names (grid headers, "every year"
// filters) still come from Intl.

const p2 = (n: number) => String(n).padStart(2, '0');
const p4 = (n: number) => String(n).padStart(4, '0');

/** "14/07/2024" from a Date's UTC fields. */
function utcDay(d: Date): string {
  return `${p2(d.getUTCDate())}/${p2(d.getUTCMonth() + 1)}/${p4(d.getUTCFullYear())}`;
}

/**
 * Renders a capture time as recorded, e.g. "14/07/2024 13:05:22 UTC+03:00".
 * Undated (null) renders as "Undated".
 */
export function formatTakenAt(wallClock: string | null | undefined, offset: number | null | undefined): string {
  const d = parseWallClock(wallClock);
  if (!d) return wallClock ? wallClock : 'Undated';
  const s = `${utcDay(d)} ${p2(d.getUTCHours())}:${p2(d.getUTCMinutes())}:${p2(d.getUTCSeconds())}`;
  return offset === null || offset === undefined ? s : `${s} ${formatOffset(offset)}`;
}

/** Date only ("14/07/2024"), wall-clock; "Undated" for null. */
export function formatWallDate(wallClock: string | null | undefined): string {
  const d = parseWallClock(wallClock);
  return d ? utcDay(d) : 'Undated';
}

/** The viewer's local date of an instant: "14/07/2024". */
export function formatLocalDay(d: Date): string {
  return `${p2(d.getDate())}/${p2(d.getMonth() + 1)}/${p4(d.getFullYear())}`;
}

/** A real instant (RFC 3339 with zone), in the viewer's zone and 24-hour clock: "14/07/2024 13:05". */
export function formatInstant(rfc3339: string | null | undefined): string {
  if (!rfc3339) return '-';
  const d = new Date(rfc3339);
  if (Number.isNaN(d.getTime())) return rfc3339;
  return `${formatLocalDay(d)} ${p2(d.getHours())}:${p2(d.getMinutes())}`;
}

/** A calendar day "2024-07-14" as "14/07/2024"; anything else is returned unchanged ("" for none). */
export function formatDay(day: string | null | undefined): string {
  if (!day) return '';
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day);
  return m ? `${m[3]}/${m[2]}/${m[1]}` : day;
}

const DMY_RE = /^\s*(\d{1,2})([/.])(\d{1,2})\2(\d{4})\s*$/;

/**
 * Parses a typed day to "yyyy-mm-dd": "14/07/2024", "4/7/2024" and
 * "14.07.2024" are accepted; the day must exist (not 29/02/2023). Null
 * for anything else.
 */
export function parseDay(s: string | null | undefined): string | null {
  if (!s) return null;
  const m = DMY_RE.exec(s);
  if (!m) return null;
  const day = Number(m[1]);
  const month = Number(m[3]);
  const year = m[4]!;
  if (month < 1 || month > 12 || day < 1 || day > 31) return null;
  const iso = `${year}-${p2(month)}-${p2(day)}`;
  const d = parseWallClock(iso);
  return d && utcDay(d) === `${p2(day)}/${p2(month)}/${year}` ? iso : null;
}

/** Binary units like the CLI: "512 B", "1.5 KiB", "2.0 GiB". */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '-';
  if (n < 1024) return `${Math.trunc(n)} B`;
  const units = ['KiB', 'MiB', 'GiB', 'TiB', 'PiB', 'EiB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${units[i]}`;
}

/** Video durations: "0:07", "1:05", "1:02:03". */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return '-';
  const total = Math.round(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const ss = String(s).padStart(2, '0');
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${ss}` : `${m}:${ss}`;
}

/** Elapsed wall time for jobs: "42s", "3m 05s", "1h 02m". */
export function formatElapsed(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) ms = 0;
  const total = Math.floor(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h > 0) return `${h}h ${String(m).padStart(2, '0')}m`;
  if (m > 0) return `${m}m ${String(s).padStart(2, '0')}s`;
  return `${s}s`;
}

/** Month name for 1..12 ("July"; style 'short' gives "Jul"). 0 → "Undated". */
export function monthName(month: number, style: 'long' | 'short' = 'long', locale?: string): string {
  if (month < 1 || month > 12) return 'Undated';
  return dtf(locale, { month: style, timeZone: 'UTC' }).format(new Date(Date.UTC(2000, month - 1, 1)));
}

/** "July 2024", or "Undated" for the 0/0 bucket. */
export function bucketLabel(year: number, month: number, locale?: string): string {
  if (year === 0 && month === 0) return 'Undated';
  return `${monthName(month, 'long', locale)} ${year}`;
}

const intFmt = new Intl.NumberFormat('en-US');

/** Thousands separators: 1234 → "1,234". */
export function formatCount(n: number): string {
  return intFmt.format(n);
}

/** Decimal degrees with 5 decimals (~1 m). */
export function formatCoord(v: number): string {
  return v.toFixed(5);
}

/**
 * Parses a wall-clock string that the server wrote in its own local zone
 * (trash `deletedAt`) as a local instant of this browser. The server runs
 * on the same machine as the browser, so the zones agree.
 */
export function parseLocalWallClock(s: string | null | undefined): Date | null {
  const d = parseWallClock(s);
  if (!d) return null;
  const out = new Date(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate(), d.getUTCHours(), d.getUTCMinutes(), d.getUTCSeconds());
  out.setFullYear(d.getUTCFullYear()); // years 0-99
  return Number.isNaN(out.getTime()) ? null : out;
}

const rtfCache = new Map<string, Intl.RelativeTimeFormat>();

/** "3 minutes ago", "yesterday", "2 months ago"; "just now" under a minute. */
export function formatAgo(when: Date | null, now: number = Date.now(), locale?: string): string {
  if (!when) return '';
  const sec = Math.round((when.getTime() - now) / 1000);
  if (Math.abs(sec) < 60) return 'just now';
  let rtf = rtfCache.get(locale ?? '');
  if (!rtf) {
    rtf = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' });
    rtfCache.set(locale ?? '', rtf);
  }
  const steps: [Intl.RelativeTimeFormatUnit, number][] = [
    ['minute', 60],
    ['hour', 3600],
    ['day', 86400],
    ['week', 7 * 86400],
    ['month', 30 * 86400],
    ['year', 365 * 86400],
  ];
  let unit: Intl.RelativeTimeFormatUnit = 'minute';
  let size = 60;
  for (const [u, s] of steps) {
    if (Math.abs(sec) >= s) {
      unit = u;
      size = s;
    }
  }
  return rtf.format(Math.round(sec / size), unit);
}

/**
 * A server time that may be Unix seconds, an RFC 3339 instant or a
 * wall-clock string in the server's zone (the similar run's `at`), as a Date.
 */
export function parseWhen(v: number | string | null | undefined): Date | null {
  if (typeof v === 'number') return Number.isFinite(v) && v > 0 ? new Date(v * 1000) : null;
  if (!v) return null;
  if (/[zZ]|[+-]\d{2}:?\d{2}$/.test(v)) {
    const d = new Date(v);
    return Number.isNaN(d.getTime()) ? null : d;
  }
  return parseLocalWallClock(v);
}
