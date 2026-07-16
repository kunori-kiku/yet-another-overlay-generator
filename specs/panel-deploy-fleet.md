# Panel Deploy & Fleet

<!-- last-verified: 2026-07-15 -->

## Responsibility

Drive the controller deploy pipeline from the current design through preview, public-key-only upload,
stage, optional keystone signing, promote, and reconciliation. Present the fleet registry,
enrollment, node detail/telemetry, manual-node bundles, agent self-update rollout, mimic catalog,
WireGuard rekey workflow, and local-mode WASM downloads without crossing the controller's
zero-knowledge boundary.

The frontend state is deliberately split. `topologyStore` owns the design/canvas and local WASM
compute; `controllerStore` is a separate composed store for connection/auth, fleet, deploy,
keystone, settings, and server synchronization (`frontend/src/stores/controllerStore.ts:1-50`;
`frontend/src/stores/controller/types.ts:28-31`). Deploy actions read the current topology lazily
from `topologyStore`, strip private keys, and then use controller APIs.

## Files

- `frontend/src/stores/controllerStore.ts:1-50` — stable public store hook composed from six
  per-domain slices under one `create()+persist` boundary.
- `frontend/src/stores/controller/deploy.ts:1-366` — server compile preview, deploy dry-run,
  shrink guard, `update-topology → stage → sign → promote`, and post-deploy pin reconciliation.
- `frontend/src/stores/controller/fleet.ts:1-152` — fleet refresh, history read, token mint, revoke,
  rekey/clear-rekey, and manual-node bundle download.
- `frontend/src/stores/controller/keystone.ts:1-253` — server-authoritative signing-key status,
  prospective enrollment/rotation, volatile retry candidate, and public signing-handle recovery.
- `frontend/src/stores/controller/settings.ts:1-63` — server settings load/save and release-assist
  calls.
- `frontend/src/stores/controller/sync.ts:52-325` — server-authoritative design hydration/save,
  conflict handling, import, and mode-boundary custody.
- `frontend/src/stores/controller/helpers.ts:173-258` — effective auth projection and deploy/fleet
  selectors, including the advisory `selectRekeyingCount` and local signing-handle check.
- `frontend/src/stores/controller/persist.ts:1-58` — single controller-state persistence allowlist
  plus static-local-mode merge guard.
- `frontend/src/components/pages/DeployPage.tsx` — mode split between local downloads and controller
  deployment.
- `frontend/src/components/deploy/DeployBar.tsx:25-31,35-147,149-270` — Deploy/Roll-keys,
  pre-deploy confirmation, advisory rekey warning, keystone status/enrollment/rotation, and orphan
  actions.
- `frontend/src/components/deploy/CompilePreview.tsx` and
  `frontend/src/components/deploy/LocalDeploy.tsx` — rendered preview and local WASM compile/export /
  deploy-script downloads.
- `frontend/src/components/deploy/EnrollmentFlow.tsx` — one-use enrollment token plus bootstrap and
  manual enrollment commands.
- `frontend/src/components/deploy/NodeRegistry.tsx:25-42,52-149,151-240` — shared desktop/mobile
  field descriptor, fleet state, drift/rekey/orphan badges, cancel-rekey, revoke, and manual-node
  handling.
- `frontend/src/components/pages/FleetPage.tsx:8-28` and
  `frontend/src/hooks/useFleetLiveRefresh.ts:4-37` — registry/enrollment page, refresh-on-entry, and
  default-off 20-second live polling that pauses in a hidden tab.
- `frontend/src/components/pages/FleetNodeDetailPage.tsx` — node detail, telemetry, conditions,
  resource history, and force-redeploy surface.
- `frontend/src/components/deploy/BootstrapSettings.tsx`,
  `frontend/src/components/deploy/AgentUpdateSettings.tsx`,
  `frontend/src/components/deploy/MimicCatalogSettings.tsx`, and
  `frontend/src/components/deploy/TelemetryHistorySettings.tsx` — full-replace controller settings
  editors.
- `frontend/src/api/controllerClient.ts:1-30` — compatibility barrel over domain-specific client
  modules.
- `frontend/src/api/controller/deploy.ts:1-107`, `frontend/src/api/controller/fleet.ts`,
  `frontend/src/api/controller/keystone.ts:1-197`, and
  `frontend/src/api/controller/settings.ts:37-203` — deploy/fleet/keystone/settings wire boundaries
  over shared transport.
- `frontend/src/lib/webauthn.ts:211-379,391-465` — new-key enrollment proof and later
  content-bound manifest assertion.

Routes are declared centrally in `frontend/src/App.tsx:36-61`. Overview, fleet, and fleet detail are
controller-only deep links; design, deploy, security, and settings remain reachable in both modes and
split internally (`frontend/src/App.tsx:24-34,43-58`).

## Inputs

- **Topology to deploy:** `useTopologyStore.getState().getTopology()`. The deploy and preview paths
  strip all private keys before serialization (`frontend/src/stores/controller/deploy.ts:99-107,127-210`).
- **Authentication:** effective bearer/session-cookie and CSRF state from `configOf`
  (`frontend/src/stores/controller/helpers.ts:173-184`); see `specs/panel-auth.md`.
- **WebAuthn:** an authenticated, `keystone`-purpose enrollment nonce for a new/rotated signing key,
  and the canonical staged manifest bytes for later signing
  (`frontend/src/stores/controller/keystone.ts:102-148`;
  `frontend/src/stores/controller/deploy.ts:243-302`).
- **Controller responses:** snake_case operator API data mapped at the per-domain API boundary. The
  shared transport targets `<baseURL><pathPrefix>/api/v1/operator/<route>` and sends
  Bearer/cookie/CSRF credentials (`frontend/src/api/controller/transport.ts:46-60,84-148`).
- **Local mode:** the Go/WASM engine behind `topologyStore` is the only local compute engine; there is
  no anonymous server compute fallback (`frontend/src/stores/topologyStore.ts:50-70,727-879`).

## Outputs

- **Deploy mutations:** public-key-only `updateTopology`, optional force-aware `stage`, optional
  `postTrustlistSignature`, and `promote` (`frontend/src/stores/controller/deploy.ts:209-303`).
- **Dry-run and compile previews:** a side-effect-free deploy blast-radius response and a
  server-authoritative compile preview with placeholder private keys
  (`frontend/src/api/controller/deploy.ts:37-97`).
- **Fleet mutations:** one-use token mint, revoke, fleet-wide rekey request, per-node clear-rekey,
  and manual-node bundle download (`frontend/src/stores/controller/fleet.ts:72-150`).
- **Keystone pin:** the candidate's public descriptor plus a one-use, UV-bearing enrollment proof to
  `POST /operator-credential` (`frontend/src/stores/controller/keystone.ts:102-166`;
  `frontend/src/api/controller/keystone.ts:47-71,134-166`).
- **Node-holder commands:** a one-shot bootstrap command or explicit `agent enroll` command composed
  from server-reported agent URL/prefix settings (`frontend/src/components/deploy/EnrollmentFlow.tsx`).
- **Local downloads:** WASM-built artifact ZIP and `.sh`/`.ps1` scripts; controller mode refuses
  these private-key-requiring actions (`frontend/src/stores/topologyStore.ts:815-879`).
- **Fleet view state:** in-memory `nodes`, `audit`, `lastDeploy`, and settings, with live-only
  detail/history fetched on demand. Only the telemetry-stripped node/settings/timestamp subset enters
  the persistence allowlist (`frontend/src/stores/controller/fleet.ts:21-70`;
  `frontend/src/stores/controller/persist.ts:29-42`).

## Decision points

- **Pre-deploy preview:** Deploy first POSTs the exact stripped canvas it would upload. If preview is
  unavailable, the UI exposes an explicit "Deploy anyway" fallback instead of deadlocking deployment
  (`frontend/src/stores/controller/deploy.ts:90-118`;
  `frontend/src/components/deploy/DeployBar.tsx:113-136`).
- **Telemetry display metadata:** an optional probe `name` is presentation-only and falls back to the
  stable ID. A name-only Fleet Save updates the controller design without changing agent
  `telemetry.json`, result/history identity, bundle digests, or deployment generation, so it does not
  require restaging.
- **Shrink guard:** emptying a non-empty server design or dropping at least half its node IDs requires
  typing the project name (or a non-empty sentinel). The confirmation carries a snapshot and any
  force selection so the eventual upload is exactly what was reviewed
  (`frontend/src/stores/controller/deploy.ts:127-207`).
- **Nothing staged:** an empty `result.staged` means no bundle is available to promote, not an error;
  the store skips both signing and promote (`frontend/src/stores/controller/deploy.ts:238-243`).
- **Keystone branch:** when `GET /trustlist` returns a manifest, the panel recovers/checks the local
  public signing descriptor, signs SHA-256 of the decoded canonical bytes with the pinned credential,
  echoes those exact bytes with the assertion, and promotes only after the signature is accepted.
  A null trust list takes the keystone-off direct-promote path
  (`frontend/src/stores/controller/deploy.ts:243-303`).
- **Prospective keystone enrollment:** first pin and acknowledged rotation begin with an actor- and
  purpose-scoped server nonce. The browser creates a candidate, then that exact candidate asserts the
  nonce with `userVerification:'required'`; only a server-verified UV proof is stored. Failure retains
  the public candidate in memory so another click retries verification with a fresh nonce instead of
  creating a duplicate (`frontend/src/stores/controller/keystone.ts:102-188`;
  `frontend/src/lib/webauthn.ts:211-334`).
- **Existing signing credentials remain usable:** later manifest assertions request UV as
  `preferred`, and the server/node generic verifier continues to accept a user-present, correctly
  bound, valid signature even if UV is absent. This avoids stranding already-deployed nodes or
  operators with historical credentials (`frontend/src/lib/webauthn.ts:338-379,391-465`;
  `internal/trustlist/webauthn.go:43-73,118-146`).
- **Keystone rotation is explicit:** a server-pinned key arms a separate confirmation instead of
  immediately starting WebAuthn. Rotation can strand the fleet until nodes are reprovisioned and a
  fresh deployment is signed (`frontend/src/stores/controller/keystone.ts:109-123,138-188`;
  `frontend/src/components/deploy/DeployBar.tsx:190-270`).
- **WireGuard rekey is advisory, not a hard deploy gate:** `selectRekeyingCount` drives a title and
  confirmation. Deploy remains enabled. Nodes that already re-registered are compiled with their new
  public key; a straggler is compiled with its current key, may rotate afterward, and then needs
  another Deploy. `clearRekey` releases a node that will never complete without revoking it
  (`frontend/src/stores/controller/helpers.ts:228-236`;
  `frontend/src/components/deploy/DeployBar.tsx:78-136,156-173`;
  `frontend/src/stores/controller/fleet.ts:96-136`).
- **Post-deploy reconciliation:** re-read the server topology and overlay allocation pins when the
  node/edge ID sets still match; perform a full server hydrate only after a concurrent structural
  change (`frontend/src/stores/controller/deploy.ts:304-341`).
- **Mode split:** local mode uses WASM compile/export; controller mode uploads a key-free design and
  stages server bundles. Switching a confidential server mirror to local flushes it (with a backup
  when dirty); local-original work is retained but loses controller-bound keys/pins/history
  (`frontend/src/stores/controller/sync.ts:258-323`).

## Invariants

- **Zero-knowledge upload:** deploy, preview, save, and import strip or drop private keys before any
  controller request. A server-hydrated canvas is treated as a confidential disposable mirror and is
  not written as topology data to localStorage (`frontend/src/stores/controller/deploy.ts:99-107,157-164`;
  `frontend/src/stores/topologyStore.ts:883-915`).
- **One controller-store persistence allowlist:** localStorage contains exactly `baseURL`,
  `pathPrefix`, `agentBaseURL`, the four non-secret keystone descriptor fields, `mode`,
  telemetry-stripped `nodes`, `settings`, and `lastSyncedAt`. It excludes tokens, CSRF, live
  telemetry, deploy previews, sync baselines, ceremony flags, and both pending WebAuthn candidates
  (`frontend/src/stores/controller/persist.ts:14-45`).
- **Audit verification precedes filtering:** the fetched audit array remains the complete raw chain
  and drives the integrity badge. The visible table filters only legacy `action:"report"` rows after
  that verification; current routine reports update Fleet state without entering the durable chain.
- **The persisted fleet cache is advisory:** it provides instant coloring only. Rekey state can prompt
  or over-warn after reload, but it never authorizes a server mutation; controller auth and
  stage/promote remain authoritative (`frontend/src/stores/controller/persist.ts:19-31`;
  `frontend/src/components/deploy/DeployBar.tsx:78-91`).
- **Server truth decides keystone status:** the enrolled chip and rotation gate use
  `serverOperatorPinned`, not the browser descriptor cache. A complete recoverable WebAuthn tuple
  atomically replaces a stale or partial browser handle; unpinned, raw-Ed25519, and incomplete
  status clears an incompatible browser handle. Concurrent probes are latest-started-wins, an exact
  pending-candidate match completes lost-response recovery, and none of these reads re-pins a key
  (`frontend/src/stores/controller/keystone.ts:29-80,150-188,192-248`).
- **Sign before promote:** when the keystone is on, the manifest assertion must be accepted before
  `promote`; the browser-provided public-key field is audit-only, while the server/node trust the
  pinned key (`frontend/src/stores/controller/deploy.ts:243-303`;
  `frontend/src/api/controller/keystone.ts:19-29`).

## Gotchas

- `trustlist_json` is standard padded base64, while every `SignedTrustList` field is base64url without
  padding. Mixing the dialects changes the manifest challenge and fails verification
  (`frontend/src/stores/controller/deploy.ts:24-41`;
  `frontend/src/api/controller/keystone.ts:19-45`).
- Roll-keys is a multi-step convergence workflow: request rotation, let agents regenerate locally and
  re-register public keys, then Deploy. Deploying during rotation is allowed but may require one more
  Deploy after stragglers finish. A stale `rekeyRequested` cache must never be described as a hard
  security gate (`frontend/src/stores/controller/fleet.ts:96-118`;
  `frontend/src/components/deploy/DeployBar.tsx:87-112`).
- WebAuthn enrollment requests no attestation. Its UV proof protects the first-party enrollment flow,
  but does not establish hardware provenance or non-exportability; backup/sync eligibility is a
  separate property. The shared warning is rendered on both login and keystone surfaces
  (`frontend/src/lib/webauthn.ts:241-268,303-334`;
  `frontend/src/components/deploy/WebAuthnEnrollmentNotice.tsx:4-11`).
- The plaintext enrollment token is shown once and retained only in component state; refreshing the
  page loses it and requires minting another (`frontend/src/components/deploy/EnrollmentFlow.tsx`).
- Manual nodes never enroll and therefore have no registry heartbeat. Their readiness is
  operator-asserted, and the panel offers a promoted placeholder-key bundle for manual installation
  (`frontend/src/components/deploy/NodeRegistry.tsx:186-200`;
  `frontend/src/stores/controller/fleet.ts:137-150`).
- Settings POST is full-replace. Every editor must round-trip the complete `ControllerSettings` value
  so an unrelated edit cannot wipe rollout, mimic, or telemetry-history configuration
  (`frontend/src/api/controller/settings.ts:165-203`).
