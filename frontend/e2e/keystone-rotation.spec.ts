import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import { readHarness, localhostURL } from './fixtures/harness'
import {
  seedAndGotoController,
  loginAsOperator,
  mintEnrollToken,
  enrollNodeViaAgent,
  importDesignViaUI,
  runE2eAgent,
  readCsrf,
} from './fixtures/panel'
import { addVirtualAuthenticator } from './fixtures/virtualAuthenticator'
import { uniqueRouterPeer, runId } from './fixtures/designs'

// Keystone rotation (plan-15 / 3.3, Phase 9) — the documented fleet-stranding root cause, proven
// fixed end to end: pin OLD (TOFU) → deploy-sign under OLD → rotate WITH ack to NEW → deploy-sign
// under NEW → a node pinned to OLD REFUSES the NEW-signed bundle and ADOPTS it after
// reprovision-keystone; plus the un-acked-rotation 409. The F1 post-reload guard is plan-14's;
// this references, not re-builds it.
//
// The OLD/NEW operator-credential PEMs are the public_key_pem the panel POSTs to
// /operator-credential (the status endpoint never exposes the PEM body), captured here and handed
// to the agent's reprovision mode as the out-of-band rotated key. Import + node-enroll run FIRST
// (they navigate); both keystone enrolls + both signing deploys then run on /deploy with no
// navigation between, so the CDP virtual authenticator (which doesn't survive a full-page nav)
// stays alive holding both credentials.
//
// Negative-proof (dev-only): point the reprovision step at the wrong (OLD) PEM as the NEW key →
// adopt-after's VerifyMembership fails → this spec goes RED.

interface CredBody {
  credential_id: string
  public_key_pem: string
  alg: string
}

test('keystone rotation: OLD-sign → acked rotate to NEW → node refuses-then-adopts; un-acked 409', async (
  { page, context },
  testInfo,
) => {
  test.setTimeout(120_000)
  const h = readHarness()
  const target = { panel: localhostURL(h.controllerOn.panel), agent: localhostURL(h.controllerOn.agent) }
  page.on('dialog', (d) => void d.accept())
  await addVirtualAuthenticator(page)

  // Capture every POST /operator-credential body (OLD pin, then NEW rotate) to recover the PEMs.
  const credBodies: CredBody[] = []
  page.on('request', (r) => {
    if (r.url().includes('/operator/operator-credential') && r.method() === 'POST') {
      const d = r.postData()
      if (d) credBodies.push(JSON.parse(d) as CredBody)
    }
  })

  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  // Enroll a node + import a design FIRST (these navigate). The node populates the signed
  // trust-list; both keystone enrolls + signing deploys below stay on /deploy (authenticator alive).
  const { topo, router, peer } = uniqueRouterPeer(runId(process.pid, testInfo.workerIndex, Date.now()))
  const designPath = testInfo.outputPath('design.json')
  fs.writeFileSync(designPath, JSON.stringify(topo))
  const nodeBearer = testInfo.outputPath('router.bearer')
  const rTok = await mintEnrollToken(page, context, target.panel, router)
  const pTok = await mintEnrollToken(page, context, target.panel, peer)
  await enrollNodeViaAgent(h, target.agent, router, rTok, testInfo.outputPath('r.key'), nodeBearer)
  await enrollNodeViaAgent(h, target.agent, peer, pTok, testInfo.outputPath('p.key'))
  await importDesignViaUI(page, target.panel, designPath)

  await page.goto(`${target.panel}/deploy`)

  // (a) Establish THIS spec's OWN OLD signing credential, then deploy-sign under it. Tolerant of a
  // prior pin on the shared keystone-ON tenant (e.g. plan-14's deploy-keystone): if nothing is
  // pinned, first-pin OLD; if a credential is already pinned, rotate (with ack) to a fresh OLD.
  // Either way the captured POST body (credBodies[0]) is THIS spec's OLD, whose private half lives
  // in this test's authenticator — so the OLD-signed deploy + the node's OLD pin are self-consistent.
  const enrollBtn = page.getByRole('button', { name: /Enroll signing key/ })
  const rotateBtn = page.getByRole('button', { name: /Rotate signing key/ })
  await expect(enrollBtn.or(rotateBtn)).toBeVisible({ timeout: 15_000 })
  if (await enrollBtn.isVisible()) {
    await enrollBtn.click()
  } else {
    await rotateBtn.click()
    await page.getByRole('button', { name: /Rotate now/ }).click()
  }
  await expect(page.getByText(/^Enrolled \(/)).toBeVisible({ timeout: 20_000 })
  await deploySigned(page)

  // (c) Rotate WITH ack to NEW (create a second credential) + deploy-sign under NEW.
  await page.getByRole('button', { name: /Rotate signing key/ }).click()
  await page.getByRole('button', { name: /Rotate now/ }).click()
  await expect(page.getByText(/^Enrolled \(/)).toBeVisible({ timeout: 20_000 })
  await deploySigned(page)

  expect(credBodies.length, 'captured the OLD pin + the NEW rotate credential bodies').toBeGreaterThanOrEqual(2)
  const oldCred = credBodies[0]
  const newCred = credBodies[credBodies.length - 1]
  expect(oldCred.public_key_pem).not.toBe(newCred.public_key_pem)
  const oldPem = testInfo.outputPath('old-cred.pem')
  const newPem = testInfo.outputPath('new-cred.pem')
  fs.writeFileSync(oldPem, oldCred.public_key_pem)
  fs.writeFileSync(newPem, newCred.public_key_pem)

  // (d) The node pinned to OLD REFUSES the NEW-signed bundle, then ADOPTS it after reprovision.
  const stdout = await runE2eAgent(h, [
    '--controller', target.agent,
    '--node-id', router,
    '--mode', 'reprovision',
    '--bearer-file', nodeBearer,
    '--operator-cred', oldPem,
    '--operator-cred-alg', oldCred.alg,
    '--new-cred-pem', newPem,
    '--operator-rpid', 'localhost',
  ])
  expect(stdout).toContain('refuse-before=ok adopt-after=ok')

  // (b) Un-acked rotation: re-pinning a DIFFERENT (now-stale OLD) credential WITHOUT rotate:true is
  // refused with 409 keystone_rotation_requires_ack (the current pin is NEW).
  const csrf = await readCsrf(context, target.panel)
  const resp = await page.request.post(`${target.panel}/api/v1/operator/operator-credential`, {
    headers: { 'X-CSRF-Token': csrf },
    data: { credential_id: oldCred.credential_id, public_key_pem: oldCred.public_key_pem, alg: oldCred.alg, rotate: false },
  })
  expect(resp.status(), 'an un-acked changed-credential pin must be 409').toBe(409)
})

// deploySigned clicks Deploy and asserts the signed deploy completes — getTrustlist + the
// /trustlist-signature accepted by the Go verifier (200) + the Last-deploy block.
async function deploySigned(page: import('@playwright/test').Page): Promise<void> {
  const sigP = page.waitForResponse(
    (r) => r.url().includes('/operator/trustlist-signature') && r.request().method() === 'POST',
    { timeout: 30_000 },
  )
  await page.getByRole('button', { name: '🚀 Deploy' }).click()
  expect((await sigP).status(), 'the Go verifier accepts the browser signature').toBe(200)
  await expect(page.getByText('Last deploy')).toBeVisible({ timeout: 20_000 })
}
