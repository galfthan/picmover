// @vitest-environment jsdom
//
// The calendar range picker on its own: the month grid, range picking by
// clicks (start, end, restart on an earlier day), keys with the roving
// tabindex, typed dd/mm/yyyy days and the presets. jsdom has no
// matchMedia, so one month is shown. (A .svelte.test.ts file so the props
// can be $state and follow onchange like a parent would.)

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushSync, mount, tick, unmount } from 'svelte';
import Calendar from './components/Calendar.svelte';

let calls: [string | undefined, string | undefined][] = [];
let app: ReturnType<typeof mount> | null = null;

function render(init: { from?: string; to?: string } = {}) {
  const props = $state({
    from: init.from,
    to: init.to,
    years: [2019, 2024] as readonly number[],
    onchange: (f: string | undefined, t: string | undefined) => {
      calls.push([f, t]);
      props.from = f;
      props.to = t;
    },
  });
  const target = document.createElement('div');
  document.body.appendChild(target);
  app = mount(Calendar, { target, props });
  flushSync();
  return props;
}

const cell = (day: string) => document.querySelector<HTMLElement>(`[role="gridcell"][data-day="${day}"][data-in="1"]`)!;
const selected = () => [...document.querySelectorAll<HTMLElement>('[role="gridcell"][aria-selected="true"]')].map((c) => c.dataset.day);
const key = (el: Element, k: string) => el.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true }));
const status = () => document.querySelector('[aria-live="polite"]')?.textContent ?? '';
const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === label)!;

async function typeIn(label: string, text: string, how: 'enter' | 'blur' = 'enter') {
  const input = document.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`)!;
  input.value = text;
  input.dispatchEvent(new Event('input', { bubbles: true }));
  if (how === 'enter') key(input, 'Enter');
  else input.dispatchEvent(new FocusEvent('blur'));
  flushSync();
  await tick();
  return input;
}

describe('Calendar', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date(2024, 2, 5, 12, 0, 0)); // 05/03/2024, local
    calls = [];
  });
  afterEach(() => {
    if (app) void unmount(app);
    app = null;
    document.body.innerHTML = '';
    vi.useRealTimers();
  });

  it('renders a Monday-first month grid with today marked', () => {
    render();
    const grid = document.querySelector('[role="grid"]')!;
    expect(grid.getAttribute('aria-label')).toBe('March 2024');
    const heads = [...grid.querySelectorAll('[role="columnheader"]')].map((h) => h.textContent?.trim());
    expect(heads).toEqual(['M', 'T', 'W', 'T', 'F', 'S', 'S']);
    const cells = grid.querySelectorAll('[role="gridcell"]');
    expect(cells).toHaveLength(42);
    // 1 March 2024 is a Friday: Monday 26 February leads.
    expect((cells[0] as HTMLElement).dataset.day).toBe('2024-02-26');
    expect(cells[0]!.classList.contains('out')).toBe(true);
    expect(cell('2024-03-05').getAttribute('aria-current')).toBe('date');
    expect(cell('2024-03-05').getAttribute('aria-label')).toBe('Tuesday 5 March 2024');
    expect(cell('2024-03-05').tabIndex).toBe(0);
    expect([...cells].filter((c) => (c as HTMLElement).tabIndex === 0)).toHaveLength(1);
    expect((document.querySelector('select[aria-label="Year"]') as HTMLSelectElement).value).toBe('2024');
    const years = [...document.querySelectorAll<HTMLOptionElement>('select[aria-label="Year"] option')].map((o) => o.value);
    expect(years).toEqual(['2024', '2023', '2022', '2021', '2020', '2019']);
  });

  it('picks a start then an end, inclusive, and restarts on an earlier day', () => {
    render();
    cell('2024-03-10').click();
    flushSync();
    expect(calls).toEqual([['2024-03-10', '2024-03-10']]);
    expect(status()).toContain('Start 10/03/2024');
    cell('2024-03-15').click();
    flushSync();
    expect(calls.at(-1)).toEqual(['2024-03-10', '2024-03-15']);
    expect(selected()).toEqual(['2024-03-10', '2024-03-11', '2024-03-12', '2024-03-13', '2024-03-14', '2024-03-15']);
    expect(cell('2024-03-10').classList.contains('start')).toBe(true);
    expect(cell('2024-03-15').classList.contains('end')).toBe(true);
    expect(cell('2024-03-12').classList.contains('inside')).toBe(true);
    expect(status()).toBe('Selected 10/03/2024 to 15/03/2024');

    // A new first click starts over; a day before that start starts over again.
    cell('2024-03-20').click();
    flushSync();
    expect(calls.at(-1)).toEqual(['2024-03-20', '2024-03-20']);
    cell('2024-03-18').click();
    flushSync();
    expect(calls.at(-1)).toEqual(['2024-03-18', '2024-03-18']);
    cell('2024-03-22').click();
    flushSync();
    expect(calls.at(-1)).toEqual(['2024-03-18', '2024-03-22']);
  });

  it('previews the end on hover', () => {
    render();
    cell('2024-03-10').click();
    flushSync();
    cell('2024-03-13').dispatchEvent(new MouseEvent('mouseenter'));
    flushSync();
    expect(cell('2024-03-12').classList.contains('preview')).toBe(true);
    expect(cell('2024-03-12').getAttribute('aria-selected')).toBe('false');
  });

  it('moves with the keys and selects with Enter', async () => {
    render();
    const start = cell('2024-03-05');
    start.focus();
    key(start, 'ArrowRight');
    await tick();
    expect((document.activeElement as HTMLElement).dataset.day).toBe('2024-03-06');
    key(document.activeElement!, 'Enter');
    flushSync();
    expect(calls.at(-1)).toEqual(['2024-03-06', '2024-03-06']);
    key(document.activeElement!, 'ArrowDown');
    await tick();
    key(document.activeElement!, 'End');
    await tick();
    expect((document.activeElement as HTMLElement).dataset.day).toBe('2024-03-17');
    key(document.activeElement!, ' ');
    flushSync();
    expect(calls.at(-1)).toEqual(['2024-03-06', '2024-03-17']);
    key(document.activeElement!, 'Home');
    await tick();
    expect((document.activeElement as HTMLElement).dataset.day).toBe('2024-03-11');
    // PageDown turns the month and keeps the day.
    key(document.activeElement!, 'PageDown');
    await tick();
    expect(document.querySelector('[role="grid"]')!.getAttribute('aria-label')).toBe('April 2024');
    expect((document.activeElement as HTMLElement).dataset.day).toBe('2024-04-11');
    expect((document.activeElement as HTMLElement).tabIndex).toBe(0);
    key(document.activeElement!, 'PageUp');
    await tick();
    expect((document.activeElement as HTMLElement).dataset.day).toBe('2024-03-11');
  });

  it('applies typed dd/mm/yyyy days and marks invalid ones', async () => {
    render({ from: '2024-03-01', to: '2024-03-10' });
    const startInput = document.querySelector<HTMLInputElement>('input[aria-label="Start day"]')!;
    expect(startInput.value).toBe('01/03/2024');
    expect(document.querySelector<HTMLInputElement>('input[aria-label="End day"]')!.value).toBe('10/03/2024');

    await typeIn('Start day', '1.2.2024');
    expect(calls.at(-1)).toEqual(['2024-02-01', '2024-03-10']);
    expect(startInput.value).toBe('01/02/2024');
    // The view moves to show the typed day.
    expect(document.querySelector('[role="grid"]')!.getAttribute('aria-label')).toBe('February 2024');

    const n = calls.length;
    const end = await typeIn('End day', '31/02/2024', 'blur');
    expect(calls).toHaveLength(n);
    expect(end.getAttribute('aria-invalid')).toBe('true');
    expect(end.classList.contains('bad')).toBe(true);
    // A start after the end is not applied either.
    const s = await typeIn('Start day', '20/03/2024');
    expect(calls).toHaveLength(n);
    expect(s.getAttribute('aria-invalid')).toBe('true');

    // Emptied: that side becomes open.
    await typeIn('End day', '');
    expect(calls.at(-1)).toEqual(['2024-02-01', undefined]);
  });

  it('applies a preset', () => {
    render();
    button('This year').click();
    flushSync();
    expect(calls.at(-1)).toEqual(['2024-01-01', '2024-12-31']);
    expect(button('This year').getAttribute('aria-pressed')).toBe('true');
    expect(document.querySelector('[role="grid"]')!.getAttribute('aria-label')).toBe('December 2024');
    button('Last 7 days').click();
    flushSync();
    expect(calls.at(-1)).toEqual(['2024-02-28', '2024-03-05']);
  });

  it('changes month with the buttons and jumps years with the select', () => {
    render();
    document.querySelector<HTMLButtonElement>('button[aria-label="Next month"]')!.click();
    flushSync();
    expect(document.querySelector('[role="grid"]')!.getAttribute('aria-label')).toBe('April 2024');
    const year = document.querySelector<HTMLSelectElement>('select[aria-label="Year"]')!;
    year.value = '2019';
    year.dispatchEvent(new Event('change', { bubbles: true }));
    flushSync();
    expect(document.querySelector('[role="grid"]')!.getAttribute('aria-label')).toBe('April 2019');
    // The focused day is not shown: the 1st of the shown month takes Tab.
    expect(cell('2019-04-01').tabIndex).toBe(0);
  });
});
