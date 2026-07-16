// Frontend data model — kept consistent with the Go backend model

export interface Topology {
  project: Project;
  domains: Domain[];
  nodes: Node[];
  edges: Edge[];
  route_policies?: RoutePolicy[];
  // Allocation-scheme version number: written by the compiler and echoed back verbatim, so that a future
  // pin-format change can detect/migrate old allocations.
  // See docs/spec/compiler/allocation-stability.md (invariant I10).
  alloc_schema_version?: number;
}

export interface Project {
  id: string;
  name: string;
  description?: string;
  version?: string;
}

export interface Domain {
  id: string;
  name: string;
  cidr: string;
  description?: string;
  allocation_mode: 'auto' | 'manual';
  routing_mode: 'static' | 'babel' | 'none';
  reserved_ranges?: string[];
  // Per-domain transit address pool, defaulting to 10.10.0.0/24; transit IPs are now allocated per domain from this CIDR.
  // Corresponds to Domain.TransitCIDR in the Go model; see docs/spec/api/wire-contract.md (Domain field table).
  transit_cidr?: string;
}

export interface Node {
  id: string;
  name: string;
  hostname?: string;
  platform?: 'debian' | 'ubuntu';
  role: 'peer' | 'router' | 'relay' | 'gateway' | 'client';
  // Deployment mode (controller mode): 'managed' (default/empty) = agent-managed; 'manual' =
  // hand-deployed, no agent, carries its own pre-known public key + endpoint. Orthogonal to role.
  // See implementation_plans/mixed-controller-local-mode-2026_06_25.
  deployment_mode?: 'managed' | 'manual';
  domain_id: string;
  overlay_ip?: string;
  mtu?: number;
  // XDP attach mode for mimic (transport=tcp): empty/'skb' = generic XDP (default, best compatibility);
  // 'native' = driver-level XDP (faster, requires NIC support). See docs/spec/artifacts/mimic.md for details.
  xdp_mode?: 'skb' | 'native';
  // Overrides the auto-detected mimic egress interface ("" / omitted = auto-detect from the default
  // route). Set for multi-homed / policy-routing nodes (e.g. "wan0"). Only meaningful on a tcp link.
  mimic_egress_interface?: string;
  // Babel router-id; auto-generated from the node id when empty (see internal/renderer/babel.go).
  // MAC-48 form (e.g. 02:11:22:33:44:55) or an IPv4 address; meaningless for the client role.
  router_id?: string;
  capabilities: NodeCapabilities;
  fixed_private_key?: boolean;
  wireguard_private_key?: string;
  wireguard_public_key?: string;
  public_endpoints?: PublicEndpoint[];
  extra_prefixes?: string[];
  // SSH connection details (for auto-deploy)
  ssh_alias?: string;
  ssh_host?: string;
  ssh_port?: number;
  ssh_user?: string;
  ssh_key_path?: string;
  // Optional active-connectivity checks run by this managed node. Each signed policy names one
  // destination host and, for TCP, one port; it is deliberately not an arbitrary URL/command.
  telemetry_probes?: TelemetryProbe[];
}

export interface TelemetryProbe {
  id: string;
  type: 'icmp' | 'tcp';
  host: string;
  port?: number;
  interval_seconds?: number;
  timeout_milliseconds?: number;
}

export interface PublicEndpoint {
  id: string;
  host: string;
  port: number;
  note?: string;
}

export interface NodeCapabilities {
  can_accept_inbound: boolean;
  can_forward: boolean;
  can_relay: boolean;
  has_public_ip: boolean;
}

export interface Edge {
  id: string;
  from_node_id: string;
  to_node_id: string;
  type: 'direct' | 'public-endpoint' | 'relay-path' | 'candidate';
  endpoint_host?: string;
  endpoint_port?: number;  // user input: 0 = auto, nonzero = NAT override
  compiled_port?: number;  // read-only: actual port set by compiler
  priority?: number;
  weight?: number;
  // Link role for parallel links: 'backup' forms its own independent link (independent WG interface/port/transit/link-local address),
  // while no role or 'primary' belongs to the "primary-link class" (A->B + B->A for the same node pair are still merged into a single bidirectional tunnel).
  // See docs/spec/data-model/edge.md (§Parallel links).
  role?: 'primary' | 'backup';
  transport?: 'udp' | 'tcp';
  // Per-link mimic→UDP fallback POLICY (plan-4). undefined/omitted = inherit fleet default; 'udp' =
  // fall back to plain UDP if mimic provisioning fails; 'none' = fail closed. Only meaningful on a
  // tcp edge. See docs/spec/data-model/edge.md §TCP transport.
  mimic_fallback?: 'udp' | 'none';
  // Per-edge dial-direction POLICY. undefined/omitted/'both' = both sides may initiate (today's
  // behavior); 'forward' = only from→to initiates (dials the required endpoint_host), the reverse
  // peer keeps its [Peer] stanza but carries no Endpoint. There is no 'reverse' (D11, one
  // spelling): single-linking the other way is expressed by flipping the edge. Pure policy: gates
  // only which peer gets a dial Endpoint, never allocation. See docs/spec/data-model/edge.md
  // §Link direction.
  link_direction?: 'both' | 'forward';
  is_enabled: boolean;
  notes?: string;
  // Allocation pins: written by the compiler and echoed back verbatim, so that a recompile preserves existing
  // allocations (port / transit IP / link-local address), so adding new nodes does not disturb existing links. See
  // docs/spec/compiler/allocation-stability.md.
  pinned_from_port?: number;
  pinned_to_port?: number;
  pinned_from_transit_ip?: string;
  pinned_to_transit_ip?: string;
  pinned_from_link_local?: string;
  pinned_to_link_local?: string;
}

// RESERVED feature: route_policies is not wired into any renderer yet, and semantic validation rejects a non-empty array.
// This type is kept only for wire compatibility — do not build features on top of it.
// See docs/spec/api/wire-contract.md ("route_policies is RESERVED").
export interface RoutePolicy {
  id: string;
  domain_id: string;
  destination_cidr: string;
  next_hop_node_id?: string;
  metric?: number;
  notes?: string;
  source_selector?: string;
  action?: 'allow' | 'deny' | 'metric-override';
  apply_to_node_id?: string;
}

// API response types
export interface ValidationError {
  field: string;
  // code + params drive client localization via the 'error.<code>' catalog (tValidationError,
  // plan-3.5a); message is the server-rendered English default (CLI/curl + i18n fallback).
  code?: string;
  params?: Record<string, string>;
  message: string;
  level: 'error' | 'warning';
}

export interface ValidateResponse {
  valid: boolean;
  errors?: ValidationError[];
  warnings?: ValidationError[];
}

export interface CompileManifest {
  project_id: string;
  project_name: string;
  version: string;
  compiled_at: string;
  node_count: number;
  checksum: string;
}

export interface CompileResponse {
  topology: Topology;
  wireguard_configs: Record<string, string>;
  babel_configs: Record<string, string>;
  sysctl_configs: Record<string, string>;
  install_scripts: Record<string, string>;
  deploy_scripts: Record<string, string>;
  manifest: CompileManifest;
  // Non-fatal advisories (double NAT, edges missing an endpoint, orphan nodes, etc.).
  // The compile path runs semantic validation and returns these warnings alongside; the UI shows them after a successful compile.
  warnings?: ValidationError[];
  // Returned only by the controller compile preview (PR6): IDs of nodes that exist in the topology but were excluded
  // from this render because they are not yet enrolled.
  // The panel uses this to warn "N nodes not yet enrolled (not compiled)". The air-gap /api/compile does not return this field.
  skipped_unenrolled?: string[];
}

export interface CompileHistoryEntry {
  id: string;
  timestamp: string;
  topology: Topology;
  compileResult: CompileResponse;
}
