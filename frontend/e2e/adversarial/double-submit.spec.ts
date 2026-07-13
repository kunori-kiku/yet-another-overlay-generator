import { test, expect } from '@playwright/test'
import { readHarness } from '../fixtures/harness'
import {
  seedAndGotoController,
  loginAsOperator,
  keystoneOffTarget,
  prepareUniqueDesign,
  selectNodeAndRename,
  mintEnrollToken,
  enrollNodeViaAgent,
} from '../fixtures/panel'
import { runId } from '../fixtures/designs'
import { OPERATOR_USER, OPERATOR_PASS } from '../fixtures/config'
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

  // plan-6: the Deploy button now opens a pre-deploy preview dialog; the dialog's Confirm button is
  // what fires deploy(). Open the dialog, then probe re-entrancy on Confirm — it stays mounted
  // through the in-flight deploy (the preview clears only on completion), so a synthetic re-click can
  // reach it. Located by its stable data-testid (its accessible NAME flips to "Deploying…" while
  // loading, so a name-based locator cannot see it mid-flight).
  await page.getByTestId('deploy').click()
  const button = page.getByTestId('deploy-preview-confirm')
  await button.click() // fires deploy(); loading:true; update-topology now pending for ~1.5s
  await expect(button).toBeDisabled() // the visual guard engages

  // Re-entrancy probe: dispatch a synthetic click straight to the (disabled) Confirm button. This
  // bubbles to React's onClick and re-invokes deploy(). A correctly idempotent deploy() early-returns
  // on loading; a re-entrant one issues a SECOND update-topology POST.
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
  const save = page.getByTestId('save-design')
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
  await expect(page.getByTestId('save-design')).toHaveText('Saved', { timeout: 20_000 })
  expect(
    faults.count('update-topology', 'POST'),
    'a re-entrant Save must not double-POST update-topology (saving guard)',
  ).toBe(1)
})

// The remaining three actions plan-16 Phase 7 step 11 names — login / Roll-keys / revoke — share the
// same in-flight-`loading` + disabled-button pattern proven double-POSTable for Deploy, so each got
// the same idempotency early-return guard; these probes pin that a synthetic re-click issues one POST.

test('a re-entrant login submit produces a single login POST', async ({ page, context }) => {
  const target = keystoneOffTarget(readHarness())
  await seedAndGotoController(page, context, target) // lands on the LoginPage

  const faults = await installFaults(page, [{ route: 'login', method: 'POST', delayMs: 1500 }])
  await page.locator('#login-username').fill(OPERATOR_USER)
  await page.locator('#login-password').fill(OPERATOR_PASS)
  const form = page.locator('form')
  await form.locator('button[type="submit"]').click() // login() fires; loading:true; login POST held ~1.5s
  // Re-entrancy probe: unlike Deploy/Roll-keys/revoke (React onClick, which fires even on a DISABLED
  // button), login submits via the form's NATIVE activation — and a disabled submit button's native
  // activation is DOM-suppressed, so a click on it would NOT re-enter. Dispatch a synthetic 'submit'
  // straight to the <form> instead: React's delegated onSubmit fires regardless of the button state,
  // genuinely re-invoking login() so the idempotency guard (not the disabled button) is what's tested.
  await form.dispatchEvent('submit')
  await form.dispatchEvent('submit')

  // The login completes and the form detaches; exactly one login POST was issued (no extra
  // rate-limit attempt burned).
  await expect(page.locator('#login-username')).toBeHidden({ timeout: 20_000 })
  expect(faults.count('login', 'POST'), 'a re-entrant login must not double-POST').toBe(1)
})

test('a re-entrant Roll-keys produces a single rekey-all POST', async ({ page, context }, testInfo) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept()) // accept the import confirm AND each Roll-keys confirm
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)
  await prepareUniqueDesign(page, context, h, target, testInfo)
  await page.goto(`${target.panel}/deploy`)

  const faults = await installFaults(page, [{ route: 'rekey-all', method: 'POST', delayMs: 1500 }])
  const rollKeys = page.getByTestId('roll-keys') // Roll-keys; stable data-testid
  const rekeyP = page.waitForResponse(
    (r) => r.url().includes('/operator/rekey-all') && r.request().method() === 'POST',
    { timeout: 20_000 },
  )
  await rollKeys.click() // confirm accepted → rollKeys(); loading:true; rekey-all held
  await expect(rollKeys).toBeDisabled()
  await rollKeys.dispatchEvent('click') // each re-click re-confirms → rollKeys() → guard drops it
  await rollKeys.dispatchEvent('click')

  await rekeyP
  expect(faults.count('rekey-all', 'POST'), 'a re-entrant Roll-keys must not double-POST rekey-all').toBe(1)
})

test('a re-entrant Revoke produces a single revoke POST', async ({ page, context }, testInfo) => {
  const h = readHarness()
  const target = keystoneOffTarget(h)
  page.on('dialog', (d) => void d.accept()) // accept each Revoke confirm
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  // Enroll a uniquely-named node so its registry row is unambiguous, then drive its Revoke.
  const node = `ds-rev-${runId(process.pid, testInfo.workerIndex, Date.now())}`
  const tok = await mintEnrollToken(page, context, target.panel, node)
  await enrollNodeViaAgent(h, target.agent, node, tok, testInfo.outputPath('dsrev.key'))
  await page.goto(`${target.panel}/fleet`)
  const row = page.locator('table tr').filter({ hasText: node })
  const revoke = row.getByRole('button', { name: 'Revoke' })
  await expect(revoke).toBeVisible({ timeout: 15_000 })

  const faults = await installFaults(page, [{ route: 'revoke', method: 'POST', delayMs: 1500 }])
  const revokeP = page.waitForResponse(
    (r) => r.url().includes('/operator/revoke') && r.request().method() === 'POST',
    { timeout: 20_000 },
  )
  await revoke.click() // confirm accepted → revoke(); loading:true; revoke held
  await revoke.dispatchEvent('click') // re-confirm → revoke() → guard drops it
  await revoke.dispatchEvent('click')

  await revokeP
  expect(faults.count('revoke', 'POST'), 'a re-entrant Revoke must not double-POST').toBe(1)
})
