import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  // The gateway pins the Host header to AIGW_BROWSER_ORIGIN, so the dev proxy
  // must preserve it instead of rewriting to the upstream host.
  server: {
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false },
      '/auth': { target: 'http://127.0.0.1:8080', changeOrigin: false },
    },
  },
  build: { sourcemap: false },
});
