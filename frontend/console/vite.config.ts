import { defineConfig } from 'vite';
import { resolve } from 'path';
import { fileURLToPath } from 'url';

import react from '@vitejs/plugin-react';

const projectDir = fileURLToPath(new URL('.', import.meta.url));
import { readFileSync, existsSync } from "fs";

// Dev HTTPS: generate certs with `mkcert localhost` or similar.
const https = process.env.CI === "true" || process.argv.includes("build") ?
  undefined :
  (existsSync("localhost-key.pem") && existsSync("localhost.pem"))
    ? { key: readFileSync("localhost-key.pem"), cert: readFileSync("localhost.pem") }
    : undefined;

// Base path for the SPA — the single source of truth. Every asset URL and
// the dev proxy key are derived from this. Change it here and rebuild; nothing
// else needs updating.
const base = '/console';
const apiProxyKey = `${base}/api`;

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
    // esbuild's default build target lowers TypeScript enums inside methods
    // to an IIFE and, together with minify's dead-code pass, drops the enum
    // variable declaration — emitting `void 0||(r={})` with no `var r`. In the
    // strict-mode bundle the first DECRQM (`ESC [ ? … $ p`) from vim then
    // throws `ReferenceError: r is not defined` inside xterm's async write
    // loop, permanently wedging the terminal (frozen screen, live input).
    // es2022 keeps the declaration (`let r;`) and still minifies.
    target: 'es2022',
  },
  server: {
    port: 3000,
    https,
    proxy: {
      // Derived from base so the proxy key always matches the fetch URLs
      // App.tsx builds from import.meta.env.BASE_URL.
      // timeout: 0 disables the default ~120s proxy timeout so long-lived
      // SSE streams (cloud shell, remote desktop) are not killed during idle.
      [apiProxyKey]: {
        target: 'http://localhost:8081',
        // The server registers routes at root (/api/v1/...), so the dev
        // proxy must strip the base prefix before forwarding. In production
        // the reverse proxy (or --console-prefix) does the same stripping.
        rewrite: (path: string) => path.startsWith(base) ? path.slice(base.length) : path,
        timeout: 0,
        proxyTimeout: 0,
      },
    },
  },
});
