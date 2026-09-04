import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { readFileSync } from "fs";
import { viteSingleFile } from 'vite-plugin-singlefile';

const https = process.env.CI === "true" || process.argv[2] === "build" ?
  undefined : { key: readFileSync("localhost-key.pem"), cert: readFileSync("localhost.pem")};

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
