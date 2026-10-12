import { describe, expect, it } from 'vitest';
import { rangeBetween, Selection, type OrderSegment } from './selection.svelte';

const seg = (...hashes: string[]): OrderSegment => ({ count: hashes.length, hashes });
const unloaded = (count: number): OrderSegment => ({ count, hashes: null });

describe('rangeBetween', () => {
  const segs = [seg('a', 'b', 'c'), unloaded(40), seg('d', 'e'), seg('f')];

  it('runs in display order whichever end comes first', () => {
    expect(rangeBetween(segs, 'b', 'c')).toEqual({ hashes: ['b', 'c'], skipped: 0 });
    expect(rangeBetween(segs, 'c', 'b')).toEqual({ hashes: ['b', 'c'], skipped: 0 });
    expect(rangeBetween(segs, 'e', 'f')).toEqual({ hashes: ['e', 'f'], skipped: 0 });
  });

  it('skips unloaded months and counts them', () => {
    expect(rangeBetween(segs, 'b', 'd')).toEqual({ hashes: ['b', 'c', 'd'], skipped: 40 });
    expect(rangeBetween(segs, 'f', 'a')).toEqual({ hashes: ['a', 'b', 'c', 'd', 'e', 'f'], skipped: 40 });
  });

  it('is null when an end is not loaded', () => {
    expect(rangeBetween(segs, 'a', 'zz')).toBeNull();
  });
});

describe('Selection', () => {
  it('toggles and remembers the anchor', () => {
    const s = new Selection();
    s.toggle('a');
    s.toggle('c');
    expect(s.list()).toEqual(['a', 'c']);
    expect(s.anchor).toBe('c');
    s.toggle('a');
    expect(s.list()).toEqual(['c']);
    expect(s.has('a')).toBe(false);
    expect(s.size).toBe(1);
  });

  it('selects shift ranges from the anchor and reports skipped items', () => {
    const s = new Selection();
    const segs = [seg('a', 'b', 'c'), unloaded(5), seg('d', 'e')];
    s.toggle('b');
    s.selectRange(segs, 'e');
    expect(s.list().sort()).toEqual(['b', 'c', 'd', 'e']);
    expect(s.skipped).toBe(5);
    expect(s.anchor).toBe('e');
    // The next range starts from the new anchor and only adds.
    s.selectRange(segs, 'a');
    expect(s.list().sort()).toEqual(['a', 'b', 'c', 'd', 'e']);
    expect(s.skipped).toBe(5);
  });

  it('deselects the range when the Shift-clicked item was selected', () => {
    const s = new Selection();
    const segs = [seg('a', 'b', 'c'), seg('d', 'e')];
    s.toggle('a');
    s.selectRange(segs, 'e');
    expect(s.size).toBe(5);
    // Anchor e; c is selected, so c..e are deselected whatever the anchor's state.
    s.selectRange(segs, 'c');
    expect(s.list()).toEqual(['a', 'b']);
    expect(s.anchor).toBe('c');
    // From c (not selected now) to d (not selected): selected again.
    s.selectRange(segs, 'd');
    expect(s.list().sort()).toEqual(['a', 'b', 'c', 'd']);
  });

  it('toggles alone without an anchor', () => {
    const s = new Selection();
    s.selectRange([seg('a', 'b')], 'b');
    expect(s.list()).toEqual(['b']);
  });

  it('clears on a scope change only', () => {
    const s = new Selection();
    s.setScope('peter|grid||');
    s.toggle('a');
    s.setScope('peter|grid||');
    expect(s.size).toBe(1);
    s.setScope('peter|grid||type=video');
    expect(s.size).toBe(0);
    expect(s.anchor).toBeNull();
    s.toggle('x');
    s.setScope('penny|grid||');
    expect(s.size).toBe(0);
  });

  it('drops removed hashes and the anchor with them', () => {
    const s = new Selection();
    s.toggle('a');
    s.toggle('b');
    s.remove(['b']);
    expect(s.list()).toEqual(['a']);
    expect(s.anchor).toBeNull();
    expect(s.peek('a')).toBe(true);
    // A deselected anchor survives the removal of other items.
    s.toggle('a');
    s.toggle('c');
    s.toggle('a');
    s.remove(['c']);
    expect(s.anchor).toBe('a');
  });
});
