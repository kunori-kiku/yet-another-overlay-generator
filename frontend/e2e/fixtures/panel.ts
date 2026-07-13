import { expect, type Page, type BrowserContext, type TestInfo } from '@playwright/test'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import fs from 'node:fs'
import { readHarness, httpURL, type HarnessState } from './harness'
import { seedControllerMode } from './seedStore'
import { OPERATOR_USER, OPERATOR_PASS } from './config'
import { uniqueRouterPeer, runId, type BuiltDesign } from './designs'

const execFileP = promisify(execFile)

// panel.ts — shared operator-journey actions on plan-13's harness, reused across the plan-14
// specs (login / session / deploy / export-import / revoke). Specs drive the UI + read
// localStorage only; nothing here imports controllerStore/controllerClient internals.

export interface ControllerTarget {
  panel: string // http://host:port of the operator/panel mux
  agent: string // http://host:port of the agent mux
}

// keystoneOffTarget is plan-13's default controller boot — NO operator credential is ever
// pinned on it, so selectServerOperatorPinned() stays false (the keystone-OFF deploy branch).
export function keystoneOffTarget(h: HarnessState = readHarness()): ControllerTarget {
  return { panel: httpURL(h.controller.panel), agent: httpURL(h.controller.agent) }
}

// seedAndGotoController seeds controller mode pointed at a target boot and navigates to it,
// landing on the LoginPage (the controller-mode gate).
export async function seedAndGotoController(
  page: Page,
  context: BrowserContext,
  target: ControllerTarget,
): Promise<void> {
  await seedControllerMode(context, { baseURL: target.panel, agentBaseURL: target.agent })
  await page.goto(`${target.panel}/`)
}

// loginAsOperator fills + submits the password login form and waits for the Shell to replace
// the LoginPage (the username field detaches on a successful login).
export async function loginAsOperator(
  page: Page,
  user: string = OPERATOR_USER,
  pass: string = OPERATOR_PASS,
): Promise<void> {
  await page.locator('#login-username').fill(user)
  await page.locator('#login-password').fill(pass)
  await page.locator('form button[type="submit"]').click()
  await expect(page.locator('#login-username')).toBeHidden({ timeout: 15_000 })
}

// logoutViaUserMenu opens the top-right account menu and signs out, waiting for the LoginPage
// to return (UserMenu.tsx: "Account" trigger → "Sign out").
export async function logoutViaUserMenu(page: Page): Promise<void> {
  await page.getByRole('button', { name: 'Account' }).click()
  await page.getByRole('button', { name: 'Sign out' }).click()
  await expect(page.locator('#login-username')).toBeVisible({ timeout: 15_000 })
}

// readCsrf reads the double-submit CSRF cookie (yaog_csrf) for the panel origin, so an
// operator-authed page.request POST can present the X-CSRF-Token header alongside the session
// cookie. Returns '' if absent (not logged in).
export async function readCsrf(context: BrowserContext, panelURL: string): Promise<string> {
  const cookies = await context.cookies(panelURL)
  return cookies.find((c) => c.name === 'yaog_csrf')?.value ?? ''
}

// mintEnrollToken mints a single-use enrollment token for nodeId via the operator API
// (POST /enrollment-token, cookie + CSRF). Returns the plaintext token. The operator must be
// logged in first. This is the operator-side of the enroll ceremony — the same effect as the
// EnrollmentFlow UI, but deterministic.
export async function mintEnrollToken(
  page: Page,
  context: BrowserContext,
  panelURL: string,
  nodeId: string,
): Promise<string> {
  const csrf = await readCsrf(context, panelURL)
  const resp = await page.request.post(`${panelURL}/api/v1/operator/enrollment-token`, {
    headers: { 'X-CSRF-Token': csrf },
    data: { node_id: nodeId, ttl_seconds: 3600 },
  })
  expect(resp.status(), 'mint enrollment-token should be 200').toBe(200)
  const body = (await resp.json()) as { token: string }
  expect(body.token, 'enrollment-token response carries a plaintext token').toBeTruthy()
  return body.token
}

// runE2eAgent invokes cmd/e2eagent with arbitrary args and returns its stdout (asserting exit 0).
// The single entry point the lifecycle specs use to drive the agent's checkin/rekey/reprovision
// modes; execFile rejects on a non-zero exit, which surfaces as a failed spec.
export async function runE2eAgent(h: HarnessState, args: string[]): Promise<string> {
  const { stdout } = await execFileP(h.agentBin, args)
  return stdout
}

// enrollNodeViaAgent runs cmd/e2eagent (--mock: enroll + check-in) for nodeId against the
// controller agent port, so the node appears APPROVED in the registry with a WG public key —
// enough for stage to compile it. keyPath should be a per-test temp path (auto-cleaned).
// bearerFile, when given, persists the issued per-node bearer token so a later invocation
// (rekey/reprovision/real-checkin) can reuse it (the enrollment token is single-use).
export async function enrollNodeViaAgent(
  h: HarnessState,
  agentURL: string,
  nodeId: string,
  token: string,
  keyPath: string,
  bearerFile?: string,
): Promise<void> {
  const args = ['--controller', agentURL, '--node-id', nodeId, '--token', token, '--mock', '--key', keyPath]
  if (bearerFile) args.push('--bearer-file', bearerFile)
  const stdout = await runE2eAgent(h, args)
  expect(stdout).toContain('E2E_AGENT')
}

// importDesignViaUI imports a design file through the panel's Import button (controller mode:
// importDesignToServer — strips keys, writes a new server version, re-hydrates the canvas).
// The caller MUST have registered a dialog handler that accepts the import confirm. It
// navigates to /design (where the I/O cluster lives) and sets the hidden file input.
export async function importDesignViaUI(page: Page, panelURL: string, filePath: string): Promise<void> {
  await page.goto(`${panelURL}/design`)
  const [resp] = await Promise.all([
    page.waitForResponse(
      (r) => r.url().includes('/operator/update-topology') && r.request().method() === 'POST',
      { timeout: 15_000 },
    ),
    page.locator('input[type="file"]').setInputFiles(filePath),
  ])
  expect(resp.status(), 'import update-topology should be 200').toBe(200)
}

// prepareUniqueDesign mints tokens + enrolls a fresh router+peer pair, writes the design to a
// per-test file, and imports it (controller mode) — leaving the canvas holding a server-held
// design whose nodes are enrolled, ready to stage+promote. Returns the unique node ids. The
// caller MUST have registered a dialog handler that accepts the import confirm. It does NOT
// deploy (so a caller can interpose a reload — e.g. the F1 cookie-only leg — before Deploy).
export async function prepareUniqueDesign(
  page: Page,
  context: BrowserContext,
  h: HarnessState,
  target: ControllerTarget,
  testInfo: TestInfo,
): Promise<BuiltDesign> {
  const built = uniqueRouterPeer(runId(process.pid, testInfo.workerIndex, Date.now()))
  const designPath = testInfo.outputPath('design.json')
  fs.writeFileSync(designPath, JSON.stringify(built.topo))
  const rTok = await mintEnrollToken(page, context, target.panel, built.router)
  const pTok = await mintEnrollToken(page, context, target.panel, built.peer)
  await enrollNodeViaAgent(h, target.agent, built.router, rTok, testInfo.outputPath('r.key'))
  await enrollNodeViaAgent(h, target.agent, built.peer, pTok, testInfo.outputPath('p.key'))
  await importDesignViaUI(page, target.panel, designPath)
  return built
}

// selectNodeAndRename dirties the controller canvas deterministically: it navigates to /design,
// selects a node by React Flow's stable data-id (opening the DesignAside NodeEditor), and edits the
// node-name input (the aside's first textbox) so the canvas diverges from the synced baseline. This
// is the non-brittle dirty path the Save / save-conflict adversarial specs need (a real React-Flow
// drag/add is the brittle interaction the deploy.spec residual flags). It drives the UI only.
export async function selectNodeAndRename(page: Page, panelURL: string, nodeId: string, newName: string): Promise<void> {
  await page.goto(`${panelURL}/design`)
  const node = page.locator(`.react-flow__node[data-id="${nodeId}"]`)
  await expect(node).toBeVisible({ timeout: 15_000 })
  await node.click()
  const nameInput = page.locator('aside input').first()
  await expect(nameInput).toBeVisible({ timeout: 10_000 })
  await nameInput.fill(newName)
  await nameInput.blur()
}

// runDeploy clicks Deploy on /deploy, confirms the plan-6 pre-deploy preview dialog, and waits for
// the Last-deploy block (a successful stage→(sign)→promote). It does NOT navigate away after.
export async function runDeploy(page: Page, target: ControllerTarget): Promise<void> {
  await page.goto(`${target.panel}/deploy`)
  await page.getByRole('button', { name: '🚀 Deploy' }).click()
  // plan-6: Deploy now opens a preview dialog first — confirm it to run the actual deploy.
  await page.getByTestId('deploy-preview-confirm').click()
  await expect(page.getByText('Last deploy')).toBeVisible({ timeout: 20_000 })
}
