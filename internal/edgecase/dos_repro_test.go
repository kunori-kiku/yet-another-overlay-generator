package edgecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// dos_repro_test.go — the DoS regression oracle (plan-16 / 3.4, Phase 4, DURABLE Go-API tier).
// Consumer: plan-8/1.8 (the S1 cap/context owner) and the post-rc.1 S2/S3 hardening roadmap.
//
// THREE reproductions, all measured directly against compiler.Compile (the durable tier survives
// the Subject-1 TS cutover that removes the anonymous /api/compile route; the today-only HTTP tier
// lives in dos_repro_http_test.go behind //go:build airgap):
//
//   - S1 allocator-cidr-reserved-cpu-blowup — a /8 domain has ~16.7M host candidates, over the
//     per-node scan budget. plan-8's cap (internal/allocator/ip.go: maxOverlayScanBudget = 1<<20)
//     ALREADY LANDED, so this is now a REGRESSION LOCK on the shipped fix: the compile must reject
//     fast with CodeOverlayScanBudgetExceeded, never run the multi-million-iteration scan. S1 was
//     the hard rc.1 blocker; this test pins it closed. The ctx-cancellation half of plan-8's fix
//     (the loop's periodic ctx.Err poll) is regression-locked by TestDoSAllocatorContextHonored.
//   - S2 unbounded-domains-and-reserved-ranges — domain/reserved-range counts are bounded only by
//     the 4 MiB body cap, not the node/edge schema bounds. Measured + bounded here; S2 hardening is
//     post-rc.1 roadmap (4.3:48). Pre-cap, this test ASSERTS BOUNDED (returns within the deadline)
//     and logs the cost; flip it to require a coded cap when post-rc.1 lands.
//   - S3 peers-gapfill-quadratic-parallel-links — gapFillTransitPair re-ParseCIDRs per iteration, so
//     many parallel backup links drive a quadratic transit-pair gap-fill. Measured + bounded; S3
//     hardening is post-rc.1 roadmap.
//
// All heavy cases are testing.Short()-gated so the CI fast lane never runs them; the full `go test
// ./...` lane does. No case may hang: dosDeadline is the hang tripwire.

// dosDeadline is the generous wall-clock ceiling for a single bounded compile. A compile that does
// not return before this is treated as an unbounded-cost regression (test failure), not a slow
// machine — it is set far above any real compile so a green machine never flakes.
const dosDeadline = 15 * time.Second

// codeOf extracts the apierr code from a (possibly %w-wrapped) compile error, or "" if the error is
// not an *apierr.Error. compiler.Compile wraps the allocator error with %w, so the chain is intact.
func codeOf(err error) apierr.Code {
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return ae.Code()
	}
	return ""
}

// measureBoundedCompile compiles topo under dosDeadline and returns (elapsed, err). It FAILS the
// test if the compile does not return before the deadline (the hang tripwire) — that is the single
// invariant every DoS repro shares: bounded cost. Per-repro assertions on top (e.g. S1's specific
// coded error) are layered by the caller. This is the helper plan-8/1.8 already flipped for S1 and
// that the post-rc.1 S2/S3 work flips next.
func measureBoundedCompile(t *testing.T, topo model.Topology) (time.Duration, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), dosDeadline)
	defer cancel()
	start := time.Now()
	_, err := Compile(ctx, topo)
	elapsed := time.Since(start)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("compile did not return within %s — unbounded-cost regression", dosDeadline)
	}
	return elapsed, err
}

// TestDoSAllocatorScanBudget (S1) — REGRESSION LOCK on plan-8's landed scan-budget cap. A /8 domain
// with reserved ranges + many nodes must reject fast with CodeOverlayScanBudgetExceeded.
func TestDoSAllocatorScanBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy DoS repro; skipped under -short (CI fast lane)")
	}
	topo := dosAllocatorReserved(64, 64) // /8, 64 nodes, 64 reserved /24s — far over the per-node budget
	elapsed, err := measureBoundedCompile(t, topo)
	t.Logf("S1 allocator /8+reserved (64 nodes): rejected in %s", elapsed)
	if got := codeOf(err); got != apierr.CodeOverlayScanBudgetExceeded {
		t.Fatalf("S1: want a %q coded rejection (plan-8 cap), got err=%v (code %q)", apierr.CodeOverlayScanBudgetExceeded, err, got)
	}
}

// TestDoSAllocatorContextHonored (S1, ctx half) — REGRESSION LOCK on plan-8's ctx-cancellation
// wiring. A pre-cancelled context must abort the compile promptly instead of running the scan to
// completion. This pins that ctx is threaded end-to-end through compiler.Compile into the allocator
// (the in-loop periodic poll is unit-tested in internal/allocator). The /12 domain is fully
// reserved, so without ctx the allocator would scan its whole ~1M host range to pool-exhaustion.
func TestDoSAllocatorContextHonored(t *testing.T) {
	d := dom("d1", "10.0.0.0/12")           // ~1M hosts: in-budget, so the scan loop would actually run
	d.ReservedRanges = []string{"10.0.0.0/12"} // every candidate reserved → full scan to exhaustion
	topo := model.Topology{Project: proj("dos-ctx"), Domains: []model.Domain{d}, Nodes: []model.Node{router("r1")}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel: the allocator's entry guard (ip.go) must short-circuit before scanning

	start := time.Now()
	_, err := Compile(ctx, topo)
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx-cancel: want context.Canceled propagated through Compile, got %v (after %s)", err, elapsed)
	}
	if elapsed > time.Second {
		t.Fatalf("ctx-cancel: compile took %s — the cancellation was not honored promptly", elapsed)
	}
	t.Logf("S1 ctx-cancel honored in %s", elapsed)
}

// TestDoSUnboundedDomains (S2) — many domains, each with reserved ranges + a node, drives one
// reserved-range-skipping allocation scan per domain. Bounded-by-body-cap today; measured + bounded
// here (post-rc.1 hardening, 4.3:48). Asserts no hang and logs the cost.
func TestDoSUnboundedDomains(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy DoS repro; skipped under -short (CI fast lane)")
	}
	const nDomains = 500
	elapsed, err := measureBoundedCompile(t, dosManyDomains(nDomains))
	t.Logf("S2 %d domains (8 reserved /24 + 1 node each): compiled in %s (err=%v)", nDomains, elapsed, err)
	if err != nil {
		t.Fatalf("S2: a large-but-legal many-domains topology should compile, got %v", err)
	}
}

// TestDoSGapFillAtScale (S3) — gap-fill at scale is reached via many DISTINCT links (a large star),
// NOT via parallel backups to one pair (those are capped first by the interface-name floor; see
// TestDegenerateBackupInterfaceCollision). A wide /16 transit lets all n links gap-fill without
// pool exhaustion. The cost is super-linear in links (gapFillTransitPair re-ParseCIDRs per
// iteration) but bounded by the 2000-node schema cap; measured + bounded here, post-rc.1 hardening
// (4.3:48). Asserts no hang and that the large-but-legal topology compiles.
func TestDoSGapFillAtScale(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy DoS repro; skipped under -short (CI fast lane)")
	}
	const nLinks = 1000 // 1001 nodes, under the 2000-node schema cap
	elapsed, err := measureBoundedCompile(t, dosStarGapFill(nLinks))
	t.Logf("S3 star with %d distinct links (/16 transit): compiled in %s (err=%v)", nLinks, elapsed, err)
	if err != nil {
		t.Fatalf("S3: a %d-link star over a /16 transit should compile, got %v", nLinks, err)
	}
}

// TestDegenerateBackupInterfaceCollision pins a real finding (C-class degenerate): parallel backup
// links to the SAME peer derive their WG interface name from one base plus a short hash, whose
// namespace collides by dosBackupInterfaceCollisionFloor links. Below the floor the topology
// compiles; at the floor the semantic validator rejects it LOUD (a non-nil, bounded, coded-direction
// error telling the operator to rename), never silently overwriting one interface with another and
// never hanging. This both documents the implicit per-peer parallel-backup fan-out limit and locks
// the loud-fail (a regression that silently accepted a collision would be a fleet-config-corruption
// class defect).
func TestDegenerateBackupInterfaceCollision(t *testing.T) {
	if _, err := Compile(context.Background(), dosBackupGapFill(dosBackupInterfaceCollisionFloor-1)); err != nil {
		t.Fatalf("%d parallel backups (below the collision floor) should compile, got %v", dosBackupInterfaceCollisionFloor-1, err)
	}
	_, err := Compile(context.Background(), dosBackupGapFill(dosBackupInterfaceCollisionFloor))
	if err == nil {
		t.Fatalf("%d parallel backups should be rejected on an interface-name collision, but compiled silently", dosBackupInterfaceCollisionFloor)
	}
	t.Logf("interface-name collision floor confirmed at %d parallel backups; rejected loud: %v",
		dosBackupInterfaceCollisionFloor, truncate(err.Error(), 120))
}

// truncate shortens a long error string for a readable test log line.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
