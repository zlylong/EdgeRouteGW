import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { fileURLToPath, URL } from 'node:url'

// Build output goes straight into the Go embed directory; `make web` cleans it
// first (keeping .gitkeep), so emptyOutDir stays off here.
export default defineConfig({
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  build: {
    outDir: '../internal/web/dist',
    emptyOutDir: false,
    sourcemap: false,
    chunkSizeWarningLimit: 700,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://127.0.0.1:18080', changeOrigin: false },
    },
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
})
