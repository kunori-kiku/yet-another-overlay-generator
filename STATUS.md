# STATUS
<!-- regenerated: 2026-06-16 -->
<!-- by: plan-8 (beta.1 closure) — subject: signed-self-update-and-rc-hardening -->

## Active work

- **Subject:** `signed-self-update-and-rc-hardening-2026_06_15` — **beta.1 DELIVERED, beta.2 PENDING**
  (10 plans across two release milestones). beta.1 = plans 1–8 (all merged + tagged); beta.2 = plans
  9–10 (the signed agent self-update *swap* + canary-then-fleet rollout, then subject close).
- **Released:** **`v2.0.0-beta.1`** (set as GitHub *latest*) — mimic-from-GitHub install, agent version
  *reporting*, full input validation, controller-mode UX & resilience, RC paperwork. rc.1 is a later
  owner call once the owed hardware smokes pass and the beta soak is clean.
- **Current plan:** **plan-9** — agent self-update mechanism + canary-then-fleet (the RISKY CORE; R1
  brick-a-fleet hazard). plan-10 then cuts beta.2 and closes the subject.
- `main` is green; plans 1–8 merged (PRs #109–#115).

## Open questions / blockers

- **Owed hardware smokes (owner-accepted risk).** Three beta.1 manual smokes gate the *tag*, not
  code-merge, and could not run here (no two-node hardware / browser authenticator / real Debian host):
  (1) two-node controller WebAuthn login → hydrated canvas + login-survives-refresh + no token in
  localStorage; (2) NAT sticky-pin Compile → edit port/transit IP → deploy → no drift; (3) mimic
  GitHub-`.deb` install on a kernel-≥6.1 Debian host. Recorded owed per `RELEASING.md`.
- A **self-update field smoke** (canary self-update + badge flip + tampered-hash refuse) will gate
  beta.2 (plan-10).
- **plan-9.5 insertion risk:** bounding the agent's `Restart=always` crash-loop may need a systemd
  unit-file change (`StartLimitBurst`) that ripples into the bootstrap renderer — the most probable wall.
- No code blockers.

## Next actions

1. **Execute plan-9** — `selfupdate.go`: verify a fetched agent binary against the signed
   `artifacts.json` pin (never the upstream `.sha256` sidecar), refuse downgrades below
   `AgentVersionFloor`, bound the crash-loop via a `PendingUpdate` breadcrumb, canary-then-fleet
   promotion; amend `PRINCIPLES.md` with the signed-self-update custody HIGH principle. Deepest review.
2. **Execute plan-10** — beta.2 closure: tag `v2.0.0-beta.2`, full close-phase (CLOSURE.md, archive the
   subject to `_completed/`, regenerate STATUS, memory update).
3. Owner to run the owed beta.1 hardware smokes when convenient.

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
