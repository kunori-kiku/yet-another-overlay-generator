package conformance

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// coverage_floor_test.go — the per-package statement-coverage FLOOR (plan-5 step 8).
//
// This is the load-bearing answer to the skeptic's "coverage is unbounded by construction": a
// frozen floor over every package of the local-compile pipeline, enforced on every CI run by the
// conformance job. Dropping a fixture, deleting a test, or adding an un-exercised Go branch that
// pushes a package below its floor reds the build, naming the package and the gap.
//
// Mechanism: for each package in coverage_floor.json the test shells out to
//
//	go test -count=1 -coverprofile=<tmp> ./internal/<pkg>/
//
// from the repo root, then reads the total statement coverage out of the profile and compares it
// to the frozen floor. It shells out (rather than relying on the parent `go test`'s own -cover) so
// the floor is self-contained: a single `go test ./internal/conformance/` enforces the whole
// pipeline's floor, exactly as the CI job invokes it, with no -coverpkg juggling in the workflow.
//
// internal/model is pure type / json-tag declarations with NO executable statements, so its profile
// is just `mode: set` with no per-statement lines and `go test -cover` prints `[no statements]`. The
// parser treats a profile with zero counted statements as a vacuous 100% (it cannot regress), which
// passes its floor of 0 — the package is still LISTED so a future executable statement landing in it
// without a test is visible, and the floor can be raised in the same edit.
//
// RE-BASELINE: this test has no -update path on purpose — the floor is a deliberate, reviewed value,
// not a snapshot to be silently refreshed. After an intentional coverage change, run
// `go test -cover ./internal/<pkg>/` for each package, edit coverage_floor.json to a couple points
// below the new measurement, and commit the diff.

// coverageFloors is the on-disk coverage_floor.json shape.
type coverageFloors struct {
	Doc    string             `json:"doc"`
	Floors map[string]float64 `json:"floors"`
}

const coverageFloorPath = "coverage_floor.json"

// repoRoot resolves the module root (two levels up from internal/conformance/) so the shelled-out
// `go test ./internal/<pkg>/` patterns and the coverprofile temp files resolve against the real
// module, independent of where the parent test process set its working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return abs
}

// loadCoverageFloors reads and parses coverage_floor.json, failing the test on any read/parse error
// or an empty floor set (an empty set would make the gate vacuously pass).
func loadCoverageFloors(t *testing.T) coverageFloors {
	t.Helper()
	raw, err := os.ReadFile(coverageFloorPath)
	if err != nil {
		t.Fatalf("read %s: %v", coverageFloorPath, err)
	}
	var cf coverageFloors
	if err := json.Unmarshal(raw, &cf); err != nil {
		t.Fatalf("parse %s: %v", coverageFloorPath, err)
	}
	if len(cf.Floors) == 0 {
		t.Fatalf("%s declares zero floors; the coverage gate would be vacuous", coverageFloorPath)
	}
	return cf
}

// packageCoverage runs `go test -count=1 -coverprofile` for one pipeline package from the repo root
// and returns its total statement-coverage percent. A package with no executable statements (e.g.
// internal/model — pure type declarations) yields a profile with no counted statements; that is a
// vacuous 100% (it cannot regress below any floor), reported as such so the no-statement case is an
// unambiguous pass rather than a parse failure.
func packageCoverage(t *testing.T, root, pkg string) float64 {
	t.Helper()
	profile := filepath.Join(t.TempDir(), pkg+".cover")
	pattern := "./internal/" + pkg + "/"

	cmd := exec.Command("go", "test", "-count=1", "-coverprofile="+profile, pattern)
	cmd.Dir = root
	// Inherit the environment (GOPROXY/GOSUMDB/PATH from CI or the local shell) so the child go
	// invocation resolves modules exactly as the parent did.
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go test %s failed (cannot measure coverage):\n%s", pattern, out)
	}

	return parseProfileTotal(t, profile, pkg)
}

// parseProfileTotal reads a coverprofile and returns the total statement-coverage percent over the
// package. The profile lines after the `mode:` header are
//
//	<file>:<startLine>.<startCol>,<endLine>.<endCol> <numStatements> <count>
//
// so the total covered fraction is (sum of numStatements where count>0) / (sum of numStatements). A
// profile with zero counted statements (no body lines, e.g. internal/model) is a vacuous 100%.
func parseProfileTotal(t *testing.T, profile, pkg string) float64 {
	t.Helper()
	raw, err := os.ReadFile(profile)
	if err != nil {
		t.Fatalf("read coverprofile for %s: %v", pkg, err)
	}
	var total, covered int
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("%s: malformed coverprofile line %q", pkg, line)
		}
		numStmts, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Fatalf("%s: parse statement count in %q: %v", pkg, line, err)
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil {
			t.Fatalf("%s: parse hit count in %q: %v", pkg, line, err)
		}
		total += numStmts
		if count > 0 {
			covered += numStmts
		}
	}
	if total == 0 {
		// No executable statements (pure declarations): coverage cannot regress, so it is a
		// vacuous pass against any floor.
		return 100.0
	}
	return float64(covered) / float64(total) * 100.0
}

// TestCoverageFloor enforces the per-package statement-coverage floor over the local-compile
// pipeline. For every package in coverage_floor.json it measures live coverage (by shelling out to
// `go test -coverprofile`) and fails if any package is below its frozen floor — the mechanical guard
// against silently un-exercised Go branches and dropped fixtures.
//
// Because it re-runs the entire pipeline test suite once per package (shelling out to `go test
// -coverprofile`), it would otherwise fire on every `go test ./...` — including the main `go` CI
// job and local inner-loop runs — double-running every pipeline suite. So it is GATED to the
// dedicated conformance job: it runs only when YAOG_CONFORMANCE_COVERAGE_FLOOR is set (the CI
// conformance-job step sets it), and is skipped everywhere else. To run it locally:
// `YAOG_CONFORMANCE_COVERAGE_FLOOR=1 go test ./internal/conformance/ -run TestCoverageFloor`.
const envCoverageFloor = "YAOG_CONFORMANCE_COVERAGE_FLOOR"

func TestCoverageFloor(t *testing.T) {
	if os.Getenv(envCoverageFloor) == "" {
		t.Skipf("coverage floor re-runs every pipeline package's tests; set %s=1 to run (the CI conformance job does)", envCoverageFloor)
	}
	cf := loadCoverageFloors(t)
	root := repoRoot(t)

	// Stable iteration order so the log reads the same on every run / machine.
	pkgs := make([]string, 0, len(cf.Floors))
	for pkg := range cf.Floors {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)

	for _, pkg := range pkgs {
		pkg := pkg
		floor := cf.Floors[pkg]
		t.Run(pkg, func(t *testing.T) {
			got := packageCoverage(t, root, pkg)
			if got+1e-9 < floor {
				t.Errorf("internal/%s coverage %.1f%% is below the frozen floor %.1f%%.\n"+
					"Add tests to restore coverage, or — if the drop is INTENTIONAL — lower the floor in %s (a couple points below the new measured value) and commit the diff.",
					pkg, got, floor, coverageFloorPath)
				return
			}
			t.Logf("internal/%s coverage %.1f%% >= floor %.1f%%", pkg, got, floor)
		})
	}
}
