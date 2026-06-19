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

  // Settle the mount-time fitView animation (fitViewOptions duration 400ms) BEFORE snapshotting the
  // baseline transform — otherwise the transform would still be animating toward its fit target and
  // would change between reads regardless of the drag, weakening the pan proof. Poll until two
  // consecutive style reads (≥150ms apart) are identical (the animation has come to rest).
  let prevStyle = ''
  await expect
    .poll(
      async () => {
        const cur = (await viewport.getAttribute('style')) ?? ''
        const stable = cur !== '' && cur === prevStyle
        prevStyle = cur
        return stable
      },
      { timeout: 5_000, intervals: [150, 150, 150] },
    )
    .toBe(true)

  const transformBefore = await viewport.getAttribute('style')
  const nodesBefore = await page.locator('.react-flow__node').count()

  // Pan the read-only canvas with a pointer drag. Start in the EMPTY top-left corner of the pane:
  // fitView centers the nodes with 0.2 padding, so the corner is background — dragging it pans,
  // whereas a center drag could land on a node (which, read-only, neither pans nor moves).
  const pane = page.locator('.react-flow__pane')
  const box = await pane.boundingBox()
  if (!box) throw new Error('canvas pane has no bounding box')
  await page.mouse.move(box.x + 15, box.y + 15)
  await page.mouse.down()
  await page.mouse.move(box.x + 135, box.y + 95, { steps: 8 })
  await page.mouse.up()

  // The viewport transform changed (the drag panned it) — and since fitView had already settled, the
  // drag is the only thing that could have moved it — but the design is untouched (read-only).
  await expect(viewport).not.toHaveAttribute('style', transformBefore ?? '')
  expect(await page.locator('.react-flow__node').count(), 'a read-only pan must not add/remove nodes').toBe(
    nodesBefore,
  )
})
