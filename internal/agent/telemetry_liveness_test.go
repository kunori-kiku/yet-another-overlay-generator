package agent

import (
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
)

// TestSamplerFreshness_ReMeasures enforces the plan-1.5 FRESHNESS CONTRACT for the injectable samplers:
// mutating the underlying system input MUST change the emitted signal. A sampler that cached a
// deploy-time value would emit the SAME output for both inputs and fail here — catching the frozen-signal
// class (the mimic-breadcrumb / selfupdate defects) structurally instead of on live-fleet smoke. It can
// only cover samplers with an injectable source; a new re-measuring sampler SHOULD be added here.
func TestSamplerFreshness_ReMeasures(t *testing.T) {
	origLoad, origStat := loadavgFn, statFn
	defer func() { loadavgFn, statFn = origLoad, origStat }()
	statFn = func() ([]byte, error) { return nil, errors.New("skip cpu") } // isolate the load reading

	// resourceSampler must re-read /proc/loadavg every Sample — two different reads → two different Load1.
	load1Of := func(raw string) float64 {
		loadavgFn = func() ([]byte, error) { return []byte(raw), nil }
		_, m := (&resourceSampler{}).Sample(time.Now())
		return m[resourceMetricKey].(hostResource).Load1
	}
	a, b := load1Of("1.00 0 0\n"), load1Of("9.00 0 0\n")
	if a != 1.0 || b != 9.0 {
		t.Fatalf("resourceSampler must re-measure loadavg live: got %v then %v (want 1.0 then 9.0)", a, b)
	}
}

// TestBuildTelemetry_RegistrationSet is the parity tripwire: BuildTelemetry must register EXACTLY the
// expected sampler set, so adding a new monitoring signal forces a conscious update here — turning "did
// you wire it into the (now unified) heartbeat path" from tribal knowledge into a failing test.
// Necessary but not sufficient for freshness (see TestSamplerFreshness_ReMeasures); it guarantees a
// signal is at least on the single unified path rather than an apply-only side channel.
func TestBuildTelemetry_RegistrationSet(t *testing.T) {
	tel := BuildTelemetry(t.TempDir())
	got := make([]string, 0, len(tel.samplers))
	for _, s := range tel.samplers {
		got = append(got, s.Name())
	}
	sort.Strings(got)
	want := []string{"active-probes", "agent-capabilities", "automatic-devices", "conditions", "mimic-capability", "native-xdp", "resource", "wireguard-peers"}
	if len(got) != len(want) {
		t.Fatalf("BuildTelemetry samplers = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("BuildTelemetry samplers = %v, want %v", got, want)
		}
	}
}

func TestBuildTelemetry_MetricDefinitionsAreCompleteUniqueAndAuthoritative(t *testing.T) {
	tel := BuildTelemetry(t.TempDir())
	if err := validateProductionMetricDefinitions(tel.samplers); err != nil {
		t.Fatalf("production metric declarations: %v", err)
	}
	owners := make(map[string]string)
	for _, sampler := range tel.samplers {
		for _, definition := range sampler.MetricDefinitions() {
			if owner, duplicate := owners[definition.Key]; duplicate {
				t.Fatalf("metric %q declared by %q and %q", definition.Key, owner, sampler.Name())
			}
			owners[definition.Key] = sampler.Name()
		}
	}
	for _, definition := range telemetrymetric.All() {
		if owners[definition.Key] == "" {
			t.Fatalf("catalog metric %q has no production sampler", definition.Key)
		}
		if definition.History == telemetrymetric.HistoryLiveOnly && definition.LiveOnlyReason == "" {
			t.Fatalf("live-only metric %q has no rationale", definition.Key)
		}
	}
	if len(owners) != len(telemetrymetric.All()) {
		t.Fatalf("declared metric owners = %v, catalog = %+v", owners, telemetrymetric.All())
	}
}

func TestValidateProductionMetricDefinitions_RejectsRegistryDrift(t *testing.T) {
	all := telemetrymetric.All()
	altered := append([]telemetrymetric.Definition(nil), all...)
	altered[0].LiveSurface = telemetrymetric.LiveSurfaceHistoryOnly
	tests := []struct {
		name     string
		samplers []Sampler
		want     string
	}{
		{name: "missing catalog metric", samplers: []Sampler{fakeSampler{name: "partial", defs: all[:len(all)-1]}}, want: "has no production sampler"},
		{name: "duplicate owner", samplers: []Sampler{fakeSampler{name: "all", defs: all}, fakeSampler{name: "duplicate", defs: []telemetrymetric.Definition{all[0]}}}, want: "declared by both"},
		{name: "changed disposition", samplers: []Sampler{fakeSampler{name: "altered", defs: altered}}, want: "changes catalog definition"},
		{name: "unknown key", samplers: []Sampler{fakeSampler{name: "unknown", defs: append(append([]telemetrymetric.Definition(nil), all...), testMetricDefinition("not-in-catalog"))}}, want: "unknown metric"},
		{name: "charted missing family", samplers: []Sampler{fakeSampler{name: "invalid", defs: []telemetrymetric.Definition{{Key: "broken", History: telemetrymetric.HistoryCharted, HistoryPriority: 1, LiveSurface: telemetrymetric.LiveSurfaceVisible}}}}, want: "invalid chart family"},
		{name: "charted non-positive priority", samplers: []Sampler{fakeSampler{name: "invalid", defs: []telemetrymetric.Definition{{Key: "broken", History: telemetrymetric.HistoryCharted, ChartFamily: telemetrymetric.ChartFamilyProbe, LiveSurface: telemetrymetric.LiveSurfaceVisible}}}}, want: "non-positive history priority"},
		{name: "charted invalid live surface", samplers: []Sampler{fakeSampler{name: "invalid", defs: []telemetrymetric.Definition{{Key: "broken", History: telemetrymetric.HistoryCharted, ChartFamily: telemetrymetric.ChartFamilyProbe, HistoryPriority: 1}}}}, want: "invalid live-surface disposition"},
		{name: "live-only declares chart fields", samplers: []Sampler{fakeSampler{name: "invalid", defs: []telemetrymetric.Definition{{Key: "broken", History: telemetrymetric.HistoryLiveOnly, ChartFamily: telemetrymetric.ChartFamilyProbe, HistoryPriority: 1, LiveSurface: telemetrymetric.LiveSurfaceVisible, LiveOnlyReason: "not chartable"}}}}, want: "declares chart family/priority"},
		{name: "live-only missing reason", samplers: []Sampler{fakeSampler{name: "invalid", defs: []telemetrymetric.Definition{{Key: "broken", History: telemetrymetric.HistoryLiveOnly, LiveSurface: telemetrymetric.LiveSurfaceVisible}}}}, want: "has no reason"},
		{name: "live-only hidden from live surface", samplers: []Sampler{fakeSampler{name: "invalid", defs: []telemetrymetric.Definition{{Key: "broken", History: telemetrymetric.HistoryLiveOnly, LiveSurface: telemetrymetric.LiveSurfaceHistoryOnly, LiveOnlyReason: "not chartable"}}}}, want: "not retained as charted history"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateProductionMetricDefinitions(tt.samplers)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateProductionMetricDefinitions() = %v, want error containing %q", err, tt.want)
			}
		})
	}
}
