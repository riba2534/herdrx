import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import { pwaBuild } from './pwa-build'

export default defineConfig({
  plugins: [react(), pwaBuild()],
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', ws: true },
      '/healthz': { target: 'http://127.0.0.1:8080' },
    },
  },
  build: { sourcemap: false },
  test: { environment: 'jsdom', setupFiles: './src/test/setup.ts' },
})
