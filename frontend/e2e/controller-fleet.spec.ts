import { test, expect } from '@playwright/test'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import os from 'node:os'
import path from 'node:path'
import { readHarness, httpURL } from './fixtures/harness'
import { seedControllerMode } from './fixtures/seedStore'
import { OPERATOR_USER, OPERATOR_PASS, ENROLL_NODE } from './fixtures/config'

// Canary 2 — the controller cross-stack wire end to end: operator login (cookie + CSRF) +
// the agent enroll/report wire (the REAL internal/agent client via cmd/e2eagent) + the
// server-truth panel refresh. A node checks in and the Fleet page shows it. Depth (deploy /
// rekey / revoke) is plan-14's job; this keeps assertions minimal.

const execFileP = promisify(execFile)

test('controller boot: operator login + agent check-in makes the node appear in Fleet', async ({
  page,
  context,
}) => {
  const h = readHarness()
  const panel = httpURL(h.controller.panel)

  // Point the panel at the controller boot (same origin as the panel it serves).
  await seedControllerMode(context, {
    baseURL: panel,
    agentBaseURL: httpURL(h.controller.agent),
  })

  // (1) The controller-mode panel gates on login. Sign in with the seeded operator account.
  await page.goto(`${panel}/`)
  await page.locator('#login-username').fill(OPERATOR_USER)
  await page.locator('#login-password').fill(OPERATOR_PASS)
  await page.locator('form button[type="submit"]').click()
  // Login succeeded → the Shell replaces the LoginPage, so the username field detaches.
  await expect(page.locator('#login-username')).toBeHidden({ timeout: 15_000 })

  // (2) A node enrolls + checks in via the REAL agent client (cmd/e2eagent --mock: enroll +
  // report, the fast deterministic check-in). The single-use enrollment token came from the
  // controller boot's READY line.
  const { stdout } = await execFileP(h.agentBin, [
    '--controller', httpURL(h.controller.agent),
    '--node-id', ENROLL_NODE,
    '--token', h.controller.enrollToken,
    '--mock',
    '--key', path.join(os.tmpdir(), `yaog-e2e-${ENROLL_NODE}-${process.pid}.key`),
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
