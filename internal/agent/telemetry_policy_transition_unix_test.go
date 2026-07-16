//go:build !windows

package agent

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrycap"
)

func TestInstallerCapabilityEnvironment_StripsSpoofedAndUnsupportedMarkers(t *testing.T) {
	base := []string{
		"KEEP=ok",
		"BASH_ENV=/tmp/restore-unsupported-capabilities",
		"BASH_FUNC_bash%%=() { export YAOG_AGENT_CAP_DEVICE_TELEMETRY_V1=1; }",
		"BASH_FUNC_restore%%=() { export YAOG_AGENT_CAP_URL_PROBES_V1=1; }",
		telemetrycap.InstallerPolicyV1Env + "=spoofed",
		telemetrycap.InstallerPolicyV2Env + "=spoofed",
		telemetrycap.InstallerURLV1Env + "=spoofed",
		telemetrycap.InstallerDeviceV1Env + "=spoofed",
	}
	environment := installerCommandEnvironment(base)
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			values[name] = value
		}
	}
	if values["KEEP"] != "ok" {
		t.Fatalf("unrelated environment was not preserved: %v", environment)
	}
	for _, entry := range environment {
		if strings.HasPrefix(entry, "BASH_ENV=") || strings.HasPrefix(entry, "BASH_FUNC_") {
			t.Fatalf("bash startup injection survived capability sanitization: %q", entry)
		}
	}
	for _, supported := range []string{
		telemetrycap.InstallerPolicyV1Env,
		telemetrycap.InstallerPolicyV2Env,
	} {
		if values[supported] != "1" {
			t.Fatalf("launcher-owned %s = %q, want 1", supported, values[supported])
		}
	}
	for _, unsupported := range []string{
		telemetrycap.InstallerURLV1Env,
		telemetrycap.InstallerDeviceV1Env,
	} {
		if _, exists := values[unsupported]; exists {
			t.Fatalf("inherited unsupported marker %s survived sanitization", unsupported)
		}
	}
}
