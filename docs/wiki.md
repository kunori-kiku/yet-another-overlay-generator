# Yet Another Overlay Generator — Wiki

> Also available in: [中文](wiki-zh.md)

This wiki is the full user guide for **YAOG** — it covers **both** ways the project runs:

- **Local generator (air-gap):** design a topology in the browser, compile it **entirely in the
  browser**, export deployable per-node bundles, and install them over SSH. No backend is involved.
- **Controller (agent-pull):** run YAOG as a long-lived service where each node **pulls** its own
  keystone-signed config and reports live health back to an operator panel.

Both modes share one compiler (the in-browser TypeScript port is pinned byte-for-byte to the Go
implementation by a CI conformance gate), so the topology model, allocation, and rendered artifacts
are identical between them. The architectural ground truth lives in [`docs/spec/`](spec/) — this wiki
is the narrative guide on top of it.

---

## Table of contents

1. [Overview](#1-overview)
2. [Core concepts](#2-core-concepts)
3. [The two modes & the build boundary](#3-the-two-modes--the-build-boundary)
4. [Local mode — design, compile, export, deploy](#4-local-mode--design-compile-export-deploy)
5. [Controller mode — the agent-pull control plane](#5-controller-mode--the-agent-pull-control-plane)
6. [Compiler internals](#6-compiler-internals)
7. [Generated artifacts](#7-generated-artifacts)
8. [Security model](#8-security-model)
9. [HTTP API reference](#9-http-api-reference)
10. [Debugging & troubleshooting](#10-debugging--troubleshooting)
11. [Glossary](#11-glossary)

---

## 1. Overview

Yet Another Overlay Generator is a web-based network design and configuration-generation system.
You define nodes, network domains, and connectivity through a visual topology editor; the system
allocates addresses and deterministically generates **WireGuard** (Layer-3 cryptographic tunnels) +
**Babel** (dynamic routing) configurations, along with one-click install scripts.

### Design philosophy

The system follows a **Design → Compile → Deploy** three-layer architecture:

```text
[Web canvas  /  CLI]
        │  Topology JSON
        ▼
[Compiler]                       ← runs in the browser (local mode) or on the controller
  ├─ Schema validation
  ├─ Semantic validation
  ├─ IP allocation
  ├─ Capability inference
  ├─ Peer derivation
  └─ Config renderers
        │  ├─ WireGuard configs
        │  ├─ Babel configs
        │  ├─ sysctl kernel params
        │  ├─ install scripts
        │  └─ deploy scripts
        ▼
[Artifact exporter]              ← per-node bundles (local) or per-node signed bundles (controller)
        ▼
[Target hosts]
        └─ install.sh runs → the overlay comes up
```

Core principles:

- **Topology as code.** The JSON topology is the single source of truth; every config is
  deterministically derived from it.
- **Deterministic compilation.** The same topology always produces the same bytes (the compiler is a
  pure function of its inputs — see [§6](#6-compiler-internals)). This is what lets the in-browser
  TypeScript compiler be pinned byte-for-byte to the Go one.
- **Idempotent deployment.** Install scripts can be safely re-run; adding a node leaves unrelated
  nodes' bundles byte-stable.
- **Keys stay where they belong.** In local mode keys are generated and held on your design host; in
  controller mode each node holds its own private key and the controller never sees it (see
  [§8](#8-security-model)).

---

## 2. Core concepts

These concepts are identical in both modes — they describe the topology model the compiler consumes.

### 2.1 Domain

A **Domain** is an overlay address space defining the allocatable IP range.

| Field | Description |
|-------|-------------|
| Name | Display name and logical identifier |
| CIDR | Address range, e.g. `10.11.0.0/24` |
| Allocation Mode | `auto` (compiler assigns) / `manual` (you specify per node) |
| Routing Mode | `babel` (dynamic routing) — the only implemented mode; `static` / `none` are reserved and **rejected at validation** today (empty normalizes to `babel`) |

### 2.2 Node and roles

A **Node** represents a machine (cloud VM, bare-metal server, container host).

**Basic fields:** Name, Hostname (optional), Platform (`debian` / `ubuntu`), Domain membership,
Overlay IP (optional manual override), WireGuard base listen port (default 51820), MTU (optional),
Router ID (optional Babel MAC-48; blank = auto-generated).

**Roles and capabilities** (authoritative source: `internal/compiler/roles.go`):

| Role | IP forward | Accept inbound | Runs Babel | Babel announces | Typical use |
|------|-----------|----------------|------------|-----------------|-------------|
| `peer` | No | No | Yes | Own overlay `/32` only | End-user node |
| `router` | Yes | Only if it has a public IP | Yes | Own `/32` + Domain CIDR + extra prefixes (when set) | Backbone forwarding node |
| `relay` | Yes | **Yes (always)** | Yes | Own `/32` + Domain CIDR + extra prefixes (when set) | NAT-traversal relay |
| `gateway` | Yes | Only if it has a public IP | Yes | Own `/32` + Domain CIDR + extra prefixes + **default route `0.0.0.0/0`** | Bridge to external networks |
| `client` | No | No | **No** | None (no Babel) | Lightweight endpoint (phone, laptop) |

> **Accept-inbound is conditional.** `router` and `gateway` accept inbound only when the node is
> publicly reachable; `relay` always accepts inbound. A node with any public endpoint is treated as
> publicly reachable even without the explicit flag (`roles.go` normalizes `HasPublicIP` up when
> `PublicEndpoints` is non-empty).

> **Extra prefixes.** `router` and `relay` announce their `extra_prefixes` (e.g. a LAN segment behind
> the node) only when the field is non-empty; `gateway` announces them unconditionally. Extra
> prefixes and the gateway default route are announced via the kernel-route mechanism
> (`redistribute ip <prefix> allow`, matching a real connected/WAN kernel route) rather than
> `redistribute local`. See [spec/roles/roles.md](spec/roles/roles.md).

> **Link cost (Babel `rxcost`) — a per-role default with per-edge overrides.** The default is per
> *node-role*: a `relay` is written with an explicit `rxcost 96` (a cost bias so paths avoid relaying
> when a direct link exists), while `router` / `gateway` / `peer` omit the token and let babeld apply
> its own wired default. An edge's `priority` (if > 0), else its `weight`, overrides the default; a
> **backup** edge carries a preset cost (384) so Babel prefers the primary while it is up. See
> [§2.3](#23-edge-directed-connection), [spec/compiler/routing-modes.md](spec/compiler/routing-modes.md),
> and [spec/artifacts/babel.md](spec/artifacts/babel.md).

> **Client role.** Client is the lightest role, for devices that don't participate in dynamic
> routing. A client uses a single `wg0` interface to connect to one router/relay/gateway. It does not
> run Babel, does not use `dummy0`, and does not use the per-peer interface model. Client reachability
> is achieved by kernel-route injection on the router side (`PostUp = ip route add <client_ip>/32 dev
> %i`) plus Babel redistribution.

**Capability fields** (inferred from role, overridable): Publicly Reachable, Can Accept Inbound, Can
Forward, Can Relay.

**Multiple public endpoints.** A node can carry several `Host:Port` public endpoint mappings
(hostnames allowed) for multi-exit / multi-ISP / NAT multi-mapping scenarios.

**SSH connection (auto-deploy).** A node can optionally store SSH connection details used by the
generated deploy scripts (local mode):

| Field | Description |
|-------|-------------|
| SSH Alias | Host alias from `~/.ssh/config`; if set, overrides the manual fields below |
| SSH Host | SSH target IP or hostname |
| SSH Port | SSH port (default 22) |
| SSH User | SSH login username (default root) |
| SSH Key Path | Path to the SSH private key on **your** machine |

> Password authentication is not supported — use key-based auth. SSH details are collapsed by default
> in the node properties panel and are never WireGuard key material.

### 2.3 Edge (directed connection)

A directed edge `A → B` means **A actively connects to B**.

| Field | Description |
|-------|-------------|
| Type | `direct` / `public-endpoint` / `relay-path` / `candidate` |
| Endpoint Host | Target public IP or hostname; pick from the target node's public endpoints or enter manually |
| Endpoint Port | Operator NAT/port-forward override: `0` (default) = compiler auto-allocates; nonzero = the external port the from-side dials verbatim |
| Compiled Port | Read-only: the port the from-side actually dials, filled in after compilation |
| Transport | `udp` = plain WireGuard. `tcp` = the link is wrapped by [mimic](https://github.com/hack3ric/mimic) (eBPF UDP→fake-TCP) for networks that throttle or block UDP. Both ends must be Linux with eBPF; MTU is auto-lowered; the installer provisions mimic from the distro. **Not** a censorship/DPI-circumvention feature. See [spec/artifacts/mimic.md](spec/artifacts/mimic.md) |
| Link direction | `A ⇄ B` (doubly linked, default) = both sides may initiate the handshake. `A → B` (single-linked) = only A ever dials; B keeps routing but never initiates. The third choice, `B → A`, **flips the edge** (visible: the arrow reverses, allocations follow their nodes) and then single-links it. See the callout below |
| Priority / Weight | Link-cost preference (lower wins); feeds the Babel `rxcost` |
| Is Enabled | Whether this edge participates in compilation |

> **Port ownership.** The compiler is the sole authority for WireGuard listen ports. `endpoint_port`
> is *not* a copy of the allocated port — leave it at `0` and the compiler dials the remote
> interface's auto-allocated listen port, writing the result to the read-only `compiled_port`. Set
> `endpoint_port` to a nonzero value only as an explicit NAT / port-forward override (e.g. a router
> DNATs external `:51900` → the node's internal `:51820`); the override is honored verbatim and
> preserved across recompiles. Full contract in [spec/data-model/edge.md](spec/data-model/edge.md).

> **Parallel links & failover.** A node pair can carry a primary link plus one or more **backup**
> links, each its own WireGuard interface. Babel picks by per-link cost and fails over automatically —
> e.g. a plain-UDP primary with a `TCP (mimic)` backup. Backup links get a higher default cost (384)
> so the primary is preferred while it is up.

> **When to single-link (accelerators & relays).** When the from-node has a public endpoint (or an
> explicit reverse edge carries a host), a doubly-linked edge quietly creates TWO dial paths: the
> from-side dials your Endpoint Host, and the to-side auto-dials the from-node's first public
> endpoint (its plain address). WireGuard keeps one *runtime* endpoint per peer and follows
> whoever handshook last — so if you route `A → B` through a UDP accelerator but B boots first, B
> dials A **direct** and the accelerator is bypassed permanently. Setting the direction to
> `A → B` (single-linked) removes the race: B keeps the tunnel, keeps routing, but never initiates —
> it simply answers A's handshake arriving through the accelerator. A single-linked edge **requires**
> an Endpoint Host (otherwise no side could dial — validation rejects it loudly), and the editor
> shows where a doubly-linked edge's *reverse* dial would come from, so the asymmetry is never a
> surprise. Client links can't be single-linked, and a primary-class edge can't be single-linked
> while its node pair carries another enabled primary-class edge (they fold into one tunnel — the
> direction would be silently ignored); backup links are their own tunnels and can (validation
> explains each case).

> **mimic needs a direct path (no L7 relay).** `tcp` (mimic) shapes UDP into fake-TCP and needs
> **L3/L4 packet transparency end to end**. An L7 / UDP-accelerator relay that terminates and
> re-originates the connection (a gost/realm-style relay doing DNAT+SNAT) breaks it — the reverse
> fake-TCP leg is `RST`'d — so a link that rides such a relay must use **`transport: udp`**, not
> `tcp`. YAOG warns at design time: an enabled `tcp` edge of type `relay-path` raises the
> `validation_edge_mimic_relay_path` warning advising `udp` (a warning, not a blocker).

### 2.4 Two-layer address separation

The system uses two independent IP pools so link addresses never collide with node-identity addresses:

| | Overlay IP (identity) | Transit IP (link) |
|---|---|---|
| Pool | Per-Domain CIDR (e.g. `10.11.0.0/24`) | Per-domain `transit_cidr` (default `10.10.0.0/24`) |
| Assigned to | `dummy0` interface | Each per-peer WireGuard interface |
| Purpose | Stable node identity (DNS, apps, monitoring) | Tunnel point-to-point addressing |
| Babel announces | Yes (`redistribute local`) | No — internal only |
| Stability | Does not change with topology | Changes as links are added/removed |

Each link also gets a pair of IPv6 link-local addresses (`fe80::X`) for Babel neighbor discovery.

### 2.5 Per-peer WireGuard interface model

**Why not a single `wg0` with multiple peers?** The traditional single-interface multi-peer model is
incompatible with Babel dynamic routing: Babel needs **one independent interface per neighbor** to
track each link's quality separately, a single `wg0` looks like one broadcast domain, and multiple
peers' `AllowedIPs` can collide.

**Per-peer design** — each peer connection uses a dedicated WireGuard interface:

```
Node alpha:
  wg-beta    ← tunnel to beta  (port 51820)
  wg-gamma   ← tunnel to gamma (port 51821)
  dummy0     ← stable overlay address
```

Each interface has: an independent listen port (base port + incrementing offset), an independent
transit IP (`/32` point-to-point) + IPv6 link-local, exactly one `[Peer]` section, `Table = off`
(wg-quick adds no routes — Babel manages routing), and `AllowedIPs = 0.0.0.0/0, ::/0` (safe with one
peer per interface).

**Interface naming.** `wg-<peer-name>`, lowercased, every character outside `[a-z0-9-]` (including
`_`) replaced by `-`. The Linux kernel caps interface names at 15 characters, so: if `wg-<clean-name>`
is ≤ 15 chars it is used verbatim; otherwise the algorithm returns `wg-` + the first 8 cleaned
characters + the first 4 hex chars of `sha256(peer-name)` (3 + 8 + 4 = 15). The hash suffix keeps two
distinct long names that share a prefix from colliding. The backend is the sole authority for this
name (`internal/naming`); the frontend always consumes the compiled name, never re-derives it. Full
algorithm in [spec/artifacts/naming.md](spec/artifacts/naming.md).

---

## 3. The two modes & the build boundary

YAOG is built from one source tree but ships as **two distinct deployments**, plus a CLI. **Which
compute surface exists depends on the build, not on runtime configuration** — this is a deliberate
security boundary. The authority is [spec/operations/deployment-topology.md](spec/operations/deployment-topology.md).

### 3.1 Local generator (air-gap) — compute in the browser

The local generator is a **pure-frontend bundle**: the panel runs entirely in the browser, and the
**in-browser TypeScript compiler** (`frontend/src/compiler/`) performs validate / compile / export.
It POSTs to no backend — there is no server listener at all, so you can host it on any static file
server or CDN. The release ships a self-contained `yaog-local-design-<version>.zip` for this; you can
also just run the frontend dev server (see [§4](#4-local-mode--design-compile-export-deploy)).

A build-time flag, `VITE_LOCAL_ONLY`, produces a **mode-locked** static site: controller mode is
made unreachable (the toggle and controller-only nav are hidden, and a persisted controller mode is
coerced back to local). This is what the `yaog-local-design` asset is built with.

### 3.2 Controller — the long-lived Go backend

The default `go build ./...` produces `yaog-server`, the **controller** (panel + API). It serves the
panel SPA, the public `GET /api/health` probe, the operator routes on `:8080`, and the agent routes on
`:9090`. The controller's compile path is the **operator-gated** server-side render
(`compile-preview` / `stage`), not an anonymous compute endpoint.

> **The default binary is controller-only and fails loud.** Running `yaog-server` **without** both
> `YAOG_CONTROLLER_STATE_DIR` and `YAOG_TENANT_ID` set **exits with a loud error** rather than
> standing up an anonymous compute listener. This is the `//go:build airgap` boundary: the four
> anonymous compute routes — `POST /api/{validate,compile,export,deploy-script}` — exist **only** in a
> `go build -tags airgap` build. In the default (shipped) controller and Docker image they are not
> linked and **return 404**. A regression test (`airgap_routes_removed_test.go`) pins this.

### 3.3 The third path — the `cmd/compiler` CLI

`cmd/compiler` is the offline CLI and reference implementation. It reads a topology JSON and writes a
bundle directory with no server at all, producing byte-identical output in either build profile:

```bash
go run ./cmd/compiler/ -input topology.json   # -input is required; -output defaults to ./output
```

### 3.4 Where compute runs, at a glance

| Artifact | Build | Compute surface |
|---|---|---|
| Static local-design site (`yaog-local-design-<ver>.zip`) | `npm run build:local` | In-browser TS compiler; **no** backend listener |
| Controller `yaog-server` (shipped binary + Docker image) | `go build ./...` | `/api/health` + operator/agent routes; compile is the operator-gated server render. The 4 anonymous routes 404. Fails loud without the controller env. |
| Local-design oracle `yaog-server-airgap-*` (dev/E2E/DAST only) | `go build -tags airgap ./...` | Retains the 4 anonymous `/api/{validate,compile,export,deploy-script}` + `/api/health` |
| `cmd/compiler` CLI | either build | Offline `render → export`, byte-identical in both profiles |

The in-browser compiler is justified as the default by the **conformance gate**
(`internal/conformance/`), a required green CI check that pins the TypeScript compiler byte-for-byte
against the Go pipeline through the frozen `localcompile.Compile` I/O contract
([spec/compiler/io-contract.md](spec/compiler/io-contract.md)).

---

## 4. Local mode — design, compile, export, deploy

In local mode everything happens in the browser; the only thing you run is the frontend.

```bash
cd frontend
npm install --legacy-peer-deps
npm run dev          # Vite dev server on :5173 — open http://localhost:5173
```

(`./dev.sh start` is a contributor convenience that also launches the Go server, but the Go server is
the controller-only build and only stays up when the controller env is set — for pure local design the
frontend above is all you need.)

### 4.1 Topology editing workflow

All editing happens on the **Design** page (the default landing in local mode):

1. **Add Domains** — define an address space (CIDR), allocation mode, routing mode.
2. **Add Nodes** — set name, platform, role, and assign to a domain.
3. **Add public endpoints** (optional) — `Host:Port` for nodes with public ingress.
4. **Configure SSH** (optional) — connection details for auto-deploy (collapsed by default).
5. **Draw Edges** — drag from source to target on the canvas; set the endpoint host (leave the port at
   `0` unless you need a NAT override).
6. **Validate** — check completeness and semantic errors (runs in the browser).
7. **Compile** — allocate IPs and ports, derive peer configs, render all artifacts (runs in the
   browser; no backend round-trip). The canvas then shows color-coded per-peer handles and each edge's
   read-only `compiled_port`.
8. **Export & Deploy** — switch to the **Deploy** page to review the compiled artifacts and download
   the artifact ZIP, plus the generated `deploy-all.sh` / `deploy-all.ps1`.

**UI layout:** the center canvas visualizes nodes and directed edges with color-coded per-peer
handles; the canvas toolbar creates domains/nodes; the right-hand aside edits the selected
domain/node/edge; the bottom bar shows validation results.

### 4.2 Validation, compilation, and export

**Validation** checks two categories:

- **Schema** — required fields, type correctness, reference validity (e.g. a node's `domain_id`
  points to an existing domain).
- **Semantic** — IP conflicts, isolated nodes, illegal CIDRs, broken NAT reachability.

**Compilation** deterministically produces per-peer WireGuard configs, per-node Babel config, per-node
sysctl params, per-node install scripts, and the project-level deploy scripts.

**Export** packages per-node directories containing all config files, `install.sh`, `manifest.json`,
and `checksums.sha256`. See [§7](#7-generated-artifacts) for the full layout and the install-script
phases.

### 4.3 Deploying the bundles

Each node's bundle is self-contained — copy it to the host and run `sudo bash install.sh`. For
fleets, the generated `deploy-all.sh` (Bash) / `deploy-all.ps1` (PowerShell) SSH into every
SSH-configured node and run the installer for you; see [§7.5](#75-auto-deploy-scripts).

---

## 5. Controller mode — the agent-pull control plane

> **New in 2.0 (beta).** Instead of exporting an air-gap bundle, run YAOG as a long-lived
> **controller** and let each node **pull** its own signed config. The controller is a single Docker
> image (the SPA panel + API); the per-node agent is a small host binary the controller hands you a
> one-line installer for. The classic generator/export flow above is unchanged.

### 5.1 Start the controller (Docker)

Requires Docker Engine with the Compose plugin (`docker compose`, v2).

```bash
# Grab the compose file (or use the one at the repo root)
curl -fsSLO https://raw.githubusercontent.com/kunori-kiku/yet-another-overlay-generator/main/docker-compose.yml

# State lives in ./data (a bind mount); the container runs as uid 65532, so create it once:
mkdir -p data && sudo chown 65532:65532 data

docker compose up -d
```

All controller state persists to `./data`, so backing up the controller is just snapshotting that
folder. The compose ships working defaults — no `.env` required. By default both ports bind to
**loopback only** (`127.0.0.1`) because the login form carries a plaintext password; reach the panel
from the same host or tunnel it (`ssh -L 8080:127.0.0.1:8080 <host>`).

> **Image visibility.** The compose pulls `ghcr.io/kunori-kiku/yaog-controller:latest`. If the pull is
> denied (the GHCR package is private), either `docker login ghcr.io` first, or build locally —
> comment `image:` and uncomment `build: .` in `docker-compose.yml`.

### 5.2 Create an operator and log in

```bash
docker compose run --rm controller create-operator \
    --state-dir /data --tenant default --username admin
```

You'll be prompted for a password (no echo); add `--force` to reset an existing operator. The panel +
operator API is at **`http://localhost:8080`** (the node-facing agent API is on **`:9090`**). In
controller mode you land on a **full-page login screen** before any panel chrome — log in as `admin`.

The password is hashed with **argon2id**; a successful login mints a session that lives in an
**httpOnly cookie**, so login survives page refresh with **no token in `localStorage`**. Sign-out is
in the top-right account menu; the optional break-glass operator token is entered from the login
page's **Recovery** disclosure. Optional second factors are **TOTP** (RFC 6238) and/or **passkeys**;
passkeys also enable passwordless login. See [§8.4](#84-operator-authentication) for the full auth
model.

> **Passkeys/WebAuthn work over `http://localhost`** (browsers treat loopback as a secure context).
> ⚠️ Use the hostname **`localhost`**, not `127.0.0.1` — WebAuthn forbids IP-address domains, so
> passkey enrollment at `127.0.0.1` fails with *"invalid domain."* For any **remote** access, front
> the controller with a TLS-terminating reverse proxy (an example `caddy` service is commented in the
> compose file).

### 5.3 The server is authoritative

In controller mode the **controller's stored design is the single source of truth**; the browser
cache is a disposable mirror. On every login (and on cookie-session restore) the panel pulls the
controller's stored topology and overwrites its local canvas.

If your browser holds a local design that is non-empty **and** differs from the server copy, the panel
downloads a fresh `pre-hydration-backup-<date>.json` and shows a notice **before** overwriting. This
fires on *every* such overwrite (not just the first), so undeployed local work is never silently lost.
In steady state (local == server) no backup is downloaded.

After login you land on **Overview**. The dashboard sections (a collapsible sidebar; deep-linkable
routes):

| Section | Route | Modes | Purpose |
|---|---|---|---|
| Overview | `/overview` | controller only | Topology + fleet at a glance |
| Design | `/design` | both | The React Flow topology canvas |
| Fleet | `/fleet` | controller only | Node enrollment + per-node detail (with "not in design" markers for orphaned rows) |
| Deploy | `/deploy` | both | Compile preview + one-click Deploy (with a shrink-guard confirmation) |
| Security | `/security` | both | TOTP/passkey enrollment, audit log, compile history |
| Settings | `/settings` | both | Mode, connection, bootstrap, appearance |

Controller mode lands on `/overview`; local mode lands on `/design` and hides Overview/Fleet.

> **Upgrading an existing controller?** This release renames the secret path-prefix env and changes
> the login/hydration flow — see [`docs/MIGRATION-controller-server-authority.md`](MIGRATION-controller-server-authority.md)
> before deploying.

### 5.4 Enroll and deploy to a node (agent pull)

To let a remote node pull its config, first expose the agent port (`:9090`) — for a lab,
`YAOG_BIND_ADDR=0.0.0.0 docker compose up -d`; for production, the TLS proxy above. Then:

1. On **Settings → Bootstrap Settings**, set the **Public Agent URL** nodes use to reach the
   controller (e.g. `https://overlay.example.com` or `http://<host>:9090`).
2. Add the node to your topology (**Design**), then on the **Fleet** page mint a single-use
   **enrollment token** for it.
3. On the target host (Linux + systemd), as root:

```bash
bash <(curl -fsSL https://<public-agent-url>/api/v1/agent/bootstrap) \
     --token <enrollment-token> --node-id <id>
```

This downloads the `yaog-agent` binary, enrolls the node, applies the current generation, and installs
a `yaog-agent.service` systemd daemon so future Deploys auto-apply. The per-node bearer token lands at
`/etc/wireguard/agent-controller.token` (mode 0600); with the keystone on, the operator's verification
credential lands at `/etc/wireguard/operator-cred.pem`. Besides the required `--token` / `--node-id`,
the one-liner accepts `--controller`, `--gh-proxy`, and `--release-base` overrides.

**The enrollment ceremony** (stdlib crypto only — no CA, no CSR, no mTLS): the panel mints a
**single-use, short-TTL** enrollment token (stored hashed); the node presents it once to `/enroll`
along with its WireGuard **public** key and receives a standing **per-node bearer token** (returned
exactly once, thereafter sent as `Authorization: Bearer …`). The enrollment token is **burned
atomically before** any identity is issued, so a token can never provision two nodes. One approved
public key binds to exactly one node-id (a duplicate is refused, 409). Revoking a node clears its
bearer token (it stops resolving on the very next call) and evicts it from every future render.

### 5.5 The deploy lifecycle — compile → stage → promote

A Deploy is a two-phase, operator-gated transition over a per-tenant monotonic **generation** counter:

1. **Compile + stage** (`Deploy` → stage). The controller loads the stored topology, selects the
   **enrolled subgraph**, runs the same frozen pipeline as local mode, and **stages** per-node signed
   bundles at `generation + 1`. Staging is reversible and invisible to agents (not yet `current`).
   Re-staging replaces the prior staged set; it does not advance the counter.
2. **Promote** (the atomic flip). All staged bundles become `current`, the generation increments, and
   every parked agent long-poll is woken. The controller never self-promotes.

**Render-what's-ready.** The controller renders only the **enrolled subgraph** — a node is included
only if it is approved *and* has a registered public key; an edge is kept only if *both* endpoints are
enrolled. This lets you design the whole fleet up front and bring nodes online incrementally; an edge
to a not-yet-enrolled peer reappears in both bundles when the far end enrolls and you re-deploy.
Allocation pins (overlay IPs, transit IPs, ports — never key material) are persisted back after each
stage, so incremental enrollment never renumbers already-live nodes.

**Delta deploy — only changed nodes re-stage.** A Deploy skips any node whose freshly compiled bundle
is **byte-identical** to the one it is already serving, leaving unchanged nodes completely alone. The
identity is a SHA-256 over the bundle's `checksums.sha256` (which covers `install.sh` and every config
file, but **not** the manifest's volatile `compiled_at`), so it is stable across recompiles whenever
the real config is unchanged and moves the instant any rendered byte does. A skipped node **keeps its
current generation** — its agent never sees a newer generation and never re-fetches — so the fleet
settles at a **mixed generation** where only the changed nodes advance, and a per-link re-handshake
happens only on the links whose endpoints actually changed (no more fleet-wide churn on a no-op
Deploy). The skip **fails open**: if the controller can't read a node's currently-served bundle it
treats the node as changed and re-stages it, never skipping on doubt.

**Pre-deploy preview.** The Deploy dialog shows a preview computed over your **current canvas** —
"N updated, M unchanged" — as a read-only dry-run that **stages nothing**. It shares the exact skip
decision and identity the real Deploy uses, so the count matches the actual outcome. If the preview
can't be fetched (e.g. a newer panel talking to an older controller), the panel surfaces the error but
still lets you **Deploy anyway** — the preview never hard-blocks a deploy.

**Force redeploy.** You can override the skip: **Force redeploy** re-stages a node (or the whole fleet)
even when unchanged, and a per-node **Force redeploy this node** sits on the node-detail page. Force is
for **on-host drift / rescue only** — a node whose local config was tampered with or lost and must be
re-pushed. It is **not** needed for ordinary changes (a real config change re-stages itself) and
**not** needed for keystone key-rotation or the first signed deploy, both of which auto-force a full
re-stage so the signed trust-list re-pins every node.

**Safety rails.** A Deploy that would empty the design or drop ≥ 50% of nodes requires typing the
project name to confirm. The controller keeps the **last 10 topology versions** for recovery, and an
append-only, hash-chained **audit log** records every enroll/revoke/stage/promote/rekey (the
`/telemetry` heartbeat is deliberately *not* audited — a 30s beat would flood the chain).

**Fleet-wide key rotation (Roll keys).** Rotation reuses the same model in four steps: (1) `rekey-all`
flags every approved node and bumps the generation to *wake* parked agents; (2) each woken agent
regenerates its private key and registers the new **public** key (skipping the stale woken bundle);
(3) you wait for every "rotating keys" badge to clear (Deploy is disabled meanwhile); (4) one normal
Deploy recompiles from the new public keys. The cost is a brief rolling per-link flap.

### 5.6 The node agent — pull, verify, apply

The agent (`cmd/agent`) is a thin, verify-then-apply wrapper over `install.sh`, not a reconciler:

1. **keygen** (one-time) generates a WireGuard keypair; the **private key** stays at
   `/etc/wireguard/agent.key` (mode 0600) and never leaves the host.
2. **poll/pull** — long-poll `GET /poll?after=<watermark>` blocks until a newer generation exists
   (a 204 means "no change"); then `GET /config` returns the promoted bundle.
3. **verify** — the agent recomputes the canonical `checksums.sha256`, verifies the keystone signature
   against the operator-pinned public credential, and re-checks every file's hash. Any mismatch is a
   **hard refusal** before anything runs as root.
4. **apply** — on a verified, non-rolled-back bundle, the agent runs the bundle's own `install.sh`,
   which splices the locally-held private key into the placeholder in the copied configs (see
   [§8.3](#83-zero-knowledge-key-custody)).
5. **report** — `POST /report` records the applied generation/checksum/health (best-effort).

`run --controller` is single-shot by default (one poll→apply→report cycle); `--daemon` loops it
continuously and is what the bootstrap installs. An anti-rollback high-water mark refuses a bundle
whose `manifest.json` build time is not newer than the last applied.

### 5.7 Signed agent self-update + version-aware rollout

An agent can replace its **own binary** with the version pinned in the verified bundle's
`artifacts.json` (which is itself covered by the bundle signature). The downloaded binary is verified
against the **signed SHA-256 pin** (never an upstream `.sha256` sidecar), passes a **self-test**
(`<newbin> version` must equal the desired version) before exec, and the swap is crash-bounded:
a `Restart=always` loop is capped at 3 attempts, after which the agent rolls back to the saved
`.bak`. A health check marks the new version **probationary**, and the **anti-downgrade floor** only
advances after a full clean cycle.

From the panel: a one-click **"Update all agents to {version}"** targets the controller's own version,
arms a **canary-then-fleet** rollout, and the controller **refuses a target newer than itself**. A
stalled rollout surfaces as a `selfupdate: Blocked` condition with an actionable reason.

### 5.8 Live fleet health — Node Conditions + the `/telemetry` heartbeat

Agents report structured **Node Conditions** — Kubernetes-style `{type, status, reason, message}` —
on a dedicated `POST /telemetry` heartbeat (default **30s**, set with the agent's
`--telemetry-interval` flag; `0` disables; the heartbeat is daemon-only). The heartbeat refreshes
conditions live, so the panel reflects *current* health instead of a frozen apply-time snapshot. It
carries conditions plus an extensible `metrics` map and deliberately **never** touches the deploy
custody fields (applied generation/checksum) — observability is strictly separate from deploy state,
and the heartbeat is not audited.

The four condition **types** (lowercase, closed set) and their `status` (`ok`/`warn`/`error`/`unknown`):

| Type | What it reports | Notable reasons |
|---|---|---|
| `configapply` | Last config apply | `Applied` (ok), `DegradedKeepingLastGood` (warn) |
| `selfupdate` | Self-update state | `Active`, `HealthConfirmedProbationary`, `Updated`, `Abandoned`, `Blocked` |
| `wireguard` | Link health | `AllPeersUp` (ok), `PeerHandshakeStale`, `SomePeersDown`, `LinkDown`, `NoInterfaces` |
| `mimic` | mimic shaper state | breadcrumb + live re-probe each heartbeat (`systemctl is-active`): `Stopped` (warn) if a should-be-running unit died since deploy, else the deploy outcome (`Active` / `FellBackToUDP` / `ModuleUnavailable` / `NativeDowngradedSkb`) |

> **`SomePeersDown` vs `LinkDown` (beta.12).** A single offline peer in a mesh (a link Babel routes
> around) now reads as **`SomePeersDown`** ("1/3 peers down") instead of the alarming whole-node
> **`LinkDown`**; `LinkDown` is reserved for *all* peers down (or a fresh apply before the first
> handshake).

**Per-peer "WireGuard links" panel (beta.12).** The node-detail page shows a **collapsible** "WireGuard
links" panel — the per-link detail behind the aggregate `wireguard` condition. Each row is one peer
with a status dot (green = up / yellow = stale / red = never) and a relative, live-ticking last
handshake. It is auto-opened only when a link is down/stale; an all-up node stays collapsed. The data
rides the heartbeat's `metrics["wireguard_peers"]` map (peer / interface / endpoint / last_handshake /
status — no key material). This telemetry is **live-only**: it is fetched fresh on refresh and
deliberately **not** persisted to the browser (a frozen handshake age would mislead, and the raw
endpoint is fleet-confidential).

**Host-resource metrics (`resource`).** Every heartbeat also carries a `resource` metric: `cpu_pct`
(optional), the `load1` / `load5` / `load15` load averages, and total / available memory. `cpu_pct` is
CPU utilisation measured as a **delta between consecutive heartbeats**, so the **first** beat after an
agent (re)starts carries **no** CPU value — a deliberate gap, not a `0` (a real 0 % and "not yet
measured" must never look alike); every later beat has it.

**CPU / RAM / load history charts.** The controller retains a bounded per-node **history** of the
`resource` metric, and the node-detail page charts it over time: CPU %, memory-used %, and the load
averages. You pick a **time range** and a **granularity** (step); the server aggregates the raw samples
into buckets (average / min / max per bucket) and **omits empty buckets** — a gap in the data (node
offline, history just enabled) stays a gap on the chart, never a fabricated zero. Very fine steps are
floored (≈ 1s), and a too-fine step over a large range is automatically widened so the chart stays
bounded; the effective step is echoed back, so the axis is labelled with what you actually got.

**Retention is configurable.** A per-node sample cap in controller **Settings** bounds the history; the
default is ≈ 20160 samples ≈ **7 days at the 30s heartbeat**. Setting the cap to **0 disables**
history — the charts then show a "history off" state. History is append-only and **never blocks the
heartbeat** (a heartbeat never writes to disk). It is also never frozen: a just-deployed node's charts
update **promptly** (not only at deploy time), because the heartbeat is the single source and the agent
nudges a fresh sample right after each apply.

### 5.9 Mimic `.deb` catalog

For distros that don't package mimic (Debian 12 / Ubuntu 24.04), the panel pins the mimic `.deb`
packages by SHA-256, per `<codename>-<arch>`. Upstream ships **two** packages you must pin together:
`mimic` (the tool) and `mimic-dkms` (its kernel module — the `mimic` package won't install without
it). **Discover from release** lists a GitHub release's `.deb` assets and **pairs** the `mimic` + the
`mimic-dkms` for one `<codename>-<arch>` into a single row; **Assist** fills in both SHA-256s (and, if
the proxy misses a sidecar, retries the direct GitHub URL). The install downloads, verifies each
against its signed pin, and installs **both** together before `dpkg`.

If a node's kernel is too old to build the module (its exact `linux-headers` are no longer in the repo
— common on a VPS booted months ago), the node editor **warns you up front** ("this kernel can't build
the mimic module — reboot into the current kernel"), and until you reboot the link falls back per its
**Mimic fallback** policy: *Fall back to UDP* brings it up as plain UDP, *Fail closed* keeps it down
with a clear `mimic` health chip — never a silent break. To fix it, on the node run
`apt-get update && apt-get install -y linux-image-cloud-amd64 linux-headers-cloud-amd64 && reboot`,
then redeploy.

**XDP mode (native vs skb).** A mimic link uses generic **skb** XDP by default (works on any NIC). You
can opt a node into **native** XDP (faster) in the node editor — but many VPS NICs don't support it, so
YAOG auto-downgrades to skb if the native attach fails (the link still comes up). The node editor shows
each node's native-XDP support **always** (so you can see it before choosing native), and the `mimic`
health chip shows the mode actually achieved.

**Egress interface.** By default mimic binds to the node's default-route NIC (auto-detected). On a
multi-homed / policy-routing node where the WireGuard egress isn't the default route, set the node's
**Mimic egress interface** (e.g. `wan0`) in the node editor; blank = auto-detect.

### 5.10 Configuration reference

Controller behavior is configured by environment variables on the container (set in
`docker-compose.yml`), plus a few server-stored settings edited in the panel.

| Variable | Default | What it does |
|---|---|---|
| `YAOG_BIND_ADDR` | `127.0.0.1` | Compose-only: host interface both published ports bind to. `0.0.0.0` to expose beyond loopback. |
| `YAOG_PANEL_PORT` | `8080` | Compose-only: host port the operator/panel API is published on. |
| `YAOG_AGENT_PORT` | `9090` | Compose-only: host port the agent API is published on. |
| `YAOG_CONTROLLER_STATE_DIR` | unset | Controller state directory. With `YAOG_TENANT_ID`, this is what switches controller mode **on** (the image sets `/data`). |
| `YAOG_TENANT_ID` | unset | Tenant identifier scoping all controller state (single-tenant for now). |
| `YAOG_CONTROLLER_AGENT_ADDR` | `:9090` | Listen address of the node-facing agent API. |
| `YAOG_OPERATOR_PATH_PREFIX` | empty | Optional secret path prefix for the operator API (`:8080`). |
| `YAOG_AGENT_PATH_PREFIX` | empty | Optional secret path prefix for the agent API (`:9090`), independent of the operator one; the bootstrap one-liner bakes it into the agent's URL. |
| `YAOG_PANEL_ORIGIN` | empty | Comma-separated allowlist of origins permitted credentialed cross-origin panel access (needed only for a different-origin panel; requires HTTPS). |
| `YAOG_SECURE_COOKIE` | `true` | `Secure` attribute on the session/CSRF cookies. `false` only for local non-TLS dev. |
| `YAOG_CONTROLLER_OPERATOR_TOKEN` | unset | Optional break-glass operator token (recovery path). Only its SHA-256 is kept. |
| `YAOG_BUNDLE_SIGNING_KEY` | unset | Path to an Ed25519 PKCS#8 PEM. When set, every bundle carries a detached signature and `install.sh` pins the public key; loading is fail-closed. |
| `YAOG_WEB_DIR` | unset | Directory the server serves the panel SPA from (the image sets `/app/web`). |

> **Secret path prefixes** mount the two audiences under distinct namespaces — operator under
> `/<operator-prefix>/api/v1/operator/`, agent under `/<agent-prefix>/api/v1/agent/` — so a path-based
> proxy can route each to its own port and you can expose only the agent endpoint publicly. This is
> defense-in-depth obscurity, **not** a security boundary; the bearer tokens and the keystone
> signature are the real ones. The old single `YAOG_CONTROLLER_PATH_PREFIX` is removed — the server
> refuses to start if it is still set. The panel's "Secret Path Prefix" field mirrors the **operator**
> prefix only.

Full reference: [spec/controller/](spec/controller/) (start with `controller-api.md` and `agent.md`).

---

## 6. Compiler internals

The compiler is the same in both modes — the in-browser TypeScript port is pinned byte-for-byte to the
Go implementation (`internal/compiler/compiler.go`) by the conformance gate. It is a **pure function**
of its inputs: no clock reads, no filesystem, no global state (every non-deterministic input is lifted
into the request). That purity is what makes the output reproducible and the two implementations
comparable.

### 6.1 Compilation pipeline

The compiler processes the topology in passes:

1. **Schema validation** — JSON structure: required fields, types, reference validity.
2. **Semantic validation** — logical consistency: IP conflicts, isolated nodes, illegal references,
   CIDR validity.
3. **IP allocation + capability inference + peer derivation** —
   - *IP allocator* (`internal/allocator/ip.go`): assigns overlay IPs sequentially from the Domain
     CIDR for nodes without a manual IP, skipping network/broadcast/reserved addresses.
   - *Capability inference* (`internal/compiler/roles.go`): derives capability fields from the role.
   - *Peer derivation* (`internal/compiler/peers.go`): turns edges into per-node `PeerInfo` (see
     [§6.2](#62-peer-derivation)).
4. **Config rendering** — four renderers plus deploy scripts:

   | Renderer | Output | Source |
   |----------|--------|--------|
   | WireGuard | one `.conf` per peer (or single `wg0` for clients) | `internal/renderer/wireguard.go` |
   | Babel | `babeld.conf` per node | `internal/renderer/babel.go` |
   | sysctl | `99-overlay.conf` | `internal/renderer/sysctl.go` |
   | Install script | `install.sh` | `internal/renderer/script.go` |
   | Deploy scripts | `deploy-all.sh` + `.ps1` | `internal/renderer/deploy.go` |

5. **Artifact export** (`internal/artifacts/export.go`) — organizes everything into per-node
   directories with a manifest and checksums.

### 6.2 Peer derivation

Peer derivation converts topology edges into concrete WireGuard peer configs.

- **Input → output:** Topology (nodes + edges) + key pairs → `map[nodeID][]PeerInfo`.
- **Two-pass algorithm.** Pass 1 pre-allocates per node pair: listen ports (incremental offset per
  node), transit IPs, and IPv6 link-locals, stored bidirectionally. Pass 2 re-iterates edges, looks up
  the pre-allocated resources, and builds the `PeerInfo` with the correct allocated port.
- **Endpoint resolution.** The forward peer uses the edge's `endpoint_host` + the allocated target
  port. The reverse peer uses a reverse edge (`B→A`) if one exists; otherwise it has no endpoint and
  relies on the forward side to initiate.
- **PersistentKeepalive.**

  | Condition | Keepalive |
  |-----------|-----------|
  | Node can accept inbound AND a reverse edge exists | 0 (disabled) |
  | Node behind NAT (can't accept inbound) | 25 s |
  | No reverse edge (unidirectional) | 25 s |

- **Transit IP allocation.** Each node pair gets a pair from its domain's `transit_cidr` (default
  `10.10.0.0/24`): link 0 → `10.10.0.1` ↔ `10.10.0.2`; link N → `10.10.0.(2N+1)` ↔ `10.10.0.(2N+2)`.
- **Listen port allocation.** Each node starts from `listen_port` (default 51820), gap-filling upward
  for each additional peer interface.
- **Pinned (sticky) allocations.** Once a link's ports, transit IPs, and link-locals are chosen, the
  compiler writes them back onto the edge as `pinned_*` fields and reuses them verbatim next compile.
  This keeps existing servers byte-stable when you add nodes. The reserve-pins-first-then-gap-fill
  contract and invariants are in [spec/compiler/allocation-stability.md](spec/compiler/allocation-stability.md).

### 6.3 Babel routing integration

Babel is the dynamic routing daemon that makes multi-hop overlay networks work; it runs whenever a
node's Domain has `routing_mode = "babel"`.

**Router-ID generation:** `SHA-256(node_id)` → first 6 bytes as a MAC-48; set the locally-administered
bit (`| 0x02`), clear multicast (`& 0xFE`). Stable (same node → same ID) and well-distributed; a
manual `router_id` overrides it.

**Interface declaration:** each per-peer WireGuard interface is declared as a Babel tunnel interface,
e.g. `interface wg-beta type tunnel hello-interval 4 update-interval 16`. The hello/update intervals
and the `rxcost` come from per-role Babel presets (`internal/renderer/babel_presets.go`); an edge's
`priority`/`weight` overrides the link cost.

**Redistribution** uses two mechanisms (`internal/renderer/babel.go`):

- `redistribute local ip <prefix> allow` — for prefixes backed by a `dummy0` connected route: the
  node's own overlay `/32`, and (router side) injected client `/32`s.
- `redistribute ip <prefix> allow` (no `local`) — for prefixes backed by a real kernel route that is
  not a `dummy0` connected route: `extra_prefixes` (LAN segments) and the gateway's `0.0.0.0/0`
  default. The non-`local` form is what lets these match a kernel route and propagate.

A trailing `redistribute local deny` prevents accidental advertisement of transit IPs or system
routes.

### 6.4 Key management and persistence

WireGuard keys are **persistent**, not regenerated on every compile.

- **Local / air-gap (AirGap custody).** The first compile of a new node generates a key pair and
  writes **both** keys back onto the node in the topology JSON (the private key round-trips so a
  stateless compiler can re-render the node's own `Interface PrivateKey`). Every later compile reuses
  the pair, so adding an unrelated node never rotates a key. Rotation is explicit: clear **both** key
  fields (forces fresh generation) or paste a different private key. A node carrying a public key but
  no private key is a hard error. Because the topology (and browser localStorage) carries live private
  keys, treat it as secret material.
- **Controller (AgentHeld custody).** The controller renders from **public keys only** — each node's
  `[Interface] PrivateKey =` line is rendered as a placeholder, and the agent splices its own
  locally-held private key in at install time. The controller never sees a private key. See
  [§8.3](#83-zero-knowledge-key-custody).

Full contract: [spec/data-model/node.md](spec/data-model/node.md) and
[spec/compiler/allocation-stability.md](spec/compiler/allocation-stability.md).

---

## 7. Generated artifacts

### 7.1 Bundle directory structure

Each node's deployment bundle contains everything needed to go live:

```
node-alpha/
  ├── wireguard/
  │   ├── wg-beta.conf       # WireGuard tunnel config to beta
  │   └── wg-gamma.conf      # WireGuard tunnel config to gamma
  ├── babel/
  │   └── babeld.conf        # Babel routing daemon config
  ├── sysctl/
  │   └── 99-overlay.conf    # Kernel params (forwarding, rp_filter)
  ├── install.sh             # One-click install script
  ├── manifest.json          # Build metadata and file manifest
  ├── checksums.sha256       # SHA-256 integrity verification
  └── README.txt             # Quick-start instructions
```

In controller mode, signed bundles additionally carry `bundle.sig` + `signing-pubkey.pem` (when
`YAOG_BUNDLE_SIGNING_KEY` is set) and `artifacts.json` (the self-update pins).

### 7.2 WireGuard config details

A per-peer config (server roles):

```ini
# WireGuard per-peer interface: wg-beta
# Node: node-alpha -> Peer: node-beta

[Interface]
PrivateKey = <private_key>          # placeholder in controller mode; spliced by the agent
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

A `client`-role node instead gets a single `wg0` with one peer (its upstream router/relay/gateway),
no `dummy0`, and no Babel.

Key design points:

- **`Table = off`** — wg-quick adds no kernel routes; with `AllowedIPs = 0.0.0.0/0` each interface
  would otherwise fight over the default route. Babel manages all routing.
- **`AllowedIPs = 0.0.0.0/0, ::/0`** — safe in the per-peer model (one peer per interface); Babel
  decides which tunnel to use.
- **`PostUp`/`PostDown`** — add the IPv6 link-local Babel needs for neighbor discovery.

### 7.3 Install script logic

`install.sh` is an idempotent, phased deployment:

```bash
sudo bash install.sh              # install / upgrade overlay
sudo bash install.sh --uninstall  # completely remove overlay from this node
```

**`--uninstall` / `-u`** tears everything down: stops and disables all managed and legacy WireGuard
interfaces, removes `/etc/wireguard/` configs, stops Babel and removes its configs/overrides, removes
the overlay SNAT rule and `overlay-snat.service`, restores sysctl defaults, removes `dummy0` and its
`overlay-dummy.service`, and reloads systemd.

**Normal install phases:**

- **Phase 0 — Cleanup.** Stop/remove existing WireGuard interfaces and old configs. A comprehensive
  legacy sweep scans all `wg*` interfaces and `/etc/wireguard/*.conf` and removes anything not managed
  by the current overlay (catches `wg0`, `wg1`, `wg-overlay`, leftovers). Stop Babel; remove old
  sysctl.
- **Phase 1 — Environment.** Verify checksums; check root; detect OS; install deps (`wireguard`,
  `wireguard-tools`, `babeld`); create `dummy0` + assign the overlay IP; install a systemd unit to
  persist `dummy0`; configure overlay SNAT (see [§7.4](#74-dummy0--tableoff--the-snat-fix)).
- **Phase 2 — Deploy config.** Copy WireGuard configs to `/etc/wireguard/`, Babel config to
  `/etc/babel/`, sysctl config to `/etc/sysctl.d/`.
- **Phase 3 — Activate & verify.** Apply sysctl; start each `wg-quick@<iface>`; install the babeld
  systemd override (depends on all WireGuard services); start/enable babeld; print a status summary.

When the bundle is signed (controller mode with the keystone on), the script verifies the embedded
public key against `bundle.sig` **before** running `sha256sum -c`; a missing signature on a
signed-build `install.sh` is treated as tamper and refused.

### 7.4 dummy0 + Table=off + the SNAT fix

`dummy0` hosts the stable overlay IP that Babel announces (apps and DNS always point here). Each
`wg-*` interface has `Table = off`, so Babel — not wg-quick — installs and removes kernel routes and
handles link failover.

**The source-address problem.** Each `wg-*` interface has a transit IP (e.g. `10.10.0.3/32`). When the
kernel sends to an overlay destination, Babel routes it via a `wg-*` interface and the kernel picks
the **transit IP** as the source — not the overlay IP on `dummy0`. The reply to a transit IP is
unroutable (transit IPs aren't announced), so `ping 10.111.0.3` silently fails while
`ping -I 10.111.0.2 10.111.0.3` works.

**The fix.** The installer adds an SNAT rule that rewrites the source of packets leaving a `wg-*`
interface with a transit source (`10.10.0.0/24`) to the node's overlay IP:

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

The installer auto-detects `nft` and falls back to `iptables`; a persistent `overlay-snat.service`
keeps the rule across reboots. To fix an existing deployment by hand, run the equivalent rule with the
node's overlay IP substituted for `<overlay_ip>`.

### 7.5 Auto-deploy scripts

Compilation generates two project-level deploy scripts: `deploy-all.sh` (Bash) and `deploy-all.ps1`
(PowerShell).

```bash
bash deploy-all.sh path/to/artifacts.zip            # deploy
bash deploy-all.sh --clean path/to/artifacts.zip    # wipe existing WG configs first
bash deploy-all.sh --uninstall                      # tear down the overlay on all nodes (no ZIP)
```

```powershell
.\deploy-all.ps1 -ArtifactsZip path\to\artifacts.zip
.\deploy-all.ps1 -ArtifactsZip path\to\artifacts.zip -Clean
.\deploy-all.ps1 -Uninstall
```

They extract the ZIP, then for each node with SSH details: SCP the self-extracting installer to
`/tmp/`, run `sudo bash /tmp/<node>.install.sh`, clean up, and print a success/skipped/failed summary.
Nodes without SSH config are skipped. SSH uses `ssh <alias>` if an alias is set, otherwise
`ssh -p <port> -i <key> <user>@<host>`; password auth is not supported.

### 7.6 Canvas visualization

After compilation the canvas shows: **multi-interface handles** (top = inbound, bottom = outbound),
one per per-peer interface, color-cycled, with hover tooltips (interface name, listen port, peer
name); **node info cards** with colored `<peerName>:<port>` tags matching the handles; and **edge
labels** `<source> → <target> | <endpoint>` color-coded by type (direct = cyan, public-endpoint =
amber, relay-path = violet, candidate = gray).

---

## 8. Security model

YAOG's security rests on a deliberate split: a headless controller breach must not be able to (a)
forge fleet **membership**, or (b) lift any WireGuard **private key**. Operator panel auth gates the
panel only — it is **not** the network trust anchor. The authority is
[spec/security/security.md](spec/security/security.md) and [spec/controller/](spec/controller/).

### 8.1 Two distinct signing roles (do not conflate)

| | **Off-host deploy keystone** | **Bundle-signing key** (`YAOG_BUNDLE_SIGNING_KEY`) |
|---|---|---|
| Holder | The operator's **hardware/synced passkey** — the private key never touches the server | A **server-side** Ed25519 PEM file (or air-gap export host) |
| Signs | The canonical **trust-list bytes** (who-is-trusted), via a content-bound WebAuthn assertion | Each per-node bundle's canonical `checksums.sha256` bytes |
| Threat closed | A compromised controller **alone cannot push membership** to the fleet | Authenticity/integrity of rendered bundles (given an out-of-band pin) |
| Server persists | Only the **non-secret public** operator credential (descriptor + public PEM) | **Never** the private key — only the public key, as a per-tenant pin |

Both use the same WebAuthn verifier; only the challenge differs (a content hash for the keystone, a
random nonce for login passkeys).

### 8.2 Off-host signing keystone

Changing **who is trusted** (admitting, evicting, or rekeying a node) requires a human hardware-key
signature over the *content of the change* — a hash of the canonical trust-list bytes plus a monotonic
version. The private key never leaves the authenticator; the controller persists only the non-secret
public credential, which is baked into the agent bootstrap script and passed as `--operator-cred`, so
the **node verifies the signature before applying**. Result: a headless controller breach has **no
autonomous capability** to change fleet membership. "Keystone ON vs OFF" is a deployment posture;
keystone OFF means nodes don't enforce signed membership (dev only).

### 8.3 Zero-knowledge key custody

**Guarantee:** no controller-rendered bundle ever contains a parseable WireGuard private key; the
registry stores **public keys only**. The render has two custody modes (`render.GenerateKeys`):

- **AirGap** (local / CLI default) — private keys round-trip through the topology JSON for stateless
  key stability. The topology and browser localStorage therefore carry private keys and must be
  treated as secret material.
- **AgentHeld** (controller) — `GenerateKeys` never returns a real private key; each node's
  `[Interface] PrivateKey =` is an intentionally-invalid placeholder, and the **agent splices its own
  locally-held private key** in at install time. Everything else is byte-identical to AirGap.

Enforcement is belt-and-braces: the panel **strips private keys before every `update-topology` POST**,
and the server **rejects (400)** any topology carrying a non-empty `wireguard_private_key`. Perpetual
test gates assert both.

### 8.4 Operator authentication

- **Bootstrap** — accounts are created out-of-band by `create-operator`; passwords hashed with
  argon2id (plaintext never stored or logged).
- **Login** — `POST /login` mints a 256-bit session (only its SHA-256 stored, 12h TTL) and sets an
  **httpOnly `yaog_session` cookie** that survives refresh without any token in web storage; the panel
  re-derives state from `GET /session`.
- **CSRF** — double-submit: login sets a readable `yaog_csrf` cookie + returns `csrf_token`; every
  cookie-path state change must echo it in `X-CSRF-Token` (constant-time compared). The Bearer path
  and GETs are exempt.
- **CORS** — `YAOG_PANEL_ORIGIN` is an exact-origin allowlist for credentialed cross-origin access; a
  wildcard is never sent with credentials. Same-origin Docker needs none.
- **TOTP (RFC 6238)** — stdlib HMAC-SHA1, login-only second factor; replay-protected, ±1-step drift.
  Honest limit: the secret is symmetric and stored at rest — convenience, weaker than a passkey, and
  **never** a keystone signing factor.
- **Passkeys** — a WebAuthn login credential (distinct from the keystone credential). Used as a 2FA
  factor (takes precedence over TOTP when both are registered) or for **passwordless** login; the
  challenge is a single-use, 5-minute, atomically-burned nonce. Synced passkeys (Bitwarden/iCloud/…)
  need no hardware key.
- **Break-glass token** (`YAOG_CONTROLLER_OPERATOR_TOKEN`) — an optional recovery credential that
  authenticates operator routes directly as a Bearer token and bypasses `/login` (the escape hatch
  from a per-username lockout).
- **Rate limiting** — a shared limiter reserves a slot on both `user:<name>` and `ip:<client>` for
  every login/passkey attempt (10 failures / 15 min → 429); no username oracle.

> **Transport is a hard requirement.** `/login` carries a plaintext password and the controller speaks
> plain HTTP (TLS is delegated to a reverse proxy). Production **must** front the controller with a
> TLS-terminating proxy. The plain-HTTP + keystone-OFF posture has no trust anchor and is dev-only
> (enforced by a startup warning, not a refusal).

### 8.5 Bundle signing — `YAOG_BUNDLE_SIGNING_KEY`

When set to the path of an Ed25519 PKCS#8 PEM, every per-node bundle gets a detached `bundle.sig`
(raw Ed25519 over the canonical `checksums.sha256`) + `signing-pubkey.pem`, and `install.sh` embeds
the verifying public key as a constant. Loading is **fail-closed** — a set-but-unreadable key aborts
the export rather than silently shipping unsigned. In controller mode the public key is **pinned per
tenant** with no silent downgrade: a previously-pinned key that goes missing → refuse (412); a
different key → refuse (409). Intentional rotation uses `YAOG_BUNDLE_SIGNING_KEY_ROTATE` (set for one
deploy, then unset). Keep the private key off the repo and protect it at rest (`chmod 600`,
`systemd-creds`, or an orchestrator secret store); a KMS/HSM can plug in via the `ConfigSigner` seam.

> **Honest limit.** Phase-0 signing ships the public key *inside* the bundle, so authenticity is only
> as strong as an out-of-band pin: a bundle from an untrusted source could be re-signed with a swapped
> key. The signature is genuine provenance for an operator-built air-gap bundle (you configured the
> key) and for the agent-pinned keystone path; an agent-pinned trust anchor is the longer-term design.

---

## 9. HTTP API reference

The route surface differs sharply by build (see [§3](#3-the-two-modes--the-build-boundary)).

### 9.1 Always present (both builds)

- `GET /api/health` — ungated public liveness probe (`{status:"ok", timestamp}`); GET only, CORS- and
  panic-wrapped. This is the **only** route in the default controller build's un-tagged server layer;
  everything else comes from the controller handler.

### 9.2 Air-gap-only anonymous compute (present **only** with `-tags airgap`)

```
POST /api/validate         POST /api/compile
POST /api/export           POST /api/deploy-script
```

> **Stale-doc warning.** Older docs (and earlier versions of this wiki) listed these as normal backend
> endpoints. They exist **only** in the `go build -tags airgap` local-design oracle; in the **default
> shipped controller and Docker image they return 404**. For offline compilation, use the in-browser
> generator, the `cmd/compiler` CLI, or the `-tags airgap` oracle.

### 9.3 Controller operator routes (`/api/v1/operator/...`, port `:8080`)

Behind `operatorAuth` except the unauthenticated login surface. Highlights: `login` /
`login/passkey/{begin,finish}` (unauth) / `logout` / `session`; `totp/*`, `passkey/*`;
`update-topology`, `stage`, `compile-preview`, `promote`, `topology` (+ `?version=N`,
`/topology/versions`); `nodes`, `revoke`, `audit`, `enrollment-token`, `rekey-all`, `clear-rekey`;
`settings`, `release-pins`, `release-assets`; `operator-credential`, `trustlist`,
`trustlist-signature`.

### 9.4 Controller agent routes (`/api/v1/agent/...`, port `:9090`)

Machine-to-machine JSON. `enroll` (no auth — single-use enrollment token) and `bootstrap` (no auth —
generic installer) are open; `config`, `poll`, `report`, `telemetry`, `rekey` require the per-node
bearer token. `telemetry` is observability-only (updates conditions + last-seen, never deploy custody)
and is not audited.

> **Status codes:** 200 OK; 400 bad/empty body; 405 wrong method; 413 body over the 4 MiB cap; 422
> structurally valid but fails to compile; 500 keygen/render/recovered-panic. Errors use the nested
> coded envelope `{"error":{"code","message","params"}}`. A node token on an operator route → 403; a
> revoked node → 403; missing/garbage credentials → 401.

---

## 10. Debugging & troubleshooting

### 10.1 Development environment

```bash
./dev.sh start     # Vite frontend on :5173 (+ the Go server when the controller env is set)
./dev.sh stop
./dev.sh restart
./dev.sh status
./dev.sh logs      # tail both log files
```

Logs in the project root: `.dev-backend.log` (Go), `.dev-frontend.log` (Vite). For pure local design
you only need `npm run dev` in `frontend/`.

### 10.2 Local-mode issues

**Compilation fails.** Compilation runs in the browser — read the error in the bottom bar and the
DevTools console. Common causes: no domain defined, a node not assigned to a domain, an invalid CIDR,
an isolated node (no edges).

**Nodes overlap on the canvas.** Drag them apart (positions persist within the session); a refresh
resets to the default grid.

**WireGuard interface won't start.**

```bash
wg show                              # all interfaces
wg show wg-beta                      # one interface
sudo wg-quick up wg-beta             # start manually
cat /etc/wireguard/wg-beta.conf      # inspect the config
systemctl status wg-quick@wg-beta    # service status
```

**Babel routes not working.**

```bash
systemctl status babeld
echo "dump" | nc ::1 33123           # dump the Babel routing table
journalctl -u babeld -f
ip route show table main | grep -E "^10\."
ip addr show dummy0                  # verify the overlay address
```

**Install script fails.**

```bash
sudo bash -x install.sh                       # verbose
cd /path/to/node-dir && sha256sum -c checksums.sha256
sudo wg-quick down wg-beta 2>/dev/null && sudo bash install.sh
```

**Network checks.**

```bash
ping -c 3 10.11.0.2                  # overlay connectivity
ping -c 3 10.10.0.2                  # transit IP (tunnel)
sudo wg show all | grep -A5 "latest handshake"
ping -M do -s 1392 10.11.0.2         # MTU
sudo tcpdump -i eth0 udp port 51820  # WireGuard UDP
```

### 10.3 Controller-mode issues

**`yaog-server` exits immediately.** The default build is controller-only; set both
`YAOG_CONTROLLER_STATE_DIR` and `YAOG_TENANT_ID` (Docker does this). Without them it fails loud by
design — it does not fall back to an anonymous compute server.

**`/api/validate` or `/api/compile` returns 404.** Expected on the shipped controller — those routes
are air-gap-build-only. Use the in-browser generator or the `cmd/compiler` CLI. (In controller mode,
Validate runs in the browser and Compile runs server-side via the operator-gated Deploy/preview path.)

**Passkey enrollment fails with "invalid domain."** You're on `http://127.0.0.1`; use the hostname
`localhost` (WebAuthn forbids IP-address domains), or front the controller with TLS for remote access.

**Login doesn't persist / cross-origin panel can't log in.** The session is an httpOnly cookie that
needs `Secure` over a non-localhost origin — set `YAOG_SECURE_COOKIE=true` behind TLS, and set
`YAOG_PANEL_ORIGIN` for a different-origin panel.

**A node shows `wireguard: LinkDown` / `SomePeersDown`.** Open the **WireGuard links** panel on the
node-detail page to see which peer is down and its last handshake. `SomePeersDown` means some (not
all) links are down — Babel routes around them; `LinkDown` means no peer has handshaked yet. On the
node: `sudo wg show all | grep -A5 handshake` and `journalctl -u yaog-agent -f`.

**A self-update is stuck.** A `selfupdate: Blocked` condition carries an actionable reason (often
"re-arm the rollout so its pins point at the target build"). The controller refuses a target newer
than itself; check the agent log with `journalctl -u yaog-agent -f`.

**Agent health checks.**

```bash
curl http://localhost:8080/api/health        # controller liveness
systemctl status yaog-agent                   # agent daemon
journalctl -u yaog-agent -f                    # agent logs (poll/verify/apply/self-update)
cat /etc/wireguard/agent-controller.token      # per-node bearer token (mode 0600)
```

---

## 11. Glossary

| Term | Meaning |
|------|---------|
| **Overlay IP** | A node's stable identity address on `dummy0`, announced by Babel. |
| **Transit IP** | A per-link point-to-point address on a `wg-*` interface; never announced. |
| **Per-peer interface** | A dedicated `wg-<peer>` WireGuard interface per neighbor (vs a single `wg0`). |
| **Domain** | An overlay address space (CIDR) with an allocation and routing mode. |
| **Generation** | The controller's monotonic deploy counter; bumped on each promote. |
| **Stage / Promote** | Stage renders bundles invisibly at `gen+1`; promote flips them to current and bumps the generation. |
| **Enrolled subgraph** | The approved, keyed nodes (and edges between them) the controller actually renders. |
| **Keystone** | The operator's off-host hardware key that signs trust-list/membership changes. |
| **Node Condition** | A structured `{type,status,reason,message}` health item (`configapply`/`selfupdate`/`wireguard`/`mimic`). |
| **AirGap vs AgentHeld** | Key-custody modes: private keys in the topology (local) vs held only by the node (controller). |
| **mimic** | An eBPF UDP→fake-TCP shaper wrapping a link for UDP-hostile networks (transport `tcp`). |

---

> **Spec cross-reference.** This wiki narrates the system; the normative details live in
> [`docs/spec/`](spec/) — `overview/`, `data-model/`, `roles/`, `compiler/`, `artifacts/`, `api/`,
> `frontend/`, `operations/`, `security/`, and `controller/`. Start with [spec/README.md](spec/README.md).
