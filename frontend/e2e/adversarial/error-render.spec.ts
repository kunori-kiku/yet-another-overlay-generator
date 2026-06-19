import { test, expect, type Page } from '@playwright/test'
import { readHarness } from '../fixtures/harness'
import {
  seedAndGotoController,
  loginAsOperator,
  keystoneOffTarget,
  prepareUniqueDesign,
  selectNodeAndRename,
} from '../fixtures/panel'
import { OPERATOR_USER, OPERATOR_PASS } from '../fixtures/config'
import { installFaults, stripAuthAndCsrf } from './faults'
import { expectedText } from './i18n'

// error-render.spec.ts (plan-16 / 3.4, Phase 8) — error-state rendering. Every expected string is
// read from the i18n catalog (expectedText), never hard-coded, so a copy-edit can't silently rot
// these. Covers: the login 429 lockout message localized EN + ZH (S9 repro via the panel — the
// rate-limit DECISION is plan-8/1.8's; this asserts only that the panel RENDERS a 429), the CSRF
// positive contract (S10 — a state-changing cookie-auth request with no CSRF is rejected 403; the
// DECISION is plan-8/1.8's), and the ErrorBoundary recoverable fallback (no white screen).

// fillLoginAndSubmit fills the login form and submits WITHOUT waiting for success (used when the
// submit is expected to surface an error rather than log in).
async function fillLoginAndSubmit(page: Page): Promise<void> {
  await page.locator('#login-username').fill(OPERATOR_USER)
  await page.locator('#login-password').fill(OPERATOR_PASS)
  await page.locator('form button[type="submit"]').click()
}

for (const lang of ['en', 'zh'] as const) {
  test(`login 429 lockout renders the localized message (${lang})`, async ({ page, context }) => {
    const h = readHarness()
    const target = keystoneOffTarget(h)
    await seedAndGotoController(page, context, target)

    if (lang === 'zh') {
      await page.getByRole('button', { name: '中文' }).click()
    }

    // Inject the real coded 429 the limiter returns (apierr.CodeAuthRateLimited) on the login POST,
    // so the panel's tError path localizes it — without touching the shared in-process limiter
    // (10 real failures would lock out the operator/IP and contaminate every other spec).
    await installFaults(page, [
      {
        route: 'login',
        method: 'POST',
        status: 429,
        body: JSON.stringify({ error: { code: 'auth_rate_limited', message: 'Too many login attempts; try again later.' } }),
      },
    ])

    await fillLoginAndSubmit(page)

    await expect(page.getByRole('alert')).toContainText(expectedText(lang, 'error.auth_rate_limited'), {
      timeout: 15_000,
    })
    // Still on the login form (the lockout did not log us in).
    await expect(page.locator('#login-username')).toBeVisible()
  })
}

test('CSRF positive contract: a cookie-auth state-changing request with no CSRF is rejected (403)', async (
  { page, context },
  testInfo,
) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept())
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)
  const built = await prepareUniqueDesign(page, context, h, target, testInfo)

  // Dirty the canvas so Save issues a write, then drop BOTH the bearer and the CSRF token: the write
  // now travels the cookie path with no CSRF, which the backend must reject.
  await selectNodeAndRename(page, target.panel, built.router, 'csrf-probe')
  await stripAuthAndCsrf(page)

  const writeP = page.waitForResponse(
    (r) => r.url().includes('/operator/update-topology') && r.request().method() === 'POST',
    { timeout: 15_000 },
  )
  await page.locator('button.bg-green-600').click()
  expect((await writeP).status(), 'a CSRF-less cookie-auth mutation must be rejected (403)').toBe(403)
})

test('ErrorBoundary shows a recoverable fallback (no white screen) on a render throw', async ({ page, context }) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  // Arm the test-only render-throw probe BEFORE the app mounts (the probe is compiled in only under
  // the VITE_E2E build flag — see App.tsx / E2ERenderThrowProbe — so it ships in no production bundle).
  await page.addInitScript(() => {
    window.__E2E_RENDER_THROW__ = true
  })
  await seedAndGotoController(page, context, target)

  // The boundary's recoverable fallback renders instead of a blank screen: a role="alert" region
  // with the localized title and a Reload affordance.
  const alert = page.getByRole('alert')
  await expect(alert).toBeVisible({ timeout: 15_000 })
  await expect(alert).toContainText(expectedText('en', 'errorBoundary.title'))
  await expect(page.getByRole('button', { name: expectedText('en', 'errorBoundary.reload') })).toBeVisible()
})

declare global {
  interface Window {
    __E2E_RENDER_THROW__?: boolean
  }
}
