# Repository Structure

```
yet-another-overlay-generator/
├── cmd/
│   ├── compiler/main.go          # CLI compiler entry point
│   └── server/main.go            # HTTP API server entry point
├── internal/
│   ├── allocator/
│   │   ├── ip.go                 # Overlay IP auto-allocation from domain CIDRs
│   │   └── ip_test.go
│   ├── api/
│   │   ├── handler.go            # HTTP handlers (health, validate, compile, export, deploy-script)
│   │   ├── handler_test.go
│   │   └── server.go             # HTTP server setup, routing, CORS
│   ├── artifacts/
│   │   ├── export.go             # Filesystem export: per-node dirs, checksums, manifests
│   │   └── export_test.go
│   ├── compiler/
│   │   ├── compiler.go           # Multi-pass compilation orchestrator
│   │   ├── compiler_test.go
│   │   ├── peers.go              # Peer derivation, transit IP/port allocation, key handling
│   │   ├── roles.go              # Role semantics (capabilities, Babel announce policies)
│   │   └── roles_test.go
│   ├── model/
│   │   ├── topology.go           # Core data model (Topology, Domain, Node, Edge, etc.)
│   │   └── topology_test.go
│   ├── renderer/
│   │   ├── babel.go              # Babel config renderer
│   │   ├── babel_presets.go      # Per-role Babel tuning presets
│   │   ├── babel_test.go
│   │   ├── deploy.go             # SSH deploy script renderer (bash + PowerShell)
│   │   ├── script.go             # Install/uninstall script renderer (per-peer + client)
│   │   ├── script_test.go
│   │   ├── sysctl.go             # Sysctl config renderer (IP forwarding, rp_filter)
│   │   ├── wireguard.go          # WireGuard config renderer (per-peer + client wg0)
│   │   └── wireguard_test.go
│   └── validator/
│       ├── nat.go                # NAT reachability validation
│       ├── schema.go             # Pass 1: structural/schema validation
│       ├── semantic.go           # Pass 2: semantic/cross-reference validation
│       └── validator_test.go
├── frontend/
│   ├── src/
│   │   ├── App.tsx               # Root application component
│   │   ├── main.tsx              # React entry point
│   │   ├── i18n.ts               # Internationalization (EN/ZH)
│   │   ├── index.css             # Global styles
│   │   ├── types/
│   │   │   └── topology.ts       # TypeScript type definitions (mirrors Go model)
│   │   ├── stores/
│   │   │   └── topologyStore.ts  # Zustand store (state, CRUD, API calls)
│   │   └── components/
│   │       ├── audit/
│   │       │   └── AuditView.tsx
│   │       ├── canvas/
│   │       │   ├── CustomEdge.tsx
│   │       │   ├── CustomNode.tsx
│   │       │   └── TopologyCanvas.tsx
│   │       ├── domains/
│   │       │   ├── DomainForm.tsx
│   │       │   └── DomainList.tsx
│   │       ├── layout/
│   │       │   ├── AppLayout.tsx
│   │       │   ├── BottomBar.tsx
│   │       │   ├── LeftPanel.tsx
│   │       │   ├── RightPanel.tsx
│   │       │   └── TopBar.tsx
│   │       └── nodes/
│   │           ├── NodeForm.tsx
│   │           └── NodeList.tsx
│   ├── index.html
│   ├── package.json
│   ├── vite.config.ts
│   └── tsconfig*.json
├── examples/
│   ├── nat-hub/topology.json
│   ├── relay-topology/topology.json
│   └── simple-mesh/topology.json
├── scripts/
│   ├── deploy.sh                 # One-click YAOG deployment (bash)
│   └── deploy.ps1                # One-click YAOG deployment (PowerShell)
├── docs/
│   ├── wiki.md                   # English documentation
│   ├── wiki-zh.md                # Chinese documentation
│   ├── DEVELOPMENT_SPEC.md       # Redirect stub → docs/spec/
│   └── spec/                     # Development specification (this folder)
├── .github/workflows/
│   └── release.yml               # Multi-platform release CI
├── dev.sh                        # Dev helper (start/stop/restart/status/logs)
├── go.mod
├── go.sum
└── README.md
```
