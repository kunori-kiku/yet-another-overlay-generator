# Yet Another Overlay Generator (YAOG)

Yet Another Overlay Generator is a robust, web-based control plane and code generator designed for declarative configuration of modern overlay networks. It seamlessly orchestrates **WireGuard** (for Layer 3 cryptographic tunnels) and **Babel** (for dynamic routing) to create complex mesh, hub-and-spoke, or hybrid topologies with ease.

## Features

- **Visual Topology Builder:** Drag-and-drop React Flow interface to design your network nodes and connect their links.
- **Smart Validation:** Early-fail checks catch logical errors such as missing public IPs, broken NAT requirements, and dangling isolated nodes.
- **Automatic Cryptographic Key Management:** Generates and distributes secure `wg` keys for your active topology automatically (during compilation).
- **Offline Configuration Bundles:** One-click deployment bundle generation — download portable `.zip` archives containing safe Bash installation scripts, sysctl modifications, Babel daemons, and WireGuard interfaces.
- **Immutable Artifacts:** Generated scripts hash and verify checksums (`sha256`) explicitly mitigating tamper attacks.

## Getting Started

### Prerequisites

- Go `1.21+`
- Node.js `v18+` (LTS recommended)

### 1. Running the Backend Server

The backend generates configurations based on REST API requests and handles compilation logic.

```bash
# From the project root
go run ./cmd/server/main.go
```

The server will begin listening on `:8080`.

### 2. Running the Frontend Dev Server

The frontend provides the interactive Topology Canvas and History/Audit UI.

```bash
# Navigate to frontend folder
cd frontend

# Install dependencies (use legacy-peer-deps if Vite/Tailwind clashes occur)
npm install --legacy-peer-deps

# Start the development server
npm run dev
```

Visit `http://localhost:5174` (or whatever Vite outputs) in your browser.

## Basic Usage Guide

1. **Add Domains:** Open exploring panels, and add a logical IP Domain (e.g., `10.10.0.0/24`) with an intuitive name. Set allocation mode to Automatic.
2. **Add Nodes:** Drop nodes onto the Topology Canvas via the Right Panel. Make sure to define their Roles (Peer for standard endpoints, Hub/Router for traffic management) and tag their actual network capabilities (e.g., `Has Public IP` / `Can Accept Inbound`).
3. **Draw Edges:** Connect nodes by dragging wires between them. Edges dictate connection direction geometry. *(Note: You don't need to manually configure reverse paths. If NAT-Node `A` connects to Public-Node `B`, the generator automatically sets up complementary endpoints and keepalive tunnels!)*.
4. **Compile & Export:** Hit `Compile` to allocate dynamic IPs and deduce WireGuard config properties. If everything validates successfully, hit `Export` to generate structural Zip deployment packages for all target systems.

## Documentation

- Check out our [Terminology Wiki](docs/wiki.md) for in-depth insights into Overlay networking definitions used within this generator.
