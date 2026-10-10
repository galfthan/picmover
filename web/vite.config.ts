/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// The built app is embedded into the Go binary (web/embed.go). In dev,
// `npm run dev` proxies /api to a running `picmover serve` on the default port.
export default defineConfig(({ mode }) => ({
  base: '/',
  // Component tests mount Svelte in jsdom and need its client runtime.
  resolve: mode === 'test' ? { conditions: ['browser'] } : undefined,
  plugins: [svelte()],
  build: {
    outDir: 'dist',
    emptyOutDir: true, // scripts/keep-gitkeep.mjs restores dist/.gitkeep
    target: 'es2022',
    chunkSizeWarningLimit: 600,
  },
  server: {
    proxy: {
      '/api': { target: 'http://127.0.0.1:7439', changeOrigin: false, ws: false },
    },
  },
  test: {
    include: ['src/**/*.test.ts'],
    environment: 'node',
  },
}));
