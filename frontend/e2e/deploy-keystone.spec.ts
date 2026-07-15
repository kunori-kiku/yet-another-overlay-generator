import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import { readHarness, localhostURL } from './fixtures/harness'
import {
  seedAndGotoController,
  loginAsOperator,
  mintEnrollToken,
  enrollNodeViaAgent,
  importDesignViaUI,
} from './fixtures/panel'
import { addVirtualAuthenticator } from './fixtures/virtualAuthenticator'
import { uniqueRouterPeer, runId } from './fixtures/designs'

// Keystone-ON deploy (plan-14 Phase 1.4 + 3.3) — the program's hardest automated leg. On the
// keystone-ON controller boot (its own tenant): enroll the off-host operator SIGNING credential
// via the CDP virtual authenticator (POST /operator-credential → serverOperatorPinned true),
// then deploy so getTrustlist goes through request() (the beta.8 F1 fix — credentials included,
// no 401), the browser signs the staged manifest via navigator.credentials.get(), and the Go
// /trustlist-signature verifier ACCEPTS the signature end to end (a successful promote proves
// it, since keystone-ON promote refuses an unsigned/wrongly-signed manifest).
//
// Ordering note: the CDP virtual authenticator does NOT survive a full-page navigation, so the
// design import + node enrolls (which navigate to /design) run FIRST, and the keystone-credential
// enroll + the signing deploy then both happen on /deploy with no navigation between them.

test('keystone-ON: enroll signing key, deploy signs the manifest, Go verifier accepts', async (
  { page, context },
  testInfo,
) => {
  // Heavy spec: WebAuthn enroll/sign ceremonies + import + two agent-subprocess enrolls + a
  // signing deploy. Ample headroom over the 30s default.
  test.setTimeout(120_000)
  const h = readHarness()
  // localhost (NOT 127.0.0.1) so the WebAuthn RP-ID is registrable (assertRegistrableRpId).
  const target = {
    panel: localhostURL(h.controllerOn.panel),
    agent: localhostURL(h.controllerOn.agent),
  }
  page.on('dialog', (d) => void d.accept())
  await addVirtualAuthenticator(page)

  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  // Import a design + enroll its nodes FIRST (these navigate /design). The keystone enroll + the
  // signing deploy then both happen on /deploy below with no navigation between.
  const { topo, router, peer } = uniqueRouterPeer(runId(process.pid, testInfo.workerIndex, Date.now()))
  const designPath = testInfo.outputPath('design.json')
  fs.writeFileSync(designPath, JSON.stringify(topo))
  const rTok = await mintEnrollToken(page, context, target.panel, router)
  const pTok = await mintEnrollToken(page, context, target.panel, peer)
  await enrollNodeViaAgent(h, target.agent, router, rTok, testInfo.outputPath('r.key'))
  await enrollNodeViaAgent(h, target.agent, peer, pTok, testInfo.outputPath('p.key'))
  await importDesignViaUI(page, target.panel, designPath)

  // (1.4) On /deploy: begin → create() → exact-candidate UV get() → POST the keystone pin.
  await page.goto(`${target.panel}/deploy`)
  await expect(page.getByText('Not enrolled')).toBeVisible({ timeout: 15_000 })
  const enrollRespP = page.waitForResponse(
    (r) => r.url().includes('operator-credential') && r.request().method() === 'POST',
    { timeout: 20_000 },
  )
  const beginP = page.waitForRequest(
    (r) => r.url().includes('/webauthn/enrollment/begin') && r.method() === 'POST',
  )
  await page.getByRole('button', { name: /Enroll signing key/ }).click()
  const beginBody = (await beginP).postDataJSON() as { purpose: string }
  const enrollResp = await enrollRespP
  const enrollBody = enrollResp.request().postDataJSON() as {
    credential_id: string
    enrollment_proof: { credential_id: string }
  }
  expect(beginBody.purpose).toBe('keystone')
  expect(enrollBody.enrollment_proof).toBeTruthy()
  expect(enrollBody.enrollment_proof.credential_id).toBe(enrollBody.credential_id)
  expect(enrollResp.status(), 'POST /operator-credential should pin the signing key').toBe(200)
  await expect(page.getByText(/^Enrolled \(/)).toBeVisible({ timeout: 20_000 })

  // (3.3) Deploy on the SAME /deploy page (no navigation since enroll, so the authenticator is
  // alive for signManifest). Capture the keystone HTTP legs to assert them precisely.
  const trustlistP = page.waitForResponse(
    (r) =>
      r.url().includes('/operator/trustlist') &&
      !r.url().includes('trustlist-signature') &&
      r.request().method() === 'GET',
    { timeout: 30_000 },
  )
  const signatureP = page.waitForResponse(
    (r) => r.url().includes('/operator/trustlist-signature') && r.request().method() === 'POST',
    { timeout: 30_000 },
  )

  await page.getByRole('button', { name: '🚀 Deploy' }).click()
  await page.getByTestId('deploy-preview-confirm').click() // plan-6 preview dialog → runs the deploy

  // F1 fix: getTrustlist goes through request() (credentials:include) — it must NOT 401 (the
  // pre-fix raw fetch dropped credentials and 401'd a cookie-only operator on a keystone-ON deploy).
  expect((await trustlistP).status(), 'getTrustlist via request() must not 401 (F1)').toBe(200)

  // The Go /trustlist-signature verifier ACCEPTS the browser-produced signature (end-to-end).
  expect((await signatureP).status(), 'Go verifier must ACCEPT the browser signature').toBe(200)

  // Promote succeeded (keystone-ON promote refuses an unsigned/wrong manifest) → Last deploy.
  await expect(page.getByText('Last deploy')).toBeVisible({ timeout: 20_000 })
  await expect(page.getByText(router, { exact: false })).toBeVisible()
})
