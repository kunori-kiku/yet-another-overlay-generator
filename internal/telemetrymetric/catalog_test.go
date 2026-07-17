package telemetrymetric

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrycap"
)

func TestCatalogDefinitionsAreUniqueAndExplicit(t *testing.T) {
	if err := ValidateCatalog(All()); err != nil {
		t.Fatalf("catalog validation: %v", err)
	}
	seen := make(map[string]struct{})
	priorities := make(map[int]string)
	for _, definition := range All() {
		if definition.Key == "" {
			t.Fatal("telemetry metric definition has an empty key")
		}
		if _, duplicate := seen[definition.Key]; duplicate {
			t.Fatalf("telemetry metric key %q is declared more than once", definition.Key)
		}
		seen[definition.Key] = struct{}{}
		switch definition.History {
		case HistoryCharted:
			switch definition.ChartFamily {
			case ChartFamilyResource, ChartFamilyProbe, ChartFamilyDevice:
			default:
				t.Fatalf("charted metric %q has invalid chart family %q", definition.Key, definition.ChartFamily)
			}
			if definition.HistoryPriority <= 0 {
				t.Fatalf("charted metric %q has non-positive history priority %d", definition.Key, definition.HistoryPriority)
			}
			if other, duplicate := priorities[definition.HistoryPriority]; duplicate {
				t.Fatalf("charted metrics %q and %q share history priority %d", other, definition.Key, definition.HistoryPriority)
			}
			priorities[definition.HistoryPriority] = definition.Key
			if definition.LiveOnlyReason != "" {
				t.Fatalf("charted metric %q has a live-only reason", definition.Key)
			}
		case HistoryLiveOnly:
			if definition.ChartFamily != "" || definition.HistoryPriority != 0 {
				t.Fatalf("live-only metric %q declares chart family/priority %q/%d", definition.Key, definition.ChartFamily, definition.HistoryPriority)
			}
			if definition.LiveOnlyReason == "" {
				t.Fatalf("live-only metric %q must document why it is not charted", definition.Key)
			}
		default:
			t.Fatalf("metric %q has invalid history disposition %q", definition.Key, definition.History)
		}
		switch definition.LiveSurface {
		case LiveSurfaceVisible:
		case LiveSurfaceHistoryOnly:
			if definition.History != HistoryCharted {
				t.Fatalf("history-only metric %q is not retained as charted history", definition.Key)
			}
		default:
			t.Fatalf("metric %q has invalid live-surface disposition %q", definition.Key, definition.LiveSurface)
		}
	}
}

func TestValidateDefinitionRejectsIncompleteFrameworkDeclarations(t *testing.T) {
	validCharted := Definition{
		Key: "test", History: HistoryCharted, ChartFamily: ChartFamilyResource,
		HistoryPriority: 1, LiveSurface: LiveSurfaceVisible,
	}
	tests := []struct {
		name       string
		definition Definition
		want       string
	}{
		{name: "empty key", definition: Definition{}, want: "key is empty"},
		{name: "unknown family", definition: Definition{Key: "x", History: HistoryCharted, ChartFamily: "future", HistoryPriority: 1, LiveSurface: LiveSurfaceVisible}, want: "invalid chart family"},
		{name: "missing priority", definition: Definition{Key: "x", History: HistoryCharted, ChartFamily: ChartFamilyResource, LiveSurface: LiveSurfaceVisible}, want: "non-positive history priority"},
		{name: "charted rationale", definition: Definition{Key: "x", History: HistoryCharted, ChartFamily: ChartFamilyResource, HistoryPriority: 1, LiveSurface: LiveSurfaceVisible, LiveOnlyReason: "wrong"}, want: "live-only reason"},
		{name: "live-only chart metadata", definition: Definition{Key: "x", History: HistoryLiveOnly, ChartFamily: ChartFamilyProbe, HistoryPriority: 1, LiveSurface: LiveSurfaceVisible, LiveOnlyReason: "state"}, want: "declares chart family/priority"},
		{name: "live-only rationale", definition: Definition{Key: "x", History: HistoryLiveOnly, LiveSurface: LiveSurfaceVisible}, want: "has no reason"},
		{name: "missing live disposition", definition: Definition{Key: "x", History: HistoryLiveOnly, LiveOnlyReason: "state"}, want: "invalid live-surface disposition"},
		{name: "unretained history-only", definition: Definition{Key: "x", History: HistoryLiveOnly, LiveSurface: LiveSurfaceHistoryOnly, LiveOnlyReason: "state"}, want: "not retained"},
	}
	if err := ValidateDefinition(validCharted); err != nil {
		t.Fatalf("valid charted definition: %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDefinition(tt.definition)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateDefinition(%+v) = %v, want error containing %q", tt.definition, err, tt.want)
			}
		})
	}
}

func TestChartedOrderAndFamilies(t *testing.T) {
	charted := Charted()
	keys := make([]string, len(charted))
	for i, definition := range charted {
		keys[i] = definition.Key
	}
	if want := []string{ResourceKey, ProbeSamplesKey, ProbeResultsKey, DeviceSamplesKey}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("charted order = %v, want %v", keys, want)
	}
	if got, want := ChartFamilies(), []ChartFamily{ChartFamilyResource, ChartFamilyProbe, ChartFamilyDevice}; !reflect.DeepEqual(got, want) {
		t.Fatalf("chart families = %v, want %v", got, want)
	}
}

func TestLiveSurfacePolicyPreservesUnknownCompatibility(t *testing.T) {
	if VisibleOnLiveSurface(ProbeSamplesKey) {
		t.Fatal("probe_samples must be history-only on the latest/live surface")
	}
	for _, key := range []string{ResourceKey, ProbeResultsKey, DeviceInventoryKey, DeviceSamplesKey, WireGuardPeersKey, NativeXDPKey, MimicCapabilityKey, AgentCapabilitiesKey, "future_metric"} {
		if !VisibleOnLiveSurface(key) {
			t.Errorf("metric %q must remain live-visible", key)
		}
	}
}

func TestDeviceMetricCatalogSplitIsExplicit(t *testing.T) {
	if DeviceInventory.History != HistoryLiveOnly || DeviceInventory.ChartFamily != "" ||
		DeviceInventory.LiveSurface != LiveSurfaceVisible || DeviceInventory.LiveOnlyReason == "" {
		t.Fatalf("device inventory definition = %+v", DeviceInventory)
	}
	if DeviceSamples.History != HistoryCharted || DeviceSamples.ChartFamily != ChartFamilyDevice ||
		DeviceSamples.HistoryPriority != 40 || DeviceSamples.LiveSurface != LiveSurfaceVisible {
		t.Fatalf("device samples definition = %+v", DeviceSamples)
	}
}

func TestAgentCapabilities_NormalizeValidateAndCatalog(t *testing.T) {
	got := NormalizeAgentCapabilities([]string{
		telemetrycap.PolicyV2,
		"Bad_Capability",
		telemetrycap.DeviceV1,
		telemetrycap.PolicyV2,
		"",
	})
	want := []string{
		telemetrycap.DeviceV1,
		telemetrycap.PolicyV2,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeAgentCapabilities = %v, want %v", got, want)
	}
	if err := ValidateAgentCapabilities(got); err != nil {
		t.Fatalf("ValidateAgentCapabilities(canonical): %v", err)
	}

	many := make([]string, MaxAgentCapabilities+4)
	for i := range many {
		many[i] = fmt.Sprintf("cap-%02d", i)
	}
	bounded := NormalizeAgentCapabilities(many)
	if len(bounded) != MaxAgentCapabilities {
		t.Fatalf("normalized capability count = %d, want cap %d", len(bounded), MaxAgentCapabilities)
	}
	if err := ValidateAgentCapabilities(bounded); err != nil {
		t.Fatalf("ValidateAgentCapabilities(bounded): %v", err)
	}
	for name, invalid := range map[string][]string{
		"missing":   nil,
		"unsorted":  {"z-cap", "a-cap"},
		"duplicate": {"a-cap", "a-cap"},
		"invalid":   {"Bad_Capability"},
		"too many":  many,
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateAgentCapabilities(invalid); err == nil {
				t.Fatalf("ValidateAgentCapabilities accepted %v", invalid)
			}
		})
	}

	found := false
	for _, definition := range All() {
		if definition.Key != AgentCapabilitiesKey {
			continue
		}
		found = true
		if definition.History != HistoryLiveOnly || definition.LiveSurface != LiveSurfaceVisible || definition.LiveOnlyReason == "" {
			t.Fatalf("agent capability catalog definition = %+v", definition)
		}
	}
	if !found {
		t.Fatal("agent capability metric is absent from the shared catalog")
	}
}
