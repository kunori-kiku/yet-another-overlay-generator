package agent

import "github.com/kunorikiku/yet-another-overlay-generator/internal/version"

// compareVersions is the agent self-update ordering primitive (plan-9): the floor / downgrade /
// min-version checks compare reported versions through it. The SemVer-ish comparator itself was moved
// to internal/version (plan-8) so the controller's refuse-newer rollout guard and the agent's floor
// can never disagree on whether a target is "newer"; this thin re-export keeps every agent call site
// (and its tests) byte-unchanged. See internal/version/version.go for the precedence rules.
func compareVersions(a, b string) int {
	return version.Compare(a, b)
}
