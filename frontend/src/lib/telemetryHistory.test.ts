// @vitest-environment node
//
// telemetryHistory.test.ts — pins the plan-4 PURE history logic the node-detail charts depend on:
// the param→query wiring (range/granularity → from/to/step), the Go-duration math, the defensive
// wire→typed parse of the plan-3 response, and the gap-aware wire→series shaping. Dependency-free
// node-env unit test (src/lib/** is in the vitest include globs); the rendered chart is e2e-covered.

import { describe, expect, it } from 'vitest';
import {
  GRANULARITIES,
  RANGE_PRESETS,
  RANGE_SECONDS,
  granularityStep,
  historyQueryString,
  metricSeries,
  parseGoDuration,
  parseNodeHistory,
  rangeWindow,
  type HistoryBucket,
} from './telemetryHistory';

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
  it('parses a full response, carrying optional cpu/mem and dropping malformed buckets', () => {
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
  });

  it('honors disabled:true with empty buckets (history off)', () => {
    const h = parseNodeHistory({ step: '5m0s', disabled: true, buckets: [] });
    expect(h.disabled).toBe(true);
    expect(h.buckets).toHaveLength(0);
  });

  it('never throws on null/garbage input', () => {
    expect(parseNodeHistory(null)).toEqual({ step: '', disabled: false, buckets: [] });
    expect(parseNodeHistory({ buckets: 'not-an-array' })).toEqual({ step: '', disabled: false, buckets: [] });
    expect(parseNodeHistory(42)).toEqual({ step: '', disabled: false, buckets: [] });
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
