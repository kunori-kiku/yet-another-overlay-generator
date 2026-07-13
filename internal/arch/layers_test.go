package arch

import (
	"os/exec"
	"strings"
	"testing"
)

// modulePath is this repository's Go module path (see go.mod).
const modulePath = "github.com/kunorikiku/yet-another-overlay-generator"

// pureCore are the packages of the pure, stateless compile pipeline. They must import
// NOTHING stateful — directly or transitively. This is the framework's load-bearing
// boundary (PRINCIPLES.md: "Stateless compiler"; the quarantine that keeps local-mode
// compute pure and keeps the compiler minimal-dependency). Ordered pipeline-first for
// readability.
var pureCore = []string{
	"internal/model",
	"internal/allocconst",
	"internal/linkid",
	"internal/naming",
	"internal/allocator",
	"internal/validator",
	"internal/compiler",
	"internal/render",
	"internal/renderer",
	"internal/artifacts",
	"internal/normalize",
	"internal/localcompile",
}

// forbidden are the stateful package roots the pure core must never import. Each match is
// a boundary violation. internal/runtimecontract does not exist yet (framework-refactor
// plan-1 creates it, to which model.Condition is evicted); it is listed now so a future
// pure-core -> runtimecontract edge is a red build the moment it appears.
var forbidden = []string{
	"internal/controller",
	"internal/api",
	"internal/agent",
	"internal/runtimecontract",
}

// allow is the shrink-only allow-list of CURRENTLY-tolerated violations, keyed by the
// "<pureCorePkg> -> <statefulPkg>" edge (both module-relative). It is EMPTY today: no
// pure-core package forward-imports a stateful one (verified 2026-07-13). It may ONLY
// SHRINK — adding an entry to buy a green build is the anti-pattern this ratchet exists
// to stop; a reviewer must reject any growth. (The complementary reverse-placement guard
// — a pure leaf must not DEFINE stateful-only types — is separate from this
// import-direction test; see framework-refactor plan-1.)
var allow = map[string]bool{}

// buildTagSets are the build-tag contexts the ratchet evaluates. The pure core must hold
// the boundary under BOTH the default build AND the //go:build airgap build (the
// two-deployment split), so a future airgap-tagged pure-core file can never evade the check
// via a build context that `go list -deps` would otherwise skip. The forbidden-edge check
// runs over the UNION of the two dependency sets.
var buildTagSets = [][]string{
	nil,        // default build context
	{"airgap"}, // //go:build airgap build context
}

// TestPureCoreImportsNothingStateful walks each pure-core package's transitive dependency
// set — under both the default and the airgap build contexts (unioned) — and fails on any
// edge into a forbidden stateful package that is not in the shrink-only allow-list.
func TestPureCoreImportsNothingStateful(t *testing.T) {
	for _, pkg := range pureCore {
		full := modulePath + "/" + pkg
		deps := map[string]bool{}
		for _, tags := range buildTagSets {
			args := []string{"list", "-deps"}
			if len(tags) > 0 {
				args = append(args, "-tags", strings.Join(tags, ","))
			}
			args = append(args, full)
			out, err := exec.Command("go", args...).CombinedOutput()
			if err != nil {
				t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
			}
			for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				deps[strings.TrimSpace(dep)] = true
			}
		}
		for dep := range deps {
			if !strings.HasPrefix(dep, modulePath+"/") {
				continue // stdlib or external dep — not our concern here
			}
			rel := strings.TrimPrefix(dep, modulePath+"/")
			for _, f := range forbidden {
				if rel == f || strings.HasPrefix(rel, f+"/") {
					edge := pkg + " -> " + rel
					if allow[edge] {
						continue
					}
					t.Errorf("pure/stateful boundary violation: %s\n\t"+
						"the pure compile core must not import stateful code. If this is a "+
						"genuinely-required, invariant-safe edge, add %q to `allow` AND get a "+
						"reviewer sign-off — but the list may only SHRINK.", edge, edge)
				}
			}
		}
	}
}
