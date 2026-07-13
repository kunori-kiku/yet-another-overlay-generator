import { defineConfig, devices } from '@playwright/test'

// The responsive device matrix (plan-17 / 3.5) runs only e2e/responsive/**; everything else is
// desktop-only. One regex, used by both the ignore (functional project) and match (device projects).
const RESPONSIVE_MATCH = /responsive\/.*\.spec\.ts$/

// The WASM multi-browser soak (framework-refactor plan-4): the wasm-design flow — the only local
// compute path that runs the in-browser Go/WASM engine end-to-end — fans out across the two
// non-Chromium engines so the manual multi-browser soak becomes an automated per-browser check
// (chromium runs it as the perpetual guard via the functional project's testIgnore below; these
// two run ONLY this spec, to stay fast + targeted). WebKit is the headline target: a
// WebKit-specific WASM quirk is exactly what the soak adds over the headless node gate.
const WASM_DESIGN_MATCH = /wasm-design\.spec\.ts$/

// The soak projects are OPT-IN (YAOG_WASM_SOAK=1) so the required frontend-e2e CI job — which
// installs ONLY Chromium — can never go red on an uninstalled browser: the projects simply do not
// exist unless opted in. CI keeps the Chromium perpetual guard; a local run (or a CI that first
// `npx playwright install webkit firefox`) enables the soak with the env flag. This is the
// automated multi-browser soak evidence gating plan-5 (the TS-twin deletion).
const wasmSoakProjects =
  process.env.YAOG_WASM_SOAK === '1'
    ? [
        { name: 'webkit', testMatch: WASM_DESIGN_MATCH, use: { ...devices['Desktop Safari'] } },
        { name: 'firefox', testMatch: WASM_DESIGN_MATCH, use: { ...devices['Desktop Firefox'] } },
      ]
    : []

// Playwright config for the YAOG browser E2E layer (plan-13 / milestone 3.1) — the first
// browser end-to-end tests the project has had. It runs the REAL built panel (served by a
// test-mode Go controller, EnableStatic) against a live controller + a real agent fixture.
//
// globalSetup boots TWO cmd/e2eserver processes (--mode controller ×2 for the keystone-OFF and
// keystone-ON tenants) on OS-assigned :0 ports and writes their resolved ports +
// enrollment tokens to a handoff file the specs read (e2e/fixtures/harness.ts). It is NOT the
// default webServer.url wait — that checks a single fixed port and cannot capture the other boots'
// ports or the enroll tokens. globalTeardown kills the children and removes the temp state dirs.
//
// SERIAL by design (workers: 1, no parallelism, no retries): the two canaries share the
// single pre-minted enrollment token and the two long-lived boots, and the required-from-
// day-one CI gate (owner decision) wants flakes surfaced, never masked by a retry. The Go
// bring-up is the determinism guarantee — readiness is gated on each boot's E2E_READY line,
// never a sleep.
export default defineConfig({
  testDir: 'e2e',
  // One worker, serial: shared single-use enroll token + shared boots; determinism over speed.
  workers: 1,
  fullyParallel: false,
  // No retries: a flake must red the gate (required-from-day-one), not be papered over.
  retries: 0,
  // `.only` left in a committed spec fails CI rather than silently narrowing the suite.
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  globalSetup: './e2e/globalSetup.ts',
  globalTeardown: './e2e/globalTeardown.ts',
  // Visual-regression baselines (plan-17 / 3.5) live under e2e/responsive/__screenshots__/, keyed by
  // project (viewport) + platform so a phone baseline never compares against a desktop one. The
  // `.gitignore` KEEPS this dir while ignoring playwright-report/ + test-results/.
  snapshotPathTemplate: 'e2e/responsive/__screenshots__/{projectName}-{platform}/{arg}{ext}',
  use: {
    // Pin the locale so the panel's detectSystemLanguage() resolves to English
    // deterministically (it returns 'zh' only for a zh navigator.language) — specs assert
    // against English UI strings.
    locale: 'en-US',
    // Capture a trace + screenshot only when a test fails, for the CI failure artifact.
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  // Visual-regression defaults (plan-17 / 3.5): a small non-zero pixel-ratio tolerance absorbs
  // sub-pixel AA noise without masking a real layout break; animations/caret are killed so a
  // snapshot is deterministic. Baselines are authoritative on Linux CI (see e2e/README.md).
  expect: {
    toHaveScreenshot: { maxDiffPixelRatio: 0.02, animations: 'disabled', caret: 'hide' },
  },
  projects: [
    // The functional + adversarial suites (plan-13/14/15/16) stay DESKTOP-ONLY: one pass, no device
    // fan-out. testIgnore keeps the responsive matrix below from re-running them three times.
    { name: 'chromium', testIgnore: RESPONSIVE_MATCH, use: { ...devices['Desktop Chrome'] } },
    // The responsive matrix (plan-17): the lg=1024 pivot. e2e/responsive/** fans out across all
    // three. Specs branch on testInfo.project.name (>= lg = 'desktop'; < lg = 'phone'/'tablet').
    { name: 'desktop', testMatch: RESPONSIVE_MATCH, use: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 800 }, hasTouch: false } },
    // Pixel-7-class mobile emulation (touch + mobile UA) pinned to the 360-wide narrow edge — the
    // worst case for the no-horizontal-overflow invariant.
    { name: 'phone', testMatch: RESPONSIVE_MATCH, use: { ...devices['Pixel 7'], viewport: { width: 360, height: 800 } } },
    // iPad-Mini-class (~768 < 1024 → on the MOBILE side of the crossover, not between). Snapshot
    // breadth. Chromium engine (not iPad Mini's default WebKit — CI installs only Chromium) at a
    // 768-wide touch viewport; the layout crossover, not the rendering engine, is what this asserts.
    { name: 'tablet', testMatch: RESPONSIVE_MATCH, use: { ...devices['Desktop Chrome'], viewport: { width: 768, height: 1024 }, hasTouch: true, isMobile: true } },
    // The plan-4 WASM cross-engine soak (webkit + firefox), scoped to the wasm-design flow. Present
    // only under YAOG_WASM_SOAK=1 (see wasmSoakProjects above) so CI's Chromium-only install is safe.
    ...wasmSoakProjects,
  ],
})
