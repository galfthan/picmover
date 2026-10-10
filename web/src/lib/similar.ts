// Helpers for the duplicates page (pure; see similar.test.ts).

import type { SimilarItem } from './api';

/** The default photo to keep in a group: most pixels, then the largest file, then the first. */
export function bestOf(items: readonly SimilarItem[]): string {
  let best: SimilarItem | undefined;
  for (const x of items) {
    if (!best) {
      best = x;
      continue;
    }
    const px = (x.width || x.w) * (x.height || x.h);
    const bp = (best.width || best.w) * (best.height || best.h);
    if (px > bp || (px === bp && x.size > best.size)) best = x;
  }
  return best?.hash ?? '';
}
