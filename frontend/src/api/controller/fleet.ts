// Fleet-registry client routes (operator-only): the node list + audit chain reads, the stored
// topology read, enrollment-token minting, topology upload, node eviction / key-rotation ops, and
// the manual-node bundle download. Also owns the snake_case → camelCase boundary mappers that
// getNodes projects each node (and its telemetry sub-objects) through.

import { request, postJSON, ControllerError, type ControllerConfig } from './transport';
import type {
  ControllerNode,
  ControllerAuditEntry,
  WireGuardPeer,
  NodeResource,
  NativeXDP,
  MimicCapability,
} from '../../types/controller';
import { mapNodeConditions, type ConditionWire } from '../../lib/nodeConditions';
import { mapProbeResults } from '../../lib/probeResults';
import { mapDeviceTelemetry } from '../../lib/deviceTelemetry';
import { parseContentDispositionFilename } from '../../lib/download';

// --- backend snake_case response shapes (used only inside this module, discarded after mapping) ---

interface NodeJSON {
  node_id: string;
  status: string;
  has_wg_public_key: boolean;
  desired_generation: number;
  applied_generation: number;
  last_checksum: string;
  last_health: string;
  agent_version?: string;
  last_seen: string;
  enrolled_at: string;
  rekey_requested: boolean;
  in_rollout?: boolean;
  conditions?: ConditionWire[];
  // The agent's extensible telemetry metrics map (served verbatim). This boundary maps the known
  // Fleet live projections; unknown keys remain forward-compatible and are ignored here.
  telemetry?: {
    wireguard_peers?: WireGuardPeerWire[];
    resource?: ResourceMetricWire;
    probe_results?: unknown;
    device_inventory?: unknown;
    device_samples?: unknown;
    native_xdp?: NativeXDPWire;
    mimic_capability?: MimicCapabilityWire;
    agent_capabilities?: unknown;
  } | null;
}

// ResourceMetricWire mirrors the agent's host resource metric (snake_case wire shape under
// telemetry.resource), mapped to NodeResource at the boundary. All fields optional (defensive).
interface ResourceMetricWire {
  cpu_pct?: number;
  load1?: number;
  load5?: number;
  load15?: number;
  mem_total_kb?: number;
  mem_available_kb?: number;
}

// NativeXDPWire mirrors the agent's egress-NIC native-XDP capability heuristic (snake_case wire shape
// under telemetry.native_xdp), mapped to NativeXDP at the boundary. All fields optional (defensive).
interface NativeXDPWire {
  capability?: string;
  driver?: string;
  kernel?: string;
}

// MimicCapabilityWire mirrors the agent's "can this node run mimic" heuristic (snake_case wire shape
// under telemetry.mimic_capability), mapped to MimicCapability at the boundary. All fields optional.
interface MimicCapabilityWire {
  capability?: string;
  kernel?: string;
}

// WireGuardPeerWire mirrors the agent's per-peer link health (snake_case wire shape under
// telemetry.wireguard_peers), mapped to the camelCase WireGuardPeer at the boundary.
interface WireGuardPeerWire {
  peer?: string;
  interface?: string;
  endpoint?: string;
  last_handshake?: number;
  status?: string;
}

interface AuditEntryJSON {
  timestamp: string;
  actor: string;
  action: string;
  node_id: string;
}

interface AuditResponseJSON {
  entries: AuditEntryJSON[] | null;
  verified: boolean;
}

interface EnrollmentTokenResponseJSON {
  token: string;
  warning?: string;
}

// MintTokenResult is the operator-facing result of minting an enrollment token: the
// plaintext token (shown once) plus an optional non-blocking design-membership
// warning (plan-6: set when the node-id is absent from the stored design).
export interface MintTokenResult {
  token: string;
  warning: string;
}

interface RevokeResponseJSON {
  node_id: string;
  revoked: boolean;
}

interface RekeyAllResponseJSON {
  requested: number;
}

interface ClearRekeyResponseJSON {
  node_id: string;
  cleared: boolean;
}

// --- snake_case → camelCase mapping ---

function mapNode(n: NodeJSON): ControllerNode {
  const devices = mapDeviceTelemetry(
    n.telemetry?.device_inventory,
    n.telemetry?.device_samples,
  );
  return {
    nodeId: n.node_id,
    status: n.status as ControllerNode['status'],
    hasWGPublicKey: n.has_wg_public_key,
    desiredGeneration: n.desired_generation,
    appliedGeneration: n.applied_generation,
    lastChecksum: n.last_checksum,
    lastHealth: n.last_health,
    agentVersion: n.agent_version ?? '',
    lastSeen: n.last_seen,
    enrolledAt: n.enrolled_at,
    rekeyRequested: n.rekey_requested,
    inRollout: n.in_rollout ?? false,
    conditions: mapNodeConditions(n.conditions),
    wireguardPeers: mapWireGuardPeers(n.telemetry?.wireguard_peers),
    resource: mapResource(n.telemetry?.resource),
    probeResults: mapProbeResults(n.telemetry?.probe_results),
    deviceInventory: devices.inventory,
    deviceSamples: devices.samples,
    nativeXDP: mapNativeXDP(n.telemetry?.native_xdp),
    mimicCapability: mapMimicCapability(n.telemetry?.mimic_capability),
    agentCapabilities: mapAgentCapabilities(n.telemetry?.agent_capabilities),
  };
}

const agentCapabilityPattern = /^[a-z0-9][a-z0-9-]{0,62}$/;
const maxAgentCapabilities = 16;

// mapAgentCapabilities accepts only the controller's exact canonical latest-heartbeat form: at most
// 16 valid tokens, already sorted and unique. It does not sort, deduplicate, or truncate malformed
// evidence client-side because doing so could turn an invalid compatibility claim into a false
// "Ready" state. A valid empty set remains [] so Fleet can distinguish it from not-confirmed.
export function mapAgentCapabilities(w: unknown): string[] | undefined {
  if (
    !w ||
    typeof w !== 'object' ||
    Array.isArray(w) ||
    Object.keys(w).length !== 1 ||
    !Object.prototype.hasOwnProperty.call(w, 'capabilities')
  ) {
    return undefined;
  }
  const raw = (w as { capabilities: unknown }).capabilities;
  if (!Array.isArray(raw) || raw.length > maxAgentCapabilities) return undefined;
  const capabilities: string[] = [];
  for (const value of raw) {
    if (typeof value !== 'string' || !agentCapabilityPattern.test(value)) return undefined;
    if (capabilities.length > 0 && capabilities[capabilities.length - 1] >= value) return undefined;
    capabilities.push(value);
  }
  return capabilities;
}

// mapResource projects the agent's host resource metric (snake_case) to NodeResource. Defensive: a
// missing metric or a non-numeric load1 (garbled input) yields undefined so the panel renders nothing;
// each numeric field is coerced (NaN/absent → 0).
function mapResource(r: ResourceMetricWire | undefined): NodeResource | undefined {
  if (!r || typeof r.load1 !== 'number') return undefined;
  const num = (v: number | undefined): number => (typeof v === 'number' && Number.isFinite(v) ? v : 0);
  const out: NodeResource = {
    load1: num(r.load1),
    load5: num(r.load5),
    load15: num(r.load15),
    memTotalKB: num(r.mem_total_kb),
    memAvailableKB: num(r.mem_available_kb),
  };
  // cpu_pct is OPTIONAL — map it ONLY when the agent reported it (a real number). Never default to 0:
  // absent means "unknown" (old agent / first beat / wrapped counter), which the panel shows as a gap.
  if (typeof r.cpu_pct === 'number' && Number.isFinite(r.cpu_pct)) {
    out.cpuPct = r.cpu_pct;
  }
  return out;
}

// mapNativeXDP projects the agent's egress-NIC native-XDP capability heuristic (snake_case) to
// NativeXDP. Defensive: a missing metric or a capability outside the closed set yields undefined so the
// panel renders no indicator; driver/kernel default to "".
export function mapNativeXDP(w: NativeXDPWire | undefined): NativeXDP | undefined {
  if (!w) return undefined;
  const cap = w.capability;
  if (cap !== 'supported' && cap !== 'conditional' && cap !== 'unsupported' && cap !== 'unknown') return undefined;
  return { capability: cap, driver: w.driver ?? '', kernel: w.kernel ?? '' };
}

// mapMimicCapability projects the agent's "can this node run mimic" heuristic (snake_case) to
// MimicCapability. Defensive: a missing metric or a capability outside the closed set yields undefined
// so the panel renders no warning (a pre-plan-3 agent / before the first heartbeat); kernel defaults to "".
export function mapMimicCapability(w: MimicCapabilityWire | undefined): MimicCapability | undefined {
  if (!w) return undefined;
  const cap = w.capability;
  if (cap !== 'ready' && cap !== 'buildable' && cap !== 'unbuildable') return undefined;
  return { capability: cap, kernel: w.kernel ?? '' };
}

// mapWireGuardPeers projects the agent's per-peer link telemetry (snake_case) to WireGuardPeer[].
// Defensive: tolerates a missing/garbled metric (returns []), and coerces an unknown status to
// 'never' (the safe "not up" default) so the panel never renders an unhandled state.
function mapWireGuardPeers(peers: WireGuardPeerWire[] | undefined): WireGuardPeer[] {
  if (!Array.isArray(peers)) return [];
  return peers.map((p) => ({
    peer: p.peer ?? p.interface ?? '',
    interface: p.interface ?? '',
    endpoint: p.endpoint ?? '',
    lastHandshake: typeof p.last_handshake === 'number' ? p.last_handshake : 0,
    status: p.status === 'up' || p.status === 'stale' ? p.status : 'never',
  }));
}

function mapAuditEntry(e: AuditEntryJSON): ControllerAuditEntry {
  return {
    timestamp: e.timestamp,
    actor: e.actor,
    action: e.action,
    nodeId: e.node_id,
  };
}

// --- public API (each takes (cfg, ...)) ---

// getNodes lists the entire fleet registry (operator-only).
export async function getNodes(cfg: ControllerConfig): Promise<ControllerNode[]> {
  const res = await request(cfg, 'nodes', { method: 'GET' });
  const data = (await res.json()) as NodeJSON[] | null;
  return (data ?? []).map(mapNode);
}

// getAudit fetches the audit chain together with whether it is complete and verifiable
// (operator-only).
export async function getAudit(
  cfg: ControllerConfig
): Promise<{ entries: ControllerAuditEntry[]; verified: boolean }> {
  const res = await request(cfg, 'audit', { method: 'GET' });
  const data = (await res.json()) as AuditResponseJSON;
  return {
    entries: (data.entries ?? []).map(mapAuditEntry),
    verified: data.verified,
  };
}

// getTopology retrieves the currently stored topology JSON (operator-only). It returns
// unknown: the stored bytes are a public-keys-only topology, and this layer imposes no
// structure (the caller interprets it as needed). A 404 (no topology stored yet on the
// server — before the first deploy) returns null so the caller keeps the local canvas; any
// other error throws as usual.
export async function getTopology(cfg: ControllerConfig): Promise<unknown | null> {
  try {
    const res = await request(cfg, 'topology', { method: 'GET' });
    return (await res.json()) as unknown;
  } catch (err) {
    // request() throws ControllerError on non-2xx; 404 = "no topology stored yet" (the normal
    // first-run shape), surfaced as null. Match on the typed status, not the message string.
    if (err instanceof ControllerError && err.status === 404) {
      return null;
    }
    throw err;
  }
}

// mintEnrollmentToken mints a one-time enrollment token for a node, returning the plaintext
// token (shown only this once).
export async function mintEnrollmentToken(
  cfg: ControllerConfig,
  nodeId: string,
  ttlSeconds: number
): Promise<MintTokenResult> {
  const res = await postJSON(
    cfg,
    'enrollment-token',
    JSON.stringify({ node_id: nodeId, ttl_seconds: ttlSeconds })
  );
  const data = (await res.json()) as EnrollmentTokenResponseJSON;
  return { token: data.token, warning: data.warning ?? '' };
}

// updateTopology uploads a new topology version (operator-only). topoJSON is the serialized
// model.Topology JSON string, submitted verbatim as the request body.
export async function updateTopology(
  cfg: ControllerConfig,
  topoJSON: string
): Promise<void> {
  await postJSON(cfg, 'update-topology', topoJSON);
}

// revoke evicts a node (operator-only); its bearer credential is invalidated immediately.
export async function revoke(cfg: ControllerConfig, nodeId: string): Promise<void> {
  const res = await postJSON(cfg, 'revoke', JSON.stringify({ node_id: nodeId }));
  // Consume the response body to free the connection; the revoked flag is always true on
  // success, so the caller needs no branch.
  await (res.json() as Promise<RevokeResponseJSON>);
}

// rekeyAll requests a WG key rotation for the whole fleet (operator-only, plan-4.6 ROUTINE
// tier): it marks every approved node RekeyRequested. This is the start of the zero-knowledge
// flow — the controller never touches private keys; each agent regenerates its own local key
// and registers the new public key via /rekey. It returns the number of nodes marked. Note:
// after marking, one more Deploy is required — only once the nodes re-register their new public
// keys does the next generation carry everyone's new public keys and let the fleet converge.
export async function rekeyAll(cfg: ControllerConfig): Promise<{ requested: number }> {
  const res = await postJSON(cfg, 'rekey-all', '');
  const data = (await res.json()) as RekeyAllResponseJSON;
  return { requested: data.requested };
}

// clearRekey clears a single node's pending rekey mark (operator-only) without evicting it —
// the node keeps its approval status and bearer credential (unlike revoke). It is used to
// release a stuck "Roll keys" straggler (an offline/dead node, or a mistakenly triggered
// fleet-wide rotation); otherwise the panel keeps warning on each deploy. Idempotent:
// returns cleared:false when there is no pending rekey mark.
export async function clearRekey(cfg: ControllerConfig, nodeId: string): Promise<{ cleared: boolean }> {
  const res = await postJSON(cfg, 'clear-rekey', JSON.stringify({ node_id: nodeId }));
  const data = (await res.json()) as ClearRekeyResponseJSON;
  return { cleared: data.cleared };
}

// downloadManualNodeBundle fetches a MANUAL node's promoted, off-host-signed install bundle as a ZIP
// (operator-only; GET <operator>/manual-node-bundle?node=<id>, the backend handler_manual_node.go). A
// managed node's agent pulls its config from /config; a manual (agent-less) node has no agent, so the
// operator downloads the same served bundle here and installs it by hand. The bundle carries
// PRIVATEKEY_PLACEHOLDER, never real key material (install.sh splices the on-box key), so zero-knowledge
// holds. Returns the blob + the server-suggested filename (the caller triggers the browser download).
export async function downloadManualNodeBundle(
  cfg: ControllerConfig,
  nodeId: string,
): Promise<{ blob: Blob; filename: string }> {
  const res = await request(cfg, `manual-node-bundle?node=${encodeURIComponent(nodeId)}`);
  const blob = await res.blob();
  // The backend (handler_manual_node.go) sets `Content-Disposition: attachment; filename="<id>-bundle.zip"`;
  // fall back to that deterministic name if the header is absent. parseContentDispositionFilename only
  // percent-decodes the RFC 5987 `filename*` form, so a plain filename with a literal '%' is safe.
  const filename = parseContentDispositionFilename(res, `${nodeId}-bundle.zip`);
  return { blob, filename };
}
