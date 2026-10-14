// Which stored rendition a grid tile shows (docs/design/renditions.md).
// Tiles are square and cover-cropped, so what fills a tile is a
// rendition's short side: the 256 px thumb while its short side covers
// the tile's device pixels, else the 640 px large while its short side
// does, else the 1600 px preview.

export type Rendition = 'thumb' | 'large' | 'preview';

/** Long side of the stored thumb in pixels. */
export const THUMB_PX = 256;
/** Long side of the stored large thumbnail in pixels. */
export const LARGE_PX = 640;

/**
 * Aspect ratio (long side / short side, at least 1) of an item of
 * `w` x `h` pixels; 1 (square) when unknown.
 */
export function aspectOf(w: number | undefined, h: number | undefined): number {
  if (!w || !h || !Number.isFinite(w) || !Number.isFinite(h) || w <= 0 || h <= 0) return 1;
  return Math.max(w, h) / Math.min(w, h);
}

const ORDER: readonly Rendition[] = ['thumb', 'large', 'preview'];

/**
 * The rendition for a tile of `tileCssPx` CSS px at `devicePixelRatio`
 * showing an item of aspect ratio `aspect` (long / short; missing or
 * bogus counts as square). `cap` is the largest rendition allowed: the
 * phone grid (docs/design/lan-share.md "Performance") caps tiles at the
 * thumb, so scrolling never makes the computer derive large renditions.
 */
export function renditionFor(tileCssPx: number, devicePixelRatio: number, aspect = 1, cap: Rendition = 'preview'): Rendition {
  const dpr = Number.isFinite(devicePixelRatio) && devicePixelRatio > 0 ? devicePixelRatio : 1;
  const ar = Number.isFinite(aspect) && aspect >= 1 ? aspect : 1;
  let r: Rendition;
  if (!Number.isFinite(tileCssPx)) r = 'thumb';
  else {
    const px = tileCssPx * dpr;
    r = px <= THUMB_PX / ar ? 'thumb' : px <= LARGE_PX / ar ? 'large' : 'preview';
  }
  return ORDER.indexOf(r) > ORDER.indexOf(cap) ? cap : r;
}

/**
 * The cap of the phone grid: the thumb for three or more columns, the
 * large only for one or two (docs/design/lan-share.md "Performance").
 */
export function shareGridCap(columns: number): Rendition {
  return columns <= 2 ? 'large' : 'thumb';
}

/** The window's current device pixel ratio (1 when unknown). */
export function pixelRatio(): number {
  const r = typeof window === 'undefined' ? 1 : window.devicePixelRatio;
  return Number.isFinite(r) && r > 0 ? r : 1;
}

/**
 * Calls `cb` with the new ratio whenever the device pixel ratio changes
 * (browser zoom, moving the window to another screen). Returns the
 * unsubscribe function; a no-op where matchMedia is missing (jsdom).
 */
export function watchPixelRatio(cb: (dpr: number) => void): () => void {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return () => {};
  let mq: MediaQueryList | null = null;
  const onChange = () => {
    subscribe();
    cb(pixelRatio());
  };
  function subscribe() {
    mq?.removeEventListener('change', onChange);
    mq = window.matchMedia(`(resolution: ${pixelRatio()}dppx)`);
    mq.addEventListener('change', onChange);
  }
  subscribe();
  return () => mq?.removeEventListener('change', onChange);
}
