import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// The manager serves the built app and proxies /api + /ws on the same origin.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: '/',
  build: {
    outDir: 'dist', emptyOutDir: true, assetsInlineLimit: 0,
    rollupOptions: { output: {
      entryFileNames: 'assets/app.js',
      chunkFileNames: 'assets/[name]-[hash].js',
      assetFileNames: asset => asset.name?.endsWith('.css') ? 'assets/app.css' : 'assets/[name]-[hash][extname]',
      manualChunks: { react: ['react', 'react-dom'] },
    } },
  },
  // For local UI development, proxy API/WS to a running control plane. Defaults to
  // localhost; override with MDD_DEV_API (e.g. https://gateway-host:8443).
  server: {
    proxy: {
      '/api': { target: process.env.MDD_DEV_API || 'https://localhost:8443', changeOrigin: true, ws: true, secure: false },
      '/ws': { target: (process.env.MDD_DEV_API || 'https://localhost:8443').replace(/^http/, 'ws'), ws: true, secure: false },
    },
  },
})
