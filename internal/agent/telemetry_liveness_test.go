package agent

import (
	"errors"
	"sort"
	"testing"
	"time"
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
	want := []string{"active-probes", "conditions", "mimic-capability", "native-xdp", "resource", "wireguard-peers"}
	if len(got) != len(want) {
		t.Fatalf("BuildTelemetry samplers = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("BuildTelemetry samplers = %v, want %v", got, want)
		}
	}
}
