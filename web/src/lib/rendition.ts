// Which stored rendition a grid tile shows (docs/design/renditions.md):
// the 256 px thumb while it covers the tile's device pixels, else the 640 px
// large.

export type Rendition = 'thumb' | 'large';

/** Long side of the stored thumb in pixels. */
export const THUMB_PX = 256;

/** The rendition for a tile of `tileCssPx` CSS px at `devicePixelRatio`. */
export function renditionFor(tileCssPx: number, devicePixelRatio: number): Rendition {
  const dpr = Number.isFinite(devicePixelRatio) && devicePixelRatio > 0 ? devicePixelRatio : 1;
  if (!Number.isFinite(tileCssPx)) return 'thumb';
  return tileCssPx * dpr <= THUMB_PX ? 'thumb' : 'large';
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
