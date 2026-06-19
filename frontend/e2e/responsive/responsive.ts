import { expect, type Page, type TestInfo } from '@playwright/test'

// responsive.ts — shared helpers for the plan-17 / 3.5 device-emulation smokes. The behavior specs
// fan out across the playwright.config device projects (desktop / phone / tablet) and branch on the
// project's side of the lg=1024 crossover. Everything here is DOM/ARIA/layout only — no spec imports
// controllerStore/controllerClient internals (same custody bar as plan-14/16).

// LG is the responsive crossover the whole suite pivots on (Subject 2's `lg` = 1024px). `desktop` is
// the ONLY project at/above it; `phone` (360) and `tablet` (768) are both below it (768 < 1024 → the
// mobile side, not "between").
export const LG = 1024

// isDesktopProject reports whether the current project is the >= lg (docked-layout) side. Specs use it
// to pick which half of the lg-boundary pair to assert.
export function isDesktopProject(testInfo: TestInfo): boolean {
  return testInfo.project.name === 'desktop'
}

// isPhoneProject reports the touch/mobile-UA narrow-edge project (the only one with hasTouch). Touch
// and tap-target specs run only here.
export function isPhoneProject(testInfo: TestInfo): boolean {
  return testInfo.project.name === 'phone'
}

// expectNoHorizontalPageOverflow asserts the document does not scroll horizontally (the no-overflow
// invariant at narrow widths). A 1px slack absorbs sub-pixel rounding.
export async function expectNoHorizontalPageOverflow(page: Page): Promise<void> {
  const { scrollW, clientW } = await page.evaluate(() => ({
    scrollW: document.documentElement.scrollWidth,
    clientW: document.documentElement.clientWidth,
  }))
  expect(scrollW, `no horizontal page overflow (scrollWidth ${scrollW} <= clientWidth ${clientW})`).toBeLessThanOrEqual(clientW + 1)
}

// gridTrackCount returns how many explicit column tracks a CSS grid renders (its computed
// grid-template-columns split into track sizes) — the deterministic way to assert "3-col vs 1-col"
// without measuring element geometry.
export async function gridTrackCount(page: Page, selector: string): Promise<number> {
  return page.locator(selector).first().evaluate((el) => {
    const cols = getComputedStyle(el).gridTemplateColumns
    return cols && cols !== 'none' ? cols.split(' ').filter(Boolean).length : 1
  })
}
