import { expect, type Page, type BrowserContext } from '@playwright/test'
import { readHarness, httpURL, type HarnessState } from './harness'
import { seedControllerMode } from './seedStore'
import { OPERATOR_USER, OPERATOR_PASS } from './config'

// panel.ts — shared operator-journey actions on plan-13's harness, reused across the plan-14
// specs (login / session / deploy / export-import / revoke). Specs drive the UI + read
// localStorage only; nothing here imports controllerStore/controllerClient internals.

export interface ControllerTarget {
  panel: string // http://host:port of the operator/panel mux
  agent: string // http://host:port of the agent mux
}

// keystoneOffTarget is plan-13's default controller boot — NO operator credential is ever
// pinned on it, so selectServerOperatorPinned() stays false (the keystone-OFF deploy branch).
export function keystoneOffTarget(h: HarnessState = readHarness()): ControllerTarget {
  return { panel: httpURL(h.controller.panel), agent: httpURL(h.controller.agent) }
}

// seedAndGotoController seeds controller mode pointed at a target boot and navigates to it,
// landing on the LoginPage (the controller-mode gate).
export async function seedAndGotoController(
  page: Page,
  context: BrowserContext,
  target: ControllerTarget,
): Promise<void> {
  await seedControllerMode(context, { baseURL: target.panel, agentBaseURL: target.agent })
  await page.goto(`${target.panel}/`)
}

// loginAsOperator fills + submits the password login form and waits for the Shell to replace
// the LoginPage (the username field detaches on a successful login).
export async function loginAsOperator(
  page: Page,
  user: string = OPERATOR_USER,
  pass: string = OPERATOR_PASS,
): Promise<void> {
  await page.locator('#login-username').fill(user)
  await page.locator('#login-password').fill(pass)
  await page.locator('form button[type="submit"]').click()
  await expect(page.locator('#login-username')).toBeHidden({ timeout: 15_000 })
}

// logoutViaUserMenu opens the top-right account menu and signs out, waiting for the LoginPage
// to return (UserMenu.tsx: "Account" trigger → "Sign out").
export async function logoutViaUserMenu(page: Page): Promise<void> {
  await page.getByRole('button', { name: 'Account' }).click()
  await page.getByRole('button', { name: 'Sign out' }).click()
  await expect(page.locator('#login-username')).toBeVisible({ timeout: 15_000 })
}
