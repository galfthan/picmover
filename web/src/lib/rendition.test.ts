import { describe, expect, it } from 'vitest';
import { renditionFor } from './rendition';

describe('renditionFor', () => {
  it('uses the thumb while it covers the tile in device pixels', () => {
    expect(renditionFor(100, 1)).toBe('thumb');
    expect(renditionFor(160, 1)).toBe('thumb');
    expect(renditionFor(256, 1)).toBe('thumb');
    expect(renditionFor(128, 2)).toBe('thumb');
    expect(renditionFor(200, 1.25)).toBe('thumb'); // 250 device px
    expect(renditionFor(170, 1.5)).toBe('thumb'); // 255
  });
  it('uses the large above 256 device px', () => {
    expect(renditionFor(257, 1)).toBe('large');
    expect(renditionFor(320, 1)).toBe('large');
    expect(renditionFor(480, 1)).toBe('large');
    expect(renditionFor(129, 2)).toBe('large');
    expect(renditionFor(160, 2)).toBe('large');
    expect(renditionFor(100, 3)).toBe('large');
    expect(renditionFor(180, 1.5)).toBe('large'); // 270
  });
  it('treats an unknown or bogus pixel ratio as 1', () => {
    expect(renditionFor(200, 0)).toBe('thumb');
    expect(renditionFor(200, Number.NaN)).toBe('thumb');
    expect(renditionFor(300, Number.NaN)).toBe('large');
    expect(renditionFor(Number.NaN, 2)).toBe('thumb');
  });
});
