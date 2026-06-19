import { test, expect } from '@playwright/test'
import { readHarness } from './fixtures/harness'
import {
  seedAndGotoController,
  loginAsOperator,
  keystoneOffTarget,
  mintEnrollToken,
  enrollNodeViaAgent,
} from './fixtures/panel'
import { runId } from './fixtures/designs'

// Revoke touchpoint (plan-14 Phase 5.1). Revoke is server-state, NOT eviction: after revoke the
// node STAYS in the registry with status 'revoked' and its Revoke action disables. The
// server-side anti-resurrection contract (S4/S5) is the Go regression suite's, not this spec's.

test('revoke keeps the node in the registry as revoked with the action disabled', async (
  { page, context },
  testInfo,
) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  // Accept the revoke confirmation (window.confirm).
  page.on('dialog', (d) => void d.accept())

  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  // Enroll a uniquely-named node so this spec's row is unambiguous in the shared registry.
  const node = `rev-${runId(process.pid, testInfo.workerIndex, Date.now())}`
  const tok = await mintEnrollToken(page, context, target.panel, node)
  await enrollNodeViaAgent(h, target.agent, node, tok, testInfo.outputPath('rev.key'))

  await page.goto(`${target.panel}/fleet`)
  // Scope to the desktop registry table row for this node (mobile cards are divs, not <tr>).
  const row = page.locator('table tr').filter({ hasText: node })
  await expect(row.getByRole('link', { name: node })).toBeVisible({ timeout: 15_000 })

  await row.getByRole('button', { name: 'Revoke' }).click()

  // The node REMAINS in the registry (server keeps it) with status 'revoked', and its Revoke
  // action is now disabled — NOT removed from the list.
  await expect(row.getByText('revoked')).toBeVisible({ timeout: 15_000 })
  await expect(row.getByRole('button', { name: 'Revoke' })).toBeDisabled()
})
