// @vitest-environment node

import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import {
  HistoryChartFamilySection,
  type HistoryChartFamilySectionProps,
} from './NodeResourceHistory';
import {
  HISTORY_CHART_FAMILIES,
  configuredHistoryProbeSelector,
  createLatestRequestCoordinator,
  createObservedRequestScheduler,
  historyRefreshFailed,
  historyRefreshIdle,
  historyRefreshSucceeded,
  initialHistoryRefreshViewState,
  type HistoryChartFamily,
  type NodeHistory,
} from '../../lib/telemetryHistory';
import type { TelemetryProbe } from '../../types/topology';

const RESOURCE_HISTORY: NodeHistory = {
  step: '30s',
  disabled: false,
  buckets: [{
    t: '2026-07-16T10:00:00Z',
    cpuPct: { avg: 25, min: 20, max: 30 },
    load1: { avg: 0.1 },
    load5: { avg: 0.08 },
    load15: { avg: 0.05 },
    memUsedPct: { avg: 50, min: 48, max: 52 },
  }],
  probes: [],
  devices: [],
};

const PROBE_HISTORY: NodeHistory = {
  step: '30s',
  disabled: false,
  buckets: [],
  probes: [{
    seriesId: 'a'.repeat(64),
    id: 'edge',
    type: 'tcp',
    host: 'edge.example.test',
    port: 443,
    intervalMS: 30_000,
    buckets: [{
      t: '2026-07-16T10:00:00Z',
      attempts: 1,
      successes: 1,
      failures: 0,
      latencyMS: { avg: 12, min: 12, max: 12 },
      failureReasons: {},
    }],
  }],
  devices: [],
};

const DEVICE_ID = 'd'.repeat(64);
const DEVICE_HISTORY: NodeHistory = {
  step: '30s',
  disabled: false,
  buckets: [],
  probes: [],
  devices: [{
    seriesId: 'e'.repeat(64),
    deviceId: DEVICE_ID,
    kind: 'block_device',
    buckets: [{
      t: '2026-07-16T10:00:00Z',
      metrics: {
        disk_read_bytes_per_second: { avg: 1024, min: 0, max: 2048 },
        disk_write_bytes_per_second: { avg: 512 },
        disk_io_busy_pct: { avg: 25 },
      },
    }],
  }],
};

const COMMON_PROPS = {
  stepMs: 30_000,
  xDomain: [Date.parse('2026-07-16T09:59:30Z'), Date.parse('2026-07-16T10:00:30Z')],
  language: 'en',
  selectedProbeID: 'edge',
  onSelectProbeID: () => undefined,
  deviceTelemetryEnabled: false,
  selectedDeviceID: null,
  onSelectDeviceID: () => undefined,
} as const;

const FAMILY_FIXTURES = {
  resource: {
    ...COMMON_PROPS,
    history: RESOURCE_HISTORY,
    configuredProbes: [],
  },
  probe: {
    ...COMMON_PROPS,
    history: PROBE_HISTORY,
    configuredProbes: [{ id: 'edge', name: 'Edge HTTPS', type: 'tcp', host: 'edge.example.test', port: 443 }],
  },
  device: {
    ...COMMON_PROPS,
    history: DEVICE_HISTORY,
    configuredProbes: [],
    deviceTelemetryEnabled: true,
    selectedDeviceID: DEVICE_ID,
    deviceInventory: {
      devices: [{
        seriesId: DEVICE_ID,
        kind: 'block_device',
        label: 'NVMe data disk',
        capacityBytes: 1024 * 1024,
        status: 'ok',
      }],
      truncated: 0,
    },
  },
} satisfies Record<HistoryChartFamily, HistoryChartFamilySectionProps>;

const EXPECTED_SERIES = {
  resource: 'timeseries-series-cpu',
  probe: 'timeseries-series-probe-latency',
  device: 'timeseries-series-disk_read_bytes_per_second',
} satisfies Record<HistoryChartFamily, string>;

describe('history chart-family renderer registry', () => {
  it('has a data-bearing fixture and an actual TimeSeriesChart for every declared family', () => {
    for (const family of HISTORY_CHART_FAMILIES) {
      const html = renderToStaticMarkup(createElement(HistoryChartFamilySection, {
        family,
        ...FAMILY_FIXTURES[family],
      }));

      expect(html, `${family} must render TimeSeriesChart`).toContain('data-testid="timeseries-chart"');
      expect(html, `${family} chart must expose an accessible image summary`).toContain('role="img"');
      expect(html, `${family} chart must expose its visible title as a name`).toContain('aria-label=');
      expect(html, `${family} chart must associate its numeric summary`).toContain('aria-describedby=');
      expect(html, `${family} must register a concrete plotted series`).toContain(
        `data-testid="${EXPECTED_SERIES[family]}"`,
      );
    }
  });

  it('summarizes the plotted values for non-visual chart readers', () => {
    const html = renderToStaticMarkup(createElement(HistoryChartFamilySection, {
      family: 'resource',
      ...FAMILY_FIXTURES.resource,
    }));

    expect(html).toContain('CPU: latest 25%; minimum 20%; maximum 30%.');
  });

  it('shows the friendly probe name while retaining the immutable id and exact destination', () => {
    const html = renderToStaticMarkup(createElement(HistoryChartFamilySection, {
      family: 'probe',
      ...FAMILY_FIXTURES.probe,
    }));

    expect(html).toContain('Edge HTTPS · edge · TCP · edge.example.test:443');
  });

  it('reuses latency and availability charts for URL mismatches without a status-code chart', () => {
    const history: NodeHistory = {
      step: '30s',
      disabled: false,
      buckets: [],
      probes: [{
        seriesId: 'f'.repeat(64),
        id: 'health',
        type: 'url',
        url: 'https://service.example/health',
        expectedStatus: 204,
        intervalMS: 30_000,
        buckets: [{
          t: '2026-07-16T10:00:00Z',
          attempts: 1,
          successes: 0,
          failures: 1,
          latencyMS: { avg: 18, min: 18, max: 18 },
          failureReasons: { unexpected_status: 1 },
        }],
      }],
      devices: [],
    };
    const html = renderToStaticMarkup(createElement(HistoryChartFamilySection, {
      family: 'probe',
      ...COMMON_PROPS,
      history,
      selectedProbeID: 'health',
      configuredProbes: [{
        id: 'health',
        name: 'Customer API',
        type: 'url',
        url: 'https://service.example/health',
        expected_status: 204,
      }],
    }));

    expect(html).toContain('Customer API · health · URL · https://service.example/health · 204');
    expect(html).toContain('data-testid="timeseries-series-probe-latency"');
    expect(html).toContain('data-testid="timeseries-series-probe-availability"');
    expect(html).toContain('Unexpected HTTP status');
    expect(html).not.toContain('status-code-chart');
    expect(html).not.toContain('Latest HTTP status');
  });

  it('reuses the same chart adapter for GPU utilization and VRAM history', () => {
    const gpuID = 'f'.repeat(64);
    const html = renderToStaticMarkup(createElement(HistoryChartFamilySection, {
      family: 'device',
      ...COMMON_PROPS,
      history: {
        step: '30s',
        disabled: false,
        buckets: [],
        probes: [],
        devices: [{
          seriesId: 'a'.repeat(64),
          deviceId: gpuID,
          kind: 'gpu',
          buckets: [{
            t: '2026-07-16T10:00:00Z',
            metrics: { gpu_utilization_pct: { avg: 40 }, gpu_vram_used_pct: { avg: 60 } },
          }],
        }],
      },
      configuredProbes: [],
      deviceTelemetryEnabled: true,
      selectedDeviceID: gpuID,
      deviceInventory: {
        devices: [{
          seriesId: gpuID,
          kind: 'gpu',
          label: 'NVIDIA accelerator',
          vendor: 'NVIDIA',
          status: 'ok',
        }],
        truncated: 0,
      },
    }));

    expect(html).toContain('data-testid="timeseries-series-gpu_utilization_pct"');
    expect(html).toContain('data-testid="timeseries-series-gpu_vram_used_pct"');
  });
});

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((res) => { resolve = res; });
  return { promise, resolve };
}

async function flushMicrotasks() {
  for (let i = 0; i < 8; i++) await Promise.resolve();
}

describe('node history component request/state seams', () => {
  interface Query {
    requestKey: string;
    observationKey: string;
    seen: string;
  }

  it('omits a malformed nonblank probe draft so resource and device history remain loadable', () => {
    expect(configuredHistoryProbeSelector({
      id: 'draft', type: 'icmp', host: 'https://not-a-bare-host.example/path',
    })).toBeUndefined();
    expect(configuredHistoryProbeSelector({
      id: 'ready', type: 'tcp', host: 'resolver.example.', port: 53,
    })).toEqual({ id: 'ready', type: 'tcp', host: 'resolver.example.', port: 53 });
    expect(configuredHistoryProbeSelector({
      id: 'bad id', type: 'icmp', host: 'resolver.example.',
    })).toBeUndefined();
    expect(configuredHistoryProbeSelector({
      id: 'bad-url', type: 'url', url: 'https://service.example/%zz',
    })).toBeUndefined();
    expect(configuredHistoryProbeSelector({
      id: 'future', type: 'future', host: 'resolver.example.',
    } as unknown as TelemetryProbe)).toBeUndefined();
  });

  it('does not request again when this node lastSeen is unchanged', () => {
    const request = vi.fn();
    const dispose = vi.fn();
    const scheduler = createObservedRequestScheduler<Query>({
      observationKey: (query) => query.observationKey,
      request,
      dispose,
    });
    const first = { requestKey: 'node/range/probe', observationKey: 'node/range/probe/seen-1', seen: 'seen-1' };
    expect(scheduler.observe(first)).toBe(true);
    expect(scheduler.observe({ ...first })).toBe(false);
    expect(request).toHaveBeenCalledTimes(1);
    scheduler.dispose();
    expect(dispose).toHaveBeenCalledOnce();
  });

  it('allows an explicit Retry observation even when node lastSeen is unchanged', () => {
    const request = vi.fn();
    const scheduler = createObservedRequestScheduler<Query>({
      observationKey: (query) => query.observationKey,
      request,
      dispose: () => undefined,
    });
    const base = { requestKey: 'node/range/probe', seen: 'seen-1' };

    expect(scheduler.observe({ ...base, observationKey: 'node/range/probe/seen-1/retry-0' })).toBe(true);
    expect(scheduler.observe({ ...base, observationKey: 'node/range/probe/seen-1/retry-0' })).toBe(false);
    expect(scheduler.observe({ ...base, observationKey: 'node/range/probe/seen-1/retry-1' })).toBe(true);
    expect(request).toHaveBeenCalledTimes(2);
  });

  it('keeps one in flight and coalesces advancing lastSeen to the latest follow-up', async () => {
    const first = deferred<string>();
    const latest = deferred<string>();
    const executed: string[] = [];
    const coordinator = createLatestRequestCoordinator<Query, string>({
      key: (query) => query.requestKey,
      execute: (query) => {
        executed.push(query.seen);
        return query.seen === 'seen-1' ? first.promise : latest.promise;
      },
      onSuccess: () => undefined,
      onError: () => undefined,
    });
    const scheduler = createObservedRequestScheduler<Query>({
      observationKey: (query) => query.observationKey,
      request: coordinator.request,
      dispose: coordinator.dispose,
    });
    const query = (seen: string): Query => ({
      requestKey: 'node/range/probe',
      observationKey: `node/range/probe/${seen}`,
      seen,
    });

    scheduler.observe(query('seen-1'));
    await flushMicrotasks();
    scheduler.observe(query('seen-2'));
    scheduler.observe(query('seen-3'));
    expect(executed).toEqual(['seen-1']);
    first.resolve('first');
    await flushMicrotasks();
    expect(executed).toEqual(['seen-1', 'seen-3']);
    latest.resolve('latest');
    await flushMicrotasks();
  });

  it('aborts component-local work on unmount/dispose and suppresses late callbacks', async () => {
    const work = deferred<string>();
    let signal: AbortSignal | undefined;
    const success = vi.fn();
    const error = vi.fn();
    const coordinator = createLatestRequestCoordinator<Query, string>({
      key: (query) => query.requestKey,
      execute: (_query, requestSignal) => {
        signal = requestSignal;
        return work.promise;
      },
      onSuccess: success,
      onError: error,
    });
    const scheduler = createObservedRequestScheduler<Query>({
      observationKey: (query) => query.observationKey,
      request: coordinator.request,
      dispose: coordinator.dispose,
    });
    scheduler.observe({ requestKey: 'key', observationKey: 'key/seen', seen: 'seen' });
    await flushMicrotasks();
    scheduler.dispose();
    expect(signal?.aborted).toBe(true);
    work.resolve('late');
    await flushMicrotasks();
    expect(success).not.toHaveBeenCalled();
    expect(error).not.toHaveBeenCalled();
  });

  it('preserves the complete last-good chart state on a transient refresh failure', () => {
    const window: [number, number] = [100, 200];
    const succeeded = historyRefreshSucceeded(
      initialHistoryRefreshViewState(),
      RESOURCE_HISTORY,
      window,
      1234,
    );
    const failed = historyRefreshIdle(historyRefreshFailed(succeeded));
    expect(failed).toEqual({
      history: RESOURCE_HISTORY,
      window,
      updating: false,
      error: true,
      lastUpdatedAt: 1234,
    });
  });
});
