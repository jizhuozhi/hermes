import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  server: {
    proxy: {
      '/api': 'http://localhost:9080'
    }
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true
  }
})
