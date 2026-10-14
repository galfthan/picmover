// CSS rules of the phone app that jsdom cannot lay out, checked in the
// source: a regression here is only visible on a real phone (the headless
// Chromium run in docs/design/lan-share.md "Review fixes" checks the result).
import { describe, expect, it } from 'vitest';
import shareCss from './share.css?raw';
import lightboxSrc from './ShareLightbox.svelte?raw';
import gridSrc from './ShareGrid.svelte?raw';
/** The declarations of the first rule whose selector list is exactly `sel`. */
function rule(css: string, sel: string): string {
  const esc = sel.replace(/[.*+?^${}()|[\]\\]/g, '\\$&').replace(/,\s*/g, ',\\s*');
  const m = new RegExp(`(?:^|[}\\s])${esc}\\s*\\{([^}]*)\\}`, 'm').exec(css);
  return m?.[1] ?? '';
}

describe('phone layout rules', () => {
  it('turns overscroll off in both axes on html and body (no back gesture from a sideways drag)', () => {
    const css = shareCss;
    expect(rule(css, 'html, body')).toMatch(/overscroll-behavior:\s*none/);
    expect(css).not.toMatch(/overscroll-behavior-y/);
  });

  it('keeps a playing video between the lightbox bars, never under them', () => {
    const src = lightboxSrc;
    const style = src.slice(src.indexOf('<style>'));
    expect(rule(style, '.lightbox')).toMatch(/--bar-h:\s*52px/);
    expect(rule(style, '.bar')).toMatch(/min-height:\s*var\(--bar-h\)/);
    const inset = rule(style, '.media.video');
    expect(inset).toMatch(/top:\s*calc\(var\(--bar-h\) \+ env\(safe-area-inset-top/);
    expect(inset).toMatch(/bottom:\s*calc\(var\(--bar-h\) \+ env\(safe-area-inset-bottom/);
  });

  it("keeps the scrubber's position marker beside the year labels, not across them", () => {
    const style = gridSrc.slice(gridSrc.indexOf('<style>'));
    const pos = rule(style, '.share-grid :global(.scrubber .pos)');
    const year = rule(style, '.share-grid :global(.scrubber .year)');
    const px = (decls: string, prop: string) => Number(new RegExp(`(?:^|[;\\s])${prop}:\\s*(\\d+)(?:px)?\\b`).exec(decls)?.[1] ?? NaN);
    // The marker ends where the labels begin.
    expect(px(pos, 'right') + px(pos, 'width')).toBeLessThanOrEqual(px(year, 'right'));
    // The labels (4 digits at 10px, about 24px) fit the gutter the canvas leaves.
    const gutter = px(rule(style, '.share-grid :global(.scrubber)'), 'width');
    expect(px(year, 'right') + 24).toBeLessThanOrEqual(gutter);
    expect(px(rule(style, '.canvas'), 'margin-right')).toBe(gutter);
  });
});
