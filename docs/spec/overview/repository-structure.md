# Repository Structure

YAOG is one Go module with a React/Vite panel. The Go side uses the standard-library
`net/http` stack rather than a web framework. Local browser compute is the same Go pipeline compiled
to WebAssembly; the controller is the stateful network service, and the node agent owns each node's
WireGuard private key.

## Package map

There are **25 direct package directories under `internal/`**:

`agent`, `allocator`, `allocconst`, `api`, `apierr`, `arch`, `artifacts`, `bundlesig`, `compiler`,
`controller`, `dast`, `edgecase`, `linkid`, `localcompile`, `model`, `naming`, `normalize`,
`regression`, `render`, `renderer`, `runtimecontract`, `trustlist`, `validator`, `version`, and
`wiredrift`.

They divide into four structural groups:

- **Pure compile core:** `model`, `allocconst`, `linkid`, `naming`, `allocator`, `validator`,
  `compiler`, `render`, `renderer`, `artifacts`, `normalize`, and `localcompile`. The architecture
  test walks their transitive imports and rejects any dependency on `controller`, `api`, `agent`, or
  `runtimecontract`; its exception allowlist is empty (`internal/arch/layers_test.go:12-50,62-102`).
- **Stateful runtime:** `controller` owns tenant-scoped persistence, staging, enrollment, operators,
  audit, telemetry history, and trust transitions; `api` adapts those operations to HTTP; `agent`
  enrolls, polls, verifies, applies, reports, and self-updates; `runtimecontract` holds stateful
  agent-to-controller condition DTOs (`internal/runtimecontract/condition.go:1-35`).
- **Cryptographic and contract leaves:** `bundlesig` owns canonical bundle signing; `trustlist` owns
  keystone canonicalization/pins/WebAuthn verification; `apierr` owns stable coded errors; `version`
  owns shared version ordering.
- **Verification-only packages:** `arch`, `dast`, `edgecase`, `regression`, and `wiredrift` turn
  architectural, HTTP-security, adversarial-input, anti-rollback, and Go/TypeScript wire-drift rules
  into tests. The wire-drift gate reads both languages' source contracts and fails in both mismatch
  directions (`internal/wiredrift/drift_test.go:17-67`).

The compilation/render boundary is intentional: `render` is the key-custody and bundle-assembly
orchestrator and imports `renderer`; `renderer` contains the text/template emitters and never imports
`render` (`internal/render/render.go:1-29`). `localcompile` is the stable façade over the entire pure
pipeline and the contract consumed by both the CLI/controller and WebAssembly shim
(`internal/localcompile/contract.go:1-27`).

## Source tree

The tree below is representative of maintained source and contract directories; generated binaries,
build outputs, caches, and fixture detail are omitted.

```text
yet-another-overlay-generator/
├── cmd/
│   ├── agent/                 # Node-agent CLI
│   ├── compiler/              # Local/AirGap compiler CLI
│   ├── server/                # Controller-only HTTP service + operator admin CLI
│   ├── wasm/                  # js/wasm bridge over internal/localcompile
│   ├── e2eagent/              # Playwright controller-agent harness
│   └── e2eserver/             # Playwright controller-server harness
├── internal/
│   ├── model/                 # Pure topology/artifact value types
│   ├── allocconst/            # Shared allocation constants (zero-import leaf)
│   ├── linkid/                # Canonical stable link identity
│   ├── naming/                # Canonical artifact/interface naming
│   ├── allocator/             # Overlay/transit allocation
│   ├── validator/             # Schema + semantic validation
│   ├── compiler/              # Multi-pass topology compiler + peer derivation
│   ├── render/                # Key custody, full render, artifact manifest assembly
│   ├── renderer/              # WireGuard/Babel/sysctl/install/deploy emitters
│   ├── artifacts/             # Bundle file set and export adapter
│   ├── normalize/             # Persisted allocation-pin healing
│   ├── localcompile/          # Pure pipeline façade + frozen WASM/Go contract
│   ├── controller/
│   │   ├── store.go              # Store interface + tenant/runtime types
│   │   ├── storecore.go          # Shared locked store behavior
│   │   ├── memstore.go           # In-memory implementation
│   │   ├── filestore*.go         # Durable implementation and I/O/audit split
│   │   ├── compile*.go           # Preview/subgraph/stage/promote/manual-node paths
│   │   ├── enrollment.go         # One-use node enrollment
│   │   ├── login_challenge.go    # Single-use assertion challenges
│   │   ├── operator.go           # Operator + login credential model
│   │   ├── keystone.go           # Pinned operator credential / membership state
│   │   ├── trustlist_sign.go     # Stage-sign commit path
│   │   ├── rekey.go              # Fleet/node rekey state
│   │   └── telemetry_history.go  # Bounded resource-history storage
│   ├── api/
│   │   ├── server.go             # Two muxes, health route, lifecycle/timeouts
│   │   ├── routes_controller.go  # Operator/agent registration and middleware
│   │   ├── adapter.go            # Shared method + structural identity adapter
│   │   ├── auth_controller.go    # Session/bearer/CSRF and node-auth chokepoints
│   │   ├── handler.go            # Public health + common HTTP helpers/DTO
│   │   ├── handler_{login,totp,passkey}.go
│   │   ├── handler_webauthn_enrollment.go
│   │   ├── handler_{deploy,keystone,topology}.go
│   │   ├── handler_{agent,enrollment,rekey}.go
│   │   ├── handler_{settings,bootstrap,manual_node}.go
│   │   ├── handler_audit.go
│   │   ├── release_{pins,assets}.go
│   │   ├── telemetry_history.go
│   │   ├── wire_controller.go    # Server-side JSON DTOs
│   │   └── static.go             # Optional built-panel SPA serving
│   ├── agent/                 # Pull/verify/apply loop, telemetry, self-update
│   ├── runtimecontract/       # Curated agent condition wire types
│   ├── bundlesig/             # Canonical per-node bundle signatures
│   ├── trustlist/             # Keystone pins/canonical manifest/assertion verifier
│   ├── apierr/                # Stable API error code registry
│   ├── version/               # Shared SemVer-ish comparator
│   └── {arch,dast,edgecase,regression,wiredrift}/  # Structural/test gates
├── frontend/
│   ├── src/
│   │   ├── App.tsx               # Router + mode-aware deep-link guards
│   │   ├── components/           # Shell, canvas, pages, deploy/fleet/security UI
│   │   ├── stores/
│   │   │   ├── topologyStore.ts      # Design/canvas + local WASM actions
│   │   │   ├── controllerStore.ts    # Stable composed controller hook
│   │   │   ├── controller/           # Auth/fleet/deploy/keystone/settings/sync slices
│   │   │   └── uiStore.ts            # Shell/appearance UI state
│   │   ├── api/
│   │   │   ├── controllerClient.ts   # Stable re-export barrel
│   │   │   └── controller/           # Per-domain client + shared transport
│   │   ├── lib/                  # Custody, WebAuthn, normalization, derivations
│   │   ├── wasm/                 # Browser loader/recovery wrapper
│   │   ├── i18n/                 # Keyed EN/ZH catalogs and error localization
│   │   ├── types/                # Topology + controller runtime types
│   │   └── theme/ + ui/          # Appearance provider and shared fields
│   └── e2e/                   # Playwright functional/security/adversarial specs
├── docs/spec/                      # Maintained deep design specifications
├── specs/                          # Cached per-component architectural reading layer
├── implementation_plans/           # Active/completed foldered implementation plans
├── test/realtunnel/                # Linux namespace/systemd real-tunnel gate
├── scripts/
│   ├── build-wasm.sh
│   ├── wasm-conformance-gate.mjs
│   └── deploy.{sh,ps1}
├── .github/workflows/              # CI, release, Docker, real-tunnel, WASM soak
├── examples/                       # simple-mesh, nat-hub, relay-topology fixtures
├── PRINCIPLES.md                   # Project invariants
├── STATUS.md                       # Current delivery/release status
└── RELEASING.md                    # Release procedure and verification ledger
```

## HTTP framework and route topology

The server constructs two `http.ServeMux` instances. The operator/panel mux always exposes
`GET /api/health`; enabling the controller adds operator routes to that mux and agent routes to the
second mux/port (`internal/api/server.go:44-80,90-95`). Both application listeners are plain HTTP;
production confidentiality is delegated to a TLS-terminating reverse proxy
(`cmd/server/main.go:26-42,149-152`).

There is one controller build and no `airgap` build-tag variant. `cmd/server` is controller-only and
fails when the controller state directory or tenant configuration is absent
(`cmd/server/main.go:26-37`). The retired anonymous `/api/validate`, `/api/compile`, `/api/export`, and
`/api/deploy-script` routes remain absent; local validation/compile/export/script generation execute
the Go pipeline in browser WASM (`internal/api/server.go:90-95`;
`cmd/wasm/main.go:3-27,50-64`).

### Agent mux

Agent routes mount under the optional `YAOG_AGENT_PATH_PREFIX` plus `/api/v1/agent/`:

- `POST /enroll` — pre-auth single-use enrollment token, with its own IP rate limit.
- `GET /bootstrap` — generic unauthenticated bootstrap script.
- `GET /config`, `GET /poll`, `POST /report`, `POST /telemetry`, and `POST /rekey` — per-node bearer
  authentication and rate limiting.

The exact registrations and middleware composition are in
`internal/api/routes_controller.go:231-260`.

### Operator mux

Operator routes mount under the optional `YAOG_OPERATOR_PATH_PREFIX` plus `/api/v1/operator/`.
Only password login and passwordless passkey begin/finish are reachable before operator auth. Logout,
session/account security, WebAuthn enrollment begin, topology/deploy/fleet, settings/release helpers,
and keystone routes all pass through the named-session or break-glass auth/CSRF chokepoint
(`internal/api/routes_controller.go:263-350`).

Typed single-method routes use `op`/`opRaw`, which centralize the method guard and structural identity
check before handler dispatch. Multi-method or identity-free-leg routes remain explicit rather than
weakening the adapter contract (`internal/api/adapter.go:3-21,32-97`;
`internal/api/routes_controller.go:271-278`).

## Frontend framework boundaries

- React Router owns the application shell routes; controller-only deep links are guarded even when
  entered directly (`frontend/src/App.tsx:24-61`).
- React Flow owns the visual graph canvas, but `topologyStore` owns the design state and local WASM
  actions. `controllerStore` independently owns network/session/fleet/deploy state and is composed
  from domain slices behind one persistence allowlist
  (`frontend/src/stores/controllerStore.ts:1-50`).
- The controller HTTP client follows the same domain split: `frontend/src/api/controllerClient.ts` is a stable barrel,
  while auth/fleet/deploy/keystone/settings/release/telemetry and WebAuthn enrollment live under
  `api/controller/` over one transport (`frontend/src/api/controllerClient.ts:1-30`).
- Internationalization is a keyed EN/ZH catalog under `frontend/src/i18n/`; English is the complete
  type authority and other catalogs fall back per key (`frontend/src/i18n/index.ts:4-45`).
- Local compute has no TypeScript compiler twin or server fallback. `frontend/src/wasm/wasmEngine.ts` calls the
  `cmd/wasm` JSON-string bridge, and the permanent conformance gate compares the WASM output with the
  frozen Go oracle (`cmd/wasm/main.go:3-27`).

## Structural hygiene gates

- `internal/arch`: pure/stateful dependency ratchet, with no current exceptions.
- `internal/wiredrift`: Go model/server/agent DTO versus frontend snake_case/omitempty drift gate.
- `internal/dast`: authenticated and unauthenticated HTTP security checks.
- `internal/edgecase` and `internal/regression`: adversarial compiler and keystone/stage regressions.
- `scripts/wasm-conformance-gate.mjs`: byte-level Go/WASM parity.
- `test/realtunnel`: Linux namespace/systemd integration of rendered bundles.
- Frontend Vitest, Playwright security/adversarial suites, ESLint, Go vet/race/coverage, vulnerability
  scanning, and formatting/drift checks are wired into CI/release workflows under
  `.github/workflows/`.
