package artifacts

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

func TestBundleFiles_TelemetryPolicyIsOptionalSignedMember(t *testing.T) {
	result := &compiler.CompileResult{
		AgentHeld: true,
		Topology:  &model.Topology{Nodes: []model.Node{{ID: "node-1", Name: "node", Role: "peer"}}},
		TelemetryPolicyJSON: map[string]string{
			"node-1": `{"version":1,"probes":[{"id":"tls","type":"tcp","host":"example.com","port":443}]}`,
		},
	}
	files := BundleFiles(result, "node-1")
	if got := files["telemetry.json"]; got != result.TelemetryPolicyJSON["node-1"] {
		t.Fatalf("telemetry.json = %q, want canonical policy", got)
	}
	delete(result.TelemetryPolicyJSON, "node-1")
	if _, ok := BundleFiles(result, "node-1")["telemetry.json"]; ok {
		t.Fatal("node without probes gained telemetry.json; historical bundle bytes must remain unchanged")
	}
	result.TelemetryPolicyJSON["node-1"] = `{"version":1,"probes":[{"id":"tls","type":"tcp","host":"example.com","port":443}]}`
	result.AgentHeld = false
	if _, ok := BundleFiles(result, "node-1")["telemetry.json"]; ok {
		t.Fatal("air-gap bundle gained an agent-only telemetry policy")
	}
}
