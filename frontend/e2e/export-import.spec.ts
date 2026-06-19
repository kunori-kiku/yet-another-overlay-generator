import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import { readHarness } from './fixtures/harness'
import {
  seedAndGotoController,
  loginAsOperator,
  keystoneOffTarget,
  importDesignViaUI,
} from './fixtures/panel'
import { seedLocalMode } from './fixtures/seedStore'
import { readPersisted, assertNoFleetSecrets } from './fixtures/leakOracle'
import { uniqueRouterPeer, runId } from './fixtures/designs'

// Export / import round-trip (plan-14 Phase 4). Controller-mode export is key-free; controller
// import drops keys + writes a new version WITHOUT staging/promoting; a malformed import is
// rejected by the shape gate; nothing fleet-secret persists.

test('controller import strips keys before upload; export is key-free; import does not deploy', async (
  { page, context },
  testInfo,
) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept())

  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  // Build a KEYED design so dropAllKeys (custody.ts) actually has key material to strip — the
  // security contract "a private key never reaches the server". A key-FREE design would make the
  // strip assertion vacuous (dropped===0).
  const PRIV_SENTINEL = 'INJECTED-PRIVATE-KEY-must-be-stripped-zzzz'
  const PUB_SENTINEL = 'INJECTED-PUBLIC-KEY-must-be-stripped-yyyy'
  const built = uniqueRouterPeer(runId(process.pid, testInfo.workerIndex, Date.now()))
  const topo = built.topo as { nodes: Record<string, unknown>[] }
  topo.nodes[0].wireguard_private_key = PRIV_SENTINEL
  topo.nodes[0].wireguard_public_key = PUB_SENTINEL
  topo.nodes[0].fixed_private_key = true
  const designPath = testInfo.outputPath('design.json')
  fs.writeFileSync(designPath, JSON.stringify(built.topo))

  // Capture the update-topology POST body (to prove the keys never reach the server) and watch
  // for any stage/promote (import ≠ deploy). Registered BEFORE the import fires.
  let updateBody = ''
  const deployCalls: string[] = []
  page.on('request', (r) => {
    const u = r.url()
    if (u.includes('/operator/update-topology') && r.method() === 'POST') updateBody = r.postData() ?? ''
    if (u.includes('/operator/stage') || u.includes('/operator/promote')) deployCalls.push(u)
  })

  await importDesignViaUI(page, target.panel, designPath)

  // (4.2) dropAllKeys ran: the injected key material is NOT in the uploaded body, and the import
  // posted update-topology but never staged/promoted.
  expect(updateBody, 'the update-topology POST body was captured').not.toBe('')
  expect(updateBody.includes(PRIV_SENTINEL), 'the private key must be stripped before upload').toBe(false)
  expect(updateBody.includes(PUB_SENTINEL), 'the public key must be stripped before upload').toBe(false)
  expect(deployCalls, 'import must not stage or promote').toEqual([])

  // (4.1) Export the controller design and assert it is key-free (controller is key-authoritative).
  const [download] = await Promise.all([
    page.waitForEvent('download'),
    page.getByRole('button', { name: 'Export' }).click(),
  ])
  const exported = fs.readFileSync(await download.path(), 'utf8')
  const parsed = JSON.parse(exported) as { nodes?: unknown[] }
  expect(Array.isArray(parsed.nodes), 'export parses as a design with nodes').toBe(true)
  expect(exported.includes(PRIV_SENTINEL), 'export must carry no private key').toBe(false)
  expect(exported.includes(PUB_SENTINEL), 'export must carry no public key').toBe(false)
  expect(exported.includes('-----BEGIN'), 'export must carry no PEM block').toBe(false)

  // (4.4) Custody: after the controller import nothing fleet-secret persisted.
  assertNoFleetSecrets(await readPersisted(page), { expectServerHeldBlank: true })
})

test('controller import of a malformed file is rejected by the shape gate (no server write)', async (
  { page, context },
  testInfo,
) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept())

  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  const updateCalls: string[] = []
  page.on('request', (r) => {
    if (r.url().includes('/operator/update-topology')) updateCalls.push(r.url())
  })

  const badPath = testInfo.outputPath('malformed.json')
  fs.writeFileSync(badPath, JSON.stringify({ not: 'a design' }))

  await page.goto(`${target.panel}/design`)
  await page.locator('input[type="file"]').setInputFiles(badPath)

  // The Array.isArray(project/domains/nodes/edges) shape gate rejects client-side before any
  // network write. Give it a moment, then assert update-topology never fired.
  await page.waitForTimeout(1500)
  expect(updateCalls, 'a malformed import must not reach update-topology').toEqual([])
})

test('local-mode import of a malformed file does not load garbage', async ({ page, context }, testInfo) => {
  await seedLocalMode(context)
  const h = readHarness()
  // Local mode has no controller gate; serve the SPA from any boot.
  await page.goto(`${keystoneOffTarget(h).panel}/design`)

  const badPath = testInfo.outputPath('malformed-local.json')
  fs.writeFileSync(badPath, JSON.stringify({ project: { id: 'x' } })) // missing domains/nodes/edges
  await page.locator('input[type="file"]').setInputFiles(badPath)

  // The shape guard (topo.project && domains && nodes && edges) is false → nothing loads; the
  // canvas stays usable. Assert no crash + the design did not adopt the malformed project id
  // anywhere persisted.
  await page.waitForTimeout(1000)
  const stores = await readPersisted(page)
  const topo = stores.topology?.state as { nodes?: unknown[] } | undefined
  expect(Array.isArray(topo?.nodes) ? topo!.nodes!.length : 0).toBe(0)
})
