//go:build airgap

// handler_airgap_heal_test.go — plan-1.6 (framework-refactor): lock the air-gap handler pre-heal.
//
// plan-1 added normalize.HealCollidingPins(topo) at the top of (*Handler).airGapRequest
// (handler_airgap.go), mirroring the controller's CompileAndStage pre-heal, so the anonymous
// air-gap compute routes (/api/compile, /api/export, /api/deploy-script) converge on the SAME
// healed compile the controller produces instead of loud-failing on a colliding-pin design.
// Nothing asserted that the pre-heal is APPLIED — an untested one-line heal is exactly the class
// of gap plan-1 closed. This file is that lock: it proves airGapRequest heals a genuinely
// colliding topology, so the divergence cannot silently regress.
//
// The heal SEMANTICS (which edge is kept, fresh reallocation) are owned elsewhere
// (internal/normalize, internal/compiler/reenable_heal_test.go); the loud safety net is owned by
// the conformance heal-collision-reenable fail fixture. This file owns only the "the air-gap
// boundary applies the heal" delta.

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/normalize"
)

// collidingPinTopology returns a fresh model.Topology carrying the C2 cross-link pin-collision
// shape — mirroring internal/edgecase/generator.go's collidingCrossLinkPins(): two DIFFERENT
// links (pa->r1 and pb->r1) whose edges both pin the SAME transit-IP pair (from 10.10.0.2,
// to 10.10.0.1) out of the domain's 10.10.0.0/24 transit pool. That is exactly the "one transit
// IP occupied by two different links" state the compiler's semantic validator rejects LOUD and
// normalize.HealCollidingPins repairs. Only the transit IPs are pinned (ports/link-locals left
// for gap-fill) so the collision is isolated to one resource.
//
// It is a builder (returns a fresh value each call) so callers get INDEPENDENT copies: healing
// one copy in place must not disturb the copy driven through the handler. The router carries the
// capabilities + public endpoint a full compile needs (so the bonus /api/compile assertion below
// can reach a healed SUCCESS); no WireGuard keys are set — the air-gap pipeline generates them.
func collidingPinTopology() model.Topology {
	dom := model.Domain{
		ID: "d1", Name: "d1", CIDR: "10.50.0.0/24",
		AllocationMode: "auto", RoutingMode: "babel",
		TransitCIDR: "10.10.0.0/24",
	}
	router := model.Node{
		ID: "r1", Name: "r1", Hostname: "r1.example.com", Role: "router", DomainID: "d1",
		Capabilities:    model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
		PublicEndpoints: []model.PublicEndpoint{{ID: "r1-ep", Host: "r1.example.com", Port: 51820}},
	}
	pa := model.Node{ID: "pa", Name: "pa", Role: "peer", DomainID: "d1"}
	pb := model.Node{ID: "pb", Name: "pb", Role: "peer", DomainID: "d1"}
	mkEdge := func(id, from string) model.Edge {
		return model.Edge{
			ID: id, FromNodeID: from, ToNodeID: "r1", Type: "public-endpoint",
			EndpointHost: "r1.example.com", Transport: "udp", IsEnabled: true,
			// Both distinct links pin the SAME transit pair — the collision.
			PinnedFromTransitIP: "10.10.0.2", PinnedToTransitIP: "10.10.0.1",
		}
	}
	return model.Topology{
		Project: model.Project{ID: "e2e-colliding-pins", Name: "colliding-pins"},
		Domains: []model.Domain{dom},
		Nodes:   []model.Node{router, pa, pb},
		Edges:   []model.Edge{mkEdge("e-a", "pa"), mkEdge("e-b", "pb")},
	}
}

// TestAirGapRequest_PreHealsCollidingPins is the two-sided proof that (*Handler).airGapRequest
// applies the pre-heal. airGapRequest does NOT compile — it heals in place then resolves env — so
// this asserts the heal directly, independent of the compile pipeline:
//
//	(1) the fixture GENUINELY collides — a fresh copy through HealCollidingPins reports a change;
//	(2) after airGapRequest, HealCollidingPins over the same (in-place-healed) topology reports
//	    NOTHING left to strip — i.e. airGapRequest already healed it.
//
// If the pre-heal line were removed from airGapRequest, (2) would still find the collision and
// the assertion would fail (verified non-vacuous during plan-1.6 execution).
func TestAirGapRequest_PreHealsCollidingPins(t *testing.T) {
	// (1) Prove the fixture is non-vacuous: an independent copy genuinely collides.
	raw := collidingPinTopology()
	if !normalize.HealCollidingPins(&raw) {
		t.Fatal("fixture is vacuous: HealCollidingPins reported NO change on the colliding-pin topology; " +
			"the two edges must pin the same transit pair for this test to guard anything")
	}

	// (2) Drive an UNHEALED copy through airGapRequest. Handler is an empty struct and
	// airGapRequest uses no Handler state, so &Handler{} is a complete receiver. With the
	// signing-key / fetch-catalog env vars unset (the default), airGapRequest returns nil error.
	topo := collidingPinTopology()
	h := &Handler{}
	req, err := h.airGapRequest(&topo)
	if err != nil {
		t.Fatalf("airGapRequest returned an error with env unset (want nil): %v", err)
	}

	// The passed topology was healed IN PLACE, so a second heal finds nothing to strip.
	if normalize.HealCollidingPins(&topo) {
		t.Error("airGapRequest did NOT pre-heal: HealCollidingPins still found a colliding pin to strip " +
			"after airGapRequest ran (the plan-1 air-gap pre-heal regressed)")
	}
	// The request's Topology snapshot is likewise already healed (defence-in-depth: the built
	// CompileRequest, not just the caller's local, carries the healed graph into the compiler).
	if normalize.HealCollidingPins(&req.Topology) {
		t.Error("airGapRequest built a CompileRequest whose Topology still carries a colliding pin")
	}
}

// TestHandleCompile_CollidingPinsHealedToSuccess drives the SAME colliding topology through the
// real /api/compile air-gap route (as the operator's local-design oracle would) and asserts a
// HEALED SUCCESS: HTTP 200 with allocated overlay IPs — NOT the loud
// validation_pin_transit_ip_duplicate_cross_link rejection the un-healed graph would draw from the
// compiler's semantic validator. This is invariant [1] (air-gap == controller parity): the fix
// must land as a success, not merely "no crash". Removing the airGapRequest pre-heal turns this
// 200 into a 4xx (verified non-vacuous during plan-1.6 execution).
func TestHandleCompile_CollidingPinsHealedToSuccess(t *testing.T) {
	server := NewServer()

	body, err := json.Marshal(collidingPinTopology())
	if err != nil {
		t.Fatalf("marshal colliding topology: %v", err)
	}

	httpReq := httptest.NewRequest(http.MethodPost, "/api/compile", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, httpReq)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 (healed success), got %d — the colliding-pin design was NOT pre-healed at "+
			"the air-gap boundary; body: %s", rec.Code, rec.Body.String())
	}

	var resp CompileResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode compile response: %v", err)
	}
	// A real (healed) compile allocated every node an overlay IP — proof the pipeline ran to
	// completion, not that a validation error was silently swallowed.
	for _, node := range resp.Topology.Nodes {
		if node.OverlayIP == "" {
			t.Errorf("node %s has no overlay IP after the healed compile", node.Name)
		}
	}
	if len(resp.WireGuardConfigs) == 0 {
		t.Error("healed compile produced no WireGuard configs")
	}
}
