import type {
  TelemetryProbeFailureReason,
  TelemetryProbeResult,
  TelemetryProbeResultStatus,
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
  'unexpected_status',
]);

export const DEFAULT_URL_EXPECTED_STATUS = 200;
const MAX_URL_BYTES = 2048;

function isValidIPv4Literal(raw: string): boolean {
  const parts = raw.split('.');
  return parts.length === 4 && parts.every((part) =>
    /^(0|[1-9][0-9]{0,2})$/.test(part) && Number(part) <= 255);
}

function ipv6PartUnits(parts: string[], allowIPv4: boolean): number | null {
  let units = 0;
  for (let i = 0; i < parts.length; i++) {
    const part = parts[i];
    if (part.includes('.')) {
      if (!allowIPv4 || i !== parts.length - 1 || !isValidIPv4Literal(part)) return null;
      units += 2;
      continue;
    }
    if (!/^[0-9A-Fa-f]{1,4}$/.test(part)) return null;
    units++;
  }
  return units;
}

function isValidIPv6Literal(raw: string): boolean {
  if (!raw.includes(':') || raw.includes('%') || raw.includes('[') || raw.includes(']')) return false;
  const compression = raw.indexOf('::');
  if (compression !== raw.lastIndexOf('::')) return false;
  if (compression === -1) {
    const parts = raw.split(':');
    return ipv6PartUnits(parts, true) === 8;
  }
  const left = raw.slice(0, compression);
  const right = raw.slice(compression + 2);
  const leftParts = left === '' ? [] : left.split(':');
  const rightParts = right === '' ? [] : right.split(':');
  // In an IPv6 literal, an embedded IPv4 address can only be the final 32 bits. With `::`
  // compression that means the final token on the right; `192.0.2.1::` is not an IP literal.
  const leftUnits = ipv6PartUnits(leftParts, false);
  const rightUnits = ipv6PartUnits(rightParts, true);
  return leftUnits !== null && rightUnits !== null && leftUnits + rightUnits < 8;
}

// Mirrors probepolicy.ValidHost: one bare IPv4/IPv6 literal or ASCII DNS hostname, including a
// single label and optional final root dot. Keeping this beside the URL validator gives Fleet edit
// feedback and history-selector admission one exact client boundary.
export function isValidProbeHost(raw: unknown): raw is string {
  if (typeof raw !== 'string' || raw.length === 0 || raw !== raw.trim() || raw.length > 253) {
    return false;
  }
  if (isValidIPv4Literal(raw) || isValidIPv6Literal(raw)) return true;
  const name = raw.endsWith('.') ? raw.slice(0, -1) : raw;
  if (name.length === 0 || name.length > 253 || /[/:?#[\]@]/.test(name)) return false;
  return name.split('.').every((label) =>
    label.length >= 1 && label.length <= 63 &&
    label[0] !== '-' && label[label.length - 1] !== '-' &&
    /^[A-Za-z0-9-]+$/.test(label));
}

function containsURLControlOrSpace(raw: string): boolean {
  // Go's unicode.IsControl is Unicode category Cc, not just the ASCII C0/DEL range. The signed
  // policy also rejects literal spaces everywhere (including opaque query text).
  return raw.includes(' ') || /\p{Cc}/u.test(raw);
}

function hasValidPercentEscapes(raw: string): boolean {
  for (let i = 0; i < raw.length; i++) {
    if (raw[i] !== '%') continue;
    if (i + 2 >= raw.length || !/^[0-9A-Fa-f]{2}$/.test(raw.slice(i + 1, i + 3))) return false;
    i += 2;
  }
  return true;
}

// URL() normalizes several inputs that the signed Go policy intentionally treats as distinct or
// invalid (for example an upper-case scheme, an empty userinfo marker, an empty port, or port 0).
// Check the raw authority first so Fleet reports the same validity that preview/stage will enforce.
function hasValidRawHTTPAuthority(raw: string): boolean {
  const schemePrefix = /^https?:\/\//i.exec(raw)?.[0] ?? '';
  if (schemePrefix === '') return false;

  const authorityStart = schemePrefix.length;
  const pathStart = raw.slice(authorityStart).search(/[/?#]/);
  const authority = pathStart === -1
    ? raw.slice(authorityStart)
    : raw.slice(authorityStart, authorityStart + pathStart);
  // The portable signed surface has no userinfo or authority escapes. WHATWG would normalize
  // `%65xample.test` while Go net/url rejects it; zones use the same percent syntax and are also
  // deliberately outside the closed probe contract.
  if (authority === '' || authority.includes('@') || authority.includes('%')) return false;

  let rawHost: string;
  let rawPort: string | undefined;
  if (authority.startsWith('[')) {
    const closingBracket = authority.indexOf(']');
    if (closingBracket === -1) return false;
    rawHost = authority.slice(1, closingBracket);
    if (!isValidIPv6Literal(rawHost)) return false;
    const suffix = authority.slice(closingBracket + 1);
    if (suffix !== '') {
      if (!suffix.startsWith(':')) return false;
      rawPort = suffix.slice(1);
    }
  } else {
    if (authority.includes('[') || authority.includes(']')) return false;
    if ((authority.match(/:/g) ?? []).length > 1) return false;
    const colon = authority.lastIndexOf(':');
    rawHost = colon === -1 ? authority : authority.slice(0, colon);
    if (!isValidProbeHost(rawHost)) return false;
    if (colon !== -1) rawPort = authority.slice(colon + 1);
  }
  if (rawPort === undefined) return true;
  if (!/^\d+$/.test(rawPort)) return false;
  const port = Number(rawPort);
  return Number.isSafeInteger(port) && port >= 1 && port <= 65535;
}

export function isValidProbeURL(raw: unknown): raw is string {
  if (
    typeof raw !== 'string' ||
    raw.length === 0 ||
    raw !== raw.trim() ||
    containsURLControlOrSpace(raw) ||
    !hasValidPercentEscapes(raw.slice(0, raw.indexOf('?') === -1 ? raw.length : raw.indexOf('?'))) ||
    raw.includes('#') ||
    !hasValidRawHTTPAuthority(raw) ||
    new TextEncoder().encode(raw).length > MAX_URL_BYTES
  ) {
    return false;
  }
  try {
    const parsed = new URL(raw);
    return (parsed.protocol === 'http:' || parsed.protocol === 'https:') &&
      parsed.hostname.length > 0 &&
      parsed.username === '' &&
      parsed.password === '';
  } catch {
    return false;
  }
}

export function isValidProbeID(raw: unknown): raw is string {
  return typeof raw === 'string' && PROBE_ID.test(raw);
}

export interface ProbeResultWire {
  id?: unknown;
  type?: unknown;
  host?: unknown;
  port?: unknown;
  url?: unknown;
  expected_status?: unknown;
  actual_status?: unknown;
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
      !isValidProbeID(wire.id) ||
      seen.has(wire.id) ||
      (wire.type !== 'icmp' && wire.type !== 'tcp' && wire.type !== 'url') ||
      (wire.status !== 'pending' && wire.status !== 'success' && wire.status !== 'failure')
    ) {
      continue;
    }

    const latencyValid = typeof wire.latency_ms === 'number' &&
      Number.isFinite(wire.latency_ms) && wire.latency_ms >= 0;
    const checkedAtValid = typeof wire.checked_at === 'string' && wire.checked_at.length > 0;
    const failureReasonValid = typeof wire.failure_reason === 'string' &&
      FAILURE_REASONS.has(wire.failure_reason as TelemetryProbeFailureReason);
    const common = {
      id: wire.id,
      status: wire.status as TelemetryProbeResultStatus,
      ...(latencyValid ? { latencyMS: wire.latency_ms as number } : {}),
      ...(checkedAtValid ? { checkedAt: wire.checked_at as string } : {}),
      ...(failureReasonValid
        ? { failureReason: wire.failure_reason as TelemetryProbeFailureReason }
        : {}),
    };

    let result: TelemetryProbeResult;
    if (wire.type === 'url') {
      const rawURL = wire.url;
      if (
        wire.host !== undefined ||
        wire.port !== undefined ||
        !isValidProbeURL(rawURL) ||
        typeof wire.expected_status !== 'number' ||
        !Number.isInteger(wire.expected_status) ||
        wire.expected_status < 100 ||
        wire.expected_status > 599
      ) {
        continue;
      }
      const expectedStatus = wire.expected_status;
      const actualStatus = wire.actual_status;
      const hasActualStatus = typeof actualStatus === 'number' &&
        Number.isInteger(actualStatus) && actualStatus >= 100 && actualStatus <= 599;
      const validOutcome = wire.status === 'pending'
        ? actualStatus === undefined && wire.latency_ms === undefined &&
          wire.checked_at === undefined && wire.failure_reason === undefined
        : wire.status === 'success'
          ? hasActualStatus && actualStatus === expectedStatus &&
            latencyValid && checkedAtValid && wire.failure_reason === undefined
          : wire.failure_reason === 'unexpected_status'
            ? hasActualStatus && actualStatus !== expectedStatus && latencyValid && checkedAtValid
            : actualStatus === undefined && wire.latency_ms === undefined && checkedAtValid &&
              failureReasonValid && wire.failure_reason !== 'unexpected_status';
      if (!validOutcome) continue;
      result = {
        ...common,
        type: 'url',
        url: rawURL,
        expectedStatus,
        ...(hasActualStatus ? { actualStatus } : {}),
      };
    } else {
      if (
        wire.url !== undefined ||
        wire.expected_status !== undefined ||
        wire.actual_status !== undefined ||
        typeof wire.host !== 'string' ||
        wire.host.length === 0 ||
        wire.host.length > 253 ||
        (wire.type === 'icmp' && wire.port !== undefined) ||
        (wire.type === 'tcp' && (
          typeof wire.port !== 'number' ||
          !Number.isInteger(wire.port) ||
          wire.port < 1 ||
          wire.port > 65535
        ))
      ) {
        continue;
      }
      const validLegacyOutcome = wire.status === 'pending'
        ? wire.latency_ms === undefined && wire.checked_at === undefined && wire.failure_reason === undefined
        : wire.status === 'success'
          ? latencyValid && checkedAtValid && wire.failure_reason === undefined
          : wire.latency_ms === undefined && checkedAtValid && failureReasonValid &&
            wire.failure_reason !== 'unexpected_status';
      if (!validLegacyOutcome) continue;
      result = wire.type === 'tcp'
        ? { ...common, type: 'tcp', host: wire.host, port: wire.port as number }
        : { ...common, type: 'icmp', host: wire.host };
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

export function effectiveExpectedStatus(
  probe: { expected_status?: number },
): number {
  return probe.expected_status || DEFAULT_URL_EXPECTED_STATUS;
}

export function formatProbeDestination(probe: TelemetryProbe | TelemetryProbeResult): string {
  if (probe.type === 'url') return probe.url;
  return formatProbeTarget(probe.host, probe.type === 'tcp' ? probe.port : undefined);
}

export function probeDestinationMissing(probe: TelemetryProbe): boolean {
  return probe.type === 'url' ? probe.url.trim() === '' : probe.host.trim() === '';
}

export function probeDestinationInvalid(probe: TelemetryProbe): boolean {
  return probe.type === 'url' ? !isValidProbeURL(probe.url) : !isValidProbeHost(probe.host);
}

export function probeExpectedStatusInvalid(probe: TelemetryProbe): boolean {
  if (probe.type !== 'url') return false;
  const status = effectiveExpectedStatus(probe);
  return !Number.isSafeInteger(status) || status < 100 || status > 599;
}

export function telemetryProbeWithType(
  probe: TelemetryProbe,
  type: TelemetryProbe['type'],
): TelemetryProbe {
  if (probe.type === type) return probe;
  const common = {
    id: probe.id,
    ...(probe.name ? { name: probe.name } : {}),
    ...(probe.interval_seconds === undefined ? {} : { interval_seconds: probe.interval_seconds }),
    ...(probe.timeout_milliseconds === undefined ? {} : { timeout_milliseconds: probe.timeout_milliseconds }),
  };
  if (type === 'url') {
    return { ...common, type: 'url', url: '', expected_status: DEFAULT_URL_EXPECTED_STATUS };
  }
  const host = probe.type === 'url' ? '' : probe.host;
  if (type === 'tcp') return { ...common, type: 'tcp', host, port: 443 };
  return { ...common, type: 'icmp', host };
}

// probeDisplayName is presentation-only. Empty/absent names fall back to the immutable ID; callers
// must continue using id + exact executable destination for matching and history requests.
export function probeDisplayName(probe: Pick<TelemetryProbe, 'id' | 'name'>): string {
  return probe.name?.trim() || probe.id;
}

// A live result belongs to a draft policy row only when both its stable ID and its executable
// destination agree. This prevents a result from the previously deployed target being displayed as
// proof about an edited-but-not-yet-deployed typed destination or URL success contract.
export function probeResultMatchesPolicy(
  probe: TelemetryProbe,
  result: TelemetryProbeResult,
): boolean {
  return (
    probe.id === result.id &&
    probe.type === result.type &&
    (probe.type === 'url'
      ? result.type === 'url' &&
        probe.url === result.url &&
        effectiveExpectedStatus(probe) === result.expectedStatus
      : result.type !== 'url' &&
        probe.host === result.host &&
        (probe.type === 'tcp' ? probe.port : undefined) ===
          (result.type === 'tcp' ? result.port : undefined))
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
    if (
      other === undefined ||
      probe.id !== other.id ||
      (probe.name || undefined) !== (other.name || undefined) ||
      probe.type !== other.type ||
      (probe.interval_seconds ?? undefined) !== (other.interval_seconds ?? undefined) ||
      (probe.timeout_milliseconds ?? undefined) !== (other.timeout_milliseconds ?? undefined)
    ) {
      return false;
    }
    if (probe.type === 'url') {
      return other.type === 'url' && probe.url === other.url &&
        effectiveExpectedStatus(probe) === effectiveExpectedStatus(other);
    }
    if (probe.type === 'tcp') {
      return other.type === 'tcp' && probe.host === other.host && probe.port === other.port;
    }
    return other.type === 'icmp' && probe.host === other.host;
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
