import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { readFileSync, existsSync } from "fs";
import { viteSingleFile } from 'vite-plugin-singlefile';

// Dev HTTPS: generate certs with `mkcert localhost` or similar.
const https = process.env.CI === "true" || process.argv.includes("build") ?
  undefined :
  (existsSync("localhost-key.pem") && existsSync("localhost.pem"))
    ? { key: readFileSync("localhost-key.pem"), cert: readFileSync("localhost.pem") }
    : undefined;

export default defineConfig({
  base: '/console/',
  plugins: [react(), viteSingleFile()],
  build: {
    cssCodeSplit: false,
  },
  server: {
    port: 3000,
    https,
    proxy: {
      '/console/api': 'http://localhost:8081',
    },
  },
});
