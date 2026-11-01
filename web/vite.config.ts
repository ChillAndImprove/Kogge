// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { existsSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import react from '@vitejs/plugin-react';
import type { Plugin } from 'vite';
import { defineConfig } from 'vitest/config';

const backend = process.env.KOGGE_DEV_BACKEND ?? 'http://localhost:8080';

/**
 * Mermaid's optional ELK layout engine (elkjs) is EPL-2.0, which Kogge's licence policy does not allow: Mermaid is built
 * without registering it — as Mermaid's own small build does — so that diagrams asking for it get the default
 * layout, and elkjs itself resolves to a stand-in. The build fails if the registration cannot be found or elkjs
 * reaches the bundle anyway.
 */
function withoutElk(): Plugin {
  let patched = false;
  return {
    name: 'kogge-without-elk',
    enforce: 'pre',
    transform(code, id) {
      if (!id.includes('/node_modules/mermaid/') || !code.includes('elkLayoutLoaders()')) return null;
      patched = true;
      return { code: code.replace('...elkLayoutLoaders()', ''), map: null };
    },
    generateBundle(_, bundle) {
      const chunks = Object.values(bundle).filter((c) => c.type === 'chunk');
      if (
        chunks.some((c) => Object.keys(c.modules).some((m) => m.includes('/node_modules/mermaid/'))) &&
        !patched
      ) {
        this.error('Mermaid was built without removing its ELK layout loaders: has Mermaid changed?');
      }
      for (const c of chunks) {
        const elk = Object.keys(c.modules).find((m) => m.includes('/node_modules/elkjs/'));
        if (elk) this.error(`elkjs (EPL-2.0) reached the bundle in ${c.fileName} through ${elk}`);
      }
    },
  };
}

/**
 * KaTeX's stylesheet offers each of its fonts as woff2, woff and ttf; browsers take the first, so only the woff2
 * files are built (the fonts stay as they are). KaTeX loads with the first formula shown
 * (MathFormula.tsx). The build fails if KaTeX's stylesheet changed, if other formats get in anyway, or if KaTeX
 * reaches a chunk that loads with the application.
 */
function katexWoff2Only(): Plugin {
  return {
    name: 'kogge-katex-woff2-only',
    enforce: 'pre',
    transform(code, id) {
      if (!id.endsWith('/node_modules/katex/dist/katex.min.css')) return null;
      const woff2 = code.replace(
        /,url\(fonts\/KaTeX_[\w-]+\.(?:woff|ttf)\) format\("(?:woff|truetype)"\)/g,
        '',
      );
      if (woff2 === code)
        this.error("KaTeX's stylesheet names no woff or ttf fonts to leave out: has KaTeX changed?");
      return { code: woff2, map: null };
    },
    generateBundle(_, bundle) {
      const files = Object.values(bundle);
      for (const f of files) {
        if (f.type === 'asset' && /KaTeX_[\w-]+\.(?:woff|ttf)$/.test(f.fileName))
          this.error(`${f.fileName}: KaTeX's fonts are built as woff2 only`);
      }
      const chunks = files.filter((f) => f.type === 'chunk');
      // The chunks the application loads as it starts: the entries and what they import, not what they load later.
      const eager = new Set<string>();
      const visit = (name: string) => {
        const c = bundle[name];
        if (eager.has(name) || c?.type !== 'chunk') return;
        eager.add(name);
        c.imports.forEach(visit);
      };
      chunks.filter((c) => c.isEntry).forEach((c) => visit(c.fileName));
      for (const c of chunks) {
        const katex = Object.keys(c.modules).find((m) => m.includes('/node_modules/katex/'));
        if (katex && eager.has(c.fileName))
          this.error(`KaTeX (${katex}) reached ${c.fileName}, which loads with the application`);
      }
    },
  };
}

/** The preview (mode showcase) ships the answers recorded on a Kogge with sample content (make showcase-record). */
function showcaseRecording(): Plugin {
  const file = fileURLToPath(new URL('./showcase/recording.json', import.meta.url));
  return {
    name: 'kogge-showcase-recording',
    generateBundle() {
      if (!existsSync(file)) this.error(`${file} is missing: record it with make showcase-record`);
      this.emitFile({ type: 'asset', fileName: 'recording.json', source: readFileSync(file) });
    },
  };
}

export default defineConfig(({ mode }) => ({
  plugins: [react(), withoutElk(), katexWoff2Only(), ...(mode === 'showcase' ? [showcaseRecording()] : [])],
  // The preview lives in a folder of GitHub Pages: its files refer to each other relatively.
  base: mode === 'showcase' ? './' : '/',
  resolve: {
    alias: {
      'elkjs/lib/elk.bundled.js': fileURLToPath(new URL('./src/lib/elk-unavailable.ts', import.meta.url)),
    },
  },
  build: {
    outDir: mode === 'showcase' ? 'dist-showcase' : '../internal/webui/dist',
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 1500,
  },
  server: {
    port: 5173,
    proxy: {
      // ws: the page editor's shared draft travels over a WebSocket (/api/v1/wiki/pages/…/edit/socket).
      '/api': { target: backend, changeOrigin: false, ws: true },
      '/auth': { target: backend, changeOrigin: false },
      '/scm': { target: backend, changeOrigin: false },
    },
  },
  test: {
    environment: 'jsdom',
    globals: false,
  },
}));
