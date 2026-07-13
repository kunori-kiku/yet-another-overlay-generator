package conformance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/allocconst"
)

// drift_test.go — the structural drift manifest (step 6 of plan-5 / 1.5).
//
// It mechanizes the in-code KEEP-IN-SYNC discipline that today has no automated check: a set of
// hand-synced authorities that, if any drifts silently, breaks the Go↔TS conformance contract or
// the byte-output assumptions the harness rests on. The manifest pins, per drift_manifest.json:
//
//   - renderer_template_hashes: the sha256 of each renderer's scoped template BLOCK (the const
//     string value, not the whole file — so cosmetic edits elsewhere in the file do not red it, but
//     touching a template — which changes the rendered bytes the conformance corpus freezes — does).
//   - validator_codes / apierr_codes: BOTH Go code-name sets with their LIVE counts and full sorted
//     lists. Adding, removing, or renaming a code reds here — the mechanical pin behind "validator
//     and apierr are the SSOT" (the catalog-sync test then forces the new code to be localized).
//   - transit_pool_cidr: allocconst.DefaultTransitCIDR — the SINGLE post-plan-8 transit-pool
//     authority (plan-8 single-sourced what used to be three duplicated consts). Pinning the one
//     source guards the value every consumer references.
//   - fe_field_lists: the F3 hand-mirrored FE field lists (PIN_FIELDS, ALLOCATION_PIN_FIELDS, and the
//     five *_OMITEMPTY lists). These mirror Go json tags / pin-field sets by hand; pinning their
//     contents makes a silent FE-side divergence a red build.
//
// Run `go test ./internal/conformance/ -run TestDrift -update` after an INTENTIONAL change to any
// pinned authority, review the diff, and commit the refreshed drift_manifest.json. A plain run (the
// gate + CI) asserts against the committed manifest and never rewrites it.

// The refresh path reuses the single package-level -update flag declared in golden_test.go
// (updateGolden), so the documented workflows behave intuitively: `go test ./internal/conformance/
// -run TestDrift -update` refreshes ONLY this manifest (the -run filter scopes which test runs), and
// `go test ./internal/conformance/ -update` (no filter) refreshes BOTH the golden corpus and this
// drift manifest in one pass. One flag, both refreshers — no second flag to register or confuse.

const driftManifestPath = "drift_manifest.json"

// Source paths are relative to this package directory (internal/conformance/).
const (
	validatorCodeSrcRel = "../validator/code.go"
	apierrCodeSrcRel    = "../apierr/apierr.go"

	// FE files live two levels up from internal/conformance/.
	feNormalizeEdges = "../../frontend/src/lib/normalizeEdges.ts"
	feTopologyStore  = "../../frontend/src/stores/topologyStore.ts"
	// The controller-store *_OMITEMPTY field lists live in the store's helpers module after the
	// framework-refactor plan-2 split of controllerStore.ts (the lists themselves are unchanged).
	feControllerHelpers = "../../frontend/src/stores/controller/helpers.ts"
)

// rendererTemplates is the set of scoped renderer template BLOCKS the manifest hashes. These are the
// SIX named const templates in internal/renderer/; deploy.go's inline b.WriteString blocks are NOT
// named consts and are deliberately excluded (pinning an unnamed inline block is brittle, and the
// plan calls for the scoped BLOCK, not the whole file). Each rendered byte these templates produce
// is frozen by the conformance golden corpus, so a template edit reds BOTH this manifest (loudly,
// naming the template) and the corpus goldens (showing the byte effect).
var rendererTemplates = []struct {
	file string
	name string
}{
	{"../renderer/babel.go", "babelConfigTemplate"},
	{"../renderer/sysctl.go", "sysctlConfigTemplate"},
	{"../renderer/wireguard.go", "wgPerPeerConfigTemplate"},
	{"../renderer/wireguard.go", "wgClientConfigTemplate"},
	{"../renderer/script.go", "installScriptTemplate"},
	{"../renderer/script.go", "clientInstallScriptTemplate"},
}

// DriftManifest is the pinned snapshot of every hand-synced authority. Field order is fixed and
// every map is rendered with sorted keys (encoding/json sorts map keys), so the committed JSON is
// byte-stable across runs and machines.
type DriftManifest struct {
	// RendererTemplateHashes maps the template const name -> sha256 hex of its scoped block string.
	RendererTemplateHashes map[string]string `json:"renderer_template_hashes"`

	// ValidatorCodes / ApierrCodes pin BOTH code systems: the live count + the full sorted set.
	ValidatorCodes CodeSet `json:"validator_codes"`
	ApierrCodes    CodeSet `json:"apierr_codes"`

	// TransitPoolCIDR pins allocconst.DefaultTransitCIDR — the single transit-pool authority.
	TransitPoolCIDR string `json:"transit_pool_cidr"`

	// FEFieldLists pins the F3 hand-mirrored FE field lists by name -> sorted field set.
	FEFieldLists map[string][]string `json:"fe_field_lists"`
}

// CodeSet is a pinned code system: the count (so a net add/remove is loud even at a glance) plus the
// full sorted list (so a rename — net-zero count — is caught too).
type CodeSet struct {
	Count int      `json:"count"`
	Codes []string `json:"codes"`
}

// constStringValue parses a Go source file and returns the unquoted string value of the named const.
// It uses go/ast so the pin survives formatting and line moves and reads exactly the source the
// renderer/validator/apierr compile from.
func constStringValue(t *testing.T, path, name string) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, id := range vs.Names {
				if id.Name != name || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("%s: const %s is not a string literal", path, name)
				}
				val, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s: unquote %s: %v", path, name, err)
				}
				return val
			}
		}
	}
	t.Fatalf("%s: const %s not found", path, name)
	return ""
}

// codeConstValues returns the sorted set of `<Name> Code = "<value>"` string-literal values in a Go
// source file (the validator and apierr code SSOTs). Mirrors the AST walk in
// internal/i18n_catalog_sync_test.go; kept local so the conformance package is self-contained.
func codeConstValues(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var codes []string
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			id, ok := vs.Type.(*ast.Ident)
			if !ok || id.Name != "Code" || len(vs.Values) != 1 {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("%s: unquote code const %q: %v", path, lit.Value, err)
			}
			codes = append(codes, val)
		}
	}
	if len(codes) == 0 {
		t.Fatalf("%s: parsed zero Code consts", path)
	}
	sort.Strings(codes)
	return codes
}

// feArrayElements extracts the string elements of a named `const <name> = [ ... ]` array literal in a
// TS file. The FE field lists are flat string arrays (single- or double-quoted, possibly spread over
// several lines, optionally ending `] as const`); this scopes to the slice between the declaration
// and its closing bracket and pulls every quoted element, returning them SORTED so element-reordering
// (which is not a contract change) never reds the pin while an add/remove/rename does.
func feArrayElements(t *testing.T, path, name string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	src := string(raw)

	declRE := regexp.MustCompile(`(?:export\s+)?const\s+` + regexp.QuoteMeta(name) + `\s*(?::[^=]+)?=\s*\[`)
	loc := declRE.FindStringIndex(src)
	if loc == nil {
		t.Fatalf("%s: array const %s not found", path, name)
	}
	rest := src[loc[1]:]
	end := -1
	for i := 0; i < len(rest); i++ {
		if rest[i] == ']' {
			end = i
			break
		}
	}
	if end < 0 {
		t.Fatalf("%s: array const %s has no closing bracket", path, name)
	}
	body := rest[:end]

	elemRE := regexp.MustCompile(`'([^']*)'|"([^"]*)"`)
	var out []string
	for _, m := range elemRE.FindAllStringSubmatch(body, -1) {
		if m[1] != "" {
			out = append(out, m[1])
		} else {
			out = append(out, m[2])
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s: array const %s parsed zero elements", path, name)
	}
	sort.Strings(out)
	return out
}

// buildDriftManifest assembles the live snapshot from every pinned source.
func buildDriftManifest(t *testing.T) DriftManifest {
	t.Helper()

	hashes := map[string]string{}
	for _, tm := range rendererTemplates {
		block := constStringValue(t, tm.file, tm.name)
		sum := sha256.Sum256([]byte(block))
		hashes[tm.name] = hex.EncodeToString(sum[:])
	}

	validatorCodes := codeConstValues(t, validatorCodeSrcRel)
	apierrCodes := codeConstValues(t, apierrCodeSrcRel)

	fe := map[string][]string{
		"PIN_FIELDS":                feArrayElements(t, feNormalizeEdges, "PIN_FIELDS"),
		"ALLOCATION_PIN_FIELDS":     feArrayElements(t, feTopologyStore, "ALLOCATION_PIN_FIELDS"),
		"PROJECT_OMITEMPTY":         feArrayElements(t, feControllerHelpers, "PROJECT_OMITEMPTY"),
		"DOMAIN_OMITEMPTY":          feArrayElements(t, feControllerHelpers, "DOMAIN_OMITEMPTY"),
		"NODE_OMITEMPTY":            feArrayElements(t, feControllerHelpers, "NODE_OMITEMPTY"),
		"EDGE_OMITEMPTY":            feArrayElements(t, feControllerHelpers, "EDGE_OMITEMPTY"),
		"PUBLIC_ENDPOINT_OMITEMPTY": feArrayElements(t, feControllerHelpers, "PUBLIC_ENDPOINT_OMITEMPTY"),
	}

	return DriftManifest{
		RendererTemplateHashes: hashes,
		ValidatorCodes:         CodeSet{Count: len(validatorCodes), Codes: validatorCodes},
		ApierrCodes:            CodeSet{Count: len(apierrCodes), Codes: apierrCodes},
		TransitPoolCIDR:        allocconst.DefaultTransitCIDR,
		FEFieldLists:           fe,
	}
}

// marshalDrift renders a DriftManifest as canonical JSON (sorted map keys via encoding/json, two-space
// indent, single trailing LF) — the same canonical style as the conformance Manifest serializer.
func marshalDrift(t *testing.T, m DriftManifest) []byte {
	t.Helper()
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("marshal drift manifest: %v", err)
	}
	return append(out, '\n')
}

// TestDrift asserts the live snapshot byte-equals the committed drift_manifest.json, or refreshes it
// when -update is set. Any unpinned drift — a touched renderer template, an added/removed/renamed
// validator or apierr code, a changed transit CIDR, or an edited FE field list — reds here with a
// first-divergence report locating the change.
func TestDrift(t *testing.T) {
	got := marshalDrift(t, buildDriftManifest(t))

	if *updateGolden {
		if err := os.WriteFile(driftManifestPath, got, 0o644); err != nil {
			t.Fatalf("write drift manifest: %v", err)
		}
		return
	}

	want, err := os.ReadFile(driftManifestPath)
	if err != nil {
		t.Fatalf("read drift manifest %s (run with -update to generate): %v", driftManifestPath, err)
	}
	if diff := FirstDivergence(want, got); diff != "" {
		t.Errorf("drift manifest %s is stale — a pinned authority changed without a refresh.\n"+
			"If the change was INTENTIONAL, run: go test ./internal/conformance/ -run TestDrift -update, review the diff, and commit.\n%s",
			driftManifestPath, diff)
	}
}
