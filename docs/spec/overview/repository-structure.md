# Repository Structure

The backend is organized as 17 internal packages. The compilation/render core has two
related packages worth calling out explicitly: **`render` is the orchestrator** (keygen,
signing, install-bundle assembly, the `render -> renderer` entry point) and **`renderer` is
the leaf** that holds the text/template emitters (wireguard / babel / script / deploy /
sysctl). The dependency graph is strictly downward — `render` imports `renderer`, never the
reverse.

The 17 internal packages are: `agent`, `allocator`, `api`, `apierr`, `artifacts`,
`bundlesig`, `compiler`, `controller`, `linkid`, `model`, `naming`, `normalize`,
`regression`, `render`, `renderer`, `trustlist`, `validator`.

```
yet-another-overlay-generator/
├── cmd/
│   ├── agent/main.go               # Node agent entry point (keygen→pull→verify→apply)
│   ├── compiler/main.go            # CLI compiler entry point
│   └── server/main.go              # HTTP API + controller server entry point
├── internal/
│   ├── agent/                      # Node agent: enroll, poll, verify, apply, self-update
│   │   ├── agent.go                # Agent orchestration
│   │   ├── controller_client.go    # Controller control-channel HTTP client
│   │   ├── cycle.go                # Poll/apply lifecycle loop
│   │   ├── keygen.go               # Per-node WireGuard key generation
│   │   ├── reprovision.go          # Re-enroll / reprovision-keystone flow
│   │   ├── selfupdate.go           # Signed agent self-update (canary→fleet)
│   │   ├── source.go               # Update source (GitHub release assets)
│   │   ├── state.go                # On-disk agent state
│   │   ├── verify.go               # Verify-before-exec of pulled bundles
│   │   └── version.go              # Agent version reporting
│   ├── allocator/
│   │   └── ip.go                   # Overlay IP auto-allocation from domain CIDRs
│   ├── api/                        # HTTP handlers (api + controller modes)
│   │   ├── handler.go              # Stateless API (health, validate, compile, export, deploy-script)
│   │   ├── server.go               # HTTP server setup, routing, CORS
│   │   ├── static.go               # Embedded panel static-asset serving
│   │   ├── routes_controller.go    # ControllerHandler wiring, route registrars, CORS, path prefixes
│   │   ├── wire_controller.go      # Controller HTTP wire structs (the FE controller.ts mirror)
│   │   ├── handler_controller.go   # Thin: controller-handler package doc + residual glue
│   │   ├── handler_agent.go        # Agent control channel: enroll, config, poll, report, rekey
│   │   ├── handler_deploy.go       # Operator deploy/fleet: stage, compile-preview, promote, nodes, revoke, audit
│   │   ├── handler_keystone.go     # Operator-credential + trust-list / manifest keystone handlers
│   │   ├── helpers_controller.go   # Shared controller-handler helpers (identity, decodeJSON, parseAfter)
│   │   ├── auth_controller.go      # Controller auth middleware (bearer token / operator session)
│   │   ├── handler_login.go        # Operator password login
│   │   ├── loginratelimit.go       # Login rate limiting
│   │   ├── handler_totp.go         # Operator TOTP 2FA
│   │   ├── handler_passkey.go      # Operator passkey (WebAuthn) login
│   │   ├── cookie_session.go       # httpOnly cookie session + CSRF
│   │   ├── handler_bootstrap.go    # Node bootstrap (self-extracting installer)
│   │   └── release_pins.go         # Agent self-update release-pins endpoint (SSRF-guarded)
│   ├── apierr/
│   │   └── apierr.go               # Structured API error codes + JSON error responses
│   ├── artifacts/
│   │   └── export.go               # Filesystem export: per-node dirs, checksums, manifests
│   ├── bundlesig/
│   │   └── bundlesig.go            # Ed25519 bundle signing / verification primitives
│   ├── compiler/
│   │   ├── compiler.go             # Multi-pass compilation orchestrator
│   │   ├── peers.go                # Peer derivation, transit IP/port allocation, key handling
│   │   └── roles.go                # Role semantics (capabilities, Babel announce policies)
│   ├── controller/                 # Controller domain: stores, staging, keystone engine
│   │   ├── store.go                # Store interface + shared types
│   │   ├── memstore.go             # In-memory Store impl
│   │   ├── filestore.go            # On-disk Store impl
│   │   ├── compile.go              # Staging/promote driver + allocation persistence
│   │   ├── keystone.go             # Trust-list/manifest-membership/epoch + operator-credential identity engine
│   │   ├── enrollment.go           # Enrollment ceremony (single-use token, PoP)
│   │   ├── operator.go             # Operator account model
│   │   ├── password.go             # Operator password hashing/verification
│   │   ├── totp.go                 # Operator TOTP secret + replay watermark
│   │   ├── login_challenge.go      # Passkey/login challenge state
│   │   ├── settings.go             # Controller settings (signing keys, fetch settings)
│   │   ├── tenantlock.go           # Per-tenant lock chokepoint
│   │   └── audit.go                # Audit hash-chain writer
│   ├── linkid/
│   │   └── linkid.go               # Stable per-link identity / naming
│   ├── model/                      # Shared leaf value types (no behavior)
│   │   ├── topology.go             # Core data model (Topology, Domain, Node, Edge, etc.)
│   │   └── artifact.go             # Artifact + InstallFetch install-bundle descriptors
│   ├── naming/
│   │   └── naming.go               # Canonical artifact naming, interface-name algorithm
│   ├── normalize/
│   │   └── pins.go                 # Pin normalization + HealCollidingPins self-heal
│   ├── regression/                 # Non-release adversarial regression suite (test-only)
│   │   └── *_test.go               # Keystone anti-rollback / served-vs-staged scenarios
│   ├── render/                     # ORCHESTRATOR: keygen + signing + install-bundle assembly
│   │   ├── render.go               # Render entry point (the render→renderer boundary)
│   │   ├── artifacts_json.go       # artifacts.json manifest assembly
│   │   └── fetchsettings_env.go    # Fetch-settings env materialization
│   ├── renderer/                   # LEAF EMITTERS: text/template producers (imported by render)
│   │   ├── babel.go                # Babel config renderer
│   │   ├── babel_presets.go        # Per-role Babel tuning presets
│   │   ├── deploy.go               # SSH deploy script renderer (bash + PowerShell)
│   │   ├── escape.go               # Shell/template escaping helpers
│   │   ├── fetch.go                # Fetch-settings rendering
│   │   ├── script.go               # Install/uninstall script renderer (per-peer + client)
│   │   ├── sysctl.go               # Sysctl config renderer (IP forwarding, rp_filter)
│   │   └── wireguard.go            # WireGuard config renderer (per-peer + client wg0)
│   ├── trustlist/                  # Operator trust-list / WebAuthn keystone primitives
│   │   ├── canonical.go            # Canonical trust-list serialization
│   │   ├── ed25519.go              # Ed25519 signature verification
│   │   ├── pins.go                 # Credential pin model
│   │   ├── types.go                # Trust-list data shapes
│   │   ├── verify.go               # Trust-list signature verification
│   │   └── webauthn.go             # WebAuthn attestation/assertion verification
│   └── validator/
│       ├── code.go                 # Validation error codes
│       ├── nat.go                  # NAT reachability validation
│       ├── schema.go               # Pass 1: structural/schema validation
│       └── semantic.go             # Pass 2: semantic/cross-reference validation
├── frontend/
│   ├── src/
│   │   ├── App.tsx                 # Root application component
│   │   ├── main.tsx                # React entry point
│   │   ├── i18n.ts                 # Internationalization (EN/ZH)
│   │   ├── index.css               # Global styles
│   │   ├── types/
│   │   │   ├── topology.ts         # TypeScript types (mirrors Go model)
│   │   │   └── controller.ts       # Controller wire types (mirrors api/wire_controller.go)
│   │   ├── stores/                 # Zustand stores (topology + controller, single source of truth)
│   │   └── components/             # React Flow canvas + panel/dashboard UI
│   ├── index.html
│   ├── package.json
│   ├── vite.config.ts
│   └── tsconfig*.json
├── examples/
│   ├── nat-hub/topology.json
│   ├── relay-topology/topology.json
│   └── simple-mesh/topology.json
├── scripts/
│   ├── deploy.sh                   # One-click YAOG deployment (bash)
│   └── deploy.ps1                  # One-click YAOG deployment (PowerShell)
├── docs/
│   ├── wiki.md                     # English documentation
│   ├── wiki-zh.md                  # Chinese documentation
│   ├── DEVELOPMENT_SPEC.md         # Redirect stub → docs/spec/
│   └── spec/                       # Development specification (this folder)
├── .github/workflows/              # Release + Docker CI
├── dev.sh                          # Dev helper (start/stop/restart/status/logs)
├── go.mod
├── go.sum
└── README.md
```
