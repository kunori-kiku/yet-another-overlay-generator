package validator

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// deploymentModeTopo is a minimal single-node topology whose only schema-relevant variable is the
// node's deployment_mode (id/name/domain/role are all valid), so the test isolates the new enum check.
func deploymentModeTopo(mode string) *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "p1", Name: "p"},
		Domains: []model.Domain{{ID: "d1", Name: "net", CIDR: "10.80.0.0/24", AllocationMode: "auto", RoutingMode: "babel"}},
		Nodes: []model.Node{{
			ID: "n1", Name: "alpha", Role: "router", DomainID: "d1", DeploymentMode: mode,
		}},
	}
}

// TestValidate_DeploymentModeEnum pins the schema enum: "" (managed default) / "managed" / "manual"
// are accepted; a typo is rejected with CodeNodeDeploymentModeInvalid (not silently treated as managed,
// since the value changes custody/admission behavior).
func TestValidate_DeploymentModeEnum(t *testing.T) {
	for _, v := range []string{"", "managed", "manual"} {
		if hasCode(ValidateSchema(deploymentModeTopo(v)), CodeNodeDeploymentModeInvalid) {
			t.Errorf("deployment_mode=%q should be accepted", v)
		}
	}
	for _, v := range []string{"manged", "Manual", "agent", "local"} {
		if !hasCode(ValidateSchema(deploymentModeTopo(v)), CodeNodeDeploymentModeInvalid) {
			t.Errorf("deployment_mode=%q should be rejected with CodeNodeDeploymentModeInvalid", v)
		}
	}
}
