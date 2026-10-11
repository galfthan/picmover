// Desktop app mode (docs/design/desktop-shell.md "Web"): `GET /api/app`
// answers only under picmover-app (404 under `picmover serve`). The shell
// loads it at startup (a failure other than 404 is retried: see
// `refresh`); the header shows the workspace folder with a Change link and
// the Libraries page the setup card and the window notices. `setWorkspace` switches the app to another workspace folder.

import { ApiError, errorMessage, getApp, setAppWorkspace, type AppInfo } from './api';
import { libraries } from './libraries.svelte';
import { isBoolean, loadJSON, saveJSON } from './storage';

/** localStorage key of the dismissed WebView2-missing notice. */
export const WEBVIEW2_NOTICE_KEY = 'picmover.webview2notice';
/** localStorage key of the dismissed window-creation-failed notice. */
export const WINDOW_FAILED_NOTICE_KEY = 'picmover.windowfailed';
/** Pauses before the automatic retries of a failed GET /api/app; after them one more on the next navigation. */
export const APP_RETRY_MS: readonly number[] = [1000, 2000, 4000];

/** Last segment of a POSIX, drive or UNC path ("C:\\Photos\\" → "Photos"; "C:\\" and "/" stay whole). */
export function folderName(path: string): string {
  const trimmed = path.replace(/[\\/]+$/, '');
  if (trimmed === '' || /^[A-Za-z]:$/.test(trimmed)) return path;
  const i = Math.max(trimmed.lastIndexOf('/'), trimmed.lastIndexOf('\\'));
  return i >= 0 ? trimmed.slice(i + 1) : trimmed;
}

class AppStore {
  /** null: not the desktop app (or not known yet; see `loaded`). */
  info = $state<AppInfo | null>(null);
  /** GET /api/app has answered (the app's description, or 404 under `picmover serve`); false while it fails. */
  loaded = $state(false);
  /** The WebView2 notice was dismissed (remembered in localStorage). */
  noticeDismissed = $state(loadJSON(WEBVIEW2_NOTICE_KEY, isBoolean) === true);
  /** The window-failed notice was dismissed (remembered in localStorage). */
  windowFailedDismissed = $state(loadJSON(WINDOW_FAILED_NOTICE_KEY, isBoolean) === true);
  #inflight: Promise<void> | null = null;
  /** Automatic retries used since the last answer. */
  #retries = 0;
  #timer: ReturnType<typeof setTimeout> | null = null;
  /** The automatic retries are used up: the next navigation tries once more. */
  #retryOnNavigate = false;

  get isApp(): boolean {
    return this.info !== null;
  }

  /** The workspace folder; the listing's root is newer after a change made elsewhere. */
  get workspace(): string {
    if (!this.info) return '';
    if (libraries.mode === 'workspace' && libraries.root) return libraries.root;
    return libraries.mode === 'unset' ? '' : this.info.workspace;
  }

  /** The workspace folder's name for the header (the full path is `workspace`). */
  get workspaceName(): string {
    return this.workspace ? folderName(this.workspace) : '';
  }

  /** The app opened in the browser on Windows because WebView2 is missing. */
  get showWebView2Notice(): boolean {
    const i = this.info;
    return !!i && i.platform === 'windows' && !i.window && i.windowReason === 'no-runtime' && !this.noticeDismissed;
  }

  dismissWebView2Notice(): void {
    this.noticeDismissed = true;
    saveJSON(WEBVIEW2_NOTICE_KEY, true);
  }

  /** The app opened in the browser because creating the native window failed. */
  get showWindowFailedNotice(): boolean {
    const i = this.info;
    return !!i && !i.window && i.windowReason === 'failed' && !this.windowFailedDismissed;
  }

  dismissWindowFailedNotice(): void {
    this.windowFailedDismissed = true;
    saveJSON(WINDOW_FAILED_NOTICE_KEY, true);
  }

  /**
   * (Re)loads GET /api/app. A 404 is final (`picmover serve`). Any other
   * failure keeps the last answer, shows nothing (the libraries listing
   * reports a lost connection) and is retried after each of APP_RETRY_MS,
   * then once more on the next navigation (`navigated`).
   */
  refresh(): Promise<void> {
    if (this.#inflight) return this.#inflight;
    if (this.#timer !== null) {
      clearTimeout(this.#timer);
      this.#timer = null;
    }
    const p = getApp()
      .then((info) => {
        this.info = info;
        this.loaded = true;
        this.#retries = 0;
        this.#retryOnNavigate = false;
      })
      .catch(() => {
        const pause = APP_RETRY_MS[this.#retries];
        if (pause !== undefined) {
          this.#retries++;
          this.#timer = setTimeout(() => {
            this.#timer = null;
            void this.refresh();
          }, pause);
        } else if (this.#retries === APP_RETRY_MS.length) {
          this.#retries++;
          this.#retryOnNavigate = true;
        }
      })
      .finally(() => {
        this.#inflight = null;
      });
    this.#inflight = p;
    return p;
  }

  /** The route changed: retries GET /api/app once when the automatic retries are used up. */
  navigated(): void {
    if (!this.#retryOnNavigate) return;
    this.#retryOnNavigate = false;
    void this.refresh();
  }

  /**
   * POST /api/app/workspace: the libraries store takes the new listing.
   * Returns null on success, else the message to show: a fixed sentence
   * for the 409 of a running job (`jobs` in the body), else the server's
   * own sentence (400 invalid path; 409 library in use or unreadable).
   */
  async setWorkspace(path: string): Promise<string | null> {
    try {
      const ws = await setAppWorkspace(path);
      libraries.apply(ws);
      if (this.info) this.info = { ...this.info, workspace: ws.root || path };
      void this.refresh();
      return null;
    } catch (e) {
      return workspaceError(e);
    }
  }
}

function workspaceError(e: unknown): string {
  if (e instanceof ApiError && e.status === 409 && typeof e.body?.jobs === 'number') return 'An import or backup is running; wait for it to finish.';
  return errorMessage(e);
}

export const app = new AppStore();
