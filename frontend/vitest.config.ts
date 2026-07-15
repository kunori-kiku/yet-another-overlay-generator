import { defineConfig } from 'vitest/config'

// Conformance test runner config (plan-5 step 7). Deliberately minimal and SEPARATE from
// vite.config.ts: the conformance suite is a pure-TS unit pin (it imports src/lib/normalizeEdges.ts
// and reads the shared Go corpus from disk via Node fs) — it needs no React/Tailwind plugins, no
// dev-server proxy, and no jsdom. A `node` environment keeps fs/path/url available and the run fast.
//
// Collects EVERY test under src/ via a single recursive glob (plan-0, FE ratchet-hygiene): the
// former per-directory allow-list ('src/**/*.conformance.test.ts' + 'src/compiler|stores|lib/**')
// silently DROPPED any test file placed outside those directories — e.g. a hypothetical
// src/hooks/*.test.ts or src/components/*.test.tsx would never run and no one would notice. The
// recursive '*.test.ts(x)' glob makes a misplaced test uncollectable-by-omission impossible: if a
// file is named as a test, it runs. The families this used to enumerate — *.conformance.test.ts
// (cross-language pins), src/compiler/**/*.test.ts (plan-4 leaf-primitive KAT/CIDR sweeps),
// src/stores/**/*.test.ts (plan-6 store-level seam suites), src/lib/**/*.test.ts (plan-8 pure-lib
// pins) — are all a strict subset of this glob, so the collected set only ever grows, never shrinks.
// All are dependency-free Node-environment unit pins that read fixtures from disk or drive the
// Zustand store with the DOM stubbed in-test; none needs React/Tailwind/jsdom. This file is
// test-only infra: it is NOT referenced by `tsc -b` (the build's tsconfig.app.json excludes the
// test globs) so it cannot affect the production build.
export default defineConfig({
  test: {
    include: ['src/**/*.test.ts', 'src/**/*.test.tsx'],
    environment: 'node',
    setupFiles: ['./vitest.setup.ts'],
    // Globals stay OFF: the conformance test imports describe/it/expect explicitly from 'vitest',
    // so eslint sees no undefined globals and the browser-globals lint config stays untouched.
    globals: false,
  },
})
