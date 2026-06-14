package validator

// code_test.go — plan-3.5a completeness gates for the validator-channel localization. Three
// independent guards (per the locked design): the compiler (an unported AddError call is a build
// error — free), newFinding's panic (an emitted code with no registry entry trips the suite),
// and the assertions below (registry is English-only; the validator source carries no Chinese in
// message position; findings round-trip coded + English end-to-end).

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

var cjk = regexp.MustCompile(`\p{Han}`)

// TestRegistryEnglishOnly: every registry template is non-empty and contains no CJK — the
// registry is the single source of the CLI/curl message + the i18n English fallback, so an
// English-locale operator never sees another language even for an un-keyed-in-the-panel code.
func TestRegistryEnglishOnly(t *testing.T) {
	if len(registry) == 0 {
		t.Fatal("registry is empty")
	}
	for code, tmpl := range registry {
		if strings.TrimSpace(tmpl) == "" {
			t.Errorf("%s has an empty template", code)
		}
		if cjk.MatchString(tmpl) {
			t.Errorf("%s template contains CJK (must be English): %q", code, tmpl)
		}
	}
}

// TestNoChineseInValidatorSource: the validator Go source carries Chinese only in // comments
// (developer-facing), never in a string literal that could reach a user. A line with a Han rune
// must also contain "//". This is the grep-gate that keeps message-position Chinese from
// regressing back in (the frontend zh catalog is now the only home for the translations).
func TestNoChineseInValidatorSource(t *testing.T) {
	for _, f := range []string{"schema.go", "semantic.go", "nat.go", "code.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			// Scan only the CODE portion (strip the // comment): Chinese is allowed in
			// developer comments, never in a string literal that could reach a user. Keying on
			// the mere presence of "//" anywhere on the line would let a Chinese string literal
			// with a trailing comment slip past (plan-3.5a review).
			code := line
			if c := strings.Index(code, "//"); c >= 0 {
				code = code[:c]
			}
			if cjk.MatchString(code) {
				t.Errorf("%s:%d has Chinese in code/string position (must be English): %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// invalidTopo is deliberately malformed in many ways (empty ids/names, no domains/nodes) so the
// schema+semantic passes emit a broad spread of findings.
func invalidTopo() *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "", Name: ""},
		Domains: []model.Domain{{ID: "", Name: "", CIDR: "not-a-cidr", AllocationMode: "bogus", RoutingMode: "bogus"}},
		Nodes:   []model.Node{{ID: "", Name: "", DomainID: "nope"}},
		Edges:   []model.Edge{{ID: "", FromNodeID: "x", ToNodeID: "y"}},
	}
}

// TestFindingsAreCodedAndEnglish: every finding from a broken topology carries a non-empty Code
// and a non-empty English (no-CJK) Message — the end-to-end proof that the channel is coded and
// the default render is English.
func TestFindingsAreCodedAndEnglish(t *testing.T) {
	var findings []ValidationError
	findings = append(findings, ValidateSchema(invalidTopo()).Errors...)
	findings = append(findings, ValidateSchema(invalidTopo()).Warnings...)
	findings = append(findings, ValidateSemantic(invalidTopo()).Errors...)
	findings = append(findings, ValidateSemantic(invalidTopo()).Warnings...)
	if len(findings) == 0 {
		t.Fatal("expected the broken topology to produce findings")
	}
	for _, f := range findings {
		if f.Code == "" {
			t.Errorf("finding [%s] has an empty Code: %q", f.Field, f.Message)
		}
		if strings.TrimSpace(f.Message) == "" {
			t.Errorf("finding [%s] code=%s has an empty Message", f.Field, f.Code)
		}
		if cjk.MatchString(f.Message) {
			t.Errorf("finding code=%s Message contains CJK (must render English): %q", f.Code, f.Message)
		}
	}
}
