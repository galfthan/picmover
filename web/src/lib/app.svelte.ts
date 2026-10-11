// Desktop app mode (docs/design/desktop-shell.md "Web"): `GET /api/app`
// answers only under picmover-app (404 under `picmover serve`). The shell
// loads it once at startup; the header shows the workspace folder with a
// Change link and the Libraries page the setup card and the WebView2
// notice. `setWorkspace` switches the app to another workspace folder.

import { ApiError, errorMessage, getApp, setAppWorkspace, type AppInfo } from './api';
import { libraries } from './libraries.svelte';
import { isBoolean, loadJSON, saveJSON } from './storage';

/** localStorage key of the dismissed WebView2-missing notice. */
export const WEBVIEW2_NOTICE_KEY = 'picmover.webview2notice';

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
  loaded = $state(false);
  /** The WebView2 notice was dismissed (remembered in localStorage). */
  noticeDismissed = $state(loadJSON(WEBVIEW2_NOTICE_KEY, isBoolean) === true);
  #inflight: Promise<void> | null = null;

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
    return !!this.info && this.info.platform === 'windows' && !this.info.window && !this.noticeDismissed;
  }

  dismissWebView2Notice(): void {
    this.noticeDismissed = true;
    saveJSON(WEBVIEW2_NOTICE_KEY, true);
  }

  /** (Re)loads GET /api/app. A failure other than 404 keeps the last answer and logs nothing to the user. */
  refresh(): Promise<void> {
    if (this.#inflight) return this.#inflight;
    const p = getApp()
      .then((info) => {
        this.info = info;
      })
      .catch(() => {
        // Not reachable: the libraries listing shows the connection error.
      })
      .finally(() => {
        this.loaded = true;
        this.#inflight = null;
      });
    this.#inflight = p;
    return p;
  }

  /**
   * POST /api/app/workspace: the libraries store takes the new listing.
   * Returns null on success, else the message to show (400: the server's
   * reason; 409: a job is running).
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
  if (e instanceof ApiError && e.status === 409) return 'An import or backup is running; wait for it to finish.';
  return errorMessage(e);
}

export const app = new AppStore();
