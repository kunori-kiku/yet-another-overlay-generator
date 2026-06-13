# Panel Deploy & Fleet

<!-- last-verified: 2026-06-13 -->

## Responsibility
Drives the controller deploy pipeline (update-topology → stage → keystone sign → promote → refresh) and the fleet UI (registry, enrollment, node detail, fleet-wide key rotation), plus the air-gap local-deploy downloads.

> **controller-server-authority-redesign (plans 5–6):** `deploy()` strips private keys
> (`lib/custody.ts` `stripPrivateKeys`) before `update-topology` — the client mirror of
> the server's 400 — and guards a shrink/empty deploy (canvas empties the server design
> or drops ≥50% of its node-ids) behind a typed project-name confirmation, computed
> against the server copy fetched best-effort. Controller-mode import placeholders private
> keys; switching controller→local purges keys/pins/compile-history behind a confirm
> (`topologyStore.purgeModeBoundaryState`). Fleet UI: NodeRegistry marks registry rows
> absent from the loaded design "not in design"; after a deploy, DeployBar lists approved
> fleet nodes absent from `lastDeploy.staged` ("enrolled but not in this design") with a
> one-click manual revoke (never automatic, D10). EnrollmentFlow composes its commands
> from the server-reported agent prefix and surfaces the token-mint design-membership
> warning. These UIs render in controller mode only (mode-gated routes).

## Files
- `frontend/src/stores/controllerStore.ts:626-766` — deploy-side store actions: `mintToken` (626-628), `enrollOperator` (636-664), `deploy` (677-733), `revoke` (736-748), `rollKeys` (754-766); fleet view state (`nodes`/`audit`/`lastDeploy`, 91-95) and `refresh` (274-304)
- `frontend/src/stores/controllerStore.ts:160-227` — `configOf` effective-bearer helper (163-170), selectors `selectHasAuth` (184-188), `base64StdToBytes` (199-206), `selectRekeyingCount` (211-215), `selectOperatorEnrolled` (221-227)
- `frontend/src/components/pages/DeployPage.tsx:11-29` — `/deploy` route; branches LocalDeploy vs DeployBar on `mode`
- `frontend/src/components/deploy/DeployBar.tsx:15-232` — Deploy / Roll-keys buttons, keystone signing-key enroll box (110-151), touch-key banner (155-169), last-deploy result (197-229)
- `frontend/src/components/deploy/CompilePreview.tsx:16-146` — read-only render of `topologyStore.compileResult`: manifest, compile warnings, per-node WG/babel/sysctl/install previews, project deploy scripts
- `frontend/src/components/deploy/LocalDeploy.tsx:6-72` — air-gap mode: compile, export artifacts ZIP, download `.sh`/`.ps1` deploy scripts (all via topologyStore)
- `frontend/src/components/deploy/ConnectionSettings.tsx:8-263` — controller base URL / secret path prefix / agent URL inputs, sign-in form, break-glass token field, Connect/Refresh action
- `frontend/src/components/deploy/BootstrapSettings.tsx:13-150` — server-persisted bootstrap settings form (public agent URL, GitHub proxy, agent release base), keyed-remount controlled form (59-61)
- `frontend/src/components/deploy/EnrollmentFlow.tsx:9-201` — mint single-use enrollment token; emits one-shot bootstrap command (34-40) and manual `agent enroll` command (26-29)
- `frontend/src/components/deploy/NodeRegistry.tsx:32-177` — fleet table (status badge, applied/desired drift, rekeying badge, revoke) + per-edge readiness list (133-173)
- `frontend/src/components/pages/FleetPage.tsx:6-13` — `/fleet` route composing NodeRegistry + EnrollmentFlow
- `frontend/src/components/pages/FleetNodeDetailPage.tsx:25-65` — `/fleet/nodes/:id` detail (registry node-id cell links here)
- `frontend/src/api/controllerClient.ts:617-769` — fleet/deploy HTTP layer: `getNodes` (620), `getAudit` (627), `mintEnrollmentToken` (646), `updateTopology` (662), `stage` (670), `promote` (681), `revoke` (690), `rekeyAll` (700), `getTrustlist` (717), `postTrustlistSignature` (739), `postOperatorCredential` (754); URL builder `ctlURL` (165-177)
- `frontend/src/types/controller.ts:8-41` — `ControllerNode`, `ControllerAuditEntry`, `StageResult` (camelCase mirrors of the operator JSON)

Routes registered in `frontend/src/App.tsx:38-40`. `AuditLog`, `TwoFactorSettings`, `PasskeySettings` live in the same directory but belong to siblings (see specs/panel-shell.md, specs/panel-auth.md).

## Inputs
- **Topology to deploy** — `useTopologyStore.getState().getTopology(): Topology` (`frontend/src/stores/topologyStore.ts:318-326`), the same `model.Topology` JSON shape `compile()` POSTs; see specs/panel-design.md.
- **Auth context** — effective bearer `sessionToken || operatorToken` + CSRF token via `configOf` (`frontend/src/stores/controllerStore.ts:163-170`); login/session lifecycle is specs/panel-auth.md.
- **WebAuthn ceremonies** — `enrollOperatorCredential` / `signManifest` from `frontend/src/lib/webauthn.ts:210,315`; see specs/panel-auth.md.
- **Controller responses** — snake_case JSON from the operator routes (`<baseURL><prefix>/api/v1/controller/<route>`, `frontend/src/api/controllerClient.ts:165-177`), mapped to camelCase at the boundary (544-566); server side is specs/controller-operator-api.md.
- **Local mode** — `compileResult` from topologyStore feeds CompilePreview (`frontend/src/components/deploy/CompilePreview.tsx:18-20`).

## Outputs
- **Deploy pipeline calls** — `updateTopology(cfg, topoJSON)` → `stage(cfg): StageResult` → optional `postTrustlistSignature` → `promote(cfg)` (`frontend/src/stores/controllerStore.ts:683-722`), landing in specs/controller-stage-promote.md and specs/keystone-trustlist.md.
- **Fleet mutations** — `revoke(cfg, nodeId)`, `rekeyAll(cfg)`, `mintEnrollmentToken(cfg, nodeId, ttlSeconds): Promise<string>` (`frontend/src/api/controllerClient.ts:690,700,646`).
- **Operator credential pin** — `postOperatorCredential(cfg, OperatorCredentialBody)` turns the keystone ON (`frontend/src/api/controllerClient.ts:754-769`).
- **Commands for node holders** — one-shot bootstrap (`bash <(curl …/bootstrap) --token … --node-id …`) and `agent enroll …` strings (`frontend/src/components/deploy/EnrollmentFlow.tsx:26-40`); consumed by specs/agent.md via specs/controller-agent-api.md. Depth: `docs/spec/controller/bootstrap.md`, `docs/spec/controller/enrollment.md`.
- **Air-gap downloads** — browser POSTs to `/api/compile`, `/api/export` (ZIP), `/api/deploy-script?format=sh|ps1` (`frontend/src/stores/topologyStore.ts:456-562`); see specs/airgap-api.md and `docs/spec/artifacts/export-bundle.md`, `docs/spec/artifacts/deploy-scripts.md`.
- **Fleet view state** — `nodes`/`audit`/`lastDeploy` consumed by NodeRegistry, FleetNodeDetailPage, DeployBar, AuditLog.

## Decision points
- **Skip promote when nothing staged**: `deploy()` only signs/promotes when `result.staged.length > 0` — an empty stage means "no nodes enrolled yet", not an error; promoting would 409 (`frontend/src/stores/controllerStore.ts:685-688`).
- **Keystone branch**: `getTrustlist` returning `null` (404 = keystone OFF) → promote directly; a manifest → require the locally-pinned credential triple, sign SHA-256 of the decoded bytes, POST the signature, then promote (`frontend/src/stores/controllerStore.ts:689-722`; `frontend/src/api/controllerClient.ts:717-732`). Depth: `docs/spec/controller/deploy.md`.
- **Rekey gate**: Deploy is disabled while `selectRekeyingCount > 0` — deploying mid-rotation recompiles with mixed old+new public keys (`frontend/src/components/deploy/DeployBar.tsx:39,73`; `frontend/src/stores/controllerStore.ts:211-215`).
- **Mode split**: `/deploy` shows LocalDeploy in `local` mode, DeployBar in `controller` mode (`frontend/src/components/pages/DeployPage.tsx:17-24`).
- **Bootstrap URL fallback**: the one-shot command curls `settings.publicAgentURL`, falling back to `agentBaseURL` with an on-screen warning when unset (`frontend/src/components/deploy/EnrollmentFlow.tsx:34-36,170-178`).
- **Registry derivations**: drift = `applied < desired` (`frontend/src/components/deploy/NodeRegistry.tsx:9-11`); an edge is "ready" iff both endpoints are `approved` (`:49-50`).

## Invariants
- **No key material in localStorage**: `partialize` persists only endpoints, the non-secret pinned-credential identifiers (credential_id/alg/rpId/public-key PEM), and an advisory nodes/settings cache; session/break-glass/CSRF tokens are memory-only (`frontend/src/stores/controllerStore.ts:768-791`). Aligns with PRINCIPLES.md "Key custody" — node WG private keys never reach the panel either (`ControllerNode` carries only `hasWGPublicKey`, `frontend/src/types/controller.ts:12-25`).
- **Sign-before-promote**: when the keystone is ON, promote is server-gated on a valid off-host signature (422 otherwise), so `deploy()` must complete the WebAuthn ceremony first (`frontend/src/stores/controllerStore.ts:670-676`).
- **Advisory cache is fail-closed**: the persisted `nodes` cache participates in exactly one gate (`selectRekeyingCount` → Deploy disable); stale data can only block a deploy, never permit one the live state would deny (`frontend/src/stores/controllerStore.ts:774-778`).

## Gotchas
- **Two base64 dialects in one flow**: `trustlist_json` is STANDARD padded base64 (pairs with Go `base64.StdEncoding`), while every `SignedTrustList` field is base64url no-pad; `base64StdToBytes` mis-decodes — and nodes reject with challenge mismatch — if either side switches dialects (`frontend/src/stores/controllerStore.ts:190-206`; `frontend/src/api/controllerClient.ts:96-141`).
- **Roll-keys is not one-shot**: `rollKeys` only marks approved nodes `rekey_requested`; agents regenerate keys and re-register on their own, and the operator must Deploy again afterward to converge the fleet — hence the confirm dialog plus the rekey Deploy gate (`frontend/src/stores/controllerStore.ts:750-766`; `frontend/src/components/deploy/DeployBar.tsx:44-55`). `selectRekeyingCount` counts only `approved` nodes because a revoked node never clears its flag (`frontend/src/stores/controllerStore.ts:212-214`).
- **Enrollment token is shown once**: the controller stores only its hash; EnrollmentFlow keeps the plaintext in component state, so a page refresh loses it and a new mint is required (`frontend/src/components/deploy/EnrollmentFlow.tsx:6-8,20`).
