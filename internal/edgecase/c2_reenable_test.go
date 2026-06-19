package edgecase

import (
	"context"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/normalize"
)

// c2_reenable_test.go — the edgecase-suite regression lock on the C2 cross-link pin-collision
// behaviour (plan-16 / 3.4, Phase 3, step 6). It pins the SHIPPED two-part contract through the
// public compile path, so a regression in EITHER half is caught from the adversarial layer:
//
//  1. Loud-fail safety net: a topology where two DIFFERENT links pin the SAME transit IP is
//     rejected by the compiler's semantic validator — never silently compiled into a config where
//     one link's interface overwrites the other's. This is defense-in-depth and is STILL LIVE; the
//     heal is a convenience at the save/deploy/load seams, not the only guard.
//  2. Heal-then-compile: normalize.HealCollidingPins (the shipped beta.7 fix, also driven on
//     re-enable per plan-8 Phase 6.3) repairs the corruption in place, after which the same
//     topology compiles cleanly.
//
// The canonical heal-semantics tests (which edge is kept, fresh-pin reallocation, divergent
// incumbent) live in internal/compiler/reenable_heal_test.go and internal/validator/
// allocation_pins_test.go; this file does NOT duplicate them — it locks the reject→heal→compile
// DELTA from the adversarial corpus so the C2 auto-heal cannot silently regress to either a
// silent-accept (data corruption) or a heal that fails to clear the collision (broken deploy).

// crossLinkCollisionFragment is a stable substring of the CodePinTransitIPDuplicateCrossLink
// message ("transit IP pin {cidr} is occupied by two different links: ..."). The compiler
// stringifies semantic errors (%v) rather than wrapping the apierr, so a substring match on the
// rendered message is the right assertion here, not errors.As on the code.
const crossLinkCollisionFragment = "occupied by two different links"

func TestC2RawCollisionRejectedLoud(t *testing.T) {
	_, err := Compile(context.Background(), collidingCrossLinkPins())
	if err == nil {
		t.Fatal("a topology with two different links pinning the same transit IP must be rejected, but it compiled")
	}
	if !strings.Contains(err.Error(), crossLinkCollisionFragment) {
		t.Fatalf("want a cross-link transit-pin collision rejection (%q), got: %v", crossLinkCollisionFragment, err)
	}
}

func TestC2HealRepairsThenCompiles(t *testing.T) {
	topo := collidingCrossLinkPins()

	if !normalize.HealCollidingPins(&topo) {
		t.Fatal("HealCollidingPins reported no change on a known cross-link collision; expected it to strip one collider")
	}

	res, err := Compile(context.Background(), topo)
	if err != nil {
		t.Fatalf("after HealCollidingPins the topology must compile cleanly, got: %v", err)
	}
	if res == nil || len(res.PeerMap) == 0 {
		t.Fatalf("healed topology compiled to an empty result: %+v", res)
	}
	// And the heal is a stable fixed point: a second heal pass finds nothing left to repair.
	if normalize.HealCollidingPins(&topo) {
		t.Error("HealCollidingPins was not idempotent: a second pass still reported changes")
	}
}
