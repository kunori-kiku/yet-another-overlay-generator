# Yet Another Overlay Generator (YAOG)

Yet Another Overlay Generator is a robust, web-based control plane and code generator designed for declarative configuration of modern overlay networks. It seamlessly orchestrates **WireGuard** (for Layer 3 cryptographic tunnels) and **Babel** (for dynamic routing) to create complex mesh, hub-and-spoke, or hybrid topologies with ease.

## Features

- **Visual Topology Builder:** Drag-and-drop React Flow interface to design your network nodes and connect their links. Color-coded per-peer interface handles appear after compilation.
- **Per-Peer WireGuard Interfaces:** Each peer connection gets a dedicated WireGuard interface with an independently allocated listen port, compatible with Babel dynamic routing.
- **Smart Validation:** Early-fail checks catch logical errors such as missing public IPs, broken NAT requirements, and dangling isolated nodes.
- **Automatic Cryptographic Key Management:** Generates and distributes secure `wg` keys for your active topology automatically (during compilation).
- **Split Endpoint Configuration:** Endpoint IP (from target node's public addresses) and Port (from compiler-allocated WireGuard interface) are configured independently, with auto-fill support.
- **SSH Auto-Deploy:** Configure SSH connection details per node, then use the generated `deploy-all.sh` / `deploy-all.ps1` scripts to deploy to all nodes via SSH in one command.
- **Comprehensive Legacy Cleanup:** Install scripts automatically detect and remove all stale WireGuard interfaces and configs (not just `wg0`), ensuring clean upgrades.
- **Offline Configuration Bundles:** One-click deployment bundle generation — download portable `.zip` archives containing safe Bash installation scripts, sysctl modifications, Babel daemons, and WireGuard interfaces.
- **Immutable Artifacts:** Generated scripts hash and verify checksums (`sha256`) explicitly mitigating tamper attacks.

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

1. **Add Domains:** Open the left panel and add a logical IP Domain (e.g., `10.10.0.0/24`). Set allocation mode to Automatic.
2. **Add Nodes:** Create nodes via the left panel. Define their Roles (Peer, Router, Relay, Gateway) and capabilities (e.g., `Publicly Reachable` / `Can Forward`).
3. **Configure SSH (optional):** Expand the SSH Connection section in node properties to set SSH alias or host/port/user/key for auto-deploy.
4. **Draw Edges:** Connect nodes by dragging from source to target on the canvas. Set the endpoint IP (from target's public addresses dropdown) and port separately.
5. **Compile:** Hit `Compile` to allocate IPs, derive peer configs, and generate all artifacts. The canvas will show color-coded per-peer interface handles.
6. **Auto-fill Ports:** After compilation, click the `Auto:<port>` button on each edge to fill in the compiler-allocated port.
7. **Export & Deploy:** Hit `Export` to download the artifact ZIP. Use the generated `deploy-all.sh` or `deploy-all.ps1` to deploy to all SSH-configured nodes in one command.

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
