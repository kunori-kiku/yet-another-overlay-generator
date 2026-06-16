# STATUS
<!-- regenerated: 2026-06-16 -->
<!-- by: plan-10 (subject close) — subject: signed-self-update-and-rc-hardening -->

## Active work

- **None in flight.** The `signed-self-update-and-rc-hardening-2026_06_15` subject is **DELIVERED and
  CLOSED** (all 10 plans merged, PRs #109–#118; archived to `implementation_plans/_completed/`).
- **Released:** **`v2.0.0-beta.2`** (GitHub *latest*) — the signed agent self-update swap +
  canary-then-fleet rollout. Built atop **`v2.0.0-beta.1`** (mimic-from-GitHub install, agent version
  reporting, full input validation, controller-mode UX, RC paperwork).

## Open questions / blockers

- **Owed manual smokes (owner-accepted risk), gate rc.1 — not code-merge:**
  1. Two-node controller WebAuthn login → hydrated canvas + login-survives-refresh + no token in
     localStorage (beta.1).
  2. NAT sticky-pin Compile → edit port/transit IP → deploy → no drift (beta.1).
  3. mimic GitHub-`.deb` install on a kernel-≥6.1 Debian host (beta.1).
  4. **Self-update field smoke (beta.2):** canary agent version → download/verify/swap/re-exec →
     badge flips → promote to fleet; tampered hash refused keep-last-good; crashing binary rolls back
     within the attempt cap. The mechanism is extensively unit-tested + deep-reviewed; the live
     end-to-end run is owed (no two-node fleet available in the build environment).
- **rc.1 is a later owner call** once the four owed smokes pass and the beta soak is clean.
- **Deferred to rc.2/GA** (documented, not built): the bootstrap-TOFU hole (the agent's first binary
  is fetched without a pre-shared pin), the FileStore SPOF (global mutex + 200ms generation poll) fix,
  the full wiki rewrite, and a frontend test runner.
- **Descoped deliverables surfaced by the 2026-06-16 post-close audit** (now tracked, see
  `CLOSURE.md` "Descoped deliverables"): (a) the plan-9 **Canary UI** (per-node update-status surface +
  in-panel target-version/canary editor) was not built — agent self-update is configured via
  `POST /api/v1/operator/settings` and observed via the plan-4 version badge; a canary-progress widget
  is a follow-up (build vs. defer is an open owner decision). (b) the `v2.0.0-beta.1` release notes omit
  the prior #98–#106 closure (cosmetic; body may be amended). The stale `validation.md` compliance prose
  (a third finding) was **fixed** in the post-audit doc change.
- No code blockers. `main` is green.

## Next actions

1. Owner: run the four owed smokes on real hardware/fleet when convenient.
2. Owner: once smokes pass + beta soak is clean, cut `rc.1` (no new features; fixes only), then GA.
3. No drafted subject is awaiting execution.

## Recently closed subjects (last 3)

- `signed-self-update-and-rc-hardening-2026_06_15` (2026-06-16, **delivered**) — beta.1 (mimic from
  GitHub with SHA-256-pinned `.deb` + signed `artifacts.json`, agent version reporting + build-version
  injection, full input validation + backend robustness, controller-mode UX/resilience, RC paperwork)
  and beta.2 (signed agent self-update + canary-then-fleet, verified-before-exec, brick-bounded).
  PRs #109–#118; released `v2.0.0-beta.1` then `v2.0.0-beta.2`.
- `controller-nat-customization-2026_06_15` (2026-06-15, delivered) — controller mode made
  server-authoritative + operator-customizable at the NAT boundary; sticky per-edge NAT port + transit
  IP, zero-knowledge compile-preview, per-node `listen_port` removed. PRs #98–#106; `v2.0.0-preview.10`.
- `extensible-i18n-and-structural-hardening-2026_06_14` (2026-06-14, delivered) — extensible keyed
  i18n + coded-at-source HTTP error envelope (`internal/apierr`) + validator-finding localizer; deploy
  artifacts Englishized; perpetual CJK/bijection gates; post-audit hardening (#70–#95).
