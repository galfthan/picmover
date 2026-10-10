import { describe, expect, it } from 'vitest';
import { bestOf } from './similar';
import type { SimilarItem } from './api';

const it_ = (hash: string, width: number, height: number, size: number): SimilarItem => ({
  id: 1,
  hash,
  type: 'image',
  takenAt: null,
  w: width,
  h: height,
  thumb: 'ok',
  width,
  height,
  size,
  relPath: `${hash}.jpg`,
});

describe('bestOf', () => {
  it('keeps the most pixels, then the larger file, then the first', () => {
    expect(bestOf([it_('a', 100, 100, 5), it_('b', 200, 100, 1)])).toBe('b');
    expect(bestOf([it_('a', 100, 100, 5), it_('b', 100, 100, 9)])).toBe('b');
    expect(bestOf([it_('a', 100, 100, 5), it_('b', 100, 100, 5)])).toBe('a');
    expect(bestOf([])).toBe('');
  });
});
