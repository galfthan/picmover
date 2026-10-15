// Fails `npm run build` when the phone app's chunks (dist/share.html and
// everything it loads) use runtime APIs or CSS that the Sailfish Browser
// (Gecko ESR 91) lacks and src/share/polyfills.ts does not install, or
// contain admin code (docs/design/lan-share.md "Sailfish Browser
// specifics"). It follows dynamic imports and their preloaded files too
// (the map's Leaflet chunk and stylesheet), and prints the share bundle's
// size: what the first page load fetches, and what loads on demand.
//
//   node scripts/check-share-apis.mjs [distDir]
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join, posix } from 'node:path';
import { fileURLToPath } from 'node:url';
import { htmlAssets, jsImports, jsLazyImports, preloadDeps, scanCSS, scanJS } from './share-compat.mjs';

const web = join(dirname(fileURLToPath(import.meta.url)), '..');
const dist = process.argv[2] ?? join(web, 'dist');
const page = join(dist, 'share.html');
if (!existsSync(page)) {
  console.error(`check-share-apis: ${page} is missing; the phone app was not built`);
  process.exit(1);
}

// The polyfilled API names, as listed in POLYFILLED.
const polySrc = readFileSync(join(web, 'src', 'share', 'polyfills.ts'), 'utf8');
const listed = /POLYFILLED[^=]*=\s*\[([^\]]*)\]/.exec(polySrc);
const polyfilled = listed ? [...(listed[1] ?? '').matchAll(/'([^']+)'/g)].map((m) => m[1] ?? '') : [];

// Breadth first: the files of the first load (the page's own and their
// static imports) come before anything reached only by a dynamic import.
const seen = new Map(); // path -> lazy
const queue = htmlAssets(readFileSync(page, 'utf8')).map((p) => ({ rel: p.replace(/^\//, ''), lazy: false }));
const errors = [];
const tolerated = new Set();
const bytes = { first: { js: 0, css: 0, n: 0 }, lazy: { js: 0, css: 0, n: 0 } };
while (queue.length) {
  queue.sort((a, b) => Number(a.lazy) - Number(b.lazy));
  const next = queue.shift();
  if (!next || seen.has(next.rel)) continue;
  const { rel, lazy } = next;
  seen.set(rel, lazy);
  const code = readFileSync(join(dist, rel), 'utf8');
  const b = lazy ? bytes.lazy : bytes.first;
  b.n++;
  if (rel.endsWith('.css')) {
    b.css += Buffer.byteLength(code);
    const r = scanCSS(rel, code);
    errors.push(...r.errors);
    for (const t of r.tolerated) tolerated.add(t.rule);
    continue;
  }
  b.js += Buffer.byteLength(code);
  errors.push(...scanJS(rel, code, polyfilled));
  const dynamic = new Set(jsLazyImports(code));
  for (const imp of jsImports(code)) queue.push({ rel: posix.normalize(posix.join(posix.dirname(rel), imp)), lazy: lazy || dynamic.has(imp) });
  for (const dep of preloadDeps(code)) queue.push({ rel: posix.normalize(dep), lazy: true });
}

const kb = (n) => `${(n / 1024).toFixed(1)} KiB`;
const { first, lazy } = bytes;
console.log(`check-share-apis: share.html loads ${first.n} files, JS ${kb(first.js)}, CSS ${kb(first.css)}`);
if (lazy.n) console.log(`check-share-apis: on demand (the map): ${lazy.n} more files, JS ${kb(lazy.js)}, CSS ${kb(lazy.css)}`);
if (tolerated.size) console.log(`check-share-apis: tolerated (with fallbacks): ${[...tolerated].join(', ')}`);
if (errors.length) {
  for (const e of errors) console.error(`check-share-apis: ${e.file}: ${e.rule} (${e.since})`);
  console.error('check-share-apis: the phone app must run on the Sailfish Browser (Gecko ESR 91); add a polyfill to src/share/polyfills.ts or avoid the feature');
  process.exit(1);
}
