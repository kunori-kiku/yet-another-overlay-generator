package api

// topology_versions_handler_test.go — subject-scoped tests for the topology
// version-history HTTP surface (controller-server-authority-redesign plan-2, D7):
// GET /topology/versions (operator-auth list) and GET /topology?version=N
// (retained payload; 404 unknown/pruned; 400 malformed).
//
// Lifecycle: subject-scoped — retire at subject close (the Store history contract
// itself is pinned by the perpetual store-compat suite).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestTopologyVersionsHTTP(t *testing.T) {
	env := newCtlTestEnv(t)

	// Unauthenticated → 401; node tokens must not read design history → enroll one
	// and present its token → 403.
	if st := doJSON(t, http.MethodGet, env.opURL("topology/versions"), "", nil, nil); st != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /topology/versions = %d, want 401", st)
	}
	nodeToken := env.enrollNode(t, "node-1")
	if st := doJSON(t, http.MethodGet, env.opURL("topology/versions"), nodeToken, nil, nil); st != http.StatusForbidden {
		t.Fatalf("node-token /topology/versions = %d, want 403", st)
	}

	// Empty history: 200 with an empty list (not 404 — the route exists).
	var list struct {
		Versions []struct {
			Version int64 `json:"version"`
			Bytes   int   `json:"bytes"`
		} `json:"versions"`
		Limit int `json:"limit"`
	}
	if st := doJSON(t, http.MethodGet, env.opURL("topology/versions"), testOperatorToken, nil, &list); st != http.StatusOK {
		t.Fatalf("empty /topology/versions = %d, want 200", st)
	}
	if len(list.Versions) != 0 {
		t.Fatalf("empty history lists %d versions, want 0", len(list.Versions))
	}
	if list.Limit <= 0 {
		t.Fatalf("limit = %d, want > 0", list.Limit)
	}

	// Three uploads → list shows v3,v2,v1 newest first.
	for i := 1; i <= 3; i++ {
		topo := smallTopo()
		topo.Project.Description = fmt.Sprintf("rev-%d", i)
		body, _ := json.Marshal(topo)
		if st := doRaw(t, http.MethodPost, env.opURL("update-topology"), testOperatorToken, body); st != http.StatusOK {
			t.Fatalf("update-topology #%d = %d, want 200", i, st)
		}
	}
	if st := doJSON(t, http.MethodGet, env.opURL("topology/versions"), testOperatorToken, nil, &list); st != http.StatusOK {
		t.Fatalf("/topology/versions = %d, want 200", st)
	}
	if len(list.Versions) != 3 || list.Versions[0].Version != 3 || list.Versions[2].Version != 1 {
		t.Fatalf("versions = %+v, want [3 2 1]", list.Versions)
	}

	// A retained payload round-trips: ?version=1 returns the FIRST upload (doJSON
	// decodes the 200 body — no hand-rolled request block).
	var got struct {
		Project struct {
			Description string `json:"description"`
		} `json:"project"`
	}
	if st := doJSON(t, http.MethodGet, env.opURL("topology?version=1"), testOperatorToken, nil, &got); st != http.StatusOK {
		t.Fatalf("GET topology?version=1 = %d, want 200", st)
	}
	if got.Project.Description != "rev-1" {
		t.Fatalf("version 1 description = %q, want rev-1", got.Project.Description)
	}

	// Unknown version → 404; malformed → 400.
	if st := doJSON(t, http.MethodGet, env.opURL("topology?version=99"), testOperatorToken, nil, nil); st != http.StatusNotFound {
		t.Fatalf("topology?version=99 = %d, want 404", st)
	}
	for _, bad := range []string{"0", "-1", "abc", "1.5"} {
		if st := doJSON(t, http.MethodGet, env.opURL("topology?version="+bad), testOperatorToken, nil, nil); st != http.StatusBadRequest {
			t.Fatalf("topology?version=%s = %d, want 400", bad, st)
		}
	}

	// No-query GET /topology still serves the CURRENT record.
	if st := doJSON(t, http.MethodGet, env.opURL("topology"), testOperatorToken, nil, nil); st != http.StatusOK {
		t.Fatalf("GET topology (current) = %d, want 200", st)
	}
}
