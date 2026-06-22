package agent

import "testing"

// TestCompareVersionsDelegates is a thin sanity check that the agent's compareVersions wrapper
// delegates to internal/version.Compare (the precedence rules themselves are pinned by
// internal/version/version_test.go, the comparator's home after the plan-8 single-sourcing move).
func TestCompareVersionsDelegates(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "2.0.0", -1},
		{"2.0.0-beta.10", "2.0.0-beta.2", 1}, // numeric pre-release ordering
		{"", "0.0.1", -1},                    // empty = minimal sentinel
		{"1.2.3", "1.2.3", 0},
	}
	for _, tc := range cases {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
