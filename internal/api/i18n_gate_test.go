package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var gateCJK = regexp.MustCompile(`\p{Han}`)

// TestNoChineseInWireAndDeploySurfaces is the perpetual grep-gate for the plan-3.5b/3.5c i18n work:
// no Chinese may live in a Go STRING-LITERAL position across the packages that produce operator-facing
// output — the HTTP error envelope (api) and the wire/deploy artifacts (compiler, render, artifacts,
// renderer). Such a string would reach an operator un-localized: the envelope is localized on the
// FRONTEND by error.<code> (the Go side is the English default), and the generated deploy scripts have
// no localization channel and must be English. Chinese in `//` developer comments is allowed.
//
// Go test runs with CWD = this package's dir, so the sibling packages are reached via "..". The strip-
// at-first-`//` rule mirrors the validator's TestNoChineseInValidatorSource (and shares its one known
// limitation: a string literal that itself contains `//` before CJK is truncated — none exist today).
// The transitional writeError shim and its legacy uncoded-message escape hatch are not gated here:
// having been deleted, any reference to them is a compile error, so the compiler is their perpetual gate.
func TestNoChineseInWireAndDeploySurfaces(t *testing.T) {
	dirs := []string{".", "../compiler", "../render", "../artifacts", "../renderer"}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dir %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			p := filepath.Join(dir, name)
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read %s: %v", p, err)
			}
			for i, line := range strings.Split(string(b), "\n") {
				code := line
				if c := strings.Index(code, "//"); c >= 0 {
					code = code[:c]
				}
				if gateCJK.MatchString(code) {
					t.Errorf("%s:%d has Chinese in code/string position (must be English): %s", p, i+1, strings.TrimSpace(line))
				}
			}
		}
	}
}
