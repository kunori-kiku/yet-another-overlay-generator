# STATUS
<!-- regenerated: 2026-06-16 -->
<!-- by: close-phase — subject: controller-panel-rollout-ui -->

## Active work

- **Delivered to `main`, tag pending (user-gated):** `controller-panel-rollout-ui-2026_06_16` — the
  operator-panel UI for the agent self-update + canary-then-fleet engine (closes the descoped plan-9
  "Canary UI"): the `AgentUpdateSettings` + `MimicCatalogSettings` config cards (with assisted
  release-pin pre-fill via the new operator `POST /release-pins`), a per-node update-status chip +
  opt-in live poll on the Fleet view, and the full-replace drop-on-save fix. All 6 plans merged
  (PRs #121–#125), each through an independent review workflow + fix + re-review. **Targets
  `v2.0.0-beta.3`** — the annotated tag push is the only remaining step and is owner-gated.
- **Last released:** **`v2.0.0-beta.2`** (GitHub *latest*) — signed agent self-update swap +
  canary-then-fleet. Built atop **`v2.0.0-beta.1`**. The `signed-self-update-and-rc-hardening-2026_06_15`
  subject is DELIVERED + CLOSED (PRs #109–#118; archived).

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

1. **Owner: push the annotated `v2.0.0-beta.3` tag** (the single remaining release step). `release.yml`
   then builds + creates the GitHub release + attaches assets; finish with
   `gh release edit v2.0.0-beta.3 --notes-file <notes> --latest`.
2. Owner: run the five owed smokes on real hardware/fleet when convenient.
3. Owner: once smokes pass + the beta soak is clean, cut `rc.1` (no new features; fixes only), then GA.
4. No drafted subject is awaiting execution.

## Recently closed subjects (last 3)

- `controller-panel-rollout-ui-2026_06_16` (2026-06-16, **delivered to main; beta.3 tag owner-gated**) —
  the operator-panel UI for signed agent self-update + canary-then-fleet (the descoped plan-9 Canary
  UI): agent + mimic config cards, assisted release-pin fetch (`POST /release-pins`, SSRF-guarded),
  per-node update-status chip + opt-in live poll, and the full-replace drop-on-save fix. PRs #121–#125.
- `signed-self-update-and-rc-hardening-2026_06_15` (2026-06-16, **delivered**) — beta.1 (mimic from
  GitHub with SHA-256-pinned `.deb` + signed `artifacts.json`, agent version reporting + build-version
  injection, full input validation + backend robustness, controller-mode UX/resilience, RC paperwork)
  and beta.2 (signed agent self-update + canary-then-fleet, verified-before-exec, brick-bounded).
  PRs #109–#118; released `v2.0.0-beta.1` then `v2.0.0-beta.2`.
- `controller-nat-customization-2026_06_15` (2026-06-15, delivered) — controller mode made
  server-authoritative + operator-customizable at the NAT boundary; sticky per-edge NAT port + transit
  IP, zero-knowledge compile-preview, per-node `listen_port` removed. PRs #98–#106; `v2.0.0-preview.10`.
