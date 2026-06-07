# STATUS
<!-- regenerated: 2026-06-07 (mimic-tcp-transport subject closed) -->
<!-- by: program closure (mimic-tcp-transport) -->

## Active work

- **Subject:** none — between subjects.
- **Last shipped:** mimic-tcp-transport (2026-06-07, PRs #18–#20, `9bae3f2..df6da21`, merged to
  main; **release not yet tagged**). `transport: "tcp"` now wraps a link with
  [mimic](https://github.com/hack3ric/mimic) (eBPF UDP→fake-TCP) for UDP-hostile networks
  (QoS/port-block/throughput) — NOT censorship. Keyless (no new field), egress-attach
  (`mimic@<egress>`, runtime-detected NIC), MTU −12, distro-installed (not bundled), clients
  included. udp output byte-identical (no drift). Prior: v1.3.0 (parallel-links + canvas).

## Open questions / blockers

- Remote ultraplan cloud session never landed its PR. If it ever appears: reconcile via a
  plan-N.5-style insertion; the frozen `docs/spec/` contracts win conflicts.

## Next actions

- **Release:** main is ahead of the v1.3.0 tag by the mimic subject — cut **v1.3.1** when ready
  (user-gated; `tcp` going from no-op to functional is a feature). The release workflow's test gate
  will re-verify before building.
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
