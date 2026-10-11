// @vitest-environment jsdom
//
// The Size filter (docs/design/renditions.md "Dimension filter"): presets
// apply at once, typed bounds only on Enter, blur or close, Clear removes
// them; and the fake server filters and validates minpx/maxpx like the
// real one.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Filters } from '../lib/api';
import { fakeServer, hashOf, settle, type FakeServer } from '../testing/fakes';

let server: FakeServer;
let cleanup: (() => void) | null = null;
const errors: unknown[] = [];

async function mountFilters(filters: Filters) {
  const svelte = await import('svelte');
  const { library } = await import('../lib/library.svelte');
  const FiltersBar = (await import('./Filters.svelte')).default;
  library.select('peter');
  await settle();
  const onchange = vi.fn<(f: Filters) => void>();
  const target = document.createElement('div');
  document.body.appendChild(target);
  const c = svelte.mount(FiltersBar, { target, props: { filters, onchange } });
  cleanup = () => void svelte.unmount(c);
  await settle();
  return onchange;
}

async function mountSize(props: { minPx?: number; maxPx?: number }) {
  const svelte = await import('svelte');
  const SizeFilter = (await import('./SizeFilter.svelte')).default;
  const onchange = vi.fn<(v: { minPx?: number; maxPx?: number }) => void>();
  const target = document.createElement('div');
  document.body.appendChild(target);
  const c = svelte.mount(SizeFilter, { target, props: { ...props, onchange } });
  cleanup = () => void svelte.unmount(c);
  await settle();
  return onchange;
}

const sizeButton = () => document.querySelector<HTMLButtonElement>('.sizefilter > button')!;
const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === label);
const input = (label: 'Min' | 'Max') =>
  [...document.querySelectorAll<HTMLLabelElement>('.sizefilter label')].find((l) => l.textContent?.includes(label))!.querySelector('input')!;

async function click(b: HTMLElement | undefined) {
  expect(b).toBeDefined();
  b!.click();
  await settle();
}

async function typeIn(el: HTMLInputElement, text: string) {
  el.focus();
  el.value = text;
  el.dispatchEvent(new Event('input', { bubbles: true }));
  await settle();
}

async function key(el: HTMLElement, k: string) {
  el.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true }));
  await settle();
}

describe('SizeFilter', () => {
  beforeEach(() => {
    vi.resetModules();
    server = fakeServer('single');
    server.install();
    errors.length = 0;
    vi.spyOn(console, 'error').mockImplementation((...a: unknown[]) => void errors.push(a));
    vi.spyOn(console, 'warn').mockImplementation((...a: unknown[]) => void errors.push(a));
  });

  afterEach(() => {
    cleanup?.();
    cleanup = null;
    document.body.innerHTML = '';
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    expect(errors).toEqual([]);
  });

  it('applies a preset from the filter bar at once', async () => {
    const onchange = await mountFilters({ type: 'image' });
    expect(sizeButton().textContent?.trim()).toBe('Any size');
    await click(sizeButton());
    expect(input('Min').getAttribute('max')).toBe('1000000');
    await click(button('Full size ≥ 2000'));
    expect(onchange).toHaveBeenLastCalledWith({ type: 'image', minPx: 2000 });
    await click(button('Thumbnails ≤ 640'));
    expect(onchange).toHaveBeenLastCalledWith({ type: 'image', maxPx: 640 });
  });

  it('summarises the bounds and Clear removes them', async () => {
    const onchange = await mountFilters({ minPx: 640, maxPx: 2000, rating: 2 });
    expect(sizeButton().textContent?.trim()).toBe('640–2000 px');
    await click(sizeButton());
    expect(input('Min').value).toBe('640');
    expect(input('Max').value).toBe('2000');
    const panelClear = [...document.querySelectorAll<HTMLButtonElement>('.sizefilter .foot button')][0];
    await click(panelClear);
    expect(onchange).toHaveBeenCalledTimes(1);
    expect(onchange).toHaveBeenLastCalledWith({ rating: 2 });
  });

  it('labels one-sided bounds', async () => {
    await mountSize({ minPx: 2000 });
    expect(sizeButton().textContent?.trim()).toBe('≥ 2000 px');
    cleanup?.();
    document.body.innerHTML = '';
    await mountSize({ maxPx: 640 });
    expect(sizeButton().textContent?.trim()).toBe('≤ 640 px');
  });

  it('applies typed values on Enter, never per keystroke', async () => {
    const onchange = await mountSize({});
    await click(sizeButton());
    await typeIn(input('Min'), '1');
    await typeIn(input('Min'), '12');
    await typeIn(input('Min'), '1200');
    expect(onchange).not.toHaveBeenCalled();
    await key(input('Min'), 'Enter');
    expect(onchange).toHaveBeenCalledTimes(1);
    expect(onchange).toHaveBeenLastCalledWith({ minPx: 1200, maxPx: undefined });
    // Enter keeps the panel open.
    expect(document.querySelector('.sizefilter .panel')).not.toBeNull();
  });

  it('applies typed values on blur and when the panel closes', async () => {
    const onchange = await mountSize({});
    await click(sizeButton());
    await typeIn(input('Max'), '800');
    input('Max').blur();
    await settle();
    expect(onchange).toHaveBeenLastCalledWith({ minPx: undefined, maxPx: 800 });

    cleanup?.();
    document.body.innerHTML = '';
    const onchange2 = await mountSize({});
    await click(sizeButton());
    await typeIn(input('Min'), '5000');
    await typeIn(input('Max'), '100');
    await key(input('Max'), 'Escape');
    expect(document.querySelector('.sizefilter .panel')).toBeNull();
    // Leaving Min for Max applied Min (blur); closing applied both, and
    // min above max is swapped, not rejected.
    expect(onchange2).toHaveBeenCalledTimes(2);
    expect(onchange2).toHaveBeenNthCalledWith(1, { minPx: 5000, maxPx: undefined });
    expect(onchange2).toHaveBeenLastCalledWith({ minPx: 100, maxPx: 5000 });

    cleanup?.();
    document.body.innerHTML = '';
    const onchange3 = await mountSize({ minPx: 300 });
    await click(sizeButton());
    await typeIn(input('Min'), 'abc');
    document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }));
    await settle();
    expect(document.querySelector('.sizefilter .panel')).toBeNull();
    // A malformed bound is dropped.
    expect(onchange3).toHaveBeenLastCalledWith({ minPx: undefined, maxPx: undefined });
  });

  it('the fake server filters by the longer edge and rejects malformed bounds', async () => {
    const { libApi, ApiError } = await import('../lib/api');
    const small = server.items.find((x) => x.id === 2)!;
    small.w = 120;
    small.h = 160;
    const api = libApi('peter');
    const count = async (f: Filters) => (await api.getTimeline(f)).reduce((n, b) => n + b.count, 0);
    expect(await count({})).toBe(8);
    expect(await count({ maxPx: 640 })).toBe(1);
    expect(await count({ minPx: 2000 })).toBe(7);
    expect(await count({ minPx: 161, maxPx: 3999 })).toBe(0);
    expect((await api.getBucket({ maxPx: 160 }, 2024, 7)).map((x) => x.hash)).toEqual([hashOf(2)]);
    for (const [k, v] of [
      ['minpx', '0'],
      ['minpx', 'abc'],
      ['maxpx', '1000001'],
    ]) {
      const r = await fetch(`/api/l/peter/timeline?${k}=${v}`);
      expect(r.status).toBe(400);
      expect(((await r.json()) as { error: string }).error).toContain(k);
    }
    await expect(api.getTimeline({ minPx: 5000, maxPx: 100 })).rejects.toBeInstanceOf(ApiError);
  });
});
