# STATUS
<!-- regenerated: 2026-06-07 (mimic-tcp-transport subject drafted) -->
<!-- by: draft-implementation-plan -->

## Active work

- **Subject:** mimic-tcp-transport-2026_06_07 (drafted; plan-1 spec amendments written, executing)
- **Branch:** plan/mimic-transport-2026-06
- **Goal:** `transport: "tcp"` = wrap the link with [mimic](https://github.com/hack3ric/mimic)
  (eBPF UDP→fake-TCP) for UDP-hostile networks (QoS/port-block/throughput) — NOT censorship. Keyless
  (no new field), egress-attach (`mimic@<egress>`), MTU −12, distro-installed (not bundled).
  3 plans: spec → backend → frontend label.
- **Last shipped:** v1.3.0 (2026-06-07): parallel-links-and-babel-failover (PRs #14–#16,
  N parallel links per node pair with Babel cost failover. Per-edge link identity
  (`internal/linkid`, primary-class keeps bare pinKey → zero drift, proven by the untouched
  perpetual I1/I2 fixtures), edge-aware interface naming (N4), role→cost presets (backup 384),
  Add-backup gesture + role-chip fan + edge-aware interface chips (shared pinned-port resolver),
  and the focus-transparency canvas interaction.

## Open questions / blockers

- Remote ultraplan cloud session never landed its PR. If it ever appears: reconcile via a
  plan-N.5-style insertion; the frozen `docs/spec/` contracts win conflicts.

## Next actions

- **plan-6.5 (open design marker, audit subject):** domain-CIDR aggregate announcement design.
- Future-subject candidates: ECMP/load-balancing across parallel links; per-edge WG keypairs
  (escape hatch documented in security.md); bundled/hybrid canvas rendering for N>3 fans;
  route_policies implementation; static-route renderer; additive-apply installer; IPv6 overlay.
- Carried-forward polish (non-blocking reviewer notes, see both archived outlines §9):
  sole-backup-pair compile test; node-ID charset hardening vs reserved separators;
  deploy.go fallback-branch test (audit subject).

## Recently closed subjects (last 3)

- [parallel-links-and-babel-failover-2026_06_07](implementation_plans/_completed/parallel-links-and-babel-failover-2026_06_07/outline.md)
  — 3 stacked PRs: per-edge link identity, babel cost failover, focus-transparency UX.
- [audit-remediation-and-allocation-stability-2026_06_07](implementation_plans/_completed/audit-remediation-and-allocation-stability-2026_06_07/outline.md)
  — 10 stacked PRs, 84 findings closed/deferred/refuted, sticky-pin allocation, contract freeze.
