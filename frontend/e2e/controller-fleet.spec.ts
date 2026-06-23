import { test, expect } from '@playwright/test'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import fs from 'node:fs'
import path from 'node:path'
import { readHarness, httpURL, e2eDir } from './fixtures/harness'
import { seedControllerMode, seedCanvasTopology } from './fixtures/seedStore'
import { OPERATOR_USER, OPERATOR_PASS, ENROLL_NODE } from './fixtures/config'

// Canary 2 — the controller cross-stack wire end to end: operator login (cookie + CSRF) +
// the agent enroll/report wire (the REAL internal/agent client via cmd/e2eagent) + the
// server-truth panel refresh. A node checks in and the Fleet page shows it. Depth (deploy /
// rekey / revoke) is plan-14's job; this keeps assertions minimal.
//
// It also pins the NEGATIVE half of DoD #5's two-boot split at the HTTP layer: the controller
// boot GATES /api/compile (401 without auth), complementing airgap-design.spec.ts's positive
// 200 half. (The authoritative server-level assertion is the required Go gate test
// internal/api/airgap_auth_gate_test.go; this makes the split E2E-observable in both directions.)

const execFileP = promisify(execFile)

const seedTopology = JSON.parse(
  fs.readFileSync(path.join(e2eDir, 'fixtures', 'seed-topology.json'), 'utf8'),
)

test('controller boot: operator login + agent check-in makes the node appear in Fleet', async (
  { page, context },
  testInfo,
) => {
  const h = readHarness()
  const panel = httpURL(h.controller.panel)

  // Point the panel at the controller boot (same origin as the panel it serves).
  await seedControllerMode(context, {
    baseURL: panel,
    agentBaseURL: httpURL(h.controller.agent),
  })

  // (1) The controller-mode panel gates on login. Sign in with the seeded operator account.
  await page.goto(`${panel}/`)

  // Two-boot split, negative half: on the controller boot, /api/compile is operator-gated.
  // Before login there is no session cookie, so an unauthenticated POST is rejected (401) —
  // the inverse of the air-gap boot's open 200 (airgap-design.spec.ts).
  const gated = await page.request.post(`${panel}/api/compile`, { data: seedTopology })
  expect(gated.status()).toBe(401)

  await page.locator('#login-username').fill(OPERATOR_USER)
  await page.locator('#login-password').fill(OPERATOR_PASS)
  await page.locator('form button[type="submit"]').click()
  // Login succeeded → the Shell replaces the LoginPage, so the username field detaches.
  await expect(page.locator('#login-username')).toBeHidden({ timeout: 15_000 })

  // (2) A node enrolls + checks in via the REAL agent client (cmd/e2eagent --mock: enroll +
  // report, the fast deterministic check-in). The single-use enrollment token came from the
  // controller boot's READY line.
  // Write the throwaway WG key into Playwright's per-test output dir (under test-results/,
  // gitignored + wiped at the start of every run) so it never accumulates in /tmp.
  const { stdout } = await execFileP(h.agentBin, [
    '--controller', httpURL(h.controller.agent),
    '--node-id', ENROLL_NODE,
    '--token', h.controller.enrollToken,
    '--mock',
    '--key', testInfo.outputPath('agent.key'),
  ])
  expect(stdout).toContain('E2E_AGENT')

  // (3) The Fleet page shows the enrolled node. Navigating reloads the panel; the Shell's
  // checkSession() restores the cookie session (refresh-surviving login), then FleetPage's
  // refresh-on-auth pulls the live registry → the node-id link appears. The desktop table
  // and the mobile cards both render a link; .first() targets the visible (≥lg) one.
  await page.goto(`${panel}/fleet`)
  await expect(page.getByRole('link', { name: ENROLL_NODE }).first()).toBeVisible({
    timeout: 15_000,
  })
})

// Controller-mode Validate is browser-local verify: the panel runs the in-browser TS validator
// and NEVER calls /api/validate (the shipped controller 404s that air-gap route; keeping verify
// off the wire minimizes the controller's attack surface — no anonymous server-side validation
// endpoint to reach). This guard is CLIENT-SIDE on purpose: the air-gap e2e controller boot DOES
// register /api/validate and answers 200 when authed, so only asserting that the panel JS never
// issues the request captures the shipped-controller behavior (the authoritative server-side 404
// lives in the !airgap Go test internal/api/airgap_routes_removed_test.go).
test('controller-mode Validate runs the in-browser validator and never calls /api/validate', async ({
  page,
  context,
}) => {
  const h = readHarness()
  const panel = httpURL(h.controller.panel)

  await seedControllerMode(context, {
    baseURL: panel,
    agentBaseURL: httpURL(h.controller.agent),
  })
  // A non-empty design so the BottomBar Validate button is enabled (it disables at 0 nodes).
  await seedCanvasTopology(context, seedTopology)

  // Fail the test if the panel ever calls /api/validate in controller mode.
  let validateCalls = 0
  await page.route('**/api/validate', async (route) => {
    validateCalls += 1
    await route.abort()
  })

  // Log in (controller-mode Shell gates on it), then open the design canvas.
  await page.goto(`${panel}/`)
  await page.locator('#login-username').fill(OPERATOR_USER)
  await page.locator('#login-password').fill(OPERATOR_PASS)
  await page.locator('form button[type="submit"]').click()
  await expect(page.locator('#login-username')).toBeHidden({ timeout: 15_000 })

  await page.goto(`${panel}/design`)
  const validateBtn = page.getByRole('button', { name: /Validate Topology/ })
  await expect(validateBtn).toBeEnabled({ timeout: 15_000 })
  await validateBtn.click()

  // The in-browser validator populated a result (the seed topology is valid) …
  await expect(page.getByText('Topology validation passed')).toBeVisible({ timeout: 15_000 })
  // … and the panel never touched the air-gap validate route.
  expect(validateCalls).toBe(0)
})
