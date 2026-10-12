// Changes made to items by the selection bar, the lightbox or the
// duplicates page, so the page underneath can update in place (ratings on
// tiles, items leaving the trash or an album) without reloading.

export type Change =
  | { kind: 'rating'; hashes: string[]; rating: number }
  /** Moved to the trash. */
  | { kind: 'trashed'; hashes: string[] }
  /** Back from the trash. */
  | { kind: 'restored'; hashes: string[] }
  /** Deleted permanently. */
  | { kind: 'deleted'; hashes: string[] }
  /** Moved to another library (gone from this one's live rows; across volumes in its trash). */
  | { kind: 'moved'; lib: string; hashes: string[] }
  | { kind: 'album'; albumId: number; added: string[]; removed: string[] }
  | { kind: 'tags'; hashes: string[] };

type Listener = (c: Change) => void;
const listeners = new Set<Listener>();

/** Subscribes; returns the unsubscribe function (usable as an $effect's return value). */
export function onChange(fn: Listener): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function emitChange(c: Change): void {
  for (const fn of [...listeners]) fn(c);
}
