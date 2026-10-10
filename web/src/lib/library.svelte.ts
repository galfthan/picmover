// Shared /api/library state: the shell (library name), filters (cameras),
// the grid's empty state and the Library page all read it.

import { errorMessage, getLibrary, type LibraryInfo } from './api';
import { baseName } from './paths';

class LibraryStore {
  info = $state<LibraryInfo | null>(null);
  error = $state<string | null>(null);
  loading = $state(false);
  #inflight: Promise<void> | null = null;

  get name(): string {
    return this.info ? baseName(this.info.root) : '';
  }

  /** Fetches /api/library; concurrent calls share one request. */
  load(): Promise<void> {
    if (this.#inflight) return this.#inflight;
    this.loading = true;
    this.#inflight = getLibrary()
      .then((info) => {
        this.info = { ...info, cameras: info.cameras ?? [] };
        this.error = null;
      })
      .catch((e: unknown) => {
        this.error = errorMessage(e);
      })
      .finally(() => {
        this.loading = false;
        this.#inflight = null;
      });
    return this.#inflight;
  }
}

export const library = new LibraryStore();
