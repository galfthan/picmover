// Lightbox gestures on phones (docs/design/lan-share.md "Lightbox"), pure
// so they can be tested without touch hardware. Android and Sailfish give
// swipes that start at a screen edge to the system (back, app switcher),
// so a swipe counts only when it starts at least EDGE px from the left and
// right edges. Sailfish Browser (Gecko 91) has no `touch-action`, so the
// lightbox listens to touch events itself (non-passive) and cancels the
// browser's panning while one finger drags.

/** Swipes must start this far from the left and right screen edges. */
export const EDGE = 32;
/** Horizontal travel that turns the page. */
export const SWIPE_X = 60;
/** Downward travel that closes the lightbox. */
export const SWIPE_DOWN = 90;
/** A tap moves less than this and lasts less than TAP_MS. */
export const TAP_SLOP = 10;
export const TAP_MS = 300;
/** Two taps this close in time make a double tap. */
export const DOUBLE_TAP_MS = 300;

export interface Point {
  x: number;
  y: number;
  /** ms */
  t: number;
}

/** Whether a touch that starts at `x` may be a swipe of ours (not the system's edge gesture). */
export function startsAwayFromEdges(x: number, viewportWidth: number, edge = EDGE): boolean {
  return x >= edge && x <= viewportWidth - edge;
}

export type Swipe = 'next' | 'prev' | 'close' | 'tap' | null;

/** What a finished one-finger touch from `a` to `b` was. */
export function classify(a: Point, b: Point): Swipe {
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  if (Math.abs(dx) < TAP_SLOP && Math.abs(dy) < TAP_SLOP && b.t - a.t < TAP_MS) return 'tap';
  if (Math.abs(dx) > SWIPE_X && Math.abs(dx) > 2 * Math.abs(dy)) return dx < 0 ? 'next' : 'prev';
  if (dy > SWIPE_DOWN && dy > 2 * Math.abs(dx)) return 'close';
  return null;
}

/** The direction a drag has settled on so far, to move the picture with the finger. */
export function dragAxis(a: Point, b: Point): 'x' | 'down' | null {
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  if (Math.abs(dx) < TAP_SLOP && Math.abs(dy) < TAP_SLOP) return null;
  if (Math.abs(dx) > Math.abs(dy)) return 'x';
  return dy > 0 ? 'down' : null;
}
