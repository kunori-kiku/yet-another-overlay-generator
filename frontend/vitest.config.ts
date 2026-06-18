import { defineConfig } from 'vitest/config'

// Conformance test runner config (plan-5 step 7). Deliberately minimal and SEPARATE from
// vite.config.ts: the conformance suite is a pure-TS unit pin (it imports src/lib/normalizeEdges.ts
// and reads the shared Go corpus from disk via Node fs) — it needs no React/Tailwind plugins, no
// dev-server proxy, and no jsdom. A `node` environment keeps fs/path/url available and the run fast.
//
// Scoped to *.conformance.test.ts so `npm run conformance` only runs the cross-language pins and
// never picks up future component/unit tests. This file is test-only infra: it is NOT referenced by
// `tsc -b` (the build's tsconfig.app.json excludes the conformance glob) so it cannot affect the
// production build.
export default defineConfig({
  test: {
    include: ['src/**/*.conformance.test.ts'],
    environment: 'node',
    // Globals stay OFF: the conformance test imports describe/it/expect explicitly from 'vitest',
    // so eslint sees no undefined globals and the browser-globals lint config stays untouched.
    globals: false,
  },
})
