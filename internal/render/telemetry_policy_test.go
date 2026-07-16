package render

import (
	"context"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

func TestAll_TelemetryPolicyFollowsAgentHeldCustody(t *testing.T) {
	routerKey := mustGenerateKey(t)
	peerKey := mustGenerateKey(t)
	clientKey := mustGenerateKey(t)

	renderWithCustody := func(t *testing.T, custody KeyCustody, publicOnly bool) *compiler.CompileResult {
		t.Helper()
		topo := custodyTopology(routerKey, peerKey, clientKey, publicOnly)
		topo.Nodes[0].TelemetryProbes = []model.TelemetryProbe{{
			ID: "control", Type: model.TelemetryProbeTCP, Host: "control.example", Port: 443,
		}}
		keys, err := GenerateKeys(topo, custody)
		if err != nil {
			t.Fatalf("GenerateKeys: %v", err)
		}
		result, err := compiler.NewCompiler().Compile(context.Background(), topo, keys)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		if len(result.TelemetryPolicyJSON) != 0 {
			t.Fatalf("compiler manufactured serialized telemetry policy: %+v", result.TelemetryPolicyJSON)
		}
		if err := All(result, keys, FetchSettings{}); err != nil {
			t.Fatalf("All: %v", err)
		}
		return result
	}

	agentHeld := renderWithCustody(t, AgentHeld, true)
	if got := agentHeld.TelemetryPolicyJSON["router-1"]; got == "" {
		t.Fatal("AgentHeld render omitted active telemetry policy")
	}

	airGap := renderWithCustody(t, AirGap, false)
	if len(airGap.TelemetryPolicyJSON) != 0 {
		t.Fatalf("AirGap render retained agent-only telemetry policy: %+v", airGap.TelemetryPolicyJSON)
	}
}
