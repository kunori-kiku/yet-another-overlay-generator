# Yet Another Overlay Generator (YAOG) — Development Specification

## 1. Project Overview

**YAOG** is a declarative control plane and code generator for overlay networks. It provides a web-based visual topology builder backed by a Go compilation engine that orchestrates **WireGuard** (Layer 3 cryptographic tunnels) and **Babel** (dynamic mesh routing) to produce ready-to-deploy configuration bundles.

### 1.1 Key Design Principle: Per-Peer WireGuard Interfaces

Unlike traditional WireGuard setups that multiplex all peers onto a single `wg0` interface, YAOG implements a **per-peer interface model**. Each peer-to-peer connection gets a dedicated WireGuard interface (e.g., `wg-alpha`, `wg-beta`), each with:

- An independently allocated listen port (base port + offset)
- A dedicated point-to-point transit IP pair (`10.10.0.0/24` pool)
- An IPv6 link-local address pair (for Babel neighbor discovery)

This enables Babel to treat each tunnel as an independent routing interface with per-link metrics, cost tuning, and fault isolation.

**Exception:** `client` role nodes use a single `wg0` interface (standard WireGuard client behavior) and do not run Babel.

### 1.2 Technology Stack

| Layer | Technology |
|---|---|
| Backend API | Go (stdlib `net/http`, no framework) |
| Frontend | React 18 + TypeScript + Vite |
| UI Canvas | React Flow (node/edge graph editor) |
| State Management | Zustand (with `persist` middleware → localStorage) |
| Internationalization | Custom `i18n.ts` (EN/ZH) |
| Crypto | `golang.zx2c4.com/wireguard/wgctrl` (WireGuard key generation) |
| CI/CD | GitHub Actions (multi-platform release workflow) |

### 1.3 Prerequisites

- **Go** 1.21+ (module declares 1.25.0)
- **Node.js** v18+ LTS
- Git

---

## 2. Repository Structure

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
│   └── DEVELOPMENT_SPEC.md       # This file
├── .github/workflows/
│   └── release.yml               # Multi-platform release CI
├── dev.sh                        # Dev helper (start/stop/restart/status/logs)
├── go.mod
├── go.sum
└── README.md
```

---

## 3. Data Model

### 3.1 Topology (Root Object)

```go
type Topology struct {
    Project       Project       `json:"project"`
    Domains       []Domain      `json:"domains"`
    Nodes         []Node        `json:"nodes"`
    Edges         []Edge        `json:"edges"`
    RoutePolicies []RoutePolicy `json:"route_policies,omitempty"`
}
```

### 3.2 Project

```go
type Project struct {
    ID          string `json:"id"`
    Name        string `json:"name"`
    Description string `json:"description,omitempty"`
    Version     string `json:"version,omitempty"`
}
```

### 3.3 Domain

A Domain represents an overlay address space.

| Field | Type | Description |
|---|---|---|
| `id` | string | Unique identifier |
| `name` | string | Human-readable name |
| `cidr` | string | Overlay CIDR (e.g., `10.0.0.0/24`) |
| `allocation_mode` | `"auto" \| "manual"` | IP allocation strategy |
| `routing_mode` | `"static" \| "babel" \| "none"` | Routing protocol |
| `reserved_ranges` | []string | CIDRs/IPs excluded from auto-allocation |
| `transit_cidr` | string | Point-to-point link address pool (default: `10.10.0.0/24`) |

### 3.4 Node

A Node represents a physical or virtual host in the overlay.

| Field | Type | Description |
|---|---|---|
| `id` | string | Unique identifier |
| `name` | string | Human-readable name (used in WG interface names) |
| `hostname` | string | OS hostname (optional) |
| `platform` | `"debian" \| "ubuntu"` | Target OS for install script |
| `role` | `"peer" \| "router" \| "relay" \| "gateway" \| "client"` | Node role |
| `domain_id` | string | Reference to owning Domain |
| `overlay_ip` | string | Assigned overlay IP (auto or manual) |
| `listen_port` | int | WireGuard base listen port (default: 51820) |
| `mtu` | int | WireGuard MTU (0 = system default, typically 1420) |
| `router_id` | string | Babel router-id in MAC-48 format (auto-generated from SHA-256 of node ID) |
| `capabilities` | NodeCapabilities | Network capabilities |
| `fixed_private_key` | bool | Whether to preserve WG private key across compiles |
| `wireguard_private_key` | string | WG private key (only with fixed_private_key) |
| `wireguard_public_key` | string | WG public key (derived) |
| `public_endpoints` | []PublicEndpoint | Public IP/port mappings for endpoint selection |
| `extra_prefixes` | []string | Additional CIDR prefixes to announce (gateway use) |
| `ssh_alias` | string | SSH config Host alias for auto-deploy |
| `ssh_host` | string | SSH host address |
| `ssh_port` | int | SSH port (default: 22) |
| `ssh_user` | string | SSH username (default: root) |
| `ssh_key_path` | string | SSH private key file path |

### 3.5 PublicEndpoint

```go
type PublicEndpoint struct {
    ID   string `json:"id"`
    Host string `json:"host"`
    Port int    `json:"port"`
    Note string `json:"note,omitempty"`
}
```

### 3.6 NodeCapabilities

| Field | Type | Description |
|---|---|---|
| `can_accept_inbound` | bool | Can receive unsolicited connections |
| `can_forward` | bool | Forwards packets between interfaces |
| `can_relay` | bool | Acts as a relay for other nodes |
| `has_public_ip` | bool | Has a publicly routable IP address |

### 3.7 Edge

An Edge represents a unidirectional connection intent ("from actively connects to to").

| Field | Type | Description |
|---|---|---|
| `id` | string | Unique identifier |
| `from_node_id` | string | Source node ID |
| `to_node_id` | string | Destination node ID |
| `type` | `"direct" \| "public-endpoint" \| "relay-path" \| "candidate"` | Connection type |
| `endpoint_host` | string | Target endpoint IP/hostname |
| `endpoint_port` | int | User-specified port (0 = use compiler-allocated port) |
| `compiled_port` | int | Read-only: actual port allocated by compiler |
| `priority` | int | Connection priority |
| `weight` | int | Connection weight |
| `transport` | `"udp" \| "tcp"` | Transport protocol |
| `is_enabled` | bool | Whether this edge is active |
| `notes` | string | Free-form notes |

### 3.8 RoutePolicy

```go
type RoutePolicy struct {
    ID              string `json:"id"`
    DomainID        string `json:"domain_id"`
    DestinationCIDR string `json:"destination_cidr"`
    NextHopNodeID   string `json:"next_hop_node_id,omitempty"`
    Metric          int    `json:"metric,omitempty"`
    Notes           string `json:"notes,omitempty"`
    SourceSelector  string `json:"source_selector,omitempty"`
    Action          string `json:"action,omitempty"`
    ApplyToNodeID   string `json:"apply_to_node_id,omitempty"`
}
```

---

## 4. Node Roles and Semantics

Each role has specific network semantics that determine capabilities, Babel behavior, and AllowedIPs strategy.

| Role | IP Forward | Accept Inbound | Runs Babel | Babel Announce | AllowedIPs Mode |
|---|---|---|---|---|---|
| **peer** | No | No | Yes | Self /32 only | point-to-point |
| **router** | Yes | If has public IP | Yes | Self + Domain CIDR + extra prefixes | point-to-point |
| **relay** | Yes | Yes (always) | Yes | Self + Domain CIDR + extra prefixes | relay-all (domain CIDR) |
| **gateway** | Yes | If has public IP | Yes | Self + Domain CIDR + extra + default route | gateway (domain + 0.0.0.0/0) |
| **client** | No | No | No | None | client (single wg0) |

### 4.1 Capability Inference

The compiler automatically infers certain capabilities from role:
- `router`: `can_forward = true`
- `relay`: `can_forward = true`, `can_relay = true`, `can_accept_inbound = true`
- `gateway`: `can_forward = true`
- `client`: all capabilities forced to `false`

---

## 5. Compilation Pipeline

The compiler operates as a multi-pass pipeline:

### Pass 1: Schema Validation (`validator.ValidateSchema`)

Structural checks on the raw topology JSON:
- Required fields present (project ID/name, domain CIDR, node role, etc.)
- CIDR format validity
- Enum value validity (roles, routing modes, transport protocols)
- Port range validity (0–65535)
- No self-loops on edges

### Pass 2: Semantic Validation (`validator.ValidateSemantic`)

Cross-reference and logical checks:
- Node domain_id references exist
- Edge from/to node references exist
- Overlay IPs within domain CIDRs
- No duplicate IDs (domains, nodes, edges)
- No IP address collisions
- Listen port conflicts (same hostname)
- Isolated node detection (warning)
- NAT reachability warnings (double-NAT, no public endpoint)
- Client edge constraints (exactly one outbound, must target router/relay/gateway, must have endpoint_host)

### Pass 3: IP Allocation (`allocator.AllocateIPs`)

- Clears overlay IPs that fall outside their domain's CIDR (handles domain CIDR changes)
- Sequentially allocates from domain CIDR, skipping network/broadcast, reserved ranges, and already-used IPs

### Pass 3b: Capability Inference (`InferCapabilitiesFromRole`)

- Applies role-based capability overrides to each node

### Pass 3c: Peer Derivation (`DerivePeers`) — Two-Phase Algorithm

**Phase 1 — Resource Pre-allocation:**
For each enabled, unique node pair:
1. Allocate a transit IP pair from `10.10.0.0/24` (sequential: `10.10.0.1/2`, `10.10.0.3/4`, ...)
2. Allocate an IPv6 link-local pair (`fe80::1/2`, `fe80::3/4`, ...)
3. Allocate listen ports for both ends: `base_port + per_node_offset++`
4. Store in `pairAllocation` map (keyed both directions)

**Phase 2 — PeerInfo Construction:**
For each enabled edge:
1. Look up the pre-allocated resources
2. Resolve endpoint: user-specified port takes priority, otherwise use the remote peer's allocated port
3. Compute PersistentKeepalive: 25s if the initiator cannot accept inbound OR there is no reverse edge
4. Generate WireGuard interface name: `wg-<remote_name>` (max 15 chars, Linux limit)
5. Set AllowedIPs to `0.0.0.0/0, ::/0` (per-peer model — routing handled by Babel)
6. Auto-generate the reverse peer (unless target is a client)

**Client handling:** Client nodes get a single `wg0` interface via `DeriveClientConfigs`, not per-peer interfaces.

### Pass 3d: CompiledPort Write-back

The compiler writes the allocated port back into `Edge.CompiledPort` so the frontend can display/auto-fill it.

### Output: CompileResult

```go
type CompileResult struct {
    Topology         *model.Topology
    PeerMap          map[string][]PeerInfo       // nodeID → per-peer interfaces
    WireGuardConfigs map[string]string            // "nodeID:ifaceName" → config content
    BabelConfigs     map[string]string            // nodeID → babeld.conf content
    SysctlConfigs    map[string]string            // nodeID → sysctl content
    InstallScripts   map[string]string            // nodeID → install.sh content
    DeployScripts    map[string]string            // "deploy-all.sh" / "deploy-all.ps1"
    ClientConfigs    map[string]*ClientPeerInfo   // nodeID → client wg0 info
    Manifest         CompileManifest
}
```

---

## 6. Rendered Artifacts

### 6.1 WireGuard Configuration (Per-Peer)

Each per-peer interface config contains:
- `[Interface]`: Private key, transit IP `/32`, `Table = off` (Babel manages routing), ListenPort, MTU
- `PostUp/PostDown`: IPv6 link-local address for Babel; optional client overlay IP route injection
- `[Peer]`: Single peer with public key, AllowedIPs (`0.0.0.0/0, ::/0`), optional Endpoint, optional PersistentKeepalive

### 6.2 WireGuard Configuration (Client wg0)

Single-interface config:
- `[Interface]`: Private key, overlay IP `/32`, ListenPort, MTU
- `[Peer]`: Router's public key, AllowedIPs = domain CIDR, Endpoint, PersistentKeepalive = 25

### 6.3 Babel Configuration

Per-node `babeld.conf`:
- `router-id`: Stable MAC-48 derived from SHA-256 of node ID
- `local-port 33123` (Babel control socket)
- Route redistribution rules based on role semantics (self /32, domain CIDR, extra prefixes, default route)
- Interface declarations: one per WireGuard tunnel, type `tunnel`, with configurable rxcost

### 6.4 Sysctl Configuration

`99-overlay.conf`:
- Forwarding nodes: `net.ipv4.ip_forward = 1`, `rp_filter = 0`
- Non-forwarding nodes: `rp_filter = 2` (loose mode for Babel compatibility)

### 6.5 Install Script

Generated per-node bash script with phases:
- **Uninstall mode** (`--uninstall` / `-u`): Complete teardown — stops all WG interfaces, disables Babel, removes configs, removes SNAT rules, removes dummy0, removes systemd services
- **Phase 0**: Cleanup previous installation (managed + legacy interfaces)
- **Phase 1**: Environment preparation — checksum verification, dependency installation, dummy0 interface creation with overlay IP, SNAT source address fix
- **Phase 2**: Configuration deployment — copies WG configs, Babel config, sysctl config
- **Phase 3**: Activation — applies sysctl, starts WG interfaces, configures babeld systemd override, shows status

**Source Address Fix (SNAT):** The per-peer WireGuard model uses transit IPs (`10.10.0.0/24`) on tunnel interfaces. Without a fix, outgoing packets to overlay destinations use the transit IP as the source instead of the overlay IP, causing `ping <overlay_ip>` to silently fail. The install script adds an SNAT rule (nftables preferred, iptables fallback) that rewrites transit source IPs to the node's overlay IP on all `wg-*` interfaces. A persistent `overlay-snat.service` systemd unit ensures the rule survives reboots.

### 6.6 Deploy Scripts

`deploy-all.sh` (bash) and `deploy-all.ps1` (PowerShell):
- Iterates all nodes with SSH details
- **Deploy mode**: Uploads self-extracting installer via SCP, executes remotely via `sudo bash`
- **Uninstall mode** (`--uninstall` / `-Uninstall`): SSHs into each node and runs teardown commands directly — stops all named WireGuard interfaces, removes configs, stops Babel, removes dummy0, reloads systemd. No installer upload needed.
- Optional `--clean` flag to remove all existing WG interfaces before deploying
- Per-node error handling (failures don't abort the run)

### 6.7 Self-Extracting Installer

The export endpoint creates a ZIP containing per-node `.install.sh` files that are self-extracting:
- Base64-encoded tar.gz payload appended after `__PAYLOAD_BELOW__` marker
- Extracts to temp dir, runs the inner `install.sh`, cleans up

### 6.8 Export Directory Structure

```
<output>/
├── deploy-all.sh
├── deploy-all.ps1
├── <node-name>/
│   ├── wireguard/
│   │   ├── wg-peer1.conf
│   │   ├── wg-peer2.conf
│   │   └── ...
│   ├── babel/
│   │   └── babeld.conf
│   ├── sysctl/
│   │   └── 99-overlay.conf
│   ├── install.sh
│   ├── checksums.sha256
│   ├── manifest.json
│   └── README.txt
└── ...
```

---

## 7. HTTP API

Base URL: `http://localhost:8080`

| Method | Endpoint | Description |
|---|---|---|
| `GET` | `/api/health` | Health check → `{ "status": "ok", "timestamp": "..." }` |
| `POST` | `/api/validate` | Validate topology → `{ "valid": bool, "errors": [...], "warnings": [...] }` |
| `POST` | `/api/compile` | Compile topology → full `CompileResponse` with all configs |
| `POST` | `/api/export` | Export artifact ZIP (binary download) |
| `POST` | `/api/deploy-script?format=sh\|ps1` | Download deploy script |

All POST endpoints accept `Content-Type: application/json` with a `Topology` object as body.

CORS is enabled for all origins (`Access-Control-Allow-Origin: *`).

---

## 8. Frontend Architecture

### 8.1 State Management

The Zustand store (`topologyStore.ts`) is the single source of truth:
- **Persisted** (localStorage): `project`, `domains`, `nodes`, `edges`, `language`
- **Volatile**: `compileResult`, `validateResult`, `isCompiling`, `isValidating`, `error`, `viewMode`, selection state, `history`

### 8.2 Component Hierarchy

```
App
└── AppLayout
    ├── TopBar            (project name, compile/validate/export buttons, language toggle)
    ├── LeftPanel          (domain list, node list, CRUD forms)
    ├── TopologyCanvas     (React Flow graph editor)
    │   ├── CustomNode     (per-node visual representation with per-peer interface handles)
    │   └── CustomEdge     (connection line with endpoint/port labels)
    ├── RightPanel         (selected item properties editor)
    └── BottomBar          (validation/compile results, error display)
```

### 8.3 TypeScript Types

Frontend types in `types/topology.ts` mirror the Go backend model exactly. Key response types:
- `ValidateResponse`: `{ valid, errors?, warnings? }`
- `CompileResponse`: `{ topology, wireguard_configs, babel_configs, sysctl_configs, install_scripts, deploy_scripts, manifest }`
- `CompileHistoryEntry`: Stores up to 5 recent compilation snapshots

### 8.4 API Integration

All API calls are made from the Zustand store actions:
- `validate()` → `POST /api/validate`
- `compile()` → `POST /api/compile` (updates topology state from response, saves to history)
- `exportArtifacts()` → `POST /api/export` (downloads ZIP via blob URL)
- `downloadDeployScript(format)` → `POST /api/deploy-script?format=sh|ps1`

---

## 9. CI/CD and Release

### 9.1 GitHub Actions Workflow (`.github/workflows/release.yml`)

Triggered on tags matching `v*`.

**Jobs:**
1. **build-frontend**: Builds React frontend (`npm ci && npm run build`), uploads `frontend/dist` as artifact
2. **build-bundles**: Matrix build across 7 platform targets:
   - Linux: `amd64`, `arm64`, `386`, `armv7`
   - Windows: `amd64`, `arm64`, `386`
   - Builds two Go binaries: `yaog-server` and `yaog-compiler`
   - Assembles bundle: `bin/` + `frontend/`
   - Archives: `.tar.gz` (Linux) or `.zip` (Windows)
3. **release**: Creates GitHub Release with all bundle archives

### 9.2 Deployment Scripts

`scripts/deploy.sh` and `scripts/deploy.ps1` pull a prebuilt bundle from GitHub Releases and set up local startup scripts:
- Auto-detects platform and architecture
- Defaults to latest release if no `--tag` specified
- Generates `start.sh`/`stop.sh` (or `.ps1` equivalents)

---

## 10. Example Topologies

Located in `examples/`:

| Example | Description |
|---|---|
| `simple-mesh/topology.json` | Basic full-mesh between a few nodes |
| `nat-hub/topology.json` | Hub-and-spoke with NAT traversal |
| `relay-topology/topology.json` | Topology using relay nodes |

---

## 11. Development Workflow

### 11.1 Quick Start

```bash
./dev.sh start      # Starts backend (:8080) + frontend (:5173)
./dev.sh stop       # Stops both
./dev.sh restart    # Restart
./dev.sh status     # Check running status
./dev.sh logs       # Tail log files
```

### 11.2 Manual Start

```bash
# Backend
go run ./cmd/server/main.go

# Frontend (separate terminal)
cd frontend
npm install --legacy-peer-deps
npm run dev
```

### 11.3 CLI Compiler

```bash
go run ./cmd/compiler/main.go -input examples/simple-mesh/topology.json -output output/
```

### 11.4 Running Tests

```bash
go test ./...
```

---

## 12. Key Algorithms

### 12.1 IP Allocation

Sequential allocation from domain CIDR:
1. Parse CIDR to get network base and host bits
2. Start from host address 1 (skip network address)
3. End before last address (skip broadcast)
4. Skip reserved ranges and already-used IPs
5. Return first available

### 12.2 WireGuard Interface Naming

```
wg-<lowercase_remote_name>  (max 15 chars, Linux kernel limit)
```
Non-alphanumeric characters (except `-`) are replaced with `-`.

### 12.3 Router ID Generation

Stable Babel router-id from SHA-256 of node ID:
```
SHA-256(nodeID) → take first 6 bytes → set locally-administered bit, clear multicast bit → format as MAC-48
```

### 12.4 Transit IP Allocation

Sequential from `10.10.0.0/24`:
```
Pair 0: 10.10.0.1, 10.10.0.2
Pair 1: 10.10.0.3, 10.10.0.4
Pair N: 10.10.0.(2N+1), 10.10.0.(2N+2)
```

IPv6 link-local follows the same pattern: `fe80::1/2`, `fe80::3/4`, ...

### 12.5 PersistentKeepalive Logic

Set to `25` (seconds) when:
- The initiating node (`from`) cannot accept inbound connections, OR
- There is no reverse edge (i.e., the remote node has no edge pointing back)

This ensures NAT-traversal keepalive for nodes behind NAT.

### 12.6 Checksum

SHA-256 of the string representation of the compiled topology, truncated to 16 hex characters. Written to manifest and verified by install scripts.

---

## 13. Security Considerations

- **Key Management**: WireGuard private keys are generated fresh per compilation unless `fixed_private_key` is set. Non-fixed keys are NOT persisted to the topology JSON (cleared after compile).
- **Checksum Verification**: Install scripts verify `checksums.sha256` (SHA-256) before deploying configs.
- **File Permissions**: WireGuard configs are written with `0600` permissions.
- **Privilege Escalation**: Install scripts require root and verify with `id -u` check.
- **Transport**: The API server has no built-in TLS — should be reverse-proxied in production.

---

## 14. Uninstall Support

Both the per-peer install script and the client install script support a `--uninstall` (or `-u`) flag:

```bash
sudo bash install.sh --uninstall
```

The uninstall operation:
1. Stops and disables all managed WireGuard interfaces
2. Stops and disables ALL remaining WireGuard interfaces (catches any leftovers)
3. Removes all WireGuard config files from `/etc/wireguard/`
4. (Per-peer nodes) Stops and disables Babel, removes Babel configs and systemd overrides
5. Removes sysctl overlay configuration and re-applies system defaults
6. (Per-peer nodes) Removes the `dummy0` overlay interface and its `overlay-dummy.service`
7. Reloads systemd daemon

---

## 15. Glossary

| Term | Definition |
|---|---|
| **Overlay IP** | The virtual IP assigned to a node within the overlay network |
| **Transit IP** | Point-to-point IP used on a per-peer WireGuard interface (from `10.10.0.0/24`) |
| **Link-local** | IPv6 `fe80::/10` address used by Babel for neighbor discovery |
| **dummy0** | A Linux dummy interface used to host the stable overlay IP (independent of tunnels) |
| **Table = off** | WireGuard option that disables automatic routing table entries (Babel manages routes instead) |
| **Per-peer interface** | Architecture where each WireGuard peer gets a dedicated network interface |
| **Self-extracting installer** | A bash script with an embedded base64 tar.gz payload |
