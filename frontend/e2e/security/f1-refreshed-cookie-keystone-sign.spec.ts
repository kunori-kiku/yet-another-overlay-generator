import { test, expect } from '@playwright/test'

import { seedAndGotoController, loginAsOperator, keystoneOffTarget } from '../fixtures/panel'

// @security (plan-21 / 4.2 Task 4) — the F1 regression probe. The beta.8 F1 bug: getTrustlist used a
// raw fetch WITHOUT credentials:'include', so after a page reload (the in-memory sessionToken is gone,
// only the httpOnly cookie remains) the keystone-sign Deploy path's trustlist fetch 401'd and the
// operator could not sign. The fix routes getTrustlist through the shared request() helper
// (controllerClient.ts → request(cfg,'trustlist'), credentials:'include' + CSRF). This replays the exact
// trigger: log in, RELOAD (drop the in-memory token), then hit the trustlist route via the ambient
// cookie and assert it authenticates (NOT 401).
//
// On the keystone-OFF boot no operator credential is pinned, so the route returns 404 ("nothing staged
// to sign", which getTrustlist maps to null) — both 200 and 404 prove the cookie authenticated across
// the refresh. A 401 is the F1 regression.
test('@security trustlist authenticates via the cookie after a page reload (F1)', async ({
  page,
  context,
}) => {
  const target = keystoneOffTarget()
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  // Reload: the in-memory sessionToken is dropped; only the httpOnly yaog_session cookie survives.
  await page.reload()
  await expect(page.getByRole('button', { name: 'Account' })).toBeVisible({ timeout: 15_000 })

  // The exact route getTrustlist hits, via the ambient cookie — must authenticate post-refresh.
  const resp = await page.request.get(`${target.panel}/api/v1/operator/trustlist`, {
    failOnStatusCode: false,
  })
  expect(
    resp.status(),
    'trustlist must authenticate via the cookie after a refresh (200 or 404, never 401 = F1)',
  ).not.toBe(401)
})
