import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Во время разработки все запросы к /api и /healthz проксируются на локальный
// бэкенд, чтобы не возиться с CORS. Адрес бэкенда можно переопределить
// переменной BACKEND_URL.
const backend = process.env.BACKEND_URL || 'http://localhost:8080'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': { target: backend, changeOrigin: true },
      '/healthz': { target: backend, changeOrigin: true },
    },
  },
  build: {
    outDir: 'dist',
  },
})
