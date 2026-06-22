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
import { wgInterfaceNameForEdge } from './naming';
import { sha256 } from '@noble/hashes/sha2.js';
import type {
  ClientPeerInfo,
  Domain,
  Edge,
  KeyPair,
  Node,
  PeerInfo,
  Topology,
} from './model';
import { BackupDefaultLinkCost, DefaultTransitCIDR } from './allocconst';

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

// ============================================================================
// Pass 2 — build forward + reverse PeerInfo from Pass 1's allocations
// ============================================================================

// transportTCP is the literal for edge.transport taking "tcp" (mimic shaping transport). mimic has no
// key and no new field; transport=="tcp" is the only signal that a link is wrapped by mimic. Mirrors
// compiler.transportTCP (peers.go:147).
const transportTCP = 'tcp';

// defaultMimicBaseMTU is the base WireGuard MTU for a mimic link when the node has no explicit MTU.
// Mirrors compiler.defaultMimicBaseMTU (peers.go:154).
const defaultMimicBaseMTU = 1420;

// mimicMTUOverhead is the byte overhead mimic (UDP->fake TCP) introduces on each WireGuard interface
// (docs/spec/artifacts/mimic.md: "MTU −12"). Mirrors compiler.mimicMTUOverhead (peers.go:159).
const mimicMTUOverhead = 12;

// isMimicEdge reports whether an edge has mimic (tcp shaping transport) enabled. Mirrors
// compiler.isMimicEdge (peers.go:166-168).
function isMimicEdge(edge: Edge | undefined): boolean {
  return edge !== undefined && edge.transport === transportTCP;
}

// effectiveMTU computes the effective MTU a WireGuard interface on a link should emit. Mirrors
// compiler.effectiveMTU (peers.go:176-185):
//   - non-mimic: keep nodeMTU as is (0 => renderer omits the MTU line, byte-unchanged);
//   - mimic: ((nodeMTU>0 ? nodeMTU : 1420) − 12).
function effectiveMTU(nodeMTU: number, mimic: boolean): number {
  if (!mimic) {
    return nodeMTU;
  }
  let base = nodeMTU;
  if (base <= 0) {
    base = defaultMimicBaseMTU;
  }
  return base - mimicMTUOverhead;
}

// Mimic-fallback policy values (mirror compiler peers.go). '' on an EDGE means "inherit the fleet
// default"; the RESOLVED value is always 'udp' or 'none'.
const mimicFallbackUDP = 'udp';
const mimicFallbackNone = 'none';

// resolveMimicFallback mirrors compiler.resolveMimicFallback (peers.go). PURE, deterministic in its
// two args only — keeps the Go↔TS conformance byte-set identical. Resolves to 'udp' or 'none' only.
// The local engine has no controller fleet default, so callers pass defaultPolicy = '' ⇒ resolves to
// 'none' everywhere ⇒ byte-identical to the pre-change pipeline.
export function resolveMimicFallback(edgePolicy: string | undefined, defaultPolicy: string): string {
  if (edgePolicy === mimicFallbackUDP) return mimicFallbackUDP;
  if (edgePolicy === mimicFallbackNone) return mimicFallbackNone;
  return defaultPolicy === mimicFallbackUDP ? mimicFallbackUDP : mimicFallbackNone;
}

// deriveLinkCost derives a link's Babel rxcost override. Resolution order (peers.go:1078-1091):
//   1. explicit operator setting: edge.priority (>0) wins, else edge.weight (>0) — adopted verbatim;
//   2. backup preset: backup link without an explicit setting => BackupDefaultLinkCost (384);
//   3. default: 0 (left to the role preset's default; the renderer decides whether to emit rxcost).
function deriveLinkCost(edge: Edge | undefined, backup: boolean): number {
  if (edge !== undefined) {
    if ((edge.priority ?? 0) > 0) {
      return edge.priority as number;
    }
    if ((edge.weight ?? 0) > 0) {
      return edge.weight as number;
    }
  }
  if (backup) {
    return BackupDefaultLinkCost;
  }
  return 0;
}

// isIPv6 reports whether a host string contains a ':' (a bracketed-IPv6 endpoint). Mirrors
// compiler.isIPv6 (peers.go:1131-1138).
function isIPv6(host: string): boolean {
  return host.includes(':');
}

// formatEndpoint formats an endpoint address (host:port, bracketing an IPv6 host). Mirrors
// compiler.formatEndpoint (peers.go:1124-1129). The port is rendered as a plain base-10 integer
// (compiler.itoa), matching Go's positive-integer formatting for the always-positive port values here.
function formatEndpoint(host: string, port: number): string {
  if (isIPv6(host)) {
    return `[${host}]:${port}`;
  }
  return `${host}:${port}`;
}

// derivePass2 builds the per-node PeerInfo map from Pass 1's link entities + allocations. Mirrors the
// Pass-2 portion of compiler.derivePeersWithDomains (peers.go:616-902):
//   - iterate topo.edges, gate de-dup on linkKey (the first primary-class edge of a node pair drives
//     creation; each backup produces its own pair);
//   - a client-from edge produces ONLY the router-side PeerInfo (the client uses a single wg0);
//   - otherwise produce the forward PeerInfo (owned by fromNode, pointing at toNode) and — unless toNode
//     is a client — the reverse PeerInfo (owned by toNode, pointing at fromNode);
//   - endpoint resolution: explicit edge.endpoint_port > peer's allocated listen port > (reverse only)
//     public-endpoint fallback; keepalive, mimic MTU, link cost, AllowedIPs, and interface names all per
//     the Go branches.
//
// `keys` maps nodeID -> KeyPair (public key derived from the fixture private key via the keygen seam).
// PURE — reads the topology + Pass-1 result, returns a fresh peerMap, mutates nothing.
function derivePass2(
  topo: Topology,
  keys: Map<string, KeyPair>,
  pass1: Pass1Result,
): Record<string, PeerInfo[]> {
  const { linkByKey, allocations } = pass1;

  // Node index.
  const nodeMap = new Map<string, Node>();
  for (const n of topo.nodes) {
    nodeMap.set(n.id, n);
  }

  // Initialize each node's peer list (so every node — even one with no edges — has an empty slice,
  // mirroring peers.go:310-312).
  const peerMap: Record<string, PeerInfo[]> = {};
  for (const node of topo.nodes) {
    peerMap[node.id] = [];
  }

  // Pre-scan enabled primary-class edge directions for the keepalive decision (peers.go:321-327): only
  // non-backup edges count toward reverse reachability.
  const enabledEdgeDirections = new Set<string>();
  for (const edge of topo.edges) {
    if (edge.is_enabled && !isBackup(edge)) {
      enabledEdgeDirections.add(`${edge.from_node_id}->${edge.to_node_id}`);
    }
  }

  // Edge reverse-lookup index keyed "from->to" -> Edge, recording ONLY primary-class edges
  // (peers.go:334-340): reverse endpoint resolution may only hit an opposite-direction primary edge.
  const edgeMap = new Map<string, Edge>();
  for (const edge of topo.edges) {
    if (edge.is_enabled && !isBackup(edge)) {
      edgeMap.set(`${edge.from_node_id}->${edge.to_node_id}`, edge);
    }
  }

  const keyOf = (nodeID: string): KeyPair =>
    keys.get(nodeID) ?? { privateKey: '', publicKey: '' };

  // PeerInfo produced for this link (gate on linkKey).
  const addedLinks = new Set<string>();

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
    if (addedLinks.has(lk)) {
      continue;
    }

    const link = linkByKey.get(lk);
    if (link === undefined) {
      continue;
    }
    const alloc = allocations.get(lk);
    if (alloc === undefined) {
      continue;
    }

    // ---- Client-from edge: produce ONLY the router-side PeerInfo (peers.go:657-708). ----
    if (fromNode.role === 'client') {
      const mimic = isMimicEdge(link.primaryEdge);
      const fromKey = keyOf(fromNode.id);
      const isForward = alloc.fromNodeID === fromNode.id;

      let routerListenPort: number;
      let routerLocalTransit: string;
      let routerRemoteTransit: string;
      let routerLocalLL: string;
      let routerRemoteLL: string;
      if (isForward) {
        routerListenPort = alloc.toPort;
        routerLocalTransit = alloc.remoteTransit;
        routerRemoteTransit = alloc.localTransit;
        routerLocalLL = alloc.remoteLL;
        routerRemoteLL = alloc.localLL;
      } else {
        routerListenPort = alloc.fromPort;
        routerLocalTransit = alloc.localTransit;
        routerRemoteTransit = alloc.remoteTransit;
        routerLocalLL = alloc.localLL;
        routerRemoteLL = alloc.remoteLL;
      }

      const routerPeer: PeerInfo = {
        nodeID: fromNode.id,
        nodeName: fromNode.name,
        publicKey: fromKey.publicKey,
        overlayIP: fromNode.overlay_ip ?? '',
        allowedIPs: [`${fromNode.overlay_ip ?? ''}/32`],
        endpoint: '',
        persistentKeepalive: 0,
        interfaceName: wgInterfaceNameForEdge(
          fromNode.name,
          link.primaryEdge.id,
          link.backup,
        ),
        listenPort: routerListenPort,
        localTransitIP: routerLocalTransit,
        remoteTransitIP: routerRemoteTransit,
        localLinkLocal: routerLocalLL,
        remoteLinkLocal: routerRemoteLL,
        isClientPeer: true,
        clientOverlayIP: fromNode.overlay_ip ?? '',
        linkCost: 0,
        mimic,
        mimicFallback: resolveMimicFallback(link.primaryEdge?.mimic_fallback, ''),
        mtu: effectiveMTU(toNode.mtu ?? 0, mimic),
      };

      peerMap[toNode.id].push(routerPeer);
      addedLinks.add(lk);
      continue;
    }

    // Whether the current edge's direction matches alloc's canonical direction.
    const isForward = alloc.fromNodeID === fromNode.id;

    const toKey = keyOf(toNode.id);
    const fromKey = keyOf(fromNode.id);

    // ---- Endpoint: explicit edge.endpoint_port wins, else the peer's allocated listen port. ----
    let endpoint = '';
    if (edge.endpoint_host) {
      let portToUse: number;
      if ((edge.endpoint_port ?? 0) > 0) {
        portToUse = edge.endpoint_port as number;
      } else {
        portToUse = isForward ? alloc.toPort : alloc.fromPort;
      }
      endpoint = formatEndpoint(edge.endpoint_host, portToUse);
    }

    // ---- PersistentKeepalive (peers.go:735-739). ----
    let keepalive = 0;
    const hasReverseEdge = enabledEdgeDirections.has(
      `${toNode.id}->${fromNode.id}`,
    );
    if (!fromNode.capabilities.can_accept_inbound || !hasReverseEdge) {
      keepalive = 25;
    }

    // ---- Local resources for the forward peer. ----
    let fromListenPort: number;
    let localTransit: string;
    let remoteTransit: string;
    let localLL: string;
    let remoteLL: string;
    if (isForward) {
      fromListenPort = alloc.fromPort;
      localTransit = alloc.localTransit;
      remoteTransit = alloc.remoteTransit;
      localLL = alloc.localLL;
      remoteLL = alloc.remoteLL;
    } else {
      fromListenPort = alloc.toPort;
      localTransit = alloc.remoteTransit;
      remoteTransit = alloc.localTransit;
      localLL = alloc.remoteLL;
      remoteLL = alloc.localLL;
    }

    const ifaceName = wgInterfaceNameForEdge(
      toNode.name,
      link.primaryEdge.id,
      link.backup,
    );
    const linkCost = deriveLinkCost(link.primaryEdge, link.backup);
    const mimic = isMimicEdge(link.primaryEdge);
    const isToClient = toNode.role === 'client';

    const peer: PeerInfo = {
      nodeID: toNode.id,
      nodeName: toNode.name,
      publicKey: toKey.publicKey,
      overlayIP: toNode.overlay_ip ?? '',
      allowedIPs: ['0.0.0.0/0', '::/0'],
      endpoint,
      persistentKeepalive: keepalive,
      interfaceName: ifaceName,
      listenPort: fromListenPort,
      localTransitIP: localTransit,
      remoteTransitIP: remoteTransit,
      localLinkLocal: localLL,
      remoteLinkLocal: remoteLL,
      isClientPeer: isToClient,
      clientOverlayIP: '',
      linkCost,
      mimic,
      mimicFallback: resolveMimicFallback(link.primaryEdge?.mimic_fallback, ''),
      // The local interface belongs to fromNode, so derive MTU from fromNode.mtu.
      mtu: effectiveMTU(fromNode.mtu ?? 0, mimic),
    };
    if (isToClient) {
      peer.allowedIPs = [`${toNode.overlay_ip ?? ''}/32`];
      peer.clientOverlayIP = toNode.overlay_ip ?? '';
    }

    peerMap[fromNode.id].push(peer);

    // ---- Skip the reverse peer when toNode is a client (the client side uses wg0). ----
    if (isToClient) {
      addedLinks.add(lk);
      continue;
    }

    addedLinks.add(lk);

    // ---- Reverse peer (owned by toNode, pointing at fromNode). ----
    let reverseKeepalive = 0;
    if (!toNode.capabilities.can_accept_inbound) {
      reverseKeepalive = 25;
    }

    const reverseIfaceName = wgInterfaceNameForEdge(
      fromNode.name,
      link.primaryEdge.id,
      link.backup,
    );

    // fromNode interface's allocated listen port (used when the reverse peer dials back to fromNode).
    let fromSideListenPort = alloc.fromPort;
    if (!isForward) {
      fromSideListenPort = alloc.toPort;
    }

    // Resolve the reverse peer's endpoint (peers.go:846-858).
    let reverseEndpoint = '';
    const reverseEdge = edgeMap.get(`${toNode.id}->${fromNode.id}`);
    if (reverseEdge !== undefined && reverseEdge.endpoint_host) {
      if ((reverseEdge.endpoint_port ?? 0) > 0) {
        reverseEndpoint = formatEndpoint(
          reverseEdge.endpoint_host,
          reverseEdge.endpoint_port as number,
        );
      } else {
        reverseEndpoint = formatEndpoint(
          reverseEdge.endpoint_host,
          fromSideListenPort,
        );
      }
    } else if (
      fromNode.capabilities.has_public_ip &&
      (fromNode.public_endpoints?.length ?? 0) > 0
    ) {
      reverseEndpoint = formatEndpoint(
        (fromNode.public_endpoints as NonNullable<Node['public_endpoints']>)[0]
          .host,
        fromSideListenPort,
      );
    }

    // The reverse peer's resources are swapped relative to the forward.
    let toListenPort: number;
    let revLocalTransit: string;
    let revRemoteTransit: string;
    let revLocalLL: string;
    let revRemoteLL: string;
    if (isForward) {
      toListenPort = alloc.toPort;
      revLocalTransit = alloc.remoteTransit;
      revRemoteTransit = alloc.localTransit;
      revLocalLL = alloc.remoteLL;
      revRemoteLL = alloc.localLL;
    } else {
      toListenPort = alloc.fromPort;
      revLocalTransit = alloc.localTransit;
      revRemoteTransit = alloc.remoteTransit;
      revLocalLL = alloc.localLL;
      revRemoteLL = alloc.remoteLL;
    }

    const reversePeer: PeerInfo = {
      nodeID: fromNode.id,
      nodeName: fromNode.name,
      publicKey: fromKey.publicKey,
      overlayIP: fromNode.overlay_ip ?? '',
      allowedIPs: ['0.0.0.0/0', '::/0'],
      endpoint: reverseEndpoint,
      persistentKeepalive: reverseKeepalive,
      interfaceName: reverseIfaceName,
      listenPort: toListenPort,
      localTransitIP: revLocalTransit,
      remoteTransitIP: revRemoteTransit,
      localLinkLocal: revLocalLL,
      remoteLinkLocal: revRemoteLL,
      isClientPeer: false,
      clientOverlayIP: '',
      linkCost,
      mimic,
      mimicFallback: resolveMimicFallback(link.primaryEdge?.mimic_fallback, ''),
      // The reverse peer's local interface belongs to toNode, so derive MTU from toNode.mtu.
      mtu: effectiveMTU(toNode.mtu ?? 0, mimic),
    };

    peerMap[toNode.id].push(reversePeer);
  }

  return peerMap;
}

// derivePeers runs the full two-pass peer derivation: Pass 1 (reserve-then-gap-fill resource
// pre-allocation) then Pass 2 (forward/reverse PeerInfo build). Mirrors compiler.derivePeers /
// derivePeersWithDomains (peers.go:264-905). Returns the per-node peerMap AND the Pass-1 allocations
// table (the latter consumed by DeriveClientConfigs + the compile write-back, which both look up by
// linkKey). PURE — operates on the supplied topology copy, mutates nothing.
//
// `reserved` is the out-of-subgraph reservation set (subgraph compiles only); local-mode / full
// compiles pass undefined, making the reservation phase a no-op with byte-identical behavior.
export function derivePeers(
  topo: Topology,
  keys: Map<string, KeyPair>,
  reserved?: ReservedAllocations,
): { peerMap: Record<string, PeerInfo[]>; pass1: Pass1Result } {
  const pass1 = derivePass1(topo, reserved);
  const peerMap = derivePass2(topo, keys, pass1);
  return { peerMap, pass1 };
}

// deriveClientConfigs generates wg0 configuration info for all client nodes. Mirrors
// compiler.DeriveClientConfigs (peers.go:1201-1314): each client node's single outbound edge resolves
// the router endpoint, and AllowedIPs is the union of every domain's CIDR and every domain's resolved
// transit CIDR (default 10.10.0.0/24 on empty), in topo.domains order, deduplicated. Returns a record
// keyed by client node ID. PURE — reads the topology + Pass-1 allocations, mutates nothing.
export function deriveClientConfigs(
  topo: Topology,
  keys: Map<string, KeyPair>,
  allocations: Map<string, PairAllocation>,
): Record<string, ClientPeerInfo> {
  const configs: Record<string, ClientPeerInfo> = {};

  const nodeMap = new Map<string, Node>();
  for (const n of topo.nodes) {
    nodeMap.set(n.id, n);
  }

  const keyOf = (nodeID: string): KeyPair =>
    keys.get(nodeID) ?? { privateKey: '', publicKey: '' };

  for (const node of topo.nodes) {
    if (node.role !== 'client') {
      continue;
    }

    // Find the client's single outbound edge (first enabled edge whose from is this client).
    let clientEdge: Edge | undefined;
    for (const e of topo.edges) {
      if (e.is_enabled && e.from_node_id === node.id) {
        clientEdge = e;
        break;
      }
    }
    if (clientEdge === undefined) {
      continue;
    }

    const routerNode = nodeMap.get(clientEdge.to_node_id);
    if (routerNode === undefined) {
      continue;
    }

    const routerKey = keyOf(routerNode.id);
    const clientKey = keyOf(node.id);

    // Router-side listen port: look up the allocation by the client edge's linkKey (validation
    // guarantees exactly one client edge, which cannot be a backup, so linkKey === pinKey).
    const alloc = allocations.get(linkKey(clientEdge));
    let routerPort = 0;
    if (alloc !== undefined) {
      routerPort = alloc.fromNodeID === node.id ? alloc.toPort : alloc.fromPort;
    }

    // Endpoint: explicit endpoint_port wins, else the auto-allocated router port.
    let routerEndpoint = '';
    if (clientEdge.endpoint_host) {
      let portToUse = 0;
      if ((clientEdge.endpoint_port ?? 0) > 0) {
        portToUse = clientEdge.endpoint_port as number;
      } else if (routerPort > 0) {
        portToUse = routerPort;
      }
      if (portToUse > 0) {
        routerEndpoint = formatEndpoint(clientEdge.endpoint_host, portToUse);
      }
    }

    // AllowedIPs prefix set: union of every domain CIDR and every domain's resolved transit CIDR
    // (default 10.10.0.0/24 on empty), in topo.domains order, deduplicated (peers.go:1270-1288).
    const domainCIDRs: string[] = [];
    const seenCIDR = new Set<string>();
    const appendCIDR = (cidr: string): void => {
      if (cidr === '' || seenCIDR.has(cidr)) {
        return;
      }
      seenCIDR.add(cidr);
      domainCIDRs.push(cidr);
    };
    for (const d of topo.domains) {
      appendCIDR(d.cidr);
    }
    for (const d of topo.domains) {
      let transitCIDR = d.transit_cidr ?? '';
      if (transitCIDR === '') {
        transitCIDR = DefaultTransitCIDR;
      }
      appendCIDR(transitCIDR);
    }

    // Client listen port (fixed base 51820; per-node listen_port has been removed).
    const listenPort = 51820;

    const mimic = isMimicEdge(clientEdge);

    configs[node.id] = {
      nodeID: node.id,
      nodeName: node.name,
      overlayIP: node.overlay_ip ?? '',
      mtu: effectiveMTU(node.mtu ?? 0, mimic),
      mimic,
      mimicFallback: resolveMimicFallback(clientEdge?.mimic_fallback, ''),
      privateKey: clientKey.privateKey,
      routerPublicKey: routerKey.publicKey,
      routerEndpoint,
      domainCIDRs,
      listenPort,
    };
  }

  return configs;
}

// generateRouterID generates a stable Babel router-id (MAC-48 form) from the SHA-256 hash of the node
// ID. Mirrors compiler.GenerateRouterID (peers.go:1154-1167): take the first 6 hash bytes; set the
// locally-administered bit and clear the multicast bit on the first byte; format as lowercase
// colon-separated hex octets. Meaningless for client nodes (Babel does not run there), but produced
// identically when needed.
export function generateRouterID(nodeID: string): string {
  const h = sha256(new TextEncoder().encode(nodeID));
  let b0 = h[0];
  b0 = (b0 | 0x02) & 0xfe;
  const octets = [b0, h[1], h[2], h[3], h[4], h[5]];
  return octets.map((b) => b.toString(16).padStart(2, '0')).join(':');
}
