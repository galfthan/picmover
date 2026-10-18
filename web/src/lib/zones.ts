// IANA time zones for the import wizard and the library's home zone.

/** The browser's zone ("Europe/Helsinki"), "UTC" when it cannot be read. */
export function browserZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

/** Every zone the browser knows, UTC first, plus the given ones. */
export function zoneList(...extra: (string | null | undefined)[]): string[] {
  let list: string[] = [];
  try {
    list = Intl.supportedValuesOf('timeZone');
  } catch {
    list = [];
  }
  const out = new Set(['UTC', ...list]);
  for (const z of extra) if (z) out.add(z);
  return [...out].sort((a, b) => (a === 'UTC' ? -1 : b === 'UTC' ? 1 : a.localeCompare(b)));
}
