package api

// topology_custody_test.go — PERPETUAL guard pinning the PRINCIPLES.md "Key custody"
// principle at the STORE BOUNDARY (controller-server-authority-redesign plan-1, D4):
// POST /update-topology refuses any payload carrying a WireGuard private key with 400,
// and the stored bytes never contain one. Controller mode is zero-knowledge — the
// panel strips client-side; a key reaching this handler is a custody bug upstream and
// must fail loudly, never be persisted.
//
// Lifecycle: PERPETUAL. Never retire this test; it is the enforcement backstop the
// docs/spec/controller/persistence.md "public-keys-only store" guarantee cites.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// newCustodyEnv stands up the operator mux (bare prefixes — custody is orthogonal to
// the path prefix) over a MemStore.
func newCustodyEnv(t *testing.T) (*httptest.Server, controller.Store) {
	t.Helper()
	store := controller.NewMemStore()
	ch := NewControllerHandler(store, testTenant, controller.HashToken(testOperatorToken), DefaultOperatorName)
	mux := http.NewServeMux()
	ch.RegisterOperatorRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, store
}

// custodyTopology builds a minimal schema-valid topology; withKey plants a private
// key on the second node (not the first — the gate must scan EVERY node).
func custodyTopology(withKey bool) model.Topology {
	topo := model.Topology{
		Project: model.Project{ID: "p1", Name: "custody"},
		Domains: []model.Domain{{ID: "d1", Name: "main", CIDR: "10.0.0.0/24", AllocationMode: "auto", RoutingMode: "babel"}},
		Nodes: []model.Node{
			{ID: "n1", Name: "alpha", Role: "peer", DomainID: "d1"},
			{ID: "n2", Name: "bravo", Role: "peer", DomainID: "d1"},
		},
	}
	if withKey {
		topo.Nodes[1].FixedPrivateKey = true
		topo.Nodes[1].WireGuardPrivateKey = "SGVsbG8tLW5vdC1hLXJlYWwta2V5LWJ1dC1zZWNyZXQ="
	}
	return topo
}

// TestTopologyCustody_RejectsPrivateKeys: a keyed payload is refused with 400 and the
// custody message, and NOTHING is stored (the store still has no topology).
func TestTopologyCustody_RejectsPrivateKeys(t *testing.T) {
	srv, store := newCustodyEnv(t)

	st := doJSON(t, http.MethodPost, srv.URL+"/api/v1/controller/update-topology", testOperatorToken, custodyTopology(true), nil)
	if st != http.StatusBadRequest {
		t.Fatalf("keyed update-topology = %d, want 400", st)
	}

	// Fail-closed means fail-CLOSED: the keyed payload must not have been persisted.
	if _, err := store.GetTopology(context.Background(), testTenant); !errors.Is(err, controller.ErrNotFound) {
		t.Fatalf("store has a topology after a refused keyed upload (err=%v); the gate must reject BEFORE PutTopology", err)
	}
}

// TestTopologyCustody_RejectionMessageNamesCustody: the 400 body tells the operator
// WHY (key custody) and WHAT to do (strip client-side) — not a generic schema error.
func TestTopologyCustody_RejectionMessageNamesCustody(t *testing.T) {
	srv, _ := newCustodyEnv(t)

	payload, err := json.Marshal(custodyTopology(true))
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/controller/update-topology", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testOperatorToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var raw map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode 400 body: %v", err)
	}
	msg := strings.ToLower(raw["error"])
	if !strings.Contains(msg, "private key") || !strings.Contains(msg, "custody") {
		t.Errorf("rejection message must name private keys + custody; got %q", raw["error"])
	}
}

// TestTopologyCustody_CleanTopologyStored: a clean (public-only) topology is accepted,
// stored verbatim, audited, and the stored bytes carry no private key material.
func TestTopologyCustody_CleanTopologyStored(t *testing.T) {
	srv, store := newCustodyEnv(t)

	var out map[string]int64
	st := doJSON(t, http.MethodPost, srv.URL+"/api/v1/controller/update-topology", testOperatorToken, custodyTopology(false), &out)
	if st != http.StatusOK {
		t.Fatalf("clean update-topology = %d, want 200", st)
	}
	if out["version"] < 1 {
		t.Errorf("version = %d, want >= 1", out["version"])
	}

	rec, err := store.GetTopology(context.Background(), testTenant)
	if err != nil {
		t.Fatalf("GetTopology after clean upload: %v", err)
	}
	if strings.Contains(string(rec.JSON), `"wireguard_private_key":"`) &&
		!strings.Contains(string(rec.JSON), `"wireguard_private_key":""`) {
		t.Errorf("stored topology bytes carry a non-empty private key:\n%s", rec.JSON)
	}

	// The update is audited (subject-scoped assertion riding in the perpetual file:
	// it documents that custody acceptance and auditability were introduced together).
	entries, err := store.ListAudit(context.Background(), testTenant)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "update-topology" {
			found = true
		}
	}
	if !found {
		t.Errorf("no update-topology audit entry after a successful upload")
	}
}

// TestTopologyCustody_NonSchemaJSONRejected: valid JSON that does not unmarshal into
// the topology model is a 400 — bytes the gate cannot custody-check are never stored.
func TestTopologyCustody_NonSchemaJSONRejected(t *testing.T) {
	srv, store := newCustodyEnv(t)

	st := doJSON(t, http.MethodPost, srv.URL+"/api/v1/controller/update-topology", testOperatorToken,
		map[string]any{"nodes": "not-a-node-array"}, nil)
	if st != http.StatusBadRequest {
		t.Fatalf("non-schema update-topology = %d, want 400", st)
	}
	if _, err := store.GetTopology(context.Background(), testTenant); !errors.Is(err, controller.ErrNotFound) {
		t.Fatalf("store has a topology after a refused non-schema upload (err=%v)", err)
	}
}
