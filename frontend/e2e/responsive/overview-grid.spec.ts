import { test, expect } from '@playwright/test'
import { readHarness } from '../fixtures/harness'
import { seedAndGotoController, loginAsOperator, keystoneOffTarget } from '../fixtures/panel'
import { gridTrackCount } from './responsive'

// overview-grid.spec.ts (plan-17 / 3.5, blocker 3) — the Overview stat grids. NOTE: these collapse
// at the `sm` = 640 boundary (OverviewPage.tsx `grid-cols-1 sm:grid-cols-3`), NOT `lg` — so this is
// the one smoke whose pair pivots on `sm`, not `lg`. The branch is therefore on viewport width >= 640
// (so the 768 tablet is on the 3-col side), not on the desktop/mobile project split.

test('overview stat grids are 3-col at >= sm and 1-col below', async ({ page, context }) => {
  const target = keystoneOffTarget(readHarness())
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)
  await page.goto(`${target.panel}/overview`)

  // Wait for the Overview to render its stat grids (they render even with an empty fleet).
  const grid = page.locator('main div.grid').first()
  await expect(grid).toBeVisible({ timeout: 15_000 })

  const wide = (page.viewportSize()?.width ?? 0) >= 640
  const tracks = await gridTrackCount(page, 'main div.grid')
  if (wide) {
    expect(tracks, 'stat grid is 3-col at >= sm (640)').toBe(3)
  } else {
    expect(tracks, 'stat grid collapses to 1-col below sm').toBe(1)
  }
})
