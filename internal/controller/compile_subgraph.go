package controller

// compile_subgraph.go — CompileSubgraph (the read-only compile half shared by stage and
// preview), the enrolled-subgraph projection (enrolledSubgraph, clientTargetEnrolled),
// and allocation write-back (persistAllocations). Split from compile.go (plan-2);
// no logic change.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/localcompile"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
)

// CompileSubgraph runs the read-only compile half shared by CompileAndStage and the
// operator compile-preview: build the enrolled subgraph of `topo` from the registry
// `nodes`, then drive the frozen, zero-knowledge pipeline through the localcompile façade
// (the single compile authority) in AgentHeld custody — private keys are
// PRIVATEKEY_PLACEHOLDER, never real material. It performs NO store writes, NO allocation
// persist, NO export, and NO staging; the caller decides what to do with the rendered result.
//
// Returns a NIL result when no node is enrolled (subgraph.Nodes empty) — the caller
// handles that case (CompileAndStage purges + audits; a preview reports "nothing
// enrolled"). `skipped` lists the node IDs present in the topology but dropped from the
// render because they are not yet enrolled. Custody invariant: because the façade runs
// AgentHeld, neither the returned result nor anything rendered from it contains a real
// private key — making this safe to surface to an authenticated operator (PR6 preview).
//
// ctx bounds the compile: an early cancellation check short-circuits before the reserved-pin
// scan, and it is then threaded into the façade (CompileResultCtx) so the allocator's per-node
// scan is cancellable on a client disconnect — the per-node scan budget remains the hard DoS
// bound for an over-large CIDR. The frozen contract itself stays context-free (the TS port
// mirrors a context-free seam); ctx is the orthogonal Go-runtime param the live callers pass.
func CompileSubgraph(ctx context.Context, topo *model.Topology, nodes []Node, fs render.FetchSettings) (*compiler.CompileResult, model.Topology, []string, error) {
	// Validate the operator-asserted identity of any MANUAL (hand-deployed, agent-less) node before it
	// is admitted: a manual node carries its own pre-known public key (no enrollment proves it), so it
	// must have one and it must be unique across the fleet. This runs on BOTH the stage and the compile
	// -preview path (both call here), so a missing/colliding manual key is a LOUD error, not the silent
	// exclusion enrolledSubgraph would otherwise apply.
	if err := validateManualNodes(topo, nodes); err != nil {
		return nil, model.Topology{}, nil, err
	}
	subgraph, skipped := enrolledSubgraph(topo, nodes)
	if len(subgraph.Nodes) == 0 {
		return nil, subgraph, skipped, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, subgraph, skipped, err
	}
	// Reserve the allocation pins held by edges in the FULL topology that are NOT in this
	// subgraph (dropped because a far end is not yet enrolled). Without this, the subgraph's
	// gap-fill restarts from .1 and can hand a fresh edge a transit IP / port / link-local that
	// a dropped edge still pins in storage — and since each incremental enrollment compiles a
	// DIFFERENT subgraph, two edges that were never compiled together collide (the "pin occupied
	// by two different links" validate error). Reserving the excluded edges' pins makes the
	// subgraph allocate around them, so a new node's links never collide with an out-of-subgraph
	// link. (Existing corruption is cleaned by the normalize-layer heal; this only prevents new.)
	included := make(map[string]bool, len(subgraph.Edges))
	for i := range subgraph.Edges {
		included[subgraph.Edges[i].ID] = true
	}
	reserved := compiler.BuildReservedFromExcludedEdges(topo, included)

	// Drive the frozen pipeline through the single shared façade (the local compile
	// authority), exactly as the air-gap callers do — keeping the controller's rendered
	// bundles byte-identical (the keystone bundle digest + bundle.sig depend on it). The
	// custody and subgraph semantics are preserved verbatim: Custody AgentHeld (private
	// keys stay PRIVATEKEY_PLACEHOLDER, never real material), the default wgtypesKeygen,
	// the WithReserved subgraph allocation path via req.Reserved, and the controller's
	// FetchSettings. CompileResult returns the raw result so CompileAndStage / the operator
	// preview keep their *compiler.CompileResult shape. CompiledAt feeds only
	// manifest.json's compiled_at (out of the checksummed set), matching the prior
	// time.Now() stamp.
	result, err := localcompile.CompileResultCtx(ctx, localcompile.CompileRequest{
		Topology:   subgraph,
		Custody:    render.AgentHeld,
		Fetch:      fs,
		Reserved:   reserved,
		CompiledAt: time.Now(),
	})
	if err != nil {
		return nil, subgraph, skipped, fmt.Errorf("controller: compiling enrolled subgraph: %w", err)
	}
	return result, subgraph, skipped, nil
}

// enrolledSubgraph projects a stored topology down to its ready subgraph under the
// render-what's-ready policy.
//
// A topology node is "ready" (included) when its authoritative WireGuard PUBLIC key
// is known. For a MANAGED node (the default) that key comes from the enrollment
// registry — the node must be NodeApproved with a non-empty WGPublicKey, and the
// agent holds the matching private key. For a MANUAL node (deployment_mode=="manual",
// hand-deployed, no agent, never enrolls) the key comes from the TOPOLOGY itself: the
// operator registered the node's design-time WireGuardPublicKey and holds its private
// key off-controller, so the controller stays zero-knowledge — it only ever sees the
// public half. Either way the included node has WireGuardPrivateKey CLEARED — a stray
// private key from an imported topology must never reach a rendered bundle.
//
// Any edge whose FromNodeID or ToNodeID is not ready is dropped; a managed edge
// activates on a later deploy once its far end enrolls, while a manual far end is
// ready immediately from its topology key.
//
// It returns the subgraph plus the list of excluded MANAGED node IDs (skipped =
// not-yet-enrolled). Manual nodes are NEVER listed as skipped — they are intentionally
// agent-less, not "not yet ready". The input topology is never mutated (value copy).
func enrolledSubgraph(topo *model.Topology, nodes []Node) (model.Topology, []string) {
	// registry indexes the enrolled public key by node ID. A managed node is enrolled iff
	// it is NodeApproved with a non-empty WGPublicKey — the admission test.
	registry := make(map[string]string, len(nodes))
	for _, n := range nodes {
		if n.Status == NodeApproved && n.WGPublicKey != "" {
			registry[n.NodeID] = n.WGPublicKey
		}
	}

	sub := model.Topology{
		Project:            topo.Project,
		Domains:            topo.Domains,
		RoutePolicies:      topo.RoutePolicies,
		AllocSchemaVersion: topo.AllocSchemaVersion,
	}

	// readyKey resolves a node's authoritative public key: the topology key for a manual
	// node, the enrollment registry for a managed node. Empty == not ready.
	readyKey := func(node model.Node) string {
		if node.IsManual() {
			return node.WireGuardPublicKey
		}
		return registry[node.ID]
	}

	// First pass: the set of nodes whose public key is known (enrolled-managed OR manual).
	ready := make(map[string]bool, len(topo.Nodes))
	for _, node := range topo.Nodes {
		if readyKey(node) != "" {
			ready[node.ID] = true
		}
	}

	// Render-what's-ready for the client role. A client requires EXACTLY ONE enabled
	// outbound edge (compiler validateClientEdges is a HARD error otherwise), so a ready
	// client whose dial target is not yet ready would be left edgeless and fail the whole
	// stage. Treat such a client as itself not-ready: exclude it now and let it activate
	// on a later deploy once its router/relay/gateway is ready.
	for _, node := range topo.Nodes {
		if ready[node.ID] && node.Role == "client" && !clientTargetEnrolled(topo, node.ID, ready) {
			delete(ready, node.ID)
		}
	}

	var skipped []string
	for _, node := range topo.Nodes { // value copy: never mutate the caller's slice
		if !ready[node.ID] {
			// A not-ready MANAGED node is "skipped" (waiting to enroll). A not-ready manual
			// node would mean a missing topology pubkey — a design error the controller-registration
			// validator will reject (plan-2); until then this branch defensively excludes it. Either
			// way a manual node is never reported as a transient enrollment skip.
			if !node.IsManual() {
				skipped = append(skipped, node.ID)
			}
			continue
		}
		node.WireGuardPublicKey = readyKey(node)
		node.WireGuardPrivateKey = ""
		sub.Nodes = append(sub.Nodes, node)
	}

	// Drop any edge whose far end is not ready: it activates on a later deploy.
	for _, edge := range topo.Edges {
		if ready[edge.FromNodeID] && ready[edge.ToNodeID] {
			sub.Edges = append(sub.Edges, edge)
		}
	}

	return sub, skipped
}

// clientTargetEnrolled reports whether a client node has an enabled outbound edge
// whose dial target is enrolled — the readiness condition for compiling the client
// (a client must have exactly one enabled outbound edge).
func clientTargetEnrolled(topo *model.Topology, clientID string, enrolled map[string]bool) bool {
	for _, e := range topo.Edges {
		if e.FromNodeID == clientID && e.IsEnabled && enrolled[e.ToNodeID] {
			return true
		}
	}
	return false
}

// persistAllocations merges the allocation pins the compiler stamped onto the
// compiled subgraph back into the FULL stored topology, then re-stores it. It copies
// per-node OverlayIP and the per-edge pin set (transit IPs, link-locals, ports,
// CompiledPort) by ID — never any key material, so the stored topology stays
// public-keys-only — and stamps AllocSchemaVersion. The next CompileAndStage then
// finds these pins in the stored topology and the compiler reuses them (sticky-pin),
// which is what keeps allocations stable across incremental enrollment (I10).
//
// Note (plan-2): a PutTopology write-back that CHANGES the stored topology counts
// as a retained version like any other — the pinned post-stage shape is itself a
// state an operator may want to recover. A write-back whose bytes equal the stored
// record (sticky pins re-derived identically, the common re-stage case) is SKIPPED:
// burning one of the bounded history slots per no-op stage would let routine
// incremental-enrollment staging flush every operator-authored version out of the
// recovery window (review finding, D7).
func persistAllocations(ctx context.Context, store Store, t TenantID, full, compiled *model.Topology, originalJSON []byte) error {
	ipByID := make(map[string]string, len(compiled.Nodes))
	for _, n := range compiled.Nodes {
		ipByID[n.ID] = n.OverlayIP
	}
	edgeByID := make(map[string]model.Edge, len(compiled.Edges))
	for _, e := range compiled.Edges {
		edgeByID[e.ID] = e
	}

	for i := range full.Nodes {
		if ip, ok := ipByID[full.Nodes[i].ID]; ok && ip != "" {
			full.Nodes[i].OverlayIP = ip
		}
	}
	for i := range full.Edges {
		c, ok := edgeByID[full.Edges[i].ID]
		if !ok {
			continue // edge not in the compiled subgraph (far end unenrolled) — leave unpinned
		}
		full.Edges[i].CompiledPort = c.CompiledPort
		full.Edges[i].PinnedFromPort = c.PinnedFromPort
		full.Edges[i].PinnedToPort = c.PinnedToPort
		full.Edges[i].PinnedFromTransitIP = c.PinnedFromTransitIP
		full.Edges[i].PinnedToTransitIP = c.PinnedToTransitIP
		full.Edges[i].PinnedFromLinkLocal = c.PinnedFromLinkLocal
		full.Edges[i].PinnedToLinkLocal = c.PinnedToLinkLocal
	}
	full.AllocSchemaVersion = compiled.AllocSchemaVersion

	raw, err := json.Marshal(full)
	if err != nil {
		return fmt.Errorf("controller: marshaling topology with persisted allocations: %w", err)
	}
	// No-op write-back: the stored record is canonical json.Marshal output (the
	// update-topology custody gate canonicalizes), so byte equality here means the
	// pins changed nothing. Skip the put — do not burn a history slot.
	if bytes.Equal(raw, originalJSON) {
		return nil
	}
	if _, err := store.PutTopology(ctx, t, raw); err != nil {
		return fmt.Errorf("controller: persisting allocations: %w", err)
	}
	return nil
}
