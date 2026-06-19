import { test, expect } from '@playwright/test'
import { readHarness } from '../fixtures/harness'
import {
  seedAndGotoController,
  loginAsOperator,
  keystoneOffTarget,
  prepareUniqueDesign,
} from '../fixtures/panel'
import { isDesktopProject, expectNoHorizontalPageOverflow } from './responsive'

// design-route.spec.ts (plan-17 / 3.5, blocker 5) — the design route's lg-boundary pair. Subject 2
// shipped the "DesignAside FULLY HIDDEN below lg" branch (DesignPage early-returns the CanvasGate +
// read-only canvas below lg; the docked edit chrome — CanvasToolbar lists aside `w-72`, DesignAside
// `w-80` — is not mounted at all), so this asserts the plan's documented ALTERNATE branch (gate-only),
// not a bottom-sheet. At >= lg: no gate, editable canvas, and selecting a node docks the DesignAside.

test('design route: full edit chrome at >= lg, read-only gate below', async ({ page, context }, testInfo) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept())
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)
  await prepareUniqueDesign(page, context, h, target, testInfo) // imports a router+peer design
  await page.goto(`${target.panel}/design`)

  const gateTitle = page.getByText('Designing needs a larger screen') // canvasGate.title
  const canvas = page.locator('.react-flow')

  if (isDesktopProject(testInfo)) {
    // >= lg: no gate; the editable canvas mounts and a node selection docks the DesignAside (w-80).
    await expect(gateTitle).toBeHidden()
    await expect(canvas).toBeVisible({ timeout: 15_000 })
    const node = page.locator('.react-flow__node').first()
    await expect(node).toBeVisible()
    await node.click()
    await expect(page.locator('aside.w-80')).toBeVisible({ timeout: 10_000 })
  } else {
    // < lg: the CanvasGate is shown and the docked edit columns are NOT mounted. Dismissing it
    // ("View read-only") reveals the read-only canvas with no horizontal page overflow.
    await expect(gateTitle).toBeVisible({ timeout: 15_000 })
    await expect(page.locator('aside.w-80')).toBeHidden()
    await page.getByRole('button', { name: 'View read-only' }).click()
    await expect(canvas).toBeVisible({ timeout: 10_000 })
    await expect(page.locator('aside.w-80')).toBeHidden()
    await expectNoHorizontalPageOverflow(page)
  }
})
