package compiler

import (
	"encoding/binary"
	"fmt"
	"net"
	"sort"
	"strconv"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/allocconst"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/linkid"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/naming"
)

// DerivePeers derives each node's WireGuard peer list from the edge topology.
// New architecture: one dedicated interface per peer.
// Returns map[nodeID][]PeerInfo.
func DerivePeers(topo *model.Topology, keys map[string]KeyPair) (map[string][]PeerInfo, map[string]*pairAllocation, error) {
	// External callers get the fail-closed default ("" resolves to "none"); the controller threads a
	// real fleet default via the compiler's WithMimicFallbackDefault → derivePeers path.
	return derivePeers(topo, keys, nil, mimicFallbackInherit)
}

// derivePeers is the internal variant of DerivePeers that additionally accepts a set
// of "edges outside the subgraph" reserved resources (reserved). Full compiles
// (air-gap CLI / API) pass nil — there are no dropped edges in the topology, so the
// behavior is byte-identical to before the change; only the controller's subgraph
// compile passes a non-nil reservation set (see CompileSubgraph).
func derivePeers(topo *model.Topology, keys map[string]KeyPair, reserved *ReservedAllocations, mimicFallbackDefault string) (map[string][]PeerInfo, map[string]*pairAllocation, error) {
	// Build the domain index
	domainMap := make(map[string]*model.Domain)
	for i := range topo.Domains {
		domainMap[topo.Domains[i].ID] = &topo.Domains[i]
	}

	return derivePeersWithDomains(topo, keys, domainMap, reserved, mimicFallbackDefault)
}

// pairAllocation holds the pre-allocated resources for a node pair (ports, transit
// IP, link-local).
type pairAllocation struct {
	fromNodeID    string
	toNodeID      string
	fromPort      int // allocated listen port for the fromNode interface
	toPort        int // allocated listen port for the toNode interface
	localTransit  string
	remoteTransit string
	localLL       string
	remoteLL      string
}

// linkEntity folds one or more enabled edges of a node pair into a single routing
// link per the unify rule (spec docs/spec/compiler/allocation-stability.md "Link
// identity with parallel edges" / "Reserve-all-pins-first"):
//   - PRIMARY CLASS: all "non-backup" edges (linkid.IsBackup==false) of the same
//     node pair fold into a single bidirectional link. primaryEdge = the first
//     enabled primary-class edge in topo.Edges order (keeping the old rule: it
//     decides the pairAllocation's from/to orientation); any extra same-direction
//     primary-class edge is an "accidental duplicate" that still maps to this
//     unified link for write-back (historical behavior; the validator warns
//     separately).
//   - Each role=="backup" edge becomes its own independent link: primaryEdge = it
//     itself, and its linkKey carries a "#edgeID" suffix to distinguish it from the
//     node pair's primary link.
//
// Link identity = linkid.LinkKey(primaryEdge): a primary link reduces to its pinKey
// (in a single-edge pair linkKey==pinKey, and the gap-fill order and values are
// byte-identical to before the parallel-link change — the zero-drift guarantee for
// existing fleets).
type linkEntity struct {
	linkKey     string
	backup      bool
	primaryEdge *model.Edge // decides from/to orientation, interface name suffix and LinkCost
	fromNode    *model.Node
	toNode      *model.Node
	transitCIDR string // resolved transit CIDR (per-pool key)
}

// derivePeersWithDomains is the core derivation logic (a two-pass algorithm),
// decomposed into named per-phase helpers so each phase is independently reviewable
// against the allocation-stability invariants (I1-I9):
//
//   - groupLinks               folds enabled edges into unified links (Pass 1 phase 1);
//   - preallocateLinkResources reserves pins then gap-fills ports / transit IPs /
//     link-locals (Pass 1 phases 2.5/3/4 — the allocation-critical core);
//   - buildPeerInfo            builds each link's forward + reverse PeerInfo, ensuring
//     the endpoint port equals the remote interface's listen port (Pass 2).
//
// The decomposition is PURE MOTION: the produced allocations, ordering and rendered
// bytes are identical to the previous single-function form (the golden corpus and
// allocation_stability_test are the byte gates).
func derivePeersWithDomains(topo *model.Topology, keys map[string]KeyPair, domainMap map[string]*model.Domain, reserved *ReservedAllocations, mimicFallbackDefault string) (map[string][]PeerInfo, map[string]*pairAllocation, error) {
	// Node index
	nodeMap := make(map[string]*model.Node)
	for i := range topo.Nodes {
		nodeMap[topo.Nodes[i].ID] = &topo.Nodes[i]
	}

	// Initialize each node's peer list
	peerMap := make(map[string][]PeerInfo)
	for _, node := range topo.Nodes {
		peerMap[node.ID] = []PeerInfo{}
	}

	// Build the primary-class edge reverse-lookup index: key="fromNodeID->toNodeID" ->
	// Edge, recording ONLY enabled primary-class edges (skip backups). Pass 2 reads it two
	// ways, both restricted to the primary class by design:
	//   - to resolve an explicit reverse edge's endpoint — the unified primary link's
	//     reverse endpoint resolution may only hit the opposite-direction primary-class
	//     edge, never a backup (spec: Reverse-edge resolution considers ONLY primary-class
	//     opposite-direction edges);
	//   - as the "does a reverse primary edge exist?" test in the keepalive decision —
	//     reverse reachability is a property of the unified primary link, and a backup edge
	//     forms its own independent link and never acts as a node pair's "reverse primary".
	// Its key set is exactly the set of enabled non-backup edge directions, so a key's
	// PRESENCE in edgeMap is the byte-identical replacement for the previous separate
	// enabledEdgeDirections bool set (which scanned the same edges for the same keys).
	edgeMap := make(map[string]*model.Edge)
	for i := range topo.Edges {
		e := &topo.Edges[i]
		if e.IsEnabled && !linkid.IsBackup(e) {
			edgeMap[e.FromNodeID+"->"+e.ToNodeID] = e
		}
	}

	// ======== Pass 1 phase 1: fold enabled edges into unified links ========
	links, linkByKey := groupLinks(topo, nodeMap, domainMap)

	// ======== Pass 1 phases 2.5/3/4: reserve pins, then gap-fill the rest ========
	allocations, err := preallocateLinkResources(links, reserved)
	if err != nil {
		return nil, nil, err
	}

	// ======== Pass 2: build PeerInfo using the pre-allocated resources ========
	buildPeerInfo(topo, keys, nodeMap, linkByKey, allocations, edgeMap, mimicFallbackDefault, peerMap)

	return peerMap, allocations, nil
}

// forEachEnabledEdge iterates topo.Edges in slice order and invokes fn once per
// enabled, non-dangling edge (both endpoints resolvable in nodeMap), passing the edge,
// its resolved endpoints and its linkid.LinkKey. It single-sources the "skip-disabled /
// resolve-endpoints / skip-dangling / compute-linkKey" preamble shared by Pass 1's link
// grouping (groupLinks) and Pass 2's PeerInfo construction (buildPeerInfo); each caller
// applies its own linkKey dedup inside fn. Iteration order and the surviving edge set
// are identical to the previous inline loops, so the produced allocations/bytes are
// unchanged.
//
// NOTE: the cross-package validator grouping loops (semantic_ports.go /
// semantic_edges.go) repeat this same enabled+resolve+linkKey preamble and are a future
// consolidation opportunity — deliberately left out of this package-local dedup to
// avoid a cross-package coupling and any allocation-byte risk.
func forEachEnabledEdge(topo *model.Topology, nodeMap map[string]*model.Node, fn func(edge *model.Edge, fromNode, toNode *model.Node, lk string)) {
	for i := range topo.Edges {
		edge := &topo.Edges[i]
		if !edge.IsEnabled {
			continue
		}
		fromNode := nodeMap[edge.FromNodeID]
		toNode := nodeMap[edge.ToNodeID]
		if fromNode == nil || toNode == nil {
			continue
		}
		fn(edge, fromNode, toNode, linkid.LinkKey(edge))
	}
}

// groupLinks folds every enabled, non-dangling edge into a linkEntity (Pass 1 phase
// 1), returning the entities in first-occurrence order plus a linkKey index. Multiple
// edges of the same node pair in the primary class share one linkKey: the first
// occurrence builds the entity, and subsequent edges (including the reverse and
// same-direction duplicates) fold into the same entity without rebuilding. A backup
// edge's linkKey carries a "#edgeID" suffix and is naturally unique, so each backup
// builds its own entity.
func groupLinks(topo *model.Topology, nodeMap map[string]*model.Node, domainMap map[string]*model.Domain) ([]*linkEntity, map[string]*linkEntity) {
	links := make([]*linkEntity, 0, len(topo.Edges))
	linkByKey := make(map[string]*linkEntity) // linkKey -> link entity (Pass 2 / write-back look up by LinkKey)

	forEachEnabledEdge(topo, nodeMap, func(edge *model.Edge, fromNode, toNode *model.Node, lk string) {
		// Multiple edges of the same node pair in the primary class share one linkKey:
		// the first occurrence builds the entity, and subsequent edges (including the
		// reverse and same-direction duplicates) fold into the same entity without
		// rebuilding. A backup edge's linkKey carries a "#edgeID" suffix and is
		// naturally unique, so each backup builds its own entity.
		if _, seen := linkByKey[lk]; seen {
			return
		}

		// Resolve the transit CIDR of the domain this link belongs to (empty falls back
		// to the default pool). It must stay consistent with allocateTransitPair's
		// internal default resolution and with DeriveClientConfigs' AllowedIPs
		// resolution (audit item D12). Unifying resolution through transitCIDRForNode
		// makes "link construction" and "external pin reservation" use one CIDR
		// ownership logic.
		transitCIDR := transitCIDRForNode(fromNode, domainMap)

		link := &linkEntity{
			linkKey:     lk,
			backup:      linkid.IsBackup(edge),
			primaryEdge: edge,
			fromNode:    fromNode,
			toNode:      toNode,
			transitCIDR: transitCIDR,
		}
		links = append(links, link)
		linkByKey[lk] = link
	})

	return links, linkByKey
}

// preallocateLinkResources runs Pass 1's reserve-then-gap-fill (Spec B) over the
// grouped links and returns the per-link allocation map keyed by linkid.LinkKey.
//
// Order-independence (I2) is guaranteed by construction: first reserve all pins in the
// topology into the resource pools, then gap-fill the unpinned links. As a result a
// new link never takes a value already occupied by an existing link, and existing
// links' values never move (I1/I3/I4). gap-fill iterates sorted by linkKey and picks
// the lowest free slot within a pool (the pinKey-deterministic order required by Spec
// B): the reservation set a link sees depends only on the topology's current pins and
// on unpinned links with a smaller pinKey, independent of array position and of the
// link's own delete/re-add history, which guarantees delete/re-add idempotence
// (I9/G1). reserved carries the pins of edges OUTSIDE this subgraph; a full compile
// passes nil, making the phase-2.5 reservation a no-op (I1, byte-identical to before).
//
// This function sorts links in place (Pass 1 phase 4's linkKey ordering). The caller's
// Pass 2 keys off linkByKey (a map), so the reordering is contained here.
func preallocateLinkResources(links []*linkEntity, reserved *ReservedAllocations) (map[string]*pairAllocation, error) {
	allocations := make(map[string]*pairAllocation) // key: linkid.LinkKey(edge)

	// ---- Reservation sets ----
	// Ports keyed by node; transit IPs stored verbatim as IP strings per CIDR pool (no
	// index reverse-lookup — see Spec B's robust choice); link-locals globally unique.
	usedPorts := make(map[string]map[int]bool)         // nodeID -> set of ports
	usedTransitIPs := make(map[string]map[string]bool) // cidr -> set of IP strings
	usedLinkLocals := make(map[string]bool)            // set of link-local strings

	markPort := func(nodeID string, port int) {
		if usedPorts[nodeID] == nil {
			usedPorts[nodeID] = make(map[int]bool)
		}
		usedPorts[nodeID][port] = true
	}
	markTransit := func(cidr, ip string) {
		if usedTransitIPs[cidr] == nil {
			usedTransitIPs[cidr] = make(map[string]bool)
		}
		usedTransitIPs[cidr][ip] = true
	}
	transitUsed := func(cidr, ip string) bool {
		return usedTransitIPs[cidr] != nil && usedTransitIPs[cidr][ip]
	}

	// ======== Pass 1 phase 2.5: reserve resources occupied by "outside-the-subgraph" edges ========
	// Before this subgraph's own pin reservation and gap-fill, mark as used the
	// resources in reserved (edges in the full topology that are not in this subgraph
	// yet still hold pins). This way any "unpinned, needs gap-fill" edge inside the
	// subgraph avoids them, and cross-subgraph collisions disappear at the source. Only
	// a subgraph compile passes reserved; a full compile passes nil, making this a
	// no-op with unchanged behavior (I1).
	if reserved != nil {
		for nodeID, ports := range reserved.ports {
			for port := range ports {
				markPort(nodeID, port)
			}
		}
		for cidr, ips := range reserved.transitIPs {
			for ip := range ips {
				markTransit(cidr, ip)
			}
		}
		for ll := range reserved.linkLocals {
			usedLinkLocals[ll] = true
		}
	}

	// ======== Pass 1 phase 3: reserve all pins ========
	// Before any gap-fill, reserve each link's valid pins resource by resource. Transit,
	// link-local, and ordinary-link port pins require complete pairs. A client link's one
	// valid non-client-side port is the deliberate single-sided exception; every other
	// partial resource is treated as unpinned here after validation has rejected it.
	pinnedAllocations := make(map[string]*pairAllocation) // linkKey -> allocation built directly from pins
	for _, link := range links {
		// Pins are taken from this link's primaryEdge: a unified primary link's pins are
		// pinned on its primary edge, and a backup link's pins are pinned on the backup
		// edge itself (where primaryEdge is that backup edge).
		edge := link.primaryEdge
		isFromClient := link.fromNode.Role == "client"
		isToClient := link.toNode.Role == "client"

		alloc := &pairAllocation{
			fromNodeID: link.fromNode.ID,
			toNodeID:   link.toNode.ID,
		}
		hasAnyPin := false

		// Port pins normally form a pair. A client link is the deliberate exception: the
		// client endpoint uses its shared wg0 and therefore has no per-link port, while the
		// non-client endpoint still owns one real per-link interface/listen port. Semantic
		// validation guarantees that only that non-client side can be present.
		switch {
		case isFromClient && !isToClient && edge.PinnedToPort > 0:
			alloc.toPort = edge.PinnedToPort
			markPort(link.toNode.ID, edge.PinnedToPort)
			hasAnyPin = true
		case isToClient && !isFromClient && edge.PinnedFromPort > 0:
			alloc.fromPort = edge.PinnedFromPort
			markPort(link.fromNode.ID, edge.PinnedFromPort)
			hasAnyPin = true
		case !isFromClient && !isToClient && edge.PinnedFromPort > 0 && edge.PinnedToPort > 0:
			alloc.fromPort = edge.PinnedFromPort
			alloc.toPort = edge.PinnedToPort
			markPort(link.fromNode.ID, edge.PinnedFromPort)
			markPort(link.toNode.ID, edge.PinnedToPort)
			hasAnyPin = true
		}

		// transit IP pin (treated as pinned only when complete-paired).
		if edge.PinnedFromTransitIP != "" && edge.PinnedToTransitIP != "" {
			alloc.localTransit = edge.PinnedFromTransitIP
			alloc.remoteTransit = edge.PinnedToTransitIP
			markTransit(link.transitCIDR, edge.PinnedFromTransitIP)
			markTransit(link.transitCIDR, edge.PinnedToTransitIP)
			hasAnyPin = true
		}

		// link-local pin (treated as pinned only when complete-paired).
		if edge.PinnedFromLinkLocal != "" && edge.PinnedToLinkLocal != "" {
			alloc.localLL = edge.PinnedFromLinkLocal
			alloc.remoteLL = edge.PinnedToLinkLocal
			usedLinkLocals[edge.PinnedFromLinkLocal] = true
			usedLinkLocals[edge.PinnedToLinkLocal] = true
			hasAnyPin = true
		}

		if hasAnyPin {
			pinnedAllocations[link.linkKey] = alloc
		}
	}

	// ======== Pass 1 phase 4: gap-fill the unpinned resources ========
	// Iterate sorted by linkKey to keep candidate order independent of array position
	// (the identity-ordered gap-fill required by the spec). In a single-edge pair
	// linkKey==pinKey, so the sort order and each value are byte-identical to before the
	// parallel-link change. Each resource takes the lowest free slot within its pool;
	// because reservation comes first and the iteration order is decided only by
	// linkKey, deleting and re-adding the same link identity sees the same reservation
	// set and reproduces the same value (I2/I9).
	sort.Slice(links, func(i, j int) bool { return links[i].linkKey < links[j].linkKey })

	for _, link := range links {
		fromNode := link.fromNode
		toNode := link.toNode
		isFromClient := fromNode.Role == "client"
		isToClient := toNode.Role == "client"

		// Take this linkKey's (partial) pin allocation as the starting point, filling in
		// the unpinned resources on top of it.
		alloc := pinnedAllocations[link.linkKey]
		if alloc == nil {
			alloc = &pairAllocation{fromNodeID: fromNode.ID, toNodeID: toNode.ID}
		}

		// ---- Ports: if unpinned, take per side "the lowest free port not below the node base" ----
		// The client side does not take part in per-peer port allocation (it uses a
		// single wg0), so its port stays 0 and is not reserved; but the "non-client side"
		// (router/relay/gateway) of an edge touching a client still needs a listen port
		// allocated, otherwise DeriveClientConfigs cannot tell which port the client
		// should dial. Hence each side is decided independently. Ordinary links carry a
		// complete pair; client links may carry the one valid non-client-side pin. In both
		// cases, any reserved side means the port allocation is already complete for that
		// link shape and gap-fill is skipped.
		portsPinned := alloc.fromPort > 0 || alloc.toPort > 0
		if !portsPinned {
			if !isFromClient {
				fromPort, err := lowestFreePort(fromNode, usedPorts)
				if err != nil {
					return nil, err
				}
				markPort(fromNode.ID, fromPort)
				alloc.fromPort = fromPort
			}
			if !isToClient {
				toPort, err := lowestFreePort(toNode, usedPorts)
				if err != nil {
					return nil, err
				}
				markPort(toNode.ID, toPort)
				alloc.toPort = toPort
			}
		}

		// ---- transit IP pair: if unpinned, take the lowest free pair in the per-CIDR pool ----
		transitPinned := alloc.localTransit != "" && alloc.remoteTransit != ""
		if !transitPinned {
			localTransit, remoteTransit, err := gapFillTransitPair(link.transitCIDR, transitUsed)
			if err != nil {
				// Propagate the inner coded error (CodeTransit*); the English wrapper adds
				// node context for logs/CLI only — errors.As still surfaces the inner code.
				return nil, fmt.Errorf("transit address allocation failed for %s<->%s: %w", fromNode.Name, toNode.Name, err)
			}
			markTransit(link.transitCIDR, localTransit)
			markTransit(link.transitCIDR, remoteTransit)
			alloc.localTransit = localTransit
			alloc.remoteTransit = remoteTransit
		}

		// ---- link-local pair: if unpinned, take the lowest free pair ----
		llPinned := alloc.localLL != "" && alloc.remoteLL != ""
		if !llPinned {
			localLL, remoteLL := gapFillLinkLocalPair(usedLinkLocals)
			usedLinkLocals[localLL] = true
			usedLinkLocals[remoteLL] = true
			alloc.localLL = localLL
			alloc.remoteLL = remoteLL
		}

		// The link allocation is keyed ONLY by linkid.LinkKey (spec I3: the per-peer
		// allocation identity is the linkKey). Pass 2 / write-back / DeriveClientConfigs all
		// look up by linkid.LinkKey(edge). The old bidirectional "from->to" directed-key alias
		// was removed in framework-refactor plan-10's dead-code sweep: production never read the
		// map by a directed key (its "old callers" no longer exist); only a transit-pool test
		// queried it, now updated to look up by linkid.PinKey. Removing the redundant key changes
		// no allocation value — every reader already uses the linkKey.
		allocations[link.linkKey] = alloc
	}

	return allocations, nil
}

// buildPeerInfo runs Pass 2: for each unified link it appends the forward + reverse
// PeerInfo (or, for a client link, only the router-side PeerInfo) into peerMap. It
// iterates edges but gates on linkKey (addedLinks) so a node pair's multiple
// primary-class edges fold into ONE pair of PeerInfo (the first primary-class edge in
// topo.Edges order drives creation, keeping the old "first-edge orientation"
// semantics), while each backup edge — carrying a unique linkKey — produces its own
// independent pair. In a single-edge pair the behavior matches before the change.
// peerMap is mutated in place.
func buildPeerInfo(topo *model.Topology, keys map[string]KeyPair, nodeMap map[string]*model.Node, linkByKey map[string]*linkEntity, allocations map[string]*pairAllocation, edgeMap map[string]*model.Edge, mimicFallbackDefault string, peerMap map[string][]PeerInfo) {
	addedLinks := make(map[string]bool) // linkKey -> whether PeerInfo has been produced for this link

	forEachEnabledEdge(topo, nodeMap, func(edge *model.Edge, fromNode, toNode *model.Node, lk string) {
		// lk is this edge's link identity (primary-class edge → pinKey; backup edge →
		// pinKey#edgeID); gate on it so each unified link produces its PeerInfo pair once.
		if addedLinks[lk] {
			return
		}

		link := linkByKey[lk]
		if link == nil {
			return
		}
		alloc := allocations[lk]
		if alloc == nil {
			return
		}

		// A client node creates no PeerInfo in peerMap (the client uses a single wg0,
		// handled by DeriveClientConfigs).
		if fromNode.Role == "client" {
			// Create only the router-side PeerInfo (router -> client direction). A client
			// edge is never a backup, so the interface name takes the non-backup short path
			// (byte-identical to before the change). This link's mimic-ness depends on
			// primaryEdge.Transport (docs/spec/data-model/edge.md §TCP transport); the MTU
			// is derived from the router (toNode) node MTU via the mimic formula.
			mimic := isMimicEdge(link.primaryEdge)
			{
				fromKey, _ := keys[fromNode.ID]
				isForward := alloc.fromNodeID == fromNode.ID

				// The router is this link's OTHER endpoint, so it reads the reverse orientation.
				routerListenPort, _, routerLocalTransit, routerRemoteTransit, routerLocalLL, routerRemoteLL := alloc.oriented(!isForward)

				routerPeer := PeerInfo{
					NodeID:              fromNode.ID,
					NodeName:            fromNode.Name,
					PublicKey:           fromKey.PublicKey,
					OverlayIP:           fromNode.OverlayIP,
					AllowedIPs:          []string{fromNode.OverlayIP + "/32"},
					Endpoint:            "",
					PersistentKeepalive: 0,
					InterfaceName:       naming.WgInterfaceNameForEdge(fromNode.Name, link.primaryEdge.ID, link.backup),
					ListenPort:          routerListenPort,
					LocalTransitIP:      routerLocalTransit,
					RemoteTransitIP:     routerRemoteTransit,
					LocalLinkLocal:      routerLocalLL,
					RemoteLinkLocal:     routerRemoteLL,
					IsClientPeer:        true,
					ClientOverlayIP:     fromNode.OverlayIP,
					Mimic:               mimic,
					MimicFallback:       resolveMimicFallback(link.primaryEdge.MimicFallback, mimicFallbackDefault),
					MTU:                 effectiveMTU(toNode.MTU, mimic),
				}

				peerMap[toNode.ID] = append(peerMap[toNode.ID], routerPeer)
			}
			addedLinks[lk] = true
			return
		}

		// In Pass 2 the producing edge is ALWAYS the first enabled edge of this linkKey —
		// the same representative/primary edge that set alloc.fromNodeID in Pass 1 (both
		// passes iterate topo.Edges in order under identical skip-disabled / skip-dangling
		// filters and gate on the linkKey). So the edge's direction always matches alloc's
		// canonical from/to: isForward is invariantly true here. The panic documents and
		// guards that invariant; the reverse-orientation reads below (oriented(!isForward))
		// intentionally mirror to the OTHER endpoint and are NOT a live variable direction.
		isForward := alloc.fromNodeID == fromNode.ID
		if !isForward {
			panic("compiler: Pass-2 producing edge must match alloc orientation (isForward invariant violated)")
		}

		toKey, _ := keys[toNode.ID]
		fromKey, _ := keys[fromNode.ID]

		// This link's dial-direction policy, from the driving edge (== link.primaryEdge on the
		// producing iteration; the validator's conflict rule guarantees a single-edge pair
		// whenever the direction is not "both", so no other edge's value can be shadowed).
		linkDirection := effectiveLinkDirection(edge)

		// === Compute the endpoint (a user-specified port takes priority, otherwise use the pre-allocated port) ===
		endpoint := ""
		if edge.EndpointHost != "" {
			var portToUse int
			if edge.EndpointPort > 0 {
				// The user specified a NAT/port-forwarding override port
				portToUse = edge.EndpointPort
			} else {
				// Auto-allocate: use the remote (dialed) interface's allocated listen port.
				// isForward is invariantly true in Pass 2 (asserted above), so the remote end
				// is always the to-side: alloc.toPort.
				portToUse = alloc.toPort
			}
			endpoint = formatEndpoint(edge.EndpointHost, portToUse)
		}

		// === Compute PersistentKeepalive ===
		// A reverse primary edge existing == its from->to key being present in edgeMap
		// (edgeMap's key set is exactly the enabled non-backup edge directions), so this is
		// the byte-identical replacement for the retired enabledEdgeDirections bool set.
		keepalive := 0
		_, hasReverseEdge := edgeMap[toNode.ID+"->"+fromNode.ID]
		if !fromNode.Capabilities.CanAcceptInbound || !hasReverseEdge {
			keepalive = 25
		}

		// === Determine the local resources ===
		fromListenPort, _, localTransit, remoteTransit, localLL, remoteLL := alloc.oriented(isForward)

		// The interface name is generated from the link identity + backup flag (spec
		// naming.md "Edge-aware names"): a backup link is distinguished by hashing
		// "primaryEdge.ID (i.e. the backup edge's own ID)" so it does not share a name
		// with the node pair's primary link interface; a non-backup link falls back
		// byte-identically to WgInterfaceName.
		ifaceName := naming.WgInterfaceNameForEdge(toNode.Name, link.primaryEdge.ID, link.backup)
		allowedIPs := []string{"0.0.0.0/0", "::/0"}

		// This link's rxcost override: the forward and reverse peers belong to one link
		// and take the same value. Resolution order (spec babel.md "Link cost
		// resolution" / contract item 4): explicit Priority/Weight (D63) > backup preset
		// 384 > default 0.
		linkCost := deriveLinkCost(link.primaryEdge, link.backup)

		// Whether this link is mimic: depends on link.primaryEdge.Transport
		// (docs/spec/data-model/edge.md §TCP transport). The forward and reverse peers
		// belong to one link and take the same mimic flag; the MTU is computed per side
		// from the local node MTU via the mimic formula (docs/spec/artifacts/mimic.md
		// "MTU −12").
		mimic := isMimicEdge(link.primaryEdge)

		// If toNode is a client, create the router-side PeerInfo with the IsClientPeer flag set
		isToClient := toNode.Role == "client"

		peer := PeerInfo{
			NodeID:              toNode.ID,
			NodeName:            toNode.Name,
			PublicKey:           toKey.PublicKey,
			OverlayIP:           toNode.OverlayIP,
			AllowedIPs:          allowedIPs,
			Endpoint:            endpoint,
			PersistentKeepalive: keepalive,
			InterfaceName:       ifaceName,
			ListenPort:          fromListenPort,
			LocalTransitIP:      localTransit,
			RemoteTransitIP:     remoteTransit,
			LocalLinkLocal:      localLL,
			RemoteLinkLocal:     remoteLL,
			IsClientPeer:        isToClient,
			ClientOverlayIP:     "",
			LinkCost:            linkCost,
			Mimic:               mimic,
			MimicFallback:       resolveMimicFallback(link.primaryEdge.MimicFallback, mimicFallbackDefault),
			// The local interface belongs to fromNode, so derive from fromNode.MTU.
			MTU: effectiveMTU(fromNode.MTU, mimic),
		}
		if isToClient {
			peer.AllowedIPs = []string{toNode.OverlayIP + "/32"}
			peer.ClientOverlayIP = toNode.OverlayIP
		}

		peerMap[fromNode.ID] = append(peerMap[fromNode.ID], peer)

		// === Auto-generate the reverse peer (skip the client's reverse — the client side uses wg0) ===
		if isToClient {
			addedLinks[lk] = true
			return
		}

		// PeerInfo has been produced for this link: gate on linkKey to ensure the node
		// pair's multiple primary-class edges (including the reverse and same-direction
		// duplicates) do not produce again, while each backup produces independently
		// (each with its own linkKey).
		addedLinks[lk] = true
		{
			reverseKeepalive := 0
			if !toNode.Capabilities.CanAcceptInbound {
				reverseKeepalive = 25
			}

			// The reverse interface names fromNode's tunnel; belonging to one link, it
			// keeps the same edgeID + backup flag.
			reverseIfaceName := naming.WgInterfaceNameForEdge(fromNode.Name, link.primaryEdge.ID, link.backup)

			// fromNode interface's allocated listen port (used when the reverse peer dials
			// back to fromNode). isForward is invariantly true in Pass 2 (asserted above),
			// so fromNode is alloc's from-side and its listen port is alloc.fromPort.
			fromSideListenPort := alloc.fromPort

			// Resolve the reverse peer's endpoint:
			//  0. A "forward" single-linked edge suppresses the reverse dial ENTIRELY (both the
			//     explicit-reverse-edge branch and the public-endpoint fallback): the reverse
			//     peer keeps AllowedIPs but never initiates, so it can never race the forward
			//     path's relay/accelerator endpoint via WireGuard endpoint roaming;
			//  1. When an explicit reverse edge exists and carries a host, resolve by the
			//     forward rule (a user-specified port takes priority, otherwise use fromNode's
			//     allocated port);
			//  2. Otherwise, if fromNode is publicly reachable and has a public endpoint
			//     configured, fall back to fromNode's public host + fromNode's allocated listen
			//     port (never use public_endpoints[0].Port — that is a node-reachability hint,
			//     not this link's listen port, and misusing it reproduces the port-ownership bug
			//     on the server).
			reverseEndpoint := ""
			if linkDirection != model.EdgeLinkDirectionForward {
				if reverseEdge, ok := edgeMap[toNode.ID+"->"+fromNode.ID]; ok && reverseEdge.EndpointHost != "" {
					if reverseEdge.EndpointPort > 0 {
						// The user specified a NAT/port-forwarding override port
						reverseEndpoint = formatEndpoint(reverseEdge.EndpointHost, reverseEdge.EndpointPort)
					} else {
						// Auto-allocate: use fromNode interface's allocated listen port
						reverseEndpoint = formatEndpoint(reverseEdge.EndpointHost, fromSideListenPort)
					}
				} else if fromNode.Capabilities.HasPublicIP && len(fromNode.PublicEndpoints) > 0 {
					// Fallback: no reverse edge (or its host is empty) and fromNode is publicly reachable
					reverseEndpoint = formatEndpoint(fromNode.PublicEndpoints[0].Host, fromSideListenPort)
				}
			}

			// The reverse peer's resources are swapped relative to the forward
			toListenPort, _, revLocalTransit, revRemoteTransit, revLocalLL, revRemoteLL := alloc.oriented(!isForward)

			reversePeer := PeerInfo{
				NodeID:              fromNode.ID,
				NodeName:            fromNode.Name,
				PublicKey:           fromKey.PublicKey,
				OverlayIP:           fromNode.OverlayIP,
				AllowedIPs:          allowedIPs,
				Endpoint:            reverseEndpoint,
				PersistentKeepalive: reverseKeepalive,
				InterfaceName:       reverseIfaceName,
				ListenPort:          toListenPort,
				LocalTransitIP:      revLocalTransit,
				RemoteTransitIP:     revRemoteTransit,
				LocalLinkLocal:      revLocalLL,
				RemoteLinkLocal:     revRemoteLL,
				// The reverse peer shares the same edge as the forward, keeping the same rxcost override value (D63).
				LinkCost: linkCost,
				// The reverse peer belongs to one link → same mimic flag; the local
				// interface belongs to toNode, so derive from toNode.MTU
				// (docs/spec/artifacts/mimic.md "MTU −12").
				Mimic:         mimic,
				MimicFallback: resolveMimicFallback(link.primaryEdge.MimicFallback, mimicFallbackDefault),
				MTU:           effectiveMTU(toNode.MTU, mimic),
			}

			peerMap[toNode.ID] = append(peerMap[toNode.ID], reversePeer)
		}
	})
}

// allocateTransitPair allocates a pair of transit IPv4 addresses by index and transitCIDR.
// If transitCIDR is empty, the default allocconst.DefaultTransitCIDR (10.10.0.0/24) is used.
// Each pair occupies 2 addresses: pair N → (network+2N+1, network+2N+2).
// The address pool spans only the usable host range [network+1, broadcast-1]: the
// network address and broadcast address are never allocated (audit item D48).
// When either address would land on the network address, broadcast address, or
// outside the subnet range, a pool-exhausted error is returned.
//
// Stable signature: a later phase rewrites the pair-allocation main loop on top of
// this function to support pins, so the (index, transitCIDR) -> (ip1, ip2, error)
// shape is kept unchanged.
func allocateTransitPair(index int, transitCIDR string) (string, string, error) {
	if transitCIDR == "" {
		transitCIDR = allocconst.DefaultTransitCIDR
	}

	_, ipNet, err := net.ParseCIDR(transitCIDR)
	if err != nil {
		return "", "", fmt.Errorf("invalid transit CIDR %q: %w", transitCIDR, err)
	}

	baseIP := ipNet.IP.To4()
	if baseIP == nil {
		return "", "", fmt.Errorf("transit CIDR must be IPv4: %q", transitCIDR)
	}

	// Derive the network and broadcast addresses generically from the mask (not hardcoded for /24).
	networkAddr := binary.BigEndian.Uint32(baseIP)
	maskBits, _ := ipNet.Mask.Size()
	// hostBits = 32 - maskBits; broadcast address = network address | (2^hostBits - 1).
	// Handle masks without usable broadcast bits (e.g. /31, /32) conservatively: simply
	// declare that the pool cannot hold any pair.
	hostBits := 32 - maskBits
	if hostBits < 2 {
		return "", "", fmt.Errorf("transit address pool exhausted (CIDR: %s, index: %d)", transitCIDR, index)
	}
	hostMask := uint32(1)<<uint(hostBits) - 1
	broadcastAddr := networkAddr | hostMask

	offset := uint32(2*index + 1)
	addr1 := networkAddr + offset
	addr2 := networkAddr + offset + 1

	// Out of range (including addr2 < addr1 from integer wraparound), or hitting the
	// network or broadcast address, is all treated as pool exhaustion. The usable host
	// range is the open interval (networkAddr, broadcastAddr), i.e. [networkAddr+1, broadcastAddr-1].
	if addr2 < addr1 ||
		addr1 <= networkAddr || addr1 >= broadcastAddr ||
		addr2 <= networkAddr || addr2 >= broadcastAddr {
		return "", "", fmt.Errorf("transit address pool exhausted (CIDR: %s, index: %d)", transitCIDR, index)
	}

	ip1 := make(net.IP, 4)
	ip2 := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip1, addr1)
	binary.BigEndian.PutUint32(ip2, addr2)

	return ip1.String(), ip2.String(), nil
}

// The canonical link identity has been moved up to internal/linkid (a leaf package
// depending only on model + stdlib), so the compiler and validator share one set of
// PinKey/LinkKey/IsBackup semantics, eliminating duplicate literals. See
// docs/spec/compiler/allocation-stability.md (Canonical link key / Link identity).

// transitPoolPairCount returns the number of usable pairs in a transit CIDR pool
// (the pair index upper bound). Uses the same mask derivation as allocateTransitPair:
// the usable host range is (network, broadcast), i.e. 2^hostBits - 2 host addresses,
// two per pair → (2^hostBits - 2) / 2 pairs.
// /24 → 127 pairs, /29 → 3 pairs, /30 → 1 pair; hostBits < 2 (/31, /32) → 0 pairs.
func transitPoolPairCount(transitCIDR string) (int, error) {
	if transitCIDR == "" {
		transitCIDR = allocconst.DefaultTransitCIDR
	}
	_, ipNet, err := net.ParseCIDR(transitCIDR)
	if err != nil {
		return 0, apierr.New(apierr.CodeTransitCIDRInvalid).With("cidr", transitCIDR).With("detail", err.Error()).Wrap(err)
	}
	if ipNet.IP.To4() == nil {
		return 0, apierr.New(apierr.CodeTransitCIDRNotIPv4).With("cidr", transitCIDR)
	}
	maskBits, _ := ipNet.Mask.Size()
	hostBits := 32 - maskBits
	if hostBits < 2 {
		return 0, nil
	}
	usableHosts := (uint64(1) << uint(hostBits)) - 2
	return int(usableHosts / 2), nil
}

// gapFillTransitPair allocates a pair of transit IPs in the per-CIDR pool for an
// unpinned link.
//
// Selection strategy: scan upward from index 0 in the per-CIDR pool, skip any pair
// where either address is already reserved (usedTransitIPs), and return the first
// pair where both ends are free; if the whole pool is full, return a clean
// exhaustion error.
//
// This function is itself a pure function of "pool + reservation set"; its
// delete/re-add idempotence (Spec B G1) is guaranteed by the caller: Pass 1 phase 4
// reserves all pins first, then iterates the unpinned links sorted by pinKey. As a
// result the reservation set a link sees depends only on "the topology's current
// pins" and "unpinned links with a smaller pinKey", independent of the link's own
// delete/re-add history and of array position — deleting and re-adding the same node
// pair reproduces the same lowest free pair (satisfying I2/I9).
//
// This is exactly the spec requirement in docs/spec/compiler/allocation-stability.md
// "Hash-seeded gap-fill": "the order in which candidate links are assigned MUST be
// deterministic in pinKey (iterate unpinned links sorted by pinKey, and within a
// pool pick the lowest free slot)".
func gapFillTransitPair(transitCIDR string, transitUsed func(cidr, ip string) bool) (string, string, error) {
	poolPairs, err := transitPoolPairCount(transitCIDR)
	if err != nil {
		return "", "", err
	}
	if poolPairs <= 0 {
		return "", "", apierr.New(apierr.CodeTransitPoolExhausted).With("cidr", transitCIDR)
	}
	for index := 0; index < poolPairs; index++ {
		ip1, ip2, err := allocateTransitPair(index, transitCIDR)
		if err != nil {
			// Every index within the pool should be usable; defensively skip any unexpected out-of-range index.
			continue
		}
		if transitUsed(transitCIDR, ip1) || transitUsed(transitCIDR, ip2) {
			continue
		}
		return ip1, ip2, nil
	}
	return "", "", apierr.New(apierr.CodeTransitPoolExhausted).With("cidr", transitCIDR)
}

// gapFillLinkLocalPair allocates a pair of IPv6 link-locals for an unpinned link.
// Isomorphic to transit: scan upward from index 0, skip any pair where either end is
// already reserved (usedLinkLocals), and return the first pair where both ends are
// free. fe80::/10 is "effectively unlimited" for any real fleet size (I6), so the
// scan necessarily succeeds within finitely many steps. delete/re-add idempotence is
// likewise guaranteed by the caller's "reserve first, then iterate by pinKey".
func gapFillLinkLocalPair(usedLinkLocals map[string]bool) (string, string) {
	for index := 0; ; index++ {
		local, remote := allocateLinkLocalPair(index)
		if usedLinkLocals[local] || usedLinkLocals[remote] {
			continue
		}
		return local, remote
	}
}

// lowestFreePort returns a node's lowest free port not below the base port 51820
// (skipping used values in usedPorts). The base port is the fixed 51820 — per-node
// listen_port is meaningless under the per-peer interface model and has been removed.
// A valid port must not exceed 65535 (audit item D11): exceeding it returns a clean
// compile-time error, avoiding rendering an illegal port that wg-quick would reject
// only at deploy time. node is still needed for the error message (node.Name) and for
// per-node deduplication (node.ID).
func lowestFreePort(node *model.Node, usedPorts map[string]map[int]bool) (int, error) {
	const base = allocconst.WGListenPortBase
	used := usedPorts[node.ID]
	for port := base; port <= 65535; port++ {
		if used == nil || !used[port] {
			return port, nil
		}
	}
	return 0, apierr.New(apierr.CodeListenPortExhausted).With("node", node.Name).With("base", strconv.Itoa(base))
}

// deriveLinkCost derives a link's Babel rxcost override value.
// Resolution order (spec docs/spec/artifacts/babel.md "Link cost resolution" / contract item 4):
//  1. Explicit operator setting (D63): edge.Priority (>0) takes priority, otherwise edge.Weight (>0) — adopted verbatim;
//  2. backup preset: the link is a backup (backup==true) and has no explicit setting → allocconst.BackupDefaultLinkCost (384);
//  3. default: return 0 (left to the role preset's default cost; the renderer decides whether to omit the rxcost token).
func deriveLinkCost(edge *model.Edge, backup bool) int {
	if edge != nil {
		if edge.Priority > 0 {
			return edge.Priority
		}
		if edge.Weight > 0 {
			return edge.Weight
		}
	}
	if backup {
		return allocconst.BackupDefaultLinkCost
	}
	return 0
}

// allocateLinkLocalPair allocates a pair of IPv6 link-local addresses by index.
// IPv6 text is hexadecimal (audit item D70): must use %x not %d, otherwise fe80::11
// would be parsed as decimal 17 — contradicting the documented promise of
// "consecutive hexadecimal numbering". The link-local index uses the same pool's pair index.
// pair 0: fe80::1, fe80::2
// pair 1: fe80::3, fe80::4
// pair 5: fe80::b, fe80::c
func allocateLinkLocalPair(index int) (string, string) {
	base := 2*index + 1
	return fmt.Sprintf("fe80::%x", base), fmt.Sprintf("fe80::%x", base+1)
}

// ClientPeerInfo describes the information needed for a client node's wg0 configuration.
type ClientPeerInfo struct {
	// Client node information
	NodeID    string
	NodeName  string
	OverlayIP string

	// The effective MTU of the wg0 interface.
	// non-mimic: keep node.MTU as is (0 ⇒ renderer omits the MTU line, byte-unchanged).
	// mimic: ((node.MTU>0 ? node.MTU : 1420) − 12) (docs/spec/artifacts/mimic.md "MTU −12").
	MTU int

	// Whether the client's single outbound edge has mimic enabled (transport=="tcp").
	// The renderer uses this (together with ListenPort) to derive the client node's
	// set of mimic listen ports.
	Mimic bool

	// MimicFallback is the RESOLVED per-link mimic-fallback policy ("udp" or "none", never "").
	// Pure policy carried for plan-5's install-script branch; not in any rendered artifact yet.
	MimicFallback string

	// The client's WireGuard private key
	PrivateKey string

	// Router-side information
	RouterPublicKey string
	RouterEndpoint  string // host:port

	// List of domain CIDRs (used as AllowedIPs)
	DomainCIDRs []string

	// The client's listen port
	ListenPort int
}

// DeriveClientConfigs generates wg0 configuration info for all client nodes.
func DeriveClientConfigs(topo *model.Topology, keys map[string]KeyPair, allocations map[string]*pairAllocation, mimicFallbackDefault string) map[string]*ClientPeerInfo {
	configs := make(map[string]*ClientPeerInfo)

	nodeMap := make(map[string]*model.Node)
	for i := range topo.Nodes {
		nodeMap[topo.Nodes[i].ID] = &topo.Nodes[i]
	}

	for _, node := range topo.Nodes {
		if node.Role != "client" {
			continue
		}

		// Find the client's single outbound edge
		var clientEdge *model.Edge
		for i := range topo.Edges {
			e := &topo.Edges[i]
			if e.IsEnabled && e.FromNodeID == node.ID {
				clientEdge = e
				break
			}
		}
		if clientEdge == nil {
			continue
		}

		routerNode := nodeMap[clientEdge.ToNodeID]
		if routerNode == nil {
			continue
		}

		routerKey, _ := keys[routerNode.ID]
		clientKey, _ := keys[node.ID]

		// Get the router-side listen port: look up the allocation by the client outbound
		// edge's linkid.LinkKey (validation guarantees exactly one client edge that
		// cannot be a backup, so linkKey is the pinKey).
		alloc := allocations[linkid.LinkKey(clientEdge)]
		var routerPort int
		if alloc != nil {
			if alloc.fromNodeID == node.ID {
				routerPort = alloc.toPort
			} else {
				routerPort = alloc.fromPort
			}
		}

		// Build the endpoint (a user-specified port takes priority, otherwise use the auto-allocated router port)
		routerEndpoint := ""
		if clientEdge.EndpointHost != "" {
			var portToUse int
			if clientEdge.EndpointPort > 0 {
				portToUse = clientEdge.EndpointPort
			} else if routerPort > 0 {
				portToUse = routerPort
			}
			if portToUse > 0 {
				routerEndpoint = formatEndpoint(clientEdge.EndpointHost, portToUse)
			}
		}

		// AllowedIPs prefix set (D30, Decision 6):
		// The client's wg0 is its only tunnel to the entire overlay, so AllowedIPs cannot
		// cover only its own domain, otherwise cross-domain overlay, the router's
		// out-of-domain /32, and the transit subnet would all be blackholed on the client
		// side. Here we take the union of "the CIDRs of all domains" and "each domain's
		// resolved transit CIDR" (when domain.TransitCIDR is empty, fall back to the
		// default 10.10.0.0/24, consistent with allocateTransitPair's resolution rule).
		// Iterate in topo.Domains slice order for determinism, and deduplicate.
		var domainCIDRs []string
		seenCIDR := make(map[string]bool)
		appendCIDR := func(cidr string) {
			if cidr == "" || seenCIDR[cidr] {
				return
			}
			seenCIDR[cidr] = true
			domainCIDRs = append(domainCIDRs, cidr)
		}
		for i := range topo.Domains {
			appendCIDR(topo.Domains[i].CIDR)
		}
		for i := range topo.Domains {
			transitCIDR := topo.Domains[i].TransitCIDR
			if transitCIDR == "" {
				transitCIDR = allocconst.DefaultTransitCIDR
			}
			appendCIDR(transitCIDR)
		}

		// Client listen port (fixed base WGListenPortBase, 51820; per-node listen_port has been removed).
		listenPort := allocconst.WGListenPortBase

		// mimic-ness is taken from the transport of the client's single outbound edge
		// (docs/spec/data-model/edge.md §TCP transport); the MTU is derived from the
		// client (node) MTU via the mimic formula (docs/spec/artifacts/mimic.md "MTU −12").
		// When non-mimic it is byte-identical to before the change (node.MTU as is).
		mimic := isMimicEdge(clientEdge)

		configs[node.ID] = &ClientPeerInfo{
			NodeID:          node.ID,
			NodeName:        node.Name,
			OverlayIP:       node.OverlayIP,
			MTU:             effectiveMTU(node.MTU, mimic),
			Mimic:           mimic,
			MimicFallback:   resolveMimicFallback(clientEdge.MimicFallback, mimicFallbackDefault),
			PrivateKey:      clientKey.PrivateKey,
			RouterPublicKey: routerKey.PublicKey,
			RouterEndpoint:  routerEndpoint,
			DomainCIDRs:     domainCIDRs,
			ListenPort:      listenPort,
		}
	}

	return configs
}
