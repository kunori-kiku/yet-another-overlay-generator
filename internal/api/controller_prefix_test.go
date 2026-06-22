package api

// controller_prefix_test.go — subject-scoped tests for the operator/agent path-prefix
// split (controller-server-authority-redesign plan-1, D3). Guards: each mux mounts its
// routes under ITS OWN prefix only; the other audience's prefix and the bare path 404;
// the bootstrap script bakes the AGENT prefix (never the operator prefix) into the
// agent's controller base URL.
//
// Lifecycle: subject-scoped — retire at subject close (the perpetual custody guard is
// topology_custody_test.go).

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// newPrefixEnv stands up the shared controller test env with independent prefixes
// applied (the scaffold itself lives in newCtlTestEnvWith — single constructor).
func newPrefixEnv(t *testing.T, operatorPrefix, agentPrefix string) (opSrv, agentSrv *httptest.Server, store controller.Store) {
	t.Helper()
	env := newCtlTestEnvWith(t, func(ch *ControllerHandler) {
		ch.SetOperatorPathPrefix(operatorPrefix)
		ch.SetAgentPathPrefix(agentPrefix)
	})
	return env.opSrv, env.agentSrv, env.store
}

// TestPrefixSplit_IndependentMounts: with two different prefixes configured, each mux
// serves ONLY under its own prefix — the other audience's prefix and the bare path 404
// on both muxes. This is the routing contract that makes a per-audience path rule
// expressible on one hostname (the misroute that 404'd operator logins routed to the
// agent port is now structurally diagnosable).
func TestPrefixSplit_IndependentMounts(t *testing.T) {
	opSrv, agentSrv, _ := newPrefixEnv(t, "/op-secret/", "agent-secret") // both normalize

	// Operator mux: own prefix → mounted (200); agent prefix and bare path → 404.
	if st := doJSON(t, http.MethodGet, opSrv.URL+"/op-secret/api/v1/operator/nodes", testOperatorToken, nil, nil); st != http.StatusOK {
		t.Errorf("operator-prefixed /nodes = %d, want 200", st)
	}
	if st := doJSON(t, http.MethodGet, opSrv.URL+"/agent-secret/api/v1/operator/nodes", testOperatorToken, nil, nil); st != http.StatusNotFound {
		t.Errorf("agent-prefixed /nodes on operator mux = %d, want 404", st)
	}
	if st := doJSON(t, http.MethodGet, opSrv.URL+"/api/v1/operator/nodes", testOperatorToken, nil, nil); st != http.StatusNotFound {
		t.Errorf("bare /nodes on operator mux = %d, want 404", st)
	}

	// Agent mux: own prefix → mounted (401 without a token proves the route exists);
	// operator prefix and bare path → 404.
	if st := doJSON(t, http.MethodGet, agentSrv.URL+"/agent-secret/api/v1/agent/config", "", nil, nil); st != http.StatusUnauthorized {
		t.Errorf("agent-prefixed /config = %d, want 401 (mounted, needs a token)", st)
	}
	if st := doJSON(t, http.MethodGet, agentSrv.URL+"/op-secret/api/v1/agent/config", "", nil, nil); st != http.StatusNotFound {
		t.Errorf("operator-prefixed /config on agent mux = %d, want 404", st)
	}
	if st := doJSON(t, http.MethodGet, agentSrv.URL+"/api/v1/agent/config", "", nil, nil); st != http.StatusNotFound {
		t.Errorf("bare /config on agent mux = %d, want 404", st)
	}
}

// TestPrefixSplit_OneSidedPrefix: setting ONLY the operator prefix leaves the agent
// routes at the bare path (and vice versa) — the two prefixes are fully independent.
func TestPrefixSplit_OneSidedPrefix(t *testing.T) {
	opSrv, agentSrv, _ := newPrefixEnv(t, "panel-only", "")

	if st := doJSON(t, http.MethodGet, opSrv.URL+"/panel-only/api/v1/operator/nodes", testOperatorToken, nil, nil); st != http.StatusOK {
		t.Errorf("operator-prefixed /nodes = %d, want 200", st)
	}
	// The agent mux serves at the BARE path: its prefix was not set.
	if st := doJSON(t, http.MethodGet, agentSrv.URL+"/api/v1/agent/config", "", nil, nil); st != http.StatusUnauthorized {
		t.Errorf("bare agent /config = %d, want 401 (mounted at bare path)", st)
	}
	if st := doJSON(t, http.MethodGet, agentSrv.URL+"/panel-only/api/v1/agent/config", "", nil, nil); st != http.StatusNotFound {
		t.Errorf("operator-prefixed agent /config = %d, want 404", st)
	}
}

// TestPrefixSplit_BootstrapBakesAgentPrefix: the bootstrap script's CONTROLLER base
// must carry the AGENT prefix — the installed agent only ever talks to the agent
// port. Baking the operator prefix instead would point every fleet node at a path
// that 404s (the exact failure mode of the shared-prefix design).
func TestPrefixSplit_BootstrapBakesAgentPrefix(t *testing.T) {
	opSrv, agentSrv, _ := newPrefixEnv(t, "op-secret", "agent-secret")

	// Save a public agent URL so the script has a base to compose with.
	st := doJSON(t, http.MethodPost, opSrv.URL+"/op-secret/api/v1/operator/settings", testOperatorToken,
		map[string]any{"public_agent_url": "https://overlay.example.com"}, nil)
	if st != http.StatusOK {
		t.Fatalf("POST settings = %d, want 200", st)
	}

	resp, err := agentSrv.Client().Get(agentSrv.URL + "/agent-secret/api/v1/agent/bootstrap")
	if err != nil {
		t.Fatalf("GET bootstrap: %v", err)
	}
	script, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET bootstrap = %d, want 200", resp.StatusCode)
	}
	if want := []byte("CONTROLLER='https://overlay.example.com/agent-secret'"); !bytes.Contains(script, want) {
		t.Errorf("bootstrap script must bake the AGENT prefix into CONTROLLER; got:\n%s",
			firstLines(string(script), 12))
	}
	if bytes.Contains(script, []byte("op-secret")) {
		t.Errorf("bootstrap script leaked the OPERATOR prefix (agents must never see it)")
	}
}

// TestPrefixSplit_Normalization: the setters normalize whitespace and slashes ("" or
// "/<seg>"); the two audiences then mount under DISTINCT fixed namespaces
// (/api/v1/operator/ vs /api/v1/agent/) so the surfaces never collide by path.
func TestPrefixSplit_Normalization(t *testing.T) {
	// in → the normalized prefix SEGMENT (empty or "/<seg>"); the fixed namespace
	// suffix is appended per audience below.
	for in, seg := range map[string]string{
		"":           "",
		"  ":         "",
		"/s3cr3t/":   "/s3cr3t",
		"s3cr3t":     "/s3cr3t",
		" /s3cr3t ":  "/s3cr3t",
		"a/b":        "/a/b",
		"//s3cr3t//": "/s3cr3t",
	} {
		ch := NewControllerHandler(controller.NewMemStore(), testTenant, "", DefaultOperatorName, "dev")
		ch.SetOperatorPathPrefix(in)
		ch.SetAgentPathPrefix(in)
		if got, want := ch.OperatorBasePath(), seg+"/api/v1/operator/"; got != want {
			t.Errorf("OperatorBasePath(%q) = %q, want %q", in, got, want)
		}
		if got, want := ch.AgentBasePath(), seg+"/api/v1/agent/"; got != want {
			t.Errorf("AgentBasePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPrefixSplit_SettingsReportAgentPrefix (plan-1.5): GET /settings reports the
// server's normalized agent prefix READ-ONLY, so the panel composes agent-facing
// URLs (bootstrap one-liner, enroll command) server-authoritatively instead of
// asking the operator to mirror a second env var. POST ignores a submitted value.
func TestPrefixSplit_SettingsReportAgentPrefix(t *testing.T) {
	opSrv, _, _ := newPrefixEnv(t, "op-secret", "agent-secret/")

	var got struct {
		AgentPathPrefix string `json:"agent_path_prefix"`
	}
	if st := doJSON(t, http.MethodGet, opSrv.URL+"/op-secret/api/v1/operator/settings", testOperatorToken, nil, &got); st != http.StatusOK {
		t.Fatalf("GET settings = %d, want 200", st)
	}
	if got.AgentPathPrefix != "/agent-secret" {
		t.Errorf("agent_path_prefix = %q, want %q (normalized)", got.AgentPathPrefix, "/agent-secret")
	}

	// POST with a forged agent_path_prefix: accepted (the field is in the wire struct)
	// but IGNORED — the next GET still reports the env-derived value.
	st := doJSON(t, http.MethodPost, opSrv.URL+"/op-secret/api/v1/operator/settings", testOperatorToken,
		map[string]any{"public_agent_url": "https://overlay.example.com", "agent_path_prefix": "/forged"}, &got)
	if st != http.StatusOK {
		t.Fatalf("POST settings = %d, want 200", st)
	}
	if got.AgentPathPrefix != "/agent-secret" {
		t.Errorf("POST response agent_path_prefix = %q, want %q (read-only)", got.AgentPathPrefix, "/agent-secret")
	}
}

// firstLines returns up to n leading lines of s (test-failure readability helper).
func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
