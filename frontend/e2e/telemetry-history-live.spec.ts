import { test, expect, type Route } from '@playwright/test'
import { readHarness } from './fixtures/harness'
import {
  keystoneOffTarget,
  loginAsOperator,
  seedAndGotoController,
} from './fixtures/panel'
import { routeOf } from './adversarial/faults'

const NODE_ID = 'telemetry-e2e-node'
const FIRST_PROBE = {
  id: 'edge-a',
  type: 'tcp',
  host: 'alpha.example.test',
  port: 443,
  interval_seconds: 30,
} as const
const SECOND_PROBE = {
  id: 'edge-b',
  type: 'icmp',
  host: 'dns.example.test',
  interval_seconds: 60,
} as const

const topology = {
  project: { id: 'telemetry-e2e', name: 'Telemetry E2E' },
  domains: [{
    id: 'telemetry-domain',
    name: 'telemetry',
    cidr: '10.77.0.0/24',
    allocation_mode: 'auto',
    routing_mode: 'babel',
  }],
  nodes: [{
    id: NODE_ID,
    name: 'Telemetry node',
    role: 'peer',
    domain_id: 'telemetry-domain',
    capabilities: { can_accept_inbound: false, can_forward: false, has_public_ip: false },
    telemetry_probes: [FIRST_PROBE, SECOND_PROBE],
  }],
  edges: [],
}

interface ProbeRequest {
  id: string
  type: string
  host: string
  port: string | null
}

function nodeWire(lastSeen: string) {
  return {
    node_id: NODE_ID,
    status: 'approved',
    has_wg_public_key: true,
    desired_generation: 1,
    applied_generation: 1,
    last_checksum: 'telemetry-e2e-checksum',
    last_health: 'ok',
    agent_version: 'v2.0.0-rc.10',
    last_seen: lastSeen,
    enrolled_at: '2026-07-16T00:00:00Z',
    rekey_requested: false,
    telemetry: {
      resource: {
        cpu_pct: 12,
        load1: 0.1,
        load5: 0.08,
        load15: 0.05,
        mem_total_kb: 1024,
        mem_available_kb: 512,
      },
    },
  }
}

function historyWire(url: URL) {
  const id = url.searchParams.get('probe_id') ?? ''
  const isSecond = id === SECOND_PROBE.id
  const to = Date.parse(url.searchParams.get('to') ?? '')
  const anchor = Number.isFinite(to) ? to : Date.now()
  const at = (offsetMS: number) => new Date(anchor - offsetMS).toISOString()
  const descriptor = isSecond
    ? { series_id: 'b'.repeat(64), ...SECOND_PROBE, interval_ms: 60_000 }
    : { series_id: 'a'.repeat(64), ...FIRST_PROBE, interval_ms: 30_000 }
  const failureReason = isSecond ? 'dns_failed' : 'timeout'
  const failures = isSecond ? 2 : 1
  const attempts = failures + 1

  return {
    step: '30s',
    disabled: false,
    buckets: [120_000, 90_000, 60_000].map((offsetMS, index) => ({
      t: at(offsetMS),
      cpu_pct: { avg: 10 + index, min: 9 + index, max: 11 + index },
      load1: { avg: 0.1 + index * 0.01, min: 0.1, max: 0.12 },
      load5: { avg: 0.08, min: 0.07, max: 0.09 },
      load15: { avg: 0.05, min: 0.04, max: 0.06 },
      mem_used_pct: { avg: 50 + index, min: 49, max: 52 },
    })),
    probes: [{
      ...descriptor,
      buckets: [{
        t: at(60_000),
        attempts,
        successes: 1,
        failures,
        interval_ms: descriptor.interval_ms,
        latency_ms: { avg: isSecond ? 25 : 12, min: isSecond ? 24 : 11, max: isSecond ? 26 : 13 },
        failure_reasons: { [failureReason]: failures },
      }],
    }],
  }
}

test('node-detail telemetry charts, exact probe selection, Live feedback, and last-good failure UX', async ({
  page,
  context,
}) => {
  const target = keystoneOffTarget(readHarness())
  let lastSeen = '2026-07-16T10:00:00Z'
  let failNextHistory = false
  let nodeReads = 0
  const probeRequests: ProbeRequest[] = []
  const historySteps: Array<string | null> = []

  await page.route('**/api/v1/operator/**', async (route: Route) => {
    const request = route.request()
    const apiRoute = routeOf(request.url())
    if (request.method() === 'GET' && apiRoute === 'nodes') {
      nodeReads++
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([nodeWire(lastSeen)]),
      })
      return
    }
    if (request.method() === 'GET' && apiRoute === 'topology') {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(topology),
      })
      return
    }
    if (request.method() === 'GET' && apiRoute === 'node-history') {
      const url = new URL(request.url())
      historySteps.push(url.searchParams.get('step'))
      probeRequests.push({
        id: url.searchParams.get('probe_id') ?? '',
        type: url.searchParams.get('probe_type') ?? '',
        host: url.searchParams.get('probe_host') ?? '',
        port: url.searchParams.get('probe_port'),
      })
      if (failNextHistory) {
        failNextHistory = false
        await route.fulfill({
          status: 503,
          contentType: 'application/json',
          body: JSON.stringify({ error: { code: 'injected_history_failure', message: 'injected history failure' } }),
        })
        return
      }
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          ...historyWire(url),
          step: url.searchParams.get('step') === '5m' ? '30m0s' : '30s',
        }),
      })
      return
    }
    await route.continue()
  })

  await seedAndGotoController(page, context, target)
  await loginAsOperator(page)
  await page.goto(`${target.panel}/fleet/nodes/${NODE_ID}`)

  const historyCard = page.getByTestId('node-resource-history')
  await expect(historyCard).toBeVisible({ timeout: 15_000 })
  await expect(historyCard.getByTestId('history-resolution-hint')).toContainText(
    'Choosing a coarser resolution can reduce the number of transferred points',
  )
  await expect.poll(() => probeRequests.at(-1)).toEqual({
    id: FIRST_PROBE.id,
    type: FIRST_PROBE.type,
    host: FIRST_PROBE.host,
    port: String(FIRST_PROBE.port),
  })
  await expect(historyCard.getByTestId('timeseries-series-probe-latency')).toBeVisible()
  await expect(historyCard.getByText('Timed out', { exact: true })).toBeVisible()

  const probeSelect = historyCard.getByTestId('history-probe-select')
  await probeSelect.selectOption(SECOND_PROBE.id)
  await expect.poll(() => probeRequests.at(-1)).toEqual({
    id: SECOND_PROBE.id,
    type: SECOND_PROBE.type,
    host: SECOND_PROBE.host,
    port: null,
  })
  await expect(probeSelect).toHaveValue(SECOND_PROBE.id)
  await expect(historyCard.getByText('DNS lookup failed', { exact: true })).toBeVisible()
  await expect(historyCard.getByTestId('probe-history-failures').getByText('2', { exact: true })).toBeVisible()

  const resolutionSelect = historyCard.getByTestId('history-granularity')
  await expect(resolutionSelect).toHaveAccessibleDescription(
    /Auto follows the node's reporting cadence.*coarser resolution can reduce/i,
  )
  await resolutionSelect.selectOption('5m')
  await expect.poll(() => historySteps.at(-1)).toBe('5m')
  await expect(historyCard.getByTestId('history-effective-resolution')).toContainText(
    'Effective resolution: 30m (widened from 5m',
  )

  const health = page.getByTestId('fleet-refresh-visible-health')
  const liveToggle = page.getByTestId('fleet-live-toggle')
  await expect(health).toContainText('Live off')
  const nodeReadsBeforeLive = nodeReads
  await liveToggle.check()
  await expect.poll(() => nodeReads).toBeGreaterThan(nodeReadsBeforeLive)
  await expect(health).toContainText('Live on')
  // Stop the timer after proving the visible Live state; the failure leg below is then driven by one
  // deterministic manual Fleet refresh instead of racing a ten-second background tick.
  await liveToggle.uncheck()

  const chartCount = await historyCard.getByTestId('timeseries-chart').count()
  lastSeen = '2026-07-16T10:00:10Z'
  failNextHistory = true
  await page.getByTestId('node-detail-refresh').click()
  await expect(historyCard.getByTestId('history-update-failed')).toBeVisible({ timeout: 15_000 })

  // A transient history error must retain the selected probe's complete last-good view instead of
  // blanking the card or reverting to the first configured destination.
  await expect(historyCard.getByTestId('timeseries-chart')).toHaveCount(chartCount)
  await expect(probeSelect).toHaveValue(SECOND_PROBE.id)
  await expect(historyCard.getByText('DNS lookup failed', { exact: true })).toBeVisible()
  expect(probeRequests.at(-1)).toEqual({
    id: SECOND_PROBE.id,
    type: SECOND_PROBE.type,
    host: SECOND_PROBE.host,
    port: null,
  })
})
