import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { viteSingleFile } from 'vite-plugin-singlefile';

export default defineConfig({
  base: '/console/',
  plugins: [react(), viteSingleFile()],
  build: {
    cssCodeSplit: false,
  },
  server: {
    port: 3000,
    proxy: {
      '/console/api': 'http://localhost:8081',
    },
  },
});
