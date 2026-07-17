package agent

import (
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrycap"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
)

// agentCapabilitiesSampler reports compile-time executable support without reading the host. These
// tokens are also the launcher-owned install markers, so a capability is added only when its parser,
// runtime, and telemetry/chart contract are all production-registered.
type agentCapabilitiesSampler struct{}

var implementedSuccessorTelemetryCapabilities = []string{
	telemetrycap.DeviceV1,
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
