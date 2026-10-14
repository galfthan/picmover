/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// The built app is embedded into the Go binary (web/embed.go). In dev,
// `npm run dev` proxies /api to a running `picmover serve` on the default port.
//
// Two entries: index.html (the admin UI) and share.html (the phone app the
// share listener serves; docs/design/lan-share.md "Phone UI"). Shared
// components land in common chunks; the phone never loads admin pages.
// One target for both: the Sailfish Browser is Gecko ESR 91, and the admin
// UI loses nothing measurable. esbuild lowers syntax, not APIs:
// scripts/check-share-apis.mjs (run by `npm run build`) fails on APIs and
// CSS that Gecko 91 lacks in the share chunks.
export const TARGET = ['es2020', 'firefox91', 'safari15', 'chrome100'];

export default defineConfig(({ mode }) => ({
  base: '/',
  // Component tests mount Svelte in jsdom and need its client runtime.
  resolve: mode === 'test' ? { conditions: ['browser'] } : undefined,
  plugins: [svelte()],
  build: {
    outDir: 'dist',
    emptyOutDir: true, // scripts/keep-gitkeep.mjs restores dist/.gitkeep
    target: TARGET,
    chunkSizeWarningLimit: 600,
    rollupOptions: {
      input: { index: 'index.html', share: 'share.html' },
    },
  },
  server: {
    proxy: {
      '/api': { target: 'http://127.0.0.1:7439', changeOrigin: false, ws: false },
    },
  },
  test: {
    include: ['src/**/*.test.ts'],
    environment: 'node',
    // The first test of a file that mounts App (after vi.resetModules)
    // imports and transforms the whole admin app: about a second on an
    // idle machine, more than vitest's default 5 s when the CPU is busy
    // (a parallel build, go test). The tests themselves run on fake
    // timers, so the timeout only guards against a hang.
    testTimeout: 30_000,
    hookTimeout: 30_000,
  },
}));
