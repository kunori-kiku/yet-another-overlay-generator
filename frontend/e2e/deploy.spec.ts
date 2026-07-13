import { test, expect } from '@playwright/test'
import { readHarness } from './fixtures/harness'
import {
  seedAndGotoController,
  loginAsOperator,
  keystoneOffTarget,
  prepareUniqueDesign,
} from './fixtures/panel'
import { readPersisted, assertNoFleetSecrets } from './fixtures/leakOracle'

// Deploy journey (plan-14 Phase 3) — keystone-OFF tenant. The keystone-ON F1 signature-acceptance
// leg (3.3) lives in deploy-keystone.spec.ts (it needs the virtual authenticator). Here: the
// design→enroll→stage→promote flow with no operator credential pinned, the post-deploy AND
// post-refresh custody checks (3.4 / DoD #5), and the F1 cookie-only getTrustlist regression.

test('keystone-OFF deploy: stage→promote, then post-deploy + post-refresh custody flush', async (
  { page, context },
  testInfo,
) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept()) // accept the controller-mode import confirm

  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  const { router } = await prepareUniqueDesign(page, context, h, target, testInfo)

  await page.goto(`${target.panel}/deploy`)
  // keystone-OFF precondition (load-bearing): no operator signing key is pinned, so the deploy
  // takes the no-trustlist branch (stage → promote, no signing). The DeployBar shows it.
  await expect(page.getByText('Not enrolled')).toBeVisible({ timeout: 15_000 })

  await page.getByRole('button', { name: '🚀 Deploy' }).click()
  await page.getByTestId('deploy-preview-confirm').click() // plan-6 preview dialog → runs the deploy
  await expect(page.getByText('Last deploy')).toBeVisible({ timeout: 20_000 })
  await expect(page.getByText(router, { exact: false })).toBeVisible()

  // (3.4 / DoD #5 post-deploy) the server-held design is blanked from localStorage — and the
  // fleet sentinels in this just-deployed design (router.example.com / 198.51.100.1) are now
  // LIVE, so the value-grep is meaningful, not vacuous.
  assertNoFleetSecrets(await readPersisted(page), { expectServerHeldBlank: true })

  // (DoD #5 post-refresh) a reload BOOTS the panel from the persisted blob and re-persists via the
  // partialize middleware — a distinct moment from the in-memory→persist write above. Custody must
  // still hold (a regression that blanks in memory but not on disk would surface only here).
  await page.reload()
  await expect(page.getByRole('button', { name: 'Account' })).toBeVisible({ timeout: 15_000 })
  assertNoFleetSecrets(await readPersisted(page), { expectServerHeldBlank: true })
})

test('keystone-OFF F1: getTrustlist on a cookie-only session is 404 (not 401) and deploy completes', async (
  { page, context },
  testInfo,
) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept())

  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)
  await prepareUniqueDesign(page, context, h, target, testInfo)

  // Drop to a COOKIE-ONLY session (the F1 condition): reload clears the in-memory bearer; the
  // Shell's checkSession() restores loggedIn from the cookie, and hydrateFromServer re-loads the
  // imported design (await the GET /topology so the canvas is ready to deploy).
  const hydrateP = page.waitForResponse(
    (r) =>
      r.url().includes('/operator/topology') &&
      r.request().method() === 'GET' &&
      r.status() === 200,
    { timeout: 15_000 },
  )
  await page.reload()
  await hydrateP

  await page.goto(`${target.panel}/deploy`)
  await expect(page.getByText('Not enrolled')).toBeVisible({ timeout: 15_000 })

  const trustlistP = page.waitForResponse(
    (r) =>
      r.url().includes('/operator/trustlist') &&
      !r.url().includes('trustlist-signature') &&
      r.request().method() === 'GET',
    { timeout: 30_000 },
  )
  await page.getByRole('button', { name: '🚀 Deploy' }).click()
  await page.getByTestId('deploy-preview-confirm').click() // plan-6 preview dialog → runs the deploy

  // F1: getTrustlist goes through request() (credentials:include) on the cookie-only session. On
  // keystone-OFF it 404s (no operator credential) → deploy promotes. The PRE-FIX raw fetch dropped
  // credentials, so a cookie-only operator got 401 here and the deploy threw. Assert 404, NOT 401.
  expect((await trustlistP).status(), 'getTrustlist cookie-only must be 404 not 401 (F1)').toBe(404)
  await expect(page.getByText('Last deploy')).toBeVisible({ timeout: 20_000 })
})

test('controller compile-preview renders the server-side compile result (3.1)', async (
  { page, context },
  testInfo,
) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept())

  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)
  // prepareUniqueDesign leaves the canvas on /design holding the imported design.
  await prepareUniqueDesign(page, context, h, target, testInfo)

  // The "🔨 Compile" toolbar button in controller mode runs the SERVER-authoritative
  // compilePreview (POST /compile-preview), not a local compile. Assert the response carries the
  // rendered per-node configs (the "preview renders configs" contract) — observed on the wire so
  // it does not depend on a full-page nav that would discard the in-memory compileResult.
  const previewP = page.waitForResponse(
    (r) => r.url().includes('/operator/compile-preview') && r.request().method() === 'POST',
    { timeout: 20_000 },
  )
  await page.getByRole('button', { name: '🔨 Compile' }).click()
  const previewResp = await previewP
  expect(previewResp.status(), 'compile-preview should be 200').toBe(200)
  const body = await previewResp.text()
  expect(body.includes('[Interface]'), 'compile-preview returns rendered WireGuard configs').toBe(true)
})

// Phase 3.5 (shrink-guard ≥50% typed-confirmation) is NOT covered here: the guard compares the
// canvas against the server's CURRENT design (controllerStore.ts deploy), but a controller-mode
// import auto-pushes (update-topology), so the server is never left larger than the canvas at
// deploy time. The only deterministic trigger is editing the live canvas to delete nodes (brittle
// React-Flow interaction), which is poorly suited to an E2E. Residual: covered by the manual
// browser smoke + a candidate unit test of the guard predicate (recorded in the outline).
