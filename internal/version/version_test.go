package version

import "testing"

// TestCompare pins the SemVer-ish ordering primitive shared by the agent self-update floor and the
// controller refuse-newer rollout guard (plan-8) — including the pre-release cases a lexical compare
// gets wrong and the empty=minimal-sentinel legacy-agent rule. Moved from internal/agent/version_test.go.
func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		// Equality + leading-v / build-metadata tolerance.
		{"1.2.3", "1.2.3", 0},
		{"v1.2.3", "1.2.3", 0},
		{"1.2.3+build.7", "1.2.3", 0},
		// Numeric release ordering.
		{"1.2.3", "1.2.4", -1},
		{"1.2.10", "1.2.9", 1},
		{"1.10.0", "1.9.0", 1},
		{"2.0.0", "1.9.9", 1},
		{"1.2", "1.2.0", 0}, // missing patch defaults to 0
		// Pre-release is below its release.
		{"1.0.0-beta.1", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc.1", 1},
		// THE case lexical compare gets wrong: numeric pre-release fields compare numerically.
		{"2.0.0-beta.2", "2.0.0-beta.10", -1},
		{"2.0.0-beta.10", "2.0.0-beta.2", 1},
		{"2.0.0-beta.2", "2.0.0-beta.2", 0},
		// Alphanumeric vs numeric field, and more-fields-wins.
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1}, // numeric < alphanumeric
		{"1.0.0-alpha.beta", "1.0.0-beta", -1},    // lexical alpha < beta
		// Empty = minimal sentinel (legacy agents below any floor).
		{"", "", 0},
		{"", "0.0.1", -1},
		{"", "1.0.0-beta.1", -1},
		{"0.0.1", "", 1},
		// Whitespace tolerance.
		{" 1.2.3 ", "1.2.3", 0},
	}
	for _, tc := range cases {
		if got := Compare(tc.a, tc.b); got != tc.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		// Antisymmetry: Compare(b,a) must be the negation.
		if got := Compare(tc.b, tc.a); got != -tc.want {
			t.Errorf("Compare(%q, %q) = %d, want %d (antisymmetry)", tc.b, tc.a, got, -tc.want)
		}
	}
}
