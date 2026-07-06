package agent

import (
	"os"
	"testing"
	"time"
)

func TestClassifyMimicCapability(t *testing.T) {
	cases := []struct {
		loaded, built, headers bool
		want                   string
	}{
		{true, false, false, "ready"},        // module loaded
		{false, true, false, "ready"},        // built for this kernel
		{true, true, true, "ready"},          // loaded wins
		{false, false, true, "buildable"},    // headers present, not yet built
		{false, false, false, "unbuildable"}, // stale kernel: not built, no headers (the hkg14 case)
	}
	for _, tc := range cases {
		if got := classifyMimicCapability(tc.loaded, tc.built, tc.headers); got != tc.want {
			t.Errorf("classifyMimicCapability(%v,%v,%v) = %q, want %q", tc.loaded, tc.built, tc.headers, got, tc.want)
		}
	}
}

func TestMimicModuleLoaded(t *testing.T) {
	// /proc/modules lines are "<name> <size> <refcount> <deps> <state> <addr>".
	loaded := "nf_tables 307200 1 - Live 0x0\nmimic 45056 0 - Live 0x0\n"
	if !mimicModuleLoaded([]byte(loaded)) {
		t.Errorf("mimicModuleLoaded should find the mimic line")
	}
	// Must match the whole module name, not a prefix (mimic_helper != mimic).
	if mimicModuleLoaded([]byte("nf_tables 307200 1 - Live 0x0\nmimic_helper 4096 0 - Live 0x0\n")) {
		t.Errorf("mimicModuleLoaded must not match a prefix (mimic_helper)")
	}
}

func TestMimicCapabilitySampler_Sample(t *testing.T) {
	origK, origP, origB, origH := kernelReleaseFn, procModulesFn, moduleBuiltFn, headersPresentFn
	defer func() { kernelReleaseFn, procModulesFn, moduleBuiltFn, headersPresentFn = origK, origP, origB, origH }()

	// Stale-kernel node (the fleet case): module not loaded, not built, no headers → unbuildable.
	kernelReleaseFn = func() ([]byte, error) { return []byte("6.1.0-13-cloud-amd64\n"), nil }
	procModulesFn = func() ([]byte, error) { return []byte(""), nil }
	moduleBuiltFn = func(string) bool { return false }
	headersPresentFn = func(string) bool { return false }

	s := mimicCapabilitySampler{}
	conds, metrics := s.Sample(time.Time{})
	if conds != nil {
		t.Errorf("sampler emits a metric, not conditions")
	}
	got, ok := metrics[mimicCapabilityMetricKey].(mimicCapability)
	if !ok {
		t.Fatalf("metric %q missing/wrong type", mimicCapabilityMetricKey)
	}
	if got.Capability != "unbuildable" || got.Kernel != "6.1.0-13-cloud-amd64" {
		t.Errorf("stale-kernel node = %+v, want {unbuildable, 6.1.0-13-cloud-amd64}", got)
	}

	// A non-Linux host (no osrelease) contributes nothing.
	kernelReleaseFn = func() ([]byte, error) { return nil, os.ErrNotExist }
	if _, m := s.Sample(time.Time{}); m != nil {
		t.Errorf("no kernel → no metric, got %v", m)
	}
}
