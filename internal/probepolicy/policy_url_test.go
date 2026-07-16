package probepolicy

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrycap"
)

func TestURLPolicyEncodedDestinationBudget(t *testing.T) {
	probes := make([]model.TelemetryProbe, MaxProbes)
	for i := range probes {
		// encoding/json expands each ampersand to \\u0026. At 339 ampersands, sixteen URL JSON
		// strings sit just below the half-envelope policy budget while exercising worst-case ASCII
		// expansion rather than relying on raw URL length.
		probes[i] = model.TelemetryProbe{
			ID: fmt.Sprintf("url-%02d", i), Type: model.TelemetryProbeURL,
			URL: "https://e/?" + strings.Repeat("&", 339),
		}
	}
	if err := Validate(probes); err != nil {
		t.Fatalf("boundary-safe encoded URL policy rejected: %v", err)
	}
	if _, err := MarshalSuccessor(SuccessorPolicy{Probes: probes}); err != nil {
		t.Fatalf("boundary-safe successor policy rejected: %v", err)
	}

	for i := range probes {
		probes[i].URL += "&"
	}
	if err := Validate(probes); err == nil || !strings.Contains(err.Error(), "encoded destination bytes") {
		t.Fatalf("over-budget encoded URL policy error = %v", err)
	}
}

func TestURLPolicyIsSuccessorOnlyAndCanonical(t *testing.T) {
	probe := model.TelemetryProbe{
		ID: "health", Type: model.TelemetryProbeURL, URL: "https://service.internal/ready?full=1",
	}
	if _, err := Marshal([]model.TelemetryProbe{probe}); err == nil {
		t.Fatal("telemetry.json v1 accepted a URL probe")
	}

	raw, err := MarshalSuccessor(SuccessorPolicy{Probes: []model.TelemetryProbe{probe}})
	if err != nil {
		t.Fatalf("MarshalSuccessor: %v", err)
	}
	want := `{"version":2,"probes":[{"id":"health","type":"url","url":"https://service.internal/ready?full=1","expected_status":200}]}`
	if string(raw) != want {
		t.Fatalf("successor URL policy = %s, want %s", raw, want)
	}
	parsed, err := ParseSuccessor(raw)
	if err != nil {
		t.Fatalf("ParseSuccessor: %v", err)
	}
	if len(parsed.Probes) != 1 || parsed.Probes[0].URL != probe.URL || parsed.Probes[0].ExpectedStatus != 200 {
		t.Fatalf("parsed URL policy = %+v", parsed)
	}
}

func TestValidateURLProbeSurface(t *testing.T) {
	valid := []model.TelemetryProbe{
		{ID: "http", Type: model.TelemetryProbeURL, URL: "http://127.0.0.1:8080/health"},
		{ID: "ipv6", Type: model.TelemetryProbeURL, URL: "https://[::1]:8443/status?check=yes", ExpectedStatus: 204},
	}
	if err := Validate(valid); err != nil {
		t.Fatalf("signed internal URL targets rejected: %v", err)
	}

	bad := []model.TelemetryProbe{
		{ID: "empty", Type: model.TelemetryProbeURL},
		{ID: "space", Type: model.TelemetryProbeURL, URL: " https://example.test/"},
		{ID: "control", Type: model.TelemetryProbeURL, URL: "https://example.test/\n"},
		{ID: "scheme", Type: model.TelemetryProbeURL, URL: "ftp://example.test/file"},
		{ID: "relative", Type: model.TelemetryProbeURL, URL: "/health"},
		{ID: "userinfo", Type: model.TelemetryProbeURL, URL: "https://user@example.test/"},
		{ID: "fragment", Type: model.TelemetryProbeURL, URL: "https://example.test/#ready"},
		{ID: "port", Type: model.TelemetryProbeURL, URL: "https://example.test:70000/"},
		{ID: "mixed", Type: model.TelemetryProbeURL, Host: "example.test", URL: "https://example.test/"},
		{ID: "status", Type: model.TelemetryProbeURL, URL: "https://example.test/", ExpectedStatus: 99},
		{ID: "host-url", Type: model.TelemetryProbeICMP, Host: "example.test", URL: "https://example.test/"},
		{ID: "oversized", Type: model.TelemetryProbeURL, URL: "https://example.test/" + strings.Repeat("x", MaxURLBytes)},
	}
	for _, probe := range bad {
		if err := Validate([]model.TelemetryProbe{probe}); err == nil {
			t.Errorf("invalid URL surface accepted: %+v", probe)
		}
	}
}

func TestURLCapabilitiesAndLegacyProjection(t *testing.T) {
	probes := []model.TelemetryProbe{
		{ID: "legacy", Type: model.TelemetryProbeICMP, Host: "resolver.internal"},
		{ID: "web", Type: model.TelemetryProbeURL, URL: "https://service.internal/", ExpectedStatus: 204},
	}
	node := model.Node{
		TelemetryProbes:  probes,
		TelemetryDevices: &model.TelemetryDevicePolicy{Mode: string(DeviceModeAllEligibleV1)},
	}
	if !RequiresSuccessor(node) {
		t.Fatal("URL/device policy did not require successor")
	}
	wantCapabilities := []string{telemetrycap.DeviceV1, telemetrycap.PolicyV2, telemetrycap.URLV1}
	if got := RequiredCapabilities(node); !reflect.DeepEqual(got, wantCapabilities) {
		t.Fatalf("RequiredCapabilities = %v, want %v", got, wantCapabilities)
	}
	ProjectLegacy(&node)
	if node.TelemetryDevices != nil || len(node.TelemetryProbes) != 1 || node.TelemetryProbes[0].ID != "legacy" {
		t.Fatalf("legacy projection = %+v", node)
	}
	if len(probes) != 2 || probes[1].Type != model.TelemetryProbeURL {
		t.Fatalf("legacy projection mutated the source backing slice: %+v", probes)
	}

	urlOnly := model.Node{TelemetryProbes: []model.TelemetryProbe{{ID: "web", Type: model.TelemetryProbeURL, URL: "http://localhost/"}}}
	if got := RequiredCapabilities(urlOnly); !reflect.DeepEqual(got, []string{telemetrycap.PolicyV2, telemetrycap.URLV1}) {
		t.Fatalf("URL-only capabilities = %v", got)
	}
}
