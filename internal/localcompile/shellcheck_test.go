package localcompile

import (
	"os/exec"
	"strings"
	"testing"
)

// shellcheckSeverity is the minimum finding level that fails the lint. It is set to "error" — the
// correctness/security-relevant class (syntax, undefined behavior) that the generated root-executed
// install script must never contain — rather than "warning"/"style", because the templates already
// carry inline `# shellcheck disable=SC2086` directives for their intentional word-splitting sites and
// this gate must stay non-flaky across shellcheck versions. Tighten to "warning" once the emitted
// corpus has been vetted against an installed shellcheck.
const shellcheckSeverity = "error"

// TestGeneratedInstallScriptsPassShellcheck lints every generated install script in the contract
// corpus with shellcheck, when it is available. Now that the install / client-install templates live as
// standalone *.sh.tmpl files (framework-refactor plan-6) and render to standalone scripts, shellcheck
// can lint the emitted root-install path directly — the reason the plan "restores" it. When shellcheck
// is not installed the test skips (so CI without it still passes); a CI job that installs shellcheck
// gates the generated root shell. Perpetual (never retired).
func TestGeneratedInstallScriptsPassShellcheck(t *testing.T) {
	bin, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skip("shellcheck not installed; skipping generated-script lint (a CI job with shellcheck gates this)")
	}

	linted := 0
	for _, fx := range loadFixtures(t) {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			art, err := Compile(requestFor(t, fx))
			if err != nil {
				t.Fatalf("Compile %s: %v", fx.Name, err)
			}
			for nodeID, files := range art.Files {
				for path, content := range files {
					if !strings.HasSuffix(path, ".sh") {
						continue
					}
					linted++
					if out, ok := shellcheckClean(bin, content); !ok {
						t.Errorf("shellcheck flagged %s / node %s / %s:\n%s", fx.Name, nodeID, path, out)
					}
				}
			}
		})
	}
	if linted == 0 {
		t.Fatal("no generated .sh script was linted — the corpus should emit an install.sh per node")
	}
}

// shellcheckClean runs shellcheck over a script fed on stdin and reports whether it is clean at
// shellcheckSeverity, returning the combined output on failure.
func shellcheckClean(bin, script string) (string, bool) {
	cmd := exec.Command(bin, "--shell=bash", "--severity="+shellcheckSeverity, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}
