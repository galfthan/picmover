// CSS rules of the phone app that jsdom cannot lay out, checked in the
// source: a regression here is only visible on a real phone (the headless
// Chromium run in docs/design/lan-share.md "Review fixes" checks the result).
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const read = (rel: string) => readFileSync(new URL(rel, import.meta.url), 'utf8');
/** The declarations of the first rule whose selector list is exactly `sel`. */
function rule(css: string, sel: string): string {
  const esc = sel.replace(/[.*+?^${}()|[\]\\]/g, '\\$&').replace(/,\s*/g, ',\\s*');
  const m = new RegExp(`(?:^|[}\\s])${esc}\\s*\\{([^}]*)\\}`, 'm').exec(css);
  return m?.[1] ?? '';
}

describe('phone layout rules', () => {
  it('turns overscroll off in both axes on html and body (no back gesture from a sideways drag)', () => {
    const css = read('./share.css');
    expect(rule(css, 'html, body')).toMatch(/overscroll-behavior:\s*none/);
    expect(css).not.toMatch(/overscroll-behavior-y/);
  });

  it('keeps a playing video between the lightbox bars, never under them', () => {
    const src = read('./ShareLightbox.svelte');
    const style = src.slice(src.indexOf('<style>'));
    expect(rule(style, '.lightbox')).toMatch(/--bar-h:\s*52px/);
    expect(rule(style, '.bar')).toMatch(/min-height:\s*var\(--bar-h\)/);
    const inset = rule(style, '.media.video');
    expect(inset).toMatch(/top:\s*calc\(var\(--bar-h\) \+ env\(safe-area-inset-top/);
    expect(inset).toMatch(/bottom:\s*calc\(var\(--bar-h\) \+ env\(safe-area-inset-bottom/);
  });
});
