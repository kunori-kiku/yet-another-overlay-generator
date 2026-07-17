package probepolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrycap"
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
		{ID: "dns-icmp", Name: "Primary 解析器", Type: model.TelemetryProbeICMP, Host: "edge.example"},
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
		{"name outer whitespace", model.TelemetryProbe{ID: "p", Name: " padded", Type: model.TelemetryProbeICMP, Host: "example.com"}},
		{"name control", model.TelemetryProbe{ID: "p", Name: "line\nbreak", Type: model.TelemetryProbeICMP, Host: "example.com"}},
		{"name line separator", model.TelemetryProbe{ID: "p", Name: "line\u2028break", Type: model.TelemetryProbeICMP, Host: "example.com"}},
		{"name bidi format", model.TelemetryProbe{ID: "p", Name: "safe\u202eevil", Type: model.TelemetryProbeICMP, Host: "example.com"}},
		{"name zero width format", model.TelemetryProbe{ID: "p", Name: "zero\u200bwidth", Type: model.TelemetryProbeICMP, Host: "example.com"}},
		{"name too long", model.TelemetryProbe{ID: "p", Name: strings.Repeat("界", MaxNameRunes+1), Type: model.TelemetryProbeICMP, Host: "example.com"}},
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
	if err := Validate([]model.TelemetryProbe{{
		ID: "emoji", Name: "Sydney latency 🌏", Type: model.TelemetryProbeICMP, Host: "example.com",
	}}); err != nil {
		t.Fatalf("printable non-ASCII name rejected: %v", err)
	}
}

func TestMarshal_DisplayNameDoesNotChangeExecutablePolicy(t *testing.T) {
	base := model.TelemetryProbe{ID: "dns", Type: model.TelemetryProbeICMP, Host: "resolver.example"}
	unnamed, err := Marshal([]model.TelemetryProbe{base})
	if err != nil {
		t.Fatalf("Marshal unnamed: %v", err)
	}
	named, err := Marshal([]model.TelemetryProbe{{
		ID: base.ID, Name: "Primary resolver", Type: base.Type, Host: base.Host,
	}})
	if err != nil {
		t.Fatalf("Marshal named: %v", err)
	}
	if !bytes.Equal(named, unnamed) {
		t.Fatalf("display name changed executable policy:\nunnamed %s\nnamed   %s", unnamed, named)
	}
	if bytes.Contains(named, []byte(`"name"`)) {
		t.Fatalf("display name leaked into telemetry.json: %s", named)
	}

	policy, err := Parse(named)
	if err != nil {
		t.Fatalf("Parse named projection: %v", err)
	}
	if policy.Probes[0].Name != "" {
		t.Fatalf("parsed executable policy acquired display metadata: %+v", policy.Probes[0])
	}
	withNameOnWire := `{"version":1,"probes":[{"id":"dns","name":"Primary resolver","type":"icmp","host":"resolver.example"}]}`
	if _, err := Parse([]byte(withNameOnWire)); err == nil {
		t.Fatal("Parse accepted controller-only display name inside strict telemetry.json")
	}
}

func TestPolicyRejectsGenericJSONMarshal(t *testing.T) {
	policy := Policy{Version: CurrentVersion, Probes: []model.TelemetryProbe{{
		ID: "dns", Name: "Controller-only label", Type: model.TelemetryProbeICMP, Host: "resolver.example",
	}}}
	for _, value := range []any{policy, &policy} {
		raw, err := json.Marshal(value)
		if !errors.Is(err, errPolicyRuntimeOnly) {
			t.Fatalf("json.Marshal(%T) error = %v, want %v", value, err, errPolicyRuntimeOnly)
		}
		if len(raw) != 0 {
			t.Fatalf("json.Marshal(%T) returned usable bytes %q", value, raw)
		}
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
		`{"version":1,"probes":[{"id":"p","name":"display-only","type":"icmp","host":"example.com"}]}`,
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

func TestPolicyV1_FrozenBytesExcludeDisplayName(t *testing.T) {
	probes := []model.TelemetryProbe{{
		ID: "control", Name: "Controller reachability", Type: model.TelemetryProbeTCP,
		Host: "control.example", Port: 443, IntervalSeconds: 30, TimeoutMilliseconds: 500,
	}}
	raw, err := Marshal(probes)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	const want = `{"version":1,"probes":[{"id":"control","type":"tcp","host":"control.example","port":443,"interval_seconds":30,"timeout_milliseconds":500}]}`
	if string(raw) != want {
		t.Fatalf("telemetry.json = %s, want frozen v1 bytes %s", raw, want)
	}
	if bytes.Contains(raw, []byte(`"name"`)) {
		t.Fatalf("controller display metadata leaked into telemetry.json: %s", raw)
	}
}

func TestSuccessorPolicy_StrictRoundTripBoundsAndHelpers(t *testing.T) {
	policy := SuccessorPolicy{
		Probes: []model.TelemetryProbe{{
			ID: "resolver", Name: "Display only", Type: model.TelemetryProbeICMP,
			Host: "resolver.example", IntervalSeconds: 30,
		}},
		Devices: &DevicePolicy{Mode: DeviceModeAllEligibleV1},
	}
	raw, err := MarshalSuccessor(policy)
	if err != nil {
		t.Fatalf("MarshalSuccessor: %v", err)
	}
	const want = `{"version":2,"probes":[{"id":"resolver","type":"icmp","host":"resolver.example","interval_seconds":30}],"devices":{"mode":"all-eligible-v1"}}`
	if string(raw) != want {
		t.Fatalf("telemetry-policy.json = %s, want %s", raw, want)
	}
	if bytes.Contains(raw, []byte(`"name"`)) {
		t.Fatalf("controller display metadata leaked into successor policy: %s", raw)
	}

	parsed, err := ParseSuccessor(raw)
	if err != nil {
		t.Fatalf("ParseSuccessor: %v", err)
	}
	if parsed.Version != SuccessorVersion || len(parsed.Probes) != 1 || parsed.Probes[0].Name != "" ||
		parsed.Devices == nil || parsed.Devices.Mode != DeviceModeAllEligibleV1 {
		t.Fatalf("parsed successor policy = %+v", parsed)
	}
	active, err := ParseActive(raw)
	if err != nil || active.Version != SuccessorVersion || active.Devices == nil {
		t.Fatalf("ParseActive(successor) = %+v, %v", active, err)
	}
	v1, err := Marshal([]model.TelemetryProbe{{ID: "legacy", Type: model.TelemetryProbeICMP, Host: "legacy.example"}})
	if err != nil {
		t.Fatal(err)
	}
	active, err = ParseActive(v1)
	if err != nil || active.Version != CurrentVersion || active.Devices != nil || len(active.Probes) != 1 {
		t.Fatalf("ParseActive(v1) = %+v, %v", active, err)
	}
	if _, err := json.Marshal(policy); !errors.Is(err, errSuccessorPolicyRuntimeOnly) {
		t.Fatalf("generic successor marshal error = %v, want %v", err, errSuccessorPolicyRuntimeOnly)
	}

	for name, invalid := range map[string][]byte{
		"unknown root field":  []byte(`{"version":2,"devices":{"mode":"all-eligible-v1"},"future":true}`),
		"unknown probe field": []byte(`{"version":2,"probes":[{"id":"p","type":"icmp","host":"example.com","name":"display"}]}`),
		"wrong version":       []byte(`{"version":1,"devices":{"mode":"all-eligible-v1"}}`),
		"empty policy":        []byte(`{"version":2}`),
		"unknown device mode": []byte(`{"version":2,"devices":{"mode":"future"}}`),
		"trailing JSON":       []byte(`{"version":2,"devices":{"mode":"all-eligible-v1"}} {}`),
		"oversize":            append(append([]byte(nil), raw...), bytes.Repeat([]byte(" "), maxPolicyBytes)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSuccessor(invalid); err == nil {
				t.Fatalf("ParseSuccessor accepted invalid %s input", name)
			}
		})
	}

	tooMany := make([]model.TelemetryProbe, MaxProbes+1)
	for i := range tooMany {
		tooMany[i] = model.TelemetryProbe{ID: fmt.Sprintf("p-%d", i), Type: model.TelemetryProbeICMP, Host: "example.com"}
	}
	if _, err := MarshalSuccessor(SuccessorPolicy{Probes: tooMany}); err == nil {
		t.Fatal("MarshalSuccessor accepted more than MaxProbes")
	}

	node := model.Node{
		TelemetryProbes:  append([]model.TelemetryProbe(nil), policy.Probes...),
		TelemetryDevices: &model.TelemetryDevicePolicy{Mode: string(DeviceModeAllEligibleV1)},
	}
	if !RequiresSuccessor(node) {
		t.Fatal("device policy did not select the successor member")
	}
	wantCapabilities := []string{
		telemetrycap.DeviceV1,
		telemetrycap.PolicyV2,
	}
	if got := RequiredCapabilities(node); !reflect.DeepEqual(got, wantCapabilities) {
		t.Fatalf("RequiredCapabilities = %v, want %v", got, wantCapabilities)
	}
	ProjectLegacy(&node)
	if node.TelemetryDevices != nil || len(node.TelemetryProbes) != 1 {
		t.Fatalf("ProjectLegacy removed the wrong fields: %+v", node)
	}
}
