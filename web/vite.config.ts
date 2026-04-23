import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// In dev, the Go API runs on :9090 and Vite on :5173. Proxy /api through so
// the browser talks to one origin and SSE/CORS is a non-issue.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:9090',
        changeOrigin: false,
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
});
