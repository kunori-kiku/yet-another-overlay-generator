import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import { readHarness } from './fixtures/harness'
import {
  seedAndGotoController,
  loginAsOperator,
  keystoneOffTarget,
  mintEnrollToken,
  enrollNodeViaAgent,
  importDesignViaUI,
} from './fixtures/panel'
import { uniqueColliding, runId } from './fixtures/designs'

// Pin-collision heal (plan-15 / 3.3, Phase 10) — a known-colliding topology (two enabled edges
// pinning the SAME router-side transit IP) is user-safe end to end: it imports + deploys WITHOUT a
// CodePin*DuplicateCrossLink 4xx, because the heal runs on update-topology (server) and on canvas
// load (FE mirror). It does NOT assert TS↔Go heal byte-equality (1.5's conformance pin owns that);
// it proves wiring/behavior. R6 precondition: first confirm the raw fixture genuinely collides.

test('a colliding topology heals on import and deploys without a duplicate-pin error', async (
  { page, context },
  testInfo,
) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept())

  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  const c = uniqueColliding(runId(process.pid, testInfo.workerIndex, Date.now()))

  // R6 precondition: the raw fixture WOULD collide — both edges pin the same router-side transit IP.
  const edges = (c.topo as { edges: Array<{ pinned_to_transit_ip?: string }> }).edges
  expect(edges[0].pinned_to_transit_ip, 'fixture edge e-a pins the colliding transit IP').toBe(c.collidingTransitIp)
  expect(edges[1].pinned_to_transit_ip, 'fixture edge e-b pins the SAME transit IP (the collision)').toBe(
    c.collidingTransitIp,
  )

  const designPath = testInfo.outputPath('colliding.json')
  fs.writeFileSync(designPath, JSON.stringify(c.topo))

  // Enroll all three nodes so stage compiles the whole graph.
  for (const [node, key] of [
    [c.router, 'cr.key'],
    [c.peerA, 'ca.key'],
    [c.peerB, 'cb.key'],
  ] as const) {
    const tok = await mintEnrollToken(page, context, target.panel, node)
    await enrollNodeViaAgent(h, target.agent, node, tok, testInfo.outputPath(key))
  }

  // Import the colliding design (the server heals colliding pins on update-topology).
  await importDesignViaUI(page, target.panel, designPath)

  // Deploy: capture the stage response — it must NOT 4xx (a duplicate-pin error would be 4xx). The
  // heal (load + server) repaired the collision, so stage compiles cleanly.
  await page.goto(`${target.panel}/deploy`)
  const stageP = page.waitForResponse(
    (r) => r.url().includes('/operator/stage') && r.request().method() === 'POST',
    { timeout: 20_000 },
  )
  await page.getByRole('button', { name: '🚀 Deploy' }).click()
  const stageResp = await stageP
  expect(stageResp.status(), 'stage must not 4xx on a duplicate-pin error (the heal repaired it)').toBe(200)

  // The deploy completes end to end.
  await expect(page.getByText('Last deploy')).toBeVisible({ timeout: 20_000 })
})
