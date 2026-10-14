// Fails `npm run build` when the phone app's chunks (dist/share.html and
// everything it loads) use runtime APIs or CSS that the Sailfish Browser
// (Gecko ESR 91) lacks and src/share/polyfills.ts does not install, or
// contain admin code (docs/design/lan-share.md "Sailfish Browser
// specifics"). It also prints the share bundle's size.
//
//   node scripts/check-share-apis.mjs [distDir]
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join, posix } from 'node:path';
import { fileURLToPath } from 'node:url';
import { htmlAssets, jsImports, scanCSS, scanJS } from './share-compat.mjs';

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

const seen = new Set();
const queue = htmlAssets(readFileSync(page, 'utf8')).map((p) => p.replace(/^\//, ''));
const errors = [];
const tolerated = new Set();
let jsBytes = 0;
let cssBytes = 0;
while (queue.length) {
  const rel = queue.shift();
  if (!rel || seen.has(rel)) continue;
  seen.add(rel);
  const code = readFileSync(join(dist, rel), 'utf8');
  if (rel.endsWith('.css')) {
    cssBytes += Buffer.byteLength(code);
    const r = scanCSS(rel, code);
    errors.push(...r.errors);
    for (const t of r.tolerated) tolerated.add(t.rule);
    continue;
  }
  jsBytes += Buffer.byteLength(code);
  errors.push(...scanJS(rel, code, polyfilled));
  for (const imp of jsImports(code)) queue.push(posix.normalize(posix.join(posix.dirname(rel), imp)));
}

const kb = (n) => `${(n / 1024).toFixed(1)} KiB`;
console.log(`check-share-apis: share.html loads ${seen.size} files, JS ${kb(jsBytes)}, CSS ${kb(cssBytes)}`);
if (tolerated.size) console.log(`check-share-apis: tolerated (with fallbacks): ${[...tolerated].join(', ')}`);
if (errors.length) {
  for (const e of errors) console.error(`check-share-apis: ${e.file}: ${e.rule} (${e.since})`);
  console.error('check-share-apis: the phone app must run on the Sailfish Browser (Gecko ESR 91); add a polyfill to src/share/polyfills.ts or avoid the feature');
  process.exit(1);
}
