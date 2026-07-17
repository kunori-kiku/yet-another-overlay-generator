# Subject: deployment stability and charted telemetry (2026_07_17)

## Mission

Repair the deployment regressions exposed while editing active telemetry, finish the already-requested
audit and probe-label improvements, then extend the signed telemetry framework with constrained URL
checks and opt-in automatic disk/GPU observation. Ship the result as v2.0.0-rc.12, with the release tag
and official container references created only after implementation, review, documentation, and the
exact-main release gates are complete.

Success means:

- deleting or drafting a probe cannot turn an ordinary validation problem into a 500 or an unsafe
  “Deploy anyway” path;
- old client-to-router allocation pins remain valid and stable across unrelated deployments;
- routine node status reports no longer flood the visible durable audit log;
- optional probe names are presentation metadata and do not perturb executable policy or staging;
- existing ICMP/TCP-only nodes keep byte-compatible version-1 policy behavior;
- URL and automatic device telemetry are versioned, signed, bounded, fail closed on old agents, and
  preserve the last-known-good policy;
- disk, GPU, URL-latency, and availability data use the shared chart/history framework rather than
  opaque latest-only values;
- Fleet remains the policy and observation home, live refresh remains visibly active on its existing
  ten-second completion cadence, and fetched history/live data stay outside browser persistence;
- v2.0.0-rc.12 becomes GitHub Latest and the official multi-platform container Latest only after the
  workflow verifies both amd64 and arm64 artifacts.

## Principles

The repo-root PRINCIPLES.md remains authoritative and is not expanded for this subject. These four
subject-level review invariants govern execution without becoming new project-wide doctrine:

- **[STATED] Compatibility and keep-last-good — HIGH.** Extended telemetry must not retroactively
  break an existing fleet. Old agents either continue using version 1 or refuse version 2 before host
  mutation; failed candidates never replace the active policy. Violations include replacing strict v1
  bytes in place, activating a policy an old launcher cannot understand, or clearing the last-known-good
  policy after a failed candidate.
- **[INFERRED FROM DOMAIN] Closed signed policy — HIGH.** Telemetry configuration remains a bounded
  typed policy covered by bundle integrity and the off-host keystone. Violations include arbitrary
  commands, URL methods/headers/bodies, shell interpolation, unsigned destinations, or accepting a
  policy member that is absent from the exact checksummed set.
- **[STATED] Chart-first production metrics — MEDIUM.** Every dynamic numeric signal introduced here
  is registered through the shared metric catalog and has controller history, API projection, and a
  frontend chart renderer. Violations include returning GPU utilization as latest-only JSON, adding a
  nested numeric device field without registry/projector/renderer coverage, or charting categorical
  HTTP status codes merely because they are numbers.
- **[STATED] Minimal useful verification — MEDIUM.** Each plan adds the smallest tests that pin its
  contracts and production regression. Violations include broad duplicate matrices that restate the
  same invariant, skipping the one production reproduction, or rerunning the full repository suite after
  every narrow edit instead of once on the final candidate.

## Current state of the world (2026-07-17)

- Branch fix/rc12-telemetry-drafts starts at origin/main commit 12be8fa with an uncommitted partial fix
  set. Preserve and reconcile it; do not reset or discard it.
- The partial code already moves topology validation toward structured 422 responses, restricts the
  compatibility bypass, suppresses new report audit rows, adds optional probe names, and corrects part
  of client-edge allocation handling. Tests, translations, drift checks, comments, and active specs
  are inconsistent with those changes and must be repaired before feature work.
- The production allocation contract is endpoint-specific: a client endpoint has no per-link listen
  port, while its router peer does. The router-side port plus complete transit/link-local pairs remain
  valid sticky allocations, and compiled_port remains the effective dial port.
- Telemetry already uses authenticated HTTP protocol-v2 metadata, a bounded replay queue, controller
  deduplication, bounded FileStore history, a 1000-bucket query budget, and a chart renderer registry.
  No WebSocket/gRPC replacement is required for this subject.
- Strict telemetry.json version 1 is parsed before install by rc.9-rc.11 agents. Replacing that file
  in place with a new schema would wedge rolling upgrades. The successor policy therefore needs a new
  signed member plus an installer capability gate, while telemetry.json remains byte-compatible for
  legacy ICMP/TCP projection.
- Fleet node detail already owns signed probe editing, live results, history, and the visible ten-second
  refresh feedback. Extend that surface; do not rename Fleet or move policy back into Design.
- STATUS.md names v2.0.0-rc.11 as current Latest. The next available candidate is expected to be
  v2.0.0-rc.12, but plan 9 must stop if that immutable tag or official image reference already exists.

## Must-read references

### Memory and status

- STATUS.md:1-45 and the current subject entry added on 2026-07-17 — rc.11 publication evidence and
  active-work handoff.
- git log --oneline -30 at 12be8fa — recent telemetry/release/custody context.
- No separate Codex memory is authoritative over the current CLAUDE.md/specs cache for this subject.

### Architecture and project docs

- PRINCIPLES.md:1-100 — project-wide invariants and review discipline.
- CLAUDE.md:95-137,140-239,266-319 and ignored AGENTS.md at the same lines — gates, canonical compile,
  controller/telemetry, active-probe, and Fleet boundaries; keep the two guides synchronized.
- specs/compiler-allocation.md:1-52; specs/model-validation.md:1-173;
  specs/controller-stage-promote.md:1-178; specs/controller-agent-api.md:1-121;
  specs/controller-store.md:1-209; specs/agent.md:1-50; specs/artifacts-signing.md:1-155;
  specs/panel-deploy-fleet.md:1-220; specs/panel-design.md:1-59.
- docs/spec/operations/active-telemetry.md:1-220 and
  docs/spec/compiler/allocation-stability.md:1-360, plus the controller/frontend normative files named by
  the per-plan Read first sections.
- RELEASING.md:18-105,120-217,219-322 — immutable transaction and verification.
- .github/workflows/ci.yml:9-312, release.yml:18-950, docker.yml:1-389 — authoritative gates and
  multi-platform publication.

### Production code

- internal/compiler/compiler.go:169-260; peers_build.go:231-520; peers_prealloc.go:1-120.
- internal/validator/semantic_pins.go:1-180; internal/normalize/pins.go:1-180.
- internal/api/handler_deploy.go:20-176; errmap.go:42-90; handler_agent.go:240-430.
- internal/probepolicy/policy.go:20-230; internal/agent/agent.go:199-350;
  verify.go:149-255; probe_runner.go:88-610; heartbeat_reliable.go:34-520.
- internal/telemetrymetric/catalog.go:13-220; internal/controller/telemetry_history.go:95-410,625-720,
  1401-1720; internal/api/telemetry_history.go:300-620.
- frontend/src/stores/controller/deploy.ts:99-160; sync.ts:81-260;
  frontend/src/components/pages/FleetNodeDetailPage.tsx:45-270;
  frontend/src/components/deploy/NodeResourceHistory.tsx:98-570.

### Test gates

- .github/workflows/ci.yml:9-312 — go, drift, frontend, WASM, E2E, real-tunnel, security.
- internal/localcompile/manifest_golden_test.go:131-260 and contract_golden_test.go:242-420.
- internal/wiredrift/drift_test.go:1-430; internal/controller/telemetry_history_test.go:64-380;
  internal/api/telemetry_history_test.go:36-430.
- scripts/test-release-assets.sh and scripts/verify-release-assets.sh.

### Web references

- https://docs.nvidia.com/deploy/nvidia-smi/index.html — official fixed-query fields and output format.
- https://www.kernel.org/doc/html/latest/gpu/amdgpu/thermal.html — AMD GPU busy percentage sysfs.
- https://www.kernel.org/doc/html/v5.12/gpu/amdgpu.html — AMD VRAM sysfs contracts.
- https://pkg.go.dev/net/http — redirect, proxy, TLS, response-header, and timeout behavior.

## Reads from specs

compiler-allocation, model-validation, controller-stage-promote, controller-agent-api,
controller-store, agent, render-keys, artifacts-signing, panel-deploy-fleet, panel-design

## Standing rules

- Preserve the dirty worktree; never reset, checkout away, or overwrite unrelated user changes.
- Use apply_patch for edits, gofmt for Go formatting, and rg/rg --files for discovery.
- Fixes execute before features. Each plan is independently reviewed, fixed, and re-reviewed before its
  implementation/status/close commits are pushed.
- close-phase status and archive actions are pre-authorized by the owner: choose delivered only when the
  plan’s definition of done is objectively met; otherwise choose partial and stop. Specs scope is also
  pre-authorized. The refresh-specs Mermaid verification remains the one mandatory user checkpoint.
- No external notification channel is assumed; absent an explicit AGENTS.md channel opt-in, use chat-only
  status and make no external send.
- Keep authenticated HTTP telemetry; no WebSocket/gRPC replacement in this subject.
- No code, documentation, status, archive, branch, tag, or registry mutation follows the annotated
  release tag. The terminal release action is intentionally outside execute-implementation-plan and
  close-phase.

## Decisions log

- 2026-07-17 — Owner: fixes before features; execute the full subject rather than stop after planning.
- 2026-07-17 — Owner: use multiple agents for repository survey and independent review/fix/re-review.
- 2026-07-17 — Owner: close-phase and specs scope may proceed autonomously; refresh-specs is required
  after integration. The skill-mandated diagram verification remains an explicit checkpoint.
- 2026-07-17 — Owner: keep PRINCIPLES.md minimal; URL mechanics are temporary implementation decisions.
- 2026-07-17 — Owner: tests must be minimal but useful.
- 2026-07-17 — Owner: URL success uses one configurable exact status, default 200; actual status is live
  metadata, not a chart; latency and availability are charts.
- 2026-07-17 — Owner: automatic device detection is opt-in, finds disks/GPUs automatically, and every
  dynamic numeric telemetry value must use the chart framework.
- 2026-07-17 — Owner: Fleet remains the natural UI home; DNS is not a separate mandatory field.
- 2026-07-17 — Owner: v2.0.0-rc.12 release and Docker Latest are in scope; the tag is the final mutation.
- 2026-07-17 — Investigation: specs cache is older than 30 days and will receive a full refresh in plan 8.
- 2026-07-17 — Plan review: use telemetry-policy.json as a separate successor member, exclusive with
  telemetry.json, and require an explicit upgrade-only deployment before ordinary v2 activation when
  capability has not been reported.
- 2026-07-17 — Plan review: split device_inventory (live-only categorical/stable metadata) from
  device_samples (charted dynamic numeric values), with devicemetric.NumericDefinitions enforcing
  field-level end-to-end chart coverage.
- 2026-07-17 — Workflow resolution: plans 1-8 use execute-implementation-plan and close-phase. Plan 9 is
  a terminal release checklist outside those skills so all closure/archive/status commits precede the
  final tag.
- 2026-07-17 — plan-1 closed as completed: `3035a2e` restored structured deploy validation,
  preserved served/staged state on rejected drafts, and restricted compatibility bypass to 404/405.
- 2026-07-17 — plan-1 specs touch update deferred to plan-8's mandatory full refresh; the current
  dirty specs cache contains coordinated later-plan changes and must not be partially committed here.
- 2026-07-17 — plan-2 closed as completed: `f136d72` preserved the non-client endpoint port and
  complete address pairs on client links, aligned Go/TypeScript healing, and normalized browser sync baselines.
- 2026-07-17 — plan-2 specs touch update deferred to plan-8's mandatory full refresh; active allocation
  documents were corrected now, while the generated component cache will be rebuilt after integration.
- 2026-07-17 — plan-3 closed as completed: `a7d4fd1` kept routine report/telemetry state out of new
  durable audit rows while preserving the verified legacy chain, and finished display-only probe names
  without changing strict version-1 policy bytes, result identity, staging, or generation.
- 2026-07-17 — plan-3 aligned the targeted active specs for audit and probe-name boundaries; the
  mandatory whole-cache `refresh-specs` regeneration remains scheduled for plan 8 after integration.
- 2026-07-17 — plan-4 closed as completed: `5e751ca` froze strict v1 policy bytes, added an
  exclusive signed successor member, and gated deployment on current authenticated agent capabilities
  with a legacy-projection rollout bridge that preserves the full saved draft.
- 2026-07-17 — plan-4 specs touch update deferred to plan-8's mandatory full refresh; the shared
  worktree already contains coordinated later-plan spec edits, so a partial cache update here would
  misrepresent the final telemetry architecture.
- 2026-07-17 — plan-5 closed as completed: `09eff0a` added successor-only fixed-GET URL probes,
  exact expected-status success, bounded latest actual-status metadata, and shared latency/availability
  history without widening the request surface or charting status codes.
- 2026-07-17 — plan-5 specs touch update deferred to plan-8's mandatory full refresh; the shared
  worktree already contains coordinated device/history spec edits, so committing a partial cache now
  would describe an intentionally intermediate architecture.
- 2026-07-17 — plan-6 closed as completed: `efabd64` added dormant bounded disk/filesystem/GPU
  contracts and Linux collectors with private hashed identity, explicit zero-versus-gap semantics,
  hard discovery/process bounds, and clean Windows/arm64 portability gates.
- 2026-07-17 — plan-6 specs touch update deferred to plan-8's mandatory full refresh; production
  registration intentionally remains absent until plan 7 activates the chart/history family atomically.
- 2026-07-17 — plan-7 closed as completed: `1bd840d` activated signed automatic device telemetry,
  kept categorical inventory live-only, retained numeric samples at their actual collection cadence,
  and added exact-series Fleet charts without persisting live hardware data in the browser.
- 2026-07-17 — plan-7 specs touch update deferred to plan-8's mandatory full refresh; the coordinated
  dirty specs and adversarial E2E coverage remain isolated for the integrated documentation/gate pass.

## Milestones

| Plan | Title | Track | Depends on |
|---|---|---|---|
| plan-1 | Restore deploy validation and compatibility boundaries | Go + frontend | — |
| plan-2 | Repair client allocation compatibility and browser baselines | Go + frontend | plan-1 |
| plan-3 | Quiet routine audits and finish display-only probe names | Go + frontend | plan-2 |
| plan-4 | Add successor signed telemetry policy and capability/readiness framework | Go + frontend-light | plan-3 |
| plan-5 | Add constrained URL probes end to end | Go + frontend | plan-4 |
| plan-6 | Build bounded automatic disk/GPU collector primitives | Go/agent | plan-4 |
| plan-7 | Activate device telemetry, history, and Fleet charts | Go + frontend | plan-6 |
| plan-8 | Integrated review, full gates, documentation, specs, and release preparation | all | plans 1-7 |
| plan-9 | Terminal publish and verification of v2.0.0-rc.12 | release action (not execute/close) | plan-8 |

## Milestone details

### plan-1 — Restore deploy validation and compatibility boundaries

- **Plan:** _completed/completed-plan-1-2026_07_17.md
- **Goal:** invalid drafts become structured blocking validation, while only a missing old-controller
  preview route permits compatibility deployment.
- **Proposed solution:** preserve validator findings through compiler/API mapping; separate draft save
  from preview/stage; classify 404/405 narrowly in the controller store.
- **Hazards:** masking operational faults as 422; a stale compatibility latch; mutation on rejected stage.
- **Verification gate:** focused mapper/stage/store/i18n tests plus clean independent re-review.
- **Stop-loss:** if stage mutates before validation, draft plan-1.5 for an atomic preflight boundary.

### plan-2 — Repair client allocation compatibility and browser baselines

- **Plan:** _completed/completed-plan-2-2026_07_17.md
- **Goal:** preserve historical router-side port/address pins and browser/server baselines.
- **Proposed solution:** endpoint-specific pin semantics across validator/compiler/reservations/normalizers;
  normalize before synchronization baselines; repair drift/goldens/specs.
- **Hazards:** remnants of the abandoned clear-all-pins behavior; allocation collisions; silent schema
  migration.
- **Verification gate:** one superset/repeated-stage reproduction, Go/TS parity, store baseline, drift and
  focused localcompile golden.
- **Stop-loss:** if correctness requires alloc_schema_version change, stop for plan-2.5 migration design.

### plan-3 — Quiet routine audits and finish display-only probe names

- **Plan:** _completed/completed-plan-3-2026_07_17.md
- **Goal:** routine status remains Fleet state, not visible durable audit noise; names remain display-only.
- **Proposed solution:** suppress new report audit appends, filter legacy rows after server verification,
  share current display labels, forbid generic JSON marshaling of runtime Policy.
- **Hazards:** weakening audit verification; making name part of series identity; leaking name into v1.
- **Verification gate:** focused API/audit/name/strict-wire/delta/UI tests and clean re-review.
- **Stop-loss:** any public Policy marshaling dependency requires a plan-3.5 API transition rather than a
  silent breaking change.

### plan-4 — rolling-upgrade-safe policy foundation

- **Plan:** _completed/completed-plan-4-2026_07_17.md
- **Goal:** add an exclusive successor policy and current-heartbeat readiness bridge without changing
  v1 bytes or erasing a saved v2 draft.
- **Proposed solution:** keep telemetry.json strict v1; introduce a separately named, versioned successor policy
  member for URL/device features; checksum/sign/keystone-bind it; add a new installer capability marker;
  advertise capability as bounded live-only authenticated telemetry; block successor preview/stage until
  the capability is confirmed; and provide an explicit upgrade-only deployment projection that preserves
  the stored v2 draft while emitting no successor policy.
- **Hazards:** an old agent parsing new bytes before it can self-update, or an installer accepting a
  policy the running binary cannot activate.
- **Verification gate:** exact v1 bytes, exclusive v1/v2 transitions, unknown/new member verification, old-agent
  fail-before-mutation, explicit upgrade-only projection, current-heartbeat capability clearing, and
  last-known-good preservation.
- **Stop-loss:** if an old released agent does not safely download and integrity-check an unknown member,
  stop and redesign the rollout before emitting successor policy.

### plan-5 — URL probes

- **Plan:** _completed/completed-plan-5-2026_07_17.md
- **Goal:** add exact-status URL checks whose actual code is live metadata and whose latency/availability
  reuse the probe chart family.
- **Proposed solution:** a distinct typed URL probe with fixed GET, ordinary TLS verification, no redirects, no
  proxy environment, no arbitrary headers/body, bounded URL/timing/body handling, expected status
  default 200, and type-aware history identity. Reuse the existing probe chart family.
- **Hazards:** turning the agent into a general HTTP request surface or merging histories after the
  success contract changes.
- **Verification gate:** one compact httptest matrix, latest-status/history-separation contract, exact-series identity,
  and one focused Fleet flow.
- **Stop-loss:** redirect/auth/header/body requirements are deferred; do not widen the command surface in
  this subject.

### plan-6 — bounded collector primitives

- **Plan:** _completed/completed-plan-6-2026_07_17.md
- **Goal:** safe injectable disk/GPU discovery and sampling without production registration.
- **Proposed solution:** separate inventory/sample DTOs, explicit block_device/filesystem/GPU identity,
  sysfs/proc collectors, fixed absolute nvidia-smi invocation, AMD sysfs, deterministic caps.
- **Hazards:** leaking raw identifiers/output, double-counting storage stacks, unbounded root command,
  confusing idle zero with missing data.
- **Verification gate:** one compound disk fixture, one NVIDIA table, one AMD/unsupported fixture, bounds
  and non-Linux compile.
- **Stop-loss:** if safe identity/payload limits cannot fit the existing telemetry ceiling, draft
  plan-6.5 before production registration.

### plan-7 — automatic devices and charts

- **Plan:** _completed/completed-plan-7-2026_07_17.md
- **Goal:** activate opt-in device collection only when inventory/latest/history/API/Fleet chart coverage
  is complete.
- **Proposed solution:** opt-in versioned policy object with mode all; periodic rediscovery; register live-only
  device_inventory and charted device_samples; activate the device family atomically across the leaf
  numeric registry, controller projector/API, frontend renderer, and exact-series query pushdown.
- **Hazards:** unbounded command output or device cardinality, unstable identifiers, fake zero samples,
  accidental browser persistence, or registering metrics without charts.
- **Verification gate:** deterministic proc/sysfs/command fixtures, catalog/projector/encoder parity, exact device query,
  custody stripping, and one focused Fleet chart integration.
- **Stop-loss:** do not add heavyweight vendor daemons/dependencies or claim unsupported hardware metrics.
  Real-hardware validation may remain a documented post-release smoke gap.

### plan-8 — integration and release readiness

- **Plan:** plan-8-2026_07_17.md
- **Goal:** obtain a clean whole-subject re-review, full final gates, refreshed specs, closed/archived
  bookkeeping, and exact reviewed main ready for a terminal tag.
- **Proposed solution:** independent reviewers cover security, controller/history, frontend/custody, compatibility,
  and framework/hygiene; fix and re-review; run the complete CI/release-equivalent gates once; update
  normative docs; perform a full refresh-specs; prepare changelog/status and merge exact reviewed work.
- **Hazards:** test sprawl or cutting a tag from a commit that differs from reviewed origin/main.
- **Verification gate:** clean worktree, exact origin/main, green required checks, reviewed generated specs, release
  references available.
- **Stop-loss:** any red required gate, unresolved review finding, stale diagram, existing rc.12 reference,
  or architecture mismatch blocks plan 9.

### plan-9 — immutable publication

- **Plan:** plan-9-2026_07_17.md (terminal checklist; not consumed by execute-implementation-plan).
- **Goal:** publish the already-closed exact-main candidate and verify its immutable GitHub/container
  transaction without any post-tag repository mutation.
- **Proposed solution:** create one annotated tag at exact current origin/main, push it once, monitor the release
  transaction, and verify all GitHub/container Latest pointers and native architectures.
- **Hazards:** partial publication or moving an immutable tag/reference.
- **Verification gate:** exact 22 assets, GitHub Latest rc.12, GHCR and configured Docker Hub version/latest digest
  agreement, amd64/arm64 native ELF/version/labels/entrypoint verification.
- **Stop-loss:** never move or overwrite rc.12. If a supposedly immutable reference already exists or the
  workflow seals only partially, stop and use the documented recovery path/new version.

## Insertion-point markers

- **plan-1.5 — atomic deploy preflight:** only if rejected validation currently writes staged/topology
  state before returning.
- **plan-2.5 — allocation schema migration:** only if endpoint-specific historical pins cannot be
  represented under alloc schema 1 without ambiguity.
- **plan-4.5 — rollout transport redesign:** if released agents do not safely verify/ignore the separate
  successor member, or current-heartbeat capability cannot survive the real proxy path without stale
  readiness.
- **plan-6.5 — device identity/payload redesign:** if deterministic safe identifiers or bounded inventory
  cannot fit the existing sample/persistence limits.
- **plan-8.5 — release-gate repair:** if CI/release workflow copies diverge or an immutable rc.12 reference
  already exists. This insertion must merge and pass exact-main CI before any tag.

## Branch and commit strategy

- Continue on fix/rc12-telemetry-drafts; preserve all current uncommitted work.
- Each plan gets one or more tightly scoped commits only when the plan explicitly separates concerns.
  Push after the plan’s review/fix/re-review is clean and close-phase bookkeeping is committed.
- Use one cumulative subject PR unless repository state requires smaller PRs. Required CI runs on the
  cumulative branch. Plan 8 runs close-phase/archive/status bookkeeping on that branch, then merges the
  fully reviewed and closed subject to main and waits for exact-main CI.
- Plan 9 is read from the archived subject and runs as a terminal action outside execute/close. It starts
  only from clean main equal to origin/main. No repository or registry mutation follows the tag push
  except the authoritative workflow mutations triggered by that tag.

## Closure criteria

- Plans 1-8 are implemented, independently reviewed, fixed, re-reviewed, closed, archived, and merged.
- Existing v1 telemetry and historical allocations remain compatible; blank drafts fail deployment
  cleanly without mutation; routine reports do not create new visible audit noise.
- URL, disk, and GPU live observations are bounded and signed; every dynamic numeric signal has history
  and a shared Fleet chart; actual URL status remains live-only.
- Browser persistence excludes live/capability/history data.
- Full refresh-specs and durable CLAUDE.md/AGENTS.md synchronization reflect the resulting framework.
- Exact-main CI/release gates are green.
- All repository bookkeeping and subject archival are complete before the immutable tag; v2.0.0-rc.12
  and official containers are then published and verified read-only, with the actual result recorded in
  the final evidence-backed handoff rather than a post-tag source mutation.

## Plan status

| Plan | Status |
|---|---|
| plan-1 | done — `3035a2e` — structured deploy validation and 404/405-only compatibility fallback |
| plan-2 | done — `f136d72` — endpoint-specific client allocation and normalized browser baselines |
| plan-3 | done — `a7d4fd1` — quiet routine audits and display-only probe names |
| plan-4 | done — `5e751ca` — exclusive signed successor policy and latest-heartbeat rollout readiness |
| plan-5 | done — `09eff0a` — constrained fixed-GET URL probes with charted latency/availability |
| plan-6 | done — `efabd64` — bounded hashed disk/filesystem/GPU collectors, explicit gaps, and dormant chart contract |
| plan-7 | done — `1bd840d` — signed automatic device telemetry with exact history and Fleet charts |
| plan-8 | pending |
| plan-9 | pending terminal release action |
