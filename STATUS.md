# STATUS
<!-- regenerated: 2026-06-09 (panel-appshell-redesign drafted, awaiting approval) -->
<!-- by: draft-implementation-plan -->

## Active work

- **Subject:** panel-appshell-redesign-2026_06_09 — **DRAFTED, awaiting user approval to build.**
  Restructure the operator panel (a PoC) into a dashboard app-shell (collapsible sidebar + top app
  bar + top-right user/theme menu + selection-driven right aside), Apple-minimal styling (auto
  dark/light, theme-scoped accent, optional translucency), persisted mode + non-secret caches, and
  refresh-surviving httpOnly-cookie login (cross-origin-capable). 6 phases (P1 shell+theme → P6
  polish). Plans: `implementation_plans/panel-appshell-redesign-2026_06_09/`. Approved mirror:
  `.claude/plans/valiant-wondering-crab.md`. Frozen: compiler/renderer/air-gap.
- **Prior subject:** controller-panel-2026_06_08 (2.0 program) — CHECKPOINTED, all merged to main;
  design `docs/design/controller-panel-design-spike-2026_06_07.md`.
- **Branch:** main (no active feature branch until P1 is approved).
- **Last shipped:** **v2.0.0-preview.3** (2026-06-09) — controller-panel operator auth stack
  (#38–#48: ConfigSigner, password login, one-shot bootstrap, Docker, TOTP, passkey, signing
  at-rest) + #49 loopback bind + #50 docker README + #51 webauthn IP-RP-ID guard + #52 path-prefix
  hint. Prior: v2.0.0-preview.2 (off-host signing keystone, #35/#36/#37); v1.4.0 (signed bundles
  + custody + agent).

## Open questions / blockers

- **Remaining Plan 5 (task #20) is GATED on user forks** — the SaaS-hardening phase (multi-tenant
  isolation, per-tenant **KMS** config-signing, **OIDC** operator login + RBAC, supply-chain
  hardening) needs the user to choose a KMS provider and an OIDC provider before building. User
  asked to checkpoint here rather than start it. See [[security-model-keystone]].
- Remote ultraplan cloud session never landed its PR. If it ever appears: reconcile via a
  plan-N.5-style insertion; the frozen `docs/spec/` contracts win conflicts.

## Next actions

- **panel-appshell-redesign — awaiting approval:** once approved, execute `plan-1` (app-shell
  scaffold + theme foundation) as the first reviewed PR, then P2–P6. See the subject folder +
  `.claude/plans/valiant-wondering-crab.md`. **Do not build until the user approves.**
- **Owed manual gate (keystone) — needs user hardware (no authenticator in CI):** a browser smoke —
  enroll a passkey/YubiKey and run a keystone-ON deploy that taps the key and promotes.
- **Owed:** real-host two-node agent smoke (enroll → pull → verify → apply → report) and real-host
  mimic failover smoke (`docs/spec/artifacts/mimic.md` §Verification) before production reliance.
- **When the user resumes the program:** detail the rest of Plan 5 into numbered sub-plans once the
  KMS + OIDC provider forks are decided (plan-5.2+). Multi-tenant structural isolation (tenant_id
  from authenticated principal + cross-tenant CI gate) is the provider-agnostic slice that can start
  without those forks.
- **plan-6.5 (open design marker, audit subject):** domain-CIDR aggregate announcement design.
- Future-subject candidates: ECMP/load-balancing across parallel links; per-edge WG keypairs
  (escape hatch documented in security.md); bundled/hybrid canvas rendering for N>3 fans;
  route_policies implementation; static-route renderer; additive-apply installer; IPv6 overlay.
- Carried-forward polish (non-blocking reviewer notes, see both archived outlines §9):
  sole-backup-pair compile test; node-ID charset hardening vs reserved separators;
  deploy.go fallback-branch test (audit subject).

## Recently closed subjects (last 3)

- [mimic-tcp-transport-2026_06_07](implementation_plans/_completed/mimic-tcp-transport-2026_06_07/outline.md)
  — 3 stacked PRs: transport:"tcp" wraps links with mimic (eBPF UDP→fake-TCP) for UDP-hostile networks.
- [parallel-links-and-babel-failover-2026_06_07](implementation_plans/_completed/parallel-links-and-babel-failover-2026_06_07/outline.md)
  — 3 stacked PRs: per-edge link identity, babel cost failover, focus-transparency UX.
- [audit-remediation-and-allocation-stability-2026_06_07](implementation_plans/_completed/audit-remediation-and-allocation-stability-2026_06_07/outline.md)
  — 10 stacked PRs, 84 findings closed/deferred/refuted, sticky-pin allocation, contract freeze.
