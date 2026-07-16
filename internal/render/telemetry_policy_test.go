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
			ID: "control", Name: "Controller reachability", Type: model.TelemetryProbeTCP, Host: "control.example", Port: 443,
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
	if got, want := agentHeld.TelemetryPolicyJSON["router-1"], `{"version":1,"probes":[{"id":"control","type":"tcp","host":"control.example","port":443}]}`; got != want {
		t.Fatalf("AgentHeld telemetry policy = %q, want display-name-free v1 bytes %q", got, want)
	}

	airGap := renderWithCustody(t, AirGap, false)
	if len(airGap.TelemetryPolicyJSON) != 0 {
		t.Fatalf("AirGap render retained agent-only telemetry policy: %+v", airGap.TelemetryPolicyJSON)
	}
}

func TestAll_SuccessorPolicyIsExclusiveAndAgentHeld(t *testing.T) {
	routerKey := mustGenerateKey(t)
	peerKey := mustGenerateKey(t)
	clientKey := mustGenerateKey(t)

	renderWithCustody := func(t *testing.T, custody KeyCustody, publicOnly bool) *compiler.CompileResult {
		t.Helper()
		topo := custodyTopology(routerKey, peerKey, clientKey, publicOnly)
		topo.Nodes[0].TelemetryProbes = []model.TelemetryProbe{{
			ID: "control", Name: "Controller reachability", Type: model.TelemetryProbeTCP,
			Host: "control.example", Port: 443,
		}}
		topo.Nodes[0].TelemetryDevices = &model.TelemetryDevicePolicy{Mode: "all-eligible-v1"}
		keys, err := GenerateKeys(topo, custody)
		if err != nil {
			t.Fatalf("GenerateKeys: %v", err)
		}
		result, err := compiler.NewCompiler().Compile(context.Background(), topo, keys)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		if err := All(result, keys, FetchSettings{}); err != nil {
			t.Fatalf("All: %v", err)
		}
		return result
	}

	agentHeld := renderWithCustody(t, AgentHeld, true)
	if len(agentHeld.TelemetryPolicyJSON) != 0 {
		t.Fatalf("successor render also emitted telemetry.json: %+v", agentHeld.TelemetryPolicyJSON)
	}
	const want = `{"version":2,"probes":[{"id":"control","type":"tcp","host":"control.example","port":443}],"devices":{"mode":"all-eligible-v1"}}`
	if got := agentHeld.TelemetrySuccessorPolicyJSON["router-1"]; got != want {
		t.Fatalf("AgentHeld successor telemetry policy = %q, want %q", got, want)
	}

	airGap := renderWithCustody(t, AirGap, false)
	if len(airGap.TelemetryPolicyJSON) != 0 || len(airGap.TelemetrySuccessorPolicyJSON) != 0 {
		t.Fatalf("AirGap render retained agent-only telemetry policy: v1=%+v v2=%+v",
			airGap.TelemetryPolicyJSON, airGap.TelemetrySuccessorPolicyJSON)
	}
}
