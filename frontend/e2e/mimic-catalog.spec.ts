import { test, expect } from '@playwright/test'
import { seedAndGotoController, loginAsOperator, keystoneOffTarget } from './fixtures/panel'

// Mimic catalog two-package round-trip (mimic-provisioning-reliability plan-2). The rc.1 live-fleet
// finding was that a mimic (transport=tcp) deploy needs BOTH upstream packages — the userspace `mimic`
// .deb AND its `mimic-dkms` module companion — but the catalog could pin only one. This is the only
// DOM-level test of the two-package catalog UI: an operator pins a <codename>-<arch> row's mimic +
// mimic-dkms pair, the "companion required" warning clears when the dkms pin is added, and the
// companion SURVIVES the full save round-trip (settingsJSON.mimic_debs.dkms_* <-> ControllerSettings
// <-> store <-> GET). Locators use data-testid (project lesson: never a color/text locator) and
// .last() so the spec is robust to the added row's dynamic id (and to a Playwright retry).

const SHA = 'a'.repeat(64)
const DKMS_SHA = 'b'.repeat(64)
const MIMIC_ASSET = 'bookworm_mimic_0.7.1-1_amd64.deb'
const DKMS_ASSET = 'bookworm_mimic-dkms_0.7.1-1_amd64.deb'
const RELEASE_BASE = 'https://github.com/hack3ric/mimic/releases/latest/download'

test('mimic catalog: pin a mimic + mimic-dkms pair; the companion warning clears and it round-trips through save', async ({
  page,
  context,
}) => {
  const target = keystoneOffTarget()
  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)
  await page.goto(`${target.panel}/settings`)

  // The mimic catalog card renders once settings load (hasAuth + settings !== null).
  const addBtn = page.getByTestId('mimic-add-package')
  await expect(addBtn).toBeVisible({ timeout: 15_000 })
  // A release base is required to save a deb pin (the default is prefilled; set it explicitly so the
  // test does not depend on the shipped default).
  await page.getByPlaceholder(RELEASE_BASE).fill(RELEASE_BASE)
  await addBtn.click()

  const lastKey = page.locator('[data-testid^="mimic-key-"]').last()
  const lastAsset = page.locator('[data-testid^="mimic-asset-"]').last()
  const lastSha = page.locator('[data-testid^="mimic-sha-"]').last()
  const lastDkmsAsset = page.locator('[data-testid^="mimic-dkms-asset-"]').last()
  const lastDkmsSha = page.locator('[data-testid^="mimic-dkms-sha-"]').last()

  // Fill only the userspace mimic pin first: the non-blocking "companion required" warning surfaces.
  await lastKey.fill('bookworm-amd64')
  await lastAsset.fill(MIMIC_ASSET)
  await lastSha.fill(SHA)
  await expect(page.getByTestId('mimic-missing-dkms-warning')).toBeVisible()

  // Add the mimic-dkms companion -> the warning clears.
  await lastDkmsAsset.fill(DKMS_ASSET)
  await lastDkmsSha.fill(DKMS_SHA)
  await expect(page.getByTestId('mimic-missing-dkms-warning')).toBeHidden()

  await page.getByTestId('mimic-save-catalog').click()
  await expect(page.getByTestId('mimic-saved-notice')).toBeVisible({ timeout: 15_000 })

  // Reload: the persisted row rehydrates with BOTH packages — proving the dkms_* fields round-tripped
  // through the server (the server-authoritative full-replace settings contract).
  await page.goto(`${target.panel}/settings`)
  await expect(page.locator('[data-testid^="mimic-dkms-asset-"]').last()).toHaveValue(DKMS_ASSET, { timeout: 15_000 })
  await expect(page.locator('[data-testid^="mimic-dkms-sha-"]').last()).toHaveValue(DKMS_SHA)
  await expect(page.locator('[data-testid^="mimic-asset-"]').last()).toHaveValue(MIMIC_ASSET)
})
