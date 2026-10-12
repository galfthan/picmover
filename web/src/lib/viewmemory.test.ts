// @vitest-environment jsdom

import { beforeEach, describe, expect, it } from 'vitest';
import { isViewEntry, memoryPath, viewKey, ViewMemory, VIEW_KEY_PREFIX } from './viewmemory.svelte';
import type { LibPage } from './routes';

describe('view memory', () => {
  beforeEach(() => sessionStorage.clear());

  it('normalises paths and hashes to one key', () => {
    expect(viewKey('/l/peter/')).toBe('/l/peter/');
    expect(viewKey('l/peter')).toBe('/l/peter/');
    expect(viewKey('#/l/peter/?type=video')).toBe('/l/peter/');
    expect(viewKey('/l/peter/albums/3')).toBe('/l/peter/albums/3');
    expect(viewKey('/l/my%20lib/map')).toBe('/l/my%20lib/map');
  });

  it('names the filterable pages', () => {
    const lib = (page: LibPage, ids: { albumId?: number; importId?: number } = {}) =>
      memoryPath({ kind: 'lib', lib: 'peter', page, albumId: ids.albumId ?? null, importId: ids.importId ?? null, viewId: null });
    expect(lib('grid')).toBe('/l/peter/');
    expect(lib('map')).toBe('/l/peter/map');
    expect(lib('trash')).toBe('/l/peter/trash');
    expect(lib('album', { albumId: 3 })).toBe('/l/peter/albums/3');
    expect(lib('importrecord', { importId: 5 })).toBe('/l/peter/imports/5');
    expect(lib('import')).toBeNull();
    expect(lib('view')).toBeNull();
    expect(memoryPath({ kind: 'libraries' })).toBeNull();
  });

  it('round-trips through sessionStorage and a new store', () => {
    const a = new ViewMemory();
    a.remember('/l/peter/', 'type=video');
    a.saveAnchor('/l/peter/', 'type=video', { year: 2023, month: 5, offset: 250 });
    a.saveAnchor('/l/peter/albums/2', '', { undated: true, offset: 10 });
    expect(a.href('/l/peter/')).toBe('#/l/peter/?type=video');
    expect(a.withMemory('#/l/peter/')).toBe('#/l/peter/?type=video');
    expect(a.withMemory('#/l/peter/?gps=1')).toBe('#/l/peter/?gps=1');
    expect(a.href('/l/peter/map')).toBe('#/l/peter/map');

    const raw = JSON.parse(sessionStorage.getItem(VIEW_KEY_PREFIX + '/l/peter/') ?? 'null') as unknown;
    expect(raw).toMatchObject({ query: 'type=video', anchor: { year: 2023, month: 5, offset: 250 } });

    const b = new ViewMemory();
    expect(b.get('/l/peter/')).toMatchObject({ query: 'type=video', anchor: { year: 2023, month: 5, offset: 250 } });
    expect(b.get('l/peter/albums/2')?.anchor).toEqual({ undated: true, offset: 10 });
  });

  it('drops the anchor when the query changes and ignores an anchor of an old query', () => {
    const m = new ViewMemory();
    m.saveAnchor('/l/peter/', 'type=video', { year: 2023, month: 5, offset: 0 });
    m.remember('/l/peter/', 'type=video');
    expect(m.get('/l/peter/')?.anchor).toBeDefined();
    m.remember('/l/peter/', 'gps=1');
    expect(m.get('/l/peter/')).toMatchObject({ query: 'gps=1' });
    expect(m.get('/l/peter/')?.anchor).toBeUndefined();
    m.saveAnchor('/l/peter/', 'type=video', { year: 2023, month: 5, offset: 0 });
    expect(m.get('/l/peter/')?.anchor).toBeUndefined();
    m.clear('/l/peter/');
    expect(m.href('/l/peter/')).toBe('#/l/peter/');
  });

  it('ignores malformed stored entries', () => {
    const bad = [
      null,
      'x',
      [],
      { query: 1, savedAt: 1 },
      { query: '', savedAt: 'now' },
      { query: '', savedAt: 1, anchor: { year: 2023, month: 13, offset: 0 } },
      { query: '', savedAt: 1, anchor: { year: 2023, month: 5, offset: -1 } },
      { query: '', savedAt: 1, anchor: { undated: 'yes', offset: 0 } },
      { query: '', savedAt: 1, anchor: { year: 1.5, month: 5, offset: 0 } },
    ];
    for (const v of bad) expect(isViewEntry(v)).toBe(false);
    expect(isViewEntry({ query: 'type=video', savedAt: 1, anchor: { undated: true, offset: 3 } })).toBe(true);

    sessionStorage.setItem(VIEW_KEY_PREFIX + '/l/peter/', '{not json');
    sessionStorage.setItem(VIEW_KEY_PREFIX + '/l/peter/map', JSON.stringify({ query: 7, savedAt: 1 }));
    const m = new ViewMemory();
    expect(m.get('/l/peter/')).toBeNull();
    expect(m.get('/l/peter/map')).toBeNull();
    expect(m.href('/l/peter/map')).toBe('#/l/peter/map');
  });
});
