package controller

import (
	"errors"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
)

// ErrTelemetryProbesRequireKeystone is returned before export, allocation persistence, or staging
// when a ready compiled node carries active outbound probes but the tenant has no pinned off-host
// operator credential. The agent repeats this requirement before activating telemetry.json.
var ErrTelemetryProbesRequireKeystone = errors.New("controller: active telemetry probes require a pinned keystone")

func requireTelemetryProbeKeystone(result *compiler.CompileResult, keystoneOn bool) error {
	if result == nil || result.Topology == nil || keystoneOn {
		return nil
	}
	for i := range result.Topology.Nodes {
		if len(result.Topology.Nodes[i].TelemetryProbes) > 0 {
			return ErrTelemetryProbesRequireKeystone
		}
	}
	return nil
}
