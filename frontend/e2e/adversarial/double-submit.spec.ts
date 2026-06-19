import { test, expect } from '@playwright/test'
import { readHarness } from '../fixtures/harness'
import {
  seedAndGotoController,
  loginAsOperator,
  keystoneOffTarget,
  prepareUniqueDesign,
  selectNodeAndRename,
} from '../fixtures/panel'
import { installFaults } from './faults'

// double-submit.spec.ts (plan-16 / 3.4, Phase 7) — re-entrancy / idempotency of the deploy() and
// saveDesign() store actions. The Deploy/Save buttons are disabled while their action is in flight
// (DeployBar: disabled={loading||noAuth}; the Save button off the `saving` flag), but a SYNTHETIC
// click dispatched straight to the element bubbles to React's delegated onClick even on a disabled
// button — so it bypasses the visual guard and re-invokes the store action. This probes whether the
// action itself is idempotent (an early-return on loading/saving) or re-entrant (double-POST).
//
// The invariant: a re-entrant submit during an in-flight action produces a SINGLE effective
// state-changing POST, never two. We hold the first request open (delayMs) to widen the in-flight
// window, fire a second synthetic click, then release and count the POSTs.

test('a re-entrant Deploy during an in-flight deploy produces a single update-topology POST', async (
  { page, context },
  testInfo,
) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept())
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)
  await prepareUniqueDesign(page, context, h, target, testInfo)
  await page.goto(`${target.panel}/deploy`)
  await expect(page.getByText('Not enrolled')).toBeVisible({ timeout: 15_000 })

  // Hold the first update-topology in flight so the second click lands during the loading window.
  const faults = await installFaults(page, [{ route: 'update-topology', method: 'POST', delayMs: 1500 }])

  // Locate the Deploy button by its stable Tailwind class: its accessible NAME flips to "Deploying…"
  // while loading, so a name-based locator cannot see it mid-flight.
  const button = page.locator('button.bg-teal-600')
  await button.click() // fires deploy(); loading:true; update-topology now pending for ~1.5s
  await expect(button).toBeDisabled() // the visual guard engages

  // Re-entrancy probe: dispatch a synthetic click straight to the (disabled) button. This bubbles
  // to React's onClick and re-invokes deploy(). A correctly idempotent deploy() early-returns on
  // loading; a re-entrant one issues a SECOND update-topology POST.
  await button.dispatchEvent('click')
  await button.dispatchEvent('click')

  await expect(page.getByText('Last deploy')).toBeVisible({ timeout: 20_000 })
  expect(
    faults.count('update-topology', 'POST'),
    'a re-entrant Deploy must not double-POST update-topology (idempotency guard)',
  ).toBe(1)
})

test('a re-entrant Save during an in-flight save produces a single update-topology POST', async (
  { page, context },
  testInfo,
) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept())
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)
  const built = await prepareUniqueDesign(page, context, h, target, testInfo)

  // saveDesign no-ops a clean canvas, so dirty it deterministically by renaming a node (the aside
  // NodeEditor) — this makes the Save button active and forces an update-topology write.
  await selectNodeAndRename(page, target.panel, built.router, `renamed-${testInfo.workerIndex}`)

  const faults = await installFaults(page, [{ route: 'update-topology', method: 'POST', delayMs: 1500 }])
  // The Save button's accessible NAME changes across states (💾 Save → Saving... → Saved), so locate
  // it by its stable Tailwind base class instead. It starts enabled (canvas dirty).
  const save = page.locator('button.bg-green-600')
  await expect(save).toBeEnabled()

  const savedP = page.waitForResponse(
    (r) => r.url().includes('/operator/update-topology') && r.request().method() === 'POST',
    { timeout: 20_000 },
  )
  await save.click()
  await expect(save).toBeDisabled() // saving guard engages
  await save.dispatchEvent('click') // re-entrancy probe during the in-flight save
  await save.dispatchEvent('click')

  await savedP
  // After the save settles the button reads "Saved" (not dirty). Exactly one write hit the wire.
  await expect(page.locator('button.bg-green-600')).toHaveText('Saved', { timeout: 20_000 })
  expect(
    faults.count('update-topology', 'POST'),
    'a re-entrant Save must not double-POST update-topology (saving guard)',
  ).toBe(1)
})
