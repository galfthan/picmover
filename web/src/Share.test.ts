// @vitest-environment jsdom
//
// Sharing on the home Wi-Fi, admin side (docs/design/lan-share.md "Entry
// points"), through the whole app against the fake server: the top-bar
// pill on every page, the Share page's global switch and settings, share
// cards (pairing with its countdown, devices, capabilities, pause, extend,
// revoke all, delete, approval requests, broken states, another process
// managing sharing), ShareDialog for both kinds, and "Share…" / "Shared
// (n)" on the album page.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fakeServer, go, settle, type FakeServer } from './testing/fakes';

let server: FakeServer;
let unmountApp: (() => void) | null = null;
const errors: unknown[] = [];

async function start(hash = '#/share') {
  location.hash = hash;
  const svelte = await import('svelte');
  const App = (await import('./App.svelte')).default;
  const app = svelte.mount(App, { target: document.body });
  unmountApp = () => void svelte.unmount(app);
  await settle();
}

const click = (el: Element) => el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
const buttons = (root: ParentNode = document) => [...root.querySelectorAll<HTMLButtonElement | HTMLAnchorElement>('button, a.btn')];
const button = (label: string, root: ParentNode = document) => buttons(root).find((b) => b.textContent?.trim() === label);
const card = (name: string) => document.querySelector<HTMLElement>(`section[aria-label="Share ${name}"]`)!;
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]');
const pill = () => document.querySelector<HTMLAnchorElement>('.topbar a[href="#/share"]');
const posts = (path: string) => server.calls.filter((c) => c.method === 'POST' && c.path === path);
const checkbox = (root: ParentNode, label: string) =>
  [...root.querySelectorAll('label')].find((l) => l.textContent?.trim() === label)?.querySelector<HTMLInputElement>('input')!;

function type(el: HTMLInputElement, value: string) {
  el.value = value;
  el.dispatchEvent(new Event('input', { bubbles: true }));
}
function choose(el: HTMLSelectElement, value: string) {
  el.value = value;
  el.dispatchEvent(new Event('change', { bubbles: true }));
}

describe('sharing (admin)', () => {
  beforeEach(() => {
    vi.resetModules();
    server = fakeServer('single');
    server.install();
    errors.length = 0;
    vi.spyOn(console, 'error').mockImplementation((...a: unknown[]) => void errors.push(a));
    vi.spyOn(console, 'warn').mockImplementation((...a: unknown[]) => void errors.push(a));
    localStorage.clear();
    sessionStorage.clear();
  });

  afterEach(() => {
    unmountApp?.();
    unmountApp = null;
    document.body.innerHTML = '';
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    expect(errors).toEqual([]);
  });

  it('shows the pill on every page: a plain Share link while off, the counts while on, amber while paused', async () => {
    await start('#/l/peter/');
    expect(pill()?.textContent?.trim()).toBe('Share');

    const s = server.addShare({ name: 'My phone', kind: 'libraries' });
    server.addShare({ name: 'Summer guests', kind: 'album', album: 2, albumName: 'Summer' });
    server.pairPhone(s.id);
    server.sharing.state = 'on';
    // The pill polls slowly while sharing is off.
    await vi.advanceTimersByTimeAsync(30_000);
    expect(pill()?.textContent).toContain('Sharing · 2 shares · 1 phone');
    expect(pill()?.classList.contains('on')).toBe(true);

    await go('#/l/peter/albums');
    expect(pill()?.textContent).toContain('Sharing · 2 shares · 1 phone');

    server.sharing.state = 'paused';
    server.sharing.reason = 'network changed';
    await vi.advanceTimersByTimeAsync(10_000);
    expect(pill()?.textContent).toContain('Sharing paused: network changed');

    click(pill()!);
    await go('#/share');
    expect(document.querySelector('h1')?.textContent).toBe('Share on Wi-Fi');
  });

  it('has no pill and says so on a server without sharing', async () => {
    server.sharing.supported = false;
    await start('#/share');
    expect(pill()).toBeNull();
    expect(document.body.textContent).toContain('This picmover server cannot share on Wi-Fi.');
    // A 404 is not polled again.
    const n = server.calls.filter((c) => c.path === '/api/share').length;
    await vi.advanceTimersByTimeAsync(60_000);
    expect(server.calls.filter((c) => c.path === '/api/share').length).toBe(n);
  });

  it('turns sharing on and changes the network, port, auto-off, .local name and certificate', async () => {
    await start();
    const sw = checkbox(document, 'Share on Wi-Fi');
    expect(sw.checked).toBe(false);
    click(sw);
    await settle();
    expect(posts('/api/share').at(-1)?.body).toEqual({ enabled: true });
    expect(document.body.textContent).toContain('https://192.168.1.20:7443/');
    expect(pill()?.textContent).toContain('Sharing · 0 shares · 0 phones');

    // Two addresses: a dropdown.
    choose(document.querySelector<HTMLSelectElement>('select[aria-label="Network address"]')!, '10.0.0.5');
    await settle();
    expect(posts('/api/share').at(-1)?.body).toEqual({ address: '10.0.0.5' });
    expect(document.body.textContent).toContain('https://10.0.0.5:7443/');

    type(document.querySelector<HTMLInputElement>('input[aria-label="Port"]')!, '7444');
    await settle();
    click(button('Save')!);
    await settle();
    expect(posts('/api/share').at(-1)?.body).toEqual({ port: 7444 });

    choose(document.querySelector<HTMLSelectElement>('select[aria-label="Turn sharing off"]')!, '1h');
    await settle();
    expect(posts('/api/share').at(-1)?.body).toEqual({ autoOff: '1h' });
    expect(document.body.textContent).toContain('turns off in 1 hour');
    expect(pill()?.textContent).toContain('60 min left');

    click(checkbox(document, "Also offer the computer's .local name"));
    await settle();
    expect(posts('/api/share').at(-1)?.body).toEqual({ mdns: true });
    expect(document.body.textContent).toContain('https://laptop.local:7444/');

    expect(document.body.textContent).toContain('SHA-256 AB:CD:EF:01:23:45:67:89');
    click(button('Renew certificate')!);
    await settle();
    expect(posts('/api/share/cert/renew')).toHaveLength(1);
    expect(document.body.textContent).toContain('SHA-256 99:88:77:66:55:44:33:22');

    // The firewall hint names the command; keepOn is the desktop app's only.
    expect(document.querySelector('details pre')?.textContent).toContain('sudo ufw allow from 192.168.1.0/24');
    expect(document.body.textContent).not.toContain('Keep sharing on');
    click(sw);
    await settle();
    expect(posts('/api/share').at(-1)?.body).toEqual({ enabled: false });
    expect(pill()?.textContent?.trim()).toBe('Share');
  });

  it('creates a library share with the defaults and pairs a phone with it', async () => {
    server.sharing.state = 'on';
    await start();
    expect(document.body.textContent).toContain('Nothing is shared yet');
    click(button('Share libraries…')!);
    await settle();
    const d = dialog()!;
    expect(d.textContent).toContain('Share libraries');
    expect(d.querySelector<HTMLInputElement>('input[type="text"]')!.value).toBe('My phone');
    // Every capability is on for a library share, and it never expires.
    for (const l of ['Play videos', 'Download originals', 'See and set star ratings', 'See where photos were taken']) expect(checkbox(d, l).checked).toBe(true);
    expect(d.querySelector<HTMLSelectElement>('select')!.value).toBe('never');
    // The one library is chosen.
    expect(checkbox(d, 'peter').checked).toBe(true);
    click(button('Share', d)!);
    await settle();
    expect(posts('/api/share/shares')[0]?.body).toEqual({
      name: 'My phone',
      scope: { kind: 'libraries', libraries: ['peter'] },
      capabilities: { video: true, download: true, rate: true, location: true },
      approve: false,
      expiresAt: null,
    });
    expect(dialog()?.textContent).toContain('“My phone” is shared.');
    click(button('Pair a phone now')!);
    await settle();

    // The pairing dialog: QR (the server's SVG), code, countdown, URL, fingerprint.
    const sid = server.sharing.shares[0]!.id;
    expect(posts(`/api/share/shares/${sid}/pairings`)).toHaveLength(1);
    const pd = dialog()!;
    const qr = pd.querySelector<HTMLImageElement>('img.qr')!;
    expect(qr.getAttribute('src')).toMatch(/^data:image\/svg\+xml;charset=utf-8,%3Csvg/);
    expect(pd.querySelector('.code')?.textContent).toBe('K7QM-3XRD');
    expect(pd.textContent).toContain('https://192.168.1.20:7443/');
    expect(pd.textContent).not.toContain('#/pair');
    expect(pd.textContent).toContain('fingerprint starts with AB:CD:EF');
    expect(pd.textContent).toMatch(/Valid for 1:5\d/);

    // Over 30 s without a phone: the firewall hint.
    await vi.advanceTimersByTimeAsync(31_000);
    expect(dialog()?.textContent).toContain('The phone cannot connect?');
    // At two minutes the code expires; a new one can be shown.
    await vi.advanceTimersByTimeAsync(90_000);
    expect(dialog()?.querySelector('.qrbox.expired')).not.toBeNull();
    click(button('Show a new code')!);
    await settle();
    expect(posts(`/api/share/shares/${sid}/pairings`)).toHaveLength(2);
    expect(dialog()?.querySelector('.qrbox.expired')).toBeNull();

    // A phone pairs: the page's 2 s poll shows it with Rename and Revoke.
    server.pairPhone(sid);
    await vi.advanceTimersByTimeAsync(2000);
    expect(dialog()?.textContent).toContain("Paired: Anna's iPhone");
    expect(dialog()?.querySelector('img.qr')).toBeNull();
    click(button('Rename', dialog()!)!);
    await settle();
    type(dialog()!.querySelector<HTMLInputElement>('input[aria-label="Phone name"]')!, 'Anna');
    click(button('Save', dialog()!)!);
    await settle();
    expect(server.sharing.shares[0]!.devices[0]!.name).toBe('Anna');
    click(button('Done')!);
    await settle();
    expect(dialog()).toBeNull();
    // Paired before closing: nothing to cancel.
    expect(posts(`/api/share/shares/${sid}/pairings/cancel`)).toHaveLength(0);
    expect(card('My phone').textContent).toContain('Anna');
    expect(card('My phone').textContent).toContain('Safari on iPhone');
    expect(pill()?.textContent).toContain('1 share · 1 phone');
  });

  it('cancels an unused pairing on close, and offers pairing only while sharing is on', async () => {
    server.addShare({ name: 'My phone', kind: 'libraries' });
    await start();
    expect(button('Pair a phone', card('My phone'))?.hasAttribute('disabled')).toBe(true);
    server.sharing.state = 'on';
    await settle();
    click(button('Pair a phone', card('My phone'))!);
    await settle();
    expect(dialog()?.querySelector('.code')).not.toBeNull();
    click(button('Close', dialog()!)!);
    await settle();
    const sid = server.sharing.shares[0]!.id;
    expect(posts(`/api/share/shares/${sid}/pairings/cancel`)).toHaveLength(1);
    expect(dialog()).toBeNull();
  });

  it('creates an album share: only videos, 30 days, and the warnings', async () => {
    await start();
    click(button('Share album…')!);
    await settle();
    const d = dialog()!;
    const [lib, album] = [...d.querySelectorAll<HTMLSelectElement>('select')];
    choose(lib!, 'peter');
    await settle();
    expect([...album!.options].map((o) => o.textContent)).toContain('Summer (2)');
    choose(album!, '2');
    await settle();
    // The name follows the album until typed.
    expect(d.querySelector<HTMLInputElement>('input[type="text"]')!.value).toBe('Summer');
    expect(checkbox(d, 'Play videos').checked).toBe(true);
    expect(checkbox(d, 'Download originals').checked).toBe(false);
    expect(checkbox(d, 'See and set star ratings').checked).toBe(false);
    expect(checkbox(d, 'See where photos were taken').checked).toBe(false);
    expect(d.textContent).toContain('Videos can be saved even without Download.');
    expect(d.textContent).not.toContain("Guests' stars replace yours");
    click(checkbox(d, 'See and set star ratings'));
    await settle();
    expect(d.textContent).toContain("Guests' stars replace yours");
    // Sharing is off: the dialog says so and offers to turn it on.
    expect(d.textContent).toContain('Sharing on Wi-Fi is off');
    type(d.querySelector<HTMLInputElement>('input[type="text"]')!, 'Summer guests');
    click(button('Share', d)!);
    await settle();
    expect(posts('/api/share/shares')[0]?.body).toEqual({
      name: 'Summer guests',
      scope: { kind: 'album', library: 'peter', album: 2 },
      capabilities: { video: true, download: false, rate: true, location: false },
      approve: false,
      expiresIn: 30 * 86400,
    });
    expect(button('Pair a phone now')?.hasAttribute('disabled')).toBe(true);
    click(button('Turn it on')!);
    await settle();
    expect(button('Pair a phone now')?.hasAttribute('disabled')).toBe(false);
    click(button('Done')!);
    await settle();
    expect(card('Summer guests').textContent).toContain('Album: Summer in peter, 2 photos');
    expect(card('Summer guests').textContent).toContain('Expires in 4 weeks');
  });

  it('manages a share: capabilities, pause, edit, revoke, revoke all, extend, delete', async () => {
    server.sharing.state = 'on';
    const s = server.addShare({ name: 'My phone', kind: 'libraries' });
    server.pairPhone(s.id, "Anna's iPhone");
    server.pairPhone(s.id, 'Pixel', 'Chrome on Android');
    await start();
    const c = () => card('My phone');
    expect(c().textContent).toContain('Library: peter');
    expect(c().textContent).toContain('Never expires');

    // A capability toggles at once; the guest warning does not apply to a library share.
    click(checkbox(c(), 'Download originals'));
    await settle();
    expect(posts(`/api/share/shares/${s.id}`).at(-1)?.body).toEqual({ capabilities: { video: true, download: false, rate: true, location: true } });
    expect(c().textContent).toContain('Videos can be saved even without Download.');

    click(button('Pause', c())!);
    await settle();
    expect(posts(`/api/share/shares/${s.id}`).at(-1)?.body).toEqual({ paused: true });
    expect(c().querySelector('.badge')?.textContent).toBe('Paused');
    expect(button('Pair a phone', c())?.hasAttribute('disabled')).toBe(true);
    click(button('Resume', c())!);
    await settle();
    expect(posts(`/api/share/shares/${s.id}`).at(-1)?.body).toEqual({ paused: false });

    // Edit: rename, expire in 7 days, ask before pairing.
    click(button('Edit', c())!);
    await settle();
    const d = dialog()!;
    type(d.querySelector<HTMLInputElement>('input[type="text"]')!, 'Phones');
    choose(d.querySelector<HTMLSelectElement>('select')!, '7d');
    click(checkbox(d, 'Ask on this computer before a phone is paired'));
    click(button('Save', d)!);
    await settle();
    const body = posts(`/api/share/shares/${s.id}`).at(-1)?.body;
    expect(body).toMatchObject({ name: 'Phones', libraries: ['peter'], approve: true, capabilities: { download: false } });
    expect(Math.abs(Date.parse(String(body?.expiresAt)) - Date.now() - 7 * 86400_000)).toBeLessThan(10_000);
    expect(card('Phones').textContent).toContain('Expires next week');
    expect(card('Phones').textContent).toContain('Asks on this computer before a phone is paired.');

    // Devices: rename inline, revoke one, then all.
    const pixel = () => [...card('Phones').querySelectorAll('.devices li')].find((li) => li.textContent?.includes('Pixel'))!;
    expect(pixel().textContent).toContain('Chrome on Android');
    expect(pixel().textContent).toContain('last seen just now from 192.168.1.34');
    click(button('Rename', pixel())!);
    await settle();
    type(card('Phones').querySelector<HTMLInputElement>('input[aria-label="Phone name"]')!, 'Pixel 7');
    click(button('Save', card('Phones'))!);
    await settle();
    expect(card('Phones').textContent).toContain('Pixel 7');
    click(button('Revoke', pixel())!);
    await settle();
    click(button('Revoke', pixel())!); // the confirmation
    await settle();
    expect(card('Phones').textContent).not.toContain('Pixel 7');
    click(button('Revoke all', card('Phones'))!);
    await settle();
    click(button('Revoke all', card('Phones'))!);
    await settle();
    expect(posts(`/api/share/shares/${s.id}/revoke-all`)).toHaveLength(1);
    expect(card('Phones').textContent).toContain('No phones paired.');

    // Expired: Extend.
    server.sharing.shares[0]!.expiresAt = new Date(Date.now() - 86400_000).toISOString();
    await vi.advanceTimersByTimeAsync(2000);
    expect(card('Phones').querySelector('.badge')?.textContent).toBe('Expired');
    expect(card('Phones').textContent).toContain('Ended yesterday');
    click(button('Extend 30 days', card('Phones'))!);
    await settle();
    expect(card('Phones').querySelector('.badge')).toBeNull();

    click(button('Delete', card('Phones'))!);
    await settle();
    click(button('Delete', card('Phones'))!);
    await settle();
    expect(posts(`/api/share/shares/${s.id}/delete`)).toHaveLength(1);
    expect(document.querySelector('.sharecard')).toBeNull();
  });

  it('shows approval requests with Approve and Deny', async () => {
    server.sharing.state = 'on';
    const s = server.addShare({ name: 'Summer guests', kind: 'album', album: 2, albumName: 'Summer', approve: true });
    server.sharing.requests.set(s.id, [
      { id: 'r1', name: "Mia's phone", browser: 'Chrome on Android', address: '192.168.1.40', at: new Date().toISOString() },
      { id: 'r2', name: 'Unknown', browser: 'Sailfish Browser', address: '192.168.1.66', at: new Date().toISOString() },
    ]);
    await start();
    const c = card('Summer guests');
    expect(c.textContent).toContain("Mia's phone wants to pair · Chrome on Android · 192.168.1.40");
    const req = (name: string) => [...c.querySelectorAll('.request')].find((r) => r.textContent?.includes(name))!;
    click(button('Approve', req("Mia's phone"))!);
    await settle();
    expect(posts(`/api/share/shares/${s.id}/requests/r1`)[0]?.body).toEqual({ approve: true });
    expect(card('Summer guests').textContent).toContain("Mia's phone");
    click(button('Deny', req('Unknown'))!);
    await settle();
    expect(posts(`/api/share/shares/${s.id}/requests/r2`)[0]?.body).toEqual({ approve: false });
    expect(card('Summer guests').querySelector('.request')).toBeNull();
  });

  it('marks broken shares and a busy library', async () => {
    server.addShare({ name: 'Old album', kind: 'album', album: 77, albumName: 'Gone album', albumMissing: true });
    server.addShare({ name: 'Elsewhere', kind: 'album', libraries: ['nope'], album: 2 });
    await start();
    expect(card('Old album').querySelector('.badge')?.textContent).toBe('Album deleted');
    expect(card('Elsewhere').querySelector('.badge')?.textContent).toBe('Library missing');
    expect(card('Old album').textContent).toContain('Delete');
  });

  it('shows a library held by another process as busy', async () => {
    unmountApp?.();
    server = fakeServer('workspace');
    server.install();
    server.addShare({ name: 'Penny', kind: 'libraries', libraries: ['penny'] });
    await start();
    expect(card('Penny').textContent).toContain('Library busy');
    // The library checkboxes list the workspace's libraries.
    click(button('Share libraries…')!);
    await settle();
    const d = dialog()!;
    expect(['peter', 'penny', 'broken'].every((l) => checkbox(d, l))).toBe(true);
    click(button('Share', d)!);
    await settle();
    expect(d.textContent).toContain('Choose at least one library.');
  });

  it('disables every control while another process manages sharing', async () => {
    server.sharing.state = 'on';
    server.sharing.managedElsewhere = { pid: 4242, cmd: 'picmover-app' };
    server.addShare({ name: 'My phone', kind: 'libraries' });
    await start();
    expect(document.body.textContent).toContain('Sharing is managed by another picmover process (pid 4242, picmover-app)');
    expect(checkbox(document, 'Share on Wi-Fi').disabled).toBe(true);
    for (const l of ['Share libraries…', 'Share album…', 'Pair a phone', 'Edit', 'Pause', 'Delete']) expect(button(l)?.hasAttribute('disabled'), l).toBe(true);
    expect(card('My phone').querySelector<HTMLFieldSetElement>('fieldset')!.disabled).toBe(true);
  });

  it('offers Share… on an album page and then links to its shares', async () => {
    server.sharing.state = 'on';
    await start('#/l/peter/albums/2');
    click(button('Share…')!);
    await settle();
    const d = dialog()!;
    expect(d.textContent).toContain('Share an album');
    expect(d.querySelector<HTMLInputElement>('input[type="text"]')!.value).toBe('Summer');
    click(button('Share', d)!);
    await settle();
    expect(posts('/api/share/shares')[0]?.body).toMatchObject({ scope: { kind: 'album', library: 'peter', album: 2 } });
    click(button('Pair a phone now')!);
    await settle();
    expect(dialog()?.querySelector('.code')?.textContent).toBe('K7QM-3XRD');
    click(button('Close', dialog()!)!);
    await settle();
    const link = button('Shared (1)') as HTMLAnchorElement;
    expect(link.getAttribute('href')).toBe('#/share?album=peter%3A2');
    await go(link.getAttribute('href')!);
    expect(document.querySelector('.sharecard.marked')?.getAttribute('aria-label')).toBe('Share Summer');
  });
});
