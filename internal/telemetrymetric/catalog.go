// Package telemetrymetric is the leaf catalog for agent telemetry metric keys and their history
// disposition. Adding a metric is deliberately not just a wire-format decision: every metric must
// declare its retained chart family (or why it is intentionally live-only) and whether its latest
// payload belongs on the Fleet live surface. Controller/API registries consume the same catalog, so a
// chartable metric cannot silently stop before retention or encoding.
package telemetrymetric

import (
	"fmt"
	"regexp"
	"sort"
)

type HistoryDisposition string

const (
	HistoryCharted  HistoryDisposition = "charted"
	HistoryLiveOnly HistoryDisposition = "live-only"
)

// ChartFamily is the closed backend history-output family. Multiple wire metrics may feed one
// family: probe_samples is the high-fidelity source and probe_results is its backward-compatible
// fallback, but both become the same probe history/API shape.
type ChartFamily string

const (
	ChartFamilyResource ChartFamily = "resource"
	ChartFamilyProbe    ChartFamily = "probe"
	ChartFamilyDevice   ChartFamily = "device"
)

// LiveSurfaceDisposition declares whether the latest opaque metric is appropriate for Node.Telemetry
// and the operator Fleet `/nodes` response. History-only source windows remain available to history
// projectors but are not echoed on every live refresh. Unknown keys deliberately remain live-visible
// for forward compatibility with a newer agent and an older controller.
type LiveSurfaceDisposition string

const (
	LiveSurfaceVisible     LiveSurfaceDisposition = "visible"
	LiveSurfaceHistoryOnly LiveSurfaceDisposition = "history-only"
)

// Definition describes one top-level entry in telemetry.metrics. Charted metrics require a family,
// a unique positive HistoryPriority, and a controller projector. HistoryPriority is executable
// ordering: lower values project first, so richer sources win exact-deduplication over fallbacks.
// Live-only metrics require a non-empty reason documenting why a time series would be misleading.
type Definition struct {
	Key             string
	History         HistoryDisposition
	ChartFamily     ChartFamily
	HistoryPriority int
	LiveSurface     LiveSurfaceDisposition
	LiveOnlyReason  string
}

const (
	ProbeResultsKey      = "probe_results"
	ProbeSamplesKey      = "probe_samples"
	ResourceKey          = "resource"
	WireGuardPeersKey    = "wireguard_peers"
	NativeXDPKey         = "native_xdp"
	MimicCapabilityKey   = "mimic_capability"
	AgentCapabilitiesKey = "agent_capabilities"
	DeviceInventoryKey   = "device_inventory"
	DeviceSamplesKey     = "device_samples"
	MaxAgentCapabilities = 16
)

var agentCapabilityPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type AgentCapabilitiesMetric struct {
	Capabilities []string `json:"capabilities"`
}

// NormalizeAgentCapabilities produces the bounded canonical wire set used by the agent. Invalid
// tokens are omitted; duplicates collapse; the retained values are sorted and capped.
func NormalizeAgentCapabilities(input []string) []string {
	seen := make(map[string]struct{}, len(input))
	capabilities := make([]string, 0, len(input))
	for _, capability := range input {
		if !agentCapabilityPattern.MatchString(capability) {
			continue
		}
		if _, duplicate := seen[capability]; duplicate {
			continue
		}
		seen[capability] = struct{}{}
		capabilities = append(capabilities, capability)
	}
	sort.Strings(capabilities)
	if len(capabilities) > MaxAgentCapabilities {
		capabilities = capabilities[:MaxAgentCapabilities]
	}
	return capabilities
}

// ValidateAgentCapabilities enforces the exact canonical latest-heartbeat form. A malformed metric
// is not readiness evidence; callers should treat validation failure as "not confirmed".
func ValidateAgentCapabilities(capabilities []string) error {
	if capabilities == nil {
		return fmt.Errorf("agent capabilities field is missing or null")
	}
	if len(capabilities) > MaxAgentCapabilities {
		return fmt.Errorf("agent capabilities exceed %d entries", MaxAgentCapabilities)
	}
	for i, capability := range capabilities {
		if !agentCapabilityPattern.MatchString(capability) {
			return fmt.Errorf("agent capability %d is invalid", i)
		}
		if i > 0 && capabilities[i-1] >= capability {
			return fmt.Errorf("agent capabilities are not sorted unique")
		}
	}
	return nil
}

var (
	ProbeResults = Definition{
		Key: ProbeResultsKey, History: HistoryCharted,
		ChartFamily: ChartFamilyProbe, HistoryPriority: 30,
		LiveSurface: LiveSurfaceVisible,
	}
	ProbeSamples = Definition{
		Key: ProbeSamplesKey, History: HistoryCharted,
		ChartFamily: ChartFamilyProbe, HistoryPriority: 20,
		LiveSurface: LiveSurfaceHistoryOnly,
	}
	Resource = Definition{
		Key: ResourceKey, History: HistoryCharted,
		ChartFamily: ChartFamilyResource, HistoryPriority: 10,
		LiveSurface: LiveSurfaceVisible,
	}
	WireGuardPeers = Definition{
		Key: WireGuardPeersKey, History: HistoryLiveOnly, LiveSurface: LiveSurfaceVisible,
		LiveOnlyReason: "peer endpoint and handshake state are current operational state, not a retained chart metric",
	}
	NativeXDP = Definition{
		Key: NativeXDPKey, History: HistoryLiveOnly, LiveSurface: LiveSurfaceVisible,
		LiveOnlyReason: "capability is a point-in-time deployment advisory rather than a sampled numeric signal",
	}
	MimicCapability = Definition{
		Key: MimicCapabilityKey, History: HistoryLiveOnly, LiveSurface: LiveSurfaceVisible,
		LiveOnlyReason: "capability is a point-in-time deployment advisory rather than a sampled numeric signal",
	}
	AgentCapabilities = Definition{
		Key: AgentCapabilitiesKey, History: HistoryLiveOnly, LiveSurface: LiveSurfaceVisible,
		LiveOnlyReason: "executable compatibility is current readiness, not a time-series measurement",
	}
	DeviceInventory = Definition{
		Key: DeviceInventoryKey, History: HistoryLiveOnly, LiveSurface: LiveSurfaceVisible,
		LiveOnlyReason: "device identity, support, mount, and truncation state are current categorical inventory rather than numeric time series",
	}
	DeviceSamples = Definition{
		Key: DeviceSamplesKey, History: HistoryCharted,
		ChartFamily: ChartFamilyDevice, HistoryPriority: 40,
		LiveSurface: LiveSurfaceVisible,
	}
)

var catalogDefinitions = []Definition{
	ProbeResults,
	ProbeSamples,
	Resource,
	WireGuardPeers,
	NativeXDP,
	MimicCapability,
	AgentCapabilities,
	DeviceInventory,
	DeviceSamples,
}

var orderedChartedDefinitions = func() []Definition {
	var definitions []Definition
	for _, definition := range catalogDefinitions {
		if definition.History == HistoryCharted {
			definitions = append(definitions, definition)
		}
	}
	sort.Slice(definitions, func(i, j int) bool {
		if definitions[i].HistoryPriority == definitions[j].HistoryPriority {
			return definitions[i].Key < definitions[j].Key
		}
		return definitions[i].HistoryPriority < definitions[j].HistoryPriority
	})
	return definitions
}()

var orderedChartFamilies = func() []ChartFamily {
	seen := make(map[ChartFamily]struct{})
	var families []ChartFamily
	for _, definition := range orderedChartedDefinitions {
		if _, ok := seen[definition.ChartFamily]; ok {
			continue
		}
		seen[definition.ChartFamily] = struct{}{}
		families = append(families, definition.ChartFamily)
	}
	return families
}()

// All returns a copy of the complete catalog. Keep the underlying list explicit so tests can detect
// duplicate keys and verify every charted/live-surface declaration across producer and controller.
func All() []Definition {
	return append([]Definition(nil), catalogDefinitions...)
}

// ValidateDefinition is the single semantic validator for catalog and sampler declarations. Agent
// registration checks consume it too, so family/priority/live-surface rules cannot drift between the
// producer and controller packages.
func ValidateDefinition(definition Definition) error {
	if definition.Key == "" {
		return fmt.Errorf("metric key is empty")
	}
	switch definition.History {
	case HistoryCharted:
		switch definition.ChartFamily {
		case ChartFamilyResource, ChartFamilyProbe, ChartFamilyDevice:
		default:
			return fmt.Errorf("charted metric %q has invalid chart family %q", definition.Key, definition.ChartFamily)
		}
		if definition.HistoryPriority <= 0 {
			return fmt.Errorf("charted metric %q has non-positive history priority %d", definition.Key, definition.HistoryPriority)
		}
		if definition.LiveOnlyReason != "" {
			return fmt.Errorf("charted metric %q has a live-only reason", definition.Key)
		}
	case HistoryLiveOnly:
		if definition.ChartFamily != "" || definition.HistoryPriority != 0 {
			return fmt.Errorf("live-only metric %q declares chart family/priority %q/%d", definition.Key, definition.ChartFamily, definition.HistoryPriority)
		}
		if definition.LiveOnlyReason == "" {
			return fmt.Errorf("live-only metric %q has no reason", definition.Key)
		}
	default:
		return fmt.Errorf("metric %q has invalid history disposition %q", definition.Key, definition.History)
	}
	switch definition.LiveSurface {
	case LiveSurfaceVisible:
	case LiveSurfaceHistoryOnly:
		if definition.History != HistoryCharted {
			return fmt.Errorf("history-only metric %q is not retained as charted history", definition.Key)
		}
	default:
		return fmt.Errorf("metric %q has invalid live-surface disposition %q", definition.Key, definition.LiveSurface)
	}
	return nil
}

// ValidateCatalog applies definition validation plus whole-catalog uniqueness constraints.
func ValidateCatalog(definitions []Definition) error {
	keys := make(map[string]struct{}, len(definitions))
	priorities := make(map[int]string)
	for _, definition := range definitions {
		if err := ValidateDefinition(definition); err != nil {
			return err
		}
		if _, duplicate := keys[definition.Key]; duplicate {
			return fmt.Errorf("catalog duplicates metric key %q", definition.Key)
		}
		keys[definition.Key] = struct{}{}
		if definition.History == HistoryCharted {
			if other, duplicate := priorities[definition.HistoryPriority]; duplicate {
				return fmt.Errorf("charted metrics %q and %q share history priority %d", other, definition.Key, definition.HistoryPriority)
			}
			priorities[definition.HistoryPriority] = definition.Key
		}
	}
	return nil
}

// Charted returns a priority-ordered copy of every metric that must reach retained history. Runtime
// projection consumes this list directly; there is no second hand-maintained metric-key order.
func Charted() []Definition {
	return append([]Definition(nil), orderedChartedDefinitions...)
}

// ChartFamilies returns each charted family once, ordered by the first source that feeds it. The API
// encoder registry consumes this list, so a new backend family cannot be retained but never encoded.
func ChartFamilies() []ChartFamily {
	return append([]ChartFamily(nil), orderedChartFamilies...)
}

// VisibleOnLiveSurface applies the known-key policy. Unknown metrics remain visible deliberately:
// otherwise deploying a newer agent before its controller would silently erase new live telemetry.
func VisibleOnLiveSurface(key string) bool {
	for _, definition := range catalogDefinitions {
		if definition.Key == key {
			return definition.LiveSurface == LiveSurfaceVisible
		}
	}
	return true
}
