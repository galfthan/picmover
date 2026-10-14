// The Gecko 91 check of the phone bundle (scripts/check-share-apis.mjs,
// run by `npm run build`): its scanner, and the polyfill module it trusts.
import { describe, expect, it } from 'vitest';
import { htmlAssets, JS_RULES, jsImports, scanCSS, scanJS } from '../../scripts/share-compat.mjs';
import { POLYFILLED } from './polyfills';

describe('share bundle check (Gecko 91)', () => {
  it('finds runtime APIs Gecko 91 lacks, unless polyfilled', () => {
    const code = 'const a=Object.hasOwn(x,"y");b.findLast(f);structuredClone(z);c.toSorted();fetch(u,{keepalive:!0});d.with(1,2)';
    expect(scanJS('a.js', code).map((f) => f.rule)).toEqual([
      'Object.hasOwn',
      'structuredClone',
      'Array.prototype.findLast',
      'Array.prototype.toSorted',
      'Array.prototype.with',
      'fetch keepalive',
    ]);
    expect(scanJS('a.js', code, ['Object.hasOwn', 'structuredClone', 'Array.prototype.findLast', 'Array.prototype.toSorted', 'Array.prototype.with', 'fetch keepalive'])).toEqual([]);
    // What Gecko 91 has.
    expect(scanJS('a.js', 'x.at(-1);s.replaceAll("a","b");x.startsWith("w");Object.fromEntries(e);new ResizeObserver(f)')).toEqual([]);
  });

  it('finds admin code in a share chunk', () => {
    expect(scanJS('a.js', 'fetch("/api/jobs")').map((f) => f.rule)).toEqual(['admin API /api/jobs']);
    expect(scanJS('a.js', 'fetch(`/api/share/shares/${id}`)').map((f) => f.rule)).toEqual(['admin API /api/share']);
    expect(scanJS('a.js', 'fetch("/api/session");fetch("/api/libraries")')).toEqual([]);
  });

  it('finds CSS Gecko 91 lacks, and tolerates touch-action', () => {
    const css = '.a{height:100dvh}.b:has(.c){color:red}@container (min-width: 1px){.d{x:y}}.e{&:hover{x:y}}.f{color:color-mix(in srgb,red,blue)}@layer base{}';
    expect(scanCSS('a.css', css).errors.map((f) => f.rule)).toEqual([
      'dynamic viewport units (dvh, svh, lvh)',
      ':has()',
      'container queries',
      'CSS nesting',
      'color-mix()',
      'cascade layers',
    ]);
    const ok = scanCSS('a.css', '.a{height:100vh;touch-action:none;content:"& :has(x)"}.b>.c~.d{inset:0;gap:4px}');
    expect(ok.errors).toEqual([]);
    expect(ok.tolerated.map((f) => f.rule)).toEqual(['touch-action']);
  });

  it('follows the page into its chunks', () => {
    const html = '<script type="module" crossorigin src="/assets/share-a.js"></script><link rel="modulepreload" crossorigin href="/assets/common-b.js"><link rel="stylesheet" href="/assets/share-c.css">';
    expect(htmlAssets(html)).toEqual(['/assets/share-a.js', '/assets/common-b.js', '/assets/share-c.css']);
    expect(jsImports('import{a as b}from"./common-b.js";import"./x.js";const m=()=>import("./lazy-d.js")')).toEqual(['./common-b.js', './x.js', './lazy-d.js']);
  });

  it('lists only polyfills the check knows by name', () => {
    // Nothing is listed today: the share bundle uses none of the APIs.
    const known = JS_RULES.map((r) => r.name);
    for (const n of POLYFILLED) expect(known).toContain(n);
  });
});
