import { test, expect } from '@playwright/test'
import { readHarness } from './fixtures/harness'
import {
  seedAndGotoController,
  loginAsOperator,
  logoutViaUserMenu,
  keystoneOffTarget,
  prepareUniqueDesign,
  runDeploy,
} from './fixtures/panel'
import { readPersisted, assertNoFleetSecrets } from './fixtures/leakOracle'

// Session lifecycle (plan-14 Phase 2): refresh-survival, logout revocation + custody flush,
// and break-glass-is-not-a-login. Against the keystone-OFF controller boot.

test('session survives a page refresh (cookie restore)', async ({ page, context }) => {
  const target = keystoneOffTarget()
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  // Reload: the in-memory session is gone, but the Shell's checkSession() restores it from the
  // httpOnly cookie (non-empty csrf gate), so the panel renders without the LoginPage.
  await page.reload()
  await expect(page.getByRole('button', { name: 'Account' })).toBeVisible({ timeout: 15_000 })
  await expect(page.locator('#login-username')).toBeHidden()
})

test('logout flushes the server-held canvas and drops the session (custody)', async (
  { page, context },
  testInfo,
) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept()) // accept the import confirm

  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  // Establish a SERVER-HELD canvas first (otherwise the logout-path flush — clearServerCanvasAtGate
  // → flushWorkspace, gated on canvasFromServer — is never exercised and the custody check is
  // vacuous): import + enroll + deploy so canvasFromServer===true and the fleet sentinels are live.
  await prepareUniqueDesign(page, context, h, target, testInfo)
  await runDeploy(page, target)

  await logoutViaUserMenu(page)

  // Custody: the logout-path flush RAN — clearServerCanvasAtGate saw the server-held canvas
  // (canvasFromServer was true) and ran flushWorkspace, resetting it to the local default
  // (canvasFromServer:false, nodes/edges empty). A regression where logout skips the flush would
  // leave canvasFromServer true → caught here. And no fleet sentinel / session secret persisted.
  const after = await readPersisted(page)
  assertNoFleetSecrets(after)
  const topo = after.topology?.state as { canvasFromServer?: boolean; nodes?: unknown[] } | undefined
  expect(topo?.canvasFromServer, 'logout must flush the server-held canvas (canvasFromServer→false)').toBe(false)
  expect(Array.isArray(topo?.nodes) ? topo!.nodes!.length : -1, 'logout must clear the design').toBe(0)

  // Server-side: GET /session is now unauthenticated (the cookie session was revoked).
  const session = await page.request.get(`${target.panel}/api/v1/operator/session`)
  expect(session.status()).toBe(401)
})

test('break-glass opens the panel without establishing a cookie login', async ({ page, context }) => {
  const target = keystoneOffTarget()
  await seedAndGotoController(page, context, target)

  // Enter a break-glass token via the collapsed recovery form. The Shell gate opens the instant
  // operatorToken is non-empty (it is NOT a login — it mints no session and is never persisted).
  await page.getByRole('button', { name: 'Recovery (break-glass)' }).click()
  await page.locator('#login-breakglass').fill('break-glass-recovery-token')
  await page.getByRole('button', { name: 'Enter with recovery token' }).click()

  // The gate opened (LoginPage gone) ...
  await expect(page.locator('#login-username')).toBeHidden({ timeout: 15_000 })
  // ... but the break-glass token was never persisted (custody): no session secrets in storage.
  assertNoFleetSecrets(await readPersisted(page))
})
