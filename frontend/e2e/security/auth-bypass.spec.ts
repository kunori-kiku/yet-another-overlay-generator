import { test, expect } from '@playwright/test'

import { keystoneOffTarget } from '../fixtures/panel'

// @security (plan-21 / 4.2 Task 4) — auth-bypass boundary. An operator-only route, hit with NO
// session (no yaog_session cookie, no Authorization: Bearer), must be refused 401 by operatorAuth
// (internal/api/auth_controller.go) — the browser-level complement to the in-process auth tests.
test('@security operator routes without a session are refused (401)', async ({ page }) => {
  const target = keystoneOffTarget()

  // A state-changing operator route with no auth at all.
  const post = await page.request.post(`${target.panel}/api/v1/operator/enrollment-token`, {
    data: { node_id: 'node-1', ttl_seconds: 3600 },
    failOnStatusCode: false,
  })
  expect(post.status(), 'unauthenticated operator POST must be 401').toBe(401)

  // A read operator route is likewise gated.
  const get = await page.request.get(`${target.panel}/api/v1/operator/trustlist`, {
    failOnStatusCode: false,
  })
  expect(get.status(), 'unauthenticated operator GET must be 401').toBe(401)
})
