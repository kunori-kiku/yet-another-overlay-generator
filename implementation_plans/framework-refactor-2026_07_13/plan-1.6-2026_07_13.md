# plan-1.6 — lock the air-gap handler pre-heal (regression test)

**Goal:** Add a regression test proving the plan-1 air-gap pre-heal (`internal/api/handler_airgap.go`
`airGapRequest`) actually heals a colliding-pin topology — so the anonymous `/compile` route converges on
the SAME healed success the controller produces, and the fix cannot SILENTLY regress back to the original
divergence (an un-tested one-line pre-heal is exactly the class of gap plan-1 closed).

**Prerequisites:** plan-1 (the air-gap handler pre-heal). **Insertion point** (split from plan-1 during
execution): the FIX is done + verified (conformance passes, builds clean, mirrors the proven controller
pre-heal), but a robust handler-level regression test needs a colliding topology WITH keys driven through
the air-gap `/compile` route — its own focused increment.

## Design (assessment recap)
The conformance FAIL fixture `heal-collision-reenable` already locks the OTHER half of the C2 design:
`localcompile`'s validator LOUD-rejects a raw collision (the safety net; unchanged by plan-1). What plan-1
ADDED is the air-gap handler pre-heal — and nothing yet asserts that `/compile` heals rather than loud-fails
a colliding input. The heal itself is proven (`internal/edgecase/c2_reenable_test.go`
`TestC2HealRepairsThenCompiles`); this locks that it is APPLIED at the air-gap boundary.

## Changes
- New test (airgap-tagged, `internal/api`): build a colliding-pin topology (two distinct links pinning the
  same transit IP — mirror `edgecase.collidingCrossLinkPins`, with valid node keys) and drive it through
  the air-gap `/compile` handler (or `h.airGapRequest` + `localcompile.CompileResultCtx`). Assert a
  SUCCESSFUL healed compile (HTTP 200 / no `validation_pin_*_duplicate_cross_link`), and that a second
  `HealCollidingPins` over the request topology reports NOTHING to heal (already repaired) — the two-sided
  proof that the pre-heal ran.
- Consider a tiny shared colliding-topology helper if `collidingCrossLinkPins` is worth exporting from
  `edgecase` (vs replicating); keep it minimal.

## Verify + branch
`go test -tags airgap ./internal/api/`; full Go suite green; the `heal-collision-reenable` fail fixture
still PASSES (unchanged). Branch `refactor/plan-1.6-airgap-heal-lock`.

## Tests produced
- The air-gap `/compile` heal-regression test — **perpetual** — guards that the anonymous compute path
  pre-heals (the divergence stays closed). Retirement: never.

## Invariants at risk
- **[1] air-gap == controller parity:** the test must assert a HEALED SUCCESS, not just "no error" — a
  colliding topology that silently compiled WRONG (no heal) would also be "no error" at the wrong layer.

## Stop-loss
If the crafted topology does not actually collide (a plain compile with no heal also passes), the test is
vacuous — verify by temporarily removing the `airGapRequest` pre-heal and observing the test FAIL (a loud
`validation_pin_*` rejection), then restore.

## Out of scope
Any heal code change (done in plan-1); the export single-source (plan-1.5); the `localcompile` loud safety
net (unchanged — the `heal-collision-reenable` fixture owns it).
