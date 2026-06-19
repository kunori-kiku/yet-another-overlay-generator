import { test, expect } from '@playwright/test'
import { readHarness, localhostURL } from './fixtures/harness'
import { seedAndGotoController, loginAsOperator, logoutViaUserMenu } from './fixtures/panel'
import { addVirtualAuthenticator } from './fixtures/virtualAuthenticator'
import { totpNow } from './fixtures/totp'
import { OPERATOR_USER, OPERATOR_PASS } from './fixtures/config'

// Login matrix — WebAuthn + TOTP legs (plan-14 Phase 1.3/1.5/1.6). These run on the
// keystone-ON controller boot (its own tenant). The keystone spec runs first (alphabetical
// file order) and pins only a SIGNING credential — independent of these LOGIN factors. The
// virtual authenticator survives a same-document logout (a React re-render, not a navigation),
// so create() and the later get() share one document.
//
// Ordering within this file matters (no per-test FileStore reset): the login-passkey test runs
// FIRST and REMOVES its passkey, so the operator account is password-only for the TOTP test
// that follows; the TOTP test runs LAST and may leave TOTP enabled (no later spec logs into the
// keystone-ON tenant in the same run, and the FileStore is fresh next run).

// Serial mode makes the in-file ordering robust (and a failure skips the rest rather than
// cascading on a leftover factor): the passkey test must remove its passkey before the TOTP
// test's password login, and the TOTP test runs last.
test.describe.configure({ mode: 'serial' })

function keystoneOnTarget() {
  const h = readHarness()
  return {
    panel: localhostURL(h.controllerOn.panel),
    agent: localhostURL(h.controllerOn.agent),
  }
}

// waitForNextTotpWindow sleeps just past the next 30s TOTP boundary so the next code differs
// from any already-consumed one (the controller's TOTP replay watermark rejects a re-used step).
async function waitForNextTotpWindow(page: import('@playwright/test').Page): Promise<void> {
  await page.waitForTimeout(30_000 - (Date.now() % 30_000) + 2_000)
}

test('login passkey: register, then sign in passwordless', async ({ page, context }) => {
  test.setTimeout(90_000)
  const target = keystoneOnTarget()
  await addVirtualAuthenticator(page)
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  // Register a LOGIN passkey (create() via the virtual authenticator). No page.goto after this
  // until the passwordless get(), so the credential survives (logout is a same-document re-render).
  await page.goto(`${target.panel}/security`)
  await page.getByRole('button', { name: 'Register a login passkey' }).click()
  await expect(page.getByText('A login passkey is registered')).toBeVisible({ timeout: 20_000 })

  // (1.5) Log out (re-render) and sign in PASSWORDLESS with the passkey (loginWithPasskey → get()).
  await logoutViaUserMenu(page)
  await page.locator('#login-username').fill(OPERATOR_USER)
  await page.getByRole('button', { name: /Sign in with passkey/ }).click()
  await expect(page.locator('#login-username')).toBeHidden({ timeout: 20_000 })
  await expect(page.getByRole('button', { name: 'Account' })).toBeVisible()

  // (1.6) password+passkey 2FA: log out, then a PASSWORD login auto-runs the passkey ceremony
  // (the account now has a passkey → the backend returns passkey_required → store.login completes
  // the assertion via the authenticator without a second click).
  await logoutViaUserMenu(page)
  await page.locator('#login-username').fill(OPERATOR_USER)
  await page.locator('#login-password').fill(OPERATOR_PASS)
  await page.locator('form button[type="submit"]').click()
  await expect(page.locator('#login-username')).toBeHidden({ timeout: 20_000 })
  await expect(page.getByRole('button', { name: 'Account' })).toBeVisible()

  // Cleanup: remove the login passkey (removal requires a fresh assertion — get() again) so the
  // account is left password-only for the TOTP test.
  await page.goto(`${target.panel}/security`)
  await page.getByRole('button', { name: 'Remove passkey' }).click()
  await expect(page.getByText('Register a login passkey')).toBeVisible({ timeout: 20_000 })
})

test('TOTP 2FA: enroll a code factor, then complete a password+code login', async ({ page, context }) => {
  test.setTimeout(90_000)
  const target = keystoneOnTarget()
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  // Enable TOTP; capture the freshly-minted secret from the enroll response.
  await page.goto(`${target.panel}/security`)
  const enrollP = page.waitForResponse(
    (r) => r.url().includes('totp/enroll') && r.request().method() === 'POST',
    { timeout: 20_000 },
  )
  await page.getByRole('button', { name: 'Enable two-factor' }).click()
  const secret = ((await (await enrollP).json()) as { secret: string }).secret
  expect(secret, 'enroll response carries the base32 secret').toBeTruthy()

  // Confirm with an in-test RFC-6238 code (the same derivation the Go side verifies).
  await page.getByPlaceholder('123456').fill(totpNow(secret))
  await page.getByRole('button', { name: 'Confirm & enable' }).click()
  await expect(page.getByText('Two-factor is enabled')).toBeVisible({ timeout: 15_000 })

  // The confirm consumed this window's code; wait for the next window so the LOGIN code is fresh
  // (the controller rejects a replayed TOTP step).
  await waitForNextTotpWindow(page)

  // Log out, then log in with password → totp_required surfaces #login-totp → a fresh code completes it.
  await logoutViaUserMenu(page)
  await page.locator('#login-username').fill(OPERATOR_USER)
  await page.locator('#login-password').fill(OPERATOR_PASS)
  await page.locator('form button[type="submit"]').click()
  await expect(page.locator('#login-totp')).toBeVisible({ timeout: 15_000 })

  // Negative: a WRONG code is rejected and re-prompts (totpNotAccepted) — it does not advance the
  // replay watermark, so the correct code in the same window still completes login below.
  await page.locator('#login-totp').fill('000000')
  await page.locator('form button[type="submit"]').click()
  await expect(page.getByRole('alert')).toBeVisible({ timeout: 15_000 })
  await expect(page.locator('#login-totp')).toBeVisible()

  await page.locator('#login-totp').fill(totpNow(secret))
  await page.locator('form button[type="submit"]').click()
  await expect(page.locator('#login-username')).toBeHidden({ timeout: 15_000 })
  await expect(page.getByRole('button', { name: 'Account' })).toBeVisible()
})

test('passwordless passkey login for an unregistered username is rejected', async ({ page, context }) => {
  test.setTimeout(60_000)
  const target = keystoneOnTarget()
  // A virtual authenticator is present but holds NO credential for this username, so the decoy
  // assertion the server issues finds no match and the login is refused (noPasskeyRegistered).
  await addVirtualAuthenticator(page)
  await seedAndGotoController(page, context, target)

  await page.locator('#login-username').fill('ghost-operator-not-registered')
  await page.getByRole('button', { name: /Sign in with passkey/ }).click()
  // The error banner surfaces and the login gate stays closed (still on the login page).
  await expect(page.getByRole('alert')).toBeVisible({ timeout: 20_000 })
  await expect(page.locator('#login-username')).toBeVisible()
})
