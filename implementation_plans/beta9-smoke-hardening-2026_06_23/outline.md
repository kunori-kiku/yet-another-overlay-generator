# Subject: beta.9 smoke-hardening — outline

<!-- drafted: 2026-06-23 by draft-implementation-plan; approved by owner -->

> **STATUS: DELIVERED (2026-06-23).** All 5 plans merged to `main` (PRs #176 spine, #177 plan-1
> telemetry, #178 plan-2 validate, #179 plan-3 signing recovery, #180 plan-4 mimic discover, #181
> plan-5 CHANGELOG). `v2.0.0-beta.10` cut from the green tip (tag on `49b3566`), `release.yml` +
> `docker.yml` green, promoted to **GitHub Latest** (`releases/latest` → beta.10; beta.9 demoted).
> Each PR independently 4-lens-reviewed (security-weighted for plan-3/4) → fixed at root → re-reviewed →
> CI green. All success criteria met in code; the **owner browser smoke** on the live fleet is the one
> owed item (telemetry un-freeze · controller Validate · signing recovery on a cleared browser · mimic
> Discover) — gates rc.1, not the release. Process lesson recorded in the decisions log
> (shared-working-tree clobber → isolated worktrees + checkout-free reviews).

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
- **plan-4 (error code):** Reuse `CodeAgentReleaseRequestInvalid` (400) / `CodeAgentReleaseFetchFailed` (502) for `release-assets` rather than mint new codes (no apierr-bijection churn). The shared fetch-failed message was genericized from "release checksum" to "from the release at {url}" so it reads correctly for both the pin (checksum sidecar) and the asset-list fetch (review nit).
- **Process lesson (shared working tree):** A review workflow's subagents ran `git checkout`/`gh pr checkout` in the SHARED main working tree, switching its branch and **discarding uncommitted plan-3 edits**. Recovered by (a) doing each branch's work in an **isolated `git worktree`** (so main-tree churn can't touch it) and (b) making every subsequent review workflow **checkout-free** — read PR code via `git show <ref>:<path>` + `gh pr diff`, never a checkout. Never leave uncommitted work in a tree a background agent may `git checkout`.

## Milestones

| Plan | Title | Status |
|------|-------|--------|
| plan-1 | Telemetry monitoring channel (`/telemetry` heartbeat + sampler framework) | ✅ merged (#177) |
| plan-2 | Controller-mode Validate runs the in-browser validator | ✅ merged (#178) |
| plan-3 | Off-host signing-handle auto-recovery | ✅ merged (#179) |
| plan-4 | Mimic catalog discover-and-pick | ✅ merged (#180) |
| plan-5 | Release `v2.0.0-beta.10` | ✅ merged (#181) → tagged + Latest |

No insertion plans were needed: the `-race` suite found no telemetry data race (plan-1.5 not triggered), and the `deriveKey` heuristic + duplicate-key guard handled the mimic asset-naming variance (plan-4.5 not triggered).

plan-1 is the priority (makes the feedback channel honest + lays the framework). Plans 2–4 are file-disjoint (parallelizable off main). plan-5 gates on all merged + green.

## Insertion-point markers (likely failure modes)

- **plan-1.5** if the dual-write conditions race or the heartbeat concurrency surfaces a real data race under `-race` that the design's lock-free reasoning missed.
- **plan-4.5** if upstream mimic asset-naming variance (dkms/dbgsym/ddeb) breaks the `deriveKey` heuristic beyond the designed duplicate-key guard.

## Closure criteria

- [x] plans 1–4 each: built → independent workflow review (4 lens) → fix → re-review clean → CI green → merged.
- [x] plan-5: `v2.0.0-beta.10` published, GitHub Latest, assets+sidecars verified (7-arch agents + `.sha256` sidecars, 7 bundles, 7 airgap servers, local-design zip), `release.yml` + `docker.yml` green.
- [ ] **Owner browser smoke** on the live fleet (the one owed item; gates rc.1, not the release): telemetry un-freeze · controller-mode Validate · signing-handle recovery on a cleared/fresh browser · mimic Discover against the real upstream.
- [ ] Non-blocking follow-up: visual-corpus baseline regen for the new mimic Discover UI.
- [ ] STATUS.md + this outline's status table updated; memory note appended; subject archived to `_completed/` at closeout.
