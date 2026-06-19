import { test, expect } from '@playwright/test'
import { readHarness, httpURL } from '../fixtures/harness'
import { seedControllerMode, seedTheme } from '../fixtures/seedStore'
import { loginAsOperator } from '../fixtures/panel'
import { isDesktopProject, isPhoneProject } from './responsive'

// snapshots.spec.ts (plan-17 / 3.5, Phase 5) — the visual-regression corpus. Pins the rendered CHROME
// of the DATA-INDEPENDENT operator surfaces × {phone, desktop} (the project sets the viewport) ×
// {light, dark} so a later CSS edit that breaks the mobile drawer or re-cramps a surface produces a
// pixel diff.
//
// SCOPE (determinism, not a compromise): the corpus pins LOGIN + SETTINGS — the surfaces whose render
// does NOT depend on the controller's mutable registry. The data-bearing surfaces
// (Overview/Fleet/Deploy/Security) are deliberately EXCLUDED from the pixel corpus: this suite shares
// one controller boot with the enrolling behavior specs, which seed uniquely-named (timestamped)
// nodes, so those surfaces' content is non-deterministic IN-SUITE and a pixel baseline of them would
// flake. Their RESPONSIVE LAYOUT is already pinned deterministically by the behavior smokes
// (fleet-table-reflow / overview-grid / page-padding-overflow / design-route) — the pixel corpus
// complements them on the stable-chrome surfaces, it does not duplicate them. See 3.5-findings.md.
//
// Load-bearing ordering: theme + controller-mode are seeded via addInitScript BEFORE navigation (so
// the anti-FOUC script paints the right theme on first frame — no ThemeProvider race), then login,
// then per-surface settle on a stable landmark, then toHaveScreenshot. Runs on desktop + phone only.
//
// Baselines are authoritative on Linux CI; regenerate with `--update-snapshots` and review the diff
// (see e2e/README.md). The CI visual step is non-blocking until the baselines pass a determinism run.

const SURFACES = [{ path: '/settings', landmark: 'settings' }] as const

for (const theme of ['light', 'dark'] as const) {
  test(`visual corpus (${theme})`, async ({ page, context }, testInfo) => {
    // Snapshots are {phone, desktop} only.
    test.skip(!isDesktopProject(testInfo) && !isPhoneProject(testInfo), 'snapshots: desktop + phone only')
    test.setTimeout(90_000)
    const h = readHarness()
    const panel = httpURL(h.controller.panel)
    page.on('dialog', (d) => void d.accept())

    // Seed theme + controller mode BEFORE any navigation (single addInitScript pass each).
    await seedTheme(context, theme)
    await seedControllerMode(context, { baseURL: panel, agentBaseURL: httpURL(h.controller.agent) })

    // Login surface first (captured BEFORE authenticating).
    await page.goto(`${panel}/`)
    await expect(page.locator('#login-username')).toBeVisible({ timeout: 15_000 })
    await expect(page).toHaveScreenshot(`login-${theme}.png`)

    await loginAsOperator(page)

    for (const surface of SURFACES) {
      await page.goto(`${panel}${surface.path}`)
      // Every authed surface carries the account menu in the Topbar — a stable settle landmark.
      await expect(page.getByRole('button', { name: 'Account' })).toBeVisible({ timeout: 15_000 })
      await expect(page).toHaveScreenshot(`${surface.landmark}-${theme}.png`)
    }
  })
}
