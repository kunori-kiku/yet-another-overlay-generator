package probepolicy

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

func TestValidHost_IPOrDNSWithoutSeparateDNSField(t *testing.T) {
	valid := []string{
		"192.0.2.10",
		"2001:db8::10",
		"probe.example.com",
		"probe.example.com.",
		"internal-host",
		"xn--bcher-kva.example",
	}
	for _, host := range valid {
		if !ValidHost(host) {
			t.Errorf("ValidHost(%q) = false, want true", host)
		}
	}
	invalid := []string{
		"",
		" probe.example",
		"probe.example ",
		"https://probe.example/status",
		"probe.example:443",
		"probe.example/path",
		"probe.example?x=1",
		"[2001:db8::10]",
		"-bad.example",
		"bad-.example",
		"bad..example",
		"bad_name.example",
		"bücher.example",
		strings.Repeat("a", 64) + ".example",
	}
	for _, host := range invalid {
		if ValidHost(host) {
			t.Errorf("ValidHost(%q) = true, want false", host)
		}
	}
}

func TestValidate_TypedKindsBoundsAndDefaults(t *testing.T) {
	valid := []model.TelemetryProbe{
		{ID: "dns-icmp", Type: model.TelemetryProbeICMP, Host: "edge.example"},
		{ID: "tcp-v6", Type: model.TelemetryProbeTCP, Host: "2001:db8::1", Port: 443, IntervalSeconds: 30, TimeoutMilliseconds: 100},
	}
	if err := Validate(valid); err != nil {
		t.Fatalf("Validate(valid): %v", err)
	}
	if got := EffectiveIntervalSeconds(valid[0]); got != DefaultIntervalSeconds {
		t.Fatalf("default interval = %d, want %d", got, DefaultIntervalSeconds)
	}
	if got := EffectiveTimeoutMilliseconds(valid[0]); got != DefaultTimeoutMilliseconds {
		t.Fatalf("default timeout = %d, want %d", got, DefaultTimeoutMilliseconds)
	}

	tests := []struct {
		name  string
		probe model.TelemetryProbe
	}{
		{"missing host", model.TelemetryProbe{ID: "p", Type: model.TelemetryProbeICMP}},
		{"unsupported future kind", model.TelemetryProbe{ID: "p", Type: "url", Host: "example.com"}},
		{"tcp missing port", model.TelemetryProbe{ID: "p", Type: model.TelemetryProbeTCP, Host: "example.com"}},
		{"tcp port high", model.TelemetryProbe{ID: "p", Type: model.TelemetryProbeTCP, Host: "example.com", Port: 65536}},
		{"icmp has port", model.TelemetryProbe{ID: "p", Type: model.TelemetryProbeICMP, Host: "example.com", Port: 7}},
		{"interval low", model.TelemetryProbe{ID: "p", Type: model.TelemetryProbeICMP, Host: "example.com", IntervalSeconds: 29}},
		{"interval high", model.TelemetryProbe{ID: "p", Type: model.TelemetryProbeICMP, Host: "example.com", IntervalSeconds: 3601}},
		{"timeout low", model.TelemetryProbe{ID: "p", Type: model.TelemetryProbeICMP, Host: "example.com", TimeoutMilliseconds: 99}},
		{"timeout high", model.TelemetryProbe{ID: "p", Type: model.TelemetryProbeICMP, Host: "example.com", TimeoutMilliseconds: 5001}},
		{"bad id", model.TelemetryProbe{ID: "bad id", Type: model.TelemetryProbeICMP, Host: "example.com"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate([]model.TelemetryProbe{tc.probe}); err == nil {
				t.Fatal("Validate unexpectedly accepted invalid probe")
			}
		})
	}

	if err := Validate([]model.TelemetryProbe{
		{ID: "same", Type: model.TelemetryProbeICMP, Host: "a.example"},
		{ID: "same", Type: model.TelemetryProbeICMP, Host: "b.example"},
	}); err == nil {
		t.Fatal("Validate accepted duplicate IDs")
	}
	tooMany := make([]model.TelemetryProbe, MaxProbes+1)
	for i := range tooMany {
		tooMany[i] = model.TelemetryProbe{ID: "p" + strings.Repeat("x", i), Type: model.TelemetryProbeICMP, Host: "example.com"}
	}
	if err := Validate(tooMany); err == nil {
		t.Fatal("Validate accepted more than MaxProbes")
	}
}

func TestParse_StrictVersionedCanonicalPolicy(t *testing.T) {
	probes := []model.TelemetryProbe{
		{ID: "ping", Type: model.TelemetryProbeICMP, Host: "dns.example"},
		{ID: "tls", Type: model.TelemetryProbeTCP, Host: "192.0.2.9", Port: 443},
	}
	raw, err := Marshal(probes)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	policy, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if policy.Version != CurrentVersion || len(policy.Probes) != 2 || policy.Probes[0].Host != "dns.example" {
		t.Fatalf("parsed policy = %+v", policy)
	}

	for _, raw := range []string{
		`{"version":1,"probes":[{"id":"p","type":"icmp","host":"example.com","dns":"extra"}]}`,
		`{"version":2,"probes":[{"id":"p","type":"icmp","host":"example.com"}]}`,
		`{"version":1,"probes":[]}`,
		`{"version":1,"probes":[{"id":"p","type":"icmp","host":"example.com"}]} {}`,
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("Parse accepted strict-contract violation: %s", raw)
		}
	}
	if raw, err := Marshal(nil); err != nil || raw != nil {
		t.Fatalf("Marshal(nil) = %q, %v; want nil, nil", raw, err)
	}
}
