# Changelog

All notable changes to YAOG (Yet Another Overlay Generator) are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Pre-1.0 `v2.0.0` is currently in a `preview → beta → rc → GA` ramp; see
[`RELEASING.md`](RELEASING.md) for the release process and tag conventions.

## [Unreleased]

## [2.0.0-beta.8] - 2026-06-18

### Security
- **Enrollment-lifecycle hardening.** Three related node-enrollment weaknesses are closed:
  - A **revoked node-id can no longer be resurrected** by a still-valid enrollment token: enroll now
    refuses a revoked node-id (`enroll_node_revoked`, HTTP 409) and the refusal is audited.
  - **Revoking a node purges its outstanding enrollment tokens**, so a leaked or unused token cannot
    re-enroll it after eviction (defense in depth with the revoked-id guard above).
  - A **re-enroll over an existing approved node** (a legitimate reinstall) stays allowed but now records
    a distinct `enroll-reenroll-approved` audit action, so the WireGuard-key + bearer-token overwrite is
    never silent.
  - **Enrollment-token TTL is capped server-side** (7 days); an over-cap TTL is rejected, bounding the
    window in which a minted token is a standing node-bring-up capability.

### Fixed
- **Panel could not keystone-sign on Deploy after a session refresh (keystone-ON tenants).** The
  `getTrustlist` request was the one authenticated route bypassing the shared request helper, so it
  carried no session cookie or CSRF token — a cookie-only operator (e.g. after a page refresh, with no
  in-memory bearer) got a 401 and the off-host signing ceremony could not start. It now goes through the
  shared helper like every other authenticated call.
- **Panic recovery now covers the operator and agent (fleet) endpoints.** Top-level panic recovery was
  wrapping only the air-gap compute routes; a panic in any operator- or agent-facing handler tore the
  connection instead of returning a coded 500. Both controller muxes are now wrapped, so a handler panic
  degrades to a clean 5xx in the fleet-bearing mode too.
- **`babeld.conf` is now byte-stable under a benign edge reorder.** A node's Babel interface and
  client-route lines were emitted in topology edge-array order, so merely reordering edges changed an
  otherwise-unchanged node's config hash / bundle digest — eroding the incremental-deploy byte-stability
  the pin/reserve/heal machinery exists to protect. The lines are now ordered by the stable per-link
  interface name, depending only on link identity. (A one-time re-deploy may re-hash affected nodes as
  they converge to the canonical order.)

## [2.0.0-beta.7] - 2026-06-17

Fixes the cross-subgraph allocation-pin collision ("pin occupied by two different links") at its root
— subgraph compiles now reserve the pins held by not-yet-enrolled edges, and a normalize pass cleans
any existing corruption on save, on deploy, and on canvas load — plus controller-mode design
Export/Import and an edge-inspector port-label clarification.

### Added
- **Design Export / Import in controller mode.** The top-bar Export/Import buttons now appear in
  controller mode too (previously local-only). Export downloads the current design as a JSON backup.
  Import is server-authoritative: the file is parsed, all key material is dropped (the controller is
  key-authoritative), it is written to the controller as a new retained topology version (where the
  server heals any colliding pins), and the canvas re-hydrates from the server — so fleet endpoint
  data is never persisted to `localStorage` and the import never auto-deploys (Deploy stays a
  separate step). Flush remains local-only.

### Fixed
- **"Port/transit/link-local pin occupied by two different links" — root cause + cleanup.** During
  incremental enrollment the controller compiles only the enrolled subgraph, dropping edges whose far
  end is not enrolled yet; the allocator's gap-fill then restarted from the bottom of each pool
  without seeing those dropped edges' pins, so across successive enrollments two edges that were never
  compiled together could be handed the same transit IP / listen port / link-local — persisted back as
  a cross-link collision that `validate` reported (and that blocked a full-graph compile) while
  incremental deploys appeared fine.
  - **Prevent:** subgraph compiles now reserve the allocation pins held by every edge *outside* the
    subgraph (into both endpoint domains' transit pools), so a new node's links never re-use a live
    link's resource. Full (air-gap) compiles are unaffected — byte-for-byte identical allocations.
  - **Clean:** a new normalize pass strips the colliding edge's pins so it re-allocates fresh. It runs
    on the controller's topology write path (every save/import is stored collision-free), at the start
    of a deploy/stage (an already-corrupt fleet self-heals on the next deploy, no manual re-save), and
    on every panel canvas load/hydrate (a stale design validates cleanly without hand-fixing edges).
    It is the exact inverse of the validator's cross-link dedup — it never touches a primary link's
    legitimately-mirrored reverse edge.

### Changed
- The edge inspector's compiled "Local ListenPort" line now names the node it belongs to, so a
  per-peer link's two distinct per-end listen ports (e.g. the local node's vs the NAT-forward target's)
  no longer read as a contradiction. Display-only.

## [2.0.0-beta.6] - 2026-06-17

Fleet/keystone operability, from live fleet operation: a stuck key rotation can be released without
evicting the node, the Deploy gate is advisory instead of a hard block, an edge role flip no longer
corrupts allocation pins (with a load-time auto-heal for existing topologies), the fleet view reflects
server truth without a re-login, and a re-bootstrap restarts the agent so its new credential takes
effect.

### Added
- **Release a stuck key rotation without evicting the node.** A fleet-wide "Roll keys" only completes
  once every node re-registers; a dead/offline straggler used to wedge the panel's Deploy gate with
  no way out but to *revoke* (evict) it. New operator endpoint `POST {operator}/clear-rekey {node_id}`
  clears a node's pending rekey flag while preserving its approval and API token (idempotent, audited),
  surfaced as a per-node **"Cancel rekey"** button in the registry.

### Changed
- **The Deploy button is advisory, not hard-blocked, while nodes are rekeying.** Deploy is the step
  that *completes* a rotation, so blocking it on a still-rekeying fleet was backwards (and a single
  offline straggler wedged every deploy). Deploy now stays enabled and routes through an advisory
  confirm — a node that hasn't re-registered deploys with its current key and re-rotates later, or you
  "Cancel rekey" a node that never will.
- **Bootstrap re-pins the operator credential by default.** The one-shot node bootstrap now overwrites
  an existing `/etc/wireguard/operator-cred.pem` with the script's baked credential instead of
  refusing when it differs. The bootstrap runs as root and is fetched fresh from the controller, so
  its baked credential is the current pinned keystone — refusing bought no security (root can rewrite
  the file directly) and only blocked a legitimate re-provision. The overwrite is still LOUD: a
  differing credential logs a NOTICE (so a stale script silently downgrading the pin stays visible)
  and points at `yaog-agent reprovision-keystone` for the if-that-was-wrong case.

### Fixed
- **Edge role change no longer corrupts allocation pins.** Flipping an edge primary↔backup re-keys its
  link identity, but the editor cleared only `compiled_port` — so a primary flipped to backup kept the
  primary's transit IPs / port / link-locals and collided with its sibling, which validation reported
  as "pin occupied by two different links" (while deploy silently tolerated it). The role change now
  clears all six `pinned_*` fields too, so the edge re-allocates fresh; a load-time migration
  auto-heals existing topologies (a backup whose pins collide with a same-pair primary is stripped and
  re-allocated; a backup with its own distinct pins is untouched).
- **The fleet view reflects server truth without a re-login.** The node registry showed only the
  persisted cache until a manual re-login, and the "Live" toggle did nothing (its poll was gated on an
  in-memory `loggedIn` lost on reload, with a 20s-delayed first tick). The Fleet/Deploy pages now
  refresh on auth (on mount and when the cookie session is restored), and enabling "Live" refreshes
  immediately.
- **Bootstrap restarts the agent so a re-bootstrap actually takes effect.** The installer set up the
  daemon with `systemctl enable --now`, which only *starts* a stopped unit — on an already-running
  agent it was a no-op, so a re-bootstrap wrote a new bearer token + operator credential to disk but
  the live daemon kept the OLD ones in memory (it reads them only at startup), leaving the node
  stuck (e.g. a `req_bearer_required` 401 poll loop). It now `systemctl restart`s the unit, which
  starts a stopped daemon and restarts a running one, so a re-bootstrap is always picked up. Cost:
  the restart re-applies the current bundle once on startup — a brief keep-last-good per-interface
  WireGuard/Babel flap, identical to the existing `Restart=always` crash/reboot re-apply. (Once-off
  `--once` installs are unaffected.)

## [2.0.0-beta.5] - 2026-06-17

Keystone-rotation safety: rotating the off-host operator credential no longer silently strands the
fleet, and a family of adjacent trust-list-serving bugs found by a new adversarial regression suite
are fixed.

### Fixed
- **Keystone rotation no longer silently strands the fleet.** Re-pinning a *different* operator
  credential used to leave every enrolled node refusing the served bundle with no signal and no
  recovery path: `agent enroll` never re-pinned the node's `/etc/wireguard/operator-cred.pem` (only a
  fresh bootstrap did), and re-pinning the controller credential did not refresh what `/config`
  served. Now a changed credential requires an explicit acknowledged rotation
  (`CodeKeystoneRotationRequiresAck`, 409), the controller surfaces a server-truth
  **`redeploy_required`** signal (panel reads it instead of a browser-local cache), and the agent
  gains **`yaog-agent reprovision-keystone`** to re-pin the new public key out of band and restart
  (PRs #129/#130/#131).
- **A mid-deploy re-stage no longer bricks `/config`.** The signed membership trust-list lived in a
  single slot that staging (unsigned) and signing both wrote and `/config` served, so a fresh
  `CompileAndStage` blanked the served signature until the next promote — every node's membership
  gate refused and `/config` 500'd while a perfectly good promoted bundle was still current. The
  trust-list is now split into a **staged** slot (the in-flight, to-be-signed manifest) and a
  **served** slot (advanced only by a signed promote); `/config` and the redeploy signal read the
  served slot, so a half-finished deploy is invisible to the fleet.
- **The agent anti-rollback floor is monotonic across a keystone-OFF run.** A keystone-OFF apply
  reports membership epoch 0, which previously reset a node's floor to 0 — letting a replayed
  older-but-validly-signed membership be accepted once the keystone was re-enabled. A successful
  apply now persists `max(applied, prior)`.
- **`/config` reads the bundle and served trust-list under one store lock** (`GetServedConfig`), so a
  concurrent promote can never hand a node a torn (old-bundle, new-manifest) pair that would
  spuriously fail its offline bundle-digest binding.

### Changed
- **`keystone_no_signed_manifest` is now 409, not 500.** Keystone-on with nothing yet signed +
  promoted under it is an operator-actionable state (sign and promote a deploy), not a server fault;
  nodes keep their current config and retry. `/config` fails closed (no partial trust-list served).

### Internal
- **Non-release adversarial regression suite** (`internal/regression`, test-only — never compiled
  into a release binary): black-box drives the real controller↔agent keystone/membership path
  end to end (rotation across a mixed fleet, anti-rollback across a rotation, algorithm confusion,
  bundle-signing-anchor × keystone composition, revoke-driven membership, re-stage-doesn't-brick,
  and an atomic-served-config concurrency probe under `-race`). Each fix above is pinned by a test
  verified load-bearing (it fails when the fix is reverted). The work was reviewed → fixed →
  re-reviewed by independent multi-agent workflows.

## [2.0.0-beta.4] - 2026-06-16

A security hardening fix: the controller no longer silently ships **unsigned** bundles when a
previously-signed fleet's signing key goes missing (or changes) across a redeploy.

### Security
- **Persisted bundle-signing anchor — no silent downgrade.** `YAOG_BUNDLE_SIGNING_KEY` is read
  fresh at export time, so a controller redeploy that dropped or swapped it silently reverted a
  signed fleet to hash-only (or to a different key) with no signal. The controller now pins the
  signing **public** key per tenant (the private key stays off-host, env-referenced) and reconciles
  it at stage time: trust-on-first-use on the first signed stage; a **missing** key on a pinned
  fleet fails loud (`signing_key_missing`, 412); a **changed** key fails loud (`signing_key_mismatch`,
  409); intentional rotation/recovery via `YAOG_BUNDLE_SIGNING_KEY_ROTATE` (one deploy, then unset).
  Pin and rotate are recorded in the hash-chained audit log. Controller-only — the air-gap export
  path has no persisted state and is unchanged (still signs iff the env key is set).

## [2.0.0-beta.3] - 2026-06-16

The third beta: closes the `controller-panel-rollout-ui` subject by building the operator-panel UI
for the signed agent self-update + canary-then-fleet engine that shipped headless in beta.2 — i.e.
the descoped plan-9 "Canary UI". Configuration and observation are no longer API-only.

### Added
- **Agent self-update rollout config card** (`AgentUpdateSettings`, Settings page, controller-mode
  only): target/min version, per-arch binary pins for the two self-update-certified arches, a canary
  node multiselect, and promote-fleet-wide behind a confirm modal (the empty-target safety contract:
  an empty target ⇒ no self-update). An **"Assist from GitHub release"** button pre-fills the per-asset
  SHA-256 pins for operator review.
- **Mimic GitHub-`.deb` catalog config card** (`MimicCatalogSettings`): version, release base, and a
  dynamic per-`<codename>-<arch>` `.deb` pins list, with a best-effort per-row assist (manual entry is
  the guaranteed fallback — external mirrors often publish no `.sha256`).
- **Assisted release-pin fetch endpoint** — operator `POST /api/v1/operator/release-pins`: fetches the
  `.sha256` sidecars through the persisted GitHub proxy and returns `renderer.Artifact` pins. Egress is
  guarded: http(s)-only, a redirect cap, a response cap, and a **dial-time private-IP reject** (loopback
  / link-local / RFC1918 / ULA / CGNAT / 6to4 / NAT64) that also defeats DNS-rebind. A
  `releases/latest/download` base + a requested version is rewritten to the tagged URL.
- **Per-node update-status chip** on the Fleet registry + node detail (`off / not-targeted / pending /
  applying / applied / failed / stale`), derived from the server-computed rollout membership
  (`in_rollout` on the nodes view), the reported version vs the target (a real SemVer comparator), and
  the agent health line. Plus an **opt-in "Live" auto-poll** (pauses while the tab is hidden, stops on
  logout).

### Fixed
- **Full-replace drop-on-save**: `POST /settings` rebuilds the settings from the body, so the panel now
  round-trips every persisted field — editing one card no longer silently wipes another's config.

### Security
- The assisted pin fetch is **convenience only and never a trust anchor**: the fetched sidecar rides the
  same untrusted transport as the binary; trust stays the controller-signed, keystone-bound
  `artifacts.json` the agent verifies a download against before exec. The panel never auto-saves or
  auto-trusts a fetched pin.

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

[Unreleased]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.8...HEAD
[2.0.0-beta.8]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.7...v2.0.0-beta.8
[2.0.0-beta.7]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.6...v2.0.0-beta.7
[2.0.0-beta.6]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.5...v2.0.0-beta.6
[2.0.0-beta.5]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.4...v2.0.0-beta.5
[2.0.0-beta.4]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.3...v2.0.0-beta.4
[2.0.0-beta.3]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.2...v2.0.0-beta.3
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
