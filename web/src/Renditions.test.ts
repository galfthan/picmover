// @vitest-environment jsdom
//
// Renditions (docs/design/renditions.md) on the Library page against the
// fake server: the large-thumbnail count in the Thumbnails section, the
// button that fills it (the normal thumbs job), and the "large filled"
// part of a thumbs job's line in the Jobs table.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { backupJobJSON, fakeServer, settle, ZERO_STATS, type FakeServer } from './testing/fakes';

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

const text = (el: Element | null | undefined) => (el?.textContent ?? '').replace(/\s+/g, ' ').trim();
const thumbsCard = () => [...document.querySelectorAll('section.card')].find((c) => c.querySelector('h2')?.textContent === 'Thumbnails')!;
const jobsCard = () => [...document.querySelectorAll('section.card')].find((c) => c.querySelector('h2')?.textContent === 'Jobs')!;

function thumbsJob(id: number, stats: Partial<typeof ZERO_STATS>) {
  return backupJobJSON({ id, kind: 'thumbs', target: '', state: 'done', stats: { ...ZERO_STATS, ...stats } });
}

describe('renditions on the Library page', () => {
  beforeEach(() => {
    vi.resetModules();
    server = fakeServer('single');
    server.install();
    errors.length = 0;
    vi.spyOn(console, 'error').mockImplementation((...a: unknown[]) => void errors.push(a));
    vi.spyOn(console, 'warn').mockImplementation((...a: unknown[]) => void errors.push(a));
    localStorage.clear();
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

  it('shows the large thumbnails to fill and starts the thumbs job for them', async () => {
    server.thumbCounts.largeMissing = 1234;
    await start('#/l/peter/library');
    const card = thumbsCard();
    expect(text(card)).toContain('1,234 large thumbnails to fill');
    const button = [...card.querySelectorAll('button')].find((b) => b.textContent === 'Generate missing thumbnails')!;
    expect(button.disabled).toBe(false);
    button.click();
    await settle();
    expect(server.posts('l/peter/jobs').map((c) => c.body)).toEqual([{ kind: 'thumbs', rebuild: false, limit: 0 }]);
  });

  it('says nothing about large thumbnails when none are missing or the server does not count them', async () => {
    await start('#/l/peter/library');
    expect(text(thumbsCard())).not.toContain('large');
    unmountApp?.();
    document.body.innerHTML = '';
    vi.resetModules();
    server.thumbCounts.largeMissing = undefined;
    await start('#/l/peter/library');
    expect(text(thumbsCard())).not.toContain('large');
  });

  it('adds the large count to a thumbs job line when there is one', async () => {
    server.backup.jobs.push(thumbsJob(7, { thumbsDone: 3, largeDone: 1500 }), thumbsJob(6, { thumbsDone: 2 }));
    await start('#/l/peter/library');
    // Result column by job id.
    const result = (id: number) => text([...jobsCard().querySelectorAll('tbody tr')].find((r) => text(r.querySelector('td')) === String(id))?.querySelectorAll('td')[4]);
    expect(result(7)).toBe('3 done, 0 unsupported, 0 failed, 1,500 large filled');
    const six = result(6);
    expect(six).toBe('2 done, 0 unsupported, 0 failed');
    expect(six).not.toContain('large');
  });
});
