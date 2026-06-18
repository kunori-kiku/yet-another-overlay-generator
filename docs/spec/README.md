# YAOG Development Specification

The specification is organized as a nested folder of logical components. Each component owns its
data shapes, behavior, and the key algorithms that implement it.

> Provenance: restructured from the former monolithic `docs/DEVELOPMENT_SPEC.md` (v1.2.0).
> That file remains as a redirect stub.

## Layout

```
docs/spec/
├── overview/
│   ├── project-overview.md        # What YAOG is, tech stack, prerequisites
│   ├── design-principles.md       # Per-peer WireGuard interface model
│   └── repository-structure.md    # Source tree map
├── data-model/
│   ├── topology.md                # Topology root object, Project
│   ├── domain.md                  # Domain (overlay address space)
│   ├── node.md                    # Node, PublicEndpoint, NodeCapabilities
│   ├── edge.md                    # Edge (connection intent, port semantics)
│   └── route-policy.md            # RoutePolicy
├── roles/
│   └── roles.md                   # Role semantics table, capability inference
├── compiler/
│   ├── pipeline.md                # Multi-pass pipeline overview, CompileResult
│   ├── validation.md              # Pass 1 (schema) + Pass 2 (semantic), coverage table
│   ├── ip-allocation.md           # Pass 3: overlay IP allocation algorithm
│   ├── peer-derivation.md         # Pass 3c: two-phase peer derivation, transit IPs,
│   │                              #   ports, endpoint resolution contract, keepalive
│   ├── routing-modes.md           # Routing-mode contract: babel default, route
│   │                              #   installation prerequisites
│   └── allocation-stability.md    # Allocation Stability & Growth: invariants I1–I10,
│                                  #   sticky-pin mechanism, migration
├── artifacts/
│   ├── wireguard.md               # Per-peer + client wg0 config rendering
│   ├── babel.md                   # babeld.conf rendering, router-id generation
│   ├── sysctl.md                  # Forwarding / rp_filter settings
│   ├── install-script.md          # Install script phases, SNAT fix, uninstall support
│   ├── deploy-scripts.md          # SSH deploy scripts, self-extracting installer
│   ├── naming.md                  # Canonical artifact naming, uniqueness invariants,
│   │                              #   interface-name algorithm
│   └── export-bundle.md           # Export directory structure, checksums
├── api/
│   ├── http-api.md                # HTTP API endpoints and contracts
│   └── wire-contract.md           # FE↔BE field parity table, round-trip rules
├── frontend/
│   └── architecture.md            # State management, components, types, API integration
├── operations/
│   ├── development-workflow.md    # dev.sh, manual start, CLI compiler, tests
│   ├── ci-cd.md                   # Release workflow, deployment scripts
│   └── examples.md                # Example topologies
├── security/
│   └── security.md                # Security considerations
├── controller/
│   ├── signing.md                 # Canonical bundle serialization + Ed25519 bundle signing
│   ├── key-custody.md             # Zero-knowledge key custody (AgentHeld split-render)
│   ├── agent.md                   # Node agent (keygen→pull→verify→apply via install.sh splice)
│   ├── persistence.md             # Controller Store interface, MemStore/FileStore, tenant
│   │                              #   chokepoint, generation/stage-promote, audit hash chain
│   ├── enrollment.md              # Enrollment ceremony: single-use token, per-node bearer
│   │                              #   token (the mTLS CSR/DevCA model is retracted —
│   │                              #   see controller-api.md's 2026-06-08 plain-HTTP+tokens note)
│   ├── deploy.md                  # Compile/stage/promote model, render-what's-ready
│   │                              #   subgraph filter, frozen-pipeline reuse via temp-dir
│   └── controller-api.md          # Controller HTTP routes, plain HTTP + per-node bearer-token
│                                  #   auth (TLS delegated to a reverse proxy; the TLS 1.3 + mTLS
│                                  #   chokepoint model is retracted per its 2026-06-08 note),
│                                  #   env-gated controller mode
└── glossary.md                    # Terminology
```

## Suggested reading order

1. `overview/` — what the system is and the one design decision everything follows from
2. `data-model/` — the shapes the compiler consumes
3. `roles/` — node role semantics
4. `compiler/` — the compilation pipeline (the core of the system)
5. `artifacts/` — what gets rendered and deployed
6. `api/`, `frontend/` — the interfaces around the compiler
7. `operations/`, `security/`, `glossary.md` — supporting material
