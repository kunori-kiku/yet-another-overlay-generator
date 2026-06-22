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
