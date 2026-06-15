# Changelog

All notable changes to YAOG (Yet Another Overlay Generator) are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Pre-1.0 `v2.0.0` is currently in a `preview → beta → rc → GA` ramp; see
[`RELEASING.md`](RELEASING.md) for the release process and tag conventions.

## [Unreleased]

Work accumulating toward **`v2.0.0-beta.1`** (the first RC-candidate beta):

### Added
- Apache-2.0 `LICENSE` + `NOTICE`, `CHANGELOG.md`, and `RELEASING.md` — the
  RC legal/process paperwork.
- CI `gofmt` gate over `./cmd ./internal` (repo-wide format cleanliness, fails on drift).

### Changed
- `docs/spec/compiler/validation.md` made honest: validator rows already shipped on
  `main` (`mtu`, `router_id`, `extra_prefixes[]`, the `ssh_*` charset/port checks,
  `routing_mode`, `route_policies` reject-if-non-empty) flipped from `none-yet` to
  their validating pass; stale audit-era "Closed by Plan N" references removed.
- `README.md` toolchain versions corrected (Go `1.25+`, Node `v20+`); "Full
  documentation" links point at `docs/spec/`; both wikis carry an air-gap-scope banner.

## [2.0.0-preview.10] - 2026-06-15

### Added
- **Controller-mode NAT customization.** Server-authoritative *Compile* preview
  (zero-knowledge placeholder keys, no persist/stage side effects) shows the
  authoritative per-edge allocation before deploy. Sticky, operator-settable NAT
  values per edge — external endpoint, internal listen port, transit IP — persist
  verbatim through Compile → adjust → Save → Deploy with no drift or clobber.
- Directional NAT readout in the edge editor (targets the to-node at its pinned port).

### Changed
- Allocation pin floor relaxed to **1024** so port-restricted NAT VPSes (fixed
  forward ranges) work.
- Per-node `listen_port` removed in favor of a uniform 51820 auto-allocation base,
  which also fixes the always-firing co-hosted port-overlap validator that blocked
  multi-node-per-host deploys.

### Fixed
- Post-deploy pin reconciliation + non-clobbering Save (fixes NAT-forward drift).
- docker-compose path-prefix env fix (#97).

PRs #97–#106.

## [2.0.0-preview.9] - 2026-06-15

### Added
- Coded-at-source HTTP error envelope (`internal/apierr`) and a validator-finding
  localizer: validation findings carry `Code`+`Params` on the 200 `ValidateResponse`,
  panel-localized via `tValidationError` (English-default).

### Changed
- Post-audit security / robustness / mode-boundary hardening and key-custody
  remediation across the controller surface.

PRs #79–#96.

## [2.0.0-preview.8] - 2026-06-14

### Added
- **Extensible keyed i18n.** Messages are keyed and structurally enforced EN/ZH
  lockstep (perpetual CJK / bijection gates); deploy artifacts Englishized.

PRs #68–#78.

## [2.0.0-preview.7] - 2026-06-13

### Changed
- **Breaking — API namespace split by audience:** operator/panel under
  `/api/v1/operator/*` (`:8080`), agent/node under `/api/v1/agent/*` (`:9090`).
  Enrolled agents must re-bootstrap onto the new agent path.

### Security
- The controller login gate is now a real data boundary: in controller mode the
  server-held design is never persisted at rest and is flushed on logout / failed
  session restore; "switch to local" wipes server-derived data while preserving
  un-synced local work.

### Fixed
- Passkey button no longer dead-disabled before a username is typed; browser tab
  title set to "Yet Another Overlay Generator (YAOG)".

PRs #66, #67.

## [2.0.0-preview.6] - 2026-06-13

### Changed
- **Breaking — controller mode is server-authoritative end-to-end.** The stored
  design is the single source of truth; the browser cache is a disposable mirror.
  `YAOG_CONTROLLER_PATH_PREFIX` split into `YAOG_OPERATOR_PATH_PREFIX` +
  `YAOG_AGENT_PATH_PREFIX` (server refuses to start on the old env). Login is now a
  full-page gate. See `docs/MIGRATION-controller-server-authority.md`.

### Added
- Server-authoritative cache with pre-hydration backup on divergent overwrite;
  topology version history (last 10, recoverable); shrink/empty deploy guard;
  identity reconciliation (one pubkey ↔ one node-id, enforced on enroll + rekey).

### Security
- Enforced zero-knowledge key custody: `POST /update-topology` rejects payloads
  carrying a WireGuard private key; controller→local switch purges keys, pins, history.

PRs #59–#65.

## [2.0.0-preview.5] - 2026-06-12

### Fixed
- `crypto.randomUUID is not a function` when the panel is served over plain HTTP on
  a non-localhost address: all client-side ID generation falls back to a UUIDv4 built
  on `crypto.getRandomValues`.

## [2.0.0-preview.4] - 2026-06-09

### Added
- Operator panel redesigned as a dashboard app-shell (react-router, Shell, routes,
  selection aside, Tailwind auto dark/light); httpOnly-cookie refresh-surviving login
  with CSRF + credentialed CORS. PRs #53–#58.

## [2.0.0-preview.3] - 2026-06-08

### Added
- Controller-panel operator authentication: password login, TOTP 2FA, and WebAuthn
  passkey login; signing-at-rest; docker-compose loopback-bind. (Single-tenant preview.)

## [2.0.0-preview.2] - 2026-06-08

### Added
- Off-host signing keystone: operator-held hardware passkey signs the canonical
  trust-list manifest; pinned promote requires a valid signature. (Single-tenant preview.)

## [2.0.0-preview.1] - 2026-06-08

### Added
- Controller panel (single-tenant preview): a long-lived control plane from which
  enrolled node agents pull keystone-signed configs.

## [1.4.0] - 2026-06-08

### Added
- Signed bundles (Ed25519), zero-knowledge key custody, and the node agent
  (enroll → poll → verify → anti-rollback → splice key → apply).

## [1.3.2] - 2026-06-07
## [1.3.1] - 2026-06-07
## [1.3.0] - 2026-06-07

### Added
- `transport: "tcp"` edges wrapped with mimic (eBPF UDP-over-fake-TCP) for
  UDP-hostile networks; parallel links + Babel failover.

## [1.2.0] - 2026-03-26

### Added
- Per-peer WireGuard interface model; SSH deploy scripts (bash + PowerShell) with
  `--uninstall`; self-extracting installers.

## [1.0.0] - 2026-03-16

- Initial release: visual topology design → WireGuard + Babel config generation.

[Unreleased]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-preview.10...HEAD
[2.0.0-preview.10]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-preview.9...v2.0.0-preview.10
[2.0.0-preview.9]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-preview.8...v2.0.0-preview.9
[2.0.0-preview.8]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-preview.7...v2.0.0-preview.8
[2.0.0-preview.7]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-preview.6...v2.0.0-preview.7
[2.0.0-preview.6]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-preview.5...v2.0.0-preview.6
[2.0.0-preview.5]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-preview.4...v2.0.0-preview.5
[2.0.0-preview.4]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-preview.3...v2.0.0-preview.4
[2.0.0-preview.3]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-preview.2...v2.0.0-preview.3
[2.0.0-preview.2]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-preview.1...v2.0.0-preview.2
[2.0.0-preview.1]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v1.4.0...v2.0.0-preview.1
[1.4.0]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v1.3.2...v1.4.0
[1.3.2]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v1.3.1...v1.3.2
[1.3.1]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v1.3.0...v1.3.1
[1.3.0]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v1.0.0...v1.2.0
[1.0.0]: https://github.com/kunori-kiku/yet-another-overlay-generator/releases/tag/v1.0.0-release
