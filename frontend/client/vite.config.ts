import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Base path for the client SPA. Independent from the admin console at /console.
const base = '/client';

export default defineConfig({
  base,
  plugins: [react()],
  build: {
    // es2022 preserves TypeScript enum declarations inside methods,
    // avoiding the IIFE elision bug documented in the console vite.config.ts.
    target: 'es2022',
  },
  server: {
    port: 3001,
  },
});
