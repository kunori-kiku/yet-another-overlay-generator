import { test, expect } from '@playwright/test'

import {
  seedAndGotoController,
  loginAsOperator,
  readCsrf,
  keystoneOffTarget,
} from '../fixtures/panel'

// @security (plan-21 / 4.2 Task 4) — CSRF double-submit boundary (S10). A logged-in operator's
// session cookie is attached ambiently to a cross-site form POST, so a state-changing request on the
// cookie path MUST also carry a matching X-CSRF-Token header (internal/api/auth_controller.go:162 →
// cookie_session.go csrfValid). This proves the gate at the browser level: cookie-authed POST without
// the header is refused (403); with the matching double-submit header it succeeds (200).
test('@security cookie-authed state-changing POST requires the CSRF header', async ({
  page,
  context,
}) => {
  const target = keystoneOffTarget()
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  const url = `${target.panel}/api/v1/operator/enrollment-token`

  // The session cookie is ambient on page.request, but with NO X-CSRF-Token header the double-submit
  // check fails — exactly the cross-site-forgery shape the gate exists to refuse.
  const noCsrf = await page.request.post(url, {
    data: { node_id: 'node-1', ttl_seconds: 3600 },
    failOnStatusCode: false,
  })
  expect(noCsrf.status(), 'cookie POST without the CSRF header must be refused (403)').toBe(403)

  // Positive control: WITH the matching double-submit header the same request succeeds.
  const csrf = await readCsrf(context, target.panel)
  expect(csrf, 'a logged-in operator has a yaog_csrf cookie').toBeTruthy()
  const withCsrf = await page.request.post(url, {
    headers: { 'X-CSRF-Token': csrf },
    data: { node_id: 'node-1', ttl_seconds: 3600 },
    failOnStatusCode: false,
  })
  expect(withCsrf.status(), 'cookie POST with the matching CSRF header succeeds (200)').toBe(200)
})
