// Sharing on the home Wi-Fi, admin side (docs/design/lan-share.md
// "Turning sharing on and off"): the state of GET /api/share, polled while
// someone looks at it. The Share page polls every 2 s (a phone that just
// scanned the QR shows up at once); the top-bar pill polls slowly. Changes
// go through here so every view sees the answer at once.

import { SvelteMap } from 'svelte/reactivity';
import {
  ApiError,
  answerPairRequest,
  createShare,
  deleteShare,
  errorMessage,
  getPairRequests,
  getSharing,
  renameDevice,
  renewShareCert,
  revokeAllDevices,
  revokeDevice,
  setSharing,
  updateShare,
  type PairRequest,
  type Share,
  type ShareCreate,
  type SharingPatch,
  type SharingStatus,
  type ShareUpdate,
} from './api';

/** Poll intervals: the Share page, the pill while sharing is on, and while it is off. */
export const POLL_FAST_MS = 2000;
export const POLL_ON_MS = 10_000;
export const POLL_OFF_MS = 30_000;

class SharingStore {
  status = $state.raw<SharingStatus | null>(null);
  /** false once the server answered 404 (no sharing in this picmover). */
  supported = $state(true);
  loaded = $state(false);
  error = $state<string | null>(null);
  /** Approval requests per share id (shares with `approve` only, while the page polls). */
  readonly requests = new SvelteMap<string, PairRequest[]>();
  #fast = 0;
  #slow = 0;
  #timer: ReturnType<typeof setTimeout> | null = null;
  #inflight: Promise<void> | null = null;
  #gen = 0;

  get on(): boolean {
    return this.status?.state === 'on';
  }

  get shares(): Share[] {
    return this.status?.shares ?? [];
  }

  /** Paired devices over all shares. */
  get phones(): number {
    return this.shares.reduce((n, s) => n + s.devices.length, 0);
  }

  /** The controls are disabled while another picmover process manages sharing. */
  get readOnly(): boolean {
    return !!this.status?.managedElsewhere;
  }

  share(id: string): Share | undefined {
    return this.shares.find((s) => s.id === id);
  }

  /** Shares of one album (for "Shared (n)" on the album page). */
  sharesOfAlbum(lib: string, album: number): Share[] {
    return this.shares.filter((s) => s.kind === 'album' && s.library?.id === lib && s.album?.id === album);
  }

  /** Fetches the state now; concurrent calls share one request. */
  refresh(): Promise<void> {
    if (this.#inflight) return this.#inflight;
    const g = this.#gen;
    const p = (async () => {
      try {
        const st = await getSharing();
        if (g !== this.#gen) return;
        if (st === null) {
          this.supported = false;
          this.status = null;
        } else {
          this.supported = true;
          this.status = st;
          if (this.#fast > 0) await this.#loadRequests(st);
        }
        this.error = null;
      } catch (e) {
        if (g === this.#gen) this.error = errorMessage(e);
      } finally {
        this.loaded = true;
        this.#inflight = null;
      }
    })();
    this.#inflight = p;
    return p;
  }

  async #loadRequests(st: SharingStatus): Promise<void> {
    const want = st.shares.filter((s) => s.approve && s.state === 'active');
    for (const id of [...this.requests.keys()]) if (!want.some((s) => s.id === id)) this.requests.delete(id);
    await Promise.all(
      want.map(async (s) => {
        try {
          this.requests.set(s.id, await getPairRequests(s.id));
        } catch {
          // keep the last list; the next poll tries again
        }
      }),
    );
  }

  /**
   * Polls while the returned function has not been called: every 2 s when
   * `fast` (the Share page), else every 10 s while sharing is on and every
   * 30 s while it is off. A server without sharing is not polled again.
   */
  watch(fast = false): () => void {
    if (fast) this.#fast++;
    else this.#slow++;
    this.#schedule(0);
    let stopped = false;
    return () => {
      if (stopped) return;
      stopped = true;
      if (fast) this.#fast--;
      else this.#slow--;
      if (this.#fast + this.#slow === 0) this.#stop();
    };
  }

  #stop(): void {
    if (this.#timer !== null) clearTimeout(this.#timer);
    this.#timer = null;
  }

  #schedule(ms: number): void {
    this.#stop();
    if (this.#fast + this.#slow === 0) return;
    this.#timer = setTimeout(() => {
      this.#timer = null;
      void this.refresh().then(() => {
        if (!this.supported) return;
        this.#schedule(this.#fast > 0 ? POLL_FAST_MS : this.on ? POLL_ON_MS : POLL_OFF_MS);
      });
    }, ms);
  }

  #apply(st: SharingStatus): SharingStatus {
    this.#gen++;
    this.status = st;
    this.supported = true;
    this.error = null;
    return st;
  }

  /** Replaces one share in the state (the answer of a share change). */
  #put(s: Share): Share {
    const st = this.status;
    if (st) {
      this.#gen++;
      const has = st.shares.some((x) => x.id === s.id);
      this.status = { ...st, shares: has ? st.shares.map((x) => (x.id === s.id ? s : x)) : [...st.shares, s] };
    }
    return s;
  }

  async set(patch: SharingPatch): Promise<SharingStatus> {
    return this.#apply(await setSharing(patch));
  }

  async renewCert(): Promise<SharingStatus> {
    return this.#apply(await renewShareCert());
  }

  async create(req: ShareCreate): Promise<Share> {
    const s = this.#put(await createShare(req));
    void this.refresh();
    return s;
  }

  async update(id: string, patch: ShareUpdate): Promise<Share> {
    return this.#put(await updateShare(id, patch));
  }

  async remove(id: string): Promise<void> {
    await deleteShare(id);
    const st = this.status;
    if (st) {
      this.#gen++;
      this.status = { ...st, shares: st.shares.filter((s) => s.id !== id) };
    }
  }

  async revokeAll(id: string): Promise<Share> {
    return this.#put(await revokeAllDevices(id));
  }

  async renameDevice(id: string, name: string): Promise<void> {
    const d = await renameDevice(id, name);
    const st = this.status;
    if (!st) return;
    this.#gen++;
    this.status = {
      ...st,
      shares: st.shares.map((s) => ({ ...s, devices: s.devices.map((x) => (x.id === id ? { ...x, name: d.name || name } : x)) })),
    };
  }

  async revokeDevice(id: string): Promise<void> {
    await revokeDevice(id);
    const st = this.status;
    if (!st) return;
    this.#gen++;
    this.status = { ...st, shares: st.shares.map((s) => ({ ...s, devices: s.devices.filter((x) => x.id !== id) })) };
  }

  async answer(share: string, request: string, approve: boolean): Promise<void> {
    try {
      await answerPairRequest(share, request, approve);
    } catch (e) {
      // The phone gave up meanwhile (410/404): the request is gone either way.
      if (!(e instanceof ApiError && (e.status === 404 || e.status === 410))) throw e;
    }
    const list = this.requests.get(share);
    if (list) this.requests.set(share, list.filter((r) => r.id !== request));
    void this.refresh();
  }
}

export const sharing = new SharingStore();
