import { describe, expect, it } from 'vitest';
import { columnsFor, layoutTimeline, yearMarks, type Bucket } from './timeline';

const P = { width: 4 * 100 + 3 * 4, tileSize: 100, gap: 4, headerHeight: 40 };
// 4 columns of exactly 100 px, row pitch 104.

describe('columnsFor', () => {
  it('fits as many tiles as possible', () => {
    expect(columnsFor(412, 100, 4)).toBe(4);
    expect(columnsFor(411, 100, 4)).toBe(3);
    expect(columnsFor(50, 100, 4)).toBe(1);
    expect(columnsFor(0, 100, 4)).toBe(1);
  });
});

describe('layoutTimeline', () => {
  it('handles no buckets', () => {
    const t = layoutTimeline([], P);
    expect(t.buckets).toEqual([]);
    expect(t.totalHeight).toBe(0);
    expect(t.visibleBuckets(0, 800, 800)).toEqual([0, 0]);
    expect(t.indexAtOffset(100)).toBe(-1);
    expect(yearMarks(t.buckets)).toEqual([]);
  });

  it('lays out one bucket', () => {
    const t = layoutTimeline([{ year: 2024, month: 7, count: 9 }], P);
    expect(t.columns).toBe(4);
    expect(t.tile).toBe(100);
    expect(t.rowPitch).toBe(104);
    const b = t.buckets[0]!;
    expect(b).toMatchObject({ year: 2024, month: 7, count: 9, top: 0, headerTop: 0, rowsTop: 40, rows: 3 });
    expect(b.height).toBe(40 + 3 * 104);
    expect(t.totalHeight).toBe(b.height);
    expect(t.visibleBuckets(0, 100)).toEqual([0, 1]);
    expect(t.tilePosition(0, 0)).toEqual({ left: 0, top: 40, size: 100, row: 0, col: 0 });
    expect(t.tilePosition(0, 5)).toEqual({ left: 104, top: 40 + 104, size: 100, row: 1, col: 1 });
    expect(t.tilePosition(0, 8)).toMatchObject({ left: 0, top: 40 + 208, row: 2, col: 0 });
  });

  it('stretches tiles to fill the width', () => {
    const t = layoutTimeline([{ year: 2024, month: 1, count: 1 }], { ...P, width: 500 });
    expect(t.columns).toBe(4);
    expect(t.tile).toBeCloseTo((500 - 12) / 4);
    expect(t.tilePosition(0, 3).left + t.tile).toBeCloseTo(500);
  });

  it('stacks buckets with padding and puts undated last', () => {
    const buckets: Bucket[] = [
      { year: 2024, month: 7, count: 4 },
      { year: 2024, month: 3, count: 5 },
      { year: 2023, month: 12, count: 1 },
      { year: 0, month: 0, count: 2 },
    ];
    const t = layoutTimeline(buckets, { ...P, bucketPadding: 10 });
    const tops = t.buckets.map((b) => b.top);
    const h1 = 40 + 104 + 10;
    const h2 = 40 + 2 * 104 + 10;
    const h3 = 40 + 104 + 10;
    expect(tops).toEqual([0, h1, h1 + h2, h1 + h2 + h3]);
    expect(t.totalHeight).toBe(h1 + h2 + h3 + h3);
    const last = t.buckets[3]!;
    expect(last).toMatchObject({ year: 0, month: 0, rows: 1 });
  });

  it('treats an empty bucket as header only', () => {
    const t = layoutTimeline([{ year: 2020, month: 1, count: 0 }], P);
    expect(t.buckets[0]!.rows).toBe(0);
    expect(t.totalHeight).toBe(40);
    expect(t.visibleRows(0, 0, 500)).toEqual([0, 0]);
  });

  describe('visible ranges', () => {
    // 50 buckets of 8 items: 2 rows each, height 40 + 208 = 248.
    const many: Bucket[] = Array.from({ length: 50 }, (_, i) => ({ year: 2024 - Math.floor(i / 12), month: 12 - (i % 12), count: 8 }));
    const t = layoutTimeline(many, P);
    const H = 248;

    it('at the top', () => {
      expect(t.totalHeight).toBe(50 * H);
      expect(t.visibleBuckets(0, 600)).toEqual([0, 3]); // tops 0, 248, 496 < 600
      expect(t.visibleBuckets(0, 600, 600)).toEqual([0, 5]); // < 1200
    });

    it('in the middle', () => {
      const top = 20 * H + 100;
      expect(t.visibleBuckets(top, 600)).toEqual([20, 23]);
      expect(t.visibleBuckets(top, 600, 600)[0]).toBe(17);
      // Rows of bucket 20: rowsTop = 20H + 40; window starts at 20H + 100.
      expect(t.visibleRows(20, top, 600)).toEqual([0, 2]);
      // Bucket 21 starts at 21H, window ends at 20H + 700 = 21H + 452.
      expect(t.visibleRows(21, top, 600)).toEqual([0, 2]);
      // A window ending inside bucket 22's first row only.
      expect(t.visibleRows(22, 22 * H + 40 - 600 + 50, 600)).toEqual([0, 1]);
      // A row scrolled past is skipped.
      expect(t.visibleRows(20, 20 * H + 40 + 110, 100)).toEqual([1, 2]);
    });

    it('at the bottom', () => {
      const top = t.totalHeight - 600;
      const [first, end] = t.visibleBuckets(top, 600);
      expect(end).toBe(50);
      expect(first).toBe(47);
      expect(t.visibleBuckets(t.totalHeight + 1000, 600)).toEqual([0, 0]);
    });

    it('maps offsets to buckets', () => {
      expect(t.indexAtOffset(-5)).toBe(0);
      expect(t.indexAtOffset(0)).toBe(0);
      expect(t.indexAtOffset(H - 1)).toBe(0);
      expect(t.indexAtOffset(H)).toBe(1);
      expect(t.indexAtOffset(49 * H + 5)).toBe(49);
      expect(t.indexAtOffset(1e9)).toBe(49);
    });
  });
});

describe('yearMarks', () => {
  it('marks the first bucket of each year and the undated bucket', () => {
    const t = layoutTimeline(
      [
        { year: 2024, month: 7, count: 4 },
        { year: 2024, month: 3, count: 4 },
        { year: 2022, month: 12, count: 4 },
        { year: 2022, month: 1, count: 4 },
        { year: 0, month: 0, count: 4 },
      ],
      P,
    );
    const marks = yearMarks(t.buckets);
    expect(marks.map((m) => [m.year, m.bucketIndex])).toEqual([
      [2024, 0],
      [2022, 2],
      [0, 4],
    ]);
    expect(marks[1]!.top).toBe(t.buckets[2]!.top);
  });
});
