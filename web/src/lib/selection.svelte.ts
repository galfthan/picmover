// Multi-select in the grid (docs/design/management.md "Selection and the
// selection bar"). The selection is a set of content hashes, kept across
// scrolling and the lightbox, cleared when the library, the page or the
// filters change (`scope`). Shift-click applies a range from the anchor
// (the last clicked tile) to the clicked tile in display order: it selects
// the range when the clicked tile was not selected and deselects it when it
// was. The grid loads the months in between first; a month that still
// could not be loaded is skipped and counted in `skipped`.

import { SvelteSet } from 'svelte/reactivity';

/** One month of the grid in display order; `hashes` is null while it is not loaded. */
export interface OrderSegment {
  count: number;
  hashes: readonly string[] | null;
}

export interface RangeResult {
  /** Hashes from one end to the other, inclusive, in display order. */
  hashes: string[];
  /** Items in unloaded months between the two ends (not selected). */
  skipped: number;
}

function locate(segments: readonly OrderSegment[], h: string): [number, number] | null {
  for (let s = 0; s < segments.length; s++) {
    const list = segments[s]?.hashes;
    if (!list) continue;
    const i = list.indexOf(h);
    if (i >= 0) return [s, i];
  }
  return null;
}

/** The hashes between a and b (either order); null when either is not in a loaded segment. */
export function rangeBetween(segments: readonly OrderSegment[], a: string, b: string): RangeResult | null {
  const pa = locate(segments, a);
  const pb = locate(segments, b);
  if (!pa || !pb) return null;
  const [start, end] = pa[0] < pb[0] || (pa[0] === pb[0] && pa[1] <= pb[1]) ? [pa, pb] : [pb, pa];
  const hashes: string[] = [];
  let skipped = 0;
  for (let s = start[0]; s <= end[0]; s++) {
    const seg = segments[s];
    if (!seg) continue;
    if (!seg.hashes) {
      skipped += seg.count;
      continue;
    }
    const from = s === start[0] ? start[1] : 0;
    const to = s === end[0] ? end[1] : seg.hashes.length - 1;
    for (let i = from; i <= to; i++) {
      const h = seg.hashes[i];
      if (h !== undefined) hashes.push(h);
    }
  }
  return { hashes, skipped };
}

export class Selection {
  #set = new SvelteSet<string>();
  #scope = '';
  /** The last clicked hash: one end of a Shift-click range. */
  anchor = $state<string | null>(null);
  /** Items skipped by the last range because their month was not loaded. */
  skipped = $state(0);

  get size(): number {
    return this.#set.size;
  }

  has(h: string): boolean {
    return this.#set.has(h);
  }

  /** has() without subscribing to the set (for scans that already depend on `size`). */
  peek(h: string): boolean {
    return Set.prototype.has.call(this.#set, h);
  }

  /** The selected hashes (insertion order). */
  list(): string[] {
    return [...this.#set];
  }

  toggle(h: string): void {
    if (this.#set.has(h)) this.#set.delete(h);
    else this.#set.add(h);
    this.anchor = h;
    this.skipped = 0;
  }

  /**
   * Shift-click on `target`: the range from the anchor takes the state
   * `target` toggles to (selected when it was not, deselected when it was),
   * whatever the anchor's own state, and `target` becomes the anchor.
   * Without an anchor in the loaded months it toggles `target` alone.
   */
  selectRange(segments: readonly OrderSegment[], target: string): void {
    const a = this.anchor;
    const r = a !== null && a !== target ? rangeBetween(segments, a, target) : null;
    if (!r) {
      this.toggle(target);
      return;
    }
    const select = !this.#set.has(target);
    for (const h of r.hashes) {
      if (select) this.#set.add(h);
      else this.#set.delete(h);
    }
    this.skipped = r.skipped;
    this.anchor = target;
  }

  add(hashes: Iterable<string>): void {
    for (const h of hashes) this.#set.add(h);
  }

  /** Drops hashes that left the view (trashed, restored, removed from the album). */
  remove(hashes: Iterable<string>): void {
    let gone = false;
    for (const h of hashes) {
      this.#set.delete(h);
      if (h === this.anchor) gone = true;
    }
    if (gone) this.anchor = null;
    if (this.#set.size === 0) this.skipped = 0;
  }

  clear(): void {
    if (this.#set.size) this.#set.clear();
    this.anchor = null;
    this.skipped = 0;
  }

  /** Clears the selection when the scope (library + page + filters) changes. */
  setScope(key: string): void {
    if (key === this.#scope) return;
    this.#scope = key;
    this.clear();
  }
}

export const selection = new Selection();
