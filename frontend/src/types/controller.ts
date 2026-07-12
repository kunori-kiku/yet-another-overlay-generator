// Frontend data model for the controller panel (plan-4.5 networked controller).
// These types mirror the operator-facing JSON shapes in internal/api/handler_controller.go,
// but uniformly use camelCase (mapped from the backend's snake_case; the mapping happens in controllerClient.ts).

// Lifecycle status of a node in the controller registry. Mirrors controller.NodeStatus:
// 'pending' (enrolled, awaiting operator approval) / 'approved' (included in the compile subgraph) /
// 'revoked' (evicted; the bearer credential is invalidated immediately).
export type ControllerNodeStatus = 'pending' | 'approved' | 'revoked';

// NodeCondition is one structured agent→panel feedback item (plan-1 model.Condition → conditionJSON).
// status drives the badge color; reason is a closed CamelCase code per type; message is the curated
// single line (already length-capped server-side — never a raw error dump). Generic by design: the
// panel renders ANY {type,status,reason} with no per-type code, so later plans add producers
// (selfupdate/wireguard/mimic) with zero new rendering. observedAt is the controller's receive stamp.
export interface NodeCondition {
  type: string;
  status: string; // 'ok' | 'warn' | 'error' | 'unknown' (open at the type level; colored by map)
  reason: string;
  message: string;
  since: string; // RFC3339 (agent-advisory)
  observedAt: string; // RFC3339 controller receive stamp
}

// Operator view of a registered node. Deliberately contains no key material (neither WG public-key bytes nor an API
// token hash): hasWGPublicKey only indicates a public key is on file. Mirrors nodeJSON in handler_controller.go.
export interface ControllerNode {
  nodeId: string;
  status: ControllerNodeStatus;
  hasWGPublicKey: boolean;
  desiredGeneration: number;
  appliedGeneration: number;
  lastChecksum: string;
  lastHealth: string;
  // plan-4: build version reported by the agent (observability); pre-version-aware older agents report an empty string, and the UI shows "—".
  agentVersion: string;
  lastSeen: string;
  enrolledAt: string;
  // plan-4.6 fleet-wide key rotation: the operator has requested a WG key rotation for this node, awaiting the agent
  // to regenerate the local private key and register the new public key via POST /rekey (the backend clears this flag once registration succeeds).
  rekeyRequested: boolean;
  // controller-panel-rollout-ui plan-1: server-computed agent self-update rollout membership
  // (AgentRolloutNodeIDs — the canary subset, or the whole fleet once promoted). The per-node
  // update-status chip reads it; the panel never re-derives canary membership client-side.
  inRollout: boolean;
  // plan-1/2: structured feedback channel. Empty array when the agent reported none (legacy agents,
  // or a node with nothing to report) — the panel then renders no conditions strip.
  conditions: NodeCondition[];
  // beta.12: per-peer WireGuard link health from the agent's /telemetry metrics map
  // (telemetry.wireguard_peers), surfaced as a collapsible per-link panel — the detail behind the
  // aggregate wireguard condition. mapNode always sets it (empty for a legacy/beta.11 agent, a
  // client node, or no peers), but it is LIVE-ONLY: deliberately NOT persisted (controllerStore
  // partialize strips it — it carries raw peer endpoints, and a frozen handshake age is stale on
  // reload), so a node rehydrated from the localStorage cache before the first refresh has no
  // wireguardPeers key. Hence optional; every reader coerces (?? []). Observability only; no key material.
  wireguardPeers?: WireGuardPeer[];
  // resource is the node's live host load + memory (plan-10 metrics["resource"]). Live-only (stripped
  // before the persisted cache, like wireguardPeers): a load average frozen at persist time is stale.
  // Observability only; carries no endpoint/IP/key material. Optional — absent for a pre-plan-10 agent.
  resource?: NodeResource;
  // nativeXDP is the egress NIC's native-XDP capability heuristic (plan-4 metrics["native_xdp"]) — a
  // PRE-DEPLOY advisory so the panel can warn before an operator picks xdp_mode=native. Live-only
  // (stripped before the persisted cache, like resource — telemetry stays out of localStorage).
  // Observability/advisory only; no endpoint/IP/key material. Optional — absent for a pre-plan-4 agent
  // or before the first heartbeat.
  nativeXDP?: NativeXDP;
  // mimicCapability is the node's "can this node build/load the mimic kernel module" heuristic (plan-3
  // metrics["mimic_capability"]) — a PRE-DEPLOY advisory so the panel can warn before an operator sets
  // transport=tcp on a node whose stale kernel can't build the DKMS module (the exact fleet case).
  // Live-only (stripped before the persisted cache, like resource). Advisory only; no endpoint/IP/key
  // material. Optional — absent for a pre-plan-3 agent or before the first heartbeat.
  mimicCapability?: MimicCapability;
}

// WireGuardPeer is one peer's live link health (the per-link panel row). peer is the link name (the
// wg-<peer> interface minus its prefix); lastHandshake is unix seconds (0 = never); status is the
// agent's up | stale | never classification. endpoint is "" for a not-yet-connected peer.
export interface WireGuardPeer {
  peer: string;
  interface: string;
  endpoint: string;
  lastHandshake: number;
  status: 'up' | 'stale' | 'never';
}

// NodeResource is the node's host load + memory (plan-10), projected from the agent's
// metrics["resource"] telemetry. Memory is in kB (as /proc/meminfo reports); mem fields are 0 when the
// kernel did not report MemAvailable. No endpoint/IP/key material.
export interface NodeResource {
  // cpuPct is host CPU utilization percent (0..100), a /proc/stat delta between heartbeats. OPTIONAL:
  // absent for a pre-plan-1 agent, the first beat after agent start (no delta yet), or a wrapped
  // counter — the panel renders a gap, never a fake 0.
  cpuPct?: number;
  load1: number;
  load5: number;
  load15: number;
  memTotalKB: number;
  memAvailableKB: number;
}

// NativeXDP is the node's egress-NIC native-XDP capability heuristic (plan-4), projected from the
// agent's metrics["native_xdp"]. Best-effort (driver-name + kernel heuristic, pure sysfs — no live-NIC
// attach); the DEFINITIVE per-node answer is the deploy-time `mimic` Node Condition (plan-3's native→skb
// auto-downgrade). No endpoint/IP/key material.
export interface NativeXDP {
  capability: 'supported' | 'conditional' | 'unsupported' | 'unknown';
  driver: string;
  kernel: string;
}

// MimicCapability is the node's "can this node run mimic" heuristic (plan-3), projected from the
// agent's metrics["mimic_capability"]. Pure filesystem inspection (module loaded/built + kernel-headers
// present — no shell, no build); the DEFINITIVE answer stays the deploy-time `mimic` Node Condition.
// "unbuildable" is the stale-kernel case (headers pruned → the module can't build). No key material.
export interface MimicCapability {
  capability: 'ready' | 'buildable' | 'unbuildable';
  kernel: string;
}

// A single record in the audit chain. Mirrors the operator-facing fields of controller.AuditEntry.
export interface ControllerAuditEntry {
  timestamp: string;
  actor: string;
  action: string;
  nodeId: string;
}

// Result of /stage: the nodes compiled into this generation, the nodes skipped because they are not enrolled, and the staged generation number.
// Mirrors stageResponseJSON in handler_controller.go.
export interface StageResult {
  staged: string[];
  skippedUnenrolled: string[];
  generation: number;
}
