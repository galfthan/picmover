// @vitest-environment jsdom
//
// The library's home time zone on the Library page, against the fake
// server (docs/design/home-time.md).

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fakeServer, settle, type FakeServer } from './testing/fakes';

let server: FakeServer;
let unmountApp: (() => void) | null = null;
const errors: unknown[] = [];

async function start(hash: string) {
  location.hash = hash;
  const svelte = await import('svelte');
  const App = (await import('./App.svelte')).default;
  const app = svelte.mount(App, { target: document.body });
  unmountApp = () => void svelte.unmount(app);
  await settle();
}

const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === label);
const card = () => [...document.querySelectorAll('section.card')].find((s) => s.querySelector('h2')?.textContent === 'Home time zone');

describe('home time zone', () => {
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
    vi.restoreAllMocks();
    expect(errors).toEqual([]);
  });

  it('is set and changed on the Library page', async () => {
    await start('#/l/peter/library');
    const c = card();
    expect(c?.textContent).toContain('Not set');
    const select = c!.querySelector<HTMLSelectElement>('select[aria-label="Home time zone"]')!;
    expect([...select.options].map((o) => o.value)).toContain('Europe/Helsinki');
    select.value = 'Europe/Helsinki';
    select.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();
    button('Set')!.click();
    await settle();
    expect(card()?.textContent).not.toContain('Not set');
    expect(button('Change')?.disabled).toBe(true);
    expect(select.value).toBe('Europe/Helsinki');

    select.value = 'Asia/Tokyo';
    select.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();
    expect(button('Change')?.disabled).toBe(false);
    button('Change')!.click();
    await settle();
    const lib = (await (await fetch('/api/l/peter/library')).json()) as { homeZone: { name: string } };
    expect(lib.homeZone.name).toBe('Asia/Tokyo');
  });
});
