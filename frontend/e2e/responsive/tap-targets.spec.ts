import { test, expect, type Locator } from '@playwright/test'
import { readHarness } from '../fixtures/harness'
import {
  seedAndGotoController,
  loginAsOperator,
  keystoneOffTarget,
  mintEnrollToken,
  enrollNodeViaAgent,
} from '../fixtures/panel'
import { runId } from '../fixtures/designs'
import { isPhoneProject } from './responsive'

// tap-targets.spec.ts (plan-17 / 3.5, blocker 7) — at phone width the primary fleet action affordances
// meet the >= 44px tap-target minimum (NodeRegistry Revoke, and Cancel-rekey which shares the same
// mobile-card button sizing). This plan verifies the sizing; the hunt found the NodeRegistry mobile
// buttons were 32px tall and landed the minimal mobile-only `min-h-11` fix the spec now pins.
//
// FILED FINDING (NOT asserted here): the Topbar I/O cluster (Import/Export/Flush) is `px-2.5 py-1
// text-xs` (~24px tall) with NO responsive variant, so it is sub-44px at phone width. A blanket
// height bump would also inflate the DESKTOP toolbar; the right fix is a CONSIDERED mobile treatment
// (bump/relocation) owned by a Subject-2 follow-up (plan-10 line 23 / plan-12). Recorded in
// docs/spec/rc1/3.5-findings.md rather than forced through a desktop-affecting change in this
// verification plan. See that ledger for the disposition.

const MIN_TAP = 44

async function expectTapTarget(loc: Locator, label: string): Promise<void> {
  await expect(loc, `${label} visible`).toBeVisible()
  const box = await loc.boundingBox()
  expect(box, `${label} has a bounding box`).not.toBeNull()
  // 0.5px slack for sub-pixel rounding.
  expect(box!.height, `${label} height >= ${MIN_TAP}px`).toBeGreaterThanOrEqual(MIN_TAP - 0.5)
  expect(box!.width, `${label} width >= ${MIN_TAP}px`).toBeGreaterThanOrEqual(MIN_TAP - 0.5)
}

test('phone: primary action tap targets are >= 44px', async ({ page, context }, testInfo) => {
  test.skip(!isPhoneProject(testInfo), 'phone-only tap-target sizing')
  const h = readHarness()
  const target = keystoneOffTarget(h)
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  // Enroll a node so the NodeRegistry renders a Revoke action.
  const node = `tap-${runId(process.pid, testInfo.workerIndex, Date.now())}`
  const tok = await mintEnrollToken(page, context, target.panel, node)
  await enrollNodeViaAgent(h, target.agent, node, tok, testInfo.outputPath('tap.key'))

  await page.goto(`${target.panel}/fleet`)
  // Revoke renders for every enrolled node; its mobile-card button carries the min-h-11 tap height
  // (Cancel-rekey shares the exact same `btn` sizing in NodeRegistry, so it is covered structurally).
  await expectTapTarget(page.getByRole('button', { name: 'Revoke' }).first(), 'NodeRegistry Revoke')
})
