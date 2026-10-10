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

/**
 * Renders a capture time as recorded, e.g. "14 Jul 2024, 13:05:22 UTC+03:00".
 * Undated (null) renders as "Undated".
 */
export function formatTakenAt(wallClock: string | null | undefined, offset: number | null | undefined, locale?: string): string {
  const d = parseWallClock(wallClock);
  if (!d) return wallClock ? wallClock : 'Undated';
  const s = dtf(locale, { dateStyle: 'medium', timeStyle: 'medium', timeZone: 'UTC' }).format(d);
  return offset === null || offset === undefined ? s : `${s} ${formatOffset(offset)}`;
}

/** Short date only ("14 Jul 2024"), wall-clock; "Undated" for null. */
export function formatWallDate(wallClock: string | null | undefined, locale?: string): string {
  const d = parseWallClock(wallClock);
  if (!d) return 'Undated';
  return dtf(locale, { dateStyle: 'medium', timeZone: 'UTC' }).format(d);
}

/** A real instant (RFC 3339 with zone), rendered in the viewer's zone. */
export function formatInstant(rfc3339: string | null | undefined, locale?: string): string {
  if (!rfc3339) return '-';
  const d = new Date(rfc3339);
  if (Number.isNaN(d.getTime())) return rfc3339;
  return dtf(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(d);
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
