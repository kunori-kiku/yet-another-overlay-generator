# Closure — signed-self-update-and-rc-hardening

**Status: DELIVERED. Closed 2026-06-16. All 10 plans (PRs #109–#118) merged to `main`, in
order, each independently reviewed before merge; two releases cut and set *latest*:
`v2.0.0-beta.1` then `v2.0.0-beta.2`.**

Took YAOG from `v2.0.0-preview.10` to a tagged **beta.1** then **beta.2** by delivering three
intertwined streams: (1) **mimic from GitHub** — a distro-first → SHA-256-pinned-`.deb`-from-GitHub
install ladder for the distros that do not yet package mimic; (2) **signed agent self-update**
("both now": the agent binary AND its dependencies), staged so the *observable* half (version
reporting) shipped in beta.1 and the *mutable* half (the verified binary swap + canary-then-fleet
rollout) shipped in beta.2; (3) **RC hardening** — the full Units A+B+C sweep (legal/docs/CI;
controller-mode UX & resilience; backend robustness & full input validation).

## Shipped (PRs)

| Plan | PR | Merge | Milestone | What |
|------|----|-------|-----------|------|
| 1 | [#109](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/109) | `bb6fe75` | beta.1 | Apache-2.0 LICENSE+NOTICE, CHANGELOG, RELEASING; CI `gofmt` gate; `validation.md` made honest. |
| 2 | [#110](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/110) | `9c7f398` | beta.1 | `render.FetchSettings` threaded through the single render path; zero value byte-identical (perpetual gate). |
| 3 | [#111](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/111) | `2b2eb71` | beta.1 | mimic GitHub-`.deb` install ladder + signed `artifacts.json` (a `bundleFiles` member); custody fix (read pin from `$SCRIPT_DIR`). |
| 4 | [#112](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/112) | `e3a24f4` | beta.1 | Agent version reporting + `-X main.BuildVersion` ldflags; `version` subcommand; per-arch agent `.sha256` sidecars; panel version badge. |
| 5 | [#113](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/113) | `93b26bf` | beta.1 | Controller-mode UX & resilience: shared `localizeError` (no raw `<status> <JSON>`), `ErrorBoundary`, revoke confirm. |
| 6 | [#114](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/114) | `3a41653` | beta.1 | Full input validation (`transit_cidr`, `endpoint_host`/`public_endpoints[].host` charset), `/enroll` throttle, graceful shutdown, bounded audit log, Docker HEALTHCHECK, topology count bound, schema-version guard. |
| 7 | [#115](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/115) | `f129b1b` | beta.1 | Air-gap mimic catalog input (`YAOG_ARTIFACT_CATALOG` + compiler flags); honest mimic install/trust docs. |
| 8 | [#116](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/116) | `c32c25c` | beta.1 | beta.1 closure: CHANGELOG/STATUS/outline roll; tag `v2.0.0-beta.1` (latest). |
| 9 | [#117](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/117) | `d45fc02` | beta.2 | **RISKY CORE:** signed agent self-update + canary-then-fleet; verified-before-exec swap; brick-bounded reconcile; PRINCIPLES amend + `agent-selfupdate.md`. |
| 10 | [#118](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/118) | — | beta.2 | beta.2 closure: CHANGELOG/STATUS/outline roll; tag `v2.0.0-beta.2` (latest); this CLOSURE.md; subject archive. |

## The trust argument (signed `artifacts.json`)

Both new fetch surfaces — the mimic GitHub `.deb` and the agent self-update binary — are pulled over
UNTRUSTED transport (github.com / a `GH_PROXY` mirror). Their integrity rests entirely on a SHA-256
pin that inherits the **existing** signature chain, adding **no new trust primitive**:

```
sha256 pin  ∈  artifacts.json  ∈  bundleFiles  ∈  checksums.sha256  ∈  bundle.sig (Ed25519)  ∈  keystone trust-list
```

`artifacts.json` is a `bundleFiles` member, so it is in the canonical `checksums.sha256` the controller
signs and the off-host keystone trust-list binds. `VerifyBundle` was hardened to **refuse a present
`artifacts.json` that is not covered by `checksums.sha256`** (closing a smuggled-pin vector), so by the
time `install.sh` or the agent reads a pin, it is the controller-signed, keystone-bound pin. The agent
verifies the downloaded bytes against THAT pin before exec — never the upstream `.sha256` sidecar — and
never downgrades below a health-confirmed floor. Self-update touches only binaries, never WireGuard
private keys, so the zero-knowledge key-custody guarantee is unaffected. Air-gap byte-identity holds:
with no catalog/rollout configured, `artifacts.json` is omitted and the bundle is byte-identical
(perpetual `render` equivalence/signing gates).

## Process

Every PR: structure-aware implementation → local gate (`go build/vet/test ./...` + `gofmt`; frontend
`lint`+`build`) → CI green → an **independent multi-agent review workflow** before merge → fix →
re-review if anything was fixed → merge (merge commit, the repo convention). Highlights:

- **plan-6** review caught 1 MAJOR (a torn trailing JSONL line could brick the audit log AND node
  enrollment) + 6 MINOR/NIT — all fixed (trailing-line tolerance + self-heal) and re-reviewed.
- **plan-9 (RISKY CORE)** went through a 4-dimension **deep review** (23 agents) that confirmed **12
  defects** — genuine fleet-brick and anti-downgrade-custody bugs (the root cause: `recordSuccess`/
  `recordFailure` rebuilt `State` and dropped the floor + breadcrumb) — a **re-review** that caught
  R1-1 still broken after the first fix, a **round-2 fix**, and a **final verification**
  (all_fixes_correct=true). The crash-loop was bounded in-agent (early-incremented breadcrumb +
  probationary promote + install-then-flip), so the plan-9.5 stop-loss (a systemd unit-file change)
  was **not** triggered.

## Verification at closure

Automated, green on merged `main` (`go build`/`vet`/`test ./...`, frontend `lint`+`build`, `gofmt`):

- **Custody (perpetual):** `internal/agent/selfupdate_test.go` — a hash-mismatch and a self-test
  failure BOTH refuse the swap/exec; a post-swap exec failure keeps the breadcrumb; the in-flight
  guard preserves `.bak` + `Attempts`.
- **Brick bound:** probation→finalize, health-fail rollback, probation-reboot resume, and crash-loop
  abandon-at-cap are all pinned; `compareVersions` table; the decision table.
- **Air-gap byte-identity (perpetual):** `internal/render` `TestAll_ZeroFetchSettings_OmitsArtifactsJSON`
  + equivalence/signing gates; per-node agent-block gating test.
- **Validators (beta.1):** `internal/validator/plan6_safety_test.go` (host charsets, transit_cidr,
  count bound short-circuit, schema-version fail-closed); audit-bound + throttle + shutdown tests.
- en/zh i18n bijection enforced by `tsc`.

## What is owed / deferred (not defects)

**Owed manual smokes (owner-accepted risk; gate rc.1, not code-merge)** — no live hardware/fleet/browser
authenticator in the build environment:
1. Two-node controller WebAuthn login → hydrated canvas, login-survives-refresh, no token in localStorage.
2. NAT sticky-pin Compile → edit port/transit IP → deploy → no drift.
3. mimic GitHub-`.deb` install on a kernel-≥6.1 Debian host.
4. Self-update field smoke (canary swap + badge flip + tampered-hash refuse + crash-rollback).

**Deferred to rc.2/GA (documented, not built):** the bootstrap-TOFU hole (the agent's first binary is
fetched without a pre-shared pin); the FileStore SPOF (one global mutex + a 200ms generation poll) fix;
the full wiki rewrite (a redirect banner ships now); a frontend test runner.

**Descoped deliverables surfaced by the post-close audit (2026-06-16)** — recorded here so the scope-down
is visible, not buried in a spec footnote:
- **Canary UI (plan-9 step 8) — not built.** The promised panel surface (a per-node
  "update pending/applied/failed" chip + an in-panel target-version/canary editor) was descoped to the
  **mimic-catalog precedent**: agent self-update is configured via the operator `POST /api/v1/operator/settings`
  API (`TargetAgentVersion`, `AgentBins`, `AgentCanaryNodeIDs`, `AgentRolloutFleetWide`), and observed via the
  plan-4 per-node version badge (`NodeRegistry.tsx` / `FleetNodeDetailPage.tsx`), which flips on a successful
  swap. PR #117 touched zero frontend files. A dedicated canary-progress widget + in-panel pin editor is a
  follow-up (build vs. defer is an open owner decision), not an rc.1 blocker.
- **beta.1 release notes (plan-8 step 2) — partial.** The published `v2.0.0-beta.1` notes cover the beta.1
  scope but omit the prior controller-nat subject (#98–#106) closure that step 2 asked them to cover; the
  CHANGELOG attributes #97–#106 to the separate `[2.0.0-preview.10]` section. Cosmetic; the release body may be
  amended.

(The stale `docs/spec/compiler/validation.md` "Compliance" prose — table rows flipped to `schema`/`semantic`
while the prose below still said "validated nowhere / MUST cover" — was a third audit finding and is **fixed**
in this same change, so it is not owed.)

**rc.1 is a later owner call** once the four owed smokes pass and the beta soak is clean.

## Pointers

- Outline + full decisions log: `outline.md` (this folder).
- Specs: `docs/spec/controller/agent-selfupdate.md` (new), `docs/spec/artifacts/mimic.md`,
  `docs/spec/controller/persistence.md` (audit bound + SPOF note), `docs/spec/compiler/validation.md`,
  `PRINCIPLES.md` (HIGH self-update custody invariant).
- Memory: [[agent-self-update-signed-verification]] (marked SHIPPED).
- Perpetual guards (never retire): `internal/agent/selfupdate_test.go` (custody + brick bound),
  `internal/render` byte-identity gates, `internal/validator/plan6_safety_test.go`.
