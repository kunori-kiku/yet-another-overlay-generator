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
import { readPersisted, assertNoFleetSecrets } from './fixtures/leakOracle'
import { uniqueRouterPeer, runId } from './fixtures/designs'

// Deploy journey (plan-14 Phase 3) — keystone-OFF tenant. The keystone-ON F1 regression +
// signature-acceptance leg (3.3) lives in deploy-keystone.spec.ts (it needs the virtual
// authenticator). Here: the design → enroll → stage → promote flow with no operator credential
// pinned, plus the post-deploy custody flush (3.4).

test('keystone-OFF deploy: import → enroll → stage → promote, then custody flush', async (
  { page, context },
  testInfo,
) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  // Accept the controller-mode import confirm dialog (window.confirm).
  page.on('dialog', (d) => void d.accept())

  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  const { topo, router, peer } = uniqueRouterPeer(runId(process.pid, testInfo.workerIndex, Date.now()))
  const designPath = testInfo.outputPath('design.json')
  fs.writeFileSync(designPath, JSON.stringify(topo))

  // Enroll both nodes (fresh single-use tokens, then a mock check-in) so stage can compile them.
  const rTok = await mintEnrollToken(page, context, target.panel, router)
  const pTok = await mintEnrollToken(page, context, target.panel, peer)
  await enrollNodeViaAgent(h, target.agent, router, rTok, testInfo.outputPath('r.key'))
  await enrollNodeViaAgent(h, target.agent, peer, pTok, testInfo.outputPath('p.key'))

  // Import the design (controller mode: key-free, server-authoritative).
  await importDesignViaUI(page, target.panel, designPath)

  // Deploy.
  await page.goto(`${target.panel}/deploy`)
  // keystone-OFF precondition (load-bearing): no operator signing key is pinned, so the deploy
  // takes the no-trustlist branch (stage → promote, no signing). The DeployBar shows it.
  await expect(page.getByText('Not enrolled')).toBeVisible({ timeout: 15_000 })

  await page.getByRole('button', { name: '🚀 Deploy' }).click()

  // The deploy succeeded: the Last-deploy block renders with a generation and the staged nodes.
  await expect(page.getByText('Last deploy')).toBeVisible({ timeout: 20_000 })
  await expect(page.getByText(router, { exact: false })).toBeVisible()

  // (3.4) Post-deploy custody: the server-held design is blanked from localStorage (no fleet
  // sentinels persist; controller-storage holds only the allowlist).
  assertNoFleetSecrets(await readPersisted(page), { expectServerHeldBlank: true })
})
