import { test, expect } from '@playwright/test'
import {
  seedAndGotoController,
  loginAsOperator,
  logoutViaUserMenu,
  keystoneOffTarget,
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

test('logout returns to login, drops the session, and leaves no secrets in storage', async ({
  page,
  context,
}) => {
  const target = keystoneOffTarget()
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  await logoutViaUserMenu(page)

  // Custody: after logout, localStorage carries no session secrets and no fleet sentinels, and
  // controller-storage holds only the persist allowlist (clearServerCanvasAtGate + partialize).
  assertNoFleetSecrets(await readPersisted(page))

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
