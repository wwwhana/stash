import { defineConfig } from 'vite';
import { resolve } from 'node:path';

export default defineConfig({
  define: {
    'process.env.NODE_ENV': JSON.stringify('production')
  },
  resolve: {
    // The console mounts template strings, which requires the runtime compiler.
    alias: {
      vue: 'vue/dist/vue.esm-bundler.js'
    }
  },
  build: {
    lib: {
      entry: resolve(import.meta.dirname, 'src/runtime.js'),
      name: 'StashVueRuntime',
      formats: ['iife'],
      fileName: () => 'vue-runtime.js'
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
