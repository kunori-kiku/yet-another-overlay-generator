// telemetryHistory.ts — PURE logic for the node-detail resource-history charts (plan-4): the
// param→query wiring (range preset + granularity → from/to/step), the wire→typed parsing of the
// plan-3 `GET node-history` response, the Go-duration math the charts need, and the wire→series
// shaping (one metric → a gap-aware point list). Deliberately dependency-free and DOM-free so it
// lives under the vitest `src/lib/**` include glob (telemetryHistory.test.ts) — the rendered chart
// (src/components/**, not globbed, node-env with no jsdom) is covered by e2e instead.
//
// Nothing here imports the controller client or React: the client calls parseNodeHistory at the
// fetch boundary, the component calls the range/step/series helpers, and both are unit-pinned here.

// RangePreset is the node-detail history window the operator picks. Values are opaque keys; the
// window length in seconds is RANGE_SECONDS.
export type RangePreset = '1h' | '6h' | '24h' | '7d';

// Granularity is the requested bucket step. 'auto' omits the step param entirely so the server
// picks a step that fits the window under its bucket cap (and echoes the effective step back); the
// rest are literal Go-duration strings the server accepts verbatim.
export type Granularity = 'auto' | '30s' | '5m' | '30m' | '1h';

// The preset/granularity option lists, in display order (the picker maps over them).
export const RANGE_PRESETS: readonly RangePreset[] = ['1h', '6h', '24h', '7d'];
export const GRANULARITIES: readonly Granularity[] = ['auto', '30s', '5m', '30m', '1h'];

// Window length per preset, in seconds.
export const RANGE_SECONDS: Record<RangePreset, number> = {
  '1h': 3600,
  '6h': 6 * 3600,
  '24h': 24 * 3600,
  '7d': 7 * 24 * 3600,
};

// metricAgg mirrors one metric's avg/min/max over a bucket (plan-3 metricAgg). min/max are optional
// on our side because a defensively-parsed wire object may carry only avg.
export interface MetricAgg {
  avg: number;
  min?: number;
  max?: number;
}

// HistoryBucket is one time bucket (plan-3 historyBucket). load1/5/15 are always present (every
// sample carries loadavg); cpuPct and memUsedPct are OPTIONAL — absent means a gap (no sample in the
// bucket carried that metric), never a fabricated 0. t is the bucket START (RFC3339).
export interface HistoryBucket {
  t: string;
  cpuPct?: MetricAgg;
  load1: MetricAgg;
  load5: MetricAgg;
  load15: MetricAgg;
  memUsedPct?: MetricAgg;
}

// NodeHistory is the parsed plan-3 response. step is the EFFECTIVE step (may be widened from the
// request); disabled true means history retention is off (fleet cap 0) → the panel shows a hint.
export interface NodeHistory {
  step: string;
  disabled: boolean;
  buckets: HistoryBucket[];
}

// ChartPoint is one plotted sample for a single metric. avg/min/max are null at a GAP (a bucket
// missing the metric, or a run of missing buckets) so a chart with connectNulls=false breaks the
// line there instead of drawing a straight segment across the gap. t is epoch ms (a numeric/time
// axis handles the irregular bucket spacing correctly).
export interface ChartPoint {
  t: number;
  avg: number | null;
  min: number | null;
  max: number | null;
}

// rangeWindow computes the RFC3339 [from, to] window for a preset relative to nowMs. `to` is now;
// `from` is now minus the preset length. toISOString() emits RFC3339 with a millisecond fraction,
// which Go's time.Parse(time.RFC3339, …) accepts (it reads an optional fractional second even when
// the layout has none).
export function rangeWindow(preset: RangePreset, nowMs: number): { from: string; to: string } {
  const to = new Date(nowMs);
  const from = new Date(nowMs - RANGE_SECONDS[preset] * 1000);
  return { from: from.toISOString(), to: to.toISOString() };
}

// granularityStep maps a Granularity to the `step` query value: undefined for 'auto' (omit the
// param → server picks), else the literal Go-duration string the server parses.
export function granularityStep(g: Granularity): string | undefined {
  return g === 'auto' ? undefined : g;
}

// historyQueryString builds the encoded query for `node-history` (node/from/to and, when set,
// step). URLSearchParams percent-encodes the RFC3339 colons; the Go handler's q.Get decodes them.
export function historyQueryString(nodeId: string, from: string, to: string, step?: string): string {
  const p = new URLSearchParams({ node: nodeId, from, to });
  if (step) p.set('step', step);
  return p.toString();
}

// GO_DURATION_UNIT_MS maps a Go-duration unit to milliseconds. Go's Duration.String() emits h/m/s/
// ms/us(µs)/ns; we accept all so parseGoDuration can read back any echoed step.
const GO_DURATION_UNIT_MS: Record<string, number> = {
  ns: 1e-6,
  us: 1e-3,
  'µs': 1e-3,
  'μs': 1e-3,
  ms: 1,
  s: 1000,
  m: 60_000,
  h: 3_600_000,
};

// parseGoDuration parses a Go-duration string (e.g. "5m0s", "30s", "1h0m0s", "300ms") to
// milliseconds. Tolerant: an unrecognized/empty input yields 0 (the caller treats 0 as "unknown
// step" and skips gap-widening). "ms" is matched before "s"/"m" by the alternation order.
export function parseGoDuration(s: string): number {
  if (!s) return 0;
  const re = /([0-9]*\.?[0-9]+)(ns|us|µs|μs|ms|s|m|h)/g;
  let total = 0;
  let matched = false;
  let m: RegExpExecArray | null;
  while ((m = re.exec(s)) !== null) {
    matched = true;
    total += parseFloat(m[1]) * (GO_DURATION_UNIT_MS[m[2]] ?? 0);
  }
  return matched ? total : 0;
}

// parseAgg defensively parses one wire metric object {avg,min,max}. A non-finite/absent avg yields
// undefined (the metric is treated as absent — a gap); min/max are carried only when finite.
function parseAgg(raw: unknown): MetricAgg | undefined {
  if (!raw || typeof raw !== 'object') return undefined;
  const o = raw as { avg?: unknown; min?: unknown; max?: unknown };
  if (typeof o.avg !== 'number' || !Number.isFinite(o.avg)) return undefined;
  const out: MetricAgg = { avg: o.avg };
  if (typeof o.min === 'number' && Number.isFinite(o.min)) out.min = o.min;
  if (typeof o.max === 'number' && Number.isFinite(o.max)) out.max = o.max;
  return out;
}

// parseBucket parses one wire bucket. A bucket without a valid timestamp or without the always-
// present loadavg triple is dropped (malformed); cpu_pct/mem_used_pct are optional (absent = gap).
function parseBucket(raw: unknown): HistoryBucket | null {
  if (!raw || typeof raw !== 'object') return null;
  const o = raw as Record<string, unknown>;
  if (typeof o.t !== 'string') return null;
  const load1 = parseAgg(o.load1);
  const load5 = parseAgg(o.load5);
  const load15 = parseAgg(o.load15);
  if (!load1 || !load5 || !load15) return null;
  const b: HistoryBucket = { t: o.t, load1, load5, load15 };
  const cpu = parseAgg(o.cpu_pct);
  if (cpu) b.cpuPct = cpu;
  const mem = parseAgg(o.mem_used_pct);
  if (mem) b.memUsedPct = mem;
  return b;
}

// parseNodeHistory maps the raw plan-3 JSON to NodeHistory. Defensive at every layer (a garbled
// field never throws): step→'' when absent, disabled strictly === true, buckets filtered to the
// well-formed ones.
export function parseNodeHistory(raw: unknown): NodeHistory {
  const o = (raw ?? {}) as { step?: unknown; disabled?: unknown; buckets?: unknown };
  const buckets = Array.isArray(o.buckets)
    ? o.buckets.map(parseBucket).filter((b): b is HistoryBucket => b !== null)
    : [];
  return {
    step: typeof o.step === 'string' ? o.step : '',
    disabled: o.disabled === true,
    buckets,
  };
}

// metricSeries projects ONE metric out of the buckets into a gap-aware ChartPoint list. `pick`
// selects the metric (e.g. b => b.cpuPct). Two gap sources are honored so connectNulls=false breaks
// the line correctly: (1) a bucket missing the metric → a null point at its timestamp; (2) a run of
// MISSING buckets (server omits empty buckets) → a single null sentinel inserted one step after the
// previous bucket when the time jump exceeds ~1.5×step. stepMs (parseGoDuration of the effective
// step) gates the sentinel; a 0/unknown step skips it (points still carry their own null avg).
export function metricSeries(
  buckets: HistoryBucket[],
  stepMs: number,
  pick: (b: HistoryBucket) => MetricAgg | undefined,
): ChartPoint[] {
  const out: ChartPoint[] = [];
  let prevMs: number | null = null;
  for (const b of buckets) {
    const ms = Date.parse(b.t);
    if (Number.isNaN(ms)) continue;
    if (prevMs !== null && stepMs > 0 && ms - prevMs > stepMs * 1.5) {
      out.push({ t: prevMs + stepMs, avg: null, min: null, max: null });
    }
    const agg = pick(b);
    if (agg) {
      out.push({ t: ms, avg: agg.avg, min: agg.min ?? null, max: agg.max ?? null });
    } else {
      out.push({ t: ms, avg: null, min: null, max: null });
    }
    prevMs = ms;
  }
  return out;
}
