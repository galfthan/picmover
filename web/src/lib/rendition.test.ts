import { describe, expect, it } from 'vitest';
import { aspectOf, renditionFor, shareGridCap } from './rendition';

const R32 = 3 / 2;
const R169 = 16 / 9;

describe('renditionFor', () => {
  it('square or unknown items: thumb up to 256 device px, large up to 640, then the preview', () => {
    expect(renditionFor(100, 1)).toBe('thumb');
    expect(renditionFor(256, 1)).toBe('thumb');
    expect(renditionFor(128, 2)).toBe('thumb');
    expect(renditionFor(200, 1.25)).toBe('thumb'); // 250 device px
    expect(renditionFor(257, 1)).toBe('large');
    expect(renditionFor(480, 1)).toBe('large');
    expect(renditionFor(320, 2)).toBe('large'); // 640
    expect(renditionFor(321, 2)).toBe('preview');
    expect(renditionFor(480, 1.5)).toBe('preview'); // 720
    expect(renditionFor(300, 3)).toBe('preview');
  });
  it('3:2 items compare against the short sides (170.7 and 426.7 px)', () => {
    expect(renditionFor(160, 1, R32)).toBe('thumb');
    expect(renditionFor(170, 1, R32)).toBe('thumb');
    expect(renditionFor(171, 1, R32)).toBe('large');
    expect(renditionFor(182, 1, R32)).toBe('large'); // the default zoom
    expect(renditionFor(85, 2, R32)).toBe('thumb');
    expect(renditionFor(86, 2, R32)).toBe('large');
    expect(renditionFor(426, 1, R32)).toBe('large');
    expect(renditionFor(427, 1, R32)).toBe('preview');
    expect(renditionFor(213, 2, R32)).toBe('large');
    expect(renditionFor(214, 2, R32)).toBe('preview');
    expect(renditionFor(182, 1.5, R32)).toBe('large'); // 273
    expect(renditionFor(300, 1.5, R32)).toBe('preview'); // 450
  });
  it('16:9 items compare against the short sides (144 and 360 px)', () => {
    expect(renditionFor(144, 1, R169)).toBe('thumb');
    expect(renditionFor(145, 1, R169)).toBe('large');
    expect(renditionFor(72, 2, R169)).toBe('thumb');
    expect(renditionFor(73, 2, R169)).toBe('large');
    expect(renditionFor(360, 1, R169)).toBe('large');
    expect(renditionFor(361, 1, R169)).toBe('preview');
    expect(renditionFor(180, 2, R169)).toBe('large');
    expect(renditionFor(181, 2, R169)).toBe('preview');
    expect(renditionFor(120, 3, R169)).toBe('large'); // 360
    expect(renditionFor(121, 3, R169)).toBe('preview');
  });
  it('treats an unknown or bogus pixel ratio as 1 and a bogus aspect as square', () => {
    expect(renditionFor(200, 0)).toBe('thumb');
    expect(renditionFor(200, Number.NaN)).toBe('thumb');
    expect(renditionFor(300, Number.NaN)).toBe('large');
    expect(renditionFor(Number.NaN, 2)).toBe('thumb');
    expect(renditionFor(200, 1, Number.NaN)).toBe('thumb');
    expect(renditionFor(200, 1, 0.5)).toBe('thumb');
  });
});

describe('the share grid cap', () => {
  it('never goes above the cap', () => {
    // A 3-column grid on a 390 px phone at DPR 3: about 390 device px per tile.
    expect(renditionFor(126, 3)).toBe('large');
    expect(renditionFor(126, 3, 1, 'thumb')).toBe('thumb');
    expect(renditionFor(300, 3)).toBe('preview');
    expect(renditionFor(300, 3, 1, 'large')).toBe('large');
    expect(renditionFor(300, 3, 1, 'thumb')).toBe('thumb');
    expect(renditionFor(300, 2, 1, 'large')).toBe('large');
    // Below the cap the usual choice stands.
    expect(renditionFor(100, 1, 1, 'large')).toBe('thumb');
    expect(renditionFor(Number.NaN, 2, 1, 'thumb')).toBe('thumb');
  });
  it('is the thumb from three columns and the large for one or two', () => {
    expect(shareGridCap(3)).toBe('thumb');
    expect(shareGridCap(5)).toBe('thumb');
    expect(shareGridCap(2)).toBe('large');
    expect(shareGridCap(1)).toBe('large');
  });
});

describe('aspectOf', () => {
  it('is long side over short side, 1 when unknown', () => {
    expect(aspectOf(6000, 4000)).toBe(1.5);
    expect(aspectOf(4000, 6000)).toBe(1.5);
    expect(aspectOf(1920, 1080)).toBeCloseTo(16 / 9);
    expect(aspectOf(0, 1080)).toBe(1);
    expect(aspectOf(undefined, undefined)).toBe(1);
    expect(aspectOf(Number.NaN, 10)).toBe(1);
  });
});
