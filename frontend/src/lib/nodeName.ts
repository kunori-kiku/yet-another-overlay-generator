// nodeName.ts — single-source the fleet's "prefer the node's friendly name, fall back to its id"
// display rule.
//
// The controller registry keys every node by its node_id (the identity the agent enrolls with); the
// human-facing name lives ONLY in the topology (the design). So the fleet views resolve id → name
// against the current design, and fall back to the id whenever a name is not available: no design is
// loaded (the enroll-first flow keeps an empty canvas), the node is an ORPHAN (present in the fleet
// but absent from the design), or the design node's name is blank. This mirrors the id-fallback rule
// the edge-readiness list already used inline (`nameByNodeId.get(id) || id`) — lifted here so the
// node-label sites and the edge list share ONE definition.

import type { Node } from '../types/topology';

// nodeNameMap builds the id → name lookup from the design's nodes (the single source of node names).
export function nodeNameMap(nodes: readonly Node[]): Map<string, string> {
  return new Map(nodes.map((n) => [n.id, n.name]));
}

// nodeDisplayName returns the node's friendly design name, or the node id when no non-blank name is
// known for it (no design / orphan / blank name). A whitespace-only name is treated as blank so a
// stray space never renders as an invisible label.
export function nodeDisplayName(nodeId: string, nameByNodeId: Map<string, string>): string {
  const name = nameByNodeId.get(nodeId);
  return name !== undefined && name.trim() !== '' ? name : nodeId;
}
