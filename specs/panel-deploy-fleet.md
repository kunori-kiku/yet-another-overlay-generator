# Panel deploy and Fleet

<!-- last-verified: 2026-07-17 -->

## Responsibility

Own the controller-mode Fleet registry, enrollment and node-detail integration surfaces, plus the
operator workflow that previews, stages, signs, promotes, and reconciles a deployment
(`frontend/src/components/pages/FleetPage.tsx:10-23`,
`frontend/src/components/deploy/DeployBar.tsx:35-148`,
`frontend/src/stores/controller/deploy.ts:192-470`).

## Files

- `frontend/src/components/pages/FleetPage.tsx:1-23` — composes Fleet refresh, registry, and enrollment.
- `frontend/src/components/pages/FleetNodeDetailPage.tsx:51-111,132-320` — integrates one node's
  readiness, lifecycle controls, and `panel-telemetry` components.
- `frontend/src/components/deploy/NodeRegistry.tsx:166-416` — presents fleet membership and node actions.
- `frontend/src/components/deploy/EnrollmentFlow.tsx:12-146` — mints and displays one-time enrollment material.
- `frontend/src/components/deploy/DeployBar.tsx:35-491,553-704` — owns deploy entry, warnings, preview,
  upgrade-first, and confirmation surfaces.
- `frontend/src/stores/controller/deploy.ts:59-470` — coordinates preview, topology upload, stage,
  keystone signing, promote, and post-deploy reconciliation.
- `frontend/src/stores/controller/fleet.ts:26-240` — owns authenticated Fleet reads, history dispatch,
  enrollment token minting, and node lifecycle actions.
- `frontend/src/hooks/useFleetLiveRefresh.ts:5-230` and
  `frontend/src/components/deploy/FleetRefreshControls.tsx:41-127` — run and expose the Fleet-only
  completion-based refresh loop.

## Inputs

The route shell supplies an authenticated controller session and selects the Fleet, node-detail, or
Deploy surface (`frontend/src/App.tsx:24-55`). The design store supplies the current whole topology;
controller slices supply server topology, node state, audit/readiness data, keystone descriptors, and
mutation methods (`frontend/src/stores/controller/deploy.ts:95-117`,
`frontend/src/components/pages/FleetNodeDetailPage.tsx:51-105`).

## Outputs

Fleet actions mint enrollment tokens, revoke or rekey nodes, download promoted manual bundles, and
persist node telemetry-policy edits through the whole-design Save boundary
(`frontend/src/stores/controller/fleet.ts:146-240`,
`frontend/src/components/pages/FleetNodeDetailPage.tsx:71-105,192-229`). Deploy uploads the stripped
design, stages the selected subgraph, obtains any keystone assertion, promotes it, then reconciles
controller-written allocation pins back into the canvas
(`frontend/src/stores/controller/deploy.ts:283-326,326-414`).

## Decision points (if any)

- Deploy preview is mandatory unless an old controller specifically returns 404 or 405; validation,
  security, storage, export, and network failures remain blocking
  (`frontend/src/stores/controller/deploy.ts:121-178`).
- A successor telemetry policy can either block normal deploy with an upgrade-first offer or run the
  explicitly selected legacy-compatible agent-upgrade phase; its saved successor draft remains intact
  (`frontend/src/stores/controller/deploy.ts:139-153,192-209`,
  `frontend/src/components/deploy/DeployBar.tsx:134-148,330-350`).
- Emptying the design or dropping at least half of server node IDs requires typed confirmation bound
  to the previewed snapshot before topology mutation continues
  (`frontend/src/stores/controller/deploy.ts:244-281`).

## Invariants

- Browser-to-controller topology writes strip private keys before preview or deploy, and a confirmed
  shrink deploys the exact snapshot the operator approved
  (`frontend/src/stores/controller/deploy.ts:105-117,222-243`).
- Stage, optional keystone signing, and promote remain one guarded operator sequence; a deployment
  with no staged nodes skips signing and promote instead of manufacturing a failure
  (`frontend/src/stores/controller/deploy.ts:192-209,326-399`).
- Fleet Live fetches only nodes on a non-overlapping ten-second completion cadence, pauses while the
  tab is hidden, refreshes immediately on return, and exposes freshness/failure feedback
  (`frontend/src/stores/controller/fleet.ts:42-52,114-130`,
  `frontend/src/hooks/useFleetLiveRefresh.ts:45-108,137-230`,
  `frontend/src/components/deploy/FleetRefreshControls.tsx:41-127`).

## Gotchas (optional)

- Saving active-telemetry edits persists a design draft but does not activate it; activation stays at
  Deploy, and unfinished draft probes may be saved while readiness validation blocks staging
  (`frontend/src/components/pages/FleetNodeDetailPage.tsx:71-87,192-229`).
- A node-detail force redeploy still uploads the whole current canvas before selecting one node for
  forced staging, so unsaved or destructive canvas changes keep the ordinary shrink gate
  (`frontend/src/components/pages/FleetNodeDetailPage.tsx:90-105,246-294`,
  `frontend/src/stores/controller/deploy.ts:185-190,244-281`).
- Background Fleet reads have their own request generation and do not clear a deploy or lifecycle
  mutation's global loading/error state (`frontend/src/stores/controller/fleet.ts:27-39,76-89,114-130`).
