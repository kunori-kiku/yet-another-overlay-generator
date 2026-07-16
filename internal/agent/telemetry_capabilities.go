package agent

import (
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrycap"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
)

// agentCapabilitiesSampler reports compile-time executable support without reading the host. URL
// probes ship with the generic successor-policy parser/launcher; device support remains absent until
// its collector and charted metric contract are implemented.
type agentCapabilitiesSampler struct{}

var implementedSuccessorTelemetryCapabilities = []string{
	telemetrycap.PolicyV2,
	telemetrycap.URLV1,
}

func (agentCapabilitiesSampler) Name() string { return "agent-capabilities" }

func (agentCapabilitiesSampler) MetricDefinitions() []telemetrymetric.Definition {
	return []telemetrymetric.Definition{telemetrymetric.AgentCapabilities}
}

func (agentCapabilitiesSampler) Sample(time.Time) ([]runtimecontract.Condition, map[string]any) {
	capabilities := telemetrymetric.NormalizeAgentCapabilities(implementedSuccessorTelemetryCapabilities)
	return nil, map[string]any{
		telemetrymetric.AgentCapabilitiesKey: telemetrymetric.AgentCapabilitiesMetric{Capabilities: capabilities},
	}
}
