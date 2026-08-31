import { defineConfig } from 'vite';
import { resolve } from 'node:path';

export default defineConfig({
  resolve: {
    // The monitor uses a template string so the standalone bundle can be
    // mounted from an embedded HTML entry point.
    alias: {
      vue: 'vue/dist/vue.esm-bundler.js'
    }
  },
  build: {
    lib: {
      entry: resolve(import.meta.dirname, 'src/monitor.js'),
      name: 'StashVueMonitor',
      formats: ['iife'],
      fileName: () => 'vue-monitor.js'
    },
    outDir: resolve(import.meta.dirname, '../internal/web/ui'),
    emptyOutDir: false,
    minify: 'esbuild',
    sourcemap: false,
    rollupOptions: {
      output: {
        inlineDynamicImports: true
      }
    }
  }
});
