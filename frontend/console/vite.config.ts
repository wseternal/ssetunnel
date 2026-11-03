import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { readFileSync, existsSync } from "fs";

// Dev HTTPS: generate certs with `mkcert localhost` or similar.
const https = process.env.CI === "true" || process.argv.includes("build") ?
  undefined :
  (existsSync("localhost-key.pem") && existsSync("localhost.pem"))
    ? { key: readFileSync("localhost-key.pem"), cert: readFileSync("localhost.pem") }
    : undefined;

export default defineConfig({
  base: process.env.VITE_BASE || '/',
  plugins: [react()],
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
      // Dev-only: matches when base='/' (VITE_BASE unset).
      // Production deployments use a reverse proxy for path stripping.
      '/api': 'http://localhost:8081',
    },
  },
});
