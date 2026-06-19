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
  runE2eAgent,
} from './fixtures/panel'
import { uniqueRouterPeer, runId } from './fixtures/designs'

// Fleet rekey (plan-15 / 3.3, Phase 7) — the two-way operator rekey matrix the panel drives:
// (a) Roll-keys (HandleRekeyAll) flags every approved node; the agent regenerates + re-registers
//     its WG key via the real Rekey wire, clearing its own rekey flag.
// (b) a straggler that never re-registers is released by the per-node "Cancel rekey" button
//     (HandleClearRekey) WITHOUT eviction (still approved, no generation bump).
// There is no third "cancel-rekey" path — only these two.

test('Roll-keys + agent rekey clears the actor; Cancel-rekey releases a straggler without eviction', async (
  { page, context },
  testInfo,
) => {
  test.setTimeout(60_000)
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept()) // import confirm + Roll-keys confirm

  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  const { topo, router, peer } = uniqueRouterPeer(runId(process.pid, testInfo.workerIndex, Date.now()))
  const designPath = testInfo.outputPath('design.json')
  fs.writeFileSync(designPath, JSON.stringify(topo))
  const routerBearer = testInfo.outputPath('router.bearer')
  const rTok = await mintEnrollToken(page, context, target.panel, router)
  const pTok = await mintEnrollToken(page, context, target.panel, peer)
  await enrollNodeViaAgent(h, target.agent, router, rTok, testInfo.outputPath('r.key'), routerBearer)
  await enrollNodeViaAgent(h, target.agent, peer, pTok, testInfo.outputPath('p.key'))

  // Deploy so a served generation exists (the agent's rekey Fetch needs a bundle).
  await importDesignViaUI(page, target.panel, designPath)
  await page.goto(`${target.panel}/deploy`)
  await page.getByRole('button', { name: '🚀 Deploy' }).click()
  await expect(page.getByText('Last deploy')).toBeVisible({ timeout: 20_000 })

  // (a) Roll-keys: flags every approved node for rotation + bumps the generation. Await the
  // /rekey-all response before driving the agent — otherwise the agent's rekey Fetch can race the
  // POST and see rekey_requested=false.
  const rekeyAllP = page.waitForResponse(
    (r) => r.url().includes('/operator/rekey-all') && r.request().method() === 'POST',
    { timeout: 15_000 },
  )
  await page.getByRole('button', { name: /Roll keys/ }).click()
  expect((await rekeyAllP).status(), 'rekey-all should be 200').toBe(200)

  // The router rotates via the real agent wire (reusing its bearer) → its rekey flag clears.
  const stdout = await runE2eAgent(h, [
    '--controller', target.agent,
    '--node-id', router,
    '--mode', 'rekey',
    '--bearer-file', routerBearer,
    '--key', testInfo.outputPath('r.key'),
  ])
  expect(stdout, 'agent completed the rekey').toContain('REKEY_DONE')

  // Server truth: navigate to Fleet (refresh-on-auth). The router (re-registered) no longer shows
  // "Cancel rekey"; the peer (straggler) still does.
  await page.goto(`${target.panel}/fleet`)
  const routerRow = page.locator('table tr').filter({ hasText: router })
  const peerRow = page.locator('table tr').filter({ hasText: peer })
  await expect(routerRow.getByRole('link', { name: router }).first()).toBeVisible({ timeout: 15_000 })
  await expect(
    routerRow.getByRole('button', { name: 'Cancel rekey' }),
    'router re-registered → no pending rekey',
  ).toHaveCount(0)
  await expect(
    peerRow.getByRole('button', { name: 'Cancel rekey' }),
    'peer straggler still owes a rekey',
  ).toBeVisible()

  // (b) Cancel-rekey the straggler → released WITHOUT eviction (still approved, button gone).
  await peerRow.getByRole('button', { name: 'Cancel rekey' }).click()
  await expect(peerRow.getByRole('button', { name: 'Cancel rekey' })).toHaveCount(0, { timeout: 15_000 })
  await expect(peerRow.getByText('approved'), 'Cancel-rekey does not evict the node').toBeVisible()
})
