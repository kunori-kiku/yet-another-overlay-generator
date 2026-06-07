# Yet Another Overlay Generator — Wiki

> Also available in: [中文](wiki-zh.md)

## 1. Overview

Yet Another Overlay Generator is a web-based interactive network design and configuration generation system. Users define nodes, network domains, and connectivity through a graphical topology interface. The system automatically allocates addresses and generates WireGuard + Babel configuration files along with one-click install scripts.

### Design Philosophy

The system follows a **Design → Compile → Deploy** three-layer architecture:

```text
[Web Frontend / CLI]
        │  Topology JSON
        ▼
[Compiler]
  ├─ Schema Validation
  ├─ Semantic Validation
  ├─ IP Allocator
  ├─ Peer Derivation
  └─ Config Renderers
        │  ├─ WireGuard configs
        │  ├─ Babel configs
        │  ├─ sysctl kernel params
        │  ├─ Install scripts
        │  └─ Deploy scripts
        ▼
[Artifact Exporter]
        │  Per-node deployment bundles
        ▼
[Target Hosts]
        └─ Run install.sh → network goes live
```

Core principles:
- **Topology as Code**: The JSON topology is the single source of truth; all configs are deterministically derived.
- **Offline Compilation**: Keys and configs are generated on a local trusted host, no online control plane needed.
- **Idempotent Deployment**: Install scripts can be safely re-run.

---

## 2. Core Concepts

### 2.1 Domain

A Domain is an overlay address space defining the allocatable IP range.

| Field | Description |
|-------|-------------|
| Name | Display name and logical identifier |
| CIDR | Address range, e.g. `10.11.0.0/24` |
| Allocation Mode | `auto` (automatic) / `manual` (user-specified) |
| Routing Mode | `babel` (dynamic routing) / `static` / `none` |

### 2.2 Node and Roles

A Node represents a machine (cloud VM, bare-metal server, container host).

**Basic fields:**
- Name, Hostname (optional), Platform (`debian` / `ubuntu`)
- Domain membership, Overlay IP (optional manual override)
- WireGuard base listen port (default 51820), MTU (optional)

**Roles and capabilities:**

| Role | Forwarding | Relay | Babel Announces | Typical Use |
|------|-----------|-------|-----------------|-------------|
| `peer` | No | No | Own IP only | End-user node |
| `router` | Yes | No | Own IP + Domain CIDR + extra prefixes (when set) | Backbone forwarding node |
| `relay` | Yes | Yes | Own IP + Domain CIDR + extra prefixes (when set), cost 96 | NAT traversal relay |
| `gateway` | Yes | No | Own IP + Domain CIDR + extra prefixes + default route | Bridge to external networks |
| `client` | No | No | None (no Babel) | Lightweight endpoint (phone, laptop) |

> **Extra prefixes:** `router` and `relay` announce their `extra_prefixes` whenever the field is
> non-empty, not only `gateway` (`internal/compiler/roles.go`). Extra prefixes and the gateway
> default route are announced via the kernel-route mechanism (`redistribute ip <prefix> allow`,
> matching a real connected/WAN kernel route) rather than `redistribute local`; see
> [spec/roles/roles.md](spec/roles/roles.md) and the audit dossier (D40/D41) for the rationale.

> **Client role:** Client is the lightest role, intended for devices that don't participate in dynamic routing. A client uses a single `wg0` interface to connect to one router/relay/gateway node. It does not run Babel, does not use `dummy0`, and does not use the per-peer interface model. Client reachability is achieved through kernel route injection on the router side (`PostUp = ip route add <client_ip>/32 dev %i`) + Babel redistribution.

**Capability fields:**
- Publicly Reachable: node is accessible from the public internet
- Can Accept Inbound: external traffic can reach this node
- Can Forward: can forward other nodes' traffic
- Can Relay: operates as a relay node

**Multiple Public Endpoints:** Nodes support multiple `Host:Port` public endpoint mappings (domains supported), for multi-exit, multi-ISP, and NAT multi-mapping scenarios.

**SSH Connection Config (Auto-Deploy):** Nodes can optionally store SSH connection details for automated remote deployment:

| Field | Description |
|-------|-------------|
| SSH Alias | Host alias from `~/.ssh/config`; if set, overrides manual fields below |
| SSH Host | SSH target IP or hostname |
| SSH Port | SSH port (default 22) |
| SSH User | SSH login username (default root) |
| SSH Key Path | Path to SSH private key file |

> Note: Password authentication is not supported. Use key-based auth. SSH details are collapsed by default in the node properties panel.

### 2.3 Edge (Directed Connection)

A directed edge `A → B` means: **A actively connects to B**.

| Field | Description |
|-------|-------------|
| Type | `direct` / `public-endpoint` / `relay-path` / `candidate` |
| Endpoint IP | Target public IP or hostname; pick from target node's public endpoints or enter manually |
| Endpoint Port | Operator NAT/port-forward override: `0` (default) = compiler auto-allocates; nonzero = the external port the from-side dials verbatim |
| Compiled Port | Read-only: the port the from-side actually dials, displayed under the port field after compilation |
| Transport | `udp` = plain WireGuard. `tcp` = the link is wrapped by [mimic](https://github.com/hack3ric/mimic) (eBPF UDP→fake-TCP) for networks that throttle or block UDP. Both ends must be Linux with eBPF; MTU is auto-lowered 12 bytes; the installer provisions mimic from the distro. **Not** a censorship/DPI-circumvention feature. See `docs/spec/artifacts/mimic.md` |
| Priority / Weight | Path preference weights |
| Is Enabled | Whether this edge participates in compilation |

> **Port ownership:** The compiler is the sole authority for WireGuard listen ports. `endpoint_port`
> is *not* a copy of the allocated port and there is no "Auto-fill" button — leave it at `0` and the
> compiler dials the remote interface's auto-allocated listen port, writing the result to the
> read-only `compiled_port`. Set `endpoint_port` to a nonzero value only as an explicit NAT /
> port-forward override (e.g. a router DNATs external `:51900` → the node's internal `:51820`); the
> override is honored verbatim and preserved across recompiles. The full authority contract is in
> [spec/data-model/edge.md](spec/data-model/edge.md).

### 2.4 Two-Layer Address Separation

The system uses two independent IP address pools to avoid link addresses conflicting with node identity addresses:

| | Overlay IP (Business Address) | Transit IP (Link Address) |
|---|---|---|
| Pool | Defined per Domain CIDR (e.g. `10.11.0.0/24`) | Per-domain `transit_cidr` (default `10.10.0.0/24`) |
| Assigned to | `dummy0` interface | Each per-peer WireGuard interface |
| Purpose | Stable node identity (DNS, apps, monitoring) | Tunnel point-to-point addressing |
| Babel announces | Yes, via `redistribute local` | No, internal use only |
| Stability | Does not change with topology | Changes with link additions/removals |

Each link also gets a pair of IPv6 link-local addresses (`fe80::X`) for Babel neighbor discovery.

### 2.5 Per-Peer WireGuard Interface Model

**Why not a single wg0 with multiple Peers?**

The traditional single-interface multi-peer WireGuard model is incompatible with Babel dynamic routing:
- Babel needs **one independent interface per neighbor** to track link quality independently
- A single wg0 with multiple peers looks like one broadcast domain to Babel
- Multiple peers' `AllowedIPs` can produce address conflicts

**Per-peer design:** Each peer connection uses a dedicated WireGuard interface:

```
Node alpha:
  wg-node-beta   ← tunnel to beta (port 51820)
  wg-node-gamma  ← tunnel to gamma (port 51821)
  dummy0         ← stable overlay address
```

Each interface features:
- Independent listen port (base port + incrementing offset)
- Independent transit IP (/32 point-to-point) + IPv6 link-local
- Only one `[Peer]` section
- `Table = off` (prevents wg-quick from adding routes; Babel manages routing)
- `AllowedIPs = 0.0.0.0/0, ::/0` (safe with one peer per interface)

**Interface naming:** `wg-<peer-name>`, lowercase, with every character outside `[a-z0-9-]`
(including `_`) replaced by `-`. The Linux kernel caps interface names at 15 characters, so the
algorithm has two paths: if `wg-<clean-name>` is at most 15 characters it is used verbatim;
otherwise the long path returns `wg-` + the first 8 cleaned characters + the first 4 hex characters
of `sha256(peer-name)` (3 + 8 + 4 = 15 chars). The hash suffix prevents two distinct long names
that share a prefix from truncating to the same interface. The backend is the sole authority for
this name (`internal/naming`); the frontend must consume the compiled name, never re-derive it. Full
algorithm in [spec/artifacts/naming.md](spec/artifacts/naming.md).

---

## 3. Usage Guide

### 3.1 Topology Editing Workflow

Standard workflow:

1. **Create Domain** — Define address space (CIDR), allocation mode, routing mode
2. **Create Nodes** — Set name, platform, role, assign to domain
3. **Add Public Endpoints** (optional) — Configure Host:Port for nodes with public ingress
4. **Configure SSH** (optional) — Add SSH connection details for auto-deploy (collapsed by default)
5. **Draw Edges** — Drag from source to target node on canvas; set the endpoint IP (leave the port at `0` unless you need a NAT override)
6. **Validate** — Check topology completeness and semantic errors
7. **Compile** — Generate all configuration files
8. **Export** — Download per-node deployment bundles

**UI Layout:**
- Center canvas: Visualize nodes and directed edges with color-coded per-peer handles
- Left panel: Create and reorder domains, nodes (drag-to-reorder)
- Right panel: Edit properties of selected domain/node/edge
- Bottom panel: Validation results and diagnostics

### 3.2 Parameter Reference

#### Domain Parameters

| Parameter | Required | Description |
|-----------|----------|-------------|
| Name | Yes | Display name and logical identifier |
| CIDR | Yes | Overlay address pool, e.g. `10.11.0.0/24` |
| Allocation Mode | Yes | `auto` / `manual` |
| Routing Mode | Yes | `babel` / `static` / `none` |

#### Node Parameters

| Parameter | Required | Description |
|-----------|----------|-------------|
| Name | Yes | Canvas and list display name |
| Hostname | No | Actual hostname or domain label |
| Platform | Yes | `debian` / `ubuntu` |
| Domain | Yes | Parent domain |
| Role | Yes | `peer` / `router` / `relay` / `gateway` / `client` |
| Overlay IP | No | Manual override; otherwise auto-assigned |
| Listen Port | No | WireGuard base listen port, default 51820 |
| MTU | No | WireGuard interface MTU, 0 = system default |
| Router ID | No | Babel router-id (MAC-48); blank = auto-generated |

**Capability fields:**

| Parameter | Description |
|-----------|-------------|
| Publicly Reachable | Node is accessible from the public internet |
| Can Accept Inbound | External traffic can reach this node |
| Can Forward | Can forward other nodes' traffic |
| Can Relay | Operates as a relay node |

**Public Endpoints (per entry):**

| Parameter | Description |
|-----------|-------------|
| Host | Public IP or domain name |
| Port | Public port |
| Note | Remark (e.g. "ISP-A exit", "Tokyo ingress") |

**SSH Connection (collapsible):**

| Parameter | Description |
|-----------|-------------|
| SSH Alias | ssh_config Host alias (overrides manual fields if set) |
| SSH Host | Target IP or hostname |
| SSH Port | Port (default 22) |
| SSH User | Username (default root) |
| SSH Key Path | Path to private key file |

#### Edge Parameters

| Parameter | Required | Description |
|-----------|----------|-------------|
| Type | Yes | `direct` / `public-endpoint` / `relay-path` / `candidate` |
| Endpoint IP | No | Target IP or domain (pick from target's public IPs or manual) |
| Endpoint Port | No | Explicit NAT/port-forward override: `0` (default) = compiler auto-allocates; nonzero = dialed verbatim |
| Compiled Port | — | Read-only: actual dialed port (filled post-compile) |
| Transport | No | `udp` / `tcp` metadata |
| Priority | No | Path priority |
| Weight | No | Path weight |
| Is Enabled | Yes | Whether to include in compilation |

### 3.3 Validation, Compilation, and Export

**Validation** checks two categories:
- **Schema validation**: Required fields, type correctness, reference validity (e.g. node's domain_id points to an existing domain)
- **Semantic validation**: IP conflicts, isolated nodes, illegal CIDRs

**Compilation** deterministically generates from the topology JSON:
- Per-peer WireGuard config files
- Per-node Babel routing config
- Per-node sysctl kernel parameters
- Per-node install scripts
- **Auto-deploy scripts** (`deploy-all.sh` and `deploy-all.ps1`)

**Export** packages per-node deployment directories containing all config files, install.sh, manifest.json, and checksums.sha256.

---

## 4. Compiler Internals

### 4.1 Compilation Pipeline

The compiler (`internal/compiler/compiler.go`) processes the topology in 5 passes:

**Pass 1: Schema Validation** — Validates JSON structure: required fields, types, reference validity.

**Pass 2: Semantic Validation** — Checks logical consistency: IP conflicts, isolated nodes, illegal edge references, CIDR validity.

**Pass 3: IP Allocation + Peer Derivation**
- **IP Allocator** (`internal/allocator/ip.go`): Sequentially assigns IPs from the Domain CIDR pool for nodes without manual IPs, skipping network/broadcast/reserved addresses
- **Capability Derivation** (`internal/compiler/roles.go`): Infers capability fields from role (e.g. `router` → `can_forward=true`)
- **Peer Derivation** (`internal/compiler/peers.go`): Processes edges to generate PeerInfo for each node pair (see 4.2)

**Pass 4: Config Rendering** — Four independent renderers plus deploy scripts:

| Renderer | Output | Source |
|----------|--------|--------|
| WireGuard | One `.conf` per peer | `internal/renderer/wireguard.go` |
| Babel | `babeld.conf` per node | `internal/renderer/babel.go` |
| sysctl | `99-overlay.conf` | `internal/renderer/sysctl.go` |
| Install script | `install.sh` | `internal/renderer/script.go` |
| Deploy scripts | `deploy-all.sh` + `.ps1` | `internal/renderer/deploy.go` |

**Pass 5: Artifact Export** (`internal/artifacts/export.go`) — Organizes into per-node directories.

### 4.2 Peer Derivation Logic

The peer derivation engine is the most complex part of the compiler, converting topology edges into concrete WireGuard peer configurations.

**Input → Output:**
- Input: Topology (nodes + edges) + key pairs
- Output: `map[nodeID][]PeerInfo` — per-node peer interface config list

**Two-pass algorithm:**

**Pass 1 — Pre-allocate resources:** For each unique node pair, allocates listen ports (using incremental offset per node), transit IPs, and link-local IPs. Stores in a `pairAllocation` struct keyed bidirectionally.

**Pass 2 — Build PeerInfo:** Iterates edges again, looks up the pre-allocated resources, and builds the PeerInfo using the correct allocated port (not the static base port).

**Endpoint resolution:**
- **Forward peer**: Uses edge's `endpoint_host` + allocated target port
- **Reverse peer**: Looks for a reverse edge (`B→A`); if found, uses its endpoint host + allocated source port; if not, reverse peer has no endpoint (relies on forward side to initiate)

**PersistentKeepalive:**

| Condition | Keepalive |
|-----------|-----------|
| Node can accept inbound AND reverse edge exists | 0 (disabled) |
| Node behind NAT (can't accept inbound) | 25 seconds |
| No reverse edge (unidirectional) | 25 seconds |

**Transit IP allocation:** Each node pair gets a pair from its domain's `transit_cidr`
(default `10.10.0.0/24`):
- Link 0: `10.10.0.1` ↔ `10.10.0.2`
- Link N: `10.10.0.(2N+1)` ↔ `10.10.0.(2N+2)`

**Listen port allocation:** Each node starts from `listen_port` (default 51820), gap-filling upward
for each additional peer interface.

**Pinned (sticky) allocations:** Once a link's listen ports, transit IP pair, and IPv6 link-locals
are chosen, the compiler writes them back onto the edge as `pinned_*` fields and reuses them
verbatim on the next compile. This keeps existing servers byte-stable when you add nodes — adding a
new node leaves unrelated nodes' bundles unchanged. WireGuard keys persist the same way (see 4.4).
The full reserve-pins-first-then-gap-fill contract and invariants are in
[spec/compiler/allocation-stability.md](spec/compiler/allocation-stability.md).

### 4.3 Babel Routing Integration

Babel is the dynamic routing daemon that makes multi-hop overlay networks work.

**When does Babel run?** When the node's Domain has `routing_mode` set to `"babel"`.

**Router-ID generation:**
1. Compute `SHA-256(node_id)`
2. Take first 6 bytes as MAC-48 address
3. Set locally administered bit (`| 0x02`), clear multicast bit (`& 0xFE`)
4. Ensures stability (same node = same ID) and uniqueness (SHA-256 distribution)
5. Users can manually specify `router_id` to override

**Interface declaration:** Each per-peer WireGuard interface is declared as a Babel tunnel interface:
```
interface wg-node-beta type tunnel hello-interval 4 update-interval 16
```

The `hello-interval 4` (hello every 4s) and `update-interval 16` (full route update every 16s)
defaults are now supplied by per-role Babel presets (`internal/renderer/babel_presets.go`), not
hardcoded in the template, so they can be tuned per role later. The `rxcost` (Default Cost below)
comes from the same preset, overridden by an edge's `priority`/`weight` link cost when set.

**Route redistribution by role:**

| Role | Announces | Default Cost |
|------|-----------|-------------|
| `peer` | Own overlay IP | 0 |
| `router` | Own overlay IP + Domain CIDR + extra prefixes (when set) | 0 |
| `relay` | Own overlay IP + Domain CIDR + extra prefixes (when set) | 96 (prefer direct) |
| `gateway` | Own overlay IP + Domain CIDR + extra prefixes + default route | 0 |
| `client` | None (no Babel) | — |

Announcements use two redistribution mechanisms (`internal/renderer/babel.go`):
- **`redistribute local ip <prefix> allow`** — for prefixes backed by a `dummy0` connected route:
  the node's own overlay `/32`, and (on the router side) injected client `/32`s.
- **`redistribute ip <prefix> allow`** (no `local` keyword) — for prefixes backed by a real
  kernel route that is *not* a `dummy0` connected route: a node's `extra_prefixes` (LAN segments)
  and the gateway's default route `0.0.0.0/0` (the WAN default). Using the non-`local` form is what
  lets these actually match a kernel route and propagate (audit dossier D40/D41).

The trailing `redistribute local deny` is critical — it prevents accidental advertisement of transit IPs or system routes.

### 4.4 Key Management and Persistence

WireGuard keys are **persistent**, not regenerated on every compile. The first compile of a new
node generates a fresh key pair and writes **both** the private and public keys back onto the node
in the topology JSON (the private key round-trips by design so a stateless compiler can re-render
the node's own `Interface PrivateKey`). Every subsequent compile reuses that pair, so adding an
unrelated node never rotates an existing node's key.

- **Rotation is explicit:** a node's key changes only when the operator clears **both** key fields
  (forcing a fresh generation) or pastes a different private key. No edit rotates a key as a side
  effect.
- **Migration contract:** a node that carries a public key but no private key is a hard error — the
  operator must paste the live private key (read from the host's `/etc/wireguard/<iface>.conf`) or
  clear both key fields to rotate. The one-time migration from the older rotate-every-compile
  behavior is described in
  [spec/compiler/allocation-stability.md](spec/compiler/allocation-stability.md).

Because the persisted topology (and browser localStorage) now carries live private keys, treat it
as secret material. The full contract is in [spec/data-model/node.md](spec/data-model/node.md) and
[spec/security/security.md](spec/security/security.md).

---

## 5. Generated Artifacts

### 5.1 Artifact Directory Structure

Each node's deployment bundle contains everything needed to go live:

```
node-alpha/
  ├── wireguard/
  │   ├── wg-node-beta.conf      # WireGuard tunnel config to beta
  │   └── wg-node-gamma.conf     # WireGuard tunnel config to gamma
  ├── babel/
  │   └── babeld.conf            # Babel routing daemon config
  ├── sysctl/
  │   └── 99-overlay.conf        # Kernel params (forwarding, rp_filter)
  ├── install.sh                 # One-click install script
  ├── manifest.json              # Build metadata and file manifest
  ├── checksums.sha256           # SHA-256 integrity verification
  └── README.txt                 # Quick-start instructions
```

### 5.2 WireGuard Config Details

Example generated per-peer WireGuard config:

```ini
# WireGuard per-peer interface: wg-node-beta
# Node: node-alpha -> Peer: node-beta

[Interface]
PrivateKey = <private_key>
Address = 10.10.0.1/32
Table = off
ListenPort = 51820

PostUp = ip -6 addr add fe80::1/64 dev %i 2>/dev/null || true
PostDown = ip -6 addr del fe80::1/64 dev %i 2>/dev/null || true

[Peer]
PublicKey = <public_key>
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = 203.0.113.2:51820
```

**Key design points:**

- **`Table = off`**: Prevents `wg-quick` from adding kernel routes. Since `AllowedIPs = 0.0.0.0/0`, without this each interface would try to add a default route, conflicting with each other. Routing is entirely managed by Babel.
- **`AllowedIPs = 0.0.0.0/0, ::/0`**: Safe in the per-peer model — each interface has only one peer, allowing any traffic through the tunnel. Babel decides which tunnel to use.
- **`PostUp`/`PostDown`**: Adds IPv6 link-local addresses needed for Babel neighbor discovery.

### 5.3 Install Script Four-Phase Logic

`install.sh` follows an idempotent phased deployment:

**Usage:**

```bash
sudo bash install.sh              # Install / upgrade overlay
sudo bash install.sh --uninstall  # Completely remove overlay from this node
```

**`--uninstall` / `-u` flag:** Performs a complete teardown:
- Stops and disables all managed and legacy WireGuard interfaces
- Removes all WireGuard config files from `/etc/wireguard/`
- Stops and disables Babel, removes Babel configs and systemd overrides
- Removes overlay SNAT rule and `overlay-snat.service`
- Removes sysctl overlay config and re-applies system defaults
- Removes the `dummy0` overlay interface and its `overlay-dummy.service`
- Reloads systemd daemon

**Normal install phases:**

**Phase 0 — Cleanup**
- Stop and remove existing WireGuard interfaces and old configs
- **Comprehensive legacy cleanup**: Scans all active `wg*` interfaces (`wg show interfaces`) and `/etc/wireguard/*.conf` files, removing anything not managed by the current overlay (catches `wg0`, `wg1`, `wg-overlay`, or any other leftover profile)
- Stop Babel daemon
- Remove old sysctl config

**Phase 1 — Environment Preparation**
- Verify file integrity (checksums.sha256)
- Check root privileges, detect OS (Debian / Ubuntu)
- Install dependencies (`wireguard`, `wireguard-tools`, `babeld`)
- Create `dummy0` interface and assign overlay IP
- Install systemd service to persist `dummy0` across reboots
- Configure overlay source NAT (SNAT) to fix source address selection (see 5.4b)

**Phase 2 — Deploy Configuration**
- Copy WireGuard configs to `/etc/wireguard/`
- Copy Babel config to `/etc/babel/`
- Copy sysctl config to `/etc/sysctl.d/`

**Phase 3 — Activate and Verify**
- Apply sysctl settings
- Start all `wg-quick@<interface>` services
- Configure babeld systemd override (declares dependency on all WireGuard services)
- Start and enable babeld
- Display status summary

### 5.4 dummy0 + Table=off Design

This combination is the key to making per-peer interfaces work with Babel:

```
┌─────────────────────────────────────────┐
│              Node alpha                   │
│                                           │
│  dummy0: 10.11.0.1/32  ← Overlay IP      │
│  (stable address, Babel announces)        │
│                                           │
│  wg-node-beta:  10.10.0.1/32 (Table=off) │
│  wg-node-gamma: 10.10.0.3/32 (Table=off) │
│                                           │
│  Babel manages all routing decisions      │
│  - Learns routes from neighbors           │
│  - Installs/removes kernel routes         │
│  - Automatically handles link failover    │
└─────────────────────────────────────────┘
```

- `dummy0` provides the stable address that Babel announces — apps and DNS always point here
- Each WireGuard interface has `Table = off`, so `wg-quick` doesn't touch the routing table
- Babel treats each `wg-*` interface as an independent tunnel link, tracking reachability independently
- When a link fails, Babel automatically reroutes through surviving links — no manual adjustment needed

### 5.4b Source Address Fix (Overlay SNAT)

**The problem:** In the per-peer interface model, each WireGuard interface has a transit IP address (e.g., `10.10.0.3/32`). When the kernel sends a packet to an overlay destination (e.g., `10.111.0.3`), Babel routes it via a `wg-*` interface, and the kernel picks the **transit IP** as the source address — not the overlay IP on `dummy0`. This causes:

- `ping 10.111.0.3` to **silently fail** (the remote receives the packet, but the reply to `10.10.0.3` is unroutable because transit IPs are not announced by Babel)
- `ping -I 10.111.0.2 10.111.0.3` to **work correctly** (explicit source override)

**The fix:** The install script adds an SNAT (Source Network Address Translation) rule that rewrites the source address of all packets leaving via `wg-*` interfaces with a transit source IP (`10.10.0.0/24`) to the node's overlay IP:

```
# nftables (preferred):
table inet overlay-snat {
    chain postrouting {
        type nat hook postrouting priority srcnat; policy accept;
        oifname "wg-*" ip saddr 10.10.0.0/24 snat to <overlay_ip>
    }
}

# iptables (fallback):
iptables -t nat -A POSTROUTING -o wg-+ -s 10.10.0.0/24 -j SNAT --to-source <overlay_ip>
```

The install script auto-detects `nft` and falls back to `iptables`. A persistent `overlay-snat.service` systemd unit ensures the rule survives reboots.

**Manual fix for existing deployments:**

```bash
# Replace <OVERLAY_IP> with the node's overlay IP (e.g. 10.111.0.2)

# nftables:
sudo nft add table inet overlay-snat
sudo nft add chain inet overlay-snat postrouting '{ type nat hook postrouting priority srcnat; policy accept; }'
sudo nft add rule inet overlay-snat postrouting oifname "wg-*" ip saddr 10.10.0.0/24 snat to <OVERLAY_IP>

# OR iptables:
sudo iptables -t nat -A POSTROUTING -o wg-+ -s 10.10.0.0/24 -j SNAT --to-source <OVERLAY_IP>
```

### 5.5 Auto-Deploy Scripts

Compilation generates two project-level auto-deploy scripts:

- `deploy-all.sh` (Bash, Linux/macOS)
- `deploy-all.ps1` (PowerShell, Windows/Linux)

**Usage:**

```bash
bash deploy-all.sh path/to/artifacts.zip

# Clean all existing WireGuard configs before deploying
bash deploy-all.sh --clean path/to/artifacts.zip

# Completely remove overlay from all nodes (no artifacts ZIP needed)
bash deploy-all.sh --uninstall
```

```powershell
.\deploy-all.ps1 -ArtifactsZip path\to\artifacts.zip

# Clean all existing WireGuard configs before deploying
.\deploy-all.ps1 -ArtifactsZip path\to\artifacts.zip -Clean

# Completely remove overlay from all nodes (no artifacts ZIP needed)
.\deploy-all.ps1 -Uninstall
```

**Options:**

| Flag (bash) | Flag (PS1) | Description |
|---|---|---|
| `--clean` | `-Clean` | Remove all existing WireGuard interfaces before deploying (useful when migrating between single-interface and per-peer layouts) |
| `--uninstall` | `-Uninstall` | SSH into each node and directly tear down the overlay: stop all named WireGuard interfaces, remove configs, stop Babel, remove dummy0, reload systemd. No installer upload needed. |

**Workflow:**
1. Extract the artifacts ZIP to a temp directory
2. For each node with SSH details configured:
   - SCP the self-extracting installer to remote `/tmp/`
   - SSH and run `sudo bash /tmp/<node>.install.sh`
   - Clean up remote temp files after execution
3. Skip nodes without SSH configuration
4. Print deployment summary (success / skipped / failed counts)

**SSH connection modes:**
- If SSH Alias is set: connects via `ssh <alias>`
- If manual SSH fields are set: connects via `ssh -p <port> -i <key> <user>@<host>`
- Password authentication is not supported

### 5.6 Canvas Visualization

After compilation, the canvas displays rich visual information:

**Multi-interface handles:**
- Each node shows multiple connection points (top = inbound, bottom = outbound)
- Each handle corresponds to a per-peer WireGuard interface
- Different peers use different colors (red, orange, yellow, green, cyan, indigo, fuchsia, rose — cycling)
- Hovering a handle shows interface name, listen port, and peer node name

**Node info cards:**
- After compilation, node cards display colored tags for each peer interface: `<peerName>:<port>`
- Colors match the corresponding handles

**Edge labels:**
- Format: `<source> → <target> | <endpoint>`
- Color-coded by type: direct=cyan, public-endpoint=amber, relay-path=violet, candidate=gray

---

## 6. Debugging and Troubleshooting

### 6.1 Development Environment

Use `dev.sh` for quick start/stop of the dev environment:

```bash
./dev.sh start     # Start backend :8080 + frontend :5173 (background)
./dev.sh stop      # Stop all
./dev.sh restart   # Stop then start
./dev.sh status    # Show running status
./dev.sh logs      # Tail both log files
```

Log files are in the project root:
- `.dev-backend.log` — Go backend log
- `.dev-frontend.log` — Vite frontend log

### 6.2 Common Issues

#### Port already in use

```bash
# See what's using the port
lsof -i :8080
lsof -i :5173

# Force stop
./dev.sh stop
```

`dev.sh stop` automatically kills processes on ports 8080/5173.

#### Nodes overlap on canvas

Node positions persist within the session after dragging. If nodes overlap:
1. Drag nodes to new positions — they will persist across subsequent operations
2. Refreshing the page resets to the default grid layout (4 columns, 280x250px spacing)

#### Compilation fails

**Common causes:**
- Missing domain definition (at least one Domain required)
- Node not assigned to a domain
- Invalid CIDR format
- Isolated node (no edges)

**Debug steps:**
1. Click "Compile" and read the error message
2. Check browser DevTools Console for frontend errors
3. Check backend log: `tail -f .dev-backend.log`

#### WireGuard interface won't start

```bash
# Check interface status
wg show

# Check specific interface
wg show wg-node-beta

# Manually start an interface
sudo wg-quick up wg-node-beta

# Inspect the config file
cat /etc/wireguard/wg-node-beta.conf

# Check systemd service status
systemctl status wg-quick@wg-node-beta
```

#### Babel routes not working

```bash
# Check babeld status
systemctl status babeld

# Dump Babel routing table
echo "dump" | nc ::1 33123

# Follow babeld logs
journalctl -u babeld -f

# Check kernel routing table
ip route show table main | grep -E "^10\."

# Verify dummy0 address
ip addr show dummy0
```

#### Install script fails

```bash
# Run in verbose mode
sudo bash -x install.sh

# Verify checksums
cd /path/to/node-dir && sha256sum -c checksums.sha256

# Manual cleanup then retry
sudo wg-quick down wg-node-beta 2>/dev/null
sudo bash install.sh
```

#### SSH auto-deploy fails

```bash
# Test SSH connection (alias)
ssh -v my-server-alias

# Test SSH connection (manual params)
ssh -v -p 22 -i ~/.ssh/id_ed25519 root@1.2.3.4

# Check key permissions (should be 600)
ls -la ~/.ssh/id_ed25519

# Test SCP upload
scp -P 22 -i ~/.ssh/id_ed25519 test.txt root@1.2.3.4:/tmp/
```

### 6.3 API Debugging

Backend API endpoints:

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/health` | GET | Health check |
| `/api/validate` | POST | Validate topology JSON |
| `/api/compile` | POST | Compile and return all configs |
| `/api/export` | POST | Compile and export ZIP artifact bundle |

```bash
# Health check
curl http://localhost:8080/api/health

# Manual compile (using exported JSON)
curl -X POST http://localhost:8080/api/compile \
  -H "Content-Type: application/json" \
  -d @project.json | jq .

# Validate topology
curl -X POST http://localhost:8080/api/validate \
  -H "Content-Type: application/json" \
  -d @project.json | jq .
```

### 6.4 Network Debugging

```bash
# Test overlay connectivity
ping -c 3 10.11.0.2

# Test WireGuard tunnel (transit IP)
ping -c 3 10.10.0.2

# Trace route
traceroute -n 10.11.0.2

# Check WireGuard handshake status
sudo wg show all | grep -A5 "latest handshake"

# Check MTU
ping -M do -s 1392 10.11.0.2

# Capture WireGuard UDP traffic
sudo tcpdump -i eth0 udp port 51820

# Capture overlay tunnel traffic
sudo tcpdump -i wg-node-beta
```
