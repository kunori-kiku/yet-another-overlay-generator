# Yet Another Overlay Generator (YAOG)

Yet Another Overlay Generator is a robust, web-based control plane and code generator designed for declarative configuration of modern overlay networks. It seamlessly orchestrates **WireGuard** (for Layer 3 cryptographic tunnels) and **Babel** (for dynamic routing) to create complex mesh, hub-and-spoke, or hybrid topologies with ease.

## Features

- **Visual Topology Builder:** Drag-and-drop React Flow interface to design your network nodes and connect their links. Color-coded per-peer interface handles appear after compilation.
- **In-Browser Compiler (no backend for local design):** Local design compiles entirely in the browser — a TypeScript port of the Go compiler, pinned **byte-for-byte** to the Go output by a CI conformance gate. The Go backend's job is the controller (the air-gap compute routes are gated behind a build tag), so the classic "design → compile → export bundle" flow needs only the static frontend.
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
- **Server-Authoritative Controller (2.0):** In controller mode the controller's stored design is the single source of truth — opening the panel lands on a full-page login gate, and each login hydrates the canvas from the server (downloading a safety backup of any divergent, undeployed local work first). WireGuard private keys are enforced never to reach the server, the controller retains the last 10 topology versions for recovery, and one approved key binds to exactly one node-id. The operator/agent secret path prefixes are independent (`YAOG_OPERATOR_PATH_PREFIX` / `YAOG_AGENT_PATH_PREFIX`). Upgrading an existing controller? See [`docs/MIGRATION-controller-server-authority.md`](docs/MIGRATION-controller-server-authority.md).
- **Dashboard App Shell (2.0):** The operator panel is a deep-linkable dashboard — **Overview, Design, Fleet, Deploy, Security, Settings** sections behind a collapsible sidebar. The Design page pairs the canvas with a creation toolbar and a selection-driven properties aside; navigation and the landing page adapt to the mode (controller → Overview, local generator → Design).
- **Off-Host Signing Keystone (2.0):** Each controller Deploy is signed by the operator's hardware-backed passkey in a WebAuthn ceremony over the exact trust-list bytes, and nodes verify the signature before applying — a compromised controller alone cannot push configs to your fleet.
- **Hardened Operator Auth (2.0):** Sessions live in an httpOnly cookie (login survives page refresh; no token in `localStorage`) with double-submit CSRF protection and credentialed CORS (`YAOG_PANEL_ORIGIN`). Second factors are TOTP (RFC 6238) and/or passkeys; passkeys also enable passwordless login.
- **Fleet Management (2.0):** Per-node fleet pages with status detail, single-use enrollment tokens minted from the panel, a compile-history/audit view, and manual fleet-wide WireGuard key rotation (Roll keys).
- **Live Fleet Health (2.0):** Agents report structured **Node Conditions** (`configapply`, `selfupdate`, `wireguard`, `mimic`) and refresh them on a dedicated `/telemetry` heartbeat (default 30s), so the panel reflects *current* health — not a frozen apply-time snapshot. The node-detail page has a collapsible **WireGuard links** panel showing each peer's last handshake, and a partly-degraded node reads `SomePeersDown` (which link is down), not a blanket `LinkDown`.
- **Signed Agent Self-Update + Version-Aware Rollout (2.0):** Agents can update their own binary from a release, verified against the controller-signed `artifacts.json` (hash + a self-test) before exec, with a crash-bounded canary-then-fleet rollout and an anti-downgrade floor. The panel knows its own version, drives a one-click "update all to the controller version," and refuses a target newer than itself; a stalled rollout surfaces as a `selfupdate: Blocked` condition.
- **Mimic `.deb` Catalog (2.0):** For distros that don't package mimic, the panel pins per-`<codename>-<arch>` `.deb` packages by SHA-256; **Discover from release** lists a GitHub release's `.deb` assets to pick from (the install verifies each against the signed pin before `dpkg`).
- **Theming, i18n & Accessibility (2.0):** System-following dark/light themes with manual override and optional translucency/vibrancy, reduced-motion support, keyboard/skip-link accessibility, and a fully bilingual English/中文 UI.

## Getting Started

### Two ways to run YAOG

- **Local generator (air-gap).** Design a topology and export deployable bundles. **Local design compiles entirely in the browser** — a TypeScript port of the Go compiler, pinned byte-for-byte to the Go oracle by a CI conformance gate — so no backend is involved. You only need the frontend; see [Quick Start](#1-quick-start) below.
- **Controller (agent-pull).** Run YAOG as a long-lived service where each node **pulls** its own keystone-signed config and reports live health back. The Go backend is the **controller**; see [Controller Mode (Docker)](#controller-mode-docker).

> The `cmd/server` **default build is controller-only**: running it without the controller env (`YAOG_CONTROLLER_STATE_DIR` + `YAOG_TENANT_ID`) **exits with a loud error** rather than standing up an anonymous compute listener — the air-gap compute routes (`/api/{validate,compile,export,deploy-script}`) are gated behind `//go:build airgap`. For offline compilation use the in-browser generator, the `cmd/compiler` CLI, or the `-tags airgap` local-design oracle.

### Prerequisites

- Node.js `v20+` (LTS recommended) — all you need for local design
- Go `1.25+` (the module pins `toolchain go1.26.4`, fetched automatically) — only to build the backend / CLI

### 1. Quick Start

For **local design**, the frontend alone is enough — its compiler runs in the browser:

```bash
cd frontend
npm install --legacy-peer-deps
npm run dev          # Vite dev server on :5173 — open http://localhost:5173
```

`./dev.sh` is a contributor convenience that runs the Vite frontend (where local design compiles) and also launches the Go server. Note the Go server is the **default controller-only build**, so it only stays up when the controller env is set (`YAOG_CONTROLLER_STATE_DIR` + `YAOG_TENANT_ID`) — for pure local design you only need the frontend above; export those vars before `./dev.sh start` to also bring up a dev controller:

```bash
./dev.sh start     # Vite frontend on :5173 (+ the Go server when the controller env is set)
./dev.sh stop      # Stop all
./dev.sh restart   # Restart both
./dev.sh status    # Check if running
./dev.sh logs      # Tail both log files
```

### 2. Running the Go backend

The `cmd/server` binary is the **controller** (or, built `-tags airgap`, the local-design oracle):

```bash
# Controller — set the controller env first (or just use Docker; see Controller Mode below):
YAOG_CONTROLLER_STATE_DIR=./data YAOG_TENANT_ID=default go run ./cmd/server/

# Air-gap local-design oracle — serves the anonymous /api/{validate,compile,export,deploy-script} routes:
go run -tags airgap ./cmd/server/
```

The CLI compiler reads a topology JSON and writes `output/` with no server at all:

```bash
go run ./cmd/compiler/ -input topology.json   # -input is required; -output defaults to ./output
```

#### Browser E2E tests

The panel has a full-stack Playwright E2E layer (built panel + a live Go controller + a
real agent fixture). See [`frontend/e2e/README.md`](frontend/e2e/README.md) to run it and
to add a scenario. It also runs as the `frontend-e2e` CI check.

## Basic Usage Guide

All topology editing happens on the **Design** page (the default landing in local mode):

1. **Add Domains:** Use the domain form in the canvas toolbar to add a logical IP Domain (e.g., `10.10.0.0/24`). Set allocation mode to Automatic.
2. **Add Nodes:** Use the node form in the canvas toolbar. Define their Roles (Peer, Router, Relay, Gateway, Client) and capabilities (e.g., a public address / `Can Forward`).
3. **Edit Properties:** Select any domain, node, or edge on the canvas (or via the toolbar's list drawer) to edit it in the right-hand aside — including the optional SSH Connection section (alias or host/port/user/key) used by auto-deploy.
4. **Draw Edges:** Connect nodes by dragging from source to target on the canvas. Set the endpoint IP (from target's public addresses dropdown). Leave the port at `0` so the compiler allocates it; only set `endpoint_port` when you need an explicit NAT/port-forward override.
5. **Compile:** Hit `Compile` in the canvas toolbar to allocate IPs and ports, derive peer configs, and generate all artifacts. This runs **in the browser** (no backend round-trip). The canvas will show color-coded per-peer interface handles, and each edge displays the allocated `compiled_port` read-only.
6. **Export & Deploy:** Switch to the **Deploy** page to review the compiled artifacts and download the artifact ZIP. Use the generated `deploy-all.sh` or `deploy-all.ps1` to deploy to all SSH-configured nodes in one command.

## Controller Mode (Docker)

> **New in 2.0 (beta).** Instead of exporting an air-gapped bundle, you can run YAOG as a long-lived **controller** and let each node **pull** its own signed config. The controller is a single Docker image (the SPA panel + API); the per-node agent is a small host binary the controller hands you a one-line installer for. The classic generator/export flow above is unchanged.

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

The panel + operator API is at **`http://localhost:8080`** (the node-facing agent API is on `:9090`). In controller mode you land on a **full-page login screen** before any of the panel chrome — log in as `admin`. The session is an httpOnly cookie, so it survives page refreshes; sign-out is in the top-right account menu, and the optional break-glass operator token is entered from the login page's **Recovery** disclosure.

**The server is authoritative.** On every login the panel pulls the controller's stored design and overwrites its local canvas — the browser cache is a disposable mirror. If your browser is holding a local design that differs from the server copy, the panel downloads a fresh `pre-hydration-backup-<date>.json` and shows a notice before overwriting — this happens on *every* such overwrite, not just the first, so undeployed local work is never silently lost.

After login you land on **Overview** (topology + fleet at a glance). The other sections: **Design** (the topology canvas), **Fleet** (node enrollment + per-node detail, with "not in design" markers for orphaned fleet rows), **Deploy** (compile preview + one-click Deploy, with a shrink-guard confirmation; the server keeps the last 10 topology versions for recovery via its API), **Security** (TOTP and passkey enrollment, audit log, compile history), and **Settings** (mode, connection, bootstrap, appearance — switching back to local mode is a confirmed, lossy action that regenerates keys). The EN/中文 language toggle sits in the top bar (and on the login screen).

> **Upgrading an existing controller?** This release renames the secret path prefix env and changes the login/hydration flow — see [`docs/MIGRATION-controller-server-authority.md`](docs/MIGRATION-controller-server-authority.md) before deploying.

By default both ports bind to **loopback only** (`127.0.0.1`) — the login form carries a plaintext password, so nothing is exposed on other interfaces out of the box. Reach the panel from the same host, or tunnel it: `ssh -L 8080:127.0.0.1:8080 <host>`.

> **Passkeys/WebAuthn work over `http://localhost`** (browsers treat loopback as a secure context), so you can test password + TOTP/passkey login locally **without** TLS. ⚠️ Use the hostname **`localhost`**, not the IP `http://127.0.0.1` — WebAuthn forbids IP-address domains, so passkey enrollment at `127.0.0.1` fails with *"invalid domain."* For any **remote** access, front the controller with a TLS-terminating reverse proxy (an example `caddy` service is commented in the compose file) — plain HTTP on a public address would both leak the password and make browsers refuse the passkey ceremony. (The rest of the panel — including Compile — degrades gracefully over plain HTTP on a LAN address; only the passkey/WebAuthn ceremonies hard-require a secure context.)

### 4. Deploy to a node (agent pull)

To let a remote node pull its config, first expose the agent port — for a lab, `YAOG_BIND_ADDR=0.0.0.0 docker compose up -d`; for production, the TLS proxy above. Then, in the panel:

1. On the **Settings** page, under **Bootstrap Settings**, set the **Public Agent URL** to where nodes reach the controller (e.g. `https://overlay.example.com` or `http://<host>:9090`).
2. Add the node to your topology (Design page), then on the **Fleet** page mint a single-use **enrollment token** for it.
3. On the target host (Linux + systemd), as root:

```bash
bash <(curl -fsSL https://<public-agent-url>/api/v1/agent/bootstrap) \
     --token <enrollment-token> --node-id <id>
```

This downloads the `yaog-agent` binary, enrolls the node, applies the current generation, and installs a systemd daemon so future **Deploy**s auto-apply. Add `--once` to apply a single generation without the daemon. With the keystone enabled, each Deploy is signed by your off-host hardware key and the node verifies the signature before applying.

Besides `--token` and `--node-id` (required), the one-liner accepts `--controller`, `--gh-proxy`, and `--release-base` to override the server-configured defaults. It installs the agent at `/usr/local/bin/yaog-agent` with a `yaog-agent.service` systemd unit; the per-node bearer token lands at `/etc/wireguard/agent-controller.token` (mode 0600), and with the keystone on, the operator's verification credential at `/etc/wireguard/operator-cred.pem`. The agent binary itself has `keygen` / `enroll` / `run` subcommands for manual or non-systemd setups — see [`docs/spec/controller/agent.md`](docs/spec/controller/agent.md).

### 5. Configuration reference

Controller behavior is configured through environment variables on the container (set them in `docker-compose.yml`), plus a few server-stored settings edited in the panel.

| Variable | Default | What it does |
|---|---|---|
| `YAOG_BIND_ADDR` | `127.0.0.1` | Compose-only: the host interface both published ports bind to. Set `0.0.0.0` to expose them beyond loopback. |
| `YAOG_PANEL_PORT` | `8080` | Compose-only: the **host** port the operator/panel API is published on (the container always listens on `8080`). Override to avoid a clash or match a proxy rule. |
| `YAOG_AGENT_PORT` | `9090` | Compose-only: the **host** port the agent API is published on (the container always listens on `9090`). |
| `YAOG_CONTROLLER_STATE_DIR` | unset | Controller state directory. Together with `YAOG_TENANT_ID`, this is what switches controller mode on (the image sets `/data`). |
| `YAOG_TENANT_ID` | unset | Tenant identifier scoping all controller state (single-tenant for now; the compose defaults it to `default`). |
| `YAOG_CONTROLLER_AGENT_ADDR` | `:9090` | Listen address of the node-facing agent API. |
| `YAOG_OPERATOR_PATH_PREFIX` | empty | Optional **secret path prefix** for the operator/panel API (`:8080`) — mounts its routes under `/<prefix>/api/v1/operator/...`. See below. |
| `YAOG_AGENT_PATH_PREFIX` | empty | Optional **secret path prefix** for the agent API (`:9090`) — independent from the operator prefix; the bootstrap one-liner bakes it into the installed agent's controller URL. |
| `YAOG_PANEL_ORIGIN` | empty | Comma-separated allowlist of origins permitted credentialed (cookie) cross-origin panel access; needed only when the panel is served from a different origin (requires HTTPS). Same-origin Docker needs none. |
| `YAOG_SECURE_COOKIE` | `true` | `Secure` attribute on the session/CSRF cookies. Set `false` only for local non-TLS development. |
| `YAOG_CONTROLLER_OPERATOR_TOKEN` | unset | Optional break-glass bearer token for operator routes — a recovery path if the operator login is lost. Only its SHA-256 is kept in memory. |
| `YAOG_BUNDLE_SIGNING_KEY` | unset | Path to an Ed25519 private key (PKCS#8 PEM). When set, every exported bundle carries a detached signature and `install.sh` pins the public key; loading is fail-closed. |
| `YAOG_WEB_DIR` | unset | Directory the server serves the panel SPA from (the image sets `/app/web`). |

**Secret path prefixes.** The two audiences also live under distinct API namespaces — the operator/panel API under `/api/v1/operator/` and the agent API under `/api/v1/agent/` — so they never collide by path. Setting `YAOG_OPERATOR_PATH_PREFIX=s3cr3t` moves the operator/panel API to `/s3cr3t/api/v1/operator/...`; `YAOG_AGENT_PATH_PREFIX` does the same for the agent API (`/<agent-prefix>/api/v1/agent/...`), independently — so a path-based proxy on one hostname can route each audience to its own port (`/<operator-prefix>/*` → `:8080`, `/<agent-prefix>/*` → `:9090`) without ambiguity, and you can expose only the agent endpoint publicly while keeping the operator panel behind a VPN. This is defense-in-depth obscurity, **not** a security boundary — bearer tokens and the keystone signature remain the real ones. The panel's **Secret Path Prefix** field is a *mirror* of the **operator** prefix, not a setting: it tells the panel where the operator API lives and must match the server's deploy-time value. Nodes never type either — the bootstrap one-liner bakes the agent-prefixed URL into the installed agent. The server names both mounted base paths in its startup log, so a proxy misroute is diagnosable from `docker compose logs` in seconds.

**Server-stored bootstrap settings** (edited on the panel's **Settings** page, persisted controller-side): the **Public Agent URL** nodes use to reach the controller, an optional **GitHub proxy** prefix for agent-binary downloads (useful behind restrictive egress), and an optional **agent release base URL** override.

Full reference: [`docs/spec/controller/docker.md`](docs/spec/controller/docker.md) and [`docs/spec/controller/bootstrap.md`](docs/spec/controller/bootstrap.md).

## Documentation

- Architectural ground truth: [`specs/`](specs/) — start with [`specs/README.md`](specs/README.md).
- Controller-mode internals (deep reference): [`docs/spec/controller/`](docs/spec/controller/).
- [Wiki (English)](docs/wiki.md) — full user guide covering **both** the local / air-gap generator and controller mode (architecture, concepts, usage, compiler internals, artifacts, operations, troubleshooting).
- [Wiki (中文)](docs/wiki-zh.md) — 覆盖**本地 / air-gap 生成器与控制器模式**的完整中文用户文档。

## Debugging

Quick debugging reference (see the [Wiki](docs/wiki.md#6-debugging-and-troubleshooting) for local-mode details):

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
