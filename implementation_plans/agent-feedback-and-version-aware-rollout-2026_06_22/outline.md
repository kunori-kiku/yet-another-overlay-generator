# Outline — Agent↔Panel Feedback Channel + Version-Aware Rollout + Mimic UDP Fallback + Default Release URLs (→ beta.9)

<!-- subject: agent-feedback-and-version-aware-rollout -->
<!-- drafted: 2026-06-22 -->
<!-- branch (proposed): feat/agent-feedback-version-rollout -->

This outline is the **durable spine** for the subject. Every plan file in this folder references
it. A fresh session can pick up the work from this file + the named plan file alone.

---

## 1. Mission

Make the controller panel a **trustworthy operations console** for the agent fleet by building a
single **reusable, structured agent→panel feedback channel** ("Node Conditions"), then using it to
land four operator-facing capabilities the owner asked for:

1. **A generic Node Conditions channel** (Kubernetes-conditions pattern) that replaces today's one
   free-form `health` string + brittle panel string-matching. First consumers: mimic, self-update
   (migrated off string-matching), WireGuard link health, and config-apply.
2. **Mimic → UDP fallback, configurable per link** (inherit / udp / none) with a fleet-wide global
   default, so a node on a kernel too old for mimic's eBPF is **not blocked** — and the panel shows a
   **clean, categorized reason** (e.g. "Mimic: fell back to UDP (kernel lacks eBPF)") rather than a
   raw error dump.
3. **Panel version self-awareness + version-aware rollout:** the controller knows and displays its
   own build version; **"Update all agents" with no version typed** drives every agent to the
   **panel's own version** (pinned to that exact release tag); and the controller **refuses** to set
   an agent target **newer than itself**.
4. **Helpful default release URLs** for both agent and mimic, and a **working "Assist from release"**
   (today mimic has no default base → hard error; agent assist leaves the base on the moving `latest`
   alias → silent rollout stall).

Then **cut and publish `v2.0.0-beta.9`** so the owner can smoke the above more easily.

### Success criteria

- [ ] A `NodeCondition{type,status,reason,message,since}` channel exists end-to-end (agent → report
      wire → controller store → operator API → panel), **additive and backward-compatible** (old
      agents send none; old controllers ignore the field).
- [ ] Mimic, self-update, WireGuard, and config-apply each emit conditions; the panel renders them
      generically (color by status, tooltip = curated message) with richer chips for known types.
- [ ] `deriveUpdateState` no longer parses free-form `health` substrings when a structured
      `selfupdate` condition is present (legacy string fallback retained for old agents only).
- [ ] Edge gains a per-link mimic-fallback policy (inherit/udp/none); a fleet-wide default setting
      exists; a node whose mimic provisioning fails brings the link up as plain UDP **iff** policy
      resolves to udp, else fails closed (unchanged) — and **either way** reports a categorized mimic
      condition.
- [ ] The controller exposes its `BuildVersion`; the panel displays it; "Update all agents" defaults
      target = controller version + pins the release base to that tag; the controller rejects a target
      version `> controllerVersion`.
- [ ] `DefaultMimicReleaseBase` exists; both agent and mimic "Assist from release" succeed from the
      shipped defaults and pin to a real tag (no moving-alias stall).
- [ ] All Go + frontend CI gates green (build both profiles, `go test -race ./...`, conformance,
      frontend-e2e, realtunnel); every PR independently workflow-reviewed → fixed → re-reviewed.
- [ ] `v2.0.0-beta.9` annotated tag pushed; release workflow publishes it as a **beta** (not Latest).

---

## 2. Principles (invariants the executor MUST respect)

Inherits all of `PRINCIPLES.md` (repo root). The HIGH-risk ones that THIS subject can violate, plus
subject-specific additions:

- **[STATED] Signed-artifact self-update custody (HIGH).** (`PRINCIPLES.md`.) The conditions channel
  and the version-rollout changes MUST NOT weaken custody: the pin still rides the controller-signed,
  keystone-bound `artifacts.json`; the gh-proxy/github.com stay untrusted transport; the
  `AgentVersionFloor` still advances only after a health-confirmed cycle; `verify.go`'s signature path
  is untouched. The new "Assist from release" convenience and the "Update to panel version" one-click
  STILL only fill pins the operator reviews/saves through the validated `/settings` path — they never
  auto-trust or persist a hash on their own. *Violation example:* having "Update all" silently write
  agent bins to the store without the operator save+sign path; trusting an upstream `.sha256` as a
  trust anchor.

- **[STATED] Generated configs must be deployable (HIGH).** The mimic-fallback path MUST yield a
  **working** link in both branches. A link that falls back to UDP must come up as a valid `wg-quick`
  config (endpoint/port already mimic-independent — `renderer/wireguard.go:80-82`; the mimic MTU−12 is
  conservative-safe for plain UDP). *Violation example:* a fallback that leaves a half-configured
  interface (mimic filter written, mimic dead, WG never started) → silent dead overlay.

- **[STATED] Generated scripts run as root on fleets (HIGH).** The mimic-fallback branch added to
  `install.sh` and any failure-category breadcrumb MUST anchor on Go-emitted constants; no
  user-controlled text (node name, reason strings derived from upstream) may reach a shell context
  unescaped. *Violation example:* echoing a captured `dmesg`/`modprobe` error verbatim into a shell
  string that later re-executes.

- **[STATED] Allocation stability — superset rule (HIGH).** Adding the per-link `mimic_fallback`
  field MUST NOT perturb any allocated value (IPs, ports, transit pairs, keys, link-locals) for
  existing entities. The field is `omitempty`/optional and pure policy — it feeds the renderer, never
  the allocator. *Violation example:* threading the fallback flag through a code path that reorders a
  port/transit counter.

- **[STATED] Backward compatibility of persisted topologies + reports (MEDIUM).** Old topology JSON
  (no `mimic_fallback`), old saved settings (no `MimicFallbackDefault`, no default mimic base), old
  agent report payloads (no `conditions`), and old controller node records (no `Conditions`) all
  continue to load/compile/report. New fields are `omitempty`/optional with safe zero-values.

- **[INFERRED — confirmed by domain] Conditions are curated, not raw (HIGH for this subject).** Each
  condition's `reason` is a **closed enum** (CamelCase code) per `type`, and `message` is a single
  length-capped human line produced by a `classify()` mapping — **never** the raw stderr/`LastError`
  dump. This is the owner's explicit "clear catch of error, not 'some error' or a lengthy duplicate."
  *Violation example:* setting `message = err.Error()` of a multi-line subprocess failure.

- **[INFERRED — confirmed by domain] Mimic exists to evade UDP-hostile networks (MEDIUM/security).**
  A fallback to plain UDP **de-cloaks** the link. Therefore fallback is **opt-in per policy** and a
  fallback event is **always surfaced loudly** (a `warn`-status condition), never silent. The shipped
  fleet-wide default is **conservative (fallback OFF)** unless the owner sets it ON; per-link `udp`
  opts a specific link in. (Decisions log D1.)

- **Execution discipline (HIGH).** `PRINCIPLES.md` §Execution discipline applies verbatim: no shims /
  monkey-patches; structure-aware hygienic code in every block; no scope compromise to "close"; every
  PR independently reviewed (4 lenses: correctness, completeness, hygiene, structure) → fixed →
  re-reviewed. (MEMORY: `review-each-pr-before-merge`, `pre-rc1-program-sequence`.)

---

## 3. Current state of the world (2026-06-22)

- **Branch:** `main` @ `85f4089` (PR #161 merged); working tree clean. Latest tag `v2.0.0-beta.8`
  (GitHub *latest*, owner override 2026-06-18). The pre-rc.1 program's authorable scope is complete;
  rc.1's terminal cut is owner-gated (`docs/spec/rc1/RC1-GATE.md`). **This subject is new work landing
  on `main` as a beta, independent of the rc.1 cut.**
- **Feedback today:** a single free-form `State.Health` string (`internal/agent/state.go:56`) set at
  `agent.go:321` ("applied") / `:355` ("degraded: keeping last-good") and `selfupdate.go:321/:348/:384`
  (self-update markers), reported via `controller_client.go:383-408` to `handler_agent.go:212-248`,
  stored as `Node.LastHealth`, and **string-matched** by the panel (`updateStatus.ts:21-43` greps for
  `'abandoned:'`, `'self-updated to'`, `'health-confirmed (probationary)'`). `State.LastError`
  (`state.go:44`) holds detail but is **never sent** to the controller.
- **Mimic today:** per-edge `transport: "udp" | "tcp"` (`model/topology.go:166-167`); `tcp` →
  `PeerInfo.Mimic` (`compiler/peers.go:252`) + MTU−12 (`:258`); install script provisions mimic and
  **fails closed** (`set -euo pipefail`, `renderer/script.go:95`) if `systemctl enable --now
  mimic@<egress>` (`script.go:721`) or any install step fails. **No fallback, no post-apply health
  check, no per-link policy.** Spec: `docs/spec/artifacts/mimic.md`, `docs/spec/data-model/edge.md`.
- **Version today:** `cmd/server/main.go:79` declares `BuildVersion` (stamped by `release.yml` `-X`),
  but it is **never threaded into the handler** and **never exposed** to any API/frontend. The release
  *does* publish `.sha256` sidecars for agent binaries (`release.yml:358,366`; verified on beta.8). The
  controller has **no** version-floor guard on rollout targets (`validateAgentRollout`,
  `handler_bootstrap.go:350-372`, checks grammar only).
- **Defaults today:** `DefaultAgentReleaseBaseURL` = `.../releases/latest/download`
  (`controller/settings.go:11`); **no `DefaultMimicReleaseBase`** (empty → mimic "Assist" hard-errors
  `assistNeedsBase`). Agent "Assist" without a version leaves the base on the moving `latest` alias
  (`release_pins.go:242-252`, `versionApplied=false`) → pinned hash diverges from later download →
  silent rollout stall (the footgun documented at `release_pins.go:206-214`).

---

## 4. Must-read references

### Memory
- `pre-rc1-program-sequence.md` — program context; beta cadence; review discipline.
- `agent-self-update-signed-verification.md` — self-update custody + floor/anti-downgrade mechanics.
- `controller-panel-rollout-ui-shipped.md` — the existing rollout UI (release-pins, in_rollout, chip).
- `mimic-tcp-transport-closed.md` — mimic transport design facts.
- `review-each-pr-before-merge.md`, `frontend-ci-uses-tsc-b.md` — process invariants.

### Specs (architectural ground truth)
- `specs/`: `agent`, `controller-store`, `controller-agent-api`, `controller-operator-api`,
  `panel-deploy-fleet`, `artifacts-signing`, `render-keys`.
- `docs/spec/`: `artifacts/mimic.md`, `data-model/edge.md`, `artifacts/install-script.md`,
  `artifacts/wireguard.md`, `operations/ci-cd.md`.

### Production code (with line anchors — from the three investigation maps)

**Feedback channel (report path):**
- `internal/agent/state.go:34-75` — `State` (LastResult :42, LastError :44, Health :56).
- `internal/agent/agent.go:72` (`Run`), `:321` (recordSuccess→"applied"), `:355` (recordFailure→"degraded").
- `internal/agent/selfupdate.go:130-159` (decideSelfUpdate), `:321/:348/:384` (health markers).
- `internal/agent/cycle.go:88` (`RunControllerCycle`).
- `internal/agent/controller_client.go:91-99` (reportRequestWire), `:138-141` (AgentVersion field),
  `:362-377` (Report), `:383-408` (postReport).
- `internal/api/handler_agent.go:212-248` (HandleReport).
- `internal/controller/store.go:80-111` (Node; LastAgentVersion :102), `:314-365` (ControllerSettings),
  `:372-396` (Clone), `:637-639` (Get/PutSettings).
- `internal/controller/filestore.go:575-607` (SetAppliedGeneration → LastAgentVersion).

**Version + rollout:**
- `cmd/server/main.go:79` (BuildVersion), `:83-86` (version subcommand), `:104` (api.NewServer),
  `:217` (NewControllerHandler).
- `internal/api/release_pins.go:242-252` (resolveReleaseBase), `:272-377` (HandleReleasePins),
  `:206-214` (the moving-alias custody note).
- `internal/api/handler_bootstrap.go:243-251` (patterns), `:254-289` (validateMimicCatalog),
  `:350-372` (validateAgentRollout).
- `internal/controller/settings.go:11` (DefaultAgentReleaseBaseURL), `:20-40` (Default/WithDefaults).
- `internal/controller/compile.go:135-152` (BuildFetchSettings), `:154-177` (AgentRolloutNodeIDs).
- `internal/render/render.go:243-266` (FetchSettings), `internal/render/artifacts_json.go:59-92`.

**Mimic:**
- `internal/model/topology.go:166-167` (Edge.Transport), `:80-85` (Node.XDPMode).
- `internal/compiler/peers.go:252` (PeerInfo.Mimic), `:258` (MTU−12).
- `internal/renderer/script.go:35-42` (InstallScriptConfig), `:437-490` (mimic install ladder),
  `:694-723` (egress detect + config write + `:721` systemctl enable), `:915-927` (collectMimicPorts).
- `internal/renderer/wireguard.go:154-161` (peer.MTU).
- `internal/validator/semantic.go` + `internal/validator/mimic_test.go` (tcp-edge platform gate).

**Frontend:**
- `frontend/src/types/topology.ts:92` (Edge.transport), `:44-46` (Node.xdp_mode).
- `frontend/src/types/controller.ts:21` (agentVersion), `:30` (inRollout).
- `frontend/src/api/controllerClient.ts:600-604` (SessionInfo), `:650/:654` (node mapping),
  `:681-706` (ControllerSettings), `:772-787` (toSettingsJSON), `:798-801` (postSettings),
  `:807-826` (AgentPinFetch types), `:831-848` (fetchPins).
- `frontend/src/stores/controllerStore.ts:399` (saveSettings), `:687-713` (refresh), `:759` (fetchReleasePins).
- `frontend/src/components/deploy/AgentUpdateSettings.tsx:160-201` (handleAssist), `:214-240` (save),
  `:296-303` (assist btn), `:354-360` (fleet-wide), `:376-382` (save rollout).
- `frontend/src/components/deploy/BootstrapSettings.tsx:112-118` (agent base field).
- `frontend/src/components/deploy/MimicCatalogSettings.tsx:140-174` (handleAssist), `:223` (base), `:234-241` (assist).
- `frontend/src/components/deploy/UpdateStatusChip.tsx:31-56`, `frontend/src/lib/updateStatus.ts:21-43`.
- `frontend/src/components/deploy/NodeRegistry.tsx:49-125`, `.../pages/FleetNodeDetailPage.tsx:54`.

### CI / release
- `.github/workflows/ci.yml` (go `-race`, conformance, frontend-e2e, realtunnel, security-scan).
- `.github/workflows/release.yml:295-320` (build + `-X main.BuildVersion`), `:348-366` (agent sidecars),
  `:436` (asset glob); `make_latest` gating for beta.
- `CHANGELOG.md` (roll for beta.9).

---

## 5. Standing rules

- All of `PRINCIPLES.md` + the MEMORY process rules. Verify locally (`go build/vet/test -race ./...`;
  `cd frontend && npm run lint && npm run build` — `tsc -b`) before pushing; CI is the authoritative gate.
- Git author `kunori-kiku <rokuyanlin@gmail.com>`; **no AI attribution** in commits/PRs; no
  `--no-verify` / amend / force-push. `gh pr edit` fails (GraphQL) — use `gh api --method PATCH`.
- Secret handling per global CLAUDE.md (never echo/commit secrets).
- One PR per plan; independent workflow review (4 lenses) → fix → re-review → merge.

---

## 6. Decisions log

- **D1 — Mimic fallback granularity = PER-LINK with a global default** (owner, 2026-06-22).
  Edge gains a tri-state `mimic_fallback` (`""`=inherit / `"udp"` / `"none"`); `ControllerSettings`
  gains `MimicFallbackDefault` (the fleet-wide policy a link inherits). *Inferred default (flag for
  owner confirm at review):* the shipped `MimicFallbackDefault` is **OFF/none** (fail-closed —
  preserves mimic's censorship-evasion guarantee by default); operators opt a link in with `"udp"` or
  flip the global ON. A fallback event is **always** a loud `warn` condition.
- **D2 — "Update all agents" (no version typed) target = the PANEL's own controller version**, pinned
  to that exact release tag (so the base leaves the moving `latest` alias) (owner, 2026-06-22).
- **D3 — Feedback channel scope = mimic + self-update + wireguard + config-apply** (fullest reuse)
  (owner, 2026-06-22). Build the generic channel; migrate self-update off string-matching; add link &
  config conditions to prove generality.
- **D4 — I (the agent) cut and push the `v2.0.0-beta.9` tag** once all plans merge green (owner,
  2026-06-22). Published as a beta (`make_latest=false`), not GitHub Latest.
- **D5 — Conditions are additive + curated** (skill/domain inference): K8s-conditions shape
  `{type,status,reason,message,since}`; `omitempty` wire field (`since` is RFC3339 `omitempty`); the
  `status` enum is **closed** — plan-1 owns it — with exactly `{ok,warn,error,unknown}`
  (`model.ConditionStatusOK`/`Warn`/`Error`/`Unknown`); closed `reason` enum per `type`; length-capped
  `message` (`model.ConditionMessageMax = 160`, capped inside `classify()`), never raw stderr. (Owner's
  "clear catch" requirement.)
- **D6 — Custody unchanged** (PRINCIPLES): assist + one-click only fill pins the operator reviews and
  saves through `/settings`; nothing auto-trusts or auto-persists a hash.

---

## 7. Milestones (one plan file each)

> Execution order is dependency-driven. Tracks A (conditions) → B (mimic) → C (version) can be
> interleaved by an executor but the binding goal's per-PR review favors the linear order below.

### Track A — the reusable feedback channel

**plan-1 — Node Conditions channel: wire + agent collector + controller store (end-to-end, behavior-preserving).**
- *Goal:* define `model.Condition{Type,Status,Reason,Message,Since}`; add `conditions []…` (omitempty)
  to the report wire (`controller_client.go` + `handler_agent.go`); add an agent-side condition
  collector + `classify` pattern; store `Node.Conditions` (+ server-stamped `ObservedAt`) in filestore.
  Wire **one** condition first (`configapply`, mirroring the existing health with no behavior change) to
  prove the path. *Hazards:* report wire is custody-adjacent — keep it additive, don't touch the signed
  bundle/`verify.go`. *Verify:* `go test -race ./internal/agent/... ./internal/controller/... ./internal/api/...`;
  round-trip + backward-compat (old payload → nil conditions) unit tests. *Stop-loss:* if the report
  schema change ripples into signing, revert to a separate report sub-field and re-plan (plan-1.5).

**plan-2 — Operator API exposure + panel generic conditions render.**
- *Goal:* nodes operator endpoint emits `conditions`; `controllerClient.ts` + `types/controller.ts`
  gain `NodeCondition`/`ControllerNode.conditions`; a generic `<NodeConditions>` strip (color by
  status, tooltip = message) in `NodeRegistry` + `FleetNodeDetailPage`. *Hazards:* full-replace settings
  contract; don't break the existing node mapping. *Verify:* `npm run lint && npm run build`; handler
  test; FE render test. *Stop-loss:* if generic render is ambiguous, ship a minimal list first.

**plan-3 — Migrate self-update + add WireGuard/link-health conditions (retire string-matching).**
- *Goal:* agent emits a `selfupdate` condition (reason ∈ Active/HealthConfirmedProbationary/Updated/
  Abandoned) and a `wireguard` condition (link summary; reason e.g. PeerHandshakeStale/LinkDown).
  `deriveUpdateState` prefers the structured condition, falling back to legacy string-matching only for
  old agents. *Hazards:* don't regress the self-update chip for legacy agents; keep `health` string for
  back-compat. *Verify:* `deriveUpdateState` unit tests (structured + legacy); `-race` agent tests.
  *Stop-loss:* keep the legacy parser intact behind the new path.

### Track B — mimic UDP fallback

**plan-4 — Per-link mimic fallback: model + compiler + validator + global default setting.**
- *Goal:* `Edge.mimic_fallback` tri-state (`""`/`udp`/`none`) + TS mirror; `ControllerSettings.MimicFallbackDefault`
  + Default/WithDefaults; compiler resolves effective per-link policy into `PeerInfo`; validator accepts
  it (tcp-only-relevant). *Hazards:* **allocation stability** — pure policy, must not touch allocation;
  conformance harness (TS==Go) must stay byte-exact. *Verify:* `go test -race ./internal/compiler/...
  ./internal/validator/...`; allocation-stability + conformance gates; model back-compat load test.
  *Stop-loss:* if the effective-policy resolution is subtle, isolate it in a pure helper with a table test.

**plan-5 — Mimic fallback mechanism: install script branch + agent detection + mimic condition.**
- *Goal:* when policy=udp and mimic provisioning fails (kernel/eBPF/deb/systemd-start), the install
  script detects the category, **skips mimic and brings the link up as plain UDP**, and writes a
  Go-constant-keyed breadcrumb; the agent reads it, `classify()`-es into a `mimic` condition
  (KernelTooOld/EbpfLoadFailed/InstallFailed/FellBackToUDP/Active). Policy=none preserves fail-closed
  (unchanged) but still reports a categorized error condition. *Hazards:* **deployable-config** +
  **root-script-safety** principles; the link must be fully up in the UDP branch; no unescaped text.
  *Verify:* script-render golden tests (both branches); classify table test; condition emission test.
  Optional realtunnel angle deferred (needs an old kernel — out of scope, noted). *Stop-loss:* if a
  fully-automatic in-script fallback is too fragile, fall back to "agent detects + re-applies UDP
  variant next cycle" (plan-5.5).

**plan-6 — Panel: per-link fallback toggle + global default setting + mimic condition chip.**
- *Goal:* edge inspector per-link control (inherit/udp/none); settings global default toggle; a rich
  mimic condition chip. *Hazards:* small-screen read-only canvas gate (Subject-2) still applies; don't
  let the canvas mutate the store when gated. *Verify:* `npm run build`; FE tests. *Stop-loss:* ship the
  settings + chip first, edge-inspector control second if the inspector surface is contested.

### Track C — version-aware rollout + defaults

**plan-7 — Controller version self-awareness (backend).**
- *Goal:* thread `main.BuildVersion` → `api.NewServer`/`NewControllerHandler` (a `SetVersion`/ctor arg)
  → expose via the operator `/session` response (and/or a dedicated `/version` operator route).
  *Hazards:* don't leak version on unauthenticated surfaces beyond what's acceptable; keep `/api/health`
  semantics. *Verify:* handler test asserts the version in the operator session payload. *Stop-loss:* a
  dedicated route is the fallback if `/session` is contentious.

**plan-8 — Panel version display + "Update all agents → panel version" + refuse-newer guard.**
- *Goal:* FE shows controller version; an "Update all agents" action sets target = controller version,
  assists with `version=controllerVersion` (→ `versionApplied=true`, base pinned to the tag), fleet-wide;
  controller `validateAgentRollout` **rejects** `TargetAgentVersion > controllerVersion` with a clear
  coded error. *Hazards:* custody (assist still fills pins for operator save, no auto-trust); the
  compare must use the existing `compareVersions` semantics (empty = minimal). *Verify:* guard unit test
  (newer rejected, equal/older accepted); FE flow test. *Stop-loss:* gate the one-click behind the
  guard so a misconfig can't push newer.

**plan-9 — Default release URLs (agent + mimic) + fix "Assist from release".**
- *Goal:* add `DefaultMimicReleaseBase` (+ Default/WithDefaults); FE placeholders → real defaults; mimic
  assist works from the default base; agent assist pins to a tag (ties to plan-8's version defaulting) —
  killing the moving-alias stall. *Hazards:* the default mimic base points at the mimic UPSTREAM
  (`hack3ric/mimic` releases), not YAOG releases; keep the gh-proxy + SSRF guards intact. *Verify:*
  settings default tests; release-pins resolve test; FE build. *Stop-loss:* if the upstream mimic asset
  grammar differs, document the operator-fill path and keep the default as a helpful placeholder.

### Release

**plan-10 — Release engineering: CHANGELOG + cut & push `v2.0.0-beta.9`.**
- *Goal:* roll `CHANGELOG.md`; confirm `BuildVersion` stamping reaches the server binary; verify all CI
  green on `main`; create + push the annotated `v2.0.0-beta.9` tag (triggers `release.yml`); verify the
  release publishes as a **beta** (not Latest) with all expected assets + sidecars. *Hazards:* outward-
  facing publish — only after every plan is merged + green; `make_latest=false` for `-beta.`. *Verify:*
  `gh release view v2.0.0-beta.9` shows assets + not-latest; smoke the new defaults against the
  published assets. *Stop-loss:* if the release workflow fails, the tag can be deleted + re-cut after fix.

---

## 8. Insertion-point markers (likely plan-N.5 needs)

- **plan-1.5 — report-schema isolation:** if adding `conditions` to the report wire ripples into the
  signed-bundle/verify path, move conditions to a separate report sub-resource instead of the main
  report payload.
- **plan-5.5 — agent-side fallback re-apply:** if a fully in-script automatic mimic→UDP fallback proves
  fragile across distros, shift the mechanism to "agent detects mimic failure post-apply, re-applies a
  UDP-variant config on the next cycle" (needs a controller-side or agent-side UDP-variant render).
- **plan-8.5 — version-source hardening:** if `compareVersions` edge cases (pre-release vs release tag
  forms) cause false rejects in the refuse-newer guard, add a normalization + table test before shipping.

---

## 9. Closure criteria

- [ ] All 10 plans delivered; each PR independently workflow-reviewed (4 lenses) → findings fixed at
      root → re-reviewed clean → CI green → merged to `main`.
- [ ] Success criteria (§1) all checked.
- [ ] No principle in §2 violated (custody, deployable-config, root-script-safety, allocation stability,
      back-compat all proven by tests/gates).
- [ ] `v2.0.0-beta.9` published as a beta; defaults + assist smoked against the published assets.
- [ ] STATUS.md updated; memory updated (`pre-rc1-program-sequence` + a new subject-closure note);
      this folder `git mv`-ed to `implementation_plans/_completed/` at closure.

## 10. Plan status table

| Plan | Title | Status |
|------|-------|--------|
| plan-1 | Node Conditions channel (wire + agent + store) | merged (#162) |
| plan-2 | Operator API + panel generic conditions render | merged (#163) |
| plan-3 | self-update + wireguard conditions (retire string-match) | merged (#164) |
| plan-4 | per-link mimic fallback (model + compiler + validator + default) | merged (#165) |
| plan-5 | mimic fallback mechanism (script + agent + condition) | merged (#166) |
| plan-6 | panel mimic fallback UI + condition chip | merged (#167) |
| plan-7 | controller version self-awareness (backend) | merged (#168) |
| plan-8 | panel version + update-all-to-panel-version + refuse-newer guard | merged (#169) |
| plan-9 | default release URLs + fix assist-from-release | merged (#170) |
| plan-10 | release: CHANGELOG + cut & push v2.0.0-beta.9 | Phase I merged (#171); Phase II (tag cut) **owner-gated** |
