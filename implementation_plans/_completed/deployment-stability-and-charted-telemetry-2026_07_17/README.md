# Deployment stability and charted telemetry — closure

## Status

- **Outcome:** Delivered at the reviewed implementation/release-ready boundary.
- **Opened:** 2026-07-17.
- **Closed:** 2026-07-17.
- **Branch:** `fix/rc12-telemetry-drafts`.
- **Reviewed implementation candidate:** `f962ab9`.
- **Plan-closure head:** `b2c8534`.
- **Release state at closure:** `v2.0.0-rc.12` is ready and uncut. The cumulative merge,
  exact-main CI, and terminal publication checklist remain mandatory before the tag.

## What was delivered

- `3035a2e` restored structured deployment validation, made invalid drafts non-mutating, and limited
  the old-controller deploy-anyway compatibility path to a genuinely absent preview route (404/405).
- `f136d72` stabilized endpoint-specific client allocations and normalized browser allocation baselines.
- `a7d4fd1` removed routine report noise from new durable audit entries and completed display-only probe
  names without changing executable/history identity.
- `5e751ca` froze strict version-1 telemetry policy bytes, added a mutually exclusive signed successor
  policy, and introduced the explicit two-deployment agent-upgrade bridge.
- `09eff0a` added bounded fixed-GET URL probes with configurable exact success status, live actual status,
  and shared charted latency/availability history.
- `efabd64` added bounded automatic disk/filesystem/GPU discovery and sampling primitives with private
  stable identities, explicit gaps, NVIDIA fixed-query and AMD sysfs provider boundaries, and portable
  non-Linux behavior.
- `1bd840d` activated opt-in automatic device telemetry end to end, including authenticated reporting,
  exact-series history, and reusable Fleet charts for every dynamic numeric metric.
- `faeee52` regenerated and independently re-reviewed the complete 18-component architecture cache.
- `f962ab9` integrated transaction/allocation/history hardening, compatibility corrections, UI and
  accessibility work, release documentation, focused regressions, and the final rc.12 candidate.

## How

Fixes were completed before features. The controller deployment path was first made non-mutating on
validation failures, allocation write-back was made optimistic and topology-versioned, routine agent
reports were separated from meaningful durable audit events, and browser state was normalized so old
compiled fields could not silently re-enter a new draft.

The telemetry extension preserved the released version-1 wire contract byte-for-byte. URL and automatic
device policy use a separately named, mutually exclusive, checksum/signature/keystone-bound successor
member. A normal deployment requires the affected managed agents' latest exact authenticated capability
advertisements; the explicit upgrade-first deployment temporarily projects successor-only fields away
without rewriting the saved draft, then a second deployment activates the retained policy.

Observation stayed on authenticated HTTP. Agents sample into bounded reporting structures; the
controller owns bounded history, exact-series selection, and rollup; Fleet owns hand-edited policy,
live feedback, and shared accessible charts. Every production numeric device/probe metric is registered
through the catalog/projector/chart framework, while categorical inventory and actual HTTP status remain
live-only by design.

The work was surveyed and independently reviewed with multiple agents across controller transactions,
security/custody, compatibility, frontend persistence/accessibility, architecture, release readiness,
and coding hygiene. Findings were fixed and returned to the same scopes until all reported clean.

## Verification evidence

The exact behavioral candidate passed:

- `go test ./...`, `go test -race ./...`, `go vet ./...`, coverage-floor, and wire-drift gates;
- frontend lint, fresh Go/WASM build, controller/local builds, and all 428 Vitest tests;
- Playwright: 78 passed and 6 intentionally skipped;
- Linux real-tunnel, govulncheck, DAST, npm audit, and gosec advisory gates;
- exact 22-asset release-shape verification and Docker Buildx validation.

Post-review documentation, bilingual copy, and comment-only corrections passed gofmt, frontend lint,
`git diff --check`, guide synchronization, and 228 relative-link checks across 25 modified Markdown
files. PowerShell was unavailable locally and remains covered by required PR/exact-main CI.

## What was parked / given up

No planned implementation was parked or abandoned.

The following proof and publication steps are deliberately unfinished at this archive boundary, not
waived: cumulative PR checks, squash merge, successful push CI on the exact main commit, a repeated
immutable-reference preflight, annotated tag creation as the final repository mutation, and verification
of all GitHub and multi-architecture container outputs. They remain in [Plan 9](plan-9-2026_07_17.md).

No physical NVIDIA or AMD GPU was available for an on-hardware smoke. Deterministic provider fixtures,
process bounds, non-Linux behavior, and cross-platform builds passed; the physical-hardware caveat stays
explicit in release documentation.

## Pointers

- [Subject outline](outline.md)
- Completed plans: [1](_completed/completed-plan-1-2026_07_17.md),
  [2](_completed/completed-plan-2-2026_07_17.md),
  [3](_completed/completed-plan-3-2026_07_17.md),
  [4](_completed/completed-plan-4-2026_07_17.md),
  [5](_completed/completed-plan-5-2026_07_17.md),
  [6](_completed/completed-plan-6-2026_07_17.md),
  [7](_completed/completed-plan-7-2026_07_17.md), and
  [8](_completed/completed-plan-8-2026_07_17.md)
- [Terminal Plan 9 release checklist](plan-9-2026_07_17.md)
- [Refreshed architecture cache](../../../specs/README.md)
- Tests: no temporary subject/plan tree was created; regression tests remain beside their production
  packages and in the ordinary frontend/E2E suites.
- Memory: no separate entry; durable findings live in the refreshed specs, local agent guides, and this
  closure record.
- Related open subject: [mixed controller/local mode](../../mixed-controller-local-mode-2026_06_25/outline.md)
