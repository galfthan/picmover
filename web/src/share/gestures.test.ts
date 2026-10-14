import { describe, expect, it } from 'vitest';
import { classify, dragAxis, EDGE, startsAwayFromEdges } from './gestures';

const p = (x: number, y: number, t = 0) => ({ x, y, t });

describe('lightbox gestures', () => {
  it('leaves swipes that start at a screen edge to the system', () => {
    expect(startsAwayFromEdges(0, 390)).toBe(false);
    expect(startsAwayFromEdges(EDGE - 1, 390)).toBe(false);
    expect(startsAwayFromEdges(EDGE, 390)).toBe(true);
    expect(startsAwayFromEdges(195, 390)).toBe(true);
    expect(startsAwayFromEdges(390 - EDGE + 1, 390)).toBe(false);
  });

  it('classifies a finished touch', () => {
    expect(classify(p(200, 300), p(100, 310, 200))).toBe('next');
    expect(classify(p(100, 300), p(200, 290, 200))).toBe('prev');
    expect(classify(p(200, 200), p(210, 320, 200))).toBe('close');
    expect(classify(p(200, 200), p(203, 204, 100))).toBe('tap');
    // Too slow for a tap, too short for a swipe.
    expect(classify(p(200, 200), p(203, 204, 800))).toBeNull();
    // Diagonal: neither.
    expect(classify(p(200, 200), p(130, 150, 200))).toBeNull();
    // Up never closes.
    expect(classify(p(200, 400), p(205, 200, 200))).toBeNull();
  });

  it('settles on an axis once the finger moved', () => {
    expect(dragAxis(p(0, 0), p(3, 3))).toBeNull();
    expect(dragAxis(p(0, 0), p(-30, 5))).toBe('x');
    expect(dragAxis(p(0, 0), p(5, 30))).toBe('down');
    expect(dragAxis(p(0, 0), p(5, -30))).toBeNull();
  });
});
