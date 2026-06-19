import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { readHarness, httpURL, e2eDir } from './fixtures/harness'
import { seedLocalMode } from './fixtures/seedStore'

// Canary 1 — the air-gap cross-stack path: the REAL built panel is served by the air-gap
// cmd/e2eserver boot, and that boot's /api/compile compiles a topology UNauthenticated.
//
// Post-Subject-1, local mode compiles IN-BROWSER (the TS compiler) by default, so the panel
// UI no longer round-trips /api/compile. This canary therefore pins the RETAINED air-gap
// compute oracle directly — the surface the backend-engine escape hatch and plan-21's
// -tags airgap DAST depend on — proving it is reachable + unauthenticated from a real
// browser context (DoD #5: air-gap boot serves /api/compile open; the controller boot gates
// it, see controller-fleet.spec.ts's sibling at the API level).

const seedTopology = JSON.parse(
  fs.readFileSync(path.join(e2eDir, 'fixtures', 'seed-topology.json'), 'utf8'),
)

test('air-gap boot serves the built panel and an unauthenticated /api/compile', async ({
  page,
  context,
}) => {
  const h = readHarness()
  const panel = httpURL(h.airgap.panel)

  // Local mode (explicit, for order-independence): no login gate, the Shell renders directly.
  await seedLocalMode(context)
  await page.goto(`${panel}/`)
  // The SPA mounted from the air-gap server — the Shell always renders the #main-content
  // landmark. Proves the real built panel is actually served by this boot.
  await expect(page.locator('#main-content')).toBeAttached()

  // The air-gap compute round-trip from the loaded panel's browser context, with NO auth
  // headers. operatorAuth is nil on this boot → gateAirgap passes through → 200 with the
  // rendered per-node configs.
  const resp = await page.request.post(`${panel}/api/compile`, { data: seedTopology })
  expect(resp.status()).toBe(200)
  const body = await resp.text()
  // A rendered WireGuard config is present for the compiled topology.
  expect(body).toContain('[Interface]')
})
