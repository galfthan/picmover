// Layout math for the virtual timeline (pure; see timeline.test.ts).
//
// The grid is a list of month buckets. Each bucket is a header row followed
// by ceil(count / columns) rows of square tiles. Because counts are known up
// front, the total height is known before any item is loaded, so the
// scrollbar is right from the start and any position can be jumped to.

export interface Bucket {
  year: number;
  month: number;
  count: number;
}

export interface LayoutParams {
  /** Usable content width in CSS px. */
  width: number;
  /** Requested tile size in CSS px (tiles stretch to fill the width). */
  tileSize: number;
  /** Gap between tiles in CSS px. */
  gap: number;
  /** Height of the month header row. */
  headerHeight: number;
  /** Extra space after each bucket's last row (default: 0). */
  bucketPadding?: number;
}

export interface BucketLayout {
  index: number;
  year: number;
  month: number;
  count: number;
  /** Top of the bucket (= its header) in timeline coordinates. */
  top: number;
  headerTop: number;
  /** Top of the first tile row. */
  rowsTop: number;
  rows: number;
  /** Header + rows + padding. */
  height: number;
}

export interface TilePos {
  /** Left edge relative to the content box. */
  left: number;
  /** Top in timeline coordinates. */
  top: number;
  size: number;
  row: number;
  col: number;
}

export interface YearMark {
  /** 0 for the undated bucket. */
  year: number;
  bucketIndex: number;
  top: number;
}

export interface Timeline {
  buckets: BucketLayout[];
  columns: number;
  /** Actual (stretched) tile size. */
  tile: number;
  gap: number;
  /** tile + gap. */
  rowPitch: number;
  totalHeight: number;
  /** Buckets intersecting [scrollTop - overscan, scrollTop + viewport + overscan]: [first, end). */
  visibleBuckets(scrollTop: number, viewportHeight: number, overscan?: number): [number, number];
  /** Tile rows of one bucket intersecting the same window: [first, end). */
  visibleRows(bucketIndex: number, scrollTop: number, viewportHeight: number, overscan?: number): [number, number];
  /** Position of item i (0-based) within bucket b. */
  tilePosition(bucketIndex: number, indexInBucket: number): TilePos;
  /** Bucket containing timeline offset y (clamped; -1 when there are no buckets). */
  indexAtOffset(y: number): number;
}

/** Number of columns for a width and requested tile size; at least 1. */
export function columnsFor(width: number, tileSize: number, gap: number): number {
  if (!(width > 0) || !(tileSize > 0)) return 1;
  return Math.max(1, Math.floor((width + gap) / (tileSize + gap)));
}

export function layoutTimeline(buckets: readonly Bucket[], p: LayoutParams): Timeline {
  const gap = Math.max(0, p.gap);
  const columns = columnsFor(p.width, p.tileSize, gap);
  // Stretch tiles so the rows fill the width exactly; fall back to the
  // requested size before the width is known.
  const tile = p.width > 0 ? Math.max(1, (p.width - (columns - 1) * gap) / columns) : p.tileSize;
  const rowPitch = tile + gap;
  const pad = p.bucketPadding ?? 0;

  const layouts: BucketLayout[] = [];
  let y = 0;
  buckets.forEach((b, index) => {
    const count = Math.max(0, b.count);
    const rows = Math.ceil(count / columns);
    const height = p.headerHeight + rows * rowPitch + pad;
    layouts.push({
      index,
      year: b.year,
      month: b.month,
      count,
      top: y,
      headerTop: y,
      rowsTop: y + p.headerHeight,
      rows,
      height,
    });
    y += height;
  });
  const totalHeight = y;

  // Last bucket whose top <= y (binary search); -1 when none.
  function indexAtOffset(off: number): number {
    if (layouts.length === 0) return -1;
    let lo = 0;
    let hi = layouts.length - 1;
    if (off <= 0) return 0;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if ((layouts[mid] as BucketLayout).top <= off) lo = mid;
      else hi = mid - 1;
    }
    return lo;
  }

  function visibleBuckets(scrollTop: number, viewportHeight: number, overscan = 0): [number, number] {
    if (layouts.length === 0) return [0, 0];
    const from = scrollTop - overscan;
    const to = scrollTop + viewportHeight + overscan;
    if (to < 0 || from >= totalHeight) return [0, 0];
    const first = indexAtOffset(Math.max(0, from));
    let end = first;
    while (end < layouts.length && (layouts[end] as BucketLayout).top < to) end++;
    return [first, Math.max(end, first + 1)];
  }

  function visibleRows(bucketIndex: number, scrollTop: number, viewportHeight: number, overscan = 0): [number, number] {
    const b = layouts[bucketIndex];
    if (!b || b.rows === 0) return [0, 0];
    const from = scrollTop - overscan - b.rowsTop;
    const to = scrollTop + viewportHeight + overscan - b.rowsTop;
    const first = Math.min(b.rows, Math.max(0, Math.floor(from / rowPitch)));
    const end = Math.min(b.rows, Math.max(0, Math.ceil(to / rowPitch)));
    return end > first ? [first, end] : [first, first];
  }

  function tilePosition(bucketIndex: number, indexInBucket: number): TilePos {
    const b = layouts[bucketIndex];
    const row = Math.floor(indexInBucket / columns);
    const col = indexInBucket % columns;
    return {
      left: col * rowPitch,
      top: (b ? b.rowsTop : 0) + row * rowPitch,
      size: tile,
      row,
      col,
    };
  }

  return {
    buckets: layouts,
    columns,
    tile,
    gap,
    rowPitch,
    totalHeight,
    visibleBuckets,
    visibleRows,
    tilePosition,
    indexAtOffset,
  };
}

/** The first bucket of every year (and the undated bucket as year 0), in order. */
export function yearMarks(buckets: readonly BucketLayout[]): YearMark[] {
  const marks: YearMark[] = [];
  let last: number | null = null;
  for (const b of buckets) {
    if (b.year !== last) {
      marks.push({ year: b.year, bucketIndex: b.index, top: b.top });
      last = b.year;
    }
  }
  return marks;
}
