import type { Edge, Node } from '../types/topology';

// edgeDirection.ts — the link-direction EDITOR helpers (D11). The model stores only
// ""≡'both' / 'forward' (one spelling); "single-link toward the from-node" is expressed by
// FLIPPING the edge so the drawn direction always equals the dial direction. These helpers are
// pure so the EdgeEditor's "to(A)" choice is one deterministic store write and unit-testable
// without a component harness. The load-time sanitize lives in normalizeEdges.ts; the compile
// semantics live in ../compiler/peers.ts (mirroring internal/compiler/peers.go).

// flipEdge returns the edge redrawn in the opposite direction:
//   - from/to swap;
//   - the three pin PAIRS mirror (pinned_from_* ⇄ pinned_to_*) — allocation-stable, because link
//     identity (linkid PinKey) and interface names (each side names the REMOTE) are both
//     direction-agnostic, so a recompile reproduces the exact same values on the swapped sides;
//   - the dial fields clear (endpoint_host / endpoint_port / compiled_port): they described how
//     the OLD from-side dialed the old to-node, which is stale the moment the dialer changes —
//     keeping them would silently dial the wrong node (the caller prefills the new target's host).
// Everything else (id, type, transport, mimic_fallback, role, priority/weight, notes, is_enabled,
// link_direction) passes through untouched; the caller decides the direction value.
// PURE: returns a new object, never mutates the input.
export function flipEdge(edge: Edge): Edge {
  const flipped: Edge = {
    ...edge,
    from_node_id: edge.to_node_id,
    to_node_id: edge.from_node_id,
    endpoint_host: undefined,
    endpoint_port: undefined,
    compiled_port: undefined,
    pinned_from_port: edge.pinned_to_port,
    pinned_to_port: edge.pinned_from_port,
    pinned_from_transit_ip: edge.pinned_to_transit_ip,
    pinned_to_transit_ip: edge.pinned_from_transit_ip,
    pinned_from_link_local: edge.pinned_to_link_local,
    pinned_to_link_local: edge.pinned_from_link_local,
  };
  return flipped;
}

// reverseDialSource resolves where a doubly-linked edge's REVERSE dial (to→from) would come from
// at compile time, mirroring the compiler's resolution order (peers.go:886-911 ⇄ peers.ts):
//   1. an enabled primary-class explicit reverse edge carrying an endpoint_host → that host;
//   2. else the from-node's public_endpoints[0].host (capability inference normalizes
//      has_public_ip UP from endpoint presence, so presence alone decides);
//   3. else null — the reverse peer is passive until dialed.
// Surfaced as an EdgeEditor readout so the both-mode asymmetry (one configurable forward dial,
// one derived reverse dial) is visible instead of tribal knowledge.
export function reverseDialSource(
  edge: Edge,
  fromNode: Node | undefined,
  edges: Edge[],
): { kind: 'reverse-edge' | 'node-endpoint'; host: string } | null {
  const reverseEdge = edges.find(
    (e) =>
      e.is_enabled &&
      (e.role ?? '') !== 'backup' &&
      e.from_node_id === edge.to_node_id &&
      e.to_node_id === edge.from_node_id &&
      !!e.endpoint_host,
  );
  if (reverseEdge?.endpoint_host) {
    return { kind: 'reverse-edge', host: reverseEdge.endpoint_host };
  }
  const host = fromNode?.public_endpoints?.[0]?.host;
  if (host) {
    return { kind: 'node-endpoint', host };
  }
  return null;
}
