package compiler

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// This file is the I1/I2 property gate for Plan 7 (sticky pin allocation, the
// incremental-growth feature), covering the core invariants of
// docs/spec/compiler/allocation-stability.md:
//   - I1 superset stability: recompiling a superset topology reproduces byte-for-byte
//     identical allocation values for every pre-existing edge.
//   - I2 order independence: allocation values do not depend on the array position of
//     nodes/edges.
//   - I9 delete reclamation + G1 gap-fill idempotency: deleting and then re-adding the
//     same node pair as a brand-new edge reproduces the same transit pair via gap-fill
//     (sort by pinKey + take the lowest free slot).
//   - I7 verbatim pin honoring: an operator's hand-pinned (valid, in-pool, non-colliding)
//     pin compiles to exactly the same values.
//   - I10 + backward compatibility: a v1.2.0-shaped topology (no pins, no
//     alloc_schema_version) compiles cleanly, with the result carrying pins and
//     AllocSchemaVersion=1.

// stableRouterNode constructs a publicly reachable router node with its overlay IP and
// base port already filled in, ready to feed directly into Compile from this file's
// property tests (the IP allocator preserves an already-set overlay IP).
func stableRouterNode(id, name, overlayIP string) model.Node {
	return model.Node{
		ID:        id,
		Name:      name,
		Hostname:  name + ".example.com",
		Role:      "router",
		DomainID:  "domain-1",
		OverlayIP: overlayIP,
		Capabilities: model.NodeCapabilities{
			CanAcceptInbound: true,
			CanForward:       true,
			HasPublicIP:      true,
		},
		PublicEndpoints: []model.PublicEndpoint{
			{ID: id + "-ep", Host: name + ".example.com", Port: 51820},
		},
	}
}

// stableDomain is the single-domain definition shared by these tests (overlay
// 10.50.0.0/24, transit left empty -> defaults to 10.10.0.0/24).
func stableDomain() model.Domain {
	return model.Domain{
		ID: "domain-1", Name: "stable", CIDR: "10.50.0.0/24",
		AllocationMode: "auto", RoutingMode: "babel",
	}
}

// stableKeys provides fixed keys for the three nodes a/b/c, avoiding any key-generation
// path (key persistence lives in another partition; this file only cares about the
// stability of port/transit/link-local allocation).
func stableKeys() map[string]KeyPair {
	return map[string]KeyPair{
		"node-a": {PrivateKey: "priv-a-fake", PublicKey: "pub-a-fake"},
		"node-b": {PrivateKey: "priv-b-fake", PublicKey: "pub-b-fake"},
		"node-c": {PrivateKey: "priv-c-fake", PublicKey: "pub-c-fake"},
	}
}

// abPins captures all of an edge's allocation outputs, for byte-for-byte comparison
// across compiles.
type abPins struct {
	fromPort      int
	toPort        int
	fromTransitIP string
	toTransitIP   string
	fromLinkLocal string
	toLinkLocal   string
	compiledPort  int
}

// capturePins pulls all allocation values for a given edge id out of the compiled
// topology.
func capturePins(t *testing.T, topo *model.Topology, edgeID string) abPins {
	t.Helper()
	edge := findEdge(topo.Edges, edgeID)
	if edge == nil {
		t.Fatalf("edge %q not found in compiled topology", edgeID)
	}
	return abPins{
		fromPort:      edge.PinnedFromPort,
		toPort:        edge.PinnedToPort,
		fromTransitIP: edge.PinnedFromTransitIP,
		toTransitIP:   edge.PinnedToTransitIP,
		fromLinkLocal: edge.PinnedFromLinkLocal,
		toLinkLocal:   edge.PinnedToLinkLocal,
		compiledPort:  edge.CompiledPort,
	}
}

// assertPinsEqual asserts two captured allocation snapshots are field-for-field equal
// (the proxy criterion for "a byte-for-byte identical bundle").
func assertPinsEqual(t *testing.T, label string, want, got abPins) {
	t.Helper()
	if want.fromPort != got.fromPort {
		t.Errorf("%s: from port should be unchanged (%d), got %d", label, want.fromPort, got.fromPort)
	}
	if want.toPort != got.toPort {
		t.Errorf("%s: to port should be unchanged (%d), got %d", label, want.toPort, got.toPort)
	}
	if want.fromTransitIP != got.fromTransitIP {
		t.Errorf("%s: from transit IP should be unchanged (%s), got %s", label, want.fromTransitIP, got.fromTransitIP)
	}
	if want.toTransitIP != got.toTransitIP {
		t.Errorf("%s: to transit IP should be unchanged (%s), got %s", label, want.toTransitIP, got.toTransitIP)
	}
	if want.fromLinkLocal != got.fromLinkLocal {
		t.Errorf("%s: from link-local should be unchanged (%s), got %s", label, want.fromLinkLocal, got.fromLinkLocal)
	}
	if want.toLinkLocal != got.toLinkLocal {
		t.Errorf("%s: to link-local should be unchanged (%s), got %s", label, want.toLinkLocal, got.toLinkLocal)
	}
	if want.compiledPort != got.compiledPort {
		t.Errorf("%s: CompiledPort should be unchanged (%d), got %d", label, want.compiledPort, got.compiledPort)
	}
}

// applyPins writes captured pins back onto an edge, simulating re-submitting for
// compilation after a frontend persistence round-trip.
func applyPins(edge *model.Edge, p abPins) {
	edge.PinnedFromPort = p.fromPort
	edge.PinnedToPort = p.toPort
	edge.PinnedFromTransitIP = p.fromTransitIP
	edge.PinnedToTransitIP = p.toTransitIP
	edge.PinnedFromLinkLocal = p.fromLinkLocal
	edge.PinnedToLinkLocal = p.toLinkLocal
}

// abEdge constructs an A->B direct edge (with endpoint_host, so CompiledPort
// participates in the comparison too).
func abEdge(id, from, to, endpointHost string) model.Edge {
	return model.Edge{
		ID: id, FromNodeID: from, ToNodeID: to,
		Type: "direct", EndpointHost: endpointHost, EndpointPort: 0,
		Transport: "udp", IsEnabled: true,
	}
}

// TestSupersetCompileReproducesAllocations is the primary gate for I1 (superset
// stability) + I2 (order independence).
//
//	topo1 = [A,B] + A-B                          ->  capture all of A-B's allocation values
//	topo2 = [A,B,C] + A-B(pinned) + A-C(appended)  ->  A-B must be byte-for-byte identical
//	topo3 = [A,B,C] + A-C(prepended) + A-B(pinned) ->  A-B still byte-for-byte identical (order independent)
//
// topo3's "prepend" is the key case: under the old position-counter implementation,
// ordering A-C before A-B would change A-B's port/transit values and thereby violate I2.
// reserve-then-gap-fill makes it hold by construction.
func TestSupersetCompileReproducesAllocations(t *testing.T) {
	c := NewCompiler()
	keys := stableKeys()

	// ---- compile 1: [A,B] + A-B ----
	topo1 := &model.Topology{
		Project: model.Project{ID: "stable-001", Name: "Superset Stability"},
		Domains: []model.Domain{stableDomain()},
		Nodes: []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
		},
		Edges: []model.Edge{
			abEdge("e-ab", "node-a", "node-b", "beta.example.com"),
		},
	}
	res1, err := c.Compile(topo1, keys)
	if err != nil {
		t.Fatalf("compile 1 failed: %v", err)
	}
	base := capturePins(t, res1.Topology, "e-ab")
	// sanity: all of A-B's allocation values should have been written back (port,
	// transit, link-local non-empty).
	if base.fromPort == 0 || base.toPort == 0 || base.fromTransitIP == "" ||
		base.toTransitIP == "" || base.fromLinkLocal == "" || base.toLinkLocal == "" {
		t.Fatalf("compile 1 should write back all of A-B's allocation as pins, got: %+v", base)
	}

	// Pull the pins written back by compile 1, to carry as A-B's pins in later topologies.
	pinnedAB := abEdge("e-ab", "node-a", "node-b", "beta.example.com")
	applyPins(&pinnedAB, base)

	// ---- compile 2: [A,B,C] + A-B(pinned) + A-C(appended, no pin) ----
	topo2 := &model.Topology{
		Project: model.Project{ID: "stable-001", Name: "Superset Stability"},
		Domains: []model.Domain{stableDomain()},
		Nodes: []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
			stableRouterNode("node-c", "gamma", "10.50.0.3"),
		},
		Edges: []model.Edge{
			pinnedAB,
			abEdge("e-ac", "node-a", "node-c", "gamma.example.com"),
		},
	}
	res2, err := c.Compile(topo2, keys)
	if err != nil {
		t.Fatalf("compile 2 failed: %v", err)
	}
	got2 := capturePins(t, res2.Topology, "e-ab")
	assertPinsEqual(t, "I1 A-B after appending C", base, got2)

	// ---- compile 3: [A,B,C] + A-C(prepended, no pin) + A-B(pinned) ----
	// The new edge is ordered before A-B: under the old position counter this would shift
	// A-B's values; here it must stay unchanged.
	topo3 := &model.Topology{
		Project: model.Project{ID: "stable-001", Name: "Superset Stability"},
		Domains: []model.Domain{stableDomain()},
		Nodes: []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
			stableRouterNode("node-c", "gamma", "10.50.0.3"),
		},
		Edges: []model.Edge{
			abEdge("e-ac", "node-a", "node-c", "gamma.example.com"),
			pinnedAB,
		},
	}
	res3, err := c.Compile(topo3, keys)
	if err != nil {
		t.Fatalf("compile 3 failed: %v", err)
	}
	got3 := capturePins(t, res3.Topology, "e-ab")
	assertPinsEqual(t, "I2 A-B after prepending A-C", base, got3)
}

// TestDeleteReAddReclaimsValues is the gate for I9 (delete reclamation) + G1 (gap-fill
// idempotency).
//
//	compile 1: [A,B,C] + A-B + A-C                           ->  capture A-C's transit pair
//	compile 2: [A,B,C] + A-B(pinned)                         ->  delete A-C, its slot is freed
//	compile 3: [A,B,C] + A-B(pinned) + A-C(brand-new id, no pin) ->  A-C should reproduce the same transit pair
//
// Why the reproduction holds: gap-fill iterates sorted by pinKey and takes the lowest
// free slot in the pool, independent of A-C's delete/re-add history and array position;
// the pre-existing A-B is always reserved first (pins honored verbatim), so A-C sees the
// same reserved set in both compiles and therefore takes the same lowest free pair.
func TestDeleteReAddReclaimsValues(t *testing.T) {
	c := NewCompiler()
	keys := stableKeys()

	nodes := func() []model.Node {
		return []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
			stableRouterNode("node-c", "gamma", "10.50.0.3"),
		}
	}

	// ---- compile 1: A-B + A-C both with no pin (first gap-fill) ----
	topo1 := &model.Topology{
		Project: model.Project{ID: "stable-002", Name: "Delete ReAdd"},
		Domains: []model.Domain{stableDomain()},
		Nodes:   nodes(),
		Edges: []model.Edge{
			abEdge("e-ab", "node-a", "node-b", "beta.example.com"),
			abEdge("e-ac", "node-a", "node-c", "gamma.example.com"),
		},
	}
	res1, err := c.Compile(topo1, keys)
	if err != nil {
		t.Fatalf("compile 1 failed: %v", err)
	}
	abBase := capturePins(t, res1.Topology, "e-ab")
	acBase := capturePins(t, res1.Topology, "e-ac")
	if acBase.fromTransitIP == "" || acBase.toTransitIP == "" {
		t.Fatalf("compile 1 should allocate a transit pair for A-C, got: %+v", acBase)
	}

	// A-B carries compile 1's pins into the later compiles (the pre-existing link is reserved first).
	pinnedAB := abEdge("e-ab", "node-a", "node-b", "beta.example.com")
	applyPins(&pinnedAB, abBase)

	// ---- compile 2: delete A-C, keep only the pinned A-B (A-C's slot is freed) ----
	topo2 := &model.Topology{
		Project: model.Project{ID: "stable-002", Name: "Delete ReAdd"},
		Domains: []model.Domain{stableDomain()},
		Nodes:   nodes(),
		Edges: []model.Edge{
			pinnedAB,
		},
	}
	if _, err := c.Compile(topo2, keys); err != nil {
		t.Fatalf("compile 2 (delete A-C) failed: %v", err)
	}

	// ---- compile 3: re-add A-C with a brand-new id and no pin ----
	topo3 := &model.Topology{
		Project: model.Project{ID: "stable-002", Name: "Delete ReAdd"},
		Domains: []model.Domain{stableDomain()},
		Nodes:   nodes(),
		Edges: []model.Edge{
			pinnedAB,
			abEdge("e-ac-readded", "node-a", "node-c", "gamma.example.com"),
		},
	}
	res3, err := c.Compile(topo3, keys)
	if err != nil {
		t.Fatalf("compile 3 (re-add A-C) failed: %v", err)
	}
	acReadded := capturePins(t, res3.Topology, "e-ac-readded")

	if acReadded.fromTransitIP != acBase.fromTransitIP || acReadded.toTransitIP != acBase.toTransitIP {
		t.Errorf("deleting then re-adding A-C should reproduce the same transit pair: original {%s, %s}, after re-add {%s, %s}",
			acBase.fromTransitIP, acBase.toTransitIP, acReadded.fromTransitIP, acReadded.toTransitIP)
	}
	// link-local should likewise be reproduced by hash seeding.
	if acReadded.fromLinkLocal != acBase.fromLinkLocal || acReadded.toLinkLocal != acBase.toLinkLocal {
		t.Errorf("deleting then re-adding A-C should reproduce the same link-local pair: original {%s, %s}, after re-add {%s, %s}",
			acBase.fromLinkLocal, acBase.toLinkLocal, acReadded.fromLinkLocal, acReadded.toLinkLocal)
	}
}

// TestPinnedValuesHonoredVerbatim is the gate for I7: an operator's hand-pinned (valid,
// in-pool, non-colliding) pins are honored verbatim after compilation; the compiler never
// renumbers them.
func TestPinnedValuesHonoredVerbatim(t *testing.T) {
	c := NewCompiler()
	keys := stableKeys()

	// Hand-pin a set of valid pins: port >= base(51820), transit inside the default pool
	// 10.10.0.0/24 and not the network/broadcast address, link-local valid hex.
	edge := abEdge("e-ab", "node-a", "node-b", "beta.example.com")
	edge.PinnedFromPort = 51830
	edge.PinnedToPort = 51831
	edge.PinnedFromTransitIP = "10.10.0.51"
	edge.PinnedToTransitIP = "10.10.0.52"
	edge.PinnedFromLinkLocal = "fe80::aa"
	edge.PinnedToLinkLocal = "fe80::ab"

	topo := &model.Topology{
		Project: model.Project{ID: "stable-003", Name: "Pins Verbatim"},
		Domains: []model.Domain{stableDomain()},
		Nodes: []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
		},
		Edges: []model.Edge{edge},
	}

	res, err := c.Compile(topo, keys)
	if err != nil {
		t.Fatalf("a topology with valid pins should compile, got error: %v", err)
	}
	got := capturePins(t, res.Topology, "e-ab")

	if got.fromPort != 51830 || got.toPort != 51831 {
		t.Errorf("port pins should be honored verbatim {51830, 51831}, got {%d, %d}", got.fromPort, got.toPort)
	}
	if got.fromTransitIP != "10.10.0.51" || got.toTransitIP != "10.10.0.52" {
		t.Errorf("transit pins should be honored verbatim {10.10.0.51, 10.10.0.52}, got {%s, %s}", got.fromTransitIP, got.toTransitIP)
	}
	if got.fromLinkLocal != "fe80::aa" || got.toLinkLocal != "fe80::ab" {
		t.Errorf("link-local pins should be honored verbatim {fe80::aa, fe80::ab}, got {%s, %s}", got.fromLinkLocal, got.toLinkLocal)
	}
	// CompiledPort should equal the remote (toNode) interface's allocated listen port = PinnedToPort.
	if got.compiledPort != 51831 {
		t.Errorf("CompiledPort should equal the remote interface port 51831, got %d", got.compiledPort)
	}
}

// backupEdge constructs an A->B direct edge with Role=backup (with endpoint_host, so
// CompiledPort participates in the comparison too). Identical to abEdge except for the
// extra Role field — a backup edge becomes its own independent link in terms of link
// identity (linkKey = pinKey + "#" + edge.ID), so it gets an independent allocation
// distinct from the primary-class link.
func backupEdge(id, from, to, endpointHost string) model.Edge {
	e := abEdge(id, from, to, endpointHost)
	e.Role = model.EdgeRoleBackup
	return e
}

// pinsNonEmpty reports whether a captured allocation set is "all non-empty" — all three
// resource classes (port, transit, link-local) have been allocated. Used to assert a
// backup link actually got a complete, independent allocation.
func pinsNonEmpty(p abPins) bool {
	return p.fromPort != 0 && p.toPort != 0 &&
		p.fromTransitIP != "" && p.toTransitIP != "" &&
		p.fromLinkLocal != "" && p.toLinkLocal != ""
}

// pinsDisjoint reports whether two allocation sets differ in every resource class —
// primary and backup are two different links (different linkKey), so their ports (on the
// same node), transit IP pairs, and link-local pairs must all be non-overlapping.
func pinsDisjoint(a, b abPins) bool {
	return a.fromPort != b.fromPort && a.toPort != b.toPort &&
		a.fromTransitIP != b.fromTransitIP && a.toTransitIP != b.toTransitIP &&
		a.fromLinkLocal != b.fromLinkLocal && a.toLinkLocal != b.toLinkLocal
}

// TestParallelBackup_PrimaryStableBackupDistinct is the primary gate for parallel-link
// stability, covering stability property 1 (single-edge reduction) and property 3
// (identity never migrates on growth) from "Link identity with parallel edges" in
// docs/spec/compiler/allocation-stability.md:
//
//	compile 1: [A,B] + A-B (primary class)             ->  capture all of primary's allocation values
//	compile 2: [A,B] + A-B(pinned) + A-B-backup(new id)  ->  primary must be byte-for-byte identical,
//	                                                         backup gets a complete allocation disjoint from primary
//	compile 3: delete backup, keep only the pinned primary ->  primary still byte-for-byte identical
//
// Appending a backup never changes the pre-existing primary link's linkKey, interface
// name, or allocation values (property 3): a backup is always distinguished by its own
// edge.ID, so the values primary got in compile 1 stay fixed in compile 2/3.
func TestParallelBackup_PrimaryStableBackupDistinct(t *testing.T) {
	c := NewCompiler()
	keys := stableKeys()

	// ---- compile 1: [A,B] + A-B (a single primary-class edge) ----
	topo1 := &model.Topology{
		Project: model.Project{ID: "parallel-001", Name: "Parallel Stability"},
		Domains: []model.Domain{stableDomain()},
		Nodes: []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
		},
		Edges: []model.Edge{
			abEdge("e-ab", "node-a", "node-b", "beta.example.com"),
		},
	}
	res1, err := c.Compile(topo1, keys)
	if err != nil {
		t.Fatalf("compile 1 failed: %v", err)
	}
	primaryBase := capturePins(t, res1.Topology, "e-ab")
	if !pinsNonEmpty(primaryBase) {
		t.Fatalf("compile 1 should write back all of the primary link's allocation as pins, got: %+v", primaryBase)
	}

	// primary carries the pins written back by compile 1 into the later compiles (the pre-existing link is reserved first and honored verbatim).
	pinnedPrimary := abEdge("e-ab", "node-a", "node-b", "beta.example.com")
	applyPins(&pinnedPrimary, primaryBase)

	// ---- compile 2: append a backup (brand-new id, no pin) to the same node pair ----
	topo2 := &model.Topology{
		Project: model.Project{ID: "parallel-001", Name: "Parallel Stability"},
		Domains: []model.Domain{stableDomain()},
		Nodes: []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
		},
		Edges: []model.Edge{
			pinnedPrimary,
			backupEdge("e-ab-backup", "node-a", "node-b", "beta.example.com"),
		},
	}
	res2, err := c.Compile(topo2, keys)
	if err != nil {
		t.Fatalf("compile 2 (append backup) failed: %v", err)
	}

	// Property 3: after appending a backup, primary's six pinned_* + CompiledPort are byte-for-byte unchanged.
	gotPrimary2 := capturePins(t, res2.Topology, "e-ab")
	assertPinsEqual(t, "primary link after appending backup", primaryBase, gotPrimary2)

	// backup must get a complete, non-empty allocation disjoint from primary in every resource class.
	gotBackup := capturePins(t, res2.Topology, "e-ab-backup")
	if !pinsNonEmpty(gotBackup) {
		t.Errorf("backup link should get a complete independent allocation, got: %+v", gotBackup)
	}
	if !pinsDisjoint(primaryBase, gotBackup) {
		t.Errorf("backup and primary are two different links; allocation values should be disjoint in every resource class.\nprimary: %+v\nbackup:  %+v", primaryBase, gotBackup)
	}

	// ---- compile 3: delete the backup, primary's values must stay unchanged ----
	topo3 := &model.Topology{
		Project: model.Project{ID: "parallel-001", Name: "Parallel Stability"},
		Domains: []model.Domain{stableDomain()},
		Nodes: []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
		},
		Edges: []model.Edge{
			pinnedPrimary,
		},
	}
	res3, err := c.Compile(topo3, keys)
	if err != nil {
		t.Fatalf("compile 3 (delete backup) failed: %v", err)
	}
	gotPrimary3 := capturePins(t, res3.Topology, "e-ab")
	assertPinsEqual(t, "primary link after deleting backup", primaryBase, gotPrimary3)
}

// TestParallelBackup_OrderIndependence covers the stability property "I2 with a parallel
// pair present": in a topology that already has a parallel link pair (primary + backup),
// appending vs prepending the backup in topo.Edges must leave every other unrelated link's
// (here A-C's) allocation values byte-for-byte identical — a backup is positional only in
// its own resources and never affects others (property 5's "backups are positional only in
// their own resources").
//
// Note: this test does not assert equality of the backup's own values across the two
// compiles (property 5 explicitly accepts that a backup's delete/re-add is not idempotent,
// and here the backup's edge.ID is the same in both topologies but at a different array
// position). The gate locks down "other links are not perturbed by the backup's position".
func TestParallelBackup_OrderIndependence(t *testing.T) {
	c := NewCompiler()
	keys := stableKeys()

	nodes := func() []model.Node {
		return []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
			stableRouterNode("node-c", "gamma", "10.50.0.3"),
		}
	}

	primary := abEdge("e-ab", "node-a", "node-b", "beta.example.com")
	backup := backupEdge("e-ab-backup", "node-a", "node-b", "beta.example.com")
	other := abEdge("e-ac", "node-a", "node-c", "gamma.example.com")

	// ---- arrangement 1: backup appended after A-C ----
	topoAppend := &model.Topology{
		Project: model.Project{ID: "parallel-002", Name: "Parallel Order"},
		Domains: []model.Domain{stableDomain()},
		Nodes:   nodes(),
		Edges:   []model.Edge{primary, other, backup},
	}
	resAppend, err := c.Compile(topoAppend, keys)
	if err != nil {
		t.Fatalf("compile (backup appended) failed: %v", err)
	}
	acAppend := capturePins(t, resAppend.Topology, "e-ac")
	if !pinsNonEmpty(acAppend) {
		t.Fatalf("A-C should get a complete allocation, got: %+v", acAppend)
	}

	// ---- arrangement 2: backup prepended before all edges ----
	topoPrepend := &model.Topology{
		Project: model.Project{ID: "parallel-002", Name: "Parallel Order"},
		Domains: []model.Domain{stableDomain()},
		Nodes:   nodes(),
		Edges:   []model.Edge{backup, primary, other},
	}
	resPrepend, err := c.Compile(topoPrepend, keys)
	if err != nil {
		t.Fatalf("compile (backup prepended) failed: %v", err)
	}
	acPrepend := capturePins(t, resPrepend.Topology, "e-ac")

	// Unrelated link A-C: changing the backup's array position must not change its allocation values (I2).
	assertPinsEqual(t, "unrelated link A-C under backup prepend/append", acAppend, acPrepend)

	// primary is likewise unaffected by the backup's position (it is primary class, linkKey == pinKey).
	primAppend := capturePins(t, resAppend.Topology, "e-ab")
	primPrepend := capturePins(t, resPrepend.Topology, "e-ab")
	assertPinsEqual(t, "primary link A-B under backup prepend/append", primAppend, primPrepend)
}

// TestPrePinTopologyCompiles is the gate for I10 + backward compatibility: a v1.2.0-shaped
// topology (no pin fields at all, no alloc_schema_version) should compile cleanly, with the
// result carrying written-back pins and AllocSchemaVersion=1.
func TestPrePinTopologyCompiles(t *testing.T) {
	c := NewCompiler()
	keys := stableKeys()

	topo := &model.Topology{
		Project: model.Project{ID: "stable-004", Name: "Pre-Pin BackCompat"},
		Domains: []model.Domain{stableDomain()},
		Nodes: []model.Node{
			stableRouterNode("node-a", "alpha", "10.50.0.1"),
			stableRouterNode("node-b", "beta", "10.50.0.2"),
		},
		// Note: deliberately set no pinned_* fields at all, and no AllocSchemaVersion.
		Edges: []model.Edge{
			abEdge("e-ab", "node-a", "node-b", "beta.example.com"),
		},
	}
	// Explicitly confirm the input is in pre-pin shape.
	if topo.AllocSchemaVersion != 0 {
		t.Fatalf("precondition: input topology's AllocSchemaVersion should be 0 (pre-pin shape)")
	}

	res, err := c.Compile(topo, keys)
	if err != nil {
		t.Fatalf("a pre-pin topology should compile cleanly, got error: %v", err)
	}

	// The result should be stamped with the current schema version (I10).
	if res.Topology.AllocSchemaVersion != AllocationSchemaVersion {
		t.Errorf("compile result's AllocSchemaVersion should be %d, got %d",
			AllocationSchemaVersion, res.Topology.AllocSchemaVersion)
	}
	if AllocationSchemaVersion != 1 {
		t.Errorf("AllocationSchemaVersion constant should be 1, got %d", AllocationSchemaVersion)
	}

	// The result should write allocation values back as pins (for the next compile to reuse).
	got := capturePins(t, res.Topology, "e-ab")
	if got.fromPort == 0 || got.toPort == 0 {
		t.Errorf("after compile A-B should write back port pins, got {%d, %d}", got.fromPort, got.toPort)
	}
	if got.fromTransitIP == "" || got.toTransitIP == "" {
		t.Errorf("after compile A-B should write back transit pins, got {%q, %q}", got.fromTransitIP, got.toTransitIP)
	}
	if got.fromLinkLocal == "" || got.toLinkLocal == "" {
		t.Errorf("after compile A-B should write back link-local pins, got {%q, %q}", got.fromLinkLocal, got.toLinkLocal)
	}
}
