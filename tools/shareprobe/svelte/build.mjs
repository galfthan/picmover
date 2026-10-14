// Builds the probe's Svelte bundle with the repo's web toolchain (Vite +
// @sveltejs/vite-plugin-svelte from web/node_modules) for the share
// entry's planned target, into ../static/svelte/probe.js (committed, so
// running the probe needs no Node).
//
//   (cd web && npm ci) && node tools/shareprobe/svelte/build.mjs
//
// It then lists the post-Gecko-91 runtime APIs that occur in the bundle
// (esbuild lowers syntax, not APIs), which is what the design's
// check-share-apis.mjs will guard.
import { createRequire } from 'node:module';
import { existsSync, readFileSync, symlinkSync, unlinkSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const web = join(here, '..', '..', '..', 'web');
const req = createRequire(join(web, 'package.json'));
const load = (id) => import(pathToFileURL(req.resolve(id)).href);
const { build } = await load('vite');
const { svelte } = await load('@sveltejs/vite-plugin-svelte');
const svelteVersion = JSON.parse(readFileSync(join(web, 'node_modules', 'svelte', 'package.json'), 'utf8')).version;
const target = ['es2020', 'firefox91', 'safari15', 'chrome100'];

// This directory has no node_modules of its own: link web's for the
// build, so bare imports (svelte, svelte/internal/…) resolve normally.
const link = join(here, 'node_modules');
const made = !existsSync(link);
if (made) symlinkSync(join(web, 'node_modules'), link, 'dir');
try {
await build({
  configFile: false,
  root: here,
  logLevel: 'warn',
  plugins: [svelte({ configFile: false })],
  define: { __SVELTE_VERSION__: JSON.stringify(svelteVersion) },
  esbuild: { target },
  build: {
    target,
    outDir: join(here, '..', 'static', 'svelte'),
    emptyOutDir: true,
    minify: true,
    lib: { entry: join(here, 'main.js'), formats: ['es'], fileName: () => 'probe.js' },
    rollupOptions: { output: { inlineDynamicImports: true } },
  },
});
} finally {
  if (made) unlinkSync(link);
}

const out = readFileSync(join(here, '..', 'static', 'svelte', 'probe.js'), 'utf8');
const apis = ['Object.hasOwn', 'structuredClone', '.findLast(', '.findLastIndex(', '.toSorted(', '.toReversed(', '.toSpliced(', '.with(', '.at('];
const found = apis.filter((a) => out.includes(a));
console.log(`built static/svelte/probe.js (${out.length} bytes, svelte ${svelteVersion}, target ${target.join(',')})`);
console.log(found.length ? `post-Gecko-91 APIs referenced: ${found.join(', ')}` : 'no listed post-Gecko-91 APIs referenced');
