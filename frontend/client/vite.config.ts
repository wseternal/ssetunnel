import { defineConfig } from 'vite';
import { resolve } from 'path';
import { fileURLToPath } from 'url';

import react from '@vitejs/plugin-react';

const projectDir = fileURLToPath(new URL('.', import.meta.url));

// Base path for the client SPA. The cli-ui server serves at root,
// so assets must reference /assets/... (not /client/assets/...).
const base = '/';

// `../shared/modules` lives outside this project, so Node resolution from
// there cannot find its dependencies. Alias its imports back into this
// project's node_modules — symlinking the directory instead would let Rollup
// resolve a second React copy and break hooks.
const sharedDeps = ['react', 'react-dom', '@mui/material', '@mui/icons-material'];
const sharedAlias = sharedDeps.map((pkg) => ({
  find: new RegExp(`^${pkg}(/.*)?$`),
  replacement: `${resolve(projectDir, 'node_modules', pkg)}$1`,
}));

export default defineConfig({
  base,
  plugins: [react()],
  resolve: { alias: sharedAlias },
  build: {
    // es2022 preserves TypeScript enum declarations inside methods,
    // avoiding the IIFE elision bug documented in the console vite.config.ts.
    target: 'es2022',
  },
  server: {
    port: 3001,
  },
});
