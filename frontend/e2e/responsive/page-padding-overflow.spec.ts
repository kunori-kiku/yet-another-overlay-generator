import { test, expect } from '@playwright/test'
import { readHarness } from '../fixtures/harness'
import {
  seedAndGotoController,
  loginAsOperator,
  keystoneOffTarget,
  mintEnrollToken,
  enrollNodeViaAgent,
} from '../fixtures/panel'
import { runId } from '../fixtures/designs'
import { expectNoHorizontalPageOverflow } from './responsive'

// page-padding-overflow.spec.ts (plan-17 / 3.5, blocker 6 + the no-overflow proof for blocker 2) —
// every operator surface must fit the narrow edge with NO horizontal PAGE scroll. Surfaces that
// reflow their INNER grids at `md` (Audit/Security, ConnectionSettings) are asserted at the
// PAGE level (not "never reflows"). Runs across the device matrix; the assertion is only non-trivial
// at the narrow projects, but holding it at desktop too is a free guard.

test('operator surfaces have no horizontal page overflow', async ({ page, context }, testInfo) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  // Enroll a node so the Fleet registry + a real node-detail route have content to lay out.
  const node = `pad-${runId(process.pid, testInfo.workerIndex, Date.now())}`
  const tok = await mintEnrollToken(page, context, target.panel, node)
  await enrollNodeViaAgent(h, target.agent, node, tok, testInfo.outputPath('pad.key'))

  const surfaces = ['/overview', '/fleet', '/deploy', '/security', '/settings', `/fleet/nodes/${node}`]
  for (const route of surfaces) {
    await page.goto(`${target.panel}${route}`)
    // Wait for the shell chrome to settle (the account menu is on every authed surface).
    await expect(page.getByRole('button', { name: 'Account' })).toBeVisible({ timeout: 15_000 })
    await expectNoHorizontalPageOverflow(page)
  }
})
