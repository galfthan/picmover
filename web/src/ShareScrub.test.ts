// @vitest-environment jsdom
//
// Scrolling a long timeline on the phone (docs/design/lan-share.md
// "Timeline scrolling"): a library of 200k photos over 15 years laid out
// from its months' counts with only the months in view loaded; the
// scrubber dragged by a finger (proportional, month by month, the labels
// no targets, the thumb grabbed without a jump, non-passive touch
// listeners); its keyboard use; "Jump to a date"; loads cancelled when the
// view moves on; the place kept when the phone turns.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { bigTimeline, fakeShareServer, settle, type FakeShareServer } from './testing/fakes';
import { layoutTimeline, scrubScale } from './lib/timeline';

let server: FakeShareServer;
let unmountApp: (() => void) | null = null;
const errors: unknown[] = [];
const months = bigTimeline();

/** A ResizeObserver whose size can change (the phone turns). */
class Sizer {
  static all: Sizer[] = [];
  static size = { width: 390, height: 700 };
  cb: ResizeObserverCallback;
  target: Element | null = null;
  constructor(cb: ResizeObserverCallback) {
    this.cb = cb;
    Sizer.all.push(this);
  }
  observe(target: Element) {
    this.target = target;
    queueMicrotask(() => this.fire());
  }
  fire() {
    this.cb([{ target: this.target, contentRect: { ...Sizer.size } } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver);
  }
  unobserve() {}
  disconnect() {}
}

async function start(hash = '#/l/peter/') {
  server = fakeShareServer('libraries', { bulk: months });
  server.install();
  Sizer.all = [];
  Sizer.size = { width: 390, height: 700 };
  vi.stubGlobal('ResizeObserver', Sizer);
  history.replaceState(null, '', '/' + hash);
  const svelte = await import('svelte');
  const ShareApp = (await import('./share/ShareApp.svelte')).default;
  const app = svelte.mount(ShareApp, { target: document.body });
  unmountApp = () => void svelte.unmount(app);
  await settle();
}

const scroller = () => document.querySelector<HTMLElement>('.share-grid .scroller')!;
const scrubber = () => document.querySelector<HTMLElement>('.scrubber')!;
const valueText = () => scrubber().getAttribute('aria-valuetext');
const mediaRequests = () => server.calls.filter((c) => c.path.startsWith('/api/l/peter/media?')).map((c) => c.path);
const click = (el: Element) => el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
const key = (k: string) => scrubber().dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true }));

/** The phone's layout: 390 px wide, a 40 px scrubber gutter, 3 columns. */
function phoneLayout(width = 390) {
  const content = width - 40 - 2;
  const columns = width < 500 ? 3 : width < 800 ? 4 : 5;
  return layoutTimeline(months, { width: content, tileSize: Math.max(40, (content - (columns - 1) * 2) / columns - 0.01), gap: 2, headerHeight: 40, bucketPadding: 8 });
}

/** A touch with one finger at y on the scrubber; jsdom has no Touch. */
function touch(kind: 'touchstart' | 'touchmove' | 'touchend', y: number): Event {
  const el = scrubber();
  const e = new Event(kind, { bubbles: true, cancelable: true });
  const t = { clientX: 370, clientY: y, identifier: 7, target: el };
  Object.defineProperty(e, 'touches', { value: kind === 'touchend' ? [] : [t] });
  Object.defineProperty(e, 'changedTouches', { value: [t] });
  el.dispatchEvent(e);
  return e;
}

describe('a long timeline on the phone', () => {
  beforeEach(() => {
    vi.resetModules();
    errors.length = 0;
    vi.spyOn(console, 'error').mockImplementation((...a: unknown[]) => void errors.push(a));
    vi.spyOn(console, 'warn').mockImplementation((...a: unknown[]) => void errors.push(a));
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

  it('lays out 200k photos from the counts and loads only the months in view', async () => {
    await start();
    const tl = phoneLayout();
    expect(document.querySelector<HTMLElement>('.share-grid .canvas')!.style.height).toBe(`${tl.totalHeight}px`);
    expect(Number(scrubber().getAttribute('aria-valuemax'))).toBe(Math.round(tl.totalHeight - 700));
    // December 2025 alone fills several screens: one month asked for, a handful of sections drawn.
    expect(mediaRequests()).toEqual(['/api/l/peter/media?year=2025&month=12']);
    expect(document.querySelectorAll('.share-grid section.bucket').length).toBeLessThanOrEqual(2);
    expect(document.querySelectorAll('.share-grid a.tile').length).toBeLessThan(100);
  });

  it('follows a finger on the scrubber proportionally, month by month, and never at a year label', async () => {
    await start();
    const tl = phoneLayout();
    const s = scrubScale(tl.totalHeight, 700, 8, 48);
    // No targets inside the track: a finger on a year label drags like anywhere else.
    expect(scrubber().querySelectorAll('button, a, [tabindex]').length).toBe(0);
    expect(scrubber().querySelector('.year')).not.toBeNull();

    // Press away from the thumb: its centre comes to the finger.
    const down = touch('touchstart', 200);
    expect(down.defaultPrevented).toBe(true); // Gecko 91 has no touch-action
    expect(scroller().scrollTop).toBeCloseTo(s.toTimeline(200), 0);
    const seen: string[] = [];
    let prev = scroller().scrollTop;
    for (let y = 200; y <= 600; y += 4) {
      const mv = touch('touchmove', y);
      expect(mv.defaultPrevented).toBe(true);
      await vi.advanceTimersByTimeAsync(16);
      const top = scroller().scrollTop;
      expect(top).toBeGreaterThanOrEqual(prev);
      expect(top).toBeCloseTo(s.toTimeline(y), 0);
      prev = top;
      const v = valueText()!;
      if (seen.at(-1) !== v) seen.push(v);
    }
    // While dragging, a large bubble names the month.
    expect(document.querySelector('.scrubber .hover.bubble')?.textContent).toBe(valueText());
    touch('touchend', 600);
    await settle();
    expect(document.querySelector('.scrubber .hover')).toBeNull();
    // Months one after another (most of them seen), not years.
    const januaries = seen.filter((v) => v.startsWith('January')).length;
    expect(seen.length).toBeGreaterThan(60);
    expect(januaries).toBeLessThan(seen.length / 6);
    // Loaded after the drag: the months where it ended, not every month passed.
    expect(mediaRequests().length).toBeLessThan(6);
  });

  it('grabbing the thumb keeps the view where it is', async () => {
    await start();
    const tl = phoneLayout();
    const s = scrubScale(tl.totalHeight, 700, 8, 48);
    touch('touchstart', 300);
    touch('touchend', 300);
    await settle();
    const top = scroller().scrollTop;
    const thumbY = s.toTrack(top);
    // Pressed 15 px below the thumb's centre: nothing moves...
    touch('touchstart', thumbY + 15);
    expect(scroller().scrollTop).toBeCloseTo(top, 3);
    // ...and moving the finger moves the view by the same track distance.
    touch('touchmove', thumbY + 15 + 50);
    expect(scroller().scrollTop).toBeCloseTo(s.toTimeline(thumbY + 50), 3);
    touch('touchend', thumbY + 65);
    await settle();
  });

  it('is a slider: a month with the arrows, a year with Page Up/Down, the month as its value', async () => {
    await start();
    const sl = scrubber();
    expect(sl.getAttribute('role')).toBe('slider');
    expect(sl.getAttribute('aria-orientation')).toBe('vertical');
    expect(valueText()).toBe('December 2025');
    key('ArrowDown');
    await settle();
    expect(valueText()).toBe('November 2025');
    key('PageDown');
    await settle();
    expect(valueText()).toBe(`${bucketOf(2024)}`);
    key('ArrowUp');
    await settle();
    expect(valueText()).toBe('January 2025');
    key('End');
    await settle();
    expect(valueText()).toBe('Undated');
  });

  it('jumps to a date: a year, then one of its months', async () => {
    await start();
    const header = document.querySelector<HTMLButtonElement>('.share-grid .header button.month')!;
    expect(header.getAttribute('aria-label')).toBe('December 2025: jump to a date');
    click(header);
    await settle();
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!;
    expect(dialog.textContent).toContain('Jump to a date');
    const yearButton = [...dialog.querySelectorAll('button')].find((b) => b.textContent?.includes('2019'))!;
    click(yearButton);
    await settle();
    // The months of 2019: the empty ones cannot be picked.
    const monthButtons = [...dialog.querySelectorAll<HTMLButtonElement>('.months button')];
    expect(monthButtons).toHaveLength(12);
    const have = new Set(months.filter((m) => m.year === 2019).map((m) => m.month));
    monthButtons.forEach((b, i) => expect(b.disabled).toBe(!have.has(i + 1)));
    const march = monthButtons[2]!;
    expect(march.disabled).toBe(false);
    click(march);
    await settle();
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    const b = phoneLayout().buckets.find((x) => x.year === 2019 && x.month === 3)!;
    expect(scroller().scrollTop).toBe(b.top);
    expect(valueText()).toBe('March 2019');
    expect(mediaRequests()).toContain('/api/l/peter/media?year=2019&month=3');
  });

  it('Escape goes back from the months, then closes', async () => {
    await start();
    click(document.querySelector('.share-grid .header button.month')!);
    await settle();
    const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]');
    click([...dialog()!.querySelectorAll('button')].find((b) => b.textContent?.includes('2016'))!);
    await settle();
    const esc = () => dialog()!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
    esc();
    await settle();
    expect(dialog()?.textContent).toContain('Jump to a date');
    esc();
    await settle();
    expect(dialog()).toBeNull();
  });

  it('cancels the loads of months the view has left', async () => {
    await start();
    // Hold the month requests to see them cancelled.
    const real = globalThis.fetch;
    const held: { url: string; signal: AbortSignal | undefined }[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (!url.includes('/media?')) return real(input, init);
        held.push({ url, signal: init?.signal ?? undefined });
        return new Promise<Response>((_, reject) => init?.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError'))));
      }),
    );
    const tl = phoneLayout();
    const at = (y: number, m: number) => tl.buckets.find((b) => b.year === y && b.month === m)!.top;
    scroller().scrollTop = at(2015, 6);
    scroller().dispatchEvent(new Event('scroll'));
    await settle();
    const first = held.filter((h) => h.url.includes('year=2015'));
    expect(first.length).toBeGreaterThan(0);
    expect(first.every((h) => !h.signal?.aborted)).toBe(true);
    scroller().scrollTop = at(2020, 6);
    scroller().dispatchEvent(new Event('scroll'));
    await settle();
    expect(first.every((h) => h.signal?.aborted)).toBe(true);
    // No error shown for a cancelled month.
    expect(document.querySelector('.berr')).toBeNull();
  });

  it('keeps the same photos at the top when the phone turns', async () => {
    await start();
    const tl = phoneLayout();
    const b = tl.buckets.find((x) => x.year === 2018 && x.month === 7)!;
    scroller().scrollTop = b.rowsTop + 30 * tl.rowPitch;
    scroller().dispatchEvent(new Event('scroll'));
    await settle();
    expect(valueText()).toBe('July 2018');
    Sizer.size = { width: 700, height: 390 };
    for (const o of Sizer.all) o.fire();
    await settle();
    const wide = phoneLayout(700);
    const nb = wide.buckets.find((x) => x.year === 2018 && x.month === 7)!;
    // Item 90 (row 30 of 3) is in row 22 of 4.
    expect(scroller().scrollTop).toBeCloseTo(nb.rowsTop + 22 * wide.rowPitch, 3);
    expect(valueText()).toBe('July 2018');
  });
});

/** The label of the first month of a year in the big timeline (newest first: its last month with photos). */
function bucketOf(year: number): string {
  const b = months.find((m) => m.year === year)!;
  const names = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'];
  return `${names[b.month - 1]} ${b.year}`;
}
