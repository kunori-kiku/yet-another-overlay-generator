import { defineConfig } from 'vitest/config'

// Conformance test runner config (plan-5 step 7). Deliberately minimal and SEPARATE from
// vite.config.ts: the conformance suite is a pure-TS unit pin (it imports src/lib/normalizeEdges.ts
// and reads the shared Go corpus from disk via Node fs) — it needs no React/Tailwind plugins, no
// dev-server proxy, and no jsdom. A `node` environment keeps fs/path/url available and the run fast.
//
// Scoped to *.conformance.test.ts (the cross-language pins), the pure-TS compiler unit tests
// (src/compiler/**/*.test.ts — the leaf-primitive KAT/CIDR sweeps the TS port ships, plan-4), and
// the store-level seam tests (src/stores/**/*.test.ts — plan-6's local-engine seam suite, the first
// store-level frontend tests). All three families are dependency-free Node-environment unit pins
// that read fixtures from disk or drive the Zustand store with the DOM stubbed in-test; none needs
// React/Tailwind/jsdom. This file is test-only infra: it is NOT referenced by `tsc -b` (the build's
// tsconfig.app.json excludes the test globs) so it cannot affect the production build.
export default defineConfig({
  test: {
    include: [
      'src/**/*.conformance.test.ts',
      'src/compiler/**/*.test.ts',
      'src/stores/**/*.test.ts',
      // Pure lib unit pins (plan-8: lib/agentRollout — the one-click rollout orchestration + the
      // controller-version usability gate). Dependency-free node-env, same family as the above.
      'src/lib/**/*.test.ts',
    ],
    environment: 'node',
    // Globals stay OFF: the conformance test imports describe/it/expect explicitly from 'vitest',
    // so eslint sees no undefined globals and the browser-globals lint config stays untouched.
    globals: false,
  },
})
