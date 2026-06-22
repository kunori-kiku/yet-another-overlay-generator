package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEdge_BackCompatLoadNoMimicFallback pins plan-4 back-compat: a legacy edge JSON with no
// mimic_fallback field loads as "" (inherit), and an edge with an empty policy round-trips WITHOUT
// the key (omitempty) — so an old persisted topology is byte-identical on save.
func TestEdge_BackCompatLoadNoMimicFallback(t *testing.T) {
	legacy := `{"id":"e1","from_node_id":"a","to_node_id":"b","type":"direct","transport":"tcp","is_enabled":true}`
	var e Edge
	if err := json.Unmarshal([]byte(legacy), &e); err != nil {
		t.Fatalf("unmarshal legacy edge: %v", err)
	}
	if e.MimicFallback != "" {
		t.Fatalf("legacy edge MimicFallback = %q, want \"\" (inherit)", e.MimicFallback)
	}

	// An empty policy is omitted on marshal (omitempty) — no new key in a re-saved legacy topology.
	out, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "mimic_fallback") {
		t.Fatalf("empty MimicFallback must be omitted, got %s", out)
	}

	// A set policy round-trips under the documented key.
	e.MimicFallback = "udp"
	out, _ = json.Marshal(e)
	if !strings.Contains(string(out), `"mimic_fallback":"udp"`) {
		t.Fatalf("set MimicFallback must marshal under mimic_fallback, got %s", out)
	}
}
