import { test, expect, type Page, type BrowserContext, type TestInfo } from '@playwright/test'
import { readHarness } from '../fixtures/harness'
import {
  seedAndGotoController,
  loginAsOperator,
  keystoneOffTarget,
  prepareUniqueDesign,
} from '../fixtures/panel'
import { installFaults } from './faults'

// errorBanner locates DeployBar's error <p> (always prefixed "⚠️ "). The exact message varies by
// fault (a torn connection renders "Failed to fetch"; a coded 5xx renders the backend message), so
// the coherence assertion is the banner's PRESENCE, not its text.
const errorBanner = (page: Page) => page.locator('p').filter({ hasText: '⚠️' })

// deploy-faults.spec.ts (plan-16 / 3.4, Phase 6) — the deploy() step × fault matrix on the 3.1
// keystone-OFF rig. Each test enrolls a fresh design, injects ONE call-count-keyed fault into the
// deploy() chain (update-topology → stage → getTrustlist → promote → reconcile), and asserts the
// panel lands in a COHERENT state: the right error surfaced (or success), the Deploy button
// re-enabled (loading:false), NO half-promoted fleet (promote call-count gated correctly), and the
// getTrustlist 404→keystone-OFF→promote-proceeds POSITIVE contract. The keystone-ON signing legs
// (trustlist-signature faults) need the virtual authenticator and are owned by deploy-keystone /
// 3.3; this file covers the keystone-OFF path that is deterministically reachable here.

const deployButton = { role: 'button' as const, name: '🚀 Deploy' }

// seedEnrolledOnDeploy logs in, enrolls a fresh router+peer design, and lands on /deploy with the
// keystone-OFF "Not enrolled" precondition shown — ready for a fault to be installed before Deploy.
async function seedEnrolledOnDeploy(page: Page, context: BrowserContext, testInfo: TestInfo) {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept())
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)
  const built = await prepareUniqueDesign(page, context, h, target, testInfo)
  await page.goto(`${target.panel}/deploy`)
  await expect(page.getByText('Not enrolled')).toBeVisible({ timeout: 15_000 })
  return { target, built }
}

test('fault at update-topology (torn connection): error surfaced, Deploy re-enabled, stage/promote never called', async (
  { page, context },
  testInfo,
) => {
  await seedEnrolledOnDeploy(page, context, testInfo)
  const faults = await installFaults(page, [{ route: 'update-topology', method: 'POST', abort: true }])

  await page.getByRole(deployButton.role, { name: deployButton.name }).click()

  // The abort throws inside deploy() before stage; the catch sets the generic localized error and
  // clears loading. No bundle was staged or promoted.
  await expect(errorBanner(page)).toBeVisible({ timeout: 15_000 })
  await expect(page.getByText('Last deploy')).toBeHidden()
  await expect(page.getByRole(deployButton.role, { name: deployButton.name })).toBeEnabled()
  expect(faults.count('stage', 'POST'), 'stage must not run after update-topology failed').toBe(0)
  expect(faults.count('promote', 'POST'), 'promote must not run after update-topology failed').toBe(0)
})

test('fault at getTrustlist (500, not 404): deploy aborts BEFORE promote, no half-promoted fleet', async (
  { page, context },
  testInfo,
) => {
  await seedEnrolledOnDeploy(page, context, testInfo)
  // 500 (not 404): getTrustlist throws → deploy aborts. A 404 would mean keystone-OFF and promote
  // would proceed (the positive case below).
  const faults = await installFaults(page, [{ route: 'trustlist', method: 'GET', status: 500 }])

  await page.getByRole(deployButton.role, { name: deployButton.name }).click()

  await expect(errorBanner(page)).toBeVisible({ timeout: 15_000 })
  await expect(page.getByText('Last deploy')).toBeHidden()
  await expect(page.getByRole(deployButton.role, { name: deployButton.name })).toBeEnabled()
  expect(faults.count('promote', 'POST'), 'a trustlist read error must abort BEFORE promote').toBe(0)
})

test('POSITIVE: getTrustlist 404 on keystone-OFF → promote proceeds and deploy completes', async (
  { page, context },
  testInfo,
) => {
  await seedEnrolledOnDeploy(page, context, testInfo)
  // No fault rules — the real keystone-OFF controller returns 404 on /trustlist, which getTrustlist
  // maps to null → promote proceeds. installFaults([]) is used purely to count the promote call.
  const faults = await installFaults(page, [])

  const promoteP = page.waitForResponse(
    (r) => r.url().includes('/operator/promote') && r.request().method() === 'POST',
    { timeout: 20_000 },
  )
  await page.getByRole(deployButton.role, { name: deployButton.name }).click()
  await promoteP
  await expect(page.getByText('Last deploy')).toBeVisible({ timeout: 20_000 })
  expect(faults.count('promote', 'POST'), 'keystone-OFF 404 must let promote proceed').toBeGreaterThanOrEqual(1)
})

test('fault at promote (500): error surfaced, deploy does not report success', async (
  { page, context },
  testInfo,
) => {
  await seedEnrolledOnDeploy(page, context, testInfo)
  const faults = await installFaults(page, [{ route: 'promote', method: 'POST', status: 500 }])

  await page.getByRole(deployButton.role, { name: deployButton.name }).click()

  await expect(errorBanner(page)).toBeVisible({ timeout: 15_000 })
  await expect(page.getByText('Last deploy')).toBeHidden()
  await expect(page.getByRole(deployButton.role, { name: deployButton.name })).toBeEnabled()
  expect(faults.count('promote', 'POST'), 'promote was attempted').toBeGreaterThanOrEqual(1)
})

test('fault at post-deploy reconcile (topology GET after promote): deploy STILL reports success', async (
  { page, context },
  testInfo,
) => {
  await seedEnrolledOnDeploy(page, context, testInfo)
  // The reconcile re-GET of /topology runs AFTER promote inside a best-effort try/catch. `after:
  // 'promote'` faults ONLY that read (never the pre-promote shrink-guard read), so we prove the
  // reconcile failure does not fail an otherwise-successful deploy.
  await installFaults(page, [{ route: 'topology', method: 'GET', after: 'promote', status: 500 }])

  await page.getByRole(deployButton.role, { name: deployButton.name }).click()

  await expect(page.getByText('Last deploy')).toBeVisible({ timeout: 20_000 })
  // No error banner: the reconcile is best-effort, so its failure is swallowed.
  await expect(errorBanner(page)).toBeHidden()
})

// NOTE on the keystone-ON trustlist-signature step: a fault there drives the SAME deploy() catch as
// the getTrustlist-500 leg above (abort BEFORE promote, coherent error, Deploy re-enabled) — `signing`
// is already cleared by the inner finally before the signature POST, so there is no signing-flag-
// specific contract beyond that shared catch. A dedicated keystone-ON signature-FAULT spec is
// deliberately NOT added here: it would require a THIRD operator-signing-credential enrollment on the
// SHARED single-credential controllerOn boot, stranding deploy-keystone.spec (which owns the
// signature-ACCEPTED happy path and assumes it is the first/only enroller). See docs/spec/rc1/3.4-findings.md.
