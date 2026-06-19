import { test, expect } from '@playwright/test'
import { readHarness } from '../fixtures/harness'
import {
  seedAndGotoController,
  loginAsOperator,
  keystoneOffTarget,
  prepareUniqueDesign,
} from '../fixtures/panel'
import { isPhoneProject } from './responsive'

// canvas-touch.spec.ts (plan-17 / 3.5, Phase 4) — the required tap()/drag interaction on the
// hasTouch phone project. Below lg the canvas is READ-ONLY behind the CanvasGate; after dismissing
// the gate, the canvas + pan/zoom Controls mount and a drag PANS the viewport, but the gesture must
// NOT mutate the design (read-only) — the node count is unchanged after the pan.

test('phone: read-only canvas pans by drag without mutating the design', async ({ page, context }, testInfo) => {
  test.skip(!isPhoneProject(testInfo), 'phone-only touch interaction (hasTouch)')
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept())
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)
  await prepareUniqueDesign(page, context, h, target, testInfo) // a router+peer design (2 nodes)
  await page.goto(`${target.panel}/design`)

  await expect(page.getByText('Designing needs a larger screen')).toBeVisible({ timeout: 15_000 })
  await page.getByRole('button', { name: 'View read-only' }).click()

  const canvas = page.locator('.react-flow')
  await expect(canvas).toBeVisible({ timeout: 10_000 })
  await expect(page.locator('.react-flow__controls')).toBeVisible()

  const viewport = page.locator('.react-flow__viewport')
  const transformBefore = await viewport.getAttribute('style')
  const nodesBefore = await page.locator('.react-flow__node').count()

  // Pan the read-only canvas with a pointer drag across the pane.
  const pane = page.locator('.react-flow__pane')
  const box = await pane.boundingBox()
  if (!box) throw new Error('canvas pane has no bounding box')
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
  await page.mouse.down()
  await page.mouse.move(box.x + box.width / 2 - 120, box.y + box.height / 2 - 80, { steps: 8 })
  await page.mouse.up()

  // The viewport transform changed (it panned) but the design is untouched (read-only).
  await expect(viewport).not.toHaveAttribute('style', transformBefore ?? '')
  expect(await page.locator('.react-flow__node').count(), 'a read-only pan must not add/remove nodes').toBe(
    nodesBefore,
  )
})
