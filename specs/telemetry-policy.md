# Telemetry policy

<!-- last-verified: 2026-07-17 -->

## Responsibility

Own the managed-node boundary from topology telemetry intent through strict executable policy,
keystone and agent-capability readiness, and the compatibility projection used to upgrade agents
before activating successor features (`internal/model/topology.go:128-167`,
`internal/probepolicy/policy.go:179-315`,
`internal/controller/telemetry_policy_readiness.go:18-109`).

## Files

- `internal/probepolicy/policy.go:26-177` — owns the frozen version-1 and separate version-2 names,
  private wire DTOs, defaults, and topology-to-executable projections.
- `internal/probepolicy/policy.go:179-419` — strictly marshals/parses each version, selects successor
  policy and capabilities, and projects successor fields back to the legacy subset.
- `internal/probepolicy/policy.go:421-653` — validates probe bounds, typed destinations, URL success
  codes, optional names, ASCII DNS/IP hosts, and schedule defaults.
- `internal/controller/telemetry_policy.go:10-25` — requires a pinned keystone before active policy
  can proceed through deployment.
- `internal/controller/telemetry_policy_readiness.go:18-143` — implements normal capability
  readiness and the explicit upgrade-agents-first projection.
- `frontend/src/lib/deployPreview.ts:85-120` — classifies configured signed-rollout coverage for the
  deploy surface owned by `panel-deploy-fleet`.

## Inputs

A topology node may contain bounded `telemetry_probes` plus an optional `telemetry_devices` selector;
probe `name` is presentation metadata, while ID, type, typed destination, expected status, interval,
and timeout form policy input (`internal/model/topology.go:128-160`). ICMP and TCP accept one IP or
ASCII DNS host, TCP additionally requires one port, and URL uses a distinct absolute HTTP(S) target
with one exact success code whose zero-value default is 200
(`internal/probepolicy/policy.go:421-505`, `internal/probepolicy/policy.go:515-638`).

URL probes or automatic device telemetry require the successor format. The required authenticated
agent tokens are the generic version-2 token plus the URL and/or device feature token selected by
that node (`internal/probepolicy/policy.go:360-391`, `internal/telemetrycap/capability.go:8-24`).

## Outputs

AgentHeld rendering emits compact `telemetry.json` version 1 for legacy ICMP/TCP-only policy, or
compact `telemetry-policy.json` version 2 when any successor feature is present; AirGap rendering
emits neither (`internal/render/render.go:371-400`). The version-1 DTO cannot acquire URL, device, or
display-name fields, and exact regression bytes remain pinned separately from the successor bytes
(`internal/probepolicy/policy.go:89-129`, `internal/render/telemetry_policy_test.go:39-47`,
`internal/render/telemetry_policy_test.go:80-93`).

The bundle constructor rejects a node that contains both filenames and includes the selected member
only for AgentHeld custody (`internal/artifacts/export.go:69-79`). Checksumming, signing, off-host
membership, and last-known-good activation are described in
[Artifacts and signing](artifacts-signing.md), [Keystone and trust lists](keystone-trustlist.md), and
[Agent runtime](agent.md).

## Decision points (if any)

- Normal deploy validates the ready subgraph, then requires each ready managed successor-policy node
  to advertise every exact capability in its latest authenticated telemetry metric; absent or
  malformed evidence blocks the named nodes (`internal/controller/telemetry_policy_readiness.go:55-66`,
  `internal/controller/telemetry_policy_readiness.go:86-104`,
  `internal/controller/telemetry_policy_readiness.go:111-143`).
- Upgrade-agents-first deep-copies the topology, validates the ready successor shape, removes URL and
  device fields only from the deployment copy, preserves version-1-compatible ICMP/TCP probes, and
  returns sorted IDs for affected ready nodes without rewriting the saved draft
  (`internal/controller/telemetry_policy_readiness.go:36-85`,
  `internal/probepolicy/policy.go:393-407`).
- Missing or partial signed-agent rollout coverage is advisory: the dialog shows which omitted nodes
  need Settings coverage or an out-of-band update, while confirmation remains disabled only during an
  in-flight deploy (`frontend/src/lib/deployPreview.ts:100-120`,
  `frontend/src/components/deploy/DeployBar.tsx:619-707`,
  `frontend/src/i18n/messages/en.ts:331-334`).

## Invariants

- `telemetry.json` version 1 remains a strict, byte-compatible legacy member; successor fields use the
  separately named version-2 member, and both members may never coexist
  (`internal/probepolicy/policy.go:89-129`, `internal/probepolicy/policy.go:179-315`,
  `internal/artifacts/export.go:69-79`).
- Probe `name` is controller/Fleet metadata only: private wire projections omit it. History identity is
  stable ID plus exact typed destination—ID/type/host/port, or ID/type/URL/effective expected status—so
  cadence edits do not split a series; a name-only change leaves bundle bytes and generation unchanged
  (`internal/probemetric/result.go:229-249`, `internal/probepolicy/policy.go:131-177`,
  `internal/probepolicy/policy.go:588-609`,
  `internal/controller/telemetry_probe_name_delta_test.go:107-180`).
- Active policy is managed-node, AgentHeld, keystone-bound authority: schema rejects manual-node
  policy, render omits it outside AgentHeld custody, and deploy preview/stage refuse it without the pinned
  credential (`internal/validator/schema.go:346-364`, `internal/render/render.go:371-400`,
  `internal/controller/telemetry_policy.go:10-25`, `internal/controller/compile_stage.go:164-180`).

## Gotchas (optional)

- Readiness validates after projecting the ready subgraph, so unfinished policy on an unready managed
  node does not block unrelated nodes; it becomes a structured schema failure when that node becomes
  ready (`internal/controller/telemetry_policy_readiness.go:55-66`,
  `internal/controller/telemetry_policy_readiness_test.go:119-158`).
- Upgrade-agents-first does not install an agent by itself; it only permits an already configured
  signed rollout, and uncovered nodes require Settings changes or an out-of-band update
  (`frontend/src/lib/deployPreview.ts:100-120`, `frontend/src/i18n/messages/en.ts:332-334`).
- Before any whole-topology write containing successor fields, the panel rechecks an authenticated
  controller schema capability so an old or rolled-back controller cannot silently erase the draft
  (`frontend/src/stores/controller/helpers.ts:33-47`, `internal/telemetrycap/capability.go:16-19`).
