import type {
  TelemetryProbeFailureReason,
  TelemetryProbeResult,
} from '../types/controller';
import type { TelemetryProbe } from '../types/topology';

const MAX_PROBE_RESULTS = 16;
const PROBE_ID = /^[A-Za-z0-9._-]{1,63}$/;
const FAILURE_REASONS = new Set<TelemetryProbeFailureReason>([
  'dns_failed',
  'timeout',
  'permission_denied',
  'connection_refused',
  'network_unreachable',
  'network_error',
]);

export interface ProbeResultWire {
  id?: unknown;
  type?: unknown;
  host?: unknown;
  port?: unknown;
  // Newer agents may echo the effective cadence for server-side history bucketing. The live latest-
  // result UI does not need it, but keep the additive wire field named here for drift checking.
  interval_ms?: unknown;
  status?: unknown;
  latency_ms?: unknown;
  checked_at?: unknown;
  failure_reason?: unknown;
}

// mapProbeResults is the defensive snake_case wire boundary for telemetry.probe_results. A malformed
// row is ignored instead of poisoning the entire Fleet response; duplicate IDs are collapsed to the
// first valid row and the signed-policy maximum is enforced again client-side.
export function mapProbeResults(raw: unknown): TelemetryProbeResult[] {
  if (!Array.isArray(raw)) return [];

  const out: TelemetryProbeResult[] = [];
  const seen = new Set<string>();
  for (const candidate of raw) {
    if (out.length >= MAX_PROBE_RESULTS) break;
    if (!candidate || typeof candidate !== 'object') continue;
    const wire = candidate as ProbeResultWire;
    if (
      typeof wire.id !== 'string' ||
      !PROBE_ID.test(wire.id) ||
      seen.has(wire.id) ||
      (wire.type !== 'icmp' && wire.type !== 'tcp') ||
      (wire.type === 'icmp' && wire.port !== undefined) ||
      (wire.type === 'tcp' && wire.port === undefined) ||
      typeof wire.host !== 'string' ||
      wire.host.length === 0 ||
      wire.host.length > 253 ||
      (wire.status !== 'pending' && wire.status !== 'success' && wire.status !== 'failure')
    ) {
      continue;
    }
    if (
      wire.port !== undefined &&
      (typeof wire.port !== 'number' ||
        !Number.isInteger(wire.port) ||
        wire.port < 1 ||
        wire.port > 65535)
    ) {
      continue;
    }

    const result: TelemetryProbeResult = {
      id: wire.id,
      type: wire.type,
      host: wire.host,
      status: wire.status,
    };
    if (wire.port !== undefined) result.port = wire.port;
    if (
      typeof wire.latency_ms === 'number' &&
      Number.isFinite(wire.latency_ms) &&
      wire.latency_ms >= 0
    ) {
      result.latencyMS = wire.latency_ms;
    }
    if (typeof wire.checked_at === 'string' && wire.checked_at.length > 0) {
      result.checkedAt = wire.checked_at;
    }
    if (
      typeof wire.failure_reason === 'string' &&
      FAILURE_REASONS.has(wire.failure_reason as TelemetryProbeFailureReason)
    ) {
      result.failureReason = wire.failure_reason as TelemetryProbeFailureReason;
    }
    seen.add(result.id);
    out.push(result);
  }
  return out;
}

export interface ProbeResultSummary {
  state: 'none' | 'success' | 'pending' | 'failure';
  total: number;
  success: number;
  pending: number;
  failure: number;
}

// TCP targets use brackets around IPv6 literals so the separately stored port is visually
// unambiguous. The signed policy itself keeps host and port as distinct fields.
export function formatProbeTarget(host: string, port: number | undefined): string {
  if (port === undefined) return host;
  return host.includes(':') ? `[${host}]:${port}` : `${host}:${port}`;
}

// probeDisplayName is presentation-only. Empty/absent names fall back to the immutable ID; callers
// must continue using id + exact executable destination for matching and history requests.
export function probeDisplayName(probe: Pick<TelemetryProbe, 'id' | 'name'>): string {
  return probe.name?.trim() || probe.id;
}

// A live result belongs to a draft policy row only when both its stable ID and its executable
// destination agree. This prevents a result from the previously deployed target being displayed as
// proof about an edited-but-not-yet-deployed host or port.
export function probeResultMatchesPolicy(
  probe: TelemetryProbe,
  result: TelemetryProbeResult,
): boolean {
  return (
    probe.id === result.id &&
    probe.type === result.type &&
    probe.host === result.host &&
    (probe.port ?? undefined) === (result.port ?? undefined)
  );
}

// Policy order is intentional in the design JSON. Compare the complete typed fields without relying
// on object key insertion order, which can differ between a server round-trip and a local edit.
export function sameTelemetryPolicy(
  left: readonly TelemetryProbe[],
  right: readonly TelemetryProbe[],
): boolean {
  return left.length === right.length && left.every((probe, index) => {
    const other = right[index];
    return other !== undefined &&
      probe.id === other.id &&
      (probe.name || undefined) === (other.name || undefined) &&
      probe.type === other.type &&
      probe.host === other.host &&
      (probe.port ?? undefined) === (other.port ?? undefined) &&
      (probe.interval_seconds ?? undefined) === (other.interval_seconds ?? undefined) &&
      (probe.timeout_milliseconds ?? undefined) === (other.timeout_milliseconds ?? undefined);
  });
}

// summarizeProbeResults treats configured probes with no matching deployed result as pending. A
// result for a removed or changed destination remains visible until the next heartbeat converges. A
// changed destination therefore contributes one current pending row plus the still-deployed result,
// instead of misattributing the old outcome to the new target.
export function summarizeProbeResults(
  configured: readonly TelemetryProbe[],
  results: readonly TelemetryProbeResult[],
): ProbeResultSummary {
  const byID = new Map(results.map((result) => [result.id, result]));
  const configuredByID = new Map(configured.map((probe) => [probe.id, probe]));
  let success = 0;
  let pending = 0;
  let failure = 0;
  for (const probe of configured) {
    const candidate = byID.get(probe.id);
    const status = candidate && probeResultMatchesPolicy(probe, candidate)
      ? candidate.status
      : undefined;
    if (status === 'success') success++;
    else if (status === 'failure') failure++;
    else pending++;
  }
  for (const result of results) {
    const configuredProbe = configuredByID.get(result.id);
    if (configuredProbe && probeResultMatchesPolicy(configuredProbe, result)) continue;
    if (result.status === 'success') success++;
    else if (result.status === 'failure') failure++;
    else pending++;
  }
  const total = success + pending + failure;
  const state = total === 0 ? 'none' : failure > 0 ? 'failure' : pending > 0 ? 'pending' : 'success';
  return { state, total, success, pending, failure };
}
