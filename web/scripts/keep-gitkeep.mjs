// Vite empties dist/ before each build; dist/.gitkeep is committed so that
// web/embed.go's `//go:embed all:dist` always matches. Re-create it.
import { writeFileSync, mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const dist = join(dirname(fileURLToPath(import.meta.url)), '..', 'dist');
mkdirSync(dist, { recursive: true });
writeFileSync(join(dist, '.gitkeep'), '');
