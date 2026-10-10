// localStorage can be unavailable (private windows, blocked site data) or
// throw; every access goes through these helpers.

export function loadJSON<T>(key: string, valid: (v: unknown) => v is T): T | null {
  try {
    const s = window.localStorage.getItem(key);
    if (s === null) return null;
    const v: unknown = JSON.parse(s);
    return valid(v) ? v : null;
  } catch {
    return null;
  }
}

export function saveJSON(key: string, value: unknown): void {
  try {
    window.localStorage.setItem(key, JSON.stringify(value));
  } catch {
    // ignore: a convenience only
  }
}

export function isNumber(v: unknown): v is number {
  return typeof v === 'number' && Number.isFinite(v);
}

export function isBoolean(v: unknown): v is boolean {
  return typeof v === 'boolean';
}
