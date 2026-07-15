package artifacts

import (
	"errors"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestExport_UnsafeNodeID_CodedError proves the coded path at the source: a node ID
// that would enable path traversal makes Export return a coded *apierr.Error (CodeExportUnsafeName,
// HTTP 400) carrying {name} — so the handler relay surfaces a localizable 400, not a raw 500.
func TestExport_UnsafeNodeID_CodedError(t *testing.T) {
	result := &compiler.CompileResult{
		Topology: &model.Topology{
			Project: model.Project{ID: "unsafe-001", Name: "Unsafe", Version: "0.1.0"},
			Domains: []model.Domain{{ID: "d1", Name: "d", CIDR: "10.10.0.0/24", RoutingMode: "babel"}},
			Nodes:   []model.Node{{ID: "../escape", Name: "safe-name", OverlayIP: "10.10.0.1", Role: "peer", DomainID: "d1"}},
		},
		Manifest: compiler.CompileManifest{ProjectID: "unsafe-001", CompiledAt: time.Now()},
	}

	_, err := Export(result, t.TempDir())
	if err == nil {
		t.Fatal("expected Export to reject an unsafe node name, got nil")
	}
	if !apierr.HasCode(err, apierr.CodeExportUnsafeName) {
		t.Fatalf("expected CodeExportUnsafeName, got: %v", err)
	}
	var ae *apierr.Error
	if !errors.As(err, &ae) {
		t.Fatalf("error is not an *apierr.Error: %v", err)
	}
	if ae.Status() != 400 {
		t.Errorf("status = %d, want 400", ae.Status())
	}
	if ae.Params()["name"] != "../escape" {
		t.Errorf("params[name] = %q, want %q", ae.Params()["name"], "../escape")
	}
}
