import { test, expect } from '@playwright/test'
import { seedAndGotoController, loginAsOperator, keystoneOffTarget } from './fixtures/panel'

// Login matrix — password legs (plan-14 Phase 1.1/1.2). The TOTP + passkey legs (1.3–1.6)
// live in login-webauthn.spec.ts (they need the CDP virtual authenticator + the keystone-ON
// boot). All run against the keystone-OFF controller boot's seeded operator account.

test('password login happy-path opens the panel', async ({ page, context }) => {
  await seedAndGotoController(page, context, keystoneOffTarget())
  // loginAsOperator asserts the Shell replaced the LoginPage (the success branch).
  await loginAsOperator(page)
  // The account menu (post-login chrome) is present — a positive signal the panel rendered.
  await expect(page.getByRole('button', { name: 'Account' })).toBeVisible()
})

test('password login with a wrong password shows the error banner and keeps the form', async ({
  page,
  context,
}) => {
  await seedAndGotoController(page, context, keystoneOffTarget())
  await page.locator('#login-username').fill('e2e-operator')
  await page.locator('#login-password').fill('definitely-the-wrong-password')
  await page.locator('form button[type="submit"]').click()
  // The 401 surfaces in the role="alert" error banner (LoginPage.tsx) and the form stays.
  await expect(page.getByRole('alert')).toBeVisible({ timeout: 15_000 })
  await expect(page.locator('#login-username')).toBeVisible()
})
