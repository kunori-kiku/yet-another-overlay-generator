// Package version provides the single SemVer-ish comparator shared by the agent self-update ordering
// (floor / anti-downgrade / min-version) and the controller's refuse-newer rollout guard (plan-8), so
// the two can never disagree on whether a target is "newer". It was moved verbatim from
// internal/agent/version.go (which now re-exports Compare via a thin wrapper) to single-source it.
package version

import (
	"strconv"
	"strings"
)

// Compare compares two SemVer-ish version strings, returning -1, 0, or 1. It implements enough of
// SemVer 2.0.0 precedence for the floor / downgrade / min-version checks and the controller's
// refuse-newer guard:
//
//   - an OPTIONAL leading "v" is tolerated; build metadata ("+...") is ignored;
//   - numeric major.minor.patch compared NUMERICALLY (missing fields default to 0);
//   - a PRE-RELEASE version is lower than its release (1.0.0-beta < 1.0.0);
//   - pre-release identifiers compare field-by-field: numeric NUMERICALLY (so "beta.2" <
//     "beta.10" — the case a lexical compare gets wrong), alphanumeric lexically, a numeric
//     field is lower than an alphanumeric one, and when all shared fields are equal the
//     version with MORE fields wins.
//
// The EMPTY string is the MINIMAL sentinel — below every real version, equal only to itself — so a
// legacy agent that reports no version is treated as below any floor and self-updates rather than
// being frozen out (plan-9 Addition 4a).
func Compare(a, b string) int {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == b {
		return 0
	}
	if a == "" {
		return -1
	}
	if b == "" {
		return 1
	}

	relA, preA := splitVersion(a)
	relB, preB := splitVersion(b)
	if c := compareReleaseCore(relA, relB); c != 0 {
		return c
	}
	// Same release core: a version WITH a pre-release ranks below one WITHOUT.
	switch {
	case preA == "" && preB == "":
		return 0
	case preA == "":
		return 1
	case preB == "":
		return -1
	default:
		return comparePrerelease(preA, preB)
	}
}

// splitVersion strips a leading "v" and build metadata, returning (releaseCore, prerelease).
func splitVersion(v string) (release, pre string) {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i] // drop build metadata
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

// compareReleaseCore compares dotted numeric cores ("1.2.3"); missing fields are 0.
func compareReleaseCore(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		ai, bi := 0, 0
		if i < len(as) {
			ai = atoiOrZero(as[i])
		}
		if i < len(bs) {
			bi = atoiOrZero(bs[i])
		}
		if ai != bi {
			if ai < bi {
				return -1
			}
			return 1
		}
	}
	return 0
}

// comparePrerelease compares dot-separated pre-release identifiers per SemVer precedence.
func comparePrerelease(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	n := len(as)
	if len(bs) < n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		if c := comparePreField(as[i], bs[i]); c != 0 {
			return c
		}
	}
	// All shared fields equal: more identifiers ⇒ higher precedence.
	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	default:
		return 0
	}
}

// comparePreField compares one pre-release identifier: numeric vs numeric numerically,
// numeric below alphanumeric, alphanumeric vs alphanumeric lexically (ASCII).
func comparePreField(a, b string) int {
	an, aerr := strconv.Atoi(a)
	bn, berr := strconv.Atoi(b)
	aNum, bNum := aerr == nil, berr == nil
	switch {
	case aNum && bNum:
		if an != bn {
			if an < bn {
				return -1
			}
			return 1
		}
		return 0
	case aNum: // numeric < alphanumeric
		return -1
	case bNum:
		return 1
	default:
		if a != b {
			if a < b {
				return -1
			}
			return 1
		}
		return 0
	}
}

// atoiOrZero parses a release-core field, defaulting to 0 on any non-numeric input so a
// malformed version never panics the comparator (it degrades to a 0 field).
func atoiOrZero(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
