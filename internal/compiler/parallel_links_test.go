package compiler

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/naming"
)

// This file is the gate for parallel links (primary + backup failover) in the compiler
// partition, covering the contracts of docs/spec/compiler/allocation-stability.md (Link
// identity with parallel edges) and docs/spec/artifacts/{naming.md,babel.md}:
//   - a node pair's primary + backup compile to two PeerInfo per side, with interface name /
//     listen port / transit IP all distinct;
//   - interface names come from the single naming authority internal/naming: primary ==
//     WgInterfaceName(remote), backup == WgInterfaceNameForEdge(remote, edgeID, true);
//   - a backup link with no priority/weight set has LinkCost == 384 (4x the babeld wired
//     default of 96);
//   - an explicit priority on the backup overrides 384;
//   - the legacy "role-less A->B + B->A" reverse pair still collapses into one link (exactly
//     one PeerInfo per side, interface name byte-for-byte identical to before the change).
//
// All these assertions go through the public surface (compiler.Compile, PeerInfo, model
// fields, the naming package) and do not touch internal implementation.

// findPeerByIface locates a PeerInfo within peers by exact InterfaceName match.
// With parallel links, the same NodeID yields multiple PeerInfo (primary and each backup),
// so lookup by NodeID is no longer valid — the interface name is the unique criterion for
// each link.
func findPeerByIface(peers []PeerInfo, iface string) *PeerInfo {
	for i := range peers {
		if peers[i].InterfaceName == iface {
			return &peers[i]
		}
	}
	return nil
}

// countPeersToRemote counts the number of PeerInfo in peers pointing at remoteID (= the
// number of interfaces this node has toward that remote).
func countPeersToRemote(peers []PeerInfo, remoteID string) int {
	n := 0
	for i := range peers {
		if peers[i].NodeID == remoteID {
			n++
		}
	}
	return n
}

// parallelPairTopology constructs a node pair A<->B plus one primary-class edge and one
// backup edge. backupID lets the caller hold onto the backup edge's ID to reconstruct its
// interface name; mutateBackup allows injecting extra config (e.g. setting Priority on the
// backup) to test cost override.
func parallelPairTopology(backupID string, mutateBackup func(*model.Edge)) *model.Topology {
	primary := abEdge("e-ab", "node-a", "node-b", "beta.example.com")
	backup := backupEdge(backupID, "node-a", "node-b", "beta.example.com")
	if mutateBackup != nil {
		mutateBackup(&backup)
	}
	return &model.Topology{
		Project: model.Project{ID: "parallel-links", Name: "Parallel Links"},
		Domains: []model.Domain{stableDomain()},
		Nodes: []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
		},
		Edges: []model.Edge{primary, backup},
	}
}

// TestParallelLinks_TwoDistinctInterfacesPerSide verifies that primary + backup produce two
// PeerInfo on each side, with interface name / listen port / transit IP all distinct; the
// interface names come from the single naming authority.
func TestParallelLinks_TwoDistinctInterfacesPerSide(t *testing.T) {
	c := NewCompiler()
	keys := stableKeys()

	const backupID = "e-ab-backup"
	topo := parallelPairTopology(backupID, nil)
	res, err := c.Compile(topo, keys)
	if err != nil {
		t.Fatalf("primary + backup topology should compile, got error: %v", err)
	}

	// Expected interface names (given by the single naming authority, verifying the compiler really derives them via the naming package).
	primaryIfaceOnA := naming.WgInterfaceName("beta")                        // A-side primary interface toward beta
	backupIfaceOnA := naming.WgInterfaceNameForEdge("beta", backupID, true)  // A-side backup interface toward beta
	primaryIfaceOnB := naming.WgInterfaceName("alpha")                       // B-side primary interface toward alpha
	backupIfaceOnB := naming.WgInterfaceNameForEdge("alpha", backupID, true) // B-side backup interface toward alpha

	// The primary and backup interface names must differ, otherwise the two links' configs would overwrite each other.
	if primaryIfaceOnA == backupIfaceOnA {
		t.Fatalf("A-side primary and backup interface names should not be equal: %q", primaryIfaceOnA)
	}

	// ---- A side: exactly two PeerInfo pointing at B ----
	aPeers := res.PeerMap["node-a"]
	if got := countPeersToRemote(aPeers, "node-b"); got != 2 {
		t.Fatalf("A side should have 2 PeerInfo pointing at B (primary + backup), got %d", got)
	}
	aPrimary := findPeerByIface(aPeers, primaryIfaceOnA)
	aBackup := findPeerByIface(aPeers, backupIfaceOnA)
	if aPrimary == nil {
		t.Fatalf("A side should have primary interface %q, got peers: %+v", primaryIfaceOnA, aPeers)
	}
	if aBackup == nil {
		t.Fatalf("A side should have backup interface %q, got peers: %+v", backupIfaceOnA, aPeers)
	}

	// ---- B side: likewise exactly two PeerInfo pointing at A ----
	bPeers := res.PeerMap["node-b"]
	if got := countPeersToRemote(bPeers, "node-a"); got != 2 {
		t.Fatalf("B side should have 2 PeerInfo pointing at A (primary + backup), got %d", got)
	}
	bPrimary := findPeerByIface(bPeers, primaryIfaceOnB)
	bBackup := findPeerByIface(bPeers, backupIfaceOnB)
	if bPrimary == nil {
		t.Fatalf("B side should have primary interface %q, got peers: %+v", primaryIfaceOnB, bPeers)
	}
	if bBackup == nil {
		t.Fatalf("B side should have backup interface %q, got peers: %+v", backupIfaceOnB, bPeers)
	}

	// ---- Listen ports distinct (two links on the same node must not contend for the same port) ----
	if aPrimary.ListenPort == aBackup.ListenPort {
		t.Errorf("A-side primary and backup listen ports should be distinct, both are %d", aPrimary.ListenPort)
	}
	if bPrimary.ListenPort == bBackup.ListenPort {
		t.Errorf("B-side primary and backup listen ports should be distinct, both are %d", bPrimary.ListenPort)
	}

	// ---- transit IPs distinct (the two links are two sets of point-to-point addresses) ----
	if aPrimary.LocalTransitIP == aBackup.LocalTransitIP {
		t.Errorf("A-side primary and backup local transit IPs should be distinct, both are %s", aPrimary.LocalTransitIP)
	}
	if aPrimary.RemoteTransitIP == aBackup.RemoteTransitIP {
		t.Errorf("A-side primary and backup remote transit IPs should be distinct, both are %s", aPrimary.RemoteTransitIP)
	}
}

// TestParallelLinks_BackupDefaultLinkCost verifies that a backup link with no priority/weight
// set has LinkCost == 384 (docs/spec/artifacts/babel.md "Link cost resolution" rule 2, the
// backup preset), while the same pair's primary link has LinkCost == 0 (uses babeld's built-in
// default, omitting rxcost at render time).
func TestParallelLinks_BackupDefaultLinkCost(t *testing.T) {
	c := NewCompiler()
	keys := stableKeys()

	const backupID = "e-ab-backup"
	topo := parallelPairTopology(backupID, nil)
	res, err := c.Compile(topo, keys)
	if err != nil {
		t.Fatalf("primary + backup topology should compile, got error: %v", err)
	}

	aPeers := res.PeerMap["node-a"]
	aPrimary := findPeerByIface(aPeers, naming.WgInterfaceName("beta"))
	aBackup := findPeerByIface(aPeers, naming.WgInterfaceNameForEdge("beta", backupID, true))
	if aPrimary == nil || aBackup == nil {
		t.Fatalf("should find both primary and backup PeerInfo, got peers: %+v", aPeers)
	}

	if aBackup.LinkCost != backupDefaultLinkCost {
		t.Errorf("a backup link with no priority/weight set should have LinkCost %d, got %d", backupDefaultLinkCost, aBackup.LinkCost)
	}
	if backupDefaultLinkCost != 384 {
		t.Errorf("backupDefaultLinkCost constant should be 384 (4x the babeld wired default of 96), got %d", backupDefaultLinkCost)
	}
	// primary (no explicit cost) should be 0, forming the cost gap needed for failover relative to backup's 384.
	if aPrimary.LinkCost != 0 {
		t.Errorf("a primary link with no priority/weight set should have LinkCost 0 (deferred to role preset/babeld default), got %d", aPrimary.LinkCost)
	}
}

// TestParallelLinks_ExplicitPriorityOverridesBackupDefault verifies that an explicit priority
// on the backup overrides the 384 backup preset (docs/spec/artifacts/babel.md "Link cost
// resolution" rule 1: an explicit operator setting has the highest priority).
func TestParallelLinks_ExplicitPriorityOverridesBackupDefault(t *testing.T) {
	c := NewCompiler()
	keys := stableKeys()

	const backupID = "e-ab-backup"
	const explicitCost = 250
	topo := parallelPairTopology(backupID, func(e *model.Edge) {
		e.Priority = explicitCost
	})
	res, err := c.Compile(topo, keys)
	if err != nil {
		t.Fatalf("a backup topology with explicit priority should compile, got error: %v", err)
	}

	aPeers := res.PeerMap["node-a"]
	aBackup := findPeerByIface(aPeers, naming.WgInterfaceNameForEdge("beta", backupID, true))
	if aBackup == nil {
		t.Fatalf("should find backup PeerInfo, got peers: %+v", aPeers)
	}

	if aBackup.LinkCost != explicitCost {
		t.Errorf("an explicit priority on the backup should override the 384 preset, want LinkCost == %d, got %d", explicitCost, aBackup.LinkCost)
	}
	if aBackup.LinkCost == backupDefaultLinkCost {
		t.Errorf("should not fall back to the backup preset %d when an explicit priority is present", backupDefaultLinkCost)
	}
}

// TestParallelLinks_LegacyReversePairOneLink verifies that the legacy "role-less A->B + B->A"
// reverse pair still collapses into one link (the unify rule preserves legacy semantics):
// exactly one PeerInfo per side, with the interface name byte-for-byte identical to before
// the change (== naming.WgInterfaceName(remote), never the backup's edge-aware form).
func TestParallelLinks_LegacyReversePairOneLink(t *testing.T) {
	c := NewCompiler()
	keys := stableKeys()

	topo := &model.Topology{
		Project: model.Project{ID: "legacy-pair", Name: "Legacy Reverse Pair"},
		Domains: []model.Domain{stableDomain()},
		Nodes: []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
		},
		// Role-less forward and reverse edges — both primary class, collapsed into one bidirectional tunnel.
		Edges: []model.Edge{
			abEdge("e-ab", "node-a", "node-b", "beta.example.com"),
			abEdge("e-ba", "node-b", "node-a", "alpha.example.com"),
		},
	}
	res, err := c.Compile(topo, keys)
	if err != nil {
		t.Fatalf("legacy reverse-pair topology should compile, got error: %v", err)
	}

	// A side has exactly one PeerInfo pointing at B (forward and reverse edges collapse into one link).
	aPeers := res.PeerMap["node-a"]
	if got := countPeersToRemote(aPeers, "node-b"); got != 1 {
		t.Fatalf("a legacy reverse pair should have exactly 1 PeerInfo pointing at B on the A side, got %d: %+v", got, aPeers)
	}
	// B side likewise has exactly one PeerInfo pointing at A.
	bPeers := res.PeerMap["node-b"]
	if got := countPeersToRemote(bPeers, "node-a"); got != 1 {
		t.Fatalf("a legacy reverse pair should have exactly 1 PeerInfo pointing at A on the B side, got %d: %+v", got, bPeers)
	}

	// Interface names must be byte-for-byte identical to before the change (primary class uses WgInterfaceName, without edge distinction).
	if aPeers[0].InterfaceName != naming.WgInterfaceName("beta") {
		t.Errorf("A-side primary class interface name should be %q, got %q", naming.WgInterfaceName("beta"), aPeers[0].InterfaceName)
	}
	if bPeers[0].InterfaceName != naming.WgInterfaceName("alpha") {
		t.Errorf("B-side primary class interface name should be %q, got %q", naming.WgInterfaceName("alpha"), bPeers[0].InterfaceName)
	}
}
