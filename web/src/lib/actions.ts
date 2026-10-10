// Item actions shared by the selection bar, the lightbox and the duplicates
// page: call the API (chunked there), announce the change so pages update
// in place, and refresh the library's counts and lists.

import { errorMessage, type AlbumRef, type ChunkProgress, type Failure, type LibApi, type Tag, type TagChange } from './api';
import { emitChange } from './changes';
import { formatCount } from './format';
import { library } from './library.svelte';
import { toasts } from './toast.svelte';

/** The hashes that are not in `failed`. */
export function succeeded(hashes: readonly string[], failed: readonly Failure[]): string[] {
  if (!failed.length) return [...hashes];
  const bad = new Set(failed.map((f) => f.hash));
  return hashes.filter((h) => !bad.has(h));
}

/** "2 items could not be moved to the trash: <first message>". */
export function failureText(failed: readonly Failure[], what: string): string {
  if (!failed.length) return '';
  const n = failed.length;
  const first = failed[0]?.error ?? '';
  return `${formatCount(n)} ${n === 1 ? 'item' : 'items'} could not be ${what}${first ? `: ${first}` : ''}`;
}

export interface Outcome {
  done: string[];
  failed: Failure[];
}

export async function rateItems(api: LibApi, hashes: readonly string[], rating: number, progress?: ChunkProgress): Promise<void> {
  await api.setRating(hashes, rating, progress);
  emitChange({ kind: 'rating', hashes: [...hashes], rating });
  void library.load();
}

export async function trashItems(api: LibApi, hashes: readonly string[], progress?: ChunkProgress): Promise<Outcome> {
  const r = await api.trash(hashes, progress);
  const done = succeeded(hashes, r.failed);
  if (done.length) emitChange({ kind: 'trashed', hashes: done });
  void library.load();
  return { done, failed: r.failed };
}

export async function restoreItems(api: LibApi, hashes: readonly string[], progress?: ChunkProgress): Promise<Outcome> {
  const r = await api.restore(hashes, progress);
  const done = succeeded(hashes, r.failed);
  if (done.length) emitChange({ kind: 'restored', hashes: done });
  void library.load();
  return { done, failed: r.failed };
}

/**
 * The Undo of a trash toast: restores `hashes` and reports what could not
 * be restored (the server answers 200 with `failed`, e.g. "the file is
 * missing") or a failed request as an error toast. Never throws.
 */
export async function undoTrash(api: LibApi, hashes: readonly string[]): Promise<void> {
  try {
    const r = await restoreItems(api, hashes);
    if (r.failed.length) toasts.error(failureText(r.failed, 'restored'));
  } catch (e) {
    toasts.error(errorMessage(e));
  }
}

export async function deleteItems(api: LibApi, hashes: readonly string[], progress?: ChunkProgress): Promise<Outcome> {
  const r = await api.deleteForever(hashes, progress);
  const done = succeeded(hashes, r.failed);
  if (done.length) emitChange({ kind: 'deleted', hashes: done });
  void library.load();
  return { done, failed: r.failed };
}

export async function emptyTrash(api: LibApi): Promise<{ deleted: number; failed: Failure[] }> {
  const r = await api.emptyTrash();
  emitChange({ kind: 'deleted', hashes: [] });
  void library.load();
  return r;
}

export async function tagItems(api: LibApi, hashes: readonly string[], change: TagChange, progress?: ChunkProgress): Promise<Tag[]> {
  const r = await api.tagMedia(hashes, change, progress);
  library.mergeTags(r.tags);
  emitChange({ kind: 'tags', hashes: [...hashes] });
  void library.loadLists();
  return r.tags;
}

/** Adds to an existing album, or creates one with the items. */
export async function addToAlbum(
  api: LibApi,
  hashes: readonly string[],
  target: { album: AlbumRef } | { create: string },
  progress?: ChunkProgress,
): Promise<AlbumRef> {
  let album: AlbumRef;
  if ('create' in target) {
    const a = await api.createAlbum(target.create, hashes, progress);
    album = { id: a.id, name: a.name };
  } else {
    await api.albumItems(target.album.id, { add: hashes }, progress);
    album = target.album;
  }
  emitChange({ kind: 'album', albumId: album.id, added: [...hashes], removed: [] });
  void library.loadLists();
  return album;
}

export async function removeFromAlbum(api: LibApi, albumId: number, hashes: readonly string[], progress?: ChunkProgress): Promise<void> {
  await api.albumItems(albumId, { remove: hashes }, progress);
  emitChange({ kind: 'album', albumId, added: [], removed: [...hashes] });
  void library.loadLists();
}
