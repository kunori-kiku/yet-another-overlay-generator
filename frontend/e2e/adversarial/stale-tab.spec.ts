import { test, expect } from '@playwright/test'
import { readHarness } from '../fixtures/harness'
import { seedAndGotoController, loginAsOperator, keystoneOffTarget } from '../fixtures/panel'
import { stripAuthHeader } from './faults'

// stale-tab.spec.ts (plan-16 / 3.4, Phase 7) — the DISTINCT stale-tab angle. The `page.reload()`
// Deploy-keystone-sign F1 regression (cookie SURVIVES a reload) is owned by 3.3's
// keystone-rotation.spec.ts and 3.2's deploy.spec.ts; this file covers two angles those do NOT:
//
//  1. In-memory bearer gone, cookie carries auth. With the Authorization header stripped from every
//     request (simulating a stale tab that lost its in-memory session bearer), the panel's authed
//     reads still succeed via the persisted httpOnly cookie (request()'s credentials:'include').
//  2. Session expired → routes to re-login. With the session cookie cleared (an expired session),
//     the panel returns to the LoginPage instead of throwing an unhandled error.

test('in-memory bearer gone: authed reads still succeed via the httpOnly cookie', async ({ page, context }) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  // Strip the Authorization bearer from every operator request: the cookie alone must authenticate.
  await stripAuthHeader(page)

  // Navigating to /fleet triggers a refresh (GET /nodes). It must succeed (200) on the cookie, and
  // the panel must stay logged in (no bounce to the LoginPage).
  const nodesP = page.waitForResponse(
    (r) => r.url().includes('/operator/nodes') && r.request().method() === 'GET',
    { timeout: 15_000 },
  )
  await page.goto(`${target.panel}/fleet`)
  expect((await nodesP).status(), 'a bearer-less authed read must succeed on the cookie').toBe(200)
  await expect(page.locator('#login-username')).toBeHidden()
  await expect(page.getByRole('button', { name: 'Account' })).toBeVisible({ timeout: 15_000 })
})

test('expired session: a cleared cookie routes the panel back to re-login', async ({ page, context }) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)
  await expect(page.getByRole('button', { name: 'Account' })).toBeVisible({ timeout: 15_000 })

  // Clear all cookies (the session is gone — expired/evicted). Unlike F1 (cookie present, survives),
  // there is now no credential at all: checkSession() on reload must gate back to the LoginPage.
  await context.clearCookies()
  await page.reload()

  await expect(page.locator('#login-username')).toBeVisible({ timeout: 15_000 })
  await expect(page.getByRole('button', { name: 'Account' })).toBeHidden()
})
