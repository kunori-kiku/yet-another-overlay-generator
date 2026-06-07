# Project Overview

**YAOG** (Yet Another Overlay Generator) is a declarative control plane and code generator for
overlay networks. It provides a web-based visual topology builder backed by a Go compilation
engine that orchestrates **WireGuard** (Layer 3 cryptographic tunnels) and **Babel** (dynamic mesh
routing) to produce ready-to-deploy configuration bundles.

The key design principle — per-peer WireGuard interfaces — is described in
[design-principles.md](design-principles.md).

## Technology Stack

| Layer | Technology |
|---|---|
| Backend API | Go (stdlib `net/http`, no framework) |
| Frontend | React 18 + TypeScript + Vite |
| UI Canvas | React Flow (node/edge graph editor) |
| State Management | Zustand (with `persist` middleware → localStorage) |
| Internationalization | Custom `i18n.ts` (EN/ZH) |
| Crypto | `golang.zx2c4.com/wireguard/wgctrl` (WireGuard key generation) |
| CI/CD | GitHub Actions (multi-platform release workflow) |

## Prerequisites

- **Go** 1.21+ (module declares 1.25.0)
- **Node.js** v18+ LTS
- Git
