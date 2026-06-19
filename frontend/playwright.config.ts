import { defineConfig, devices } from '@playwright/test'

// Playwright config for the YAOG browser E2E layer (plan-13 / milestone 3.1) — the first
// browser end-to-end tests the project has had. It runs the REAL built panel (served by a
// test-mode Go controller, EnableStatic) against a live controller + a real agent fixture.
//
// globalSetup boots TWO cmd/e2eserver processes (--mode controller + --mode airgap) on
// OS-assigned :0 ports and writes their resolved ports + the enrollment token to a handoff
// file the specs read (e2e/fixtures/harness.ts). It is NOT the default webServer.url wait —
// that checks a single fixed port and cannot capture the second boot's port or the enroll
// token. globalTeardown kills both children and removes the temp state dir.
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
  use: {
    // Capture a trace + screenshot only when a test fails, for the CI failure artifact.
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
