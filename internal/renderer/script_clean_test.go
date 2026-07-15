package renderer

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestInstallScripts_CleanRunsOnlyAfterIntegrityAndPrivilegeGates pins the deployment-safety
// ordering used by deploy-all: --clean remains available for layout migrations, but a candidate
// bundle must authenticate and pass its checksum manifest before it can remove the last-good
// WireGuard configuration. Both installer architectures own the flag because deploy-all treats
// router/peer and client bundles uniformly.
func TestInstallScripts_CleanRunsOnlyAfterIntegrityAndPrivilegeGates(t *testing.T) {
	t.Parallel()

	router := &model.Node{ID: "router-1", Name: "router-1", Role: "router", Platform: "debian"}
	routerScript, err := RenderInstallScript(router, nil, false)
	if err != nil {
		t.Fatalf("RenderInstallScript: %v", err)
	}

	client := &model.Node{ID: "client-1", Name: "client-1", Role: "client", Platform: "debian"}
	clientScript, err := RenderClientInstallScript(client)
	if err != nil {
		t.Fatalf("RenderClientInstallScript: %v", err)
	}

	for name, script := range map[string]string{"router": routerScript, "client": clientScript} {
		t.Run(name, func(t *testing.T) {
			flag := strings.Index(script, "--clean) CLEAN=1")
			checksum := strings.Index(script, "sha256sum --status -c checksums.sha256")
			root := strings.Index(script, `if [ "$(id -u)" -ne 0 ]`)
			clean := strings.Index(script, "=== Cleaning existing WireGuard interfaces ===")
			phase0 := strings.Index(script, "=== Phase 0: Cleanup Previous Installation ===")
			if flag < 0 || checksum < 0 || root < 0 || clean < 0 || phase0 < 0 {
				t.Fatalf("rendered %s installer is missing clean/integrity markers", name)
			}
			if !(flag < checksum && checksum < root && root < clean && clean < phase0) {
				t.Fatalf("unsafe %s installer ordering: flag=%d checksum=%d root=%d clean=%d phase0=%d", name, flag, checksum, root, clean, phase0)
			}
		})
	}
}

// TestInstallScripts_IntegrityFailureExecutesNoMutation is an execution-order gate, not just a text
// assertion. It impersonates root and puts sentinels in every destructive command the uninstall,
// clean, and Phase-0 paths use. A missing/bad integrity artifact must exit before any shim runs.
func TestInstallScripts_IntegrityFailureExecutesNoMutation(t *testing.T) {
	router := &model.Node{ID: "router-1", Name: "router-1", Role: "router", Platform: "debian"}
	client := &model.Node{ID: "client-1", Name: "client-1", Role: "client", Platform: "debian"}
	pubPEM := sigTestPubkeyPEM(t)

	routerUnsigned, err := RenderInstallScript(router, nil, false)
	if err != nil {
		t.Fatalf("RenderInstallScript: %v", err)
	}
	clientUnsigned, err := RenderClientInstallScript(client)
	if err != nil {
		t.Fatalf("RenderClientInstallScript: %v", err)
	}
	routerSigned, err := RenderInstallScriptSigned(router, nil, false, pubPEM, CustodySplice{}, model.InstallFetch{})
	if err != nil {
		t.Fatalf("RenderInstallScriptSigned: %v", err)
	}
	clientSigned, err := RenderClientInstallScriptSigned(client, pubPEM, CustodySplice{}, model.InstallFetch{})
	if err != nil {
		t.Fatalf("RenderClientInstallScriptSigned: %v", err)
	}

	tests := []struct {
		name      string
		script    string
		flag      string
		checksums string
		wantError string
	}{
		{name: "router/missing-checksums/uninstall", script: routerUnsigned, flag: "--uninstall", wantError: "checksums.sha256 is missing"},
		{name: "client/missing-checksums/clean", script: clientUnsigned, flag: "--clean", wantError: "checksums.sha256 is missing"},
		{name: "router/bad-checksum/clean", script: routerUnsigned, flag: "--clean", checksums: strings.Repeat("0", 64) + "  install.sh\n", wantError: "Checksum validation failed"},
		{name: "client/bad-checksum/uninstall", script: clientUnsigned, flag: "--uninstall", checksums: strings.Repeat("0", 64) + "  install.sh\n", wantError: "Checksum validation failed"},
		{name: "router/missing-signature/uninstall", script: routerSigned, flag: "--uninstall", wantError: "bundle.sig is missing"},
		{name: "client/missing-signature/clean", script: clientSigned, flag: "--clean", wantError: "bundle.sig is missing"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			installer := filepath.Join(dir, "install.sh")
			if err := os.WriteFile(installer, []byte(tc.script), 0o755); err != nil {
				t.Fatalf("write installer: %v", err)
			}
			if tc.checksums != "" {
				if err := os.WriteFile(filepath.Join(dir, "checksums.sha256"), []byte(tc.checksums), 0o644); err != nil {
					t.Fatalf("write checksums: %v", err)
				}
			}

			shimDir := t.TempDir()
			sentinel := filepath.Join(t.TempDir(), "mutation-ran")
			for _, name := range []string{"wg", "wg-quick", "systemctl", "rm", "sysctl", "ip", "nft", "iptables", "iptables-save", "modprobe"} {
				body := "#!/bin/sh\nprintf '%s\\n' \"$0 $*\" >> \"$YAOG_MUTATION_SENTINEL\"\n"
				if err := os.WriteFile(filepath.Join(shimDir, name), []byte(body), 0o755); err != nil {
					t.Fatalf("write %s shim: %v", name, err)
				}
			}
			// Make the script believe it is root; integrity must still fail before this probe.
			if err := os.WriteFile(filepath.Join(shimDir, "id"), []byte("#!/bin/sh\necho 0\n"), 0o755); err != nil {
				t.Fatalf("write id shim: %v", err)
			}

			cmd := exec.Command("bash", installer, tc.flag)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"), "YAOG_MUTATION_SENTINEL="+sentinel)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("integrity-invalid installer succeeded:\n%s", out)
			}
			if !strings.Contains(string(out), tc.wantError) {
				t.Fatalf("installer error did not explain integrity refusal; want %q in:\n%s", tc.wantError, out)
			}
			if raw, err := os.ReadFile(sentinel); err == nil {
				t.Fatalf("integrity-invalid installer executed a mutation command: %s", raw)
			} else if !os.IsNotExist(err) {
				t.Fatalf("stat mutation sentinel: %v", err)
			}
		})
	}
}

// TestDeployScripts_DelegateCleanToVerifiedInstaller prevents deploy-all from reviving its former
// remote cleanup heredoc. The archive member must be uploaded first and its own integrity-gated
// --clean path must own every destructive layout mutation, on both operator platforms.
func TestDeployScripts_DelegateCleanToVerifiedInstaller(t *testing.T) {
	t.Parallel()

	topo, peerMap, babelConfigs := deployGoldenTopology()
	bash, ps1, err := RenderDeployScripts(topo, peerMap, babelConfigs)
	if err != nil {
		t.Fatalf("RenderDeployScripts: %v", err)
	}

	for _, forbidden := range []string{"CLEAN_EOF", "Cleaning existing WireGuard interfaces on"} {
		if strings.Contains(bash, forbidden) {
			t.Fatalf("bash deploy-all still performs pre-verification remote cleanup (%q)", forbidden)
		}
	}
	if !strings.Contains(bash, `INSTALL_ARGS=" --clean"`) {
		t.Fatal("bash deploy-all does not derive the verified installer's --clean argument")
	}
	upload := strings.Index(bash, `-r "$BUNDLE_DIR"`)
	verifiedClean := strings.Index(bash, `sudo bash '$REMOTE_DIR/bundle/install.sh'$INSTALL_ARGS`)
	if upload < 0 || verifiedClean < 0 || upload >= verifiedClean {
		t.Fatalf("bash deploy order does not upload then invoke the integrity-gated cleaner: upload=%d apply=%d", upload, verifiedClean)
	}

	if strings.Contains(ps1, "$CleanScript") || strings.Contains(ps1, "Cleaning existing WireGuard interfaces on") {
		t.Fatal("PowerShell deploy-all still performs pre-verification remote cleanup")
	}
	if !strings.Contains(ps1, `$InstallArgs = if ($Clean) { " --clean" } else { "" }`) {
		t.Fatal("PowerShell deploy-all does not derive the verified installer's --clean argument")
	}
	psUpload := strings.Index(ps1, `-r $BundleDir $ScpDestination`)
	psVerifiedClean := strings.Index(ps1, `"sudo bash '$RemoteDir/bundle/install.sh'" + $InstallArgs`)
	if psUpload < 0 || psVerifiedClean < 0 || psUpload >= psVerifiedClean {
		t.Fatalf("PowerShell deploy order does not upload then invoke the integrity-gated cleaner: upload=%d apply=%d", psUpload, psVerifiedClean)
	}
}
