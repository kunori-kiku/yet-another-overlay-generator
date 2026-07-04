package agent

import (
	"errors"
	"testing"
	"time"
)

func TestClassifyNativeXDP(t *testing.T) {
	cases := map[string]string{
		"ena": "supported", "mlx5_core": "supported", "i40e": "supported", "veth": "supported",
		"e1000": "unsupported", "r8169": "unsupported", "8139too": "unsupported",
		"virtio_net":      "conditional",
		"some_new_driver": "unknown", "": "unknown",
	}
	for driver, want := range cases {
		if got := classifyNativeXDP(driver); got != want {
			t.Errorf("classifyNativeXDP(%q) = %q, want %q", driver, got, want)
		}
	}
}

func TestDefaultRouteIface(t *testing.T) {
	// header + a non-default route (subnet) + the default route (Destination 00000000) -> eth0
	route := "Iface\tDestination\tGateway\tFlags\n" +
		"eth0\t00A8C0\t00000000\t0001\n" +
		"eth0\t00000000\t0102A8C0\t0003\n"
	if got := defaultRouteIface([]byte(route)); got != "eth0" {
		t.Errorf("defaultRouteIface = %q, want eth0", got)
	}
	if got := defaultRouteIface([]byte("Iface\tDestination\neth0\t00A8C0\n")); got != "" {
		t.Errorf("no default route must yield \"\", got %q", got)
	}
}

func TestNativeXDPSampler_Sample(t *testing.T) {
	origRoute, origDriver, origKernel := procNetRouteFn, nicDriverFn, kernelReleaseFn
	t.Cleanup(func() { procNetRouteFn, nicDriverFn, kernelReleaseFn = origRoute, origDriver, origKernel })
	s := nativeXDPSampler{}

	procNetRouteFn = func() ([]byte, error) {
		return []byte("Iface\tDestination\tGateway\nens5\t00000000\t0102A8C0\n"), nil
	}
	nicDriverFn = func(iface string) (string, error) {
		if iface != "ens5" {
			t.Fatalf("probed the wrong iface: %q", iface)
		}
		return "ena", nil
	}
	kernelReleaseFn = func() ([]byte, error) { return []byte("6.1.0-13-cloud-amd64\n"), nil }

	_, metrics := s.Sample(time.Now())
	got, ok := metrics[nativeXDPMetricKey].(nativeXDPCapability)
	if !ok {
		t.Fatalf("metrics[%q] missing or wrong type: %+v", nativeXDPMetricKey, metrics)
	}
	if got.Capability != "supported" || got.Driver != "ena" || got.Kernel != "6.1.0-13-cloud-amd64" {
		t.Errorf("native_xdp = %+v, want {supported ena 6.1.0-13-cloud-amd64}", got)
	}

	// A driver read error → "unknown" (WITH the kernel), not silence.
	nicDriverFn = func(string) (string, error) { return "", errors.New("no driver") }
	_, metrics = s.Sample(time.Now())
	if g := metrics[nativeXDPMetricKey].(nativeXDPCapability); g.Capability != "unknown" || g.Kernel == "" {
		t.Errorf("driver read error must yield unknown+kernel, got %+v", g)
	}

	// No default route → no signal (nil metrics).
	procNetRouteFn = func() ([]byte, error) { return []byte("Iface\tDestination\n"), nil }
	if _, m := s.Sample(time.Now()); m != nil {
		t.Errorf("no default route must yield nil metrics, got %+v", m)
	}
}
