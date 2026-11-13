import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// The build lands in dist/, which web/embed.go compiles into the Go binary.
// In development, `npm run dev` proxies API calls to a server on :8080.
export default defineConfig({
  plugins: [react()],
  build: { outDir: 'dist', emptyOutDir: true },
  server: {
    proxy: { '/api': 'http://localhost:8080' },
  },
});
