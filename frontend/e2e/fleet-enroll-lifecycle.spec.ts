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

// Fleet enroll lifecycle (plan-15 / 3.3, Phase 6) — the FULL real-mode agent wire end to end:
// enroll → (operator deploys) → poll → fetch → VerifyBundle → report the APPLIED generation,
// against the real built panel + a real OS-port agent process (not an in-proc httptest.Server).
// Goes beyond plan-13/14's --mock check-in: it fetches + verifies a real deployed bundle and
// reports the applied generation, which the Fleet registry then reflects.

test('enroll → deploy → real poll/fetch/verify/report the applied generation', async (
  { page, context },
  testInfo,
) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept())

  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  const { topo, router, peer } = uniqueRouterPeer(runId(process.pid, testInfo.workerIndex, Date.now()))
  const designPath = testInfo.outputPath('design.json')
  fs.writeFileSync(designPath, JSON.stringify(topo))

  // Enroll both nodes so stage compiles them. The router persists its bearer (so the real-mode
  // check-in below reuses it — the enrollment token is single-use); the peer is a mock check-in.
  const routerBearer = testInfo.outputPath('router.bearer')
  const rTok = await mintEnrollToken(page, context, target.panel, router)
  const pTok = await mintEnrollToken(page, context, target.panel, peer)
  await enrollNodeViaAgent(h, target.agent, router, rTok, testInfo.outputPath('r.key'), routerBearer)
  await enrollNodeViaAgent(h, target.agent, peer, pTok, testInfo.outputPath('p.key'))

  // Operator deploys → a promoted generation exists.
  await importDesignViaUI(page, target.panel, designPath)
  await page.goto(`${target.panel}/deploy`)
  await page.getByRole('button', { name: '🚀 Deploy' }).click()
  await page.getByTestId('deploy-preview-confirm').click() // plan-6 preview dialog
  await expect(page.getByText('Last deploy')).toBeVisible({ timeout: 20_000 })

  // The router does a REAL check-in (reusing its bearer): poll the promoted generation, fetch its
  // bundle, VerifyBundle, and report the APPLIED generation (≥1). install.sh is never run.
  const stdout = await runE2eAgent(h, [
    '--controller', target.agent,
    '--node-id', router,
    '--bearer-file', routerBearer,
    '--key', testInfo.outputPath('r.key'),
  ])
  const m = stdout.match(/reported_generation=(\d+)/)
  expect(m, 'agent printed a reported_generation').not.toBeNull()
  expect(Number(m![1]), 'the agent fetched + verified a real deployed generation (≥1)').toBeGreaterThanOrEqual(1)

  // The Fleet registry reflects the node.
  await page.goto(`${target.panel}/fleet`)
  await expect(page.getByRole('link', { name: router }).first()).toBeVisible({ timeout: 15_000 })
})
