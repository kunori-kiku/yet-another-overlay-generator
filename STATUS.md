# STATUS
<!-- regenerated: 2026-06-07 (post-merge closure) -->
<!-- by: program closure (audit-remediation-and-allocation-stability) -->

## Active work

- **Subject:** none — between subjects.
- **Last shipped:** audit-remediation-and-allocation-stability (2026-06-07): the full 10-PR
  remediation chain (#3–#12) merged into `main` (`d5065ed..fe93788`). All 84 audit findings
  dispositioned (dossier Appendix B); sticky-pin allocation live with the I1/I2 superset
  property gate in the perpetual suite; specs frozen under `docs/spec/`.

## Open questions / blockers

- Remote ultraplan cloud session never landed its PR. If it ever appears: reconcile via a
  plan-N.5-style insertion; the frozen `docs/spec/` contracts win conflicts.

## Next actions

- **plan-6.5 (open design marker):** domain-CIDR aggregate announcement — deferred from Plan 6
  under the byte-identical self-/32 stop-loss. Needs its own design pass before any babel change.
- Future-subject candidates recorded in the outline's out-of-scope list: route_policies
  implementation, static-route renderer, additive-apply installer, per-node deploy selector,
  IPv6 overlay support.
- Small carried-forward polish (non-blocking reviewer notes, listed in the archived outline §9):
  RightPanel prefix-match lookup tightening, D71 validator test, deploy.go fallback-branch test.

## Recently closed subjects (last 3)

- [audit-remediation-and-allocation-stability-2026_06_07](implementation_plans/_completed/audit-remediation-and-allocation-stability-2026_06_07/outline.md)
  — 10 stacked PRs, 84 findings closed/deferred/refuted, sticky-pin allocation, contract freeze.
