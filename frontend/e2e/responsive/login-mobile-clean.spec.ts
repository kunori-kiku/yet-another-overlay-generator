import { test, expect } from '@playwright/test'
import { readHarness } from '../fixtures/harness'
import { seedAndGotoController, keystoneOffTarget } from '../fixtures/panel'
import { isPhoneProject, expectNoHorizontalPageOverflow } from './responsive'

// login-mobile-clean.spec.ts (plan-17 / 3.5, Phase 4 negative control) — the unauthenticated login
// gate must NOT leak the shell chrome. A 2.2 regression where the off-canvas Drawer / Topbar
// hamburger renders in the LoginPage branch (before any session exists) would surface here: at phone
// width there is NO hamburger, the login form is present and centered, and the page does not overflow.

test('login gate stays clean on a phone — no shell chrome leak', async ({ page, context }, testInfo) => {
  test.skip(!isPhoneProject(testInfo), 'phone-only negative control')
  const target = keystoneOffTarget(readHarness())
  await seedAndGotoController(page, context, target) // lands on the LoginPage, not authenticated

  await expect(page.locator('#login-username')).toBeVisible({ timeout: 15_000 })
  // The shell hamburger + nav drawer must NOT exist in the login gate.
  await expect(page.getByRole('button', { name: 'Open navigation' })).toBeHidden()
  await expect(page.getByRole('dialog')).toBeHidden()
  await expectNoHorizontalPageOverflow(page)
})
