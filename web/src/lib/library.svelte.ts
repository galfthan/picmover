// The open library's /api/l/<id>/library state plus its tag and album
// lists: the shell (switcher, trash count), the filters (cameras, tags,
// albums), the grid's empty state and the Library page read it. The shell
// selects the library from the route (`select`).

import { errorMessage, libApi, type Album, type LibApi, type LibraryInfo, type Tag } from './api';
import { baseName } from './paths';

class LibraryStore {
  /** Id of the library the UI is in; null on the Libraries page. */
  id = $state<string | null>(null);
  /** Display name from the workspace listing (falls back to the root's base name). */
  displayName = $state('');
  info = $state<LibraryInfo | null>(null);
  error = $state<string | null>(null);
  loading = $state(false);
  tags = $state.raw<Tag[]>([]);
  albums = $state.raw<Album[]>([]);
  /** Bumped by every load(); answers for an older library or generation are dropped. */
  #gen = 0;
  #inflight: Promise<void> | null = null;
  /** A load() arrived while one was in flight: that one may have read the old state, so load again after it. */
  #dirty = false;

  get api(): LibApi | null {
    return this.id === null ? null : libApi(this.id);
  }

  get name(): string {
    return this.displayName || (this.info ? baseName(this.info.root) : (this.id ?? ''));
  }

  /**
   * Items in the trash: the catalog's count (`summary.deleted`), which is
   * what the trash page lists and what Empty trash deletes. user.db's
   * `trash` rows can differ (a file put in .picmover/trash by hand and
   * recorded by a rebuild has no row there).
   */
  get trashed(): number {
    return this.info?.summary.deleted ?? 0;
  }

  /** Switches to library `id` (or none) and loads it. */
  select(id: string | null, displayName = ''): void {
    if (id === this.id) {
      if (displayName) this.displayName = displayName;
      return;
    }
    this.id = id;
    this.displayName = displayName;
    this.info = null;
    this.error = null;
    this.tags = [];
    this.albums = [];
    this.#inflight = null;
    this.#dirty = false;
    this.#gen++;
    if (id !== null) void this.load();
  }

  /**
   * Fetches the summary, tags and albums. A call while a load is in flight
   * shares it, and that load is followed by one more (the first request may
   * have been answered before the caller's write), so the promise resolves
   * once the state is at least as new as the call.
   */
  load(): Promise<void> {
    const id = this.id;
    if (id === null) return Promise.resolve();
    if (this.#inflight) {
      this.#dirty = true;
      return this.#inflight;
    }
    const g = ++this.#gen;
    const api = libApi(id);
    this.loading = true;
    const p = Promise.all([
      api.getLibrary().then(
        (info) => {
          if (g !== this.#gen) return;
          this.info = { ...info, cameras: info.cameras ?? [] };
          this.error = null;
        },
        (e: unknown) => {
          if (g === this.#gen) this.error = errorMessage(e);
        },
      ),
      this.#loadLists(g, api),
    ]).then((): Promise<void> | undefined => {
      if (g !== this.#gen) return;
      this.loading = false;
      this.#inflight = null;
      if (this.#dirty) {
        this.#dirty = false;
        return this.load();
      }
    });
    this.#inflight = p;
    return p;
  }

  /** Reloads the tag and album lists only (after an edit). Resolves to an error message, or null. */
  loadLists(): Promise<string | null> {
    const id = this.id;
    if (id === null) return Promise.resolve(null);
    return this.#loadLists(this.#gen, libApi(id));
  }

  async #loadLists(g: number, api: LibApi): Promise<string | null> {
    // The lists are a convenience for pickers and filters; failures leave them as they were.
    let err: string | null = null;
    const fail = (e: unknown) => {
      err ??= errorMessage(e);
      return null;
    };
    const [tags, albums] = await Promise.all([api.getTags().catch(fail), api.getAlbums().catch(fail)]);
    if (g !== this.#gen || api.id !== this.id) return err;
    if (tags) this.tags = [...tags].sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));
    if (albums) this.albums = albums;
    return err;
  }

  /** Adds or replaces tags in the list (after a create). */
  mergeTags(list: readonly Tag[]): void {
    if (!list.length) return;
    const m = new Map<number, Tag>(this.tags.map((t) => [t.id, t]));
    for (const t of list) m.set(t.id, { ...m.get(t.id), ...t });
    this.tags = [...m.values()].sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));
  }
}

export const library = new LibraryStore();
