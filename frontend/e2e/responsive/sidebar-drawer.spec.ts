import { test, expect } from '@playwright/test'
import { readHarness } from '../fixtures/harness'
import { seedAndGotoController, loginAsOperator, keystoneOffTarget } from '../fixtures/panel'
import { isDesktopProject } from './responsive'

// sidebar-drawer.spec.ts (plan-17 / 3.5, blocker 1) — the lg-boundary PAIR for the navigation
// sidebar, fanned across the device matrix. At >= lg the docked Sidebar (Shell.tsx `hidden lg:flex`)
// is shown and NO hamburger renders; below lg the docked sidebar is gone, the hamburger
// (`shell.openNav` = "Open navigation", `lg:hidden`) is shown, and tapping it opens the off-canvas
// nav Drawer (role="dialog" + aria-modal). Binds to ARIA (accessible name / dialog role), never a
// CSS class or testid. Fails on pre-Subject-2 main (no hamburger, no Drawer existed there).

test('navigation: docked sidebar at >= lg, hamburger-opened drawer below lg', async (
  { page, context },
  testInfo,
) => {
  const target = keystoneOffTarget(readHarness())
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)

  const hamburger = page.getByRole('button', { name: 'Open navigation' })

  if (isDesktopProject(testInfo)) {
    // >= lg: the docked sidebar carries the nav; no hamburger affordance.
    await expect(hamburger).toBeHidden()
    // A nav link is reachable without opening anything (docked sidebar visible).
    await expect(page.getByRole('link', { name: 'Fleet' })).toBeVisible()
  } else {
    // < lg: the hamburger is the only way to the nav; the docked sidebar is gone.
    await expect(hamburger).toBeVisible()
    // No nav Drawer is open yet.
    await expect(page.getByRole('dialog')).toBeHidden()
    await hamburger.tap()
    // Tapping opens the off-canvas nav Drawer (Subject 2's shared Drawer: role=dialog + aria-modal).
    const drawer = page.getByRole('dialog')
    await expect(drawer).toBeVisible()
    await expect(drawer.getByRole('link', { name: 'Fleet' })).toBeVisible()
  }
})
