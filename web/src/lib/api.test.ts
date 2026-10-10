import { describe, expect, it } from 'vitest';
import { previewUrl, thumbUrl } from './api';

describe('thumbnail and preview URLs', () => {
  const hash = '00000000000000ab';
  it('carry thumbAt as a cache-busting version', () => {
    expect(thumbUrl({ hash, thumbAt: 1760090000 })).toBe('/api/thumb/00000000000000ab?v=1760090000');
    expect(previewUrl({ hash, thumbAt: 1760090000 })).toBe('/api/preview/00000000000000ab?v=1760090000');
  });
  it('are unversioned without thumbAt (map clusters, older servers)', () => {
    expect(thumbUrl({ hash })).toBe('/api/thumb/00000000000000ab');
    expect(previewUrl({ hash })).toBe('/api/preview/00000000000000ab');
  });
  it('ignore a thumbAt that is not a finite number', () => {
    const bad = JSON.parse('{"hash":"00000000000000ab","thumbAt":"\\"><img>"}') as { hash: string; thumbAt?: number };
    expect(thumbUrl(bad)).toBe('/api/thumb/00000000000000ab');
    expect(thumbUrl({ hash, thumbAt: Number.NaN })).toBe('/api/thumb/00000000000000ab');
  });
});
