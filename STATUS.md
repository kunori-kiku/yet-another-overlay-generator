# STATUS
<!-- regenerated: 2026-06-08 (controller-panel 2.0 program drafted) -->
<!-- by: draft-implementation-plan -->

## Active work

- **Subject:** controller-panel-2026_06_08 (2.0 program, drafted — Phase 0 ready to execute).
- **Branch:** plan/controller-panel-2026-06
- **Goal:** evolve YAOG into an agent-pull controller panel (zero-knowledge key custody, two-tier
  signing, hosted multi-tenant), additive to main, air-gap path frozen. Design:
  `docs/design/controller-panel-design-spike-2026_06_07.md`. 5 plans: P0 sign-bundle (detailed) →
  P1a split-render + P1b agent (detailed) → P2 enrollment/persistence (milestone) → P3 multi-tenant/
  KMS (milestone). Execute Plan 1 (Phase 0) first — ships standalone (candidate v1.4.0).
- **Last shipped:** mimic-tcp-transport (2026-06-07, PRs #18–#20, `9bae3f2..df6da21`, merged to
  main, released **v1.3.1 + v1.3.2**). `transport: "tcp"` now wraps a link with
  [mimic](https://github.com/hack3ric/mimic) (eBPF UDP→fake-TCP) for UDP-hostile networks
  (QoS/port-block/throughput) — NOT censorship. Keyless (no new field), egress-attach
  (`mimic@<egress>`, runtime-detected NIC), MTU −12, distro-installed (not bundled), clients
  included. udp output byte-identical (no drift). Prior: v1.3.0 (parallel-links + canvas).

## Open questions / blockers

- Remote ultraplan cloud session never landed its PR. If it ever appears: reconcile via a
  plan-N.5-style insertion; the frozen `docs/spec/` contracts win conflicts.

## Next actions

- **Execute Plan 1 (controller Phase 0):** canonical bundle serialization + Ed25519 signing of the
  install bundle (stdlib, no DB/agent) — ships standalone as candidate v1.4.0 and de-risks the
  signing primitive the rest of the 2.0 program depends on.
- **Owed:** real-host mimic failover smoke (two Linux nodes; procedure in
  `docs/spec/artifacts/mimic.md` §Verification) before relying on it in production.
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
