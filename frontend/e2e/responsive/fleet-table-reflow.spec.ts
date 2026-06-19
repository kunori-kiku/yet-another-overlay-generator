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
import { isDesktopProject, expectNoHorizontalPageOverflow } from './responsive'

// fleet-table-reflow.spec.ts (plan-17 / 3.5, blocker 4) — the NodeRegistry 8-column table reflows to
// cards below lg. The desktop table lives in a `hidden lg:block` wrapper; the mobile cards in a
// `lg:hidden` block (both iterate the same descriptor spine). At >= lg the <table> is shown; below lg
// it is hidden, the node still renders (its link is visible in the card), and there is NO horizontal
// PAGE overflow at the narrow edge. Binds to the <table> element + the node's ARIA link (no testid).

test('fleet registry is a table at >= lg and reflows to cards (no overflow) below', async (
  { page, context },
  testInfo,
) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  // Enroll a uniquely-named node so its row/card is unambiguous in the shared registry.
  const node = `reflow-${runId(process.pid, testInfo.workerIndex, Date.now())}`
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)
  const tok = await mintEnrollToken(page, context, target.panel, node)
  await enrollNodeViaAgent(h, target.agent, node, tok, testInfo.outputPath('reflow.key'))

  await page.goto(`${target.panel}/fleet`)
  // The node is present in either presentation — its link is the ARIA anchor.
  await expect(page.getByRole('link', { name: node }).first()).toBeVisible({ timeout: 15_000 })

  const table = page.locator('table')
  if (isDesktopProject(testInfo)) {
    await expect(table).toBeVisible()
  } else {
    // Below lg the desktop table wrapper is hidden; the card presentation carries the node.
    await expect(table).toBeHidden()
    await expectNoHorizontalPageOverflow(page)
  }
})
