import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import { tanstackRouter } from '@tanstack/router-plugin/vite'
import { fileURLToPath } from 'node:url'

// A running `deed serve` (development mode, deploy/dev/deed.yaml) for `pnpm dev`.
const api = process.env.DEED_API ?? 'http://127.0.0.1:8787'

export default defineConfig({
  plugins: [tanstackRouter({ target: 'react', autoCodeSplitting: true }), react()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  build: {
    // Embedded into the Go binary (internal/webui). emptyOutDir wipes the directory; public/README.txt is copied back
    // so the placeholder that keeps `go:embed all:dist` compiling survives every build.
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 1500,
    // The CSP forbids data: fonts and scripts; keep every asset a separate file.
    assetsInlineLimit: 0,
  },
  worker: { format: 'es' },
  server: {
    // /auth/oidc/* are the single sign-on redirects (outside the JSON API). The server refuses cross-origin unsafe
    // requests, so the proxy presents the backend's own origin (its public_url) for requests from the dev server.
    proxy: {
      '/api': {
        target: api,
        configure: (proxy) => {
          proxy.on('proxyReq', (req) => {
            if (req.getHeader('origin')) req.setHeader('origin', new URL(api).origin)
          })
        },
      },
      '/auth': api,
    },
  },
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
  },
})
