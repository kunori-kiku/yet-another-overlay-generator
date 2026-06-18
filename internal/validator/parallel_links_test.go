package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/naming"
)

// This file is the gate for parallel links (primary / backup failover) in the
// semantic/structural validation partition. It covers docs/spec/compiler/validation.md
// "Parallel links" and docs/spec/artifacts/{naming.md,babel.md}:
//   - role enum: only empty / "primary" / "backup" are allowed; anything else is rejected (schema phase).
//   - at most one explicit "primary" edge per node pair (more than one is an error).
//   - client edges must not be backup (client uses a single wg0 and does not participate in parallel links).
//   - equal-cost warning: if all links of a one-to-many group resolve to the same cost, no failover
//     preference can be expressed (warning); the default primary(96/babeld default) + backup(384) has
//     a cost gap and should not trigger it.
//   - no-primary warning: every link of a node pair is backup (e.g. after a role flip).
//   - D71 re-domained: an unrolled same-direction duplicate edge still warns, and the message suggests
//     switching to role: "backup"; backup edges do not trigger it.
//   - interface-name uniqueness (invariant N4): all primary/backup interface names on a node must not collide.
//
// The assertion style follows the neighboring validator tests: substring matching on a stable field
// prefix (edges[ / nodes[...].name) and a stable text fragment, asserting both "should trigger" and
// "should not trigger" to avoid vacuous passes.

// --- cross-field + message stable-fragment matching helpers ---

// errMatching reports whether some error exists whose Field contains fieldFrag and whose Message
// contains msgFrag. An empty fragment means that dimension is unconstrained.
func errMatching(result *ValidationResult, fieldFrag, msgFrag string) bool {
	for _, e := range result.Errors {
		if (fieldFrag == "" || containsSubstring(e.Field, fieldFrag)) &&
			(msgFrag == "" || containsSubstring(e.Message, msgFrag)) {
			return true
		}
	}
	return false
}

// warnMatching reports whether some warning exists whose Field contains fieldFrag and whose Message
// contains msgFrag.
func warnMatching(result *ValidationResult, fieldFrag, msgFrag string) bool {
	for _, w := range result.Warnings {
		if (fieldFrag == "" || containsSubstring(w.Field, fieldFrag)) &&
			(msgFrag == "" || containsSubstring(w.Message, msgFrag)) {
			return true
		}
	}
	return false
}

// parallelBaseTopology is the valid baseline for the parallel-links tests: two publicly reachable
// routers (to avoid NAT warnings) with one primary + backup edge pair (A->B). Both ends have
// HasPublicIP, and the edge carries an endpoint_host while the target node declares no
// public_endpoints, so the endpoint-consistency warning is not triggered.
func parallelBaseTopology() *model.Topology {
	topo := &model.Topology{
		Project: model.Project{ID: "pl-001", Name: "Parallel Links"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "test", CIDR: "10.10.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "node-a", Name: "alpha", Hostname: "alpha.example.com",
				Platform: "debian", Role: "router", DomainID: "domain-1",
				OverlayIP: "10.10.0.1",
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: true, CanForward: true, HasPublicIP: true,
				},
			},
			{
				ID: "node-b", Name: "beta", Hostname: "beta.example.com",
				Platform: "debian", Role: "router", DomainID: "domain-1",
				OverlayIP: "10.10.0.2",
				Capabilities: model.NodeCapabilities{
					CanAcceptInbound: true, CanForward: true, HasPublicIP: true,
				},
			},
		},
		Edges: []model.Edge{
			{
				ID: "e-ab", FromNodeID: "node-a", ToNodeID: "node-b",
				Type: "direct", EndpointHost: "beta.example.com",
				Transport: "udp", IsEnabled: true,
				// empty role == primary class.
			},
			{
				ID: "e-ab-backup", FromNodeID: "node-a", ToNodeID: "node-b",
				Type: "direct", EndpointHost: "beta.example.com",
				Transport: "udp", IsEnabled: true,
				Role: model.EdgeRoleBackup,
			},
		},
	}
	return topo
}

// --- 1. role enum rejection (schema phase) ---

// TestParallelLinks_RoleEnumRejected verifies that an illegal role value is rejected by schema
// validation while legal values pass.
func TestParallelLinks_RoleEnumRejected(t *testing.T) {
	// illegal role: errors at edges[0].role.
	topo := parallelBaseTopology()
	topo.Edges[0].Role = "tertiary"
	result := ValidateSchema(topo)
	assertHasError(t, result, "edges[0].role")

	// legal roles (primary / backup / empty): the role field must not error.
	for _, role := range []string{"", model.EdgeRolePrimary, model.EdgeRoleBackup} {
		ok := parallelBaseTopology()
		ok.Edges[0].Role = role
		res := ValidateSchema(ok)
		for _, e := range res.Errors {
			if containsSubstring(e.Field, "edges[0].role") {
				t.Errorf("legal role %q should not error in the schema phase, but got: %s", role, e.Error())
			}
		}
	}
}

// --- 2. at most one explicit primary per node pair ---

// TestParallelLinks_MultipleExplicitPrimaryRejected verifies that two explicit role:"primary" edges
// on the same node pair error; while "one explicit primary + one backup" is legal and must not
// trigger that error.
func TestParallelLinks_MultipleExplicitPrimaryRejected(t *testing.T) {
	// two explicit primaries (same node pair) -> error.
	topo := parallelBaseTopology()
	topo.Edges[0].Role = model.EdgeRolePrimary
	topo.Edges[1].Role = model.EdgeRolePrimary
	topo.Edges[1].FromNodeID = "node-b" // reversed, still the same node pair (same pinKey)
	topo.Edges[1].ToNodeID = "node-a"
	topo.Edges[1].EndpointHost = "alpha.example.com"
	result := ValidateSemantic(topo)
	if !errMatching(result, "edges[", "primary") {
		t.Errorf("two explicit primary edges on the same node pair should error (message contains primary), got: %v", result.Errors)
	}

	// one explicit primary + one backup -> should not trigger the "multiple primary" error.
	ok := parallelBaseTopology()
	ok.Edges[0].Role = model.EdgeRolePrimary // primary
	// Edges[1] is already backup.
	res := ValidateSemantic(ok)
	if errMatching(res, "edges[", "primary") {
		t.Errorf("one primary + one backup should not trigger the multiple-primary error, got: %v", res.Errors)
	}
}

// --- 3. client edges must not be backup ---

// TestParallelLinks_BackupOnClientRejected verifies that a backup edge touching a client node is
// rejected, while an ordinary (non-backup) client edge does not trigger that error.
func TestParallelLinks_BackupOnClientRejected(t *testing.T) {
	// build a client -> router topology and mark the outbound edge as backup.
	clientTopo := func() *model.Topology {
		return &model.Topology{
			Project: model.Project{ID: "pl-client", Name: "Client Backup"},
			Domains: []model.Domain{{
				ID: "domain-1", Name: "test", CIDR: "10.10.0.0/24",
				AllocationMode: "auto", RoutingMode: "babel",
			}},
			Nodes: []model.Node{
				{
					ID: "router-1", Name: "router", Role: "router", DomainID: "domain-1",
					OverlayIP:    "10.10.0.1",
					Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
				},
				{
					ID: "client-1", Name: "laptop", Role: "client", DomainID: "domain-1",
					OverlayIP: "10.10.0.9",
				},
			},
			Edges: []model.Edge{
				{
					ID: "e-cli", FromNodeID: "client-1", ToNodeID: "router-1",
					Type: "public-endpoint", EndpointHost: "router.example.com",
					Transport: "udp", IsEnabled: true,
				},
			},
		}
	}

	// backup client edge -> error (message names client and backup).
	bad := clientTopo()
	bad.Edges[0].Role = model.EdgeRoleBackup
	badRes := ValidateSemantic(bad)
	if !errMatching(badRes, "edges[", "backup") && !errMatching(badRes, "edges[", "client") {
		t.Errorf("a client edge set to backup should error (message contains backup/client), got: %v", badRes.Errors)
	}

	// ordinary client edge (no role) -> does not trigger the backup-on-client error.
	good := clientTopo()
	goodRes := ValidateSemantic(good)
	if errMatching(goodRes, "edges[", "backup") {
		t.Errorf("an ordinary client edge should not trigger the backup-on-client error, got: %v", goodRes.Errors)
	}
}

// --- 4. equal-cost warning vs. default primary+backup not warning ---

// TestParallelLinks_EqualCostWarning verifies that a one-to-many group warns if all links resolve to
// the same cost; while the default primary(no explicit cost) + backup(384), having a cost gap, never
// triggers the equal-cost warning.
func TestParallelLinks_EqualCostWarning(t *testing.T) {
	// two links explicitly set to the same cost (same node pair: one primary, one backup, but equal priority) -> warning.
	equal := parallelBaseTopology()
	equal.Edges[0].Priority = 200 // primary
	equal.Edges[1].Priority = 200 // backup, but explicit cost equals primary -> no failover preference
	equalRes := ValidateSemantic(equal)
	if !warnMatching(equalRes, "", "") {
		t.Fatalf("equal-cost topology should produce at least one warning, but produced none")
	}
	// there must be an equal-cost warning related to "cost". Match a stable fragment: "cost".
	if !warnMatching(equalRes, "", "cost") {
		t.Errorf("two equal-cost links should trigger the equal-cost warning (message contains cost), got: %v", equalRes.Warnings)
	}

	// default primary + backup (primary has no cost -> 0/babeld default; backup -> 384): there is a
	// cost gap, so the equal-cost warning should not trigger.
	gap := parallelBaseTopology() // Edges[0] empty role, Edges[1] backup, neither with explicit priority
	gapRes := ValidateSemantic(gap)
	if warnMatching(gapRes, "", "cost") {
		t.Errorf("default primary(96/default) + backup(384) has a cost gap and should not trigger the equal-cost warning, got: %v", gapRes.Warnings)
	}
}

// --- 5. no-primary warning ---

// TestParallelLinks_NoPrimaryWarning verifies that when every link of a node pair is backup (no
// primary-class edge at all) it warns; while a pair that has a primary-class edge does not trigger
// that warning.
func TestParallelLinks_NoPrimaryWarning(t *testing.T) {
	// node pair all-backup (flip the baseline's primary-class e-ab to backup as well, giving it a new id so each becomes its own link).
	noPrimary := parallelBaseTopology()
	noPrimary.Edges[0].Role = model.EdgeRoleBackup
	noPrimary.Edges[0].ID = "e-ab-bk0"
	// Edges[1] is already backup.
	noPrimaryRes := ValidateSemantic(noPrimary)
	if !warnMatching(noPrimaryRes, "", "primary") {
		t.Errorf("a node pair that is all backup (no primary) should warn (message contains primary), got: %v", noPrimaryRes.Warnings)
	}

	// a pair with a primary-class edge (the baseline itself) -> does not trigger the no-primary warning.
	hasPrimary := parallelBaseTopology() // Edges[0] empty role == primary class
	hasPrimaryRes := ValidateSemantic(hasPrimary)
	for _, w := range hasPrimaryRes.Warnings {
		// only a violation when the warning actually names the "no primary" semantics; match the stable English fragment "no primary".
		if containsSubstring(w.Message, "no primary") {
			t.Errorf("a pair with a primary-class edge should not trigger the no-primary warning, got: %s", w.Message)
		}
	}
}

// --- 6. D71 re-domained: unrolled same-direction duplicate edge warns (suggests backup); backup edges do not trigger ---

// TestParallelLinks_D71DuplicateStillWarnsSuggestsBackup verifies D71 re-domaining:
//   - an unrolled same-direction duplicate edge still triggers the duplicate-edge warning, and the message suggests switching to role: "backup";
//   - one primary + one backup (same direction) no longer triggers the duplicate-edge warning (backup is the supported parallel-links usage).
func TestParallelLinks_D71DuplicateStillWarnsSuggestsBackup(t *testing.T) {
	// two unrolled same-direction edges (A->B) -> duplicate-edge warning, message suggests backup.
	dup := parallelBaseTopology()
	dup.Edges[1].Role = "" // drop backup so both are unrolled same-direction edges
	dupRes := ValidateSemantic(dup)
	if !warnMatching(dupRes, "edges[", "backup") {
		t.Errorf("an unrolled same-direction duplicate edge should warn and suggest role: backup (message contains backup), got: %v", dupRes.Warnings)
	}

	// one unrolled primary + one backup (same direction) -> no longer triggers the "duplicate edge" warning.
	// use the stable fragment "only the first takes effect" from the D71 duplicate-edge message to judge whether that warning was wrongly triggered.
	mixed := parallelBaseTopology() // Edges[0] empty role, Edges[1] backup
	mixedRes := ValidateSemantic(mixed)
	if warnMatching(mixedRes, "edges[", "only the first takes effect") {
		t.Errorf("one primary + one backup should no longer trigger the D71 duplicate-edge warning, got: %v", mixedRes.Warnings)
	}
}

// --- 7. interface-name uniqueness (invariant N4) ---

// TestParallelLinks_BackupInterfaceNamesDistinct verifies that two backup edges between the same node
// pair generate distinct WireGuard interface names (edge-aware naming folds edge.ID into the hash),
// thereby satisfying the precondition for N4; and asserts the N4 validation path does not false-positive
// on a valid topology.
//
// The 16-bit hash suffix makes constructing a real collision impractical, so this test instead asserts
// "the two backup interface names are DISTINCT" (non-collision at the naming-authority level) and verifies
// that N4 validation produces no interface-name uniqueness error on a valid primary+backup topology.
func TestParallelLinks_BackupInterfaceNamesDistinct(t *testing.T) {
	const (
		backupID1 = "e-ab-bk1"
		backupID2 = "e-ab-bk2"
	)
	// naming authority: two backup interface names with the same remote but different edge.ID must differ.
	name1 := naming.WgInterfaceNameForEdge("beta", backupID1, true)
	name2 := naming.WgInterfaceNameForEdge("beta", backupID2, true)
	if name1 == name2 {
		t.Fatalf("two backup interface names with different edge.ID should differ, but both were %q", name1)
	}
	// the backup interface name must also differ from the primary interface name.
	if name1 == naming.WgInterfaceName("beta") {
		t.Errorf("the backup interface name should not equal the primary interface name: %q", name1)
	}

	// N4 validation path: a valid topology with primary + two backups (distinct interface names) must not produce an interface-name uniqueness error.
	topo := parallelBaseTopology()
	topo.Edges[1].ID = backupID1 // first backup
	topo.Edges = append(topo.Edges, model.Edge{
		ID: backupID2, FromNodeID: "node-a", ToNodeID: "node-b",
		Type: "direct", EndpointHost: "beta.example.com",
		Transport: "udp", IsEnabled: true, Role: model.EdgeRoleBackup,
	})
	res := ValidateSemantic(topo)
	// no interface-name collision error ("interface name" is the stable fragment for N4) may appear.
	if errMatching(res, "", "interface name") {
		t.Errorf("a primary + two backups topology with distinct interface names should not trigger the N4 interface-name uniqueness error, got: %v", res.Errors)
	}
}
