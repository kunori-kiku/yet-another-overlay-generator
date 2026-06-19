import { test, expect } from '@playwright/test'
import { readHarness } from '../fixtures/harness'
import {
  seedAndGotoController,
  loginAsOperator,
  keystoneOffTarget,
  prepareUniqueDesign,
  selectNodeAndRename,
} from '../fixtures/panel'
import { seedLocalMode } from '../fixtures/seedStore'

// nav.spec.ts (plan-16 / 3.4, Phase 8) — routing-guard + back/forward coherence.
//
//  1. Local-mode deep-link redirect: a controller-only route (/fleet) deep-linked in LOCAL mode is
//     redirected to the local landing (/design) by RequireControllerMode — reachability matches nav
//     visibility, so no stale/empty controller UI renders.
//  2. Back/forward edit-preservation: an unsaved canvas edit survives a design→deploy→back round
//     trip (the store, not the route, holds the canvas), and the dirty flag stays consistent.

test('local-mode deep-link to a controller route redirects to the local landing', async ({ page, context }) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  await seedLocalMode(context)

  // Deep-link the controller-only Fleet route while in local mode.
  await page.goto(`${target.panel}/fleet`)

  // RequireControllerMode redirects to /design (landingPathForMode('local')); the local design
  // canvas renders and no Fleet UI is shown.
  await expect(page).toHaveURL(/\/design$/, { timeout: 15_000 })
  await expect(page.locator('.react-flow')).toBeVisible({ timeout: 15_000 })
})

test('an unsaved canvas edit survives a design→deploy→back navigation (dirty flag consistent)', async (
  { page, context },
  testInfo,
) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept())
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)
  const built = await prepareUniqueDesign(page, context, h, target, testInfo)

  // Edit the canvas (dirty). The Save button (stable green class) becomes enabled.
  await selectNodeAndRename(page, target.panel, built.router, 'edit-survives-nav')
  const save = page.locator('button.bg-green-600')
  await expect(save).toBeEnabled()

  // Navigate to /deploy via the in-app nav LINK (SPA route change), NOT page.goto — a full reload
  // would re-hydrate the controller canvas from the server and legitimately drop the unsaved edit,
  // which is not what "back/forward preservation" means. Then go back through browser history.
  await page.getByRole('link', { name: 'Deploy', exact: true }).click()
  await expect(page).toHaveURL(/\/deploy$/, { timeout: 15_000 })
  await expect(page.getByRole('button', { name: '🚀 Deploy' })).toBeVisible({ timeout: 15_000 })
  await page.goBack()
  await expect(page).toHaveURL(/\/design$/, { timeout: 15_000 })

  // The edit persisted (the renamed node is still selected with the new name) and the canvas is
  // still dirty — Save remains enabled, not silently reset by the navigation.
  await expect(page.locator(`.react-flow__node[data-id="${built.router}"]`)).toBeVisible({ timeout: 15_000 })
  await expect(page.locator('button.bg-green-600')).toBeEnabled()
})
