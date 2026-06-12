# Yet Another Overlay Generator (YAOG)

Yet Another Overlay Generator is a robust, web-based control plane and code generator designed for declarative configuration of modern overlay networks. It seamlessly orchestrates **WireGuard** (for Layer 3 cryptographic tunnels) and **Babel** (for dynamic routing) to create complex mesh, hub-and-spoke, or hybrid topologies with ease.

## Features

- **Visual Topology Builder:** Drag-and-drop React Flow interface to design your network nodes and connect their links. Color-coded per-peer interface handles appear after compilation.
- **Per-Peer WireGuard Interfaces:** Each peer connection gets a dedicated WireGuard interface with an independently allocated listen port, compatible with Babel dynamic routing.
- **Parallel Links & Babel Failover:** A node pair can carry a primary link plus backup links, each its own WireGuard interface; Babel picks by per-link cost and fails over automatically (e.g. a plain-UDP primary with a `TCP (mimic)` backup). See [`docs/spec/data-model/edge.md`](docs/spec/data-model/edge.md).
- **TCP-Shaping Transport (mimic):** Set an edge's transport to **`TCP (mimic)`** to wrap its WireGuard traffic with [mimic](https://github.com/hack3ric/mimic) (an eBPF UDP→fake-TCP shaper) so the link traverses networks that throttle (UDP QoS) or block UDP ports. The install script provisions mimic from the node's distribution and configures it automatically; MTU is auto-lowered and both endpoints must be Linux with eBPF. This is a connectivity/performance feature for UDP-restricted networks — **not** a censorship/DPI-circumvention tool. See [`docs/spec/artifacts/mimic.md`](docs/spec/artifacts/mimic.md).
- **Smart Validation:** Early-fail checks catch logical errors such as missing public IPs, broken NAT requirements, and dangling isolated nodes.
- **Persistent Cryptographic Key Management:** Generates `wg` keys for new nodes on first compile and persists them back onto the topology, so subsequent recompiles reuse the same keys. Key rotation is an explicit operator action (see [`docs/spec/data-model/node.md`](docs/spec/data-model/node.md)).
- **Compiler-Owned Ports:** The compiler is the sole authority for WireGuard listen ports. `compiled_port` is read-only output; `endpoint_port` is an explicit operator NAT/port-forward override only. Allocations are pinned per link, so values stay stable across recompiles when you add nodes (see [`docs/spec/data-model/edge.md`](docs/spec/data-model/edge.md) and [`docs/spec/compiler/allocation-stability.md`](docs/spec/compiler/allocation-stability.md)).
- **SSH Auto-Deploy:** Configure SSH connection details per node, then use the generated `deploy-all.sh` / `deploy-all.ps1` scripts to deploy to all nodes via SSH in one command.
- **Comprehensive Legacy Cleanup:** Install scripts automatically detect and remove all stale WireGuard interfaces and configs (not just `wg0`), ensuring clean upgrades.
- **Offline Configuration Bundles:** One-click deployment bundle generation — download portable `.zip` archives containing safe Bash installation scripts, sysctl modifications, Babel daemons, and WireGuard interfaces.
- **Immutable Artifacts:** Generated scripts hash and verify checksums (`sha256`) explicitly mitigating tamper attacks.
- **Controller Mode (Agent-Pull Deploy):** Optionally run YAOG as a long-lived **controller** — a single Docker image (panel + API) where each node **pulls** its own keystone-signed config instead of you exporting a bundle. Operator login (password + optional TOTP / passkey 2FA), one-line node enrollment, and one-click Deploy. See [Controller Mode (Docker)](#controller-mode-docker).
- **Dashboard App Shell (2.0):** The operator panel is a deep-linkable dashboard — **Overview, Design, Fleet, Deploy, Security, Settings** sections behind a collapsible sidebar. The Design page pairs the canvas with a creation toolbar and a selection-driven properties aside; navigation and the landing page adapt to the mode (controller → Overview, local generator → Design).
- **Off-Host Signing Keystone (2.0):** Each controller Deploy is signed by the operator's hardware-backed passkey in a WebAuthn ceremony over the exact trust-list bytes, and nodes verify the signature before applying — a compromised controller alone cannot push configs to your fleet.
- **Hardened Operator Auth (2.0):** Sessions live in an httpOnly cookie (login survives page refresh; no token in `localStorage`) with double-submit CSRF protection and credentialed CORS (`YAOG_PANEL_ORIGIN`). Second factors are TOTP (RFC 6238) and/or passkeys; passkeys also enable passwordless login.
- **Fleet Management (2.0):** Per-node fleet pages with status detail, single-use enrollment tokens minted from the panel, a compile-history/audit view, and manual fleet-wide WireGuard key rotation (Roll keys).
- **Theming, i18n & Accessibility (2.0):** System-following dark/light themes with manual override and optional translucency/vibrancy, reduced-motion support, keyboard/skip-link accessibility, and a fully bilingual English/中文 UI.

## Getting Started

### Prerequisites

- Go `1.21+`
- Node.js `v18+` (LTS recommended)

### 1. Quick Start (Dev Script)

The easiest way to run both backend and frontend:

```bash
./dev.sh start
```

This starts the Go backend on `:8080` and Vite frontend on `:5173` in the background. Visit `http://localhost:5173`.

```bash
./dev.sh stop      # Stop all
./dev.sh restart   # Restart both
./dev.sh status    # Check if running
./dev.sh logs      # Tail both log files
```

### 2. Manual Setup

#### Backend

```bash
# From the project root
go run ./cmd/server/main.go
```

The server will begin listening on `:8080`.

#### Frontend

```bash
cd frontend
npm install --legacy-peer-deps
npm run dev
```

Visit `http://localhost:5173` in your browser.

## Basic Usage Guide

All topology editing happens on the **Design** page (the default landing in local mode):

1. **Add Domains:** Use the domain form in the canvas toolbar to add a logical IP Domain (e.g., `10.10.0.0/24`). Set allocation mode to Automatic.
2. **Add Nodes:** Use the node form in the canvas toolbar. Define their Roles (Peer, Router, Relay, Gateway, Client) and capabilities (e.g., a public address / `Can Forward`).
3. **Edit Properties:** Select any domain, node, or edge on the canvas (or via the toolbar's list drawer) to edit it in the right-hand aside — including the optional SSH Connection section (alias or host/port/user/key) used by auto-deploy.
4. **Draw Edges:** Connect nodes by dragging from source to target on the canvas. Set the endpoint IP (from target's public addresses dropdown). Leave the port at `0` so the compiler allocates it; only set `endpoint_port` when you need an explicit NAT/port-forward override.
5. **Compile:** Hit `Compile` in the canvas toolbar to allocate IPs and ports, derive peer configs, and generate all artifacts. The canvas will show color-coded per-peer interface handles, and each edge displays the allocated `compiled_port` read-only.
6. **Export & Deploy:** Switch to the **Deploy** page to review the compiled artifacts and download the artifact ZIP. Use the generated `deploy-all.sh` or `deploy-all.ps1` to deploy to all SSH-configured nodes in one command.

## Controller Mode (Docker)

> **New in 2.0 (preview).** Instead of exporting an air-gapped bundle, you can run YAOG as a long-lived **controller** and let each node **pull** its own signed config. The controller is a single Docker image (the SPA panel + API); the per-node agent is a small host binary the controller hands you a one-line installer for. The classic generator/export flow above is unchanged.

Requires Docker Engine with the Compose plugin (`docker compose`, v2).

### 1. Start the controller

```bash
# Grab the compose file (or clone the repo and use the one at the root)
curl -fsSLO https://raw.githubusercontent.com/kunori-kiku/yet-another-overlay-generator/main/docker-compose.yml

# State lives in ./data (a bind mount). The container runs as uid 65532,
# so create that directory with the right owner ONCE:
mkdir -p data && sudo chown 65532:65532 data

docker compose up -d
```

All controller state persists to `./data` next to the compose file, so backing up the controller is just snapshotting that folder. No `.env` is required — the compose file ships with working defaults.

> **Image visibility:** the compose pulls `ghcr.io/kunori-kiku/yaog-controller:latest`. If the pull is denied (the GHCR package is private), either run `docker login ghcr.io` first, or build locally — comment `image:` and uncomment `build: .` in `docker-compose.yml` (needs a repo checkout).

### 2. Create an operator account

```bash
docker compose run --rm controller create-operator \
    --state-dir /data --tenant default --username admin
```

You'll be prompted for a password (entered without echo). This is the account you log into the panel with. Re-run with `--force` to reset an existing operator's password.

### 3. Open the panel

The panel + operator API is at **`http://localhost:8080`** (the node-facing agent API is on `:9090`). Log in as `admin` — the session is an httpOnly cookie, so it survives page refreshes.

In controller mode you land on **Overview** (topology + fleet at a glance). The other sections: **Design** (the topology canvas), **Fleet** (node enrollment + per-node detail), **Deploy** (compile preview + one-click Deploy), **Security** (TOTP and passkey enrollment, audit log, compile history), and **Settings** (mode, connection, bootstrap, appearance). The EN/中文 language toggle sits in the top bar.

By default both ports bind to **loopback only** (`127.0.0.1`) — the login form carries a plaintext password, so nothing is exposed on other interfaces out of the box. Reach the panel from the same host, or tunnel it: `ssh -L 8080:127.0.0.1:8080 <host>`.

> **Passkeys/WebAuthn work over `http://localhost`** (browsers treat loopback as a secure context), so you can test password + TOTP/passkey login locally **without** TLS. ⚠️ Use the hostname **`localhost`**, not the IP `http://127.0.0.1` — WebAuthn forbids IP-address domains, so passkey enrollment at `127.0.0.1` fails with *"invalid domain."* For any **remote** access, front the controller with a TLS-terminating reverse proxy (an example `caddy` service is commented in the compose file) — plain HTTP on a public address would both leak the password and make browsers refuse the passkey ceremony. (As of `v2.0.0-preview.5` the rest of the panel — including Compile — degrades gracefully over plain HTTP on a LAN address; only the passkey/WebAuthn ceremonies hard-require a secure context.)

### 4. Deploy to a node (agent pull)

To let a remote node pull its config, first expose the agent port — for a lab, `YAOG_BIND_ADDR=0.0.0.0 docker compose up -d`; for production, the TLS proxy above. Then, in the panel:

1. On the **Settings** page, under **Bootstrap Settings**, set the **Public Agent URL** to where nodes reach the controller (e.g. `https://overlay.example.com` or `http://<host>:9090`).
2. Add the node to your topology (Design page), then on the **Fleet** page mint a single-use **enrollment token** for it.
3. On the target host (Linux + systemd), as root:

```bash
bash <(curl -fsSL https://<public-agent-url>/api/v1/controller/bootstrap) \
     --token <enrollment-token> --node-id <id>
```

This downloads the `yaog-agent` binary, enrolls the node, applies the current generation, and installs a systemd daemon so future **Deploy**s auto-apply. Add `--once` to apply a single generation without the daemon. With the keystone enabled, each Deploy is signed by your off-host hardware key and the node verifies the signature before applying.

Full reference: [`docs/spec/controller/docker.md`](docs/spec/controller/docker.md) and [`docs/spec/controller/bootstrap.md`](docs/spec/controller/bootstrap.md).

## Documentation

- [Wiki (English)](docs/wiki.md) — Full documentation including architecture, parameters, and troubleshooting
- [Wiki (中文)](docs/wiki-zh.md) — 完整中文文档

## Debugging

Quick debugging reference (see the [Wiki](docs/wiki.md#6-debugging-and-troubleshooting) for full details):

```bash
# Dev environment logs
./dev.sh logs

# API health check
curl http://localhost:8080/api/health

# WireGuard status
sudo wg show

# Babel routing table
echo "dump" | nc ::1 33123

# Test overlay connectivity
ping 10.11.0.2
```

## One-Click Deploy (Prebuilt Binaries)

The repository includes two deployment scripts that pull a **single prebuilt platform bundle** from GitHub Releases into a subdirectory under your current directory, then generate local startup scripts for backend + frontend:

- `scripts/deploy.sh` (bash)
- `scripts/deploy.ps1` (PowerShell on Linux/Windows)

Release assets are now published as platform bundles:

- Linux: `yaog-bundle-linux-<arch>.tar.gz`
- Windows: `yaog-bundle-windows-<arch>.zip`

Supported targets currently include:

- Linux: `amd64`, `arm64`, `386`, `armv7`
- Windows: `amd64`, `arm64`, `386`

### Required Tag Format

Release workflow triggers on tags matching:

`v*`

Recommended semantic version style:

- `v0.1.0`
- `v1.0.0`
- `v1.2.3`

If you do not pass `--tag` / `-Tag`, deploy uses the **latest release** automatically.

### Bash Example

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/kunori-kiku/yet-another-overlay-generator/main/scripts/deploy.sh)
```

Deploy a specific tag:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/kunori-kiku/yet-another-overlay-generator/main/scripts/deploy.sh) --tag v0.1.0
```

Optional flags:

- `--dir ./yaog-v0.1.0`
- `--skip-frontend`

After deployment:

```bash
cd ./yaog-<resolved-tag>
./start.sh
```

This starts both:

- Backend API (default `127.0.0.1:8080`)
- Frontend static server + `/api/*` proxy (default `127.0.0.1:5173`)

Stop services:

```bash
./stop.sh
```

### PowerShell Example

```powershell
pwsh -NoProfile -Command "& ([scriptblock]::Create((Invoke-RestMethod https://raw.githubusercontent.com/kunori-kiku/yet-another-overlay-generator/main/scripts/deploy.ps1)))"
```

Deploy a specific tag:

```powershell
pwsh -NoProfile -Command "& ([scriptblock]::Create((Invoke-RestMethod https://raw.githubusercontent.com/kunori-kiku/yet-another-overlay-generator/main/scripts/deploy.ps1))) -Tag v0.1.0"
```

Optional flags:

- `-Dir ./yaog-v0.1.0`
- `-SkipFrontend`

Start locally from generated directory:

```powershell
cd ./yaog-<resolved-tag>
pwsh ./start.ps1
```
