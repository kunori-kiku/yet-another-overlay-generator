# STATUS
<!-- regenerated: 2026-06-15 -->
<!-- by: draft-implementation-plan (subject: signed-self-update-and-rc-hardening) -->

## Active work

- **Subject:** `signed-self-update-and-rc-hardening-2026_06_15` — **DRAFTED, awaiting execution** (10 plans;
  two release milestones: beta.1 = plans 1–8, beta.2 = plans 9–10). Combines mimic-from-GitHub install +
  signed agent self-update ("both now") + the full RC-hardening sweep (Units A+B+C). Refined via 3 all-Opus
  workflows (`wfhuw2hd8` design, `wxajvgzp5` comprehensiveness, on the `wiakgi4v5` RC survey).
- **Branch:** `plan/signed-self-update-and-rc-hardening` (plan docs, off main); `rc-hardening` carries the
  already-landed Apache-2.0 LICENSE+NOTICE (plan-1's first deliverable).
- **Released:** `v2.0.0-preview.10` (latest); the subject targets `beta.1` next, then `beta.2`; rc.1 a later
  owner call.
- **Current plan:** plan-1 (RC paperwork & trust) — pending; runs parallel to plan-2/6.

## Open questions / blockers

- **Locked owner decisions** (in the outline Decisions log): beta.1 excludes the self-update swap (→ beta.2);
  self-update = canary-then-fleet; full validator none-yet table; release.yml publishes agent SHAs now,
  bootstrap-TOFU hole deferred to rc.2; Apache-2.0; manual mimic catalog; air-gap omits `artifacts.json`.
- **Two manual hardware smokes** (two-node controller login/hydration; NAT sticky round-trip) — gate the
  beta.1 TAG (plan-8), not code-merge. Plus a self-update field smoke gates beta.2 (plan-10).
- No code blockers. `main` green; the subject is drafted, not executed.

## Next actions

1. **Execute plan-1** (RC paperwork & trust) — no code, runs parallel to everything; then plan-2 (FetchSettings
   byte-identical gate) → plan-3 (mimic) → plan-4 (version reporting), with plan-6 (robustness) in parallel.
2. Owner to run the two beta.1 hardware smokes when convenient (plan-8 gate).

## Recently closed subjects (last 3)

- `controller-nat-customization-2026_06_15` (2026-06-15, **delivered**) — controller mode made
  server-authoritative + operator-customizable at the NAT boundary: sticky operator-settable per-edge
  NAT port + transit IP, server-authoritative compile-preview (zero-knowledge), per-node `listen_port`
  removed (fixing the always-firing co-hosted overlap rule). PRs #98–#106; released `v2.0.0-preview.10`.
- `extensible-i18n-and-structural-hardening-2026_06_14` (2026-06-14, delivered) — extensible keyed
  i18n + coded-at-source HTTP error envelope (`internal/apierr`) + validator-finding localizer; deploy
  artifacts Englishized; perpetual CJK/bijection gates; post-audit security/robustness/mode-boundary +
  key-custody remediation (#70–#95).
- `controller-server-authority-redesign-2026_06_12` (2026-06-14, delivered) — server-authoritative
  controller mode, login gate, key custody, prefix split (#59–#65).
