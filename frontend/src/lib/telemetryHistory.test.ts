// @vitest-environment node
//
// telemetryHistory.test.ts — pins the PURE history logic the node-detail charts depend on:
// the param→query wiring (range/granularity → from/to/step), the Go-duration math, the defensive
// wire→typed parse of the plan-3 response, and the gap-aware wire→series shaping. Dependency-free
// node-env unit test; rendered controls have separate SSR coverage.

import { describe, expect, it, vi } from 'vitest';
import {
  GRANULARITIES,
  HISTORY_CHART_FAMILIES,
  RANGE_PRESETS,
  RANGE_SECONDS,
  createLatestRequestCoordinator,
  formatHistoryResolution,
  granularityStep,
  historyQueryString,
  metricSeries,
  parseGoDuration,
  parseNodeHistory,
  probeAvailabilitySeries,
  probeHistoryFallbackIntervalMS,
  probeHistoryMatchesPolicy,
  probeLatencySeries,
  rangeWindow,
  resolutionWasWidened,
  summarizeProbeFailures,
  type HistoryBucket,
  type ProbeHistoryBucket,
} from './telemetryHistory';

describe('history chart families', () => {
  it('keeps the frontend parser authority explicit and ordered', () => {
    expect(HISTORY_CHART_FAMILIES).toEqual(['resource', 'probe']);
  });
});

describe('rangeWindow', () => {
  const now = Date.parse('2026-07-13T12:00:00.000Z');

  it('emits RFC3339 [from,to] spanning exactly the preset length', () => {
    const { from, to } = rangeWindow('6h', now);
    expect(to).toBe('2026-07-13T12:00:00.000Z');
    expect(from).toBe('2026-07-13T06:00:00.000Z');
    expect((Date.parse(to) - Date.parse(from)) / 1000).toBe(RANGE_SECONDS['6h']);
  });

  it('covers every preset with a distinct, correct span', () => {
    for (const p of RANGE_PRESETS) {
      const { from, to } = rangeWindow(p, now);
      expect((Date.parse(to) - Date.parse(from)) / 1000).toBe(RANGE_SECONDS[p]);
    }
  });
});

describe('granularityStep', () => {
  it('omits the step for auto (server picks) and passes the literal Go-duration otherwise', () => {
    expect(granularityStep('auto')).toBeUndefined();
    expect(granularityStep('30s')).toBe('30s');
    expect(granularityStep('5m')).toBe('5m');
    expect(granularityStep('30m')).toBe('30m');
    expect(granularityStep('1h')).toBe('1h');
  });

  it('every non-auto granularity is a parseable Go duration', () => {
    for (const g of GRANULARITIES) {
      const step = granularityStep(g);
      if (step === undefined) continue;
      expect(parseGoDuration(step)).toBeGreaterThan(0);
    }
  });

  it('recognizes server widening semantically and formats fractional cap steps compactly', () => {
    expect(resolutionWasWidened('30s', '20m10.811s')).toBe(true);
    expect(resolutionWasWidened('5m', '5m0s')).toBe(false);
    expect(resolutionWasWidened('auto', '20m10.811s')).toBe(false);
    expect(formatHistoryResolution('20m10.811s')).toBe('20m 11s');
    expect(formatHistoryResolution('1h0m0s')).toBe('1h');
  });
});

describe('historyQueryString', () => {
  it('includes node/from/to and encodes RFC3339 colons; omits step when absent', () => {
    const q = historyQueryString('node-a', '2026-07-13T06:00:00Z', '2026-07-13T12:00:00Z');
    const p = new URLSearchParams(q);
    expect(p.get('node')).toBe('node-a');
    expect(p.get('from')).toBe('2026-07-13T06:00:00Z');
    expect(p.get('to')).toBe('2026-07-13T12:00:00Z');
    expect(p.has('step')).toBe(false);
    expect(q).toContain('%3A'); // colons are percent-encoded on the wire
  });

  it('includes step when set', () => {
    const q = historyQueryString('n', 'a', 'b', '5m');
    expect(new URLSearchParams(q).get('step')).toBe('5m');
  });

  it('encodes an exact probe destination or explicitly omits probe history', () => {
    const selected = new URLSearchParams(historyQueryString('n', 'a', 'b', undefined, {
      probe: { id: 'db/main', type: 'tcp', host: 'db.example', port: 5432 },
    }));
    expect(selected.get('probe_id')).toBe('db/main');
    expect(selected.get('probe_type')).toBe('tcp');
    expect(selected.get('probe_host')).toBe('db.example');
    expect(selected.get('probe_port')).toBe('5432');
    expect(selected.has('include_probes')).toBe(false);

    const urlSelected = new URLSearchParams(historyQueryString('n', 'a', 'b', undefined, {
      probe: {
        id: 'health',
        type: 'url',
        url: 'https://service.example/health?full=1',
        expectedStatus: 204,
      },
    }));
    expect(urlSelected.get('probe_type')).toBe('url');
    expect(urlSelected.get('probe_url')).toBe('https://service.example/health?full=1');
    expect(urlSelected.get('probe_expected_status')).toBe('204');
    expect(urlSelected.has('probe_host')).toBe(false);
    expect(urlSelected.has('probe_port')).toBe(false);

    const resourceOnly = new URLSearchParams(historyQueryString('n', 'a', 'b', undefined, {
      includeProbes: false,
    }));
    expect(resourceOnly.get('include_probes')).toBe('false');
    expect(resourceOnly.has('probe_id')).toBe(false);
  });
});

describe('parseGoDuration', () => {
  it('parses the forms Go emits', () => {
    expect(parseGoDuration('30s')).toBe(30_000);
    expect(parseGoDuration('5m0s')).toBe(300_000);
    expect(parseGoDuration('1h0m0s')).toBe(3_600_000);
    expect(parseGoDuration('1m30s')).toBe(90_000);
    expect(parseGoDuration('300ms')).toBe(300);
    expect(parseGoDuration('1.5s')).toBe(1_500);
  });

  it('is tolerant of empty/garbage (→ 0)', () => {
    expect(parseGoDuration('')).toBe(0);
    expect(parseGoDuration('nonsense')).toBe(0);
  });
});

describe('parseNodeHistory', () => {
  it('parses a full additive response, carrying resource and probe history defensively', () => {
    const wire = {
      step: '5m0s',
      buckets: [
        {
          t: '2026-07-13T10:00:00Z',
          cpu_pct: { avg: 40.1, min: 30, max: 55 },
          load1: { avg: 1.2, min: 1, max: 1.5 },
          load5: { avg: 1.1 },
          load15: { avg: 1.0 },
          mem_used_pct: { avg: 62.5, min: 60, max: 70 },
        },
        // a valid bucket with cpu/mem ABSENT (a gap for those metrics)
        { t: '2026-07-13T10:05:00Z', load1: { avg: 2 }, load5: { avg: 2 }, load15: { avg: 2 } },
        // malformed: no loadavg → dropped
        { t: '2026-07-13T10:10:00Z' },
        // malformed: no timestamp → dropped
        { load1: { avg: 3 }, load5: { avg: 3 }, load15: { avg: 3 } },
      ],
      probes: [
        {
          series_id: 'a'.repeat(64),
          id: 'tcp-main',
          type: 'tcp',
          host: 'db.example.net',
          port: 5432,
          interval_ms: 60_000,
          buckets: [
            {
              t: '2026-07-13T10:00:00Z',
              attempts: 5,
              successes: 4,
              failures: 1,
              interval_ms: 60_000,
              latency_ms: { avg: 12.5, min: 10, max: 20 },
              failure_reasons: { timeout: 1, zero_ignored: 0, malformed: -1 },
            },
            // malformed counts → dropped without losing the series.
            { t: '2026-07-13T10:05:00Z', attempts: 1, successes: 2, failures: 0 },
          ],
        },
        // malformed TCP descriptor (no port) → dropped.
        { series_id: 'bad', id: 'bad', type: 'tcp', host: 'bad.example', buckets: [] },
      ],
    };
    const h = parseNodeHistory(wire);
    expect(h.step).toBe('5m0s');
    expect(h.disabled).toBe(false);
    expect(h.buckets).toHaveLength(2);
    expect(h.buckets[0].cpuPct).toEqual({ avg: 40.1, min: 30, max: 55 });
    expect(h.buckets[0].memUsedPct).toEqual({ avg: 62.5, min: 60, max: 70 });
    expect(h.buckets[0].load5).toEqual({ avg: 1.1 }); // min/max absent → carried as avg-only
    expect(h.buckets[1].cpuPct).toBeUndefined();
    expect(h.buckets[1].memUsedPct).toBeUndefined();
    expect(h.probes).toEqual([
      {
        seriesId: 'a'.repeat(64),
        id: 'tcp-main',
        type: 'tcp',
        host: 'db.example.net',
        port: 5432,
        intervalMS: 60_000,
        buckets: [{
          t: '2026-07-13T10:00:00Z',
          attempts: 5,
          successes: 4,
          failures: 1,
          intervalMS: 60_000,
          latencyMS: { avg: 12.5, min: 10, max: 20 },
          failureReasons: { timeout: 1 },
        }],
      },
    ]);
  });

  it('honors disabled:true with empty buckets (history off)', () => {
    const h = parseNodeHistory({ step: '5m0s', disabled: true, buckets: [] });
    expect(h.disabled).toBe(true);
    expect(h.buckets).toHaveLength(0);
    expect(h.probes).toHaveLength(0);
  });

  it('parses URL mismatch latency without admitting actual status into history', () => {
    const history = parseNodeHistory({
      probes: [{
        series_id: 'c'.repeat(64),
        id: 'health',
        type: 'url',
        url: 'https://service.example/health',
        expected_status: 204,
        actual_status: 500,
        interval_ms: 30_000,
        buckets: [{
          t: '2026-07-17T10:00:00Z',
          attempts: 1,
          successes: 0,
          failures: 1,
          latency_ms: { avg: 17, min: 17, max: 17 },
          failure_reasons: { unexpected_status: 1 },
          actual_status: 500,
        }],
      }],
    });

    expect(history.probes).toEqual([{
      seriesId: 'c'.repeat(64),
      id: 'health',
      type: 'url',
      url: 'https://service.example/health',
      expectedStatus: 204,
      intervalMS: 30_000,
      buckets: [{
        t: '2026-07-17T10:00:00Z',
        attempts: 1,
        successes: 0,
        failures: 1,
        latencyMS: { avg: 17, min: 17, max: 17 },
        failureReasons: { unexpected_status: 1 },
      }],
    }]);
    expect(JSON.stringify(history)).not.toContain('actualStatus');
    expect(JSON.stringify(history)).not.toContain('actual_status');
  });

  it('never throws on null/garbage input', () => {
    const empty = { step: '', disabled: false, buckets: [], probes: [] };
    expect(parseNodeHistory(null)).toEqual(empty);
    expect(parseNodeHistory({ buckets: 'not-an-array', probes: {} })).toEqual(empty);
    expect(parseNodeHistory(42)).toEqual(empty);
  });

  it('bounds accepted cardinality without letting malformed prefixes consume the budget', () => {
    const resourceBucket = {
      t: '2026-07-13T10:00:00Z',
      load1: { avg: 1 }, load5: { avg: 1 }, load15: { avg: 1 },
    };
    const probeBucket = {
      t: '2026-07-13T10:00:00Z', attempts: 1, successes: 1, failures: 0,
      latency_ms: { avg: 1 },
    };
    const validSeries = (index: number) => ({
      series_id: index.toString(16).padStart(64, '0'),
      id: `probe-${index}`,
      type: 'icmp',
      host: 'example.test',
      interval_ms: 30_000,
      buckets: [
        ...Array.from({ length: 20 }, () => ({ malformed: true })),
        ...Array.from({ length: 1005 }, () => probeBucket),
      ],
    });
    const history = parseNodeHistory({
      buckets: [
        ...Array.from({ length: 20 }, () => ({ malformed: true })),
        ...Array.from({ length: 1005 }, () => resourceBucket),
      ],
      probes: [
        ...Array.from({ length: 20 }, () => ({ malformed: true })),
        ...Array.from({ length: 20 }, (_, index) => validSeries(index + 1)),
      ],
    });
    expect(history.buckets).toHaveLength(1000);
    expect(history.probes).toHaveLength(16);
    expect(history.probes[0].buckets).toHaveLength(1000);

    expect(parseNodeHistory({ probes: [
      { ...validSeries(1), series_id: 'A'.repeat(64) },
      { ...validSeries(2), series_id: 'short' },
    ] }).probes).toHaveLength(0);

    const degradedCadences = parseNodeHistory({ probes: [
      { ...validSeries(3), interval_ms: 30_500 },
      { ...validSeries(4), interval_ms: 29_000 },
      { ...validSeries(5), interval_ms: 3_601_000 },
    ] }).probes;
    expect(degradedCadences).toHaveLength(3);
    expect(degradedCadences.every((series) => series.intervalMS === undefined)).toBe(true);

    const boundedReasons = parseNodeHistory({ probes: [{
      ...validSeries(6),
      buckets: [{
        t: probeBucket.t,
        attempts: 16,
        successes: 0,
        failures: 16,
        interval_ms: 3_601_000,
        failure_reasons: Object.fromEntries([
          ...Array.from({ length: 20 }, (_, index) => [`ignored_${index}`, 0] as const),
          ...Array.from({ length: 16 }, (_, index) => [`reason_${index}`, 1] as const),
        ]),
      }, {
        t: probeBucket.t,
        attempts: 1_000_001,
        successes: 1_000_001,
        failures: 0,
      }],
    }] }).probes[0];
    expect(Object.keys(boundedReasons.buckets[0].failureReasons)).toHaveLength(16);
    expect(boundedReasons.buckets[0].intervalMS).toBeUndefined();
    expect(boundedReasons.buckets).toHaveLength(1);
  });
});

describe('metricSeries', () => {
  const stepMs = 5 * 60_000; // 5m
  const buckets: HistoryBucket[] = [
    {
      t: '2026-07-13T10:00:00Z',
      cpuPct: { avg: 40, min: 30, max: 55 },
      load1: { avg: 1 },
      load5: { avg: 1 },
      load15: { avg: 1 },
    },
    // metric-absent gap: cpu missing in this bucket
    { t: '2026-07-13T10:05:00Z', load1: { avg: 2 }, load5: { avg: 2 }, load15: { avg: 2 } },
    // a run of MISSING buckets: 30 min later (jump ≫ 1.5×step)
    {
      t: '2026-07-13T10:35:00Z',
      cpuPct: { avg: 50, min: 45, max: 60 },
      load1: { avg: 3 },
      load5: { avg: 3 },
      load15: { avg: 3 },
    },
  ];

  it('maps avg and carries min/max as a band; a metric-absent bucket is a null point', () => {
    const s = metricSeries(buckets, stepMs, (b) => b.cpuPct);
    // point0(avg), point1(metric-absent → null), sentinel(missing-bucket gap → null), point2(avg)
    expect(s).toHaveLength(4);
    expect(s[0]).toEqual({ t: Date.parse('2026-07-13T10:00:00Z'), avg: 40, min: 30, max: 55 });
    expect(s[1].avg).toBeNull();
    expect(s[1].min).toBeNull();
    // the inter-bucket gap sentinel lands one step after the previous bucket
    expect(s[2]).toEqual({ t: Date.parse('2026-07-13T10:05:00Z') + stepMs, avg: null, min: null, max: null });
    expect(s[3].avg).toBe(50);
  });

  it('always-present loadavg still breaks across a missing-bucket run', () => {
    const s = metricSeries(buckets, stepMs, (b) => b.load1);
    // no metric-absent gap for load; only the one inter-bucket sentinel between 10:05 and 10:35
    const nulls = s.filter((p) => p.avg === null);
    expect(nulls).toHaveLength(1);
    expect(s.map((p) => p.avg)).toEqual([1, 2, null, 3]);
  });

  it('does not invent a gap for one empty bucket at the heartbeat-cadence boundary', () => {
    const oneEmptyBucket: HistoryBucket[] = [
      {
        t: '2026-07-13T10:00:00Z',
        load1: { avg: 1 },
        load5: { avg: 1 },
        load15: { avg: 1 },
      },
      {
        t: '2026-07-13T10:10:00Z',
        load1: { avg: 2 },
        load5: { avg: 2 },
        load15: { avg: 2 },
      },
    ];
    const s = metricSeries(oneEmptyBucket, stepMs, (b) => b.load1);
    expect(s.map((p) => p.avg)).toEqual([1, 2]);
  });

  it('does not insert gap sentinels when the step is unknown (0)', () => {
    const s = metricSeries(buckets, 0, (b) => b.load1);
    expect(s.filter((p) => p.avg === null)).toHaveLength(0);
    expect(s).toHaveLength(3);
  });

  it('carries a band only when both min and max are present', () => {
    const s = metricSeries(buckets, stepMs, (b) => b.load1); // load has avg only
    expect(s[0]).toEqual({ t: Date.parse('2026-07-13T10:00:00Z'), avg: 1, min: null, max: null });
  });
});

describe('probe history series', () => {
  const stepMs = 5 * 60_000;
  const buckets: ProbeHistoryBucket[] = [
    {
      t: '2026-07-13T10:00:00Z',
      attempts: 4,
      successes: 3,
      failures: 1,
      latencyMS: { avg: 15, min: 10, max: 20 },
      failureReasons: { timeout: 1 },
    },
    {
      t: '2026-07-13T10:05:00Z',
      attempts: 2,
      successes: 0,
      failures: 2,
      failureReasons: { timeout: 1, dns_failed: 1 },
    },
    {
      t: '2026-07-13T10:35:00Z',
      attempts: 1,
      successes: 1,
      failures: 0,
      latencyMS: { avg: 9 },
      failureReasons: {},
    },
    {
      t: '2026-07-13T10:40:00Z',
      attempts: 1,
      successes: 0,
      failures: 1,
      failureReasons: {},
    },
  ];

  it('plots latency bands, failure-only buckets as null, and missing-bucket gaps', () => {
    expect(probeLatencySeries(buckets, stepMs)).toEqual([
      { t: Date.parse('2026-07-13T10:00:00Z'), avg: 15, min: 10, max: 20 },
      { t: Date.parse('2026-07-13T10:05:00Z'), avg: null, min: null, max: null },
      { t: Date.parse('2026-07-13T10:10:00Z'), avg: null, min: null, max: null },
      { t: Date.parse('2026-07-13T10:35:00Z'), avg: 9, min: null, max: null },
      { t: Date.parse('2026-07-13T10:40:00Z'), avg: null, min: null, max: null },
    ]);
  });

  it('derives availability without turning failures into latency zeroes', () => {
    expect(probeAvailabilitySeries(buckets, 0).map((point) => point.avg)).toEqual([75, 0, 100, 0]);
  });

  it('uses the exact current policy cadence when an rc.9 response has no interval metadata', () => {
    const rc9: ProbeHistoryBucket[] = [
      { t: '2026-07-13T10:00:00Z', attempts: 1, successes: 1, failures: 0, latencyMS: { avg: 1 }, failureReasons: {} },
      { t: '2026-07-13T10:10:00Z', attempts: 1, successes: 1, failures: 0, latencyMS: { avg: 2 }, failureReasons: {} },
      { t: '2026-07-13T10:25:00Z', attempts: 1, successes: 1, failures: 0, latencyMS: { avg: 3 }, failureReasons: {} },
    ];
    const points = probeLatencySeries(rc9, 30_000, 5 * 60_000);
    expect(points.map((point) => point.avg)).toEqual([1, 2, null, 3]);
    expect(points[2].t).toBe(Date.parse('2026-07-13T10:15:00Z'));
  });

  it('prefers controller-observed deployed cadence over an edited current draft', () => {
    expect(probeHistoryFallbackIntervalMS(30_000, 3600)).toBe(30_000);
    expect(probeHistoryFallbackIntervalMS(undefined, 300)).toBe(300_000);
    expect(probeHistoryFallbackIntervalMS(undefined, undefined)).toBe(60_000);
  });

  it('uses adjacent bucket cadences across both schedule-transition directions', () => {
    const transitioned: ProbeHistoryBucket[] = [
      { t: '2026-07-13T10:00:00Z', attempts: 1, successes: 1, failures: 0, intervalMS: 30_000, latencyMS: { avg: 1 }, failureReasons: {} },
      { t: '2026-07-13T11:00:00Z', attempts: 1, successes: 1, failures: 0, intervalMS: 3_600_000, latencyMS: { avg: 2 }, failureReasons: {} },
      { t: '2026-07-13T12:00:00Z', attempts: 1, successes: 1, failures: 0, intervalMS: 30_000, latencyMS: { avg: 3 }, failureReasons: {} },
      { t: '2026-07-13T12:05:00Z', attempts: 1, successes: 1, failures: 0, intervalMS: 30_000, latencyMS: { avg: 4 }, failureReasons: {} },
    ];
    const points = probeLatencySeries(transitioned, 30_000, 30_000);
    expect(points.map((point) => point.avg)).toEqual([1, 2, 3, null, 4]);
    expect(points[3].t).toBe(Date.parse('2026-07-13T12:00:30Z'));
  });

  it('does not let a slower current-policy fallback bridge historical fast-cadence gaps', () => {
    const historicalFast: ProbeHistoryBucket[] = [
      { t: '2026-07-13T10:00:00Z', attempts: 1, successes: 1, failures: 0, intervalMS: 30_000, latencyMS: { avg: 1 }, failureReasons: {} },
      { t: '2026-07-13T10:05:00Z', attempts: 1, successes: 1, failures: 0, intervalMS: 30_000, latencyMS: { avg: 2 }, failureReasons: {} },
    ];
    const points = probeLatencySeries(historicalFast, 30_000, 3_600_000);
    expect(points.map((point) => point.avg)).toEqual([1, null, 2]);
    expect(points[1].t).toBe(Date.parse('2026-07-13T10:00:30Z'));
  });

  it('sums and orders failure reasons across the selected range', () => {
    expect(summarizeProbeFailures(buckets)).toEqual([
      { reason: 'timeout', count: 2 },
      { reason: 'dns_failed', count: 1 },
      { reason: 'uncategorized', count: 1 },
    ]);
  });

  it('matches history to the complete executable destination, not id alone', () => {
    const series = parseNodeHistory({
      probes: [{
        series_id: 'b'.repeat(64), id: 'same-id', type: 'tcp', host: 'old.example', port: 443, buckets: [],
      }],
    }).probes[0];
    expect(probeHistoryMatchesPolicy(
      { id: 'same-id', type: 'tcp', host: 'old.example', port: 443 },
      series,
    )).toBe(true);
    expect(probeHistoryMatchesPolicy(
      { id: 'same-id', type: 'tcp', host: 'new.example', port: 443 },
      series,
    )).toBe(false);
    expect(probeHistoryMatchesPolicy(
      { id: 'same-id', type: 'tcp', host: 'old.example', port: 8443 },
      series,
    )).toBe(false);
  });

  it('matches URL history by exact URL and effective expected status', () => {
    const series = parseNodeHistory({ probes: [{
      series_id: 'd'.repeat(64),
      id: 'health',
      type: 'url',
      url: 'https://service.example/health',
      expected_status: 200,
      buckets: [],
    }] }).probes[0];
    expect(probeHistoryMatchesPolicy(
      { id: 'health', type: 'url', url: 'https://service.example/health' },
      series,
    )).toBe(true);
    expect(probeHistoryMatchesPolicy(
      { id: 'health', type: 'url', url: 'https://service.example/health', expected_status: 204 },
      series,
    )).toBe(false);
  });
});

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

async function flushMicrotasks() {
  for (let i = 0; i < 8; i++) await Promise.resolve();
}

describe('latest request coordinator', () => {
  it('coalesces same-key refreshes to one latest deferred request without overlap', async () => {
    const first = deferred<string>();
    const latest = deferred<string>();
    const execute = vi.fn((query: { key: string; sequence: number }) =>
      query.sequence === 1 ? first.promise : latest.promise,
    );
    const successes: Array<[string, number]> = [];
    const errors: unknown[] = [];
    const idle = vi.fn();
    const coordinator = createLatestRequestCoordinator({
      key: (query: { key: string; sequence: number }) => query.key,
      execute: (query) => execute(query),
      onSuccess: (result, query) => successes.push([result, query.sequence]),
      onError: (error) => errors.push(error),
      onIdle: idle,
    });

    coordinator.request({ key: 'same', sequence: 1 });
    await flushMicrotasks();
    coordinator.request({ key: 'same', sequence: 2 });
    coordinator.request({ key: 'same', sequence: 3 });
    expect(execute).toHaveBeenCalledTimes(1);

    first.resolve('first');
    await flushMicrotasks();
    expect(execute.mock.calls.map(([query]) => query.sequence)).toEqual([1, 3]);
    expect(successes).toEqual([['first', 1]]);

    latest.resolve('latest');
    await flushMicrotasks();
    expect(successes).toEqual([['first', 1], ['latest', 3]]);
    expect(errors).toEqual([]);
    expect(idle).toHaveBeenCalledTimes(1);
  });

  it('aborts a changed key, waits for settlement, and installs only the latest result', async () => {
    const old = deferred<string>();
    const next = deferred<string>();
    const signals: AbortSignal[] = [];
    let active = 0;
    let maximumActive = 0;
    const execute = vi.fn((query: { key: string }, signal: AbortSignal) => {
      signals.push(signal);
      active++;
      maximumActive = Math.max(maximumActive, active);
      const promise = query.key === 'old' ? old.promise : next.promise;
      return promise.finally(() => { active--; });
    });
    const success = vi.fn();
    const error = vi.fn();
    const coordinator = createLatestRequestCoordinator({
      key: (query: { key: string }) => query.key,
      execute,
      onSuccess: success,
      onError: error,
    });

    coordinator.request({ key: 'old' });
    await flushMicrotasks();
    coordinator.request({ key: 'new' });
    expect(signals[0].aborted).toBe(true);
    expect(execute).toHaveBeenCalledTimes(1);

    // Even an abort-insensitive transport cannot overlap requests; its stale success is ignored.
    old.resolve('stale');
    await flushMicrotasks();
    expect(execute).toHaveBeenCalledTimes(2);
    expect(maximumActive).toBe(1);
    expect(success).not.toHaveBeenCalled();
    expect(error).not.toHaveBeenCalled();

    next.resolve('current');
    await flushMicrotasks();
    expect(success).toHaveBeenCalledOnce();
    expect(success.mock.calls[0][0]).toBe('current');
    expect(success.mock.calls[0][1]).toEqual({ key: 'new' });
  });

  it('invalidates callbacks and pending work on dispose', async () => {
    const work = deferred<string>();
    const success = vi.fn();
    const error = vi.fn();
    const coordinator = createLatestRequestCoordinator({
      key: (query: string) => query,
      execute: () => work.promise,
      onSuccess: success,
      onError: error,
    });
    coordinator.request('one');
    await flushMicrotasks();
    coordinator.request('two');
    coordinator.dispose();
    work.resolve('late');
    await flushMicrotasks();
    expect(success).not.toHaveBeenCalled();
    expect(error).not.toHaveBeenCalled();
  });
});
