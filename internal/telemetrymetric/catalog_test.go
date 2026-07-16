package telemetrymetric

import (
	"reflect"
	"strings"
	"testing"
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
			case ChartFamilyResource, ChartFamilyProbe:
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
	if want := []string{ResourceKey, ProbeSamplesKey, ProbeResultsKey}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("charted order = %v, want %v", keys, want)
	}
	if got, want := ChartFamilies(), []ChartFamily{ChartFamilyResource, ChartFamilyProbe}; !reflect.DeepEqual(got, want) {
		t.Fatalf("chart families = %v, want %v", got, want)
	}
}

func TestLiveSurfacePolicyPreservesUnknownCompatibility(t *testing.T) {
	if VisibleOnLiveSurface(ProbeSamplesKey) {
		t.Fatal("probe_samples must be history-only on the latest/live surface")
	}
	for _, key := range []string{ResourceKey, ProbeResultsKey, WireGuardPeersKey, NativeXDPKey, MimicCapabilityKey, "future_metric"} {
		if !VisibleOnLiveSurface(key) {
			t.Errorf("metric %q must remain live-visible", key)
		}
	}
}
