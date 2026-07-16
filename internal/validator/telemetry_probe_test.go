package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

func telemetryProbeTopology(probe model.TelemetryProbe, deploymentMode string) *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "p", Name: "project"},
		Domains: []model.Domain{{
			ID: "d", Name: "domain", CIDR: "10.0.0.0/24", AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{{
			ID: "n", Name: "node", DomainID: "d", Role: "peer", DeploymentMode: deploymentMode,
			TelemetryProbes: []model.TelemetryProbe{probe},
		}},
	}
}

func TestValidateSchema_TelemetryProbeManualDestination(t *testing.T) {
	valid := model.TelemetryProbe{ID: "dns-tls", Type: model.TelemetryProbeTCP, Host: "service.example", Port: 443}
	if result := ValidateSchema(telemetryProbeTopology(valid, model.DeploymentManaged)); hasCode(result, CodeNodeTelemetryProbesInvalid) {
		t.Fatalf("managed DNS-host probe rejected: %+v", result.Errors)
	}
	if result := ValidateSchema(telemetryProbeTopology(valid, model.DeploymentManual)); !hasCode(result, CodeNodeTelemetryProbesInvalid) {
		t.Fatalf("manual source node must reject active probes: %+v", result.Errors)
	}
	url := model.TelemetryProbe{ID: "future-url", Type: model.TelemetryProbeTCP, Host: "https://service.example/health", Port: 443}
	if result := ValidateSchema(telemetryProbeTopology(url, model.DeploymentManaged)); !hasCode(result, CodeNodeTelemetryProbesInvalid) {
		t.Fatalf("URL syntax must remain outside the host contract: %+v", result.Errors)
	}
}
