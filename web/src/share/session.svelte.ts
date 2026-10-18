// The phone's session (GET /api/session) and the states that replace
// every page (docs/design/lan-share.md "Error screens"): not paired (401),
// removed from the computer (401 after having been paired here), and the
// share refusing (403 `{state}`: paused, expired, gone, unavailable). Any
// request of the app that meets one of those switches the shell here.

import { errorText, getSession, setRefusalHandler, ShareError, type Session } from './shareapi';

/** 'offline': the computer did not answer; 'busy': it answered 503 (an album share's library is busy). */
export type SessionStatus = 'loading' | 'ready' | 'unpaired' | 'blocked' | 'offline' | 'busy';

class ShareSession {
  status = $state<SessionStatus>('loading');
  session = $state.raw<Session | null>(null);
  /** With 'blocked': the share's state (paused, expired, gone, unavailable). */
  blocked = $state('');
  /** With 'unpaired': this phone was paired in this page and the computer removed it. */
  removed = $state(false);
  /** With 'offline': what went wrong. */
  error = $state('');
  #gen = 0;

  constructor() {
    setRefusalHandler((status, state) => this.refused(status, state));
  }

  get share() {
    return this.session?.share ?? null;
  }

  get caps() {
    return this.session?.share.capabilities ?? { video: false, download: false, rate: false, location: false };
  }

  /** Reads the session; `quiet` keeps the current page while it loads (a capability check). */
  async load(quiet = false): Promise<void> {
    const g = ++this.#gen;
    if (!quiet) this.status = 'loading';
    try {
      const s = await getSession();
      if (g !== this.#gen) return;
      this.session = s;
      this.status = 'ready';
      this.removed = false;
      this.blocked = '';
    } catch (e) {
      if (g !== this.#gen) return;
      if (e instanceof ShareError && (e.status === 401 || (e.status === 403 && e.state))) this.refused(e.status, e.state);
      else if (e instanceof ShareError && e.status === 503) {
        this.error = e.message;
        this.status = 'busy';
      } else {
        this.error = errorText(e);
        this.status = 'offline';
      }
    }
  }

  #refreshing: Promise<void> | null = null;
  #lastRefresh = 0;

  /**
   * Reads the session again in the background (the page came back into
   * view, a timer, a media URL or a rating was refused): capabilities
   * turned on or off on the computer take effect without a reload. Only a
   * refusal (401, 403 `{state}`) changes the page; a failure to reach the
   * computer or a busy library keeps what is shown (the next request that
   * matters says so). An unchanged session keeps its object, so nothing
   * downstream reloads. With `minGap`, a call within that many ms of the
   * last one reuses it (many images failing together ask once).
   */
  refresh(minGap = 0): Promise<void> {
    if (this.status !== 'ready') return Promise.resolve();
    if (this.#refreshing) return this.#refreshing;
    if (minGap > 0 && Date.now() - this.#lastRefresh < minGap) return Promise.resolve();
    this.#lastRefresh = Date.now();
    const g = this.#gen;
    const p = getSession()
      .then((s) => {
        if (g !== this.#gen || this.status !== 'ready') return;
        if (JSON.stringify(s) !== JSON.stringify(this.session)) this.session = s;
      })
      .catch((e: unknown) => {
        if (g !== this.#gen) return;
        if (e instanceof ShareError && (e.status === 401 || (e.status === 403 && e.state))) this.refused(e.status, e.state);
      })
      .finally(() => {
        if (this.#refreshing === p) this.#refreshing = null;
      });
    this.#refreshing = p;
    return p;
  }

  /** A successful pairing: the answer is the session. */
  paired(s: Session): void {
    this.#gen++;
    this.session = s;
    this.status = 'ready';
    this.removed = false;
    this.blocked = '';
  }

  refused(status: number, state: string): void {
    this.#gen++;
    if (status === 401) {
      this.removed = this.status === 'ready' || this.removed;
      this.status = 'unpaired';
      this.session = null;
    } else {
      this.blocked = state;
      this.status = 'blocked';
    }
  }
}

export const shareSession = new ShareSession();
