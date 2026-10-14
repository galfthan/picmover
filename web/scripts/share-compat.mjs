// What the phone app's bundle must not use: runtime APIs and CSS that the
// Sailfish Browser (Gecko ESR 91, Sailfish OS 5.1) lacks, measured by the
// Phase 0 probe (docs/design/lan-share.md "Phase 0 probe"). esbuild lowers
// syntax for the build target but never APIs or these CSS features, so
// check-share-apis.mjs scans the built share chunks with these lists after
// every `npm run build`. Pure (no Node APIs), so a test can use it too.

/**
 * @typedef {{ name: string, re: RegExp, since: string }} Rule
 * @typedef {{ file: string, rule: string, since: string }} Finding
 */

/** @type {Rule[]} Runtime APIs missing in Gecko 91, with the version that added them. */
export const JS_RULES = [
  { name: 'Object.hasOwn', re: /\bObject\.hasOwn\b/, since: 'Gecko 92' },
  { name: 'structuredClone', re: /\bstructuredClone\b/, since: 'Gecko 94' },
  { name: 'Array.prototype.findLast', re: /\.findLast(?:Index)?\(/, since: 'Gecko 104' },
  { name: 'Array.prototype.toSorted', re: /\.to(?:Sorted|Reversed|Spliced)\(/, since: 'Gecko 115' },
  { name: 'Array.prototype.with', re: /\.with\(/, since: 'Gecko 115' },
  { name: 'Array.fromAsync', re: /\bArray\.fromAsync\b/, since: 'Gecko 115' },
  { name: 'Object.groupBy', re: /\b(?:Object|Map)\.groupBy\b/, since: 'Gecko 119' },
  { name: 'Promise.withResolvers', re: /\bPromise\.withResolvers\b/, since: 'Gecko 121' },
  { name: 'Set methods', re: /\.(?:union|intersection|symmetricDifference|isSubsetOf|isSupersetOf|isDisjointFrom)\(/, since: 'Gecko 127' },
  { name: 'String.prototype.isWellFormed', re: /\.(?:isWellFormed|toWellFormed)\(/, since: 'Gecko 119' },
  { name: 'crypto.randomUUID', re: /\.randomUUID\(/, since: 'Gecko 95' },
  { name: 'AbortSignal.timeout', re: /\bAbortSignal\.(?:timeout|any)\b/, since: 'Gecko 100' },
  { name: 'fetch keepalive', re: /\bkeepalive\s*:/, since: 'not in Gecko 91' },
  { name: 'HTMLDialogElement', re: /\.showModal\(|\bHTMLDialogElement\b/, since: 'Gecko 98' },
  { name: 'Popover API', re: /\.(?:showPopover|hidePopover|togglePopover)\(/, since: 'Gecko 125' },
  { name: 'Element.checkVisibility', re: /\.checkVisibility\(/, since: 'Gecko 106' },
  { name: 'Intl.Segmenter', re: /\bIntl\.Segmenter\b/, since: 'Gecko 125' },
];

/** @type {Rule[]} CSS that Gecko 91 lacks (and that esbuild does not lower). */
export const CSS_RULES = [
  { name: 'dynamic viewport units (dvh, svh, lvh)', re: /\d(?:[dsl]v(?:h|w|i|b|min|max))\b/, since: 'Gecko 101' },
  { name: ':has()', re: /:has\(/, since: 'Gecko 121' },
  { name: 'container queries', re: /@container\b|\bcontainer-type\s*:|\d(?:cqw|cqh|cqi|cqb|cqmin|cqmax)\b/, since: 'Gecko 110' },
  { name: 'CSS nesting', re: /(?:^|[{};,\s])&[\w.:#[\s>+~*-]/, since: 'Gecko 117' },
  { name: 'color-mix()', re: /\bcolor-mix\(/, since: 'Gecko 113' },
  { name: 'cascade layers', re: /@layer\b/, since: 'Gecko 97' },
];

/**
 * CSS that Gecko 91 ignores but that is allowed because every use has a
 * fallback: `touch-action` (the lightbox and the scrubber cancel the
 * browser's panning with non-passive touch listeners themselves).
 * @type {Rule[]}
 */
export const CSS_TOLERATED = [{ name: 'touch-action', re: /\btouch-action\s*:/, since: 'not in Sailfish Browser' }];

/**
 * Admin code that must never be in the phone's chunks: URLs only the admin
 * client builds (the share listener has no such routes).
 * @type {Rule[]}
 */
export const ADMIN_MARKERS = [
  { name: 'admin API /api/jobs', re: /\/api\/jobs\b/, since: '' },
  { name: 'admin API /api/fs', re: /\/api\/fs\b/, since: '' },
  { name: 'admin API /api/share', re: /\/api\/share\b/, since: '' },
  { name: 'admin API /api/app', re: /\/api\/app\b/, since: '' },
];

/**
 * Drops string literals and comments from CSS, so `content: "&"` cannot trip a rule.
 * @param {string} css
 */
function cssCode(css) {
  return css.replace(/\/\*[\s\S]*?\*\//g, '').replace(/"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'/g, '""');
}

/**
 * The rules a chunk breaks.
 * @param {string} file
 * @param {string} code
 * @param {readonly string[]} polyfilled API names installed by src/share/polyfills.ts
 * @returns {Finding[]}
 */
export function scanJS(file, code, polyfilled = []) {
  /** @type {Finding[]} */
  const out = [];
  for (const r of JS_RULES) if (!polyfilled.includes(r.name) && r.re.test(code)) out.push({ file, rule: r.name, since: r.since });
  for (const r of ADMIN_MARKERS) if (r.re.test(code)) out.push({ file, rule: r.name, since: 'admin code in the share bundle' });
  return out;
}

/**
 * @param {string} file
 * @param {string} css
 * @returns {{ errors: Finding[], tolerated: Finding[] }}
 */
export function scanCSS(file, css) {
  const code = cssCode(css);
  return {
    errors: CSS_RULES.filter((r) => r.re.test(code)).map((r) => ({ file, rule: r.name, since: r.since })),
    tolerated: CSS_TOLERATED.filter((r) => r.re.test(code)).map((r) => ({ file, rule: r.name, since: r.since })),
  };
}

/**
 * The assets a built HTML page loads: its module scripts, module preloads and stylesheets.
 * @param {string} html
 * @returns {string[]} paths as written (e.g. "/assets/share-x.js")
 */
export function htmlAssets(html) {
  /** @type {string[]} */
  const out = [];
  const re = /<(?:script|link)\b[^>]*?\b(?:src|href)="([^"]+\.(?:js|css))"/g;
  for (let m = re.exec(html); m; m = re.exec(html)) if (m[1]) out.push(m[1]);
  return out;
}

/**
 * The chunks a built JS chunk imports, statically or dynamically, as relative specifiers ("./x.js").
 * @param {string} code
 * @returns {string[]}
 */
export function jsImports(code) {
  /** @type {string[]} */
  const out = [];
  const re = /\b(?:from|import)\s*\(?\s*["'](\.{1,2}\/[^"']+\.js)["']/g;
  for (let m = re.exec(code); m; m = re.exec(code)) if (m[1]) out.push(m[1]);
  return out;
}
