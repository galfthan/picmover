// @vitest-environment jsdom
//
// The Import page's live panels: the imported count split by kind and the
// thumbnails made while copying (JobProgress), the one status of a
// disconnected source, and the Recent files list: one list with a filter
// by outcome and by type, errors marked and copyable (ResultsList).

import { afterEach, describe, expect, it } from 'vitest';
import type { Job, JobResult } from './lib/api';
import { tick } from 'svelte';
import { ZERO_STATS } from './testing/fakes';

const settle = async () => {
  await tick();
  await Promise.resolve();
  await tick();
};
import { DISCONNECTED_MESSAGE } from './lib/jobtext';

let cleanup: (() => void) | null = null;

afterEach(() => {
  cleanup?.();
  cleanup = null;
  document.body.innerHTML = '';
});

const components = {
  progress: () => import('./components/JobProgress.svelte'),
  results: () => import('./components/ResultsList.svelte'),
};

async function mount(which: keyof typeof components, props: Record<string, unknown>): Promise<HTMLElement> {
  const svelte = await import('svelte');
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const C = (await components[which]()).default as any;
  const target = document.createElement('div');
  document.body.appendChild(target);
  const c = svelte.mount(C, { target, props });
  cleanup = () => void svelte.unmount(c);
  await settle();
  return target;
}

function importJob(stats: Partial<Job['stats']>): Job {
  return {
    id: 1,
    kind: 'import',
    state: 'running',
    library: 'peter',
    source: '/phone/DCIM',
    move: false,
    startedAt: new Date().toISOString(),
    finishedAt: null,
    stats: { ...ZERO_STATS, ...stats },
    error: null,
  };
}

const stat = (root: HTMLElement, label: string) =>
  [...root.querySelectorAll('.stat')].find((s) => s.querySelector('.k')?.textContent === label)?.querySelector('.v')?.textContent;

describe('import progress', () => {
  it('splits the imported count by kind and shows thumbnails of the files recorded', async () => {
    const root = await mount('progress', {
      job: importJob({ scanned: 20, imported: 1234, importedPhotos: 1000, importedRaw: 200, importedVideos: 34, existing: 6, thumbsDone: 300, thumbsFailed: 2 }),
    });
    expect(stat(root, 'Photos imported')).toBe('1,000');
    expect(stat(root, 'RAW imported')).toBe('200');
    expect(stat(root, 'Videos imported')).toBe('34');
    expect(stat(root, 'Imported')).toBeUndefined();
    expect(root.querySelector('[data-testid="thumbs-of"]')?.textContent?.replace(/\s+/g, ' ').trim()).toBe('302 of 1,240');
    expect(root.textContent).not.toContain('Source disconnected');
  });

  it('shows one status when the source disconnected', async () => {
    const root = await mount('progress', {
      job: importJob({ scanned: 20, imported: 3, importedPhotos: 3, sourceDisconnected: true }),
    });
    const notes = [...root.querySelectorAll('.notice')].map((n) => n.textContent);
    expect(notes).toEqual([DISCONNECTED_MESSAGE]);
  });
});

const res = (seq: number, status: string, source: string, kind?: string, message?: string): JobResult => ({ seq, status, source, kind, message });

describe('recent files', () => {
  const results = [
    res(1, 'imported', '/p/a.jpg', 'image'),
    res(2, 'duplicate', '/p/b.jpg', 'image', 'already in catalog'),
    res(4, 'imported', '/p/c.mp4', 'video'),
    res(5, 'imported', '/p/d.dng', 'image_raw'),
  ];
  const errors = [res(3, 'error', '/p/x.jpg', 'image', 'copy: lstat /p/x.jpg: no such file or directory'), res(6, 'error', '/p/y.mp4', 'video', 'copy: read: I/O error')];

  const rows = (root: HTMLElement) => [...root.querySelectorAll<HTMLElement>('.results .row')];
  const chip = (root: HTMLElement, label: string) =>
    [...root.querySelectorAll<HTMLButtonElement>('button.chip')].find((b) => b.textContent?.replace(/\s+/g, ' ').trim().startsWith(label))!;

  it('is one list, newest first, with the errors in it and marked', async () => {
    const root = await mount('results', { results, errors, errorCount: 7 });
    expect(root.querySelector('.pinned')).toBeNull();
    expect(root.querySelectorAll('.results').length).toBe(1);
    expect(rows(root).map((r) => r.dataset.status)).toEqual(['error', 'imported', 'imported', 'error', 'duplicate', 'imported']);
    const err = rows(root)[0]!;
    expect(err.classList.contains('error')).toBe(true);
    expect(err.textContent).toContain('/p/y.mp4');
    expect(err.textContent).toContain('copy: read: I/O error');
    expect(err.querySelector('button')?.textContent?.trim()).toBe('Copy');
    // The job's error count, not only the errors kept.
    expect(chip(root, 'Errors').textContent?.replace(/\s+/g, ' ').trim()).toBe('Errors 7');
  });

  it('filters by outcome and by type', async () => {
    const root = await mount('results', { results, errors, errorCount: 2 });
    chip(root, 'Errors').click();
    await settle();
    expect(rows(root).map((r) => r.querySelector('.path')?.textContent)).toEqual(['/p/y.mp4', '/p/x.jpg']);
    expect([...root.querySelectorAll('button')].some((b) => b.textContent?.trim() === 'Copy all errors')).toBe(true);
    chip(root, 'Videos').click();
    await settle();
    expect(rows(root).map((r) => r.querySelector('.path')?.textContent)).toEqual(['/p/y.mp4']);
    chip(root, 'All').click();
    await settle();
    expect(rows(root).map((r) => r.querySelector('.path')?.textContent?.trim())).toEqual(['/p/y.mp4', '/p/c.mp4']);
    chip(root, 'All types').click();
    chip(root, 'Imported').click();
    await settle();
    expect(rows(root).map((r) => r.dataset.status)).toEqual(['imported', 'imported', 'imported']);
    chip(root, 'RAW').click();
    await settle();
    expect(rows(root).map((r) => r.querySelector('.path')?.textContent?.trim())).toEqual(['/p/d.dng']);
  });
});
