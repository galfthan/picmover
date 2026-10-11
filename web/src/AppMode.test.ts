// @vitest-environment jsdom
//
// Desktop app mode (docs/design/desktop-shell.md "Web") against the fake
// server: GET /api/app decides it; the header shows the workspace folder
// with Change; the Libraries page shows the setup card while no workspace
// is set (and after Change), posts /api/app/workspace and shows 400/409
// inline; the WebView2-missing notice; and the unset mode's library 409s
// send the route home once, without toasts.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { appInfo, backupJobJSON, fakeServer, go, settle, T1, type FakeServer } from './testing/fakes';

let server: FakeServer;
let unmountApp: (() => void) | null = null;
const errors: unknown[] = [];

async function start(hash = '#/') {
  location.hash = hash;
  const svelte = await import('svelte');
  const App = (await import('./App.svelte')).default;
  const app = svelte.mount(App, { target: document.body });
  unmountApp = () => void svelte.unmount(app);
  await settle();
}

function stop() {
  unmountApp?.();
  unmountApp = null;
  document.body.innerHTML = '';
}

const button = (root: ParentNode, label: string) => [...root.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === label);
const setupCard = () => document.querySelector<HTMLElement>('section.setup');
const header = () => document.querySelector<HTMLElement>('header.topbar')!;
const cards = () => [...document.querySelectorAll('.libcard .name')].map((n) => n.textContent);
const gets = (path: string) => server.calls.filter((c) => c.method === 'GET' && c.path === `/api/${path}`);

/** Browses the setup card's picker to `path` (typed into the path bar). */
async function browse(path: string) {
  const input = setupCard()!.querySelector<HTMLInputElement>('input[aria-label="Folder path"]')!;
  input.value = path;
  input.dispatchEvent(new Event('input'));
  setupCard()!.querySelector<HTMLFormElement>('form.pathbar')!.requestSubmit();
  await settle();
}

async function useFolder() {
  button(setupCard()!, 'Use this folder')!.click();
  await settle();
}

describe('desktop app mode', () => {
  beforeEach(() => {
    vi.resetModules();
    errors.length = 0;
    vi.spyOn(console, 'error').mockImplementation((...a: unknown[]) => void errors.push(a));
    vi.spyOn(console, 'warn').mockImplementation((...a: unknown[]) => void errors.push(a));
    localStorage.clear();
  });

  afterEach(() => {
    stop();
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    expect(errors).toEqual([]);
  });

  it('is off under picmover serve: no workspace name in the header, no setup card', async () => {
    server = fakeServer('workspace');
    server.install();
    await start();
    expect(gets('app')).toHaveLength(1);
    expect(header().querySelector('.workspace')).toBeNull();
    expect(header().textContent).toContain('/media');
    expect(setupCard()).toBeNull();
    expect(document.querySelector('.webview2')).toBeNull();
    expect(cards()).toEqual(['peter', 'penny', 'broken', 'scans']);
  });

  it('shows the setup card, disabled, when serve somehow has no workspace', async () => {
    server = fakeServer('unset');
    server.install();
    await start();
    expect(setupCard()?.textContent).toContain('Choose the folder that holds your libraries');
    expect(setupCard()?.textContent).toContain('picmover serve <folder>');
    await browse('/mnt');
    expect(button(setupCard()!, 'Use this folder')!.disabled).toBe(true);
    expect(server.posts('app/workspace')).toHaveLength(0);
  });

  it('sets the workspace from the setup card, then lists its libraries', async () => {
    server = fakeServer('unset');
    server.app.info = appInfo({ workspace: '' });
    server.install();
    await start('#/l/peter/');
    // No workspace: library routes go home, where the card is the whole page.
    expect(location.hash).toBe('#/');
    expect(server.posts('libraries/peter/open')).toHaveLength(0);
    const card = setupCard()!;
    expect(card.querySelector('h2')?.textContent).toBe('Choose the folder that holds your libraries');
    expect(card.textContent).toContain('one subfolder per library; an empty folder is fine');
    expect(card.textContent).not.toContain('picmover serve');
    expect(cards()).toEqual([]);
    expect(document.body.textContent).not.toContain('No libraries yet');
    expect(header().querySelector('.workspace .ws-name')?.textContent).toBe('none');

    // The picker starts at the roots; choose /mnt.
    [...card.querySelectorAll<HTMLButtonElement>('button.entry')].find((b) => b.querySelector('.name')?.textContent === 'mnt')!.click();
    await settle();
    await useFolder();
    expect(server.posts('app/workspace').map((c) => c.body)).toEqual([{ path: '/mnt' }]);
    expect(setupCard()).toBeNull();
    expect(cards()).toEqual(['peter', 'penny', 'broken', 'scans']);
    expect(document.querySelector('.libcard')?.textContent).toContain('/mnt/peter');
    expect(document.querySelector('.root')?.textContent).toContain('/mnt');
    const ws = header().querySelector<HTMLElement>('.workspace')!;
    expect(ws.querySelector('.ws-name')?.textContent).toBe('mnt');
    expect(ws.getAttribute('title')).toBe('/mnt');
    expect(document.querySelector('.toast')).toBeNull();
  });

  it('shows a refused folder (400) and a running job (409) inline, not as toasts', async () => {
    server = fakeServer('unset');
    server.app.info = appInfo({ workspace: '' });
    server.install();
    await start();
    await browse('/media/peter');
    await useFolder();
    expect(setupCard()!.querySelector('[role="alert"]')?.textContent).toBe('the folder is a library; choose the folder that holds it');

    server.backup.jobs.push(backupJobJSON({ id: 7, kind: 'backup', target: T1 }));
    await browse('/mnt');
    await useFolder();
    expect(setupCard()!.querySelector('[role="alert"]')?.textContent).toBe('An import or backup is running; wait for it to finish.');
    expect(server.ws.mode).toBe('unset');
    expect(document.querySelector('.toast')).toBeNull();
  });

  it('shows the workspace name in the header; Change opens the card with the current folder', async () => {
    server = fakeServer('workspace');
    server.app.info = appInfo({ workspace: '/media' });
    server.install();
    await start('#/l/peter/');
    // The library chip still names the library; the workspace sits beside it.
    expect(header().querySelector('.libchip .name')?.textContent).toBe('peter');
    expect(header().querySelector('.libchip .root')?.textContent).toBe('/media/peter');
    const ws = header().querySelector<HTMLElement>('.workspace')!;
    expect(ws.querySelector('.ws-name')?.textContent).toBe('media');
    expect(ws.getAttribute('title')).toBe('/media');
    const change = ws.querySelector<HTMLAnchorElement>('a.ws-change')!;
    expect(change.textContent).toBe('Change');

    await go(change.getAttribute('href')!);
    expect(location.hash).toBe('#/?setup=1');
    const card = setupCard()!;
    expect(card.querySelector('.current')?.textContent).toContain('/media');
    expect(cards()).toEqual(['peter', 'penny', 'broken', 'scans']); // the list stays below
    // Cancel closes the card.
    button(card, 'Cancel')!.click();
    await settle();
    expect(location.hash).toBe('#/');
    expect(setupCard()).toBeNull();

    await go('#/?setup=1');
    await browse('/mnt');
    await useFolder();
    expect(server.posts('app/workspace').map((c) => c.body)).toEqual([{ path: '/mnt' }]);
    expect(location.hash).toBe('#/');
    expect(setupCard()).toBeNull();
    expect(header().querySelector('.ws-name')?.textContent).toBe('mnt');
    expect(document.querySelector('.libcard')?.textContent).toContain('/mnt/peter');
    // The old library is closed by the switch: opening it again asks the server.
    await go('#/l/peter/');
    expect(server.posts('libraries/peter/open')).toHaveLength(2);
  });

  it('shows the WebView2 notice on Windows without the window; dismissal is remembered', async () => {
    server = fakeServer('workspace');
    server.app.info = appInfo({ platform: 'windows', window: false });
    server.install();
    await start();
    const notice = document.querySelector<HTMLElement>('.webview2')!;
    expect(notice.textContent).toContain('The app opened in your browser because the WebView2 runtime is missing.');
    expect(notice.querySelector('a')?.getAttribute('href')).toBe('https://developer.microsoft.com/microsoft-edge/webview2/');
    notice.querySelector<HTMLButtonElement>('button[aria-label="Dismiss"]')!.click();
    await settle();
    expect(document.querySelector('.webview2')).toBeNull();
    expect(localStorage.getItem('picmover.webview2notice')).toBe('true');

    stop();
    vi.resetModules();
    await start();
    expect(document.querySelector('h1')?.textContent).toBe('Libraries');
    expect(document.querySelector('.webview2')).toBeNull();
  });

  it('hides the WebView2 notice with the window, and on other platforms', async () => {
    for (const info of [appInfo({ platform: 'windows', window: true }), appInfo({ platform: 'linux', window: false }), appInfo({ platform: 'darwin', window: false })]) {
      vi.resetModules();
      server = fakeServer('workspace');
      server.app.info = info;
      server.install();
      await start();
      expect(document.querySelector('h1')?.textContent).toBe('Libraries');
      expect(document.querySelector('.webview2')).toBeNull();
      stop();
      vi.useRealTimers();
      vi.unstubAllGlobals();
    }
  });

  it('sends a library view home once when the workspace goes away (409 state "unset"), without toasts', async () => {
    server = fakeServer('workspace');
    server.app.info = appInfo();
    server.install();
    await start('#/l/peter/');
    expect(document.querySelectorAll('a.tile').length).toBe(8);
    const listings = gets('libraries').length;

    // The workspace is gone (changed elsewhere and then lost): every library route answers 409 unset.
    server.ws.mode = 'unset';
    server.ws.root = '';
    const before = server.calls.length;
    await go('#/l/peter/albums');
    expect(server.calls.slice(before).some((c) => c.path.startsWith('/api/l/peter/'))).toBe(true);
    expect(location.hash).toBe('#/');
    expect(setupCard()).not.toBeNull();
    expect(document.querySelector('.toast')).toBeNull();
    expect(gets('libraries').length - listings).toBeLessThanOrEqual(2);
    await settle();
    expect(location.hash).toBe('#/');
    expect(document.querySelector('.toast')).toBeNull();
  });
});
