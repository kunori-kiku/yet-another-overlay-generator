package renderer

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrycap"
)

func TestInstallerCapability_LegacyProbeRequiresPolicyAwareAgent(t *testing.T) {
	tests := []struct {
		name   string
		render func(*model.Node, CustodySplice) (string, error)
	}{
		{
			name: "peer",
			render: func(node *model.Node, splice CustodySplice) (string, error) {
				return RenderInstallScriptSigned(node, nil, false, "", splice, model.InstallFetch{})
			},
		},
		{
			name: "client",
			render: func(node *model.Node, splice CustodySplice) (string, error) {
				node.Role = "client"
				return RenderClientInstallScriptSigned(node, "", splice, model.InstallFetch{})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			node := &model.Node{
				ID: "node-1", Name: "node-1", Role: "peer", Platform: "debian",
				TelemetryProbes: []model.TelemetryProbe{{
					ID: "gateway", Type: model.TelemetryProbeTCP, Host: "gateway.example", Port: 443,
				}},
			}
			script, err := tc.render(node, CustodySplice{Enabled: true, Token: "PRIVATEKEY_PLACEHOLDER"})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if !strings.Contains(script, telemetrycap.InstallerPolicyV1Env) {
				t.Fatalf("probe-bearing AgentHeld installer lacks %s gate", telemetrycap.InstallerPolicyV1Env)
			}

			path := filepath.Join(t.TempDir(), "install.sh")
			if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", path)
			cmd.Env = withoutEnvironmentVariable(os.Environ(), telemetrycap.InstallerPolicyV1Env)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Run(); err == nil {
				t.Fatal("pre-rc.9 launcher unexpectedly applied a probe-bearing bundle")
			}
			if !strings.Contains(stderr.String(), "requires yaog-agent v2.0.0-rc.9 or later") {
				t.Fatalf("refusal = %q, want actionable upgrade message", stderr.String())
			}
		})
	}
}

func TestInstallerCapability_GateIsAgentHeldAndPolicyScoped(t *testing.T) {
	node := &model.Node{ID: "node-1", Name: "node-1", Role: "peer", Platform: "debian"}
	withoutProbes, err := RenderInstallScriptSigned(node, nil, false, "", CustodySplice{Enabled: true, Token: "PRIVATEKEY_PLACEHOLDER"}, model.InstallFetch{})
	if err != nil {
		t.Fatal(err)
	}
	node.TelemetryProbes = []model.TelemetryProbe{{ID: "dns", Type: model.TelemetryProbeICMP, Host: "resolver.example"}}
	airGap, err := RenderInstallScriptSigned(node, nil, false, "", CustodySplice{}, model.InstallFetch{})
	if err != nil {
		t.Fatal(err)
	}
	for label, script := range map[string]string{"AgentHeld without probes": withoutProbes, "air-gap with design probes": airGap} {
		if strings.Contains(script, telemetrycap.InstallerPolicyV1Env) {
			t.Fatalf("%s unexpectedly gained an agent capability gate", label)
		}
	}
}

func TestInstallerCapability_SuccessorRequiresGenericAndDeviceMarkers(t *testing.T) {
	node := &model.Node{
		ID: "node-1", Name: "node-1", Role: "peer", Platform: "debian",
		TelemetryDevices: &model.TelemetryDevicePolicy{Mode: "all-eligible-v1"},
	}
	requirements, err := requiredInstallerCapabilities(node, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(requirements) != 2 {
		t.Fatalf("successor installer requirements = %+v, want generic and device markers", requirements)
	}
	wantExpressions := []string{
		"${" + telemetrycap.InstallerDeviceV1Env + ":-}",
		"${" + telemetrycap.InstallerPolicyV2Env + ":-}",
	}
	for i, want := range wantExpressions {
		if got := requirements[i].ValueExpression.String(); got != want {
			t.Fatalf("requirement %d expression = %q, want %q", i, got, want)
		}
	}

	script, err := RenderInstallScriptSigned(node, nil, false, "", CustodySplice{Enabled: true, Token: "PRIVATEKEY_PLACEHOLDER"}, model.InstallFetch{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	mutationBoundary := strings.Index(script, `SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"`)
	if mutationBoundary < 0 {
		t.Fatal("installer lacks expected pre-mutation boundary")
	}
	for _, envName := range []string{
		telemetrycap.InstallerDeviceV1Env,
		telemetrycap.InstallerPolicyV2Env,
	} {
		marker := strings.Index(script, "${"+envName+":-}")
		if marker < 0 || marker > mutationBoundary {
			t.Fatalf("%s gate is absent or occurs after installer mutation setup", envName)
		}
	}
	if got := strings.Count(script, `if [ "$UNINSTALL" -eq 0 ] &&`); got != 2 {
		t.Fatalf("successor installer has %d capability gates guarded by normal apply, want 2", got)
	}

	path := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", path)
	base := withoutEnvironmentVariable(os.Environ(), telemetrycap.InstallerPolicyV2Env)
	base = withoutEnvironmentVariable(base, telemetrycap.InstallerDeviceV1Env)
	cmd.Env = append(base,
		telemetrycap.InstallerDeviceV1Env+"=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("launcher missing the generic successor marker unexpectedly applied the bundle")
	}
	if !strings.Contains(stderr.String(), "telemetry-policy-v2 capable") {
		t.Fatalf("generic successor refusal = %q", stderr.String())
	}
}

func TestInstallerCapability_URLRequiresGenericAndFeatureMarkers(t *testing.T) {
	node := &model.Node{
		ID: "node-1", Name: "node-1", Role: "peer", Platform: "debian",
		TelemetryProbes: []model.TelemetryProbe{{
			ID: "health", Type: model.TelemetryProbeURL, URL: "https://service.internal/ready",
		}},
	}
	requirements, err := requiredInstallerCapabilities(node, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(requirements) != 2 ||
		requirements[0].ValueExpression.String() != "${"+telemetrycap.InstallerPolicyV2Env+":-}" ||
		requirements[1].ValueExpression.String() != "${"+telemetrycap.InstallerURLV1Env+":-}" {
		t.Fatalf("URL installer requirements = %+v, want generic and URL markers", requirements)
	}

	script, err := RenderInstallScriptSigned(node, nil, false, "", CustodySplice{Enabled: true, Token: "PRIVATEKEY_PLACEHOLDER"}, model.InstallFetch{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	path := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", path)
	base := withoutEnvironmentVariable(os.Environ(), telemetrycap.InstallerPolicyV2Env)
	base = withoutEnvironmentVariable(base, telemetrycap.InstallerURLV1Env)
	cmd.Env = append(base, telemetrycap.InstallerPolicyV2Env+"=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("generic-v2-only launcher unexpectedly applied a URL bundle")
	}
	if !strings.Contains(stderr.String(), "url-probes-v1 capable") {
		t.Fatalf("URL capability refusal = %q", stderr.String())
	}
}

func withoutEnvironmentVariable(base []string, name string) []string {
	prefix := name + "="
	out := make([]string, 0, len(base))
	for _, entry := range base {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return out
}
