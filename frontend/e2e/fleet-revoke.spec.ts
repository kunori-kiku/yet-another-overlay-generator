import { test, expect } from '@playwright/test'
import { readHarness } from './fixtures/harness'
import {
  seedAndGotoController,
  loginAsOperator,
  keystoneOffTarget,
  mintEnrollToken,
  enrollNodeViaAgent,
  runE2eAgent,
} from './fixtures/panel'
import { runId } from './fixtures/designs'

// Fleet revoke — the S4/S5 fleet-lifecycle DELTA on top of plan-14's basic revoke touchpoint
// (which already asserts the node stays in the registry as 'revoked' with the action disabled).
// This spec proves, end to end through a real agent process, what the basic touchpoint does not:
// (a) a revoked node's LIVE agent check-in is rejected (bearer revoked — S-revoke), and
// (b) a still-held SECOND enrollment token cannot RESURRECT the node (S5 purge-on-revoke / S4
//     revoked-resurrection guard) — it never succeeds.
//
// Negative-proof (dev-only): skip the HandleRevoke token purge (S5) → the held second token
// enrolls 200 (resurrection) → this spec goes RED.

test('revoke: live agent check-in is rejected and a held second token cannot resurrect the node', async (
  { page, context },
  testInfo,
) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept()) // revoke confirmation

  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  const node = `rev-${runId(process.pid, testInfo.workerIndex, Date.now())}`
  const bearer = testInfo.outputPath('node.bearer')
  const tok = await mintEnrollToken(page, context, target.panel, node)
  await enrollNodeViaAgent(h, target.agent, node, tok, testInfo.outputPath('n.key'), bearer)

  // The operator mints a SECOND enrollment token for the SAME node BEFORE revoking — the S5
  // resurrection vector (a token the node still holds when it is evicted).
  const secondToken = await mintEnrollToken(page, context, target.panel, node)

  // Sanity: the live agent can check in BEFORE revoke (reusing its bearer).
  expect(await runE2eAgent(h, ['--controller', target.agent, '--node-id', node, '--bearer-file', bearer, '--mock', '--key', testInfo.outputPath('n.key')])).toContain('E2E_AGENT')

  // Revoke the node in the panel (HandleRevoke: status→revoked, bearer revoked, enroll tokens purged).
  await page.goto(`${target.panel}/fleet`)
  const row = page.locator('table tr').filter({ hasText: node })
  await expect(row.getByRole('link', { name: node }).first()).toBeVisible({ timeout: 15_000 })
  await row.getByRole('button', { name: 'Revoke' }).click()
  await expect(row.getByText('revoked'), '(c) the panel reflects the revoked node').toBeVisible({ timeout: 15_000 })

  // (a) the revoked node's live agent check-in is now rejected (bearer revoked) → the agent exits
  // non-zero, so the invocation rejects.
  await expect(
    runE2eAgent(h, ['--controller', target.agent, '--node-id', node, '--bearer-file', bearer, '--mock', '--key', testInfo.outputPath('n.key')]),
    '(a) revoked bearer must be rejected on the next check-in',
  ).rejects.toThrow()

  // (b) the still-held SECOND token cannot resurrect the node — enroll fails (401 purged / 409
  // revoked-resurrection guard), never succeeds.
  await expect(
    runE2eAgent(h, ['--controller', target.agent, '--node-id', node, '--token', secondToken, '--mock', '--key', testInfo.outputPath('resurrect.key')]),
    '(b) a held second enrollment token must NOT resurrect a revoked node',
  ).rejects.toThrow()
})
