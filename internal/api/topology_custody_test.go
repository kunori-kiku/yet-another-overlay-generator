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

// newCustodyEnv rides the package's single controller test-env constructor (bare
// prefixes — custody is orthogonal to the path prefix).
func newCustodyEnv(t *testing.T) (*httptest.Server, controller.Store) {
	t.Helper()
	env := newCtlTestEnv(t)
	return env.opSrv, env.store
}

// custodyTopology derives from the package's canonical minimal fixture (smallTopo —
// one source of "schema-valid topology" truth); withKey plants a private key on the
// SECOND node, not the first, so the gate is proven to scan every node.
func custodyTopology(withKey bool) model.Topology {
	topo := *smallTopo()
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

	st := doJSON(t, http.MethodPost, srv.URL+"/api/v1/operator/update-topology", testOperatorToken, custodyTopology(true), nil)
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
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/operator/update-topology", bytes.NewReader(payload))
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
// stored in canonical form, audited, and the stored bytes carry no private key material.
func TestTopologyCustody_CleanTopologyStored(t *testing.T) {
	srv, store := newCustodyEnv(t)

	var out map[string]int64
	st := doJSON(t, http.MethodPost, srv.URL+"/api/v1/operator/update-topology", testOperatorToken, custodyTopology(false), &out)
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

	st := doJSON(t, http.MethodPost, srv.URL+"/api/v1/operator/update-topology", testOperatorToken,
		map[string]any{"nodes": "not-a-node-array"}, nil)
	if st != http.StatusBadRequest {
		t.Fatalf("non-schema update-topology = %d, want 400", st)
	}
	if _, err := store.GetTopology(context.Background(), testTenant); !errors.Is(err, controller.ErrNotFound) {
		t.Fatalf("store has a topology after a refused non-schema upload (err=%v)", err)
	}
}

// TestTopologyCustody_CanonicalStorageDefeatsSmuggling: the gate checks the PARSED
// view, so the store must persist the canonical re-marshaled form — raw bytes could
// smuggle key material past the gate via duplicate JSON keys (last-key-wins parsing
// leaves the parsed field empty while the raw bytes keep the key) or via fields
// outside the model (silently dropped by Unmarshal, preserved verbatim). Stored
// bytes must equal the checked view by construction.
func TestTopologyCustody_CanonicalStorageDefeatsSmuggling(t *testing.T) {
	srv, store := newCustodyEnv(t)

	const smuggled = "U01VR0dMRUQtUFJJVkFURS1LRVktQllURVM="
	clean, err := json.Marshal(custodyTopology(false))
	if err != nil {
		t.Fatal(err)
	}
	// Plant (1) a duplicate-key pair on the first node object — the KEYED copy first,
	// an empty copy second, so the parsed (last-wins) view passes the gate — and
	// (2) an unknown top-level field stashing the same secret.
	dupKey := bytes.Replace(clean,
		[]byte(`"nodes":[{`),
		[]byte(`"nodes":[{"wireguard_private_key":"`+smuggled+`","wireguard_private_key":"",`), 1)
	payload := bytes.Replace(dupKey,
		[]byte(`{"project":`),
		[]byte(`{"smuggled_field":"`+smuggled+`","project":`), 1)
	if !json.Valid(payload) {
		t.Fatalf("test bug: crafted payload is not valid JSON:\n%s", payload)
	}

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/operator/update-topology", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testOperatorToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// The parsed view is clean, so the upload is accepted — the guarantee under test
	// is that what gets STORED is the parsed view, not the smuggling bytes.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("smuggle-shaped upload = %d, want 200 (parsed view is clean)", resp.StatusCode)
	}

	rec, err := store.GetTopology(context.Background(), testTenant)
	if err != nil {
		t.Fatalf("GetTopology: %v", err)
	}
	if bytes.Contains(rec.JSON, []byte(smuggled)) {
		t.Fatalf("stored topology bytes carry smuggled key material (canonicalization failed):\n%s", rec.JSON)
	}
	if bytes.Contains(rec.JSON, []byte("smuggled_field")) {
		t.Fatalf("stored topology bytes carry the unknown stash field:\n%s", rec.JSON)
	}
}
