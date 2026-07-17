# plan-4 — Add successor signed telemetry policy and rollout readiness

**Outline:** [outline.md](./outline.md) — read it completely before this plan, especially Principles,
Standing rules, the plan-4 milestone, and the plan-status table.

**Subject:** deployment-stability-and-charted-telemetry-2026_07_17
**Track:** Go + frontend-light
**Depends on:** plan-3

## Prerequisites

- The outline status rows for plans 1–3 are `done`, their work and close-phase commits are pushed, and
  the current branch is `fix/rc12-telemetry-drafts`.
- Preserve the dirty-worktree rule from the outline: inspect `git status --short`, do not reset or
  discard unrelated edits, and stage only the paths named in this plan.
- Before editing, confirm from the released rc.9–rc.11 behavior at
  `internal/agent/agent.go:199-280` and `internal/agent/verify.go:149-255` that an old agent verifies an
  unknown checksummed member but parses strict `telemetry.json` before self-update. If an old agent
  rejects an unknown checksummed member, stop and request plan-4.5; the separate-successor rollout is
  not safe under that predecessor behavior.
- Do not infer support from `agent_version`. Readiness is an authenticated capability from the latest
  accepted heartbeat only.

## Goal

Add a separately named, strictly versioned successor executable telemetry policy without changing a
single byte of legacy version-1 `telemetry.json`. Make v1 and successor policy mutually exclusive in
bundles and durable agent state, fail closed before host mutation when a launcher lacks any required
capability, and provide a deliberate two-deployment “Upgrade agents first” path that preserves the
stored successor draft while compiling an in-memory legacy projection.

## Reads from specs

Reads from specs: agent, controller-agent-api, controller-stage-promote, artifacts-signing, render-keys, panel-deploy-fleet, model-validation

## Read first

Line anchors are against the pre-plan-4 tree; if plans 1–3 shifted them, relocate by the named symbol
instead of guessing.

1. `internal/probepolicy/policy.go:20-238` — v1 constants, private wire DTO, `Marshal`, `Parse`, and
   validation limits.
2. `internal/model/topology.go:128-157` and `frontend/src/types/topology.ts:69-84` — topology probe
   shape and Go/TypeScript omission contract.
3. `internal/compiler/compiler.go:55-68`, `internal/render/render.go:319-389`,
   `internal/artifacts/export.go:41-75,280-310`, and `internal/localcompile/compile.go:126-155` — policy
   render/export/member-set path.
4. `internal/agent/verify.go:149-255`, `internal/agent/agent.go:199-350`, and
   `internal/agent/state.go:100-135` — verified candidate, self-update order, and last-known-good state.
5. `internal/runtimecontract/installer_capability.go:1-11`,
   `internal/agent/installer_command_unix.go:33-61`, `internal/renderer/script.go:100-112,230-240,465-510`,
   `internal/renderer/templates/install.sh.tmpl:20-45`, and
   `internal/renderer/templates/client-install.sh.tmpl:20-45` — installer capability gate.
6. `internal/telemetrymetric/catalog.go:13-221`, `internal/agent/telemetry.go:21-226`, and
   `internal/controller/storecore_telemetry.go:145-215` — production metric registry and latest
   heartbeat replacement semantics.
7. `internal/controller/compile_stage.go:39-190`, `internal/controller/compile_preview.go:20-132`, and
   `internal/controller/telemetry_policy.go:1-24` — stage/preview shared decisions and keystone gate.
8. `internal/api/handler_deploy.go:20-125`, `internal/api/wire_controller.go:75-115`,
   `internal/api/errmap.go:42-70`, and `internal/apierr/apierr.go:28-120,190-240` — deploy request and
   structured-error surfaces.
9. `frontend/src/api/controller/deploy.ts:1-107`, `frontend/src/api/controller/fleet.ts:1-210`,
   `frontend/src/stores/controller/deploy.ts:1-397`,
   `frontend/src/stores/controller/types.ts:1-403`,
   `frontend/src/stores/controller/persist.ts:1-58`,
   `frontend/src/components/deploy/DeployBar.tsx:30-440`,
   `frontend/src/components/pages/FleetNodeDetailPage.tsx:45-270`, and
   `frontend/src/lib/custody.ts:75-110` — upgrade action, Fleet readiness, and non-persistence.
10. `internal/probepolicy/policy_test.go:1-175`, `internal/artifacts/export_telemetry_test.go:1-31`,
    `internal/renderer/script_telemetry_capability_test.go:1-96`,
    `internal/telemetrymetric/catalog_test.go:1-117`, and
    `internal/controller/telemetry_reliable_test.go:1-262` — existing compatibility guards to extend.

## Implementation steps

### Step 1 — Keep v1 frozen and define the successor contract

At `internal/probepolicy/policy.go:20-137`, retain `FileName`, `CurrentVersion`, `Marshal`, and `Parse`
as the strict v1 contract. Add separately named successor APIs; do not make `Parse` accept both files
and do not route v1 through the new DTO.

```go
const (
    FileName          = "telemetry.json"        // strict predecessor; version 1 forever
    SuccessorFileName = "telemetry-policy.json" // strict successor; version 2
    SuccessorVersion  = 2
)

type DeviceMode string

const DeviceModeAllEligibleV1 DeviceMode = "all-eligible-v1"

type DevicePolicy struct {
    Mode DeviceMode `json:"mode"`
}

type SuccessorPolicy struct {
    Version int                    `json:"version"`
    Probes  []model.TelemetryProbe `json:"probes,omitempty"`
    Devices *DevicePolicy          `json:"devices,omitempty"`
}

func MarshalSuccessor(policy SuccessorPolicy) ([]byte, error)
func ParseSuccessor(data []byte) (*SuccessorPolicy, error)
func ParseActive(data []byte) (*SuccessorPolicy, error)
func RequiresSuccessor(node model.Node) bool
func RequiredCapabilities(node model.Node) []string
func ProjectLegacy(node *model.Node)
```

- `Marshal`/`Parse` and their private v1 DTO remain byte-for-byte and behaviorally unchanged.
- `MarshalSuccessor` writes an explicit version 2, enforces the existing 64 KiB whole-policy cap,
  rejects an empty executable policy, and uses a private strict DTO. `ParseSuccessor` rejects unknown
  fields and trailing JSON.
- `ParseActive` inspects only the bounded root `version`, delegates to strict v1 or successor parsing,
  and exists for the single durable last-known-good state field. It does not accept a filename or
  weaken either decoder.
- `ProjectLegacy` mutates only an already-copied node: it clears successor-only device configuration
  and retains v1-capable ICMP/TCP probes. Plan 5 extends it to remove URL probes.
- Add `model.TelemetryDevicePolicy` and optional `Node.TelemetryDevices` at
  `internal/model/topology.go:128-157`, plus the exact optional mirror in
  `frontend/src/types/topology.ts:69-84`. Saving this field is permitted as draft work.
- Capability requirements are feature-specific. The foundation requires `telemetry-policy-v2`;
  device policy additionally requires `device-telemetry-v1`, which this plan deliberately does not
  advertise. That keeps a crafted device draft undeployable until plan 7 activates the collector.

### Step 2 — Render exactly one policy member and reject both-file bundles

At `internal/compiler/compiler.go:55-68`, add a separate successor map:

```go
TelemetryPolicyJSON          map[string]string // telemetry.json v1
TelemetrySuccessorPolicyJSON map[string]string // telemetry-policy.json v2
```

At `internal/render/render.go:319-389`:

- For AgentHeld nodes where `RequiresSuccessor(node)` is false, call the unchanged v1 `Marshal`.
- For a successor node, put the complete executable policy—legacy-capable probes plus successor
  fields—in `TelemetrySuccessorPolicyJSON` and leave `TelemetryPolicyJSON[node.ID]` absent.
- Air-gap results contain neither agent-only member.

Change `internal/artifacts/export.go:41-75` to make the member-set API return an error:

```go
func BundleFiles(result *compiler.CompileResult, nodeID string) (map[string]string, error)
```

Reject a node whose two maps are both non-empty; otherwise emit exactly one fixed filename. Update the
known callers in `internal/artifacts/export.go:280-310`, `internal/localcompile/compile.go:126-155`,
`internal/localcompile/contract_test.go:80-105`, and artifact tests. This is an explicitly authorized
internal signature change; do not introduce an unchecked compatibility wrapper.

### Step 3 — Make durable transitions exclusive and last-known-good

At `internal/agent/verify.go:220-252`, explicitly reject a bundle containing both policy filenames and
require either present member to be listed in `checksums.sha256`.

Extract the pre-mutation candidate logic at `internal/agent/agent.go:199-230`:

```go
func candidateTelemetryPolicy(
    files map[string][]byte,
    operatorCredentialConfigured bool,
) (json.RawMessage, error)
```

- Both files present: reject before membership verification, staging, pending intent, or installer.
- v1 present: strict `Parse`, canonicalize with unchanged `Marshal`.
- successor present: strict `ParseSuccessor`, canonicalize with `MarshalSuccessor`.
- neither present: return nil; a successful apply clears active policy.
- Any policy requires the existing off-host operator credential and membership gate.

Keep one `State.ActiveTelemetryPolicy` field at `internal/agent/state.go:124-130`. A successful apply
atomically replaces it with the one candidate; successful omission/uninstall clears it; any parse,
membership, self-update, staging, installer, or state-write failure preserves the previous bytes.
`activeProbeSampler.Sample` moves from `Parse` to `ParseActive`; it consumes only the parsed probe
subset and does not activate device work in this plan.

The permanent transition test must cover `v1 -> successor -> v1 -> omission`, successor failure
preserving v1, v1 failure preserving successor, and both-file rejection.

### Step 4 — Add launcher and heartbeat capabilities without proxy-sensitive headers

At `internal/runtimecontract/installer_capability.go:1-11`, add closed capability tokens and their
launcher environment markers:

```go
const (
    AgentCapabilityTelemetryPolicyV2 = "telemetry-policy-v2"
    AgentCapabilityURLProbesV1       = "url-probes-v1"
    AgentCapabilityDeviceTelemetryV1 = "device-telemetry-v1"

    InstallerCapabilityTelemetryPolicyV2Env = "YAOG_AGENT_CAP_TELEMETRY_POLICY_V2"
    InstallerCapabilityURLProbesV1Env       = "YAOG_AGENT_CAP_URL_PROBES_V1"
    InstallerCapabilityDeviceTelemetryV1Env = "YAOG_AGENT_CAP_DEVICE_TELEMETRY_V1"
)
```

Plan 4’s binary sets v1 and generic-v2 installer markers only. It must strip inherited values for all
known markers before setting the capabilities it actually implements. The templates gain one bounded
sorted required-marker list; successor bundles require generic v2 and each feature marker. Uninstall
remains available without markers.

Add a cataloged live-only metric at `internal/telemetrymetric/catalog.go:54-103`:

```go
const AgentCapabilitiesKey = "agent_capabilities"

type AgentCapabilitiesMetric struct {
    Capabilities []string `json:"capabilities"`
}
```

Implement a zero-I/O production sampler in `internal/agent/telemetry_capabilities.go` and register it
in `BuildTelemetry` at `internal/agent/telemetry.go:168-184`. Bound to at most 16 sorted unique tokens,
each matching `[a-z0-9][a-z0-9-]{0,62}`. The live-only reason is: executable compatibility is current
readiness, not a time-series measurement.

Do not use a custom request header: a CDN or reverse proxy may strip it and permanently wedge the
upgrade. Because `RecordTelemetry` replaces the latest metrics map, a subsequent heartbeat that omits
`agent_capabilities` clears readiness. Duplicate/replayed older samples must not replace a newer live
map. Unknown-metric forward compatibility lets a new agent report this safely to an old controller.

### Step 5 — Share readiness and implement “Upgrade agents first”

Add `internal/controller/telemetry_policy_readiness.go` with these concrete shapes:

```go
type TelemetryPolicyDeployMode string

const (
    TelemetryPolicyDeployNormal             TelemetryPolicyDeployMode = "normal"
    TelemetryPolicyDeployUpgradeAgentsFirst TelemetryPolicyDeployMode = "upgrade-agents-first"
)

type TelemetryPolicyReadinessError struct {
    NodeIDs []string
}

func PrepareTelemetryPolicyDeployment(
    topo *model.Topology,
    nodes []Node,
    mode TelemetryPolicyDeployMode,
) (projected *model.Topology, omittedNodeIDs []string, err error)
```

Rules:

- Always deep-copy the topology; never mutate the caller or stored design.
- Normal mode checks only ready managed nodes that would receive successor policy. Every exact token
  from `RequiredCapabilities(node)` must appear in that node’s latest `agent_capabilities` metric.
  Missing, malformed, or omitted latest metrics mean “not confirmed.” Return sorted node IDs in the
  typed error. Do not infer from version strings and do not allow Deploy-anyway bypass.
- Upgrade-agents-first mode calls `ProjectLegacy` on every successor-requiring node in the copy,
  returning their IDs. It emits no successor member; it may retain legacy v1 ICMP/TCP policy. It does
  not delete or rewrite successor fields in the stored topology.
- The upgrade projection is not evidence of readiness. The ordinary second deployment remains blocked
  until a later accepted heartbeat reports the required capability tokens.

Extend `stageConfig`/`CompileAndStage` at `internal/controller/compile_stage.go:39-90` and
`DeployPreview` at `internal/controller/compile_preview.go:20-60` with the same mode, and call the
helper before signer/export/allocation/staged-set mutation. Both paths must compile the returned copy.

```go
func WithTelemetryPolicyDeployMode(mode TelemetryPolicyDeployMode) StageOption
func DeployPreview(
    ctx context.Context,
    store Store,
    tenant TenantID,
    topo *model.Topology,
    mode ...TelemetryPolicyDeployMode,
) (DeployPreviewResult, error)
```

Add `telemetry_policy_mode` to `stageRequestJSON`; keep the existing raw-topology preview body and use
the optional query `?telemetry_policy_mode=upgrade-agents-first`. Return omitted node IDs in preview
and stage responses so the confirmation states exactly what phase one does. Normal remains the omitted
default for old clients.

Register a precondition-failed API code such as `telemetry_policy_upgrade_required` with bounded node
parameters and English/Chinese localization. It is never classified as the old-controller 404/405
compatibility fallback.

Frontend function shapes:

```ts
export type TelemetryPolicyDeployMode = 'normal' | 'upgrade-agents-first';
export async function deployPreview(
  cfg: ControllerConfig,
  topoJSON: string,
  mode?: TelemetryPolicyDeployMode,
): Promise<DeployPreview>;
export async function stage(
  cfg: ControllerConfig,
  force?: DeployForceArg,
  mode?: TelemetryPolicyDeployMode,
): Promise<StageResult>;
```

On the exact structured readiness error, `DeployBar` offers “Upgrade agents first.” That action previews
and deploys the legacy projection while the full current canvas is still saved through
`update-topology`. Fleet node detail maps latest capability state to `Ready`, `Upgrade required`, or
`Not confirmed`. Add `agentCapabilities` to `ControllerNode`, and clear it in `stripLiveTelemetry` so it
never enters persisted Zustand state.

### Step 6 — Verification gate

Run exactly these focused commands after `gofmt` on changed Go files:

```bash
GOSUMDB=sum.golang.org GOPROXY=https://proxy.golang.org,direct \
go test ./internal/probepolicy ./internal/render ./internal/artifacts ./internal/localcompile \
  ./internal/renderer ./internal/agent ./internal/telemetrymetric ./internal/controller \
  ./internal/api ./internal/apierr ./internal/wiredrift \
  -run 'PolicyV1|SuccessorPolicy|TelemetryPolicy|BundleFiles|InstallerCapability|AgentCapabilities|UpgradeAgentsFirst|Readiness|LastKnownGood|WireDrift' \
  -count=1

cd frontend
npm run vitest -- \
  src/stores/controllerStore.telemetryPolicyUpgrade.test.ts \
  src/lib/custody.test.ts \
  src/api/controllerClient.deployPreview.test.ts
```

Do not run the full repository suite here; plan 8 owns it. Any exact-v1 byte drift, ability to export
both members, readiness inferred from version, stale capability surviving a later absent heartbeat, or
mutation of the saved successor draft is a stop-loss requiring correction before review.

### Step 7 — Independent review, fix, re-review

Use independent reviewers for:

- predecessor/successor member and durable-state exclusivity;
- parse/self-update/install order and all fail-before-mutation markers;
- shared preview/stage projection and absence of a Deploy-anyway bypass;
- latest-heartbeat replacement, replay behavior, browser persistence, and code hygiene.

Fix every actionable finding, rerun only the affected focused command plus the full gate above, and
obtain a clean re-review before committing.

### Step 8 — Exact commit and push

First verify `git diff --name-only` contains only plan-owned paths. Then use this exact form; do not
stage the outline, STATUS, other plans, or unrelated dirty files:

```bash
git add \
  internal/model/topology.go internal/compiler/compiler.go internal/probepolicy \
  internal/render internal/artifacts internal/localcompile internal/runtimecontract \
  internal/renderer/script.go internal/renderer/script_telemetry_capability_test.go \
  internal/renderer/templates/install.sh.tmpl internal/renderer/templates/client-install.sh.tmpl \
  internal/agent/agent.go internal/agent/state.go internal/agent/verify.go \
  internal/agent/installer_command_unix.go internal/agent/telemetry.go \
  internal/agent/telemetry_capabilities.go internal/agent/telemetry_policy_transition_test.go \
  internal/telemetrymetric internal/controller/compile_stage.go \
  internal/controller/compile_preview.go internal/controller/telemetry_policy_readiness.go \
  internal/controller/telemetry_policy_readiness_test.go internal/api/handler_deploy.go \
  internal/api/wire_controller.go internal/api/errmap.go internal/apierr \
  frontend/src/types frontend/src/api/controller frontend/src/stores/controller \
  frontend/src/stores/controllerStore.telemetryPolicyUpgrade.test.ts \
  frontend/src/components/deploy/DeployBar.tsx \
  frontend/src/components/pages/FleetNodeDetailPage.tsx frontend/src/lib/custody.ts \
  frontend/src/lib/custody.test.ts frontend/src/i18n/messages/en.ts frontend/src/i18n/messages/zh.ts
GIT_AUTHOR_NAME='KunoriKiku' GIT_AUTHOR_EMAIL='rokuyanlin@gmail.com' \
GIT_COMMITTER_NAME='KunoriKiku' GIT_COMMITTER_EMAIL='rokuyanlin@gmail.com' \
git commit -F - <<'EOF'
feat(telemetry): add versioned policy readiness

Preserve strict telemetry.json v1 bytes, add an exclusive signed successor member, and gate its
deployment on latest-heartbeat capabilities. Provide an explicit legacy projection for the first
agent-upgrade deployment without erasing the saved successor draft.
EOF
git push origin fix/rc12-telemetry-drafts
```

After the work commit is pushed, let `execute-implementation-plan` update the outline status in its
separate bookkeeping commit and hand this plan to `close-phase`.

## Tests produced by this plan

- `internal/probepolicy/policy_test.go`
  - **Lifecycle:** perpetual
  - **Guards:** exact v1 bytes plus strict, bounded successor parsing without weakening either decoder.
  - **Retirement trigger:** never while v1 fleets or successor policy are supported.
  - **Retirement destination:** `tests/legacy/perpetual/internal/probepolicy/policy_test.go` only after an explicitly approved compatibility retirement.
- `internal/artifacts/export_telemetry_test.go`
  - **Lifecycle:** perpetual
  - **Guards:** exactly one of the predecessor/successor members is checksummed and exported.
  - **Retirement trigger:** never while either member is accepted.
  - **Retirement destination:** `tests/legacy/perpetual/internal/artifacts/export_telemetry_test.go`.
- `internal/agent/telemetry_policy_transition_test.go`
  - **Lifecycle:** perpetual
  - **Guards:** exclusive v1/successor transitions, both-file rejection, omission clearing, and failed-candidate last-known-good preservation.
  - **Retirement trigger:** never; this pins the subject’s HIGH compatibility principle.
  - **Retirement destination:** `tests/legacy/perpetual/internal/agent/telemetry_policy_transition_test.go`.
- `internal/controller/telemetry_policy_readiness_test.go`
  - **Lifecycle:** perpetual
  - **Guards:** latest-heartbeat capability replacement and the shared normal versus upgrade-agents-first projection used by preview and stage.
  - **Retirement trigger:** never while rolling upgrades use capability gating.
  - **Retirement destination:** `tests/legacy/perpetual/internal/controller/telemetry_policy_readiness_test.go`.
- `frontend/src/stores/controllerStore.telemetryPolicyUpgrade.test.ts`
  - **Lifecycle:** perpetual
  - **Guards:** only the structured readiness error offers phase one, the saved draft stays intact, and normal deploy remains blocked until capability confirmation.
  - **Retirement trigger:** never while the two-deployment bridge exists.
  - **Retirement destination:** `tests/legacy/perpetual/frontend/controllerStore.telemetryPolicyUpgrade.test.ts`.
- `frontend/src/lib/custody.test.ts`
  - **Lifecycle:** perpetual
  - **Guards:** live agent capabilities are stripped from browser persistence.
  - **Retirement trigger:** never while controller state persists to localStorage.
  - **Retirement destination:** `tests/legacy/perpetual/frontend/custody.test.ts`.

## Definition of done

- [ ] Legacy ICMP/TCP nodes still emit exact canonical v1 bytes and no successor member.
- [ ] Successor bundles contain exactly one signed/checksummed policy member; both-file input is rejected.
- [ ] Durable active policy is one last-known-good value across v1, successor, omission, failure, and uninstall.
- [ ] The launcher requires generic and feature-specific capabilities before normal host mutation.
- [ ] `agent_capabilities` is bounded, authenticated, live-only, replaced by the latest accepted heartbeat,
      and absent from browser persistence.
- [ ] Normal preview and stage share one readiness decision and cannot be bypassed by Deploy anyway.
- [ ] “Upgrade agents first” preserves the full saved draft, emits no successor member, and does not
      claim readiness before the later capability heartbeat.
- [ ] Focused tests pass, independent review is clean after fixes, the work commit is pushed, and
      close-phase completes the plan handoff.

## Out of scope

- URL validation or execution; plan 5 adds the URL feature capability and runner.
- Device collection, production registration, history, or charts; plans 6–7 own them.
- Inferring capabilities from semantic versions, silently stripping successor policy in normal mode,
  or allowing the old-controller Deploy-anyway path to bypass a compatibility/security failure.
- Changing `PRINCIPLES.md`, the outline, STATUS, source outside the named ownership list, release tags,
  releases, or container references.
