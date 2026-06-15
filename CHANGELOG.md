# Changelog

All notable changes to YAOG (Yet Another Overlay Generator) are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Pre-1.0 `v2.0.0` is currently in a `preview → beta → rc → GA` ramp; see
[`RELEASING.md`](RELEASING.md) for the release process and tag conventions.

## [Unreleased]

_Nothing yet._

## [2.0.0-beta.2] - 2026-06-16

The second beta (set as the GitHub *latest* release): closes the
`signed-self-update-and-rc-hardening` subject by shipping the **signed agent self-update
swap** + **canary-then-fleet rollout** — the mutable half whose observable half (version
reporting) shipped in beta.1.

### Added
- **Signed agent self-update (canary-then-fleet).** A controller-managed agent replaces its own
  binary with the version pinned in the bundle's controller-signed, keystone-bound
  `artifacts.json` — verified against the signed SHA-256 pin BEFORE exec (never the upstream
  `.sha256` sidecar), self-tested, then atomically swapped (install-then-flip, so the binary is
  never absent) and re-exec'd. It never downgrades below the health-confirmed `AgentVersionFloor`,
  and a bad swap **cannot brick a node**: a crash-durable breadcrumb is reconciled on every boot
  (two-phase — an early attempt-bump bounds even an early-init crash; a probationary promote that
  finalizes only after one clean daemon cycle; rollback to the prior binary + abandon at the
  attempt cap), bounding the systemd restart loop with no unit-file change.
- **Canary rollout controls.** `ControllerSettings` gains `TargetAgentVersion` (empty ⇒ no
  self-update — the safety contract), `MinAgentVersion` (forces an update before applying an
  incompatible bundle), `AgentBins` (`linux-<arch>` → {asset, sha256}, strict-validated at POST),
  `AgentCanaryNodeIDs`, and `AgentRolloutFleetWide` (the promote-to-fleet action). The
  `artifacts.json` agent block is **per-node** (only rollout nodes), so a bad target is caught on
  the canary subset before it reaches the fleet; air-gap byte-identity holds (no rollout ⇒ no
  block). `PRINCIPLES.md` gains the HIGH self-update custody invariant; see
  `docs/spec/controller/agent-selfupdate.md`.
- The agent `run` gains `--gh-proxy` (baked into the bootstrap systemd unit) for self-update
  downloads through a GitHub mirror.

### Notes
- **Owed self-update field smoke (owner-accepted risk).** The end-to-end field smoke (publish a
  canary agent version → download/verify/swap/re-exec → badge flips → promote to fleet; a tampered
  hash refused keep-last-good; a crashing binary rolls back within the attempt cap) requires a live
  two-node fleet and could not run in this environment. Recorded **owed** per `RELEASING.md`. The
  mechanism is extensively unit-tested (decision table, custody hash-mismatch/self-test refusal,
  probation/finalize/rollback/abandon, in-flight guard) and was hardened across a deep review +
  two re-reviews; rc.1 remains a later owner call once this smoke and the two owed beta.1 hardware
  smokes pass and beta soak is clean.

PR #117.

## [2.0.0-beta.1] - 2026-06-16

The first RC-candidate beta (set as the GitHub *latest* release): feature-complete for the
beta.1 milestone of the `signed-self-update-and-rc-hardening` subject — mimic-from-GitHub
install, agent version *reporting*, full input validation, controller-mode UX & resilience,
and the RC legal/process paperwork. The signed self-update *swap* itself lands in beta.2.

### Added
- **mimic GitHub-`.deb` install** (plan-3/7). When mimic is not in the node's distro repos
  (Debian 12 / Ubuntu 24.04), `install.sh` falls back to a **SHA-256-pinned `.deb` from
  GitHub**: distro-first → pinned download → `sha256sum -c` verify → `apt-get install`, failing
  closed under `set -euo pipefail`. The pin lives in a new signed `artifacts.json` bundle member
  (pin ∈ `artifacts.json` ∈ `bundleFiles` ∈ `checksums.sha256` ∈ `bundle.sig` ∈ keystone
  trust-list — no new trust primitive). Air-gap / local mode supplies the catalog via
  `YAOG_ARTIFACT_CATALOG` (+ `YAOG_GITHUB_PROXY` / `YAOG_MIMIC_VERSION`) or `cmd/compiler`
  `--artifact-catalog` / `--gh-proxy` / `--mimic-version`. With no catalog, `artifacts.json` is
  omitted and the bundle stays **byte-identical** to before (D4).
- **Agent version reporting + build-version injection** (plan-4). `release.yml` and the Docker
  image stamp the release tag via `-X main.BuildVersion=<tag>`; `yaog-server|compiler|agent
  version` print it; the agent reports its version on `/report` and the panel shows each node's
  reported version. Per-arch standalone agent binaries + `.sha256` sidecars are published.
- **Controller-mode UX & resilience** (plan-5). Every controller error is localized through the
  shared `localizeError` (no raw `<status> <JSON>` reaches the operator); a React `ErrorBoundary`
  replaces the white-screen crash; revoke is confirmation-gated.
- **Backend robustness & full input validation** (plan-6). New schema gates for `transit_cidr`
  (IPv4-only, /8–/30) and the `endpoint_host` / `public_endpoints[].host` charset; a per-IP
  `/enroll` brute-force throttle; **graceful SIGTERM/SIGINT shutdown** draining both listeners
  (long-polls cancelled at once); a **bounded, append-only audit log** (JSONL + amortized
  rotation, no full-file rewrite per append); a Docker `HEALTHCHECK` on `/api/health`; a
  topology node/edge **count bound**; and a schema-version **forward-compat guard** (reject a
  topology stamped by a newer YAOG).
- Apache-2.0 `LICENSE` + `NOTICE`, `CHANGELOG.md`, and `RELEASING.md` — the RC legal/process
  paperwork (plan-1).
- CI `gofmt` gate over `./cmd ./internal` (repo-wide format cleanliness, fails on drift).

### Changed
- **`render.FetchSettings`** threaded through the single shared render path (plan-2) as the typed
  channel for install-time fetch pins; the zero value is byte-identical to the prior output (the
  perpetual equivalence/signing gates enforce it).
- `docs/spec/compiler/validation.md` made honest: every coverage-table row whose validator
  already ships on `main` (including `mtu`, `router_id`, `extra_prefixes[]`, the `ssh_*`
  charset/port checks, `routing_mode`, `route_policies`, and the edge `role`/`transport` rows)
  flipped from `none-yet`/`planned` to its validating pass; the three genuinely-missing rows
  (`transit_cidr`, `public_endpoints[].host`, `endpoint_host` charset) closed by plan-6. The
  mimic and persistence specs document the GitHub-`.deb` trust chain and the bounded audit log.
- `README.md` toolchain versions corrected (Go `1.25+`, Node `v20+`); "Full documentation" links
  point at `docs/spec/`; both wikis carry an air-gap-scope banner.

### Notes
- **Owed hardware smokes (owner-accepted risk).** The three beta.1 manual smokes — two-node
  controller WebAuthn login/hydration, NAT sticky-pin deploy round-trip, and the mimic
  GitHub-`.deb` install on a real kernel-≥6.1 Debian host — gate the *tag*, not code-merge, and
  could not run in this environment (no two-node hardware / browser authenticator / real host).
  They are recorded **owed** per `RELEASING.md`; rc.1 remains a later owner call once they pass.

PRs #109–#115.

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

### Fixed
- mimic `xdp_mode` override (`skb`/`native`) for VPS NIC compatibility.

## [1.3.1] - 2026-06-07

### Added
- `transport: "tcp"` edges wrapped with mimic (eBPF UDP-over-fake-TCP) for
  UDP-hostile networks.

## [1.3.0] - 2026-06-07

### Added
- Parallel links + Babel failover; audit remediation + allocation-stability hardening.

## [1.2.0] - 2026-03-26

### Added
- Per-peer WireGuard interface model; SSH deploy scripts (bash + PowerShell) with
  `--uninstall`; self-extracting installers.

## [1.0.0] - 2026-03-16

- Initial release: visual topology design → WireGuard + Babel config generation.

[Unreleased]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.2...HEAD
[2.0.0-beta.2]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.1...v2.0.0-beta.2
[2.0.0-beta.1]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-preview.10...v2.0.0-beta.1
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
[1.2.0]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v1.0.0-release...v1.2.0
[1.0.0]: https://github.com/kunori-kiku/yet-another-overlay-generator/releases/tag/v1.0.0-release
