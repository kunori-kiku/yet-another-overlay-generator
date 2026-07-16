package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
)

func telemetryDeviceTopology(mode, deploymentMode string) *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "p", Name: "project"},
		Domains: []model.Domain{{
			ID: "d", Name: "domain", CIDR: "10.0.0.0/24", AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{{
			ID: "n", Name: "node", DomainID: "d", Role: "peer", DeploymentMode: deploymentMode,
			TelemetryDevices: &model.TelemetryDevicePolicy{Mode: mode},
		}},
	}
}

func TestValidateSchema_TelemetryDevicesUseCanonicalManagedMode(t *testing.T) {
	valid := telemetryDeviceTopology(string(probepolicy.DeviceModeAllEligibleV1), model.DeploymentManaged)
	if result := ValidateSchema(valid); hasCode(result, CodeNodeTelemetryDevicesInvalid) {
		t.Fatalf("managed automatic device telemetry rejected: %+v", result.Errors)
	}

	for name, topo := range map[string]*model.Topology{
		"unknown mode": telemetryDeviceTopology("future-mode", model.DeploymentManaged),
		"manual node":  telemetryDeviceTopology(string(probepolicy.DeviceModeAllEligibleV1), model.DeploymentManual),
	} {
		t.Run(name, func(t *testing.T) {
			result := ValidateSchema(topo)
			if !hasCode(result, CodeNodeTelemetryDevicesInvalid) {
				t.Fatalf("invalid automatic device telemetry accepted: %+v", result.Errors)
			}
			found := false
			for _, finding := range result.Errors {
				if finding.Code == string(CodeNodeTelemetryDevicesInvalid) {
					found = true
					if finding.Field != "nodes[0].telemetry_devices" {
						t.Fatalf("validation field = %q, want nodes[0].telemetry_devices", finding.Field)
					}
				}
			}
			if !found {
				t.Fatal("device validation finding disappeared")
			}
		})
	}
}
