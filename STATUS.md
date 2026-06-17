# STATUS
<!-- regenerated: 2026-06-17 -->
<!-- by: keystone-rotation-safety subject -->

## Active work

- **In progress — keystone-rotation safety (untagged; on `main` + branch `test/keystone-regression-suite`).**
  Reproduced and fixed the root cause where rotating the off-host operator credential silently
  stranded the whole fleet: a changed credential now requires an acknowledged rotation, the
  controller exposes a server-truth `redeploy_required` signal, and the agent gains
  `reprovision-keystone` (PRs #129/#130/#131, on `main`). A non-release adversarial regression suite
  (`internal/regression`) then surfaced three adjacent trust-list-serving bugs — all fixed on
  `test/keystone-regression-suite`: the **served-vs-staged trust-list split** (a mid-deploy re-stage
  no longer bricks `/config`), a **monotonic anti-rollback floor** across a keystone-OFF apply, and an
  **atomic `GetServedConfig`** snapshot (no torn bundle/manifest pair); plus
  `keystone_no_signed_manifest` reclassified 500→409. Reviewed → fixed → re-reviewed by independent
  multi-agent workflows; full suite + `-race` + `vet` + `gofmt` green. See CHANGELOG `[Unreleased]`.
- **Released:** **`v2.0.0-beta.4`** (GitHub *latest*) — a security hardening fix (PR #128): the
  controller persists the bundle-signing **public** key per tenant (`SigningAnchor`) and reconciles
  it at stage time, so a redeploy that drops or swaps `YAOG_BUNDLE_SIGNING_KEY` now FAILS LOUD
  (`signing_key_missing` 412 / `signing_key_mismatch` 409) instead of silently shipping unsigned
  bundles. Trust-on-first-use; rotation via `YAOG_BUNDLE_SIGNING_KEY_ROTATE`; private key stays
  off-host; pin/rotate audited; air-gap export unchanged. Reviewed (5 findings) → fixed → re-review clean.
- **Prior releases:** **`v2.0.0-beta.3`** — the operator-panel UI for agent self-update +
  canary-then-fleet (the descoped plan-9 "Canary UI"): agent + mimic config cards, assisted
  release-pin fetch (`POST /release-pins`, SSRF-guarded), per-node update-status chip + opt-in live
  poll, the full-replace drop-on-save fix (PRs #121–#126). Atop **`v2.0.0-beta.2`** /
  **`v2.0.0-beta.1`** (`signed-self-update-and-rc-hardening`, PRs #109–#118).

## Open questions / blockers

- **Owed manual smokes (owner-accepted risk), gate rc.1 — not code-merge:**
  1. Two-node controller WebAuthn login → hydrated canvas + login-survives-refresh + no token in
     localStorage (beta.1).
  2. NAT sticky-pin Compile → edit port/transit IP → deploy → no drift (beta.1).
  3. mimic GitHub-`.deb` install on a kernel-≥6.1 Debian host (beta.1).
  4. **Self-update field smoke (beta.2):** canary agent version → download/verify/swap/re-exec →
     badge flips → promote to fleet; tampered hash refused keep-last-good; crashing binary rolls back
     within the attempt cap. Mechanism unit-tested + deep-reviewed; the live end-to-end run is owed.
  5. **Panel rollout-UI smoke (beta.3):** in controller mode, the agent + mimic config cards render,
     "Assist from GitHub release" pre-fills pins, a bootstrap-field save round-trips the rollout/mimic
     config (drop-on-save), fleet-wide gates on the confirm, the per-node chip shows
     pending→applying→applied as a canary advances, and the Live poll stops on logout. No FE test
     runner exists, so this is owner-verified in a browser.
- **rc.1 is a later owner call** once the owed smokes pass and the beta soak is clean.
- **Deferred to rc.2/GA** (documented, not built): the bootstrap-TOFU hole (the agent's first binary
  is fetched without a pre-shared pin); the FileStore SPOF (global mutex + 200ms generation poll) fix;
  a reliable *persistent* per-node `failed` update-state (would need a positive agent-reported field —
  the chip's `failed` is best-effort/transient today); the full wiki rewrite; a frontend test runner.
- No code blockers. `main` is green.

## Next actions

1. Owner: run the five owed smokes on real hardware/fleet when convenient.
2. Owner: once the smokes pass + the beta soak is clean, cut `rc.1` (no new features; fixes only), then GA.
3. No drafted subject is awaiting execution.

## Recently closed subjects (last 3)

- `controller-panel-rollout-ui-2026_06_16` (2026-06-16, **released `v2.0.0-beta.3`, GitHub latest**) —
  the operator-panel UI for signed agent self-update + canary-then-fleet (the descoped plan-9 Canary
  UI): agent + mimic config cards, assisted release-pin fetch (`POST /release-pins`, SSRF-guarded),
  per-node update-status chip + opt-in live poll, and the full-replace drop-on-save fix. PRs #121–#126.
- `signed-self-update-and-rc-hardening-2026_06_15` (2026-06-16, **delivered**) — beta.1 (mimic from
  GitHub with SHA-256-pinned `.deb` + signed `artifacts.json`, agent version reporting + build-version
  injection, full input validation + backend robustness, controller-mode UX/resilience, RC paperwork)
  and beta.2 (signed agent self-update + canary-then-fleet, verified-before-exec, brick-bounded).
  PRs #109–#118; released `v2.0.0-beta.1` then `v2.0.0-beta.2`.
- `controller-nat-customization-2026_06_15` (2026-06-15, delivered) — controller mode made
  server-authoritative + operator-customizable at the NAT boundary; sticky per-edge NAT port + transit
  IP, zero-knowledge compile-preview, per-node `listen_port` removed. PRs #98–#106; `v2.0.0-preview.10`.
