import type { TelemetryProbe } from '../types/topology';
import { isValidProbeURL } from './probeResults';

// telemetryHistory.ts — PURE logic for the node-detail telemetry-history charts: the param→query
// wiring (range preset + granularity → from/to/step), the wire→typed parsing of the additive
// `GET node-history` response, the Go-duration math the charts need, and the resource/probe
// wire→series shaping. Deliberately dependency-free and DOM-free so it
// is pinned directly in telemetryHistory.test.ts without needing a browser DOM.
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

// Every history family that crosses the Go controller→panel boundary. The Go catalog/drift gate
// reads this literal as the frontend authority, while the parser and production renderer each use
// the derived union in an exhaustive `satisfies Record<...>` registry. A newly charted Go family
// therefore cannot stop at retention or parsing without making CI red.
export const HISTORY_CHART_FAMILIES = ['resource', 'probe'] as const;
export type HistoryChartFamily = typeof HISTORY_CHART_FAMILIES[number];

const MAX_HISTORY_BUCKETS = 1000;
const MAX_PROBE_SERIES = 16;
const MAX_FAILURE_REASONS = 16;
const MAX_HISTORY_SAMPLE_COUNT = 1_000_000;
const MIN_PROBE_INTERVAL_MS = 30_000;
const MAX_PROBE_INTERVAL_MS = 3_600_000;
const PROBE_SERIES_ID = /^[0-9a-f]{64}$/;
const FAILURE_REASON = /^[a-z][a-z0-9_]{0,63}$/;

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

// ProbeHistoryBucket aggregates completed attempts for one signed probe policy in one time bucket.
// latencyMS is absent when no completed response carried latency; that is a chart gap, never 0 ms.
// failureReasons is intentionally an open string map so a newer controller can add a stable reason
// without making an older panel discard an otherwise valid bucket.
export interface ProbeHistoryBucket {
  t: string;
  attempts: number;
  successes: number;
  failures: number;
  // The cadence effective for the latest attempt in this bucket. Newer controllers provide this
  // per bucket so a chart can cross signed-policy schedule changes without treating the transition
  // as either an outage or an indefinitely connected line. rc.9 responses omit it.
  intervalMS?: number;
  latencyMS?: MetricAgg;
  failureReasons: Record<string, number>;
}

// ProbeHistorySeries is keyed by the controller's policy identity. The executable destination is
// repeated so the UI can require an exact typed-destination match with the CURRENT design before it
// attributes old measurements to a configured probe.
interface ProbeHistorySeriesBase {
  seriesId: string;
  id: string;
  intervalMS?: number;
  buckets: ProbeHistoryBucket[];
}

export type ProbeHistorySeries = ProbeHistorySeriesBase & (
  | { type: 'icmp'; host: string; port?: never; url?: never; expectedStatus?: never }
  | { type: 'tcp'; host: string; port: number; url?: never; expectedStatus?: never }
  | { type: 'url'; url: string; expectedStatus: number; host?: never; port?: never }
);

// NodeHistory is the parsed response. step is the EFFECTIVE step (may be widened from the
// request); disabled true means history retention is off (fleet cap 0) → the panel shows a hint.
export interface NodeHistory {
  step: string;
  disabled: boolean;
  buckets: HistoryBucket[];
  probes: ProbeHistorySeries[];
}

interface HistoryProbeSelectorBase {
  id: string;
}

export type HistoryProbeSelector = HistoryProbeSelectorBase & (
  | { type: 'icmp'; host: string; port?: never; url?: never; expectedStatus?: never }
  | { type: 'tcp'; host: string; port: number; url?: never; expectedStatus?: never }
  | { type: 'url'; url: string; expectedStatus: number; host?: never; port?: never }
);

// Request options stay component-local. `probe` is an exact executable-destination selector;
// `includeProbes:false` is useful when the current node has no configured probe. `signal` is not
// serialized and lets the node-detail request coordinator retire a superseded fetch promptly.
export interface NodeHistoryRequestOptions {
  includeProbes?: boolean;
  probe?: HistoryProbeSelector;
  signal?: AbortSignal;
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

// The controller may widen a requested step to keep the complete response inside its global bucket
// budget. Compare durations rather than strings because Go echoes canonical forms such as `5m0s`
// while the select uses `5m`.
export function resolutionWasWidened(requested: Granularity, effective: string): boolean {
  const requestedStep = granularityStep(requested);
  if (requestedStep === undefined) return false;
  const requestedMS = parseGoDuration(requestedStep);
  const effectiveMS = parseGoDuration(effective);
  return requestedMS > 0 && effectiveMS > requestedMS;
}

// Turn a canonical Go duration into a compact human-readable chart label. Bucket-cap division can
// produce fractional-second strings; round UP to a whole second so the UI never promises finer
// resolution than the controller actually returned.
export function formatHistoryResolution(step: string): string {
  const parsedMS = parseGoDuration(step);
  if (!(parsedMS > 0)) return step;
  let seconds = Math.ceil(parsedMS / 1000);
  const hours = Math.floor(seconds / 3600);
  seconds -= hours * 3600;
  const minutes = Math.floor(seconds / 60);
  seconds -= minutes * 60;
  const parts: string[] = [];
  if (hours > 0) parts.push(`${hours}h`);
  if (minutes > 0) parts.push(`${minutes}m`);
  if (seconds > 0 || parts.length === 0) parts.push(`${seconds}s`);
  return parts.join(' ');
}

// historyQueryString builds the encoded query for `node-history` (node/from/to and, when set,
// step). URLSearchParams percent-encodes the RFC3339 colons; the Go handler's q.Get decodes them.
export function historyQueryString(
  nodeId: string,
  from: string,
  to: string,
  step?: string,
  options: Pick<NodeHistoryRequestOptions, 'includeProbes' | 'probe'> = {},
): string {
  const p = new URLSearchParams({ node: nodeId, from, to });
  if (step) p.set('step', step);
  if (options.includeProbes === false) p.set('include_probes', 'false');
  if (options.probe) {
    p.set('probe_id', options.probe.id);
    p.set('probe_type', options.probe.type);
    if (options.probe.type === 'url') {
      p.set('probe_url', options.probe.url);
      p.set('probe_expected_status', String(options.probe.expectedStatus));
    } else {
      p.set('probe_host', options.probe.host);
    }
    if (options.probe.type === 'tcp') {
      p.set('probe_port', String(options.probe.port));
    }
  }
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

function parseAccepted<T>(raw: unknown, limit: number, parse: (candidate: unknown) => T | null): T[] {
  if (!Array.isArray(raw)) return [];
  const accepted: T[] = [];
  for (const candidate of raw) {
    const parsed = parse(candidate);
    if (parsed === null) continue;
    accepted.push(parsed);
    if (accepted.length >= limit) break;
  }
  return accepted;
}

// parseBucket parses one wire bucket. A bucket without a valid timestamp or without the always-
// present loadavg triple is dropped (malformed); cpu_pct/mem_used_pct are optional (absent = gap).
function parseBucket(raw: unknown): HistoryBucket | null {
  if (!raw || typeof raw !== 'object') return null;
  const o = raw as Record<string, unknown>;
  if (typeof o.t !== 'string' || Number.isNaN(Date.parse(o.t))) return null;
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

function parseCount(raw: unknown): number | null {
  return typeof raw === 'number' &&
    Number.isSafeInteger(raw) &&
    raw >= 0 &&
    raw <= MAX_HISTORY_SAMPLE_COUNT
    ? raw
    : null;
}

function parseFailureReasons(raw: unknown): Record<string, number> {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return {};
  const out: Record<string, number> = {};
  const record = raw as Record<string, unknown>;
  let accepted = 0;
  for (const reason in record) {
    if (!Object.prototype.hasOwnProperty.call(record, reason)) continue;
    const count = record[reason];
    const parsed = parseCount(count);
    if (FAILURE_REASON.test(reason) && parsed !== null && parsed > 0) {
      out[reason] = parsed;
      accepted++;
      if (accepted >= MAX_FAILURE_REASONS) break;
    }
  }
  return out;
}

function parseProbeIntervalMS(raw: unknown): number | undefined {
  const parsed = parseCount(raw);
  return parsed !== null &&
    parsed >= MIN_PROBE_INTERVAL_MS &&
    parsed <= MAX_PROBE_INTERVAL_MS &&
    parsed % 1000 === 0
    ? parsed
    : undefined;
}

function parseProbeBucket(raw: unknown): ProbeHistoryBucket | null {
  if (!raw || typeof raw !== 'object') return null;
  const o = raw as Record<string, unknown>;
  if (typeof o.t !== 'string' || Number.isNaN(Date.parse(o.t))) return null;
  const attempts = parseCount(o.attempts);
  const successes = parseCount(o.successes);
  const failures = parseCount(o.failures);
  if (
    attempts === null ||
    successes === null ||
    failures === null ||
    successes + failures !== attempts
  ) {
    return null;
  }
  const failureReasons = parseFailureReasons(o.failure_reasons);
  if (Object.values(failureReasons).reduce((sum, count) => sum + count, 0) > failures) return null;
  const bucket: ProbeHistoryBucket = {
    t: o.t,
    attempts,
    successes,
    failures,
    failureReasons,
  };
  const intervalMS = parseProbeIntervalMS(o.interval_ms);
  if (intervalMS !== undefined) bucket.intervalMS = intervalMS;
  const latency = parseAgg(o.latency_ms);
  if (
    latency &&
    latency.avg >= 0 &&
    (latency.min === undefined || latency.min >= 0) &&
    (latency.max === undefined || latency.max >= 0)
  ) {
    bucket.latencyMS = latency;
  }
  return bucket;
}

function parseProbeSeries(raw: unknown): ProbeHistorySeries | null {
  if (!raw || typeof raw !== 'object') return null;
  const o = raw as Record<string, unknown>;
  if (
    typeof o.series_id !== 'string' ||
    !PROBE_SERIES_ID.test(o.series_id) ||
    typeof o.id !== 'string' ||
    o.id.length === 0 ||
    o.id.length > 63 ||
    (o.type !== 'icmp' && o.type !== 'tcp' && o.type !== 'url')
  ) {
    return null;
  }
  const common = {
    seriesId: o.series_id,
    id: o.id,
    intervalMS: parseProbeIntervalMS(o.interval_ms),
    buckets: parseAccepted(o.buckets, MAX_HISTORY_BUCKETS, parseProbeBucket),
  };
  if (o.type === 'url') {
    const expectedStatus = parseCount(o.expected_status);
    if (
      o.host !== undefined ||
      o.port !== undefined ||
      !isValidProbeURL(o.url) ||
      expectedStatus === null || expectedStatus < 100 || expectedStatus > 599
    ) {
      return null;
    }
    return {
      seriesId: common.seriesId,
      id: common.id,
      type: 'url',
      url: o.url,
      expectedStatus,
      ...(common.intervalMS === undefined ? {} : { intervalMS: common.intervalMS }),
      buckets: common.buckets,
    };
  }
  if (
    o.url !== undefined ||
    o.expected_status !== undefined ||
    typeof o.host !== 'string' ||
    o.host.length === 0 ||
    o.host.length > 253
  ) {
    return null;
  }
  if (o.type === 'tcp') {
    const parsed = parseCount(o.port);
    if (parsed === null || parsed < 1 || parsed > 65535) return null;
    return {
      seriesId: common.seriesId,
      id: common.id,
      type: 'tcp',
      host: o.host,
      port: parsed,
      ...(common.intervalMS === undefined ? {} : { intervalMS: common.intervalMS }),
      buckets: common.buckets,
    };
  }
  if (o.port !== undefined) return null;
  // Cadence is advisory chart metadata. A newer controller value outside the currently understood
  // range must not discard a valid exact series; degrade it to "unknown" and use current policy.
  return {
    seriesId: common.seriesId,
    id: common.id,
    type: 'icmp',
    host: o.host,
    ...(common.intervalMS === undefined ? {} : { intervalMS: common.intervalMS }),
    buckets: common.buckets,
  };
}

type HistoryFamilyParser = (wire: Record<string, unknown>) => Partial<NodeHistory>;

// Keep the wire's existing additive shape, but make family admission explicit and exhaustive. The
// registry is deliberately in the real parse path (not a parallel manifest used only by a test), so
// a family cannot be declared without supplying the parser that exposes its typed chart data.
const HISTORY_CHART_PARSERS = {
  resource: (wire) => ({
    buckets: parseAccepted(wire.buckets, MAX_HISTORY_BUCKETS, parseBucket),
  }),
  probe: (wire) => ({
    probes: parseAccepted(wire.probes, MAX_PROBE_SERIES, parseProbeSeries),
  }),
} satisfies Record<HistoryChartFamily, HistoryFamilyParser>;

// parseNodeHistory maps the raw JSON to NodeHistory. Defensive at every layer (a garbled
// field never throws): step→'' when absent, disabled strictly === true, buckets filtered to the
// well-formed ones.
export function parseNodeHistory(raw: unknown): NodeHistory {
  const wire = raw && typeof raw === 'object' && !Array.isArray(raw)
    ? raw as Record<string, unknown>
    : {};
  const history: NodeHistory = {
    step: typeof wire.step === 'string' ? wire.step : '',
    disabled: wire.disabled === true,
    buckets: [],
    probes: [],
  };
  for (const family of HISTORY_CHART_FAMILIES) {
    Object.assign(history, HISTORY_CHART_PARSERS[family](wire));
  }
  return history;
}

// metricSeries projects ONE metric out of the buckets into a gap-aware ChartPoint list. `pick`
// selects the metric (e.g. b => b.cpuPct). Two gap sources are honored so connectNulls=false breaks
// the line correctly: (1) a bucket missing the metric → a null point at its timestamp; (2) a run of
// MISSING buckets (server omits empty buckets) → a single null sentinel inserted one step after the
// previous bucket only when MORE THAN one whole bucket is empty. One empty bucket is tolerated: at
// the 30s heartbeat floor, ordinary scheduling jitter can place two healthy consecutive samples on
// opposite sides of a bucket boundary and leave that one bucket empty. stepMs (parseGoDuration of the
// effective step) gates the sentinel; a 0/unknown step skips it (points still carry their own null avg).
function aggregateSeries<T extends { t: string }>(
  buckets: readonly T[],
  stepMs: number,
  pick: (b: T) => MetricAgg | undefined,
): ChartPoint[] {
  const out: ChartPoint[] = [];
  let prevMs: number | null = null;
  for (const b of buckets) {
    const ms = Date.parse(b.t);
    if (Number.isNaN(ms)) continue;
    if (prevMs !== null && stepMs > 0 && ms - prevMs > stepMs * 2) {
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

export function metricSeries(
  buckets: HistoryBucket[],
  stepMs: number,
  pick: (b: HistoryBucket) => MetricAgg | undefined,
): ChartPoint[] {
  return aggregateSeries(buckets, stepMs, pick);
}

function aggregateProbeSeries(
  buckets: readonly ProbeHistoryBucket[],
  stepMs: number,
  fallbackIntervalMS: number,
  pick: (bucket: ProbeHistoryBucket) => MetricAgg | undefined,
): ChartPoint[] {
  const out: ChartPoint[] = [];
  let previous: { t: number; intervalMS?: number } | null = null;
  for (const bucket of buckets) {
    const ms = Date.parse(bucket.t);
    if (Number.isNaN(ms)) continue;
    if (previous !== null) {
      // Either side of a signed schedule transition may explain the distance to the next attempt:
      // 30s→1h and 1h→30s both legitimately have a one-hour boundary segment. The query step still
      // sets the minimum useful chart cadence, while the observed series cadence (or current exact
      // policy for rc.9) supplies the fallback when one side lacks bucket metadata.
      const segmentCadence = Math.max(
        stepMs,
        previous.intervalMS ?? fallbackIntervalMS,
        bucket.intervalMS ?? fallbackIntervalMS,
      );
      if (segmentCadence > 0 && ms - previous.t > segmentCadence * 2) {
        out.push({ t: previous.t + segmentCadence, avg: null, min: null, max: null });
      }
    }
    const agg = pick(bucket);
    out.push(agg
      ? { t: ms, avg: agg.avg, min: agg.min ?? null, max: agg.max ?? null }
      : { t: ms, avg: null, min: null, max: null });
    previous = { t: ms, intervalMS: bucket.intervalMS };
  }
  return out;
}

export function probeLatencySeries(
  buckets: readonly ProbeHistoryBucket[],
  stepMs: number,
  fallbackIntervalMS = 0,
): ChartPoint[] {
  return aggregateProbeSeries(buckets, stepMs, fallbackIntervalMS, (bucket) => bucket.latencyMS);
}

export function probeAvailabilitySeries(
  buckets: readonly ProbeHistoryBucket[],
  stepMs: number,
  fallbackIntervalMS = 0,
): ChartPoint[] {
  return aggregateProbeSeries(buckets, stepMs, fallbackIntervalMS, (bucket) => {
    if (bucket.attempts === 0) return undefined;
    const pct = bucket.successes / bucket.attempts * 100;
    return { avg: pct };
  });
}

// Prefer the cadence actually observed by the controller for this exact deployed series. The
// current topology is an editable draft and may be saved-but-not-deployed or awaiting convergence;
// use it only for legacy responses that carry no series cadence at all.
export function probeHistoryFallbackIntervalMS(
  observedIntervalMS: number | undefined,
  configuredIntervalSeconds: number | undefined,
): number {
  if (
    Number.isSafeInteger(observedIntervalMS) &&
    (observedIntervalMS ?? 0) >= MIN_PROBE_INTERVAL_MS &&
    (observedIntervalMS ?? 0) <= MAX_PROBE_INTERVAL_MS &&
    (observedIntervalMS ?? 0) % 1000 === 0
  ) {
    return observedIntervalMS as number;
  }
  const configured = configuredIntervalSeconds ?? 60;
  return Number.isSafeInteger(configured) && configured >= 30 && configured <= 3600
    ? configured * 1000
    : 60_000;
}

export interface ProbeFailureSummary {
  reason: string;
  count: number;
}

export function summarizeProbeFailures(buckets: readonly ProbeHistoryBucket[]): ProbeFailureSummary[] {
  const totals = new Map<string, number>();
  for (const bucket of buckets) {
    let categorized = 0;
    for (const [reason, count] of Object.entries(bucket.failureReasons)) {
      totals.set(reason, (totals.get(reason) ?? 0) + count);
      categorized += count;
    }
    const uncategorized = bucket.failures - categorized;
    if (uncategorized > 0) totals.set('uncategorized', (totals.get('uncategorized') ?? 0) + uncategorized);
  }
  return [...totals.entries()]
    .map(([reason, count]) => ({ reason, count }))
    .sort((left, right) => right.count - left.count || left.reason.localeCompare(right.reason));
}

export function probeHistoryMatchesPolicy(
  probe: TelemetryProbe,
  series: ProbeHistorySeries,
): boolean {
  if (probe.id !== series.id || probe.type !== series.type) return false;
  if (probe.type === 'url') {
    return series.type === 'url' && probe.url === series.url &&
      (probe.expected_status || 200) === series.expectedStatus;
  }
  if (probe.type === 'tcp') {
    return series.type === 'tcp' && probe.host === series.host && probe.port === series.port;
  }
  return series.type === 'icmp' && probe.host === series.host;
}

export interface LatestRequestCoordinator<Query> {
  request: (query: Query) => void;
  // Cancels active/pending work and invalidates callbacks. It intentionally remains reusable so
  // React Strict Mode's setup→cleanup→setup probe can issue the second mount request safely.
  dispose: () => void;
}

export interface ObservedRequestScheduler<Query> {
  observe: (query: Query) => boolean;
  dispose: () => void;
}

// React dependency arrays already avoid ordinary duplicate effects, but this tiny production seam
// makes the node-specific trigger explicit and testable: an unrelated store render or a Fleet poll
// with unchanged node.last_seen cannot issue another history request.
export function createObservedRequestScheduler<Query>(options: {
  observationKey: (query: Query) => string;
  request: (query: Query) => void;
  dispose: () => void;
}): ObservedRequestScheduler<Query> {
  let lastObservation: string | null = null;
  return {
    observe(query) {
      const observation = options.observationKey(query);
      if (observation === lastObservation) return false;
      lastObservation = observation;
      options.request(query);
      return true;
    },
    dispose() {
      lastObservation = null;
      options.dispose();
    },
  };
}

export interface HistoryRefreshViewState {
  history: NodeHistory | null;
  window: [number, number] | null;
  updating: boolean;
  error: boolean;
  lastUpdatedAt: number | null;
}

export function initialHistoryRefreshViewState(): HistoryRefreshViewState {
  return { history: null, window: null, updating: true, error: false, lastUpdatedAt: null };
}

export function historyRefreshStarted(state: HistoryRefreshViewState): HistoryRefreshViewState {
  return { ...state, updating: true, error: false };
}

export function historyRefreshSucceeded(
  state: HistoryRefreshViewState,
  history: NodeHistory,
  window: [number, number],
  completedAt: number,
): HistoryRefreshViewState {
  return { ...state, history, window, error: false, lastUpdatedAt: completedAt };
}

export function historyRefreshFailed(state: HistoryRefreshViewState): HistoryRefreshViewState {
  // Deliberately retain history/window/lastUpdatedAt. A transport failure is a stale-data signal,
  // not evidence that retained history vanished.
  return { ...state, error: true };
}

export function historyRefreshIdle(state: HistoryRefreshViewState): HistoryRefreshViewState {
  return { ...state, updating: false };
}

export function createLatestRequestCoordinator<Query, Result>(options: {
  key: (query: Query) => string;
  execute: (query: Query, signal: AbortSignal) => Promise<Result>;
  onStart?: (query: Query) => void;
  onSuccess: (result: Result, query: Query) => void;
  onError: (error: unknown, query: Query) => void;
  onIdle?: () => void;
}): LatestRequestCoordinator<Query> {
  type Entry = { query: Query; key: string; controller: AbortController; generation: number };
  let generation = 0;
  let active: Entry | null = null;
  let pending: { query: Query; key: string } | null = null;

  const start = (query: Query, key: string) => {
    const entry: Entry = { query, key, controller: new AbortController(), generation };
    active = entry;
    void Promise.resolve()
      .then(() => {
        if (active !== entry || entry.generation !== generation || entry.controller.signal.aborted) {
          return Promise.reject(new DOMException('Request superseded', 'AbortError'));
        }
        options.onStart?.(query);
        return options.execute(query, entry.controller.signal);
      })
      .then((result) => {
        if (active === entry && entry.generation === generation && !entry.controller.signal.aborted) {
          options.onSuccess(result, query);
        }
      })
      .catch((error: unknown) => {
        if (active === entry && entry.generation === generation && !entry.controller.signal.aborted) {
          options.onError(error, query);
        }
      })
      .finally(() => {
        if (active !== entry || entry.generation !== generation) return;
        active = null;
        const next = pending;
        pending = null;
        if (next) {
          start(next.query, next.key);
        } else {
          options.onIdle?.();
        }
      });
  };

  return {
    request(query) {
      const key = options.key(query);
      if (active) {
        // Keep exactly one latest deferred request. A parameter/selector change aborts the old fetch;
        // a same-key Live tick coalesces and runs once after the current response has settled.
        pending = { query, key };
        if (active.key !== key) active.controller.abort();
        return;
      }
      start(query, key);
    },
    dispose() {
      generation++;
      active?.controller.abort();
      active = null;
      pending = null;
    },
  };
}
