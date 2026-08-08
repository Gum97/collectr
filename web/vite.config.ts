import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { resolve } from 'node:path'

// Two entries, deliberately.
//
// The admin app is behind a login and can afford React. The public form is the
// page a customer on a phone actually waits for, and it is measured by the same
// funnel this product reports on -- so it gets a hand-written module with no
// framework in it at all. Sharing one bundle between them would mean the
// customer downloads the form builder.
export default defineConfig({
  plugins: [react()],
  build: {
    // Written straight into the Go package that embeds it. go:embed cannot
    // reach outside its own directory, and copying in the Dockerfile would mean
    // a local `go build` embeds whatever was left there last.
    outDir: resolve(__dirname, '../internal/webui/dist'),
    emptyOutDir: true,
    rollupOptions: {
      input: {
        admin: resolve(__dirname, 'index.html'),
        form: resolve(__dirname, 'src/public/form.ts'),
      },
      output: {
        entryFileNames: 'assets/[name]-[hash].js',
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash][extname]',
      },
    },
  },
  server: {
    port: 5173,
    // The Go server owns every route that is not a static asset. Proxying in
    // development keeps cookie-based sessions working without CORS, which is the
    // thing that usually forces a team into token auth by accident.
    proxy: {
      '/api': { target: 'http://localhost:8080', changeOrigin: false },
      '/r': 'http://localhost:8080',
      '/q': 'http://localhost:8080',
    },
  },
})
