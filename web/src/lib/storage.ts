// localStorage and sessionStorage can be unavailable (private windows,
// blocked site data) or throw; every access goes through these helpers.
// localStorage outlives the tab; sessionStorage lives as long as the tab
// (a reload keeps it, a new tab starts empty).

function load<T>(store: () => Storage, key: string, valid: (v: unknown) => v is T): T | null {
  try {
    const s = store().getItem(key);
    if (s === null) return null;
    const v: unknown = JSON.parse(s);
    return valid(v) ? v : null;
  } catch {
    return null;
  }
}

function save(store: () => Storage, key: string, value: unknown): void {
  try {
    store().setItem(key, JSON.stringify(value));
  } catch {
    // ignore: a convenience only
  }
}

const local = () => window.localStorage;
const session = () => window.sessionStorage;

export function loadJSON<T>(key: string, valid: (v: unknown) => v is T): T | null {
  return load(local, key, valid);
}

export function saveJSON(key: string, value: unknown): void {
  save(local, key, value);
}

export function loadSession<T>(key: string, valid: (v: unknown) => v is T): T | null {
  return load(session, key, valid);
}

export function saveSession(key: string, value: unknown): void {
  save(session, key, value);
}

export function isNumber(v: unknown): v is number {
  return typeof v === 'number' && Number.isFinite(v);
}

export function isBoolean(v: unknown): v is boolean {
  return typeof v === 'boolean';
}
