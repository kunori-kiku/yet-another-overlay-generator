// Peer derivation — the TypeScript mirror of internal/compiler/peers.go.
//
// THIS FILE (substep 14) ports PASS 1 ONLY: the reserve-all-pins-FIRST-then-gap-fill resource
// pre-allocation (Spec B, docs/spec/compiler/allocation-stability.md). Pass 2 (forward/reverse PeerInfo
// build) + DeriveClientConfigs + GenerateRouterID land in the next step.
//
// Pass 1 folds the enabled edges into link entities (the primary class of a node pair unifies A->B /
// B->A + same-pair primaries under one linkKey; each backup edge is its own link via its "#id" linkKey),
// RESERVES every complete pinned_* set into the resource pools, then gap-fills the UNPINNED links
// iterated SORTED BY linkKey (the ONLY allocation-order sort — mirrors peers.go's sort.Slice EXACTLY),
// taking the lowest-free slot per pool: lowestFreePort (base 51820), gapFillTransitPair,
// gapFillLinkLocalPair. This is invariants I1/I8 — a superset recompile reproduces every pre-existing
// edge's value, and delete/re-add of a link identity is idempotent.
//
// Go is the authoritative oracle: every allocated port / transit IP / link-local MUST equal the Go side
// value-for-value — a wrong value silently disagrees with the controller (worse than a crash). The pool
// math is reused from cidr.ts (every uint32 step >>> 0 coerced).

import {
  gapFillLinkLocalPair,
  gapFillTransitPair,
} from './cidr';
import { CompileCode, CompileError } from './errors';
import { isBackup, linkKey } from './linkid';
import type { Domain, Edge, Node, Topology } from './model';
import { DefaultTransitCIDR } from './allocconst';

// PairAllocation holds the pre-allocated resources for a node pair / link (ports, transit IP pair,
// link-local pair), oriented by the link's canonical from/to. Mirrors compiler.pairAllocation
// (peers.go:285-294) field-for-field. fromPort/toPort are 0 for an un-allocated side (a client side
// keeps 0 — it uses a single wg0 with no per-peer port).
export interface PairAllocation {
  fromNodeID: string;
  toNodeID: string;
  fromPort: number; // allocated listen port for the fromNode interface (0 = client / un-allocated)
  toPort: number; // allocated listen port for the toNode interface (0 = client / un-allocated)
  localTransit: string;
  remoteTransit: string;
  localLL: string;
  remoteLL: string;
}

// LinkEntity is the per-link folding of edges: the primary class of a node pair (all enabled non-backup
// edges) folds into ONE bidirectional link; each backup edge is its own link. Mirrors the in-function
// linkEntity struct (peers.go:372-379). primaryEdge decides the from/to orientation, the interface-name
// suffix, and the LinkCost; transitCIDR is the per-pool key (resolved via transitCIDRForNode).
export interface LinkEntity {
  linkKey: string;
  backup: boolean;
  primaryEdge: Edge; // decides from/to orientation, interface name suffix and LinkCost
  fromNode: Node;
  toNode: Node;
  transitCIDR: string; // resolved transit CIDR (per-pool key)
}

// Pass1Result is the output of Pass 1: the resolved link entities (in topo.Edges fold order, NOT sorted
// — the sort is internal to gap-fill), the linkKey->entity index, and the allocations table. The
// allocations table is keyed by linkid.LinkKey(edge), PLUS a bidirectional "from->to"/"to->from" alias
// for each primary-class link (backward compatibility with directed-key lookups). Pass 2 / write-back /
// DeriveClientConfigs all read this table by LinkKey(edge).
export interface Pass1Result {
  links: LinkEntity[];
  linkByKey: Map<string, LinkEntity>;
  allocations: Map<string, PairAllocation>;
}

// ReservedAllocations holds the allocation resources occupied by "edges outside the subgraph" that a
// subgraph compile must avoid. Mirrors compiler.ReservedAllocations (peers.go:51-55). Full / local-mode
// compiles pass none (undefined), making the reservation phase a no-op (byte-identical behavior).
export interface ReservedAllocations {
  ports: Map<string, Set<number>>; // nodeID -> set of ports
  transitIPs: Map<string, Set<string>>; // resolved CIDR -> set of IP strings
  linkLocals: Set<string>; // set of link-local strings
}

// transitCIDRForNode resolves the transit CIDR ownership for a link: the TransitCIDR of the domain the
// from-node belongs to, falling back to the default pool on empty (no domain / unconfigured). Mirrors
// compiler.transitCIDRForNode (peers.go:25-32). This is the single ownership logic shared by link
// construction and external pin reservation.
function transitCIDRForNode(
  from: Node | undefined,
  domainMap: Map<string, Domain>,
): string {
  if (from !== undefined) {
    const domain = domainMap.get(from.domain_id);
    if (domain !== undefined && domain.transit_cidr) {
      return domain.transit_cidr;
    }
  }
  return DefaultTransitCIDR;
}

// lowestFreePort returns a node's lowest free port not below the base port 51820 (skipping used values).
// Mirrors compiler.lowestFreePort (peers.go:1062-1071): the base is the FIXED 51820 (per-node
// listen_port is meaningless under the per-peer interface model). A port must not exceed 65535 (D11):
// exceeding it throws CodeListenPortExhausted (with the node name + base) rather than rendering a port
// wg-quick would reject only at deploy time.
function lowestFreePort(
  node: Node,
  usedPorts: Map<string, Set<number>>,
): number {
  const base = 51820;
  const used = usedPorts.get(node.id);
  for (let port = base; port <= 65535; port++) {
    if (used === undefined || !used.has(port)) {
      return port;
    }
  }
  throw new CompileError(CompileCode.ListenPortExhausted, {
    node: node.name,
    base: String(base),
  });
}

// derivePass1 runs Pass 1 of peer derivation: fold edges into link entities, reserve all complete pins,
// then gap-fill the unpinned links sorted by linkKey. Mirrors the Pass-1 portion of
// compiler.derivePeersWithDomains (peers.go:300-614). Returns the link entities, the linkKey index, and
// the allocations table (keyed by linkKey + directed aliases). PURE — it never mutates topo; the only
// throws are the coded pool/port-exhaustion failures bubbled from the cidr.ts pool math + lowestFreePort.
//
// `reserved` is the out-of-subgraph reservation set (subgraph compiles only); local-mode / full compiles
// pass undefined, making the reservation phase a no-op with byte-identical behavior.
export function derivePass1(
  topo: Topology,
  reserved?: ReservedAllocations,
): Pass1Result {
  // Build the domain index (used for the per-link transit CIDR resolution).
  const domainMap = new Map<string, Domain>();
  for (const d of topo.domains) {
    domainMap.set(d.id, d);
  }

  // Node index.
  const nodeMap = new Map<string, Node>();
  for (const n of topo.nodes) {
    nodeMap.set(n.id, n);
  }

  // ======== Pass 1: fold edges into link entities (topo.Edges order) ========
  // PRIMARY CLASS: all enabled non-backup edges of a node pair fold into one bidirectional link
  // (primaryEdge = the first enabled primary-class edge in topo.Edges order, which decides from/to
  // orientation). Each backup edge becomes its own link (its linkKey carries the "#edgeID" suffix).
  const links: LinkEntity[] = [];
  const linkByKey = new Map<string, LinkEntity>();

  for (const edge of topo.edges) {
    if (!edge.is_enabled) {
      continue;
    }
    const fromNode = nodeMap.get(edge.from_node_id);
    const toNode = nodeMap.get(edge.to_node_id);
    if (fromNode === undefined || toNode === undefined) {
      continue;
    }

    const lk = linkKey(edge);
    // Multiple edges of a node pair in the primary class share one linkKey: the first occurrence builds
    // the entity, subsequent edges (reverse + same-direction duplicates) fold in without rebuilding. A
    // backup edge's linkKey is unique (the "#edgeID" suffix), so each backup builds its own entity.
    if (linkByKey.has(lk)) {
      continue;
    }

    const transitCIDR = transitCIDRForNode(fromNode, domainMap);

    const link: LinkEntity = {
      linkKey: lk,
      backup: isBackup(edge),
      primaryEdge: edge,
      fromNode,
      toNode,
      transitCIDR,
    };
    links.push(link);
    linkByKey.set(lk, link);
  }

  // ---- Reservation sets ----
  // Ports keyed by node; transit IPs stored verbatim as IP strings per CIDR pool; link-locals globally
  // unique. Stored RAW (no canonicalization) to mirror Go's map keying byte-for-byte.
  const usedPorts = new Map<string, Set<number>>(); // nodeID -> set of ports
  const usedTransitIPs = new Map<string, Set<string>>(); // cidr -> set of IP strings
  const usedLinkLocals = new Set<string>(); // set of link-local strings

  const markPort = (nodeID: string, port: number): void => {
    let set = usedPorts.get(nodeID);
    if (set === undefined) {
      set = new Set<number>();
      usedPorts.set(nodeID, set);
    }
    set.add(port);
  };
  const markTransit = (cidr: string, ip: string): void => {
    let set = usedTransitIPs.get(cidr);
    if (set === undefined) {
      set = new Set<string>();
      usedTransitIPs.set(cidr, set);
    }
    set.add(ip);
  };
  const transitUsed = (cidr: string, ip: string): boolean => {
    const set = usedTransitIPs.get(cidr);
    return set !== undefined && set.has(ip);
  };

  // ======== Pass 1 phase 2.5: reserve resources occupied by "outside-the-subgraph" edges ========
  // Only a subgraph compile passes `reserved`; a full / local-mode compile passes undefined, making
  // this a no-op (I1). Mirrors peers.go:454-468.
  if (reserved !== undefined) {
    for (const [nodeID, ports] of reserved.ports) {
      for (const port of ports) {
        markPort(nodeID, port);
      }
    }
    for (const [cidr, ips] of reserved.transitIPs) {
      for (const ip of ips) {
        markTransit(cidr, ip);
      }
    }
    for (const ll of reserved.linkLocals) {
      usedLinkLocals.add(ll);
    }
  }

  // ======== Pass 1 phase 3: reserve all pins ========
  // Before any gap-fill, reserve each link's complete-paired pins resource by resource. A partial pin
  // (single-sided value) is treated as "that resource is unpinned" and skipped. Iterated over the
  // insertion-order `links` (NOT the sorted order — reservation is order-independent set semantics,
  // matching Go where the sort happens AFTER this loop). Mirrors peers.go:475-520.
  const pinnedAllocations = new Map<string, PairAllocation>(); // linkKey -> alloc built from pins
  for (const link of links) {
    // Pins are taken from this link's primaryEdge (a unified primary link's pins live on its primary
    // edge; a backup link's pins live on the backup edge itself, which IS its primaryEdge).
    const edge = link.primaryEdge;
    const isFromClient = link.fromNode.role === 'client';
    const isToClient = link.toNode.role === 'client';

    const alloc: PairAllocation = {
      fromNodeID: link.fromNode.id,
      toNodeID: link.toNode.id,
      fromPort: 0,
      toPort: 0,
      localTransit: '',
      remoteTransit: '',
      localLL: '',
      remoteLL: '',
    };
    let hasAnyPin = false;

    // Port pin (pinned only when complete-paired and neither side is a client).
    if (
      !isFromClient &&
      !isToClient &&
      (edge.pinned_from_port ?? 0) > 0 &&
      (edge.pinned_to_port ?? 0) > 0
    ) {
      alloc.fromPort = edge.pinned_from_port as number;
      alloc.toPort = edge.pinned_to_port as number;
      markPort(link.fromNode.id, alloc.fromPort);
      markPort(link.toNode.id, alloc.toPort);
      hasAnyPin = true;
    }

    // Transit IP pin (pinned only when complete-paired).
    if (edge.pinned_from_transit_ip && edge.pinned_to_transit_ip) {
      alloc.localTransit = edge.pinned_from_transit_ip;
      alloc.remoteTransit = edge.pinned_to_transit_ip;
      markTransit(link.transitCIDR, alloc.localTransit);
      markTransit(link.transitCIDR, alloc.remoteTransit);
      hasAnyPin = true;
    }

    // Link-local pin (pinned only when complete-paired).
    if (edge.pinned_from_link_local && edge.pinned_to_link_local) {
      alloc.localLL = edge.pinned_from_link_local;
      alloc.remoteLL = edge.pinned_to_link_local;
      usedLinkLocals.add(alloc.localLL);
      usedLinkLocals.add(alloc.remoteLL);
      hasAnyPin = true;
    }

    if (hasAnyPin) {
      pinnedAllocations.set(link.linkKey, alloc);
    }
  }

  // ======== Pass 1 phase 4: gap-fill the unpinned resources ========
  // Iterate SORTED BY linkKey (the ONLY allocation-order sort) so candidate order is independent of
  // array position — the identity-ordered gap-fill the spec requires. In a single-edge pair
  // linkKey===pinKey, so the sort order and each value are byte-identical to before the parallel-link
  // change. Each resource takes the lowest free slot within its pool; because reservation comes first
  // and the order is decided only by linkKey, delete+re-add of the same link identity reproduces the
  // same value (I2/I9). Mirrors peers.go:530-614.
  //
  // Sort a COPY of the entity references so links[] stays in fold order for Pass 2 (Go sorts the slice
  // in place; the TS Pass 2 re-iterates topo.Edges and looks up by linkKey, so the array order of
  // `links` is irrelevant to Pass 2 — copying keeps the exported `links` in the documented fold order).
  const sortedLinks = links.slice();
  sortedLinks.sort((a, b) => (a.linkKey < b.linkKey ? -1 : a.linkKey > b.linkKey ? 1 : 0));

  const allocations = new Map<string, PairAllocation>(); // key: linkKey (+ directed aliases for primary)

  for (const link of sortedLinks) {
    const fromNode = link.fromNode;
    const toNode = link.toNode;
    const isFromClient = fromNode.role === 'client';
    const isToClient = toNode.role === 'client';

    // Take this linkKey's (partial) pin allocation as the starting point, filling the unpinned
    // resources on top of it.
    let alloc = pinnedAllocations.get(link.linkKey);
    if (alloc === undefined) {
      alloc = {
        fromNodeID: fromNode.id,
        toNodeID: toNode.id,
        fromPort: 0,
        toPort: 0,
        localTransit: '',
        remoteTransit: '',
        localLL: '',
        remoteLL: '',
      };
    }

    // ---- Ports: if unpinned, take per side the lowest free port not below the node base ----
    // The client side does not take part in per-peer port allocation (single wg0), so its port stays 0
    // and is not reserved; the non-client side still needs a listen port (so DeriveClientConfigs knows
    // which port the client dials). Each side is decided independently. Port pins are paired, so if
    // either side is pinned the whole pair is treated as pinned and allocation is skipped.
    const portsPinned = alloc.fromPort > 0 || alloc.toPort > 0;
    if (!portsPinned) {
      if (!isFromClient) {
        const fromPort = lowestFreePort(fromNode, usedPorts);
        markPort(fromNode.id, fromPort);
        alloc.fromPort = fromPort;
      }
      if (!isToClient) {
        const toPort = lowestFreePort(toNode, usedPorts);
        markPort(toNode.id, toPort);
        alloc.toPort = toPort;
      }
    }

    // ---- Transit IP pair: if unpinned, take the lowest free pair in the per-CIDR pool ----
    const transitPinned = alloc.localTransit !== '' && alloc.remoteTransit !== '';
    if (!transitPinned) {
      const [localTransit, remoteTransit] = gapFillTransitPair(
        link.transitCIDR,
        transitUsed,
      );
      markTransit(link.transitCIDR, localTransit);
      markTransit(link.transitCIDR, remoteTransit);
      alloc.localTransit = localTransit;
      alloc.remoteTransit = remoteTransit;
    }

    // ---- Link-local pair: if unpinned, take the lowest free pair ----
    const llPinned = alloc.localLL !== '' && alloc.remoteLL !== '';
    if (!llPinned) {
      const [localLL, remoteLL] = gapFillLinkLocalPair(usedLinkLocals);
      usedLinkLocals.add(localLL);
      usedLinkLocals.add(remoteLL);
      alloc.localLL = localLL;
      alloc.remoteLL = remoteLL;
    }

    // The link allocation uses linkid.LinkKey as its canonical key (I3). Pass 2 / write-back /
    // DeriveClientConfigs all look up by linkid.LinkKey(edge).
    allocations.set(link.linkKey, alloc);

    // Additionally register a bidirectional "from->to" alias for the primary-class link (backward
    // compatibility with directed-key lookups). A backup link owns its linkKey exclusively and
    // registers no directed alias (to avoid same-direction backups overwriting each other). The linkKey
    // (containing "|"/"#") and the directed key (containing "->") have disjoint character sets and never
    // collide. Mirrors peers.go:610-613.
    if (!link.backup) {
      allocations.set(`${fromNode.id}->${toNode.id}`, alloc);
      allocations.set(`${toNode.id}->${fromNode.id}`, alloc);
    }
  }

  return { links, linkByKey, allocations };
}
