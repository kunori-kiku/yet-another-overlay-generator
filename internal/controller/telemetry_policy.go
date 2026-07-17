package controller

import (
	"errors"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
)

// ErrTelemetryProbesRequireKeystone is returned before export, allocation persistence, or staging
// when a ready compiled node carries active telemetry authority but the tenant has no pinned off-host
// operator credential. The historical name remains part of the error contract; it now also covers
// successor-only system-observation policy such as automatic device telemetry.
var ErrTelemetryProbesRequireKeystone = errors.New("controller: active telemetry policy requires a pinned keystone")

func requireTelemetryProbeKeystone(result *compiler.CompileResult, keystoneOn bool) error {
	if result == nil || result.Topology == nil || keystoneOn {
		return nil
	}
	for i := range result.Topology.Nodes {
		if len(result.Topology.Nodes[i].TelemetryProbes) > 0 || probepolicy.RequiresSuccessor(result.Topology.Nodes[i]) {
			return ErrTelemetryProbesRequireKeystone
		}
	}
	return nil
}
