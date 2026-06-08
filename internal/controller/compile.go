package controller

// compile.go is the controller's compile-and-stage step (plan-4.3a): it turns the
// stored, public-keys-only topology plus the enrolled registry into signed
// per-node bundles staged at the next generation.
//
// Two design commitments shape this file:
//
//   - REUSE the frozen pipeline, do not reimplement it. The compiler, renderer,
//     and exporter stay frozen and dependency-minimal (see
//     docs/spec/controller/persistence.md §The quarantine boundary). This step
//     drives them exactly as the air-gap CLI/API does — render.GenerateKeys (in
//     AgentHeld custody) → compiler.Compile → render.All → artifacts.Export — and
//     reads the export back through a temp directory. The temp-dir round-trip is a
//     deliberate no-duplication choice; an in-memory Export is a possible later
//     optimization (docs/spec/controller/deploy.md §Reusing the frozen pipeline).
//
//   - RENDER WHAT'S READY. Only the enrolled subgraph is compiled: a topology node
//     is included iff its registry record is NodeApproved with a non-empty
//     WGPublicKey, and any edge whose far end is not enrolled is dropped. Excluded
//     nodes are reported as SkippedUnenrolled and an edge to an unenrolled peer
//     fills in on a later deploy once that peer has enrolled. See
//     docs/spec/controller/deploy.md §The render-what's-ready policy.
//
// Zero-knowledge custody is preserved end-to-end: GenerateKeys runs in AgentHeld
// mode, which emits PrivateKeyPlaceholder for each node's own key and never a real
// private key; the registry holds public keys only. The enrolled node's
// WireGuardPublicKey is set from the registry value (and any stray private key on
// the topology node is cleared) before rendering.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/artifacts"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
)

// StageResult reports the outcome of CompileAndStage. Staged and SkippedUnenrolled
// are NODE IDs (the registry/agent identity), not node names. Generation is the
// staged generation (CurrentGeneration+1); it becomes current only when the
// operator calls Store.PromoteStaged.
type StageResult struct {
	// Staged holds the node IDs that were compiled and staged this generation.
	Staged []string
	// SkippedUnenrolled holds the node IDs present in the topology but excluded
	// from the render because they are not yet enrolled (not NodeApproved, or no
	// WGPublicKey). Each fills in on a later deploy once it enrolls.
	SkippedUnenrolled []string
	// Generation is the staged generation. Zero when nothing was staged.
	Generation int64
}

// CompileAndStage renders the enrolled subgraph of the stored topology into signed
// per-node bundles and stages them at the next generation.
//
// The flow:
//
//  1. Load the stored topology (ErrNotFound → empty result, no error).
//  2. Build the enrolled subgraph: include a topology node iff its registry record
//     is NodeApproved with a non-empty WGPublicKey, set its WireGuardPublicKey from
//     the registry value (clearing any private key), and drop every edge with an
//     endpoint outside the enrolled set. Zero enrolled → empty result, no error.
//  3. GenerateKeys(AgentHeld) → Compile → render.All on the subgraph.
//  4. Export to a temp dir (removed on return).
//  5. Read each enrolled node's exported dir back into a file map and StageBundle it
//     at CurrentGeneration+1.
//  6. Append one "stage" audit entry.
//
// Bundles are signed iff YAOG_BUNDLE_SIGNING_KEY is set — that signing happens
// inside artifacts.Export (the Phase-0 env path), not here.
func CompileAndStage(ctx context.Context, store Store, t TenantID, now time.Time) (StageResult, error) {
	// (1) Load the stored, public-keys-only topology. No stored topology is a
	// benign no-op: there is nothing to stage yet.
	rec, err := store.GetTopology(ctx, t)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return StageResult{}, nil
		}
		return StageResult{}, fmt.Errorf("controller: loading topology to stage: %w", err)
	}
	var topo model.Topology
	if err := json.Unmarshal(rec.JSON, &topo); err != nil {
		return StageResult{}, fmt.Errorf("controller: parsing stored topology: %w", err)
	}

	// (2) Build the enrolled subgraph from the registry.
	nodes, err := store.ListNodes(ctx, t)
	if err != nil {
		return StageResult{}, fmt.Errorf("controller: listing nodes to stage: %w", err)
	}
	subgraph, skipped := enrolledSubgraph(&topo, nodes)

	// Nothing enrolled → nothing to render or stage. Report the skips so the caller
	// can surface "no node has enrolled yet".
	if len(subgraph.Nodes) == 0 {
		return StageResult{SkippedUnenrolled: skipped}, nil
	}

	// (3) Drive the frozen pipeline: AgentHeld keys (zero-knowledge), compile, render.
	keys, err := render.GenerateKeys(&subgraph, render.AgentHeld)
	if err != nil {
		return StageResult{}, fmt.Errorf("controller: preparing keys for stage: %w", err)
	}
	result, err := compiler.NewCompiler().Compile(&subgraph, keys)
	if err != nil {
		return StageResult{}, fmt.Errorf("controller: compiling enrolled subgraph: %w", err)
	}
	if err := render.All(result, keys); err != nil {
		return StageResult{}, fmt.Errorf("controller: rendering enrolled subgraph: %w", err)
	}

	// Persist the compiled allocation pins (overlay IPs, transit pairs, link-locals,
	// ports) back into the FULL stored topology, so a later re-compile sticky-pins them.
	// This is what makes the render-what's-ready model satisfy invariant I10: once a
	// node/edge has been staged at an allocation, enrolling another node never renumbers
	// it (the compiler reuses the pins it finds in the stored topology and only allocates
	// genuinely new entities).
	if err := persistAllocations(ctx, store, t, &topo, result.Topology); err != nil {
		return StageResult{}, err
	}

	// (4) Export to a temp dir we own and remove on return. Export writes one dir per
	// node (keyed by node.Name) and, when YAOG_BUNDLE_SIGNING_KEY is set, the
	// bundle.sig + signing-pubkey.pem inside each.
	tmp, err := os.MkdirTemp("", "yaog-stage-")
	if err != nil {
		return StageResult{}, fmt.Errorf("controller: creating stage temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)
	if _, err := artifacts.Export(result, tmp); err != nil {
		return StageResult{}, fmt.Errorf("controller: exporting bundles to stage: %w", err)
	}

	// (5) Read each enrolled node's exported dir back into a file map and stage it at
	// the next generation. The exported dir is named by node.Name, but StageBundle
	// keys by node.ID; each subgraph node carries both, so we read tmp/<Name>/ and
	// stage under its ID.
	cur, err := store.CurrentGeneration(ctx, t)
	if err != nil {
		return StageResult{}, fmt.Errorf("controller: reading current generation: %w", err)
	}
	nextGen := cur + 1

	var staged []string
	for _, node := range subgraph.Nodes {
		nodeDir := filepath.Join(tmp, node.Name)
		files, err := readBundleDir(nodeDir)
		if err != nil {
			return StageResult{}, fmt.Errorf("controller: reading bundle for node %s: %w", node.ID, err)
		}
		if err := store.StageBundle(ctx, t, SignedBundle{
			NodeID:     node.ID,
			Generation: nextGen,
			Files:      files,
			IsStaged:   true,
		}); err != nil {
			return StageResult{}, fmt.Errorf("controller: staging bundle for node %s: %w", node.ID, err)
		}
		staged = append(staged, node.ID)
	}

	// (6) One audit entry for the whole stage operation. The actor is the operator
	// (staging is an operator-driven step); NodeID is empty because the entry covers
	// the fleet-wide stage, not a single node.
	if _, err := store.AppendAudit(ctx, t, AuditEntry{
		Timestamp: now,
		Actor:     "operator",
		Action:    "stage",
		NodeID:    "",
	}); err != nil {
		return StageResult{}, fmt.Errorf("controller: appending stage audit: %w", err)
	}

	return StageResult{
		Staged:            staged,
		SkippedUnenrolled: skipped,
		Generation:        nextGen,
	}, nil
}

// enrolledSubgraph projects a stored topology down to its enrolled subgraph under
// the render-what's-ready policy.
//
// A topology node is included iff the registry holds a record for it that is
// NodeApproved with a non-empty WGPublicKey. On every included node it stamps
// WireGuardPublicKey from the registry value (authoritative: the agent holds the
// matching private key) and clears WireGuardPrivateKey — zero-knowledge custody
// means a stray private key from an imported topology must never reach a rendered
// bundle. Any edge whose FromNodeID or ToNodeID is outside the enrolled set is
// dropped; that edge activates on a later deploy once its far end enrolls.
//
// It returns the subgraph plus the list of excluded topology node IDs (skipped).
// The input topology is never mutated (nodes are projected by value copy).
func enrolledSubgraph(topo *model.Topology, nodes []Node) (model.Topology, []string) {
	// registry indexes the enrolled public key by node ID. A node is enrolled iff it
	// is NodeApproved with a non-empty WGPublicKey — the admission test.
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

	// First pass: the set of nodes whose key material is enrolled.
	enrolled := make(map[string]bool, len(topo.Nodes))
	for _, node := range topo.Nodes {
		if _, ok := registry[node.ID]; ok {
			enrolled[node.ID] = true
		}
	}

	// Render-what's-ready for the client role. A client requires EXACTLY ONE enabled
	// outbound edge (compiler validateClientEdges is a HARD error otherwise), so an
	// enrolled client whose dial target is not yet enrolled would be left edgeless and
	// fail the whole stage. Treat such a client as itself not-ready: exclude it now and
	// let it activate on a later deploy once its router/relay/gateway enrolls. (Clients
	// are leaves — excluding one cannot strand another — so a single pass suffices.)
	for _, node := range topo.Nodes {
		if enrolled[node.ID] && node.Role == "client" && !clientTargetEnrolled(topo, node.ID, enrolled) {
			delete(enrolled, node.ID)
		}
	}

	var skipped []string
	for _, node := range topo.Nodes { // value copy: never mutate the caller's slice
		if !enrolled[node.ID] {
			skipped = append(skipped, node.ID)
			continue
		}
		node.WireGuardPublicKey = registry[node.ID]
		node.WireGuardPrivateKey = ""
		sub.Nodes = append(sub.Nodes, node)
	}

	// Drop any edge whose far end is not enrolled: it activates on a later deploy.
	for _, edge := range topo.Edges {
		if enrolled[edge.FromNodeID] && enrolled[edge.ToNodeID] {
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
func persistAllocations(ctx context.Context, store Store, t TenantID, full, compiled *model.Topology) error {
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
	if _, err := store.PutTopology(ctx, t, raw); err != nil {
		return fmt.Errorf("controller: persisting allocations: %w", err)
	}
	return nil
}

// readBundleDir walks an exported node directory and returns its files keyed by
// bundle-relative slash path (e.g. "install.sh", "wireguard/wg-alpha.conf"). It
// skips directories and normalizes separators with filepath.ToSlash so the bundle
// keys are platform-independent — the same keys the agent expects regardless of the
// controller's OS.
func readBundleDir(nodeDir string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	err := filepath.Walk(nodeDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(nodeDir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
