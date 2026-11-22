import {defineConfig} from 'vite';
import {resolve} from 'node:path';
import {writeFileSync} from 'node:fs';
import {injectQr} from './qr.js';

// Run from web/. Output goes to web/dist (here: dist), which the Go binary embeds. Fonts must stay separate files:
// the Content-Security-Policy does not allow data: fonts.
export default defineConfig({
  esbuild: {jsx: 'automatic'},
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    assetsInlineLimit: 0,
    rollupOptions: {input: {index: resolve('index.html'), landing: resolve('landing.html'), privacy: resolve('privacy.html')}},
  },
  plugins: [{
    name: 'keep-embed-dir',
    // go:embed needs at least one file in dist even before the first build.
    closeBundle() { writeFileSync('dist/.gitkeep', ''); },
  }, {
    name: 'landing-qr',
    transformIndexHtml: injectQr,
  }],
  server: {proxy: {'/api': 'http://localhost:8080'}},
});
