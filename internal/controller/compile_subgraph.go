package controller

// compile_subgraph.go contains the read-only compile half shared by stage and preview,
// deployment-ready projection, and optimistic allocation write-back.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/localcompile"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
)

// CompileSubgraph runs the read-only compile half shared by CompileAndStage and the
// operator compile-preview: build the deployment-ready subgraph of `topo` from registry-backed
// managed nodes and validated topology-backed manual nodes, then drive the frozen, zero-knowledge pipeline through the localcompile façade
// (the single compile authority) in AgentHeld custody — private keys are
// PRIVATEKEY_PLACEHOLDER, never real material. It performs NO store writes, NO allocation
// persist, NO export, and NO staging; the caller decides what to do with the rendered result.
//
// Returns a NIL result when no node is ready (subgraph.Nodes empty) — the caller
// handles that case (CompileAndStage purges + audits; a preview reports "nothing ready").
// `skipped` lists managed-node IDs dropped from the ready projection (the public wire field retains
// the historical name SkippedUnenrolled). Custody invariant: because the façade runs
// AgentHeld, neither the returned result nor anything rendered from it contains a real
// private key — making this safe to surface to an authenticated operator (PR6 preview).
//
// ctx bounds the compile: an early cancellation check short-circuits before the reserved-pin
// scan, and it is then threaded into the façade (CompileResultCtx) so the allocator's per-node
// scan is cancellable on a client disconnect — the per-node scan budget remains the hard DoS
// bound for an over-large CIDR. The frozen contract itself stays context-free (the TS port
// mirrors a context-free seam); ctx is the orthogonal Go-runtime param the live callers pass.
func CompileSubgraph(ctx context.Context, topo *model.Topology, nodes []Node, fs render.FetchSettings) (*compiler.CompileResult, model.Topology, []string, error) {
	subgraph, skipped, err := projectEnrolledSubgraph(topo, nodes)
	if err != nil {
		return nil, model.Topology{}, nil, err
	}
	if len(subgraph.Nodes) == 0 {
		return nil, subgraph, skipped, nil
	}

	signer, err := bundlesig.LoadConfigSignerFromEnv()
	if err != nil {
		return nil, model.Topology{}, nil, fmt.Errorf("controller: loading bundle signing key: %w", err)
	}
	return CompileSubgraphWithSigner(ctx, topo, nodes, fs, signer)
}

// CompileSubgraphWithSigner is CompileSubgraph with the optional tier-1 bundle signer already
// resolved. Stage/preview callers use it together with artifacts.ExportWithSigner so the public
// key embedded in install.sh and the detached signature sidecars are derived from one immutable
// signer snapshot rather than two filesystem/environment reads.
func CompileSubgraphWithSigner(ctx context.Context, topo *model.Topology, nodes []Node, fs render.FetchSettings, signer bundlesig.ConfigSigner) (*compiler.CompileResult, model.Topology, []string, error) {
	subgraph, skipped, err := projectEnrolledSubgraph(topo, nodes)
	if err != nil {
		return nil, model.Topology{}, nil, err
	}
	if len(subgraph.Nodes) == 0 {
		return nil, subgraph, skipped, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, subgraph, skipped, err
	}
	// Reserve the allocation pins held by edges in the FULL topology that are NOT in this
	// subgraph (dropped because a far end is not deployment-ready). Without this, the subgraph's
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
		SigningKey: signer,
		Reserved:   reserved,
		CompiledAt: time.Now(),
	})
	if err != nil {
		return nil, subgraph, skipped, fmt.Errorf("controller: compiling ready subgraph: %w", err)
	}
	return result, subgraph, skipped, nil
}

// projectEnrolledSubgraph performs the deployment-readiness half without rendering. Stage uses
// this preflight to preserve the security-critical empty-stage purge even when the configured
// bundle-signing key is unreadable: no ready node means no signed bytes are needed, but stale
// staged bundles must still be destroyed. CompileSubgraphWithSigner uses the same helper so the
// preflight and actual compile cannot disagree about manual-node admission or readiness.
func projectEnrolledSubgraph(topo *model.Topology, nodes []Node) (model.Topology, []string, error) {
	// Validate the operator-asserted identity of any MANUAL (hand-deployed, agent-less) node before it
	// is admitted: a manual node carries its own pre-known public key (no enrollment proves it), so it
	// must have one and it must be unique across the fleet. This runs on BOTH the stage and the compile
	// -preview path, so a missing/colliding manual key is a LOUD error, not the silent exclusion
	// the historical enrolledSubgraph helper would otherwise apply.
	if err := validateManualNodes(topo, nodes); err != nil {
		return model.Topology{}, nil, err
	}
	subgraph, skipped := enrolledSubgraph(topo, nodes)
	return subgraph, skipped, nil
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
// It returns the subgraph plus the list of excluded MANAGED node IDs. The historical
// skipped/SkippedUnenrolled name covers every managed node excluded from readiness,
// including a client whose target is not ready. Manual nodes are NEVER listed as
// skipped — they are intentionally agent-less. The input topology is never mutated
// (value copy).
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
			// A not-ready MANAGED node is "skipped". A not-ready manual
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

// clientTargetEnrolled is the historical helper name for checking whether a client
// has an enabled outbound edge whose dial target is deployment-ready. A manual target
// can be ready without enrollment. A client must have exactly one enabled outbound edge.
func clientTargetEnrolled(topo *model.Topology, clientID string, ready map[string]bool) bool {
	for _, e := range topo.Edges {
		if e.FromNodeID == clientID && e.IsEnabled && ready[e.ToNodeID] {
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
// Note (plan-2): CompareAndSetTopology always checks originalVersion, including when
// the derived bytes are identical, so a stale deployment snapshot cannot overwrite or
// silently pass a concurrent Save. A changed CAS write-back counts as a retained version
// like any other — the pinned post-stage shape is recoverable. A byte-identical CAS is a
// data/history no-op after that version check; otherwise routine incremental-enrollment
// staging could flush operator-authored versions from the bounded recovery window.
func persistAllocations(ctx context.Context, store Store, t TenantID, full, compiled *model.Topology, originalVersion int64) error {
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
			continue // edge not in the compiled subgraph (far end not ready) — leave unpinned
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
	// Compare even when write-back is byte-identical. The Store makes that case a no-op without
	// consuming history, but still catches an operator save that landed after this stage loaded its
	// design before this commit point. The deployment snapshot linearizes at this compare; a later
	// save remains the next unapplied draft, as it does for any save made after a deployment starts.
	if _, err := store.CompareAndSetTopology(ctx, t, originalVersion, raw); err != nil {
		return fmt.Errorf("controller: persisting allocations: %w", err)
	}
	return nil
}
