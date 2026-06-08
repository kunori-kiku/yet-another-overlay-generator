# STATUS
<!-- regenerated: 2026-06-08 (controller-panel 2.0 — keystone-complete checkpoint) -->
<!-- by: execution session (post plan-5.1d merge) -->

## Active work

- **Subject:** controller-panel-2026_06_08 (2.0 program) — **CHECKPOINT: off-host signing keystone
  complete, paused at a clean boundary by user request.**
- **Branch:** main (all controller work merged; no active feature branch).
- **Where it stands:** Phase 0 → Phase 1 (custody + agent) → Phase 2 (single-tenant controller +
  panel + key rotation) → **Plan 5.1 keystone** are ALL merged to main. Design:
  `docs/design/controller-panel-design-spike-2026_06_07.md`; plans in
  `implementation_plans/controller-panel-2026_06_08/`.
- **Last shipped:** **v2.0.0-preview.2** (2026-06-08, `c658170`) — the off-host signing keystone.
  Plan 5.1, PRs #35/#36/#37: `internal/trustlist` (WebAuthn ES256/EdDSA + Ed25519 verifier,
  fail-closed); controller stages-unsigned + `PromoteStaged` refuses (422) without a valid off-host
  signature + agent `VerifyMembership`; browser WebAuthn enroll/sign ceremony. install.sh-bypass
  closed via "sign the bundle per Deploy" (`Member.BundleSHA256 = hex(sha256(checksums.sha256))`).
  Net: a breached controller — even with the operator token + host bundle key — cannot forge
  membership or alter what runs as root, lacking the off-host signature. Prior: v2.0.0-preview.1
  (controller panel single-tenant preview), v1.4.0 (signed bundles + custody + agent).

## Open questions / blockers

- **Remaining Plan 5 (task #20) is GATED on user forks** — the SaaS-hardening phase (multi-tenant
  isolation, per-tenant **KMS** config-signing, **OIDC** operator login + RBAC, supply-chain
  hardening) needs the user to choose a KMS provider and an OIDC provider before building. User
  asked to checkpoint here rather than start it. See [[security-model-keystone]].
- Remote ultraplan cloud session never landed its PR. If it ever appears: reconcile via a
  plan-N.5-style insertion; the frozen `docs/spec/` contracts win conflicts.

## Next actions

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
