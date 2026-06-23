import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import { readHarness } from '../fixtures/harness'
import {
  seedAndGotoController,
  loginAsOperator,
  keystoneOffTarget,
  prepareUniqueDesign,
  selectNodeAndRename,
  importDesignViaUI,
  readCsrf,
} from '../fixtures/panel'
import { uniqueRouterPeer, runId } from '../fixtures/designs'
import { installFaults } from './faults'

// concurrency.spec.ts (plan-16 / 3.4, Phase 7) — concurrent-operator coherence. Two tests:
//
//  1. Save conflict (deterministic, one-context + server-seeded mutation — the plan's preferred
//     hard-assertion variant). Operator A dirties the canvas, then the server design is changed out
//     from under A (a second operator's edit, replayed as a direct authenticated POST). A's Save
//     must detect the divergence (saveConflict) and NOT silently clobber the server — proven by the
//     conflict dialog appearing and ZERO update-topology writes from the Save.
//  2. Server-authoritative (true two-context smoke, R6). A and B are independent browser contexts
//     (separate cookie jars) logged in as the same operator. B replaces the server design; A reloads
//     and its canvas adopts B's design — A's stale view never resurrects and clobbers B's change.

test('Save detects a concurrent server change and does not clobber it (saveConflict)', async (
  { page, context },
  testInfo,
) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept())
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)
  const built = await prepareUniqueDesign(page, context, h, target, testInfo)

  // A makes a local edit (canvas now dirty vs the synced baseline).
  await selectNodeAndRename(page, target.panel, built.router, 'edited-by-A')

  // A second operator changes the server design (replayed as a direct authenticated POST using A's
  // cookie + CSRF — same effect as another tab's Save). This diverges the SERVER from A's baseline.
  const csrf = await readCsrf(context, target.panel)
  const serverEdit = { ...built.topo, project: { ...built.topo.project, name: 'changed-by-other-operator' } }
  const resp = await page.request.post(`${target.panel}/api/v1/operator/update-topology`, {
    headers: { 'X-CSRF-Token': csrf, 'Content-Type': 'application/json' },
    data: JSON.stringify(serverEdit),
  })
  expect(resp.status(), 'the concurrent server edit should persist (200)').toBe(200)

  // Count writes from the Save attempt only (faults installed after the external edit).
  const faults = await installFaults(page, [])
  const save = page.getByTestId('save-design')
  await expect(save).toBeEnabled()
  await save.click()

  // The conflict dialog appears; saveDesign returned BEFORE writing — no clobber of the other edit.
  await expect(page.getByRole('dialog')).toBeVisible({ timeout: 15_000 })
  await expect(page.getByText('Server design changed')).toBeVisible()
  expect(
    faults.count('update-topology', 'POST'),
    'a conflicted Save must NOT write (no silent clobber)',
  ).toBe(0)
})

test('a second operator’s server change is adopted on reload, not clobbered by a stale tab', async (
  { browser },
  testInfo,
) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)

  // Context A: import design A.
  const ctxA = await browser.newContext()
  const pageA = await ctxA.newPage()
  pageA.on('dialog', (d) => void d.accept())
  await seedAndGotoController(pageA, ctxA, target)
  await loginAsOperator(pageA)
  const a = uniqueRouterPeer(runId(process.pid, testInfo.workerIndex, Date.now()))
  const aPath = testInfo.outputPath('a.json')
  fs.writeFileSync(aPath, JSON.stringify(a.topo))
  await importDesignViaUI(pageA, target.panel, aPath)

  // Context B (independent cookie jar): import a DIFFERENT design B → server now holds B.
  const ctxB = await browser.newContext()
  const pageB = await ctxB.newPage()
  pageB.on('dialog', (d) => void d.accept())
  await seedAndGotoController(pageB, ctxB, target)
  await loginAsOperator(pageB)
  const b = uniqueRouterPeer(runId(process.pid, testInfo.workerIndex + 1000, Date.now() + 1))
  const bPath = testInfo.outputPath('b.json')
  fs.writeFileSync(bPath, JSON.stringify(b.topo))
  await importDesignViaUI(pageB, target.panel, bPath)

  // A reloads: hydrateFromServer pulls the server's authoritative design (now B's). A's canvas must
  // show B's nodes — the server change is adopted, A's stale design is not resurrected.
  await pageA.reload()
  await expect(pageA.getByRole('button', { name: 'Account' })).toBeVisible({ timeout: 15_000 })
  await pageA.goto(`${target.panel}/design`)
  await expect(pageA.locator(`.react-flow__node[data-id="${b.router}"]`)).toBeVisible({ timeout: 15_000 })
  await expect(pageA.locator(`.react-flow__node[data-id="${a.router}"]`)).toBeHidden()

  await ctxA.close()
  await ctxB.close()
})
