import { test, expect, type Page } from '@playwright/test'
import { readHarness, httpURL } from './fixtures/harness'
import { seedLocalMode, seedCanvasTopology } from './fixtures/seedStore'

// Link-direction UX (plan-2 of link-directionality, D11): the EdgeEditor's direction select,
// the single-linked canvas chip, the explicit "to(A)" edge FLIP (swap from/to, mirror pins,
// clear stale dial fields, prefill the new target's public host), and the loud in-browser
// validation for a single-linked edge without a dial host. Runs on the air-gap boot in local
// mode — validation is the in-browser TS validator, no server round-trip.
//
// Locators are data-testid only (project lesson: color/class locators broke on the theme
// refactor): edge-label-<id> (the label pill, an equivalent selection target to the edge path),
// link-direction-select, edge-direction-chip, reverse-dial-readout.

// Two PUBLIC routers so the direction semantics are non-trivial (without the feature the
// auto-reverse would dial alpha's public endpoint — the race). Pins seeded so the flip's
// mirroring is observable in the persisted store.
const dirTopology = {
  project: { id: 'e2e-linkdir', name: 'E2E Link Direction' },
  domains: [
    { id: 'domain-1', name: 'net', cidr: '10.62.0.0/24', allocation_mode: 'auto', routing_mode: 'babel' },
  ],
  nodes: [
    {
      id: 'node-a', name: 'alpha', role: 'router', domain_id: 'domain-1',
      capabilities: { can_accept_inbound: true, can_forward: true, has_public_ip: true },
      public_endpoints: [{ id: 'a-ep', host: 'a.example', port: 51820 }],
    },
    {
      id: 'node-b', name: 'beta', role: 'router', domain_id: 'domain-1',
      capabilities: { can_accept_inbound: true, can_forward: true, has_public_ip: true },
      public_endpoints: [{ id: 'b-ep', host: 'b.example', port: 51820 }],
    },
  ],
  edges: [
    {
      id: 'e-dir', from_node_id: 'node-a', to_node_id: 'node-b', type: 'public-endpoint',
      endpoint_host: 'accel.example', transport: 'udp', is_enabled: true,
      pinned_from_port: 51820, pinned_to_port: 51821,
      pinned_from_transit_ip: '10.10.0.1', pinned_to_transit_ip: '10.10.0.2',
      pinned_from_link_local: 'fe80::1', pinned_to_link_local: 'fe80::2',
    },
  ],
}

// readEdge pulls the persisted edge back out of the zustand topology-storage envelope — the
// store IS the wire truth (what export/compile/deploy would see), so asserting on it pins the
// actual data effect of each UI action, not a rendering detail.
async function readEdge(page: Page): Promise<Record<string, unknown>> {
  return page.evaluate(() => {
    const raw = localStorage.getItem('topology-storage')
    if (!raw) throw new Error('topology-storage missing')
    return JSON.parse(raw).state.edges[0]
  })
}

async function openEdgeEditor(page: Page, panel: string): Promise<void> {
  await page.goto(`${panel}/design`)
  const pill = page.getByTestId('edge-label-e-dir')
  await expect(pill).toBeVisible({ timeout: 15_000 })
  await pill.click()
  await expect(page.getByTestId('link-direction-select')).toBeVisible({ timeout: 10_000 })
}

test('direction select: both shows the reverse-dial readout; forward persists + chips the canvas; flip redraws the edge', async ({
  page,
  context,
}) => {
  const h = readHarness()
  const panel = httpURL(h.airgap.panel)
  await seedLocalMode(context)
  await seedCanvasTopology(context, dirTopology)
  await openEdgeEditor(page, panel)

  const select = page.getByTestId('link-direction-select')
  await expect(select).toHaveValue('both')
  // Both-mode readout: the reverse dial resolves from alpha's node endpoint (no reverse edge).
  await expect(page.getByTestId('reverse-dial-readout')).toContainText('a.example')
  // Doubly-linked edges carry no direction chip (zero cosmetic churn for existing designs).
  await expect(page.getByTestId('edge-direction-chip')).toHaveCount(0)

  // forward: persists on the edge and chips the canvas.
  await select.selectOption('forward')
  await expect(page.getByTestId('edge-direction-chip')).toBeVisible()
  await expect.poll(async () => (await readEdge(page)).link_direction).toBe('forward')

  // flip ("to(A)" = single-link toward alpha): one atomic redraw — from/to swap, the pin pairs
  // mirror (each node keeps its own allocated values), the stale dial fields clear, and the new
  // target's (alpha's) public host prefills.
  await select.selectOption('flip')
  await expect.poll(async () => (await readEdge(page)).from_node_id).toBe('node-b')
  const flipped = await readEdge(page)
  expect(flipped.to_node_id).toBe('node-a')
  expect(flipped.link_direction).toBe('forward')
  expect(flipped.endpoint_host).toBe('a.example') // prefilled from alpha's public endpoint
  expect(flipped.pinned_from_port).toBe(51821) // beta's port followed beta to the from side
  expect(flipped.pinned_to_port).toBe(51820) // alpha's port followed alpha to the to side
  expect(flipped.pinned_from_transit_ip).toBe('10.10.0.2')
  expect(flipped.pinned_to_transit_ip).toBe('10.10.0.1')
  expect(flipped.compiled_port).toBeUndefined()
  // The canvas pill re-labels in the new drawn (= dial) direction; still single-linked.
  await expect(page.getByTestId('edge-label-e-dir')).toContainText('beta → alpha')
  await expect(page.getByTestId('edge-direction-chip')).toBeVisible()
})

test('a single-linked edge without a dial host warns inline and fails Validate loudly', async ({
  page,
  context,
}) => {
  const h = readHarness()
  const panel = httpURL(h.airgap.panel)
  await seedLocalMode(context)
  await seedCanvasTopology(context, dirTopology)
  await openEdgeEditor(page, panel)

  await page.getByTestId('link-direction-select').selectOption('forward')
  // Clear the dial host via the editor's manual host input (the require-explicit-host field).
  const hostInput = page.getByPlaceholder(/IP or hostname|IP 或主机名/)
  await hostInput.fill('')
  // Inline warning appears immediately (early feedback before Validate).
  await expect(page.getByText(/single-linked edge needs an endpoint host/i)).toBeVisible()

  // The in-browser validator rejects it with the coded error (loud, not a silent dead link).
  await page.getByRole('button', { name: /validate/i }).click()
  await expect(
    page.getByText(/sets link_direction forward but has no endpoint_host/),
  ).toBeVisible({ timeout: 10_000 })
})
