# RC1-GATE — real-tunnel netns integration (plan-18 / 3.6)

This file records the status of the **MANDATORY rc.1-gating** real-tunnel integration tier and the
non-flake / negative-proof precondition that must be satisfied **before** the `v2.0.0-rc.1` tag is
cut. The tier lives in `test/realtunnel/` (see its `README.md` for how to run it).

## What the gate proves

Every other YAOG test asserts on bytes (golden-compared rendered configs). This tier asserts on the
**kernel**: it brings up the configs `cmd/compiler` actually generates (per-peer WireGuard + `babeld`
+ the **unmodified** `install.sh`) inside per-node `systemd-nspawn` containers on one runner, then
asserts the overlay works. The required floor is the **`simple-mesh` canary** (`TestSimpleMeshCanary`):

| # | Assertion | Status |
|---|-----------|--------|
| a | per-interface WireGuard handshake | ✅ |
| b | babel-converged kernel route to every node's `OverlayIP/32` | ✅ |
| c | end-to-end overlay ping, 0% loss | ✅ |
| d | SNAT transit→overlay source rewrite (rule installed **and** functionally rewriting) | ✅ |

`(d)` is a **full required floor assertion, not demoted.** Transit IPs are allocated `/32` (no shared
subnet), so a transit-sourced overlay ping's reply is routable back ONLY if egress SNAT rewrote the
source to the babel-announced overlay IP — the rewrite is load-bearing on every topology, so `(d)` is
functionally meaningful on the canary itself (no netns blocker, no Tier-2 demotion).

## Gate wiring

- **`ci.yml` → job `realtunnel`** (per PR): canary + nspawn lifecycle + the no-root template-shape
  pin (required); the additive scenarios run non-blocking (`continue-on-error`).
- **`release.yml` → job `gate-realtunnel`**: mirrors the canary so a tag can't ship code that would
  have failed PR CI (flows into the release DAG via `build-frontend`'s `needs`).
- **`realtunnel-bakein.yml`** (manual `workflow_dispatch`): the 20× non-flake bake-in + the negative
  proof — the rc.1 precondition recorded below.
- All three share `.github/actions/realtunnel-setup` so their setup stays identical.

## Phase-9 precondition — 20/20 bake-in + negative proof

A required gate must not be flaky. Before tagging `v2.0.0-rc.1`:

1. **Run the bake-in workflow** (`Actions → realtunnel-bakein → Run workflow`, default 20 runs) and
   require **20/20 green** for `TestSimpleMeshCanary`.
2. The same workflow runs the **negative proof** (`REALTUNNEL_NEGATIVE=drop-snat` →
   `TestNegativeProof`): it drops the SNAT rule and confirms the required SNAT assertion CATCHES the
   break (the test is GREEN only when the broken wire is detected on every node).
3. Record the run URL + result in the ledger below, then have plan-22 / 4.3 wire `realtunnel` as a
   distinct required status check (see owner actions).

### Ledger

| Date | Environment | Canary bake-in | Negative proof | Evidence |
|------|-------------|----------------|----------------|----------|
| 2026-06-20 | local (kernel 6.8, nested systemd-nspawn) | 3/3 green (canary 31–43s each) | ✅ drop-snat caught on all 3 nodes; unknown-fault typo fails loud | dev-box run; full suite (canary + C3 + relay + nat-hub + lifecycle + template-pin) green |
| _pending_ | CI `ubuntu-latest` (authoritative) | _run realtunnel-bakein, require 20/20_ | _included in the same workflow run_ | _paste the Actions run URL here before tagging rc.1_ |

The **CI `ubuntu-latest` run is authoritative** for the 20/20 precondition; the local 3/3 + negative
proof above establishes non-flakiness and teeth on the dev kernel. The CI bake-in has not yet been
run (it is owner-triggered) — that run is the last box to tick before rc.1.

## Deviations / reconciliations (recorded for review)

- **Execution = Option B (owner directive, 2026-06-18), not Option A.** The plan offered Option A
  (extract command lines from `install.sh` and run them) or Option B (run the **unmodified**
  `install.sh` under real `systemd` in `systemd-nspawn`). The owner chose **B** (higher fidelity).
  Consequently there is **no command extraction**; DoD #7's extraction half is N/A. The anti-drift
  guard it called for is still present as `TestTemplateShapePin`, which greps a freshly-rendered
  `install.sh` for the command shapes the harness's assertions depend on (`dummy0 type dummy`,
  `wg-quick up`, `babeld -c /etc/babel/`, the SNAT rule) and fails loud if `script.go` drifts.
- **C3 is already fixed (plan-8), so the C3 scenario guards the FIX, not the pre-fix bug.** The
  reverse-endpoint fallback no longer keys on the raw `has_public_ip`: `InferCapabilitiesFromRole`
  (roles.go) normalizes `HasPublicIP` up from `len(PublicEndpoints)>0` before `derivePeers`. The
  `testdata/c3-onedir` fixture therefore asserts the **post-fix** contract on the kernel: the
  endpoint-bearing peer (`public_endpoints` + `has_public_ip=false`) gets a **populated** reverse
  `Endpoint` (the normalization fired — revert it and the assertion goes red), while the genuinely
  unreachable peer (no `public_endpoints`) gets an **empty** one (correct one-directional). This is a
  regression guard for the C3 fix plus the legitimate empty-reverse-endpoint contract — strictly more
  valuable than reproducing a bug that no longer exists.
- **SNAT functional isolation requires polling, not adjacency.** Transit IPs are `/32`, so the SNAT
  probe is meaningful on any topology; the only subtlety is that SNAT-carried delivery needs the
  overlay route to have converged, so `requireSNATRewrite` polls (bounded, fails loud) rather than
  single-shotting.

## Owner actions before rc.1

- [ ] Run **`realtunnel-bakein`** on CI; require 20/20; paste the run URL into the ledger.
- [ ] **plan-22 / 4.3 (owner_flag):** list `realtunnel` (ci.yml job) as a **distinct required status
      check** on `main`, separate from `frontend-e2e`, and update branch protection + the gate doc.
      The release mirror (`gate-realtunnel`) is already in place.
- [ ] Confirm the CI `ubuntu-latest` runner can boot nested `systemd-nspawn --boot` (validated
      locally on 6.8; first green CI run confirms runner viability — if it cannot, the blocker + the
      Tier-2 fallback are documented in `test/realtunnel/README.md`).
