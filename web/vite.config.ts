/// <reference types="vitest/config" />
import fs from 'fs'
import path from 'path'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { tanstackRouter } from '@tanstack/router-plugin/vite'
import { playwright } from '@vitest/browser-playwright'

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    tanstackRouter({
      target: 'react',
      autoCodeSplitting: true,
    }),
    react(),
    tailwindcss(),
    {
      name: 'fdsync-keep-placeholder',
      closeBundle() {
        fs.writeFileSync(path.resolve(__dirname, '../internal/panel/dist/.keep'), '')
      },
    },
  ],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  // The build is embedded into fdsync-server.exe (internal/panel).
  build: {
    outDir: '../internal/panel/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 4096, // Monaco is one big, local chunk
  },
  // `pnpm dev`: the API comes from a running fdsync-server (panel address).
  server: {
    proxy: { '/api': process.env.FDSYNC_PANEL ?? 'http://127.0.0.1:7444' },
  },
  test: {
    silent: 'passed-only',
    unstubEnvs: true,
    browser: {
      enabled: true,
      provider: playwright(),
      instances: [{ browser: 'chromium' }],
    },
    coverage: {
      // include: ['src/**/*.{js,jsx,ts,tsx}'], // Uncomment to expand the report to all src/**/* so untested modules appear as 0% coverage.
      exclude: [
        'src/components/ui/**',
        'src/assets/**',
        'src/tanstack-table.d.ts',
        'src/routeTree.gen.ts',
        'src/test-utils/**',
        'src/routes/**',
      ],
    },
  },
})
