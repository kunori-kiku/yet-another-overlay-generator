// Shared "per-edge <-> compiled WG interface" resolver (Decisions #12), used by both RightPanel and CustomNode,
// to avoid two divergent implementations. Pure function, does not read the store; types are imported from ../types/topology.
//
// Key constraint (never infer peerName by stripping the 'wg-' prefix):
// backup-link interface names look like wg-<clean8><hash4> (e.g. wg-betaa3f2); stripping the prefix yields a garbage chip.
// The correct approach is to match an interface back to its owning edge by "pinned port" —— each interface's ListenPort on a node is unique,
// so (pinned_from_port===P && from_node_id===N) or (pinned_to_port===P && to_node_id===N)
// locates the edge deterministically; then take the "peer node name" as peerName and reuse edge.role to decide the ★/bN marker.
// Interface naming authority lives in the backend (docs/spec/artifacts/naming.md); the frontend only consumes it and never recomputes interface names.
import type { Node, Edge } from '../types/topology';

export interface CompiledInterfaceInfo {
  interfaceName: string; // real interface name (e.g. "wg-beta" / "wg-betaa3f2"), used in the tooltip; never strip 'wg-'
  listenPort: number;    // ListenPort parsed from the config body
  peerName: string;      // peer node name (when matched to an edge); falls back to the interface name itself when unmatched
  edgeId?: string;       // matched edge id (omitted when unmatched)
  role: 'primary' | 'backup' | 'unknown'; // backup->'backup'; matched non-backup->'primary'; unmatched->'unknown'
}

// Parse ListenPort from the WG config body (port-allocation authority lives in the backend). Returns null when it cannot be parsed (callers skip the entry accordingly).
function parseListenPort(config: string | undefined): number | null {
  if (!config) return null;
  const m = config.match(/ListenPort\s*=\s*(\d+)/);
  if (!m) return null;
  const port = parseInt(m[1], 10);
  return Number.isFinite(port) ? port : null;
}

// Match "the interface listening on port P on node N" back to its owning edge by pinned port:
//   (pinned_from_port===P && from_node_id===N) or (pinned_to_port===P && to_node_id===N).
// Ports are unique within a node, so the match is deterministic. Returns undefined when the interface is not yet compiled / missing a pin / has no corresponding edge.
function matchEdgeByPinnedPort(
  nodeId: string,
  listenPort: number,
  edges: Edge[]
): Edge | undefined {
  return edges.find(
    (e) =>
      (e.pinned_from_port === listenPort && e.from_node_id === nodeId) ||
      (e.pinned_to_port === listenPort && e.to_node_id === nodeId)
  );
}

// Resolve all compiled interfaces on a given node into role-annotated display info.
// Config keys look like "<nodeID>:<interfaceName>"; only entries belonging to nodeId are processed.
// Graceful degradation: no ListenPort (cannot parse) -> skip the entry; missing pin / no corresponding edge -> role:'unknown',
// peerName falls back verbatim to the interface name (never strip 'wg-').
export function resolveNodeInterfaces(
  nodeId: string,
  wireguardConfigs: Record<string, string>,
  nodes: Node[],
  edges: Edge[]
): CompiledInterfaceInfo[] {
  const out: CompiledInterfaceInfo[] = [];
  if (!wireguardConfigs) return out;

  for (const [key, config] of Object.entries(wireguardConfigs)) {
    const colonIdx = key.indexOf(':');
    if (colonIdx < 0) continue;
    const keyNodeId = key.slice(0, colonIdx);
    if (keyNodeId !== nodeId) continue;
    const interfaceName = key.slice(colonIdx + 1);

    const listenPort = parseListenPort(config);
    if (listenPort === null) continue; // cannot parse the port -> skip

    const edge = matchEdgeByPinnedPort(nodeId, listenPort, edges);
    if (!edge) {
      // No edge matched (missing pin / not yet compiled / no corresponding edge): role unknown, peerName falls back to the interface name.
      out.push({
        interfaceName,
        listenPort,
        peerName: interfaceName,
        role: 'unknown',
      });
      continue;
    }

    // Peer node = the other end of the edge (the side opposite the node the interface is on).
    const otherNodeId =
      edge.from_node_id === nodeId ? edge.to_node_id : edge.from_node_id;
    const otherNode = nodes.find((n) => n.id === otherNodeId);
    const peerName = otherNode?.name || interfaceName;
    const role: CompiledInterfaceInfo['role'] =
      edge.role === 'backup' ? 'backup' : 'primary';

    out.push({
      interfaceName,
      listenPort,
      peerName,
      edgeId: edge.id,
      role,
    });
  }

  return out;
}

// Resolve the compiled interface on one side of a single edge (used by RightPanel's "per-edge compiled values" panel).
// fromSide=true -> on from_node_id, find the interface listening on pinned_from_port;
// fromSide=false -> on to_node_id, find the interface listening on pinned_to_port.
// Missing pin (not yet compiled) / cannot parse ListenPort / no matching interface found -> return null.
export function resolveEdgeInterface(
  edge: Edge,
  fromSide: boolean,
  wireguardConfigs: Record<string, string>
): { interfaceName: string; listenPort: number } | null {
  if (!wireguardConfigs) return null;
  const nodeId = fromSide ? edge.from_node_id : edge.to_node_id;
  const pinnedPort = fromSide ? edge.pinned_from_port : edge.pinned_to_port;
  if (pinnedPort === undefined || pinnedPort === null) return null;

  for (const [key, config] of Object.entries(wireguardConfigs)) {
    const colonIdx = key.indexOf(':');
    if (colonIdx < 0) continue;
    const keyNodeId = key.slice(0, colonIdx);
    if (keyNodeId !== nodeId) continue;

    const listenPort = parseListenPort(config);
    if (listenPort === null) continue;
    if (listenPort !== pinnedPort) continue;

    return { interfaceName: key.slice(colonIdx + 1), listenPort };
  }

  return null;
}
