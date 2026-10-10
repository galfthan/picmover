// Workspace state (docs/design/management.md "Workspace", "Routes"): the
// library listing, opening libraries on demand, creating them and
// recording plain folders in place. `gate(id)` tells the shell what to show
// for #/l/<id>/...: the page when the library is open, else a one-line
// state (opening, busy with Retry, error with Rebuild, folder with Record,
// rebuilding with progress). `gateOf` is the pure part; see
// libraries.test.ts.

import { SvelteMap, SvelteSet } from 'svelte/reactivity';
import { ApiError, createLibrary, errorMessage, getWorkspace, isRecord, openLibrary, type FolderEntry, type Holder, type Job, type Lib } from './api';
import { formatInstant } from './format';
import { jobs } from './jobs.svelte';

export type Gate =
  /** The listing has not loaded yet. */
  | { kind: 'loading' }
  /** The listing failed. */
  | { kind: 'failed'; message: string }
  /** No library or folder by that id. */
  | { kind: 'missing' }
  /** Listed but not open yet: the shell opens it. */
  | { kind: 'closed'; lib: Lib }
  | { kind: 'opening'; lib: Lib }
  | { kind: 'open'; lib: Lib }
  /** Locked by another process. */
  | { kind: 'busy'; lib: Lib; holder: Holder | null; message: string }
  /** media.db unreadable or needs a rebuild. */
  | { kind: 'error'; lib: Lib; message: string }
  /** The open request failed for another reason (Retry). */
  | { kind: 'openFailed'; lib: Lib; message: string }
  | { kind: 'rebuilding'; lib: Lib | null; folder: FolderEntry | null }
  /** A plain folder: can be recorded in place. */
  | { kind: 'folder'; folder: FolderEntry };

export interface GateInput {
  loaded: boolean;
  error: string | null;
  libraries: readonly Lib[];
  folders: readonly FolderEntry[];
  /** Ids with an open request in flight. */
  opening: ReadonlySet<string>;
  /** Ids whose last open request failed (not 409/503), with the message. */
  failures: ReadonlyMap<string, string>;
  /** Ids with a rebuild started from this page and not yet seen in a listing. */
  rebuilding: ReadonlySet<string>;
}

/** "in use by picmover import (pid 812) since 10 Oct 2026, 12:00". */
export function holderText(h: Holder | null | undefined): string {
  if (!h) return 'in use by another picmover process';
  const cmd = h.cmd ? `picmover ${h.cmd}` : 'another picmover process';
  const pid = h.pid ? ` (pid ${h.pid})` : '';
  const since = h.since ? ` since ${formatInstant(h.since)}` : '';
  return `in use by ${cmd}${pid}${since}`;
}

export function gateOf(s: GateInput, id: string): Gate {
  if (!s.loaded) return s.error ? { kind: 'failed', message: s.error } : { kind: 'loading' };
  const lib = s.libraries.find((l) => l.id === id);
  if (!lib) {
    const folder = s.folders.find((f) => f.name === id) ?? null;
    if (folder) return s.rebuilding.has(id) ? { kind: 'rebuilding', lib: null, folder } : { kind: 'folder', folder };
    return { kind: 'missing' };
  }
  if (s.rebuilding.has(id) && lib.state !== 'open') return { kind: 'rebuilding', lib, folder: null };
  switch (lib.state) {
    case 'open':
      return { kind: 'open', lib };
    case 'rebuilding':
      return { kind: 'rebuilding', lib, folder: null };
    case 'busy':
      return { kind: 'busy', lib, holder: lib.holder ?? null, message: holderText(lib.holder) };
    case 'error':
      return { kind: 'error', lib, message: lib.error || 'The catalog cannot be read.' };
    case 'folder':
      return { kind: 'folder', folder: { name: lib.id, path: lib.path } };
    default: {
      if (s.opening.has(id)) return { kind: 'opening', lib };
      const f = s.failures.get(id);
      return f !== undefined ? { kind: 'openFailed', lib, message: f } : { kind: 'closed', lib };
    }
  }
}

function holderFrom(v: unknown): Holder | null {
  if (!isRecord(v)) return null;
  return {
    pid: typeof v.pid === 'number' ? v.pid : 0,
    cmd: typeof v.cmd === 'string' ? v.cmd : '',
    since: typeof v.since === 'string' ? v.since : '',
  };
}

/** A library-scoped 409 within this long of the last reopen is reported, not retried. */
const REOPEN_GUARD_MS = 5000;

export class LibrariesStore {
  root = $state('');
  mode = $state<'workspace' | 'single' | null>(null);
  libraries = $state<Lib[]>([]);
  folders = $state<FolderEntry[]>([]);
  loaded = $state(false);
  error = $state<string | null>(null);
  loading = $state(false);
  readonly opening = new SvelteSet<string>();
  readonly failures = new SvelteMap<string, string>();
  readonly rebuilding = new SvelteSet<string>();
  #gen = 0;
  #inflight: Promise<void> | null = null;
  #reopened = new Map<string, number>();

  /** The library of single mode. */
  get singleId(): string | null {
    return this.mode === 'single' ? (this.libraries[0]?.id ?? null) : null;
  }

  find(id: string): Lib | undefined {
    return this.libraries.find((l) => l.id === id);
  }

  gate(id: string): Gate {
    return gateOf(this, id);
  }

  /** Fetches the listing (the server rescans the root); concurrent calls share one request. */
  load(): Promise<void> {
    if (this.#inflight) return this.#inflight;
    const g = ++this.#gen;
    this.loading = true;
    const p = getWorkspace()
      .then((ws) => {
        if (g !== this.#gen) return;
        this.root = ws.root;
        this.mode = ws.mode;
        this.libraries = ws.libraries;
        this.folders = ws.folders;
        this.error = null;
        this.loaded = true;
        // The listing now knows about rebuilds; keep only those it does not show yet.
        for (const id of [...this.rebuilding]) {
          const l = this.find(id);
          if (l && l.state !== 'closed' && l.state !== 'folder') this.rebuilding.delete(id);
          if (!l && !this.folders.some((f) => f.name === id)) this.rebuilding.delete(id);
        }
        if (!jobs.active) this.rebuilding.clear();
      })
      .catch((e: unknown) => {
        if (g === this.#gen) this.error = errorMessage(e);
      })
      .finally(() => {
        if (g === this.#gen) {
          this.loading = false;
          this.#inflight = null;
        }
      });
    this.#inflight = p;
    return p;
  }

  #put(lib: Lib): void {
    const i = this.libraries.findIndex((l) => l.id === lib.id);
    if (i >= 0) this.libraries[i] = lib;
    else {
      this.libraries = [...this.libraries, lib].sort((a, b) => a.name.localeCompare(b.name));
      this.folders = this.folders.filter((f) => f.name !== lib.id);
    }
  }

  #patch(id: string, patch: Partial<Lib>): void {
    const l = this.find(id);
    if (l) this.#put({ ...l, ...patch });
  }

  /** Opens a library (lock, store, user.db). Never throws; the gate shows the outcome. */
  async open(id: string): Promise<void> {
    if (this.opening.has(id)) return;
    this.opening.add(id);
    this.failures.delete(id);
    try {
      const lib = await openLibrary(id);
      this.#put({ ...lib, state: lib.state || 'open' });
      this.#reopened.set(id, Date.now());
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        this.#patch(id, { state: 'busy', holder: holderFrom(e.body?.holder) });
      } else if (e instanceof ApiError && e.status === 503) {
        this.#patch(id, { state: 'rebuilding' });
      } else if (e instanceof ApiError && e.status === 404) {
        await this.load();
      } else {
        this.failures.set(id, errorMessage(e));
      }
    } finally {
      this.opening.delete(id);
    }
  }

  /** Creates <root>/<name> with an empty catalog; returns the new library (not yet open). */
  async create(name: string): Promise<Lib> {
    const lib = await createLibrary(name.trim());
    this.#put(lib);
    return lib;
  }

  /**
   * Starts a rebuild: records a plain folder in place, or rebuilds an
   * unreadable catalog. Throws the API error (e.g. 409: another job runs).
   */
  async rebuild(id: string): Promise<Job> {
    const job = await jobs.start(id, { kind: 'rebuild' }, 'workspace');
    this.rebuilding.add(id);
    this.failures.delete(id);
    this.#patch(id, { state: 'rebuilding' });
    return job;
  }

  /** A library-scoped request found the library not open (409) or rebuilding (503). */
  unavailable(id: string, status: number, state: string | null): void {
    if (status === 503) {
      this.#patch(id, { state: 'rebuilding' });
      return;
    }
    const last = this.#reopened.get(id) ?? 0;
    if (Date.now() - last < REOPEN_GUARD_MS) {
      this.failures.set(id, 'The library was closed by the server.');
      this.#patch(id, { state: 'closed' });
      return;
    }
    this.#patch(id, { state: state === 'busy' || state === 'error' || state === 'rebuilding' ? state : 'closed' });
    if (state === 'busy' || state === 'error') void this.load();
  }
}

export const libraries = new LibrariesStore();

const RESERVED = /^(con|prn|aux|nul|com[1-9]|lpt[1-9])(\..*)?$/i;

/**
 * Client-side check of a new library name, mirroring the server's id rules
 * (one path component, no / \ : or NUL, not starting with '.', at most 255
 * bytes, no Windows device names). Returns the problem, or null when fine.
 */
export function libraryNameProblem(raw: string, existing: readonly string[] = []): string | null {
  const name = raw.trim();
  if (!name) return 'Enter a name.';
  if (/[/\\:\u0000]/.test(name)) return 'A name cannot contain / \\ or :.';
  if (name.startsWith('.')) return 'A name cannot start with a dot.';
  if (new TextEncoder().encode(name).length > 255) return 'The name is too long.';
  if (RESERVED.test(name)) return `“${name}” is a reserved name on Windows.`;
  if (/[\u0000-\u001f\u007f]/.test(name)) return 'A name cannot contain control characters.';
  if (existing.some((e) => e === name)) return 'A library or folder with this name exists.';
  return null;
}
