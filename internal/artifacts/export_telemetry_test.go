package artifacts

import (
	"path/filepath"
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
		TelemetrySuccessorPolicyJSON: map[string]string{},
	}
	files, err := BundleFiles(result, "node-1")
	if err != nil {
		t.Fatalf("BundleFiles: %v", err)
	}
	if got := files["telemetry.json"]; got != result.TelemetryPolicyJSON["node-1"] {
		t.Fatalf("telemetry.json = %q, want canonical policy", got)
	}
	delete(result.TelemetryPolicyJSON, "node-1")
	files, err = BundleFiles(result, "node-1")
	if err != nil {
		t.Fatalf("BundleFiles without policy: %v", err)
	}
	if _, ok := files["telemetry.json"]; ok {
		t.Fatal("node without probes gained telemetry.json; historical bundle bytes must remain unchanged")
	}

	const successor = `{"version":2,"devices":{"mode":"all-eligible-v1"}}`
	result.TelemetrySuccessorPolicyJSON["node-1"] = successor
	files, err = BundleFiles(result, "node-1")
	if err != nil {
		t.Fatalf("BundleFiles successor: %v", err)
	}
	if got := files["telemetry-policy.json"]; got != successor {
		t.Fatalf("telemetry-policy.json = %q, want canonical successor policy", got)
	}
	if _, ok := files["telemetry.json"]; ok {
		t.Fatal("successor bundle also contains telemetry.json")
	}

	result.TelemetryPolicyJSON["node-1"] = `{"version":1,"probes":[{"id":"tls","type":"tcp","host":"example.com","port":443}]}`
	if _, err := BundleFiles(result, "node-1"); err == nil {
		t.Fatal("BundleFiles accepted both telemetry policy members")
	}
	delete(result.TelemetryPolicyJSON, "node-1")
	t.Setenv("YAOG_BUNDLE_SIGNING_KEY", "")
	output := filepath.Join(t.TempDir(), "export")
	if _, err := Export(result, output); err != nil {
		t.Fatalf("Export successor: %v", err)
	}
	if _, covered := readChecksumFor(t, filepath.Join(output, "node-1", "checksums.sha256"), "telemetry-policy.json"); !covered {
		t.Fatal("successor telemetry policy is not covered by checksums.sha256")
	}

	result.AgentHeld = false
	files, err = BundleFiles(result, "node-1")
	if err != nil {
		t.Fatalf("BundleFiles air-gap: %v", err)
	}
	if _, ok := files["telemetry.json"]; ok {
		t.Fatal("air-gap bundle gained an agent-only telemetry policy")
	}
	if _, ok := files["telemetry-policy.json"]; ok {
		t.Fatal("air-gap bundle gained an agent-only successor telemetry policy")
	}
}
