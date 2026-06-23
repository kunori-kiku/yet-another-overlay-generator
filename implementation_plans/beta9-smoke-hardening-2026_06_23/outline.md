# Subject: beta.9 smoke-hardening — outline

<!-- drafted: 2026-06-23 by draft-implementation-plan; approved by owner -->

## Mission

Fix the cluster of real defects + UX gaps surfaced while smoking `v2.0.0-beta.9` on a live ~9-node fleet, and ship them as **`v2.0.0-beta.10`** (promoted to GitHub Latest, like beta.9) so the owner can re-smoke. The headline: the **Node Conditions feedback channel is lying** — conditions are sampled only at apply time (WireGuard mid-handshake → `LinkDown`; self-update mid-probation → `HealthConfirmedProbationary`) and never refresh while idle, so the panel freezes a worst-case post-apply snapshot even though the overlay + self-update are healthy. Build the fix as a **dedicated, extensible monitoring channel** (`/telemetry` heartbeat).

**Success criteria:**
- The panel reflects **live** node health (conditions refresh on a heartbeat, not just at apply).
- Controller-mode "Validate" works (no `/api/validate` 404).
- A cleared/new browser can sign a Deploy by recovering the off-host signing descriptor (no fleet-stranding rotation), with the keystone off-host guarantee intact.
- The mimic `.deb` catalog assists by discovering + picking release assets (no hand-typing filenames).
- `v2.0.0-beta.10` published as GitHub Latest, all assets + sidecars, smokes green.

## Principles

(Project-wide rules in CLAUDE.md + memory still apply. Subject-specific invariants:)

- **[STATED] Keystone off-host guarantee is inviolable (HIGH).** Signing recovery serves only the **public** descriptor (credentialId + alg + rpId + audit-only PEM); the private key never leaves the authenticator and a physical tap is required per signature. A compromised controller must still be unable to forge a deploy. Violation example: serving/persisting any private material, or removing the tap requirement.
- **[STATED] No monkey-patches / shims / ugly workarounds (HIGH).** Each change is structure-aware + clean; root-cause fixes, not band-aids.
- **[INFERRED] Deploy custody is separate from observability (HIGH).** The `/telemetry` channel must NEVER touch `applied_generation` / `checksum` / deploy-status fields (`SetAppliedGeneration` has no monotonic guard, and the resume cursor is advanced past the applied gen on rekey/idle wakes — the BLOCKER-2 fix). Telemetry carries live health only. Violation example: a heartbeat reporting the resume cursor as the applied generation → masks rekey-stragglers, breaks orphan display.
- **[INFERRED] Best-effort observability never tears down the overlay (HIGH).** The heartbeat is daemon-only, ticker-paced, swallow-and-log; a failed/unreachable controller must never block the poll loop or kill the running overlay.
- **[STATED] Per-PR independent workflow review (correctness / completeness / hygiene / structure) → fix → re-review (MEDIUM).** Close each plan + the subject.

## Current state of the world

- Branch `main` @ `b3e7206`; tag `v2.0.0-beta.9` is GitHub Latest (owner-promoted from prerelease 2026-06-23).
- The agent-feedback-and-version-aware-rollout subject (Node Conditions, mimic→UDP fallback, version-aware rollout, default release URLs) shipped in beta.9 (PRs #162–#175).
- Root causes for this subject were confirmed from live node logs (`journalctl -u yaog-agent`: `handshake 2 seconds ago`, `self-update … finalized after a clean cycle`) + a gen-26 retest, and all five fixes were adversarially designed + verified by workflows (`w0s4u9zxq`, `w2mslu5jt`).

## Must-read references

**Memory:** `review-each-pr-before-merge`, `frontend-ci-uses-tsc-b`, `security-model-keystone`, `agent-feedback-version-rollout-shipped`, `Go IS available locally`.

**Production code (with line numbers):**
- Telemetry: `internal/agent/conditions.go:56` (collectConditions), `internal/agent/conditions_wireguard.go` (classifyWGDump), `internal/agent/agent.go:345/:382` (apply-time report), `internal/agent/controller_client.go:388` (postReport), `internal/agent/cycle.go` (cfg.After = resume cursor, NOT applied gen), `cmd/agent/main.go:~510` (daemon loop), `internal/api/handler_agent.go:212` (HandleReport), `internal/api/routes_controller.go:190` (RegisterAgentRoutes), `internal/api/wire_controller.go:56` (reportRequestJSON), `internal/controller/store.go` + `memstore.go:198` + `filestore.go:581` (SetAppliedGeneration, stampConditions), `internal/model/condition.go:13-15` (Type/Status are plain strings).
- Validate-404: `frontend/src/stores/topologyStore.ts:765` (validate) + `:50-73` (seam docstring), `frontend/src/compiler/localEngine.ts:22-24`, `internal/api/airgap_routes.go:33`, `internal/api/airgap_routes_removed_test.go`, `frontend/src/components/layout/BottomBar.tsx:~19`.
- Signing: `internal/api/wire_controller.go:252` (operatorCredentialStatusJSON), `internal/api/handler_keystone.go:116`, `frontend/src/api/controllerClient.ts:219/:1094`, `frontend/src/stores/controllerStore.ts:1292/:1470`, `frontend/src/lib/webauthn.ts:13/:313`.
- Mimic: `internal/api/release_pins.go` (SSRF guards, releaseLatestSuffix), `internal/apierr/apierr.go`, `frontend/src/components/deploy/MimicCatalogSettings.tsx`.
- Release: `.github/workflows/release.yml`, `CHANGELOG.md`, `cmd/server/main.go:79` (BuildVersion).

**Test gates:** `go test -race ./...`, `go test -tags airgap ./...`, conformance, frontend-e2e (Playwright), realtunnel netns, security-scan; FE `tsc -b` + eslint + vitest.

## Decisions log

- **Preflight Q1 (scope):** One subject, 5 plans (not split signing out).
- **Preflight Q2 (release seq):** All fixes in one beta.10 (no incremental hotfix).
- **Preflight Q3 (signing security):** Serve the public descriptor (preserves off-host guarantee) — NOT discoverable-passkey re-enrollment.
- **Preflight Q4 (heartbeat cadence):** Dedicated faster interval — explicitly to build a reusable/extensible **monitoring framework** the owner can extend later (latency, resource metrics).
- **Midflight (heartbeat transport):** A dedicated `/telemetry` endpoint (not reuse of `/report`) — clean separation of observability from deploy custody.

## Milestones

| Plan | Title | Status |
|------|-------|--------|
| plan-1 | Telemetry monitoring channel (`/telemetry` heartbeat + sampler framework) | pending |
| plan-2 | Controller-mode Validate runs the in-browser validator | pending |
| plan-3 | Off-host signing-handle auto-recovery | pending |
| plan-4 | Mimic catalog discover-and-pick | pending |
| plan-5 | Release `v2.0.0-beta.10` | pending |

plan-1 is the priority (makes the feedback channel honest + lays the framework). Plans 2–4 are file-disjoint (parallelizable off main). plan-5 gates on all merged + green.

## Insertion-point markers (likely failure modes)

- **plan-1.5** if the dual-write conditions race or the heartbeat concurrency surfaces a real data race under `-race` that the design's lock-free reasoning missed.
- **plan-4.5** if upstream mimic asset-naming variance (dkms/dbgsym/ddeb) breaks the `deriveKey` heuristic beyond the designed duplicate-key guard.

## Closure criteria

- [ ] plans 1–4 each: built → independent workflow review (4 lens) → fix → re-review clean → CI green → merged.
- [ ] plan-5: `v2.0.0-beta.10` published, GitHub Latest, assets+sidecars verified, smokes green (version stamp + telemetry heartbeat).
- [ ] STATUS.md + this outline's status table updated; memory note appended; subject archived to `_completed/` at closeout.
