# Changelog

All notable changes to YAOG (Yet Another Overlay Generator) are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Pre-1.0 `v2.0.0` is currently in a `preview → beta → rc → GA` ramp; see
[`RELEASING.md`](RELEASING.md) for the release process and tag conventions.

## [Unreleased]

## [2.0.0-beta.16] - 2026-06-27

A smoke-hardening of beta.15: while smoking the fleet, the controller's view of a node could look
**stale** — old agent version, a stuck `selfupdate: Blocked`, a frozen `Last Seen` — even when the node
itself was healthy and had successfully self-updated. The node and the beta.15 self-update fix were
working; these are three fixes to the **status/observability path**. Owner fleet re-smoke gates
promotion to Latest.

### Fixed
- **The node-detail page (`/fleet/nodes/:id`) now refreshes.** It previously rendered a frozen
  `localStorage` snapshot — no refresh-on-mount and no poll — so an operator watching a single node saw
  status (`Last Seen`, agent version, conditions) that never advanced even when the controller was
  current. It now refreshes on mount, offers a manual **Refresh** button plus the opt-in **Live** poll
  (both shared with the `/fleet` list via one hook), shows a **"last synced"** stamp, and surfaces a
  failed refresh (an expired session or a controller 502) instead of silently freezing.
- **`selfupdate: Blocked` no longer outlives a successful update.** A node that recovered and finished
  self-updating kept reporting `Blocked` — the latch from the earlier deferred/failed attempts — until
  the next config deploy bumped its generation. `FinalizeSelfUpdate` now clears the latch (a confirmed
  self-update means the node is no longer blocked), complementing the existing new-generation and
  idle-retry clears.
- **The telemetry heartbeat is hardened against a wedged probe.** `wg show all dump` (run twice per
  beat) now runs under a 10s timeout, and the heartbeat loop has a panic-recover, so a hung
  `wg`/netlink or a stray panic can no longer silently freeze a node's `Last Seen` while the agent
  process is alive.

### Note
- The controller's intermittent **502s** (its reverse-proxy / origin availability) are an operational
  concern, not an agent bug — the agent correctly keeps last-good and retries every interval. If status
  propagation stalls during an outage window, stabilize the controller origin; nothing is lost once it
  is reachable again.

## [2.0.0-beta.15] - 2026-06-27

Adds **mixed controller + local mode** — some nodes in a controller topology can be hand-deployed
("manual") rather than agent-managed — and fixes the agent self-update so a stalled rollout recovers
on its own instead of needing a manual service restart (reproduced on the live fleet while smoking
beta.14). Both items want an owner fleet smoke before this is promoted to Latest.

### Added
- **Mixed controller + local mode (manual nodes).** A node can be marked `deployment_mode: manual`:
  hand-deployed, agent-less, carrying its own operator-asserted WireGuard **public** key in the design
  (the private key never leaves the box; zero-knowledge custody is unchanged). The controller admits a
  manual node into the fleet through the single `enrolledSubgraph` chokepoint — managed peers carry it
  as a `[Peer]`, it carries the managed peers, and the off-host-signed membership manifest includes it
  — while keeping it out of the agent-convergence/edge-readiness gating (it is intentionally
  unmonitored). Its identity is validated at stage time (a manual node must carry a public key, unique
  across manual + enrolled nodes — new `manual_node_invalid` error). End-to-end flow:
  - **Design:** the node editor exposes a Managed | Manual control (controller mode) with a
    public-key field + hint; controller import keeps a manual node's public key (drops every private
    key + managed public key as before).
  - **Provision:** `yaog-agent kit --node-id <id> [--endpoint host:port]` ensures the on-box WG key
    (the same file `install.sh` later splices over `PRIVATEKEY_PLACEHOLDER`) and prints a
    `{node_id, wireguard_public_key, endpoint}` descriptor to paste into the design. It never contacts
    the controller.
  - **Deliver:** the operator downloads the node's promoted, signed bundle from the panel (a
    "manual / unmonitored" card with a Download button → `GET <operator>/manual-node-bundle?node=<id>`)
    and runs `sudo bash install.sh` on the host, which splices the on-box private key.
- **`agent kit` subcommand** (see above) for on-box provisioning of a manual node.

### Fixed
- **Agent self-update no longer needs a manual `systemctl restart yaog-agent` to recover.** A
  post-apply self-update whose binary download failed (e.g. a slow/timed-out GitHub-proxy fetch) was
  deferred and only retried on the *next applied generation* — so on a stable generation it stayed
  wedged until a restart. Now: (1) the daemon re-attempts a deferred self-update on its **idle cycles**
  on a backoff (`--selfupdate-retry-interval`, default 10m) without waiting for a new generation;
  (2) the download tries the configured proxy first, then **falls back to a direct GitHub fetch**;
  (3) the download is bounded by a response-header timeout + a **stall watchdog** + one shared absolute
  ceiling instead of a single total deadline that tripped on the body read of a large binary over a
  slow link. The signed-self-update custody model is unchanged — every retry re-verifies the bundle and
  the swap is still gated by the SHA-256-vs-signed-pin check; the crash-loop cap, anti-downgrade floor,
  abandoned-target memory, and in-flight guard are untouched.

## [2.0.0-beta.14] - 2026-06-25

Two bounded fixes surfaced while running the live fleet: the mimic TCP-transport filter now matches
the real on-the-wire flow (it silently fell back to plain UDP on multi-homed hosts), and the last
dark/light theme stragglers now follow the active theme.

### Fixed
- **mimic `transport: tcp` links silently dropped to plain UDP on some hosts.** The eBPF filter was a
  single `local=<egress_ip>:<port>` line pinned to the source IP of the route to `1.1.1.1`, matched by
  an exact lookup with no fallback — so when that IP didn't equal the source IP WireGuard actually put
  on the wire (multi-homing / secondary or floating IPs / policy routing), or resolved to a loopback
  address when `1.1.1.1` was null-routed, mimic shaped nothing and the tunnel went out as plain UDP.
  Now each node also emits a route-independent `remote=<peer_ip>:<peer_port>` filter per dialed mimic
  peer (the peer endpoint is known and stable, so it matches regardless of the local source IP), keeps
  the `local=` lines for the listen direction, rejects a loopback/empty egress IP (new
  `egress_unresolved` condition + the per-link UDP/fail-closed policy instead of a dead filter), and
  formats IPv6 filters correctly. The install script no longer aborts under `set -euo pipefail` when
  host resolution or egress detection fails (it degrades per policy). Rendered byte-for-byte identically
  by the in-browser compiler and the controller. **Note:** the data-plane behavior is confirmed by a
  two-host real-host smoke (see `docs/spec/artifacts/mimic.md`); for mimic links prefer IP-literal
  endpoints so the `remote=` filter is unambiguous.
- **The last panel light/dark theme inconsistencies.** Node-condition chips were dark-only (illegible
  in light mode) — now driven by the semantic status tokens; the canvas grid and edge labels are now
  theme-aware (the categorical edge-type / role hues are kept, deduplicated into one shared map); and
  the primary **Deploy / Compile** buttons no longer render grey in dark mode — they use a new
  dedicated `--cta` token (vivid teal in both themes) instead of the deliberately-graphite identity
  accent.

## [2.0.0-beta.13] - 2026-06-24

Makes the whole panel follow the light/dark theme — the feature views now theme like the app-shell —
plus refreshed bilingual documentation. Published as `v2.0.0-beta.13` and promoted to **GitHub Latest**.

### Changed
- **The entire panel follows the selected light/dark theme.** Previously only the app-shell themed
  correctly; the feature components (deploy/fleet panels, design editors, forms, audit, settings,
  canvas) were hardcoded dark, so in light mode they stayed dark "islands" in a light shell. All ~39
  components now reference the semantic CSS-variable tokens, so the full UI follows the active theme in
  both modes. Adds neutral `--control` tokens plus four semantic status token families (`--danger` /
  `--success` / `--warning` / `--info`, each with `-bg` / `-border` / `-solid` / `-solid-fg`), defined
  for both themes, so status badges, buttons, and banners stay legible in light *and* dark. Categorical
  hues (canvas node-role colors, edge-type colors, per-peer status dots) are kept as distinct cues. No
  agent/controller behavior change — panel styling only.

### Docs
- **Full bilingual wiki rewrite** (`docs/wiki.md` + `docs/wiki-zh.md`): covers both the local /
  air-gap generator and controller mode end-to-end — the two-modes / `//go:build airgap` boundary,
  controller operations (enrollment, stage→promote, agent pull/verify/apply, signed self-update, live
  fleet health + the per-peer WireGuard panel), the security model, and an accurate per-build HTTP API
  reference. Corrects stale facts (the air-gap-only `/api/{validate,compile,export}` routes, in-browser
  local compilation, per-edge Babel link cost incl. the relay `rxcost 96` preset).
- **README freshened**: in-browser local compile, controller-only default backend, the 2.0 beta feature set.

## [2.0.0-beta.12] - 2026-06-23

Surfaces per-peer WireGuard link health — the granularity the aggregate `wireguard` condition hid.
Published as `v2.0.0-beta.12` and promoted to **GitHub Latest**.

### Added
- **Per-peer WireGuard link telemetry + a collapsible panel.** The telemetry framework's first real
  metric probe: the agent emits per-peer link health (`{peer, interface, endpoint, last_handshake,
  status}`) on the existing `/telemetry` heartbeat as `metrics["wireguard_peers"]` — no wire change,
  no key material. The controller persists + serves it under `node.telemetry`, and the node detail
  page renders a collapsible **"WireGuard links"** panel showing each peer's last handshake (relative,
  live-ticking) + a status dot, auto-opened only when a link is down. This is the granularity behind
  the aggregate condition: you can now see *which* link is down, not just a whole-node state.

### Changed
- **The aggregate `wireguard` condition distinguishes some-down from all-down.** A single
  never-handshaked peer in a mesh (an offline/asymmetric link Babel routes around) now reads as
  **`SomePeersDown`** ("1/3 peers down"), not a misleading whole-node `LinkDown`; `LinkDown` is
  reserved for *all* peers down (or a fresh apply). The per-peer panel shows the offending link.
- **Telemetry metrics are now persisted + served.** `RecordTelemetry` stores the heartbeat's
  extensible metrics map on the node (replaced wholesale, observability-only — still never touches
  applied generation or any custody floor) and `/nodes` serves it under `node.telemetry`.

## [2.0.0-beta.11] - 2026-06-23

A fast follow-up fixing two findings from smoking beta.10 on the live fleet — both reproduced against
the real `hack3ric/mimic` upstream + a real `gh-proxy.com`. Published as `v2.0.0-beta.11` and promoted
to **GitHub Latest**.

### Fixed
- **Mimic catalog "Discover from release" works again.** Discovery was routing the GitHub REST API
  call through the configured GitHub proxy, whose shared API identity is globally rate-limited (a
  `403` for everyone) — so discovery failed even with a correct URL. Discovery now hits the GitHub
  REST API **directly** (`api.github.com`); the dial-time SSRF egress guard + the `github.com`
  host-pin still apply, and the actual `.deb` **downloads** still route through the proxy (its real
  purpose). The release-base parser is also more forgiving — it accepts the forms an operator
  naturally pastes (`.../releases`, `.../releases/latest`, `.../releases/tag|tags/<tag>`, the repo
  root) and **normalizes** the field to the canonical `.../releases/latest/download` |
  `.../releases/download/<tag>` form the install fetches from. Discovery no longer applies the
  "Catalog version" field (it is operator bookkeeping — the base alone selects the release), so a
  stale/nonexistent version tag can no longer 404 the discover. Clearer failure message.
- **A stalled agent self-update rollout is now visible in the panel.** When a post-apply self-update
  keeps being **deferred** — most commonly because the rollout target was bumped but its pins still
  resolve to the *old* binary, so the agent's self-test correctly refuses the version/hash-mismatched
  binary (no brick) — the agent now records the reason and surfaces it as a `selfupdate: Blocked`
  condition (lowest precedence), live via the `/telemetry` heartbeat. The panel shows **why** a node
  is not advancing instead of it silently staying behind; the remedy is to re-arm the rollout's pins
  (the one-click "update all → controller version" re-fetches matching pins) and redeploy. The signal
  is observability only — it touches no custody/anti-rollback state and is self-clearing.

## [2.0.0-beta.10] - 2026-06-23

A smoke-hardening release for the cluster of live-fleet defects and UX gaps surfaced while smoking
beta.9 on a ~9-node fleet. The headline: the **Node Conditions feedback channel shipped in beta.9
was lying** — conditions were sampled once, at apply time (while WireGuard was still mid-handshake
and a self-update still in probation), and never re-sampled while the node was idle, so the panel
froze a worst-case post-apply snapshot even though the overlay and self-update were healthy. beta.10
makes the channel honest with a dedicated, extensible telemetry heartbeat, and fixes three more
smoke findings (a controller-mode Validate 404, a fleet-stranding signing-key re-pin nudge, and the
mimic catalog's hand-typed `.deb` filenames). Backward-compatible throughout (old agents keep
reporting on `/report`; old controllers ignore the new field). Published as `v2.0.0-beta.10` and
promoted to **GitHub Latest**.

### Added
- **Telemetry monitoring channel — a live-health heartbeat.** A new agent-authenticated
  `POST /telemetry` carries the node's conditions (and an extensible metrics map) on a fast interval
  (`--telemetry-interval`, default 30s), separate from the apply-time `/report`. It is built as a
  reusable monitoring framework: an agent-side `Sampler` interface (conditions are the first probe;
  latency / resource metrics plug in later with zero wire change) aggregated under a panic-isolating
  collector. Telemetry carries **no** `applied_generation`/checksum — observability is kept strictly
  separate from deploy custody, so a heartbeat can only refresh the node's conditions + last-seen and
  can never advance or regress its applied generation. The panel needs no change (it already projects
  `node.conditions`).
- **Mimic catalog discover-and-pick.** The mimic `.deb` catalog gains a "Discover from release"
  button that lists a GitHub release's `.deb` assets (operator-only, SSRF-guarded, through the
  gh-proxy) so the operator picks from a checklist and labels each `<codename>-<arch>` instead of
  hand-typing exact upstream filenames. Debug sidecars (`dbgsym` / `.ddeb`) are filtered out, two
  picks that would collide on a label are blocked (so a save can't silently drop a pin), and the
  SHA-256 stays blank — custody is unchanged (the hash is still fetched by the per-row Assist and
  saved through the validated `/settings` path).

### Fixed
- **Node Conditions no longer freeze at apply time.** With the telemetry heartbeat, conditions
  refresh every interval, so a node whose WireGuard finished handshaking and whose self-update left
  probation reports `wireguard: AllPeersUp` / `selfupdate: Updated` within one interval instead of
  showing the stale worst-case `LinkDown` / `HealthConfirmedProbationary` indefinitely. `/report`
  still stamps conditions at apply time; both wholesale-replace the field (last-writer-wins), and the
  heartbeat — firing far more often — supersedes the stale snapshot.
- **Controller-mode Validate no longer 404s.** The shipped controller build gates the air-gap
  compute routes off (`//go:build airgap`), but the panel was POSTing `/api/validate` in controller
  mode → 404. Validation is structural (schema + semantic, key-free), so it now runs the in-browser
  TypeScript validator in controller mode too (browser-local verify). The controller neither serves
  nor calls `/api/validate`, keeping its attack surface minimal — even the `VITE_YAOG_LOCAL_ENGINE=
  backend` escape hatch is local-mode-only.
- **Deploy recovers an off-host signing key on a cleared/fresh browser.** A browser with no local
  signing descriptor showed "no operator signing key is enrolled" on Deploy — even when the
  controller HAD a credential pinned — nudging the operator toward a fleet-stranding re-pin. The
  controller now serves the **non-secret** signing descriptor (credentialId + alg + rpId + the
  audit-only public PEM already baked into every node bundle), and the panel recovers it into the
  empty local slots (WebAuthn only, fill-empty-only) so Deploy just re-prompts the authenticator for
  a tap. The deploy precondition message is also split so a pinned-but-unrecovered browser is told to
  connect the authenticator / re-enroll — never to re-pin.

### Security
- **Signing-handle recovery preserves the off-host keystone boundary.** The recovered descriptor is
  non-secret public material the controller already bakes into every node bundle; the private key
  never leaves the authenticator, a tap is required per signature, and a node verifies each signature
  against its OWN pinned key — never the served PEM. A compromised controller still cannot forge a
  deploy.
- **Release-asset discovery is SSRF-guarded.** The new `release-assets` endpoint reuses the
  release-pin egress guard (dial-time private-IP reject + the gh-proxy), pins the API host to
  `api.github.com`, and rejects owner/repo/tag path traversal — so a crafted base cannot reach an
  internal address.

## [2.0.0-beta.9] - 2026-06-23

The first release since beta.8, folding in the whole pre-rc.1 program — a backend/local-compute
refactor with a hardening security re-audit, a phone/responsive operator UX pass, an end-to-end test
foundation — plus the agent→panel feedback work that makes the controller panel a trustworthy fleet
operations console (structured "Node Conditions", per-link mimic→UDP fallback, version-aware agent
rollout, working default release URLs). The agent-feedback features are additive and
backward-compatible (old agents send no conditions; old controllers ignore the field). One deliberate
**backward-incompatible** hardening: the default controller build no longer serves the anonymous
`/api/{validate,compile,export,deploy-script}` compute routes (see **Changed**). Published as
`v2.0.0-beta.9` and promoted to **GitHub Latest** (the `releases/latest/download` alias resolves
here) for easier deployment.

### Added
- **Node Conditions feedback channel.** Agents now report structured, curated conditions
  (`{type, status, reason, message, since}`) for `config-apply`, `self-update`, `wireguard`, and
  `mimic` alongside the legacy `health` string. A single agent-side `classify()` chokepoint caps each
  message and emits only closed reason enums (never raw stderr), and the panel renders them
  generically (colour by status, tooltip = curated message).
- **Mimic → UDP fallback, per link.** An edge gains a tri-state `mimic_fallback`
  (inherit / `udp` / `none`) with a fleet-wide `MimicFallbackDefault`. When the resolved policy is
  `udp` and mimic provisioning fails (kernel too old, eBPF load, install), the link comes up as plain
  UDP and reports a categorised, loud `warn` mimic condition instead of staying down; otherwise it
  fails closed (unchanged). The shipped default is conservative (`none` — fail closed), preserving
  mimic's censorship-evasion guarantee; the operator opts in fleet-wide or per link.
- **Default release URLs + working "Assist from release."** A `DefaultMimicReleaseBase` (the upstream
  `hack3ric/mimic` `releases/latest/download` alias) ships so the mimic `.deb` catalog assist no
  longer hard-errors on a never-edited controller, and the agent assist pins to a real release tag
  instead of leaving the base on the moving `latest` alias (killing the silent rollout stall). The
  panel placeholders now show the real defaults. Custody is unchanged — assist only fills pins the
  operator reviews and saves through the validated `/settings` path.
- **Phone / responsive operator UX.** The operator panel now lays out for phone and tablet: an
  off-canvas navigation drawer/sheet, a read-only canvas gate on small screens, and a responsive
  pass across the operator pages.
- **In-browser local design compiler.** Local (non-controller) design now compiles entirely in the
  browser via a TypeScript port of the Go compiler, pinned byte-for-byte to the Go output by a
  Go↔TS conformance gate in CI — so local design needs no backend at all.

### Changed
- **Version-aware agent rollout.** The controller now knows and displays its own build version (in
  the user menu, surfaced on `/session` and login). "Update all agents" with no version typed targets
  the panel's own version (one click: sets the target, fetches its pins, arms the fleet-wide confirm —
  never auto-saves), and the controller refuses to set an agent target newer than itself
  (`agent_target_newer_than_controller`, with an advisory hint before save). The version comparator is
  single-sourced (`internal/version`) so the agent's anti-downgrade floor and the controller's
  refuse-newer guard can never diverge.
- **Self-update status is read from a structured condition** rather than free-form `health`
  substring-matching when a `selfupdate` condition is present (the legacy string fallback is retained
  for old agents).
- **Local design moved to the browser; the backend is controller-only.** Local mode is now
  browser-resident by default and the Go backend's job is the operator-gated controller path.
- **Anonymous compute routes are gated behind a build tag (backward-incompatible for the default
  build).** The four anonymous routes `POST /api/{validate,compile,export,deploy-script}` are now
  compiled only into the `-tags airgap` build (the local-design oracle / air-gapped target) and are
  **absent (404) from the default controller build**, so no unauthenticated path reaches the compile
  pipeline in the shipped controller. The default controller boot fails loud if its controller
  environment is unset rather than standing up an anonymous compute listener. Operators who relied on
  the anonymous routes must use the `-tags airgap` build or the in-browser local-design path.

### Security
- **Toolchain + crypto bump clearing reachable CVEs.** Builds now pin `toolchain go1.26.4` and
  `golang.org/x/crypto v0.52.0` (up from the go1.25 stdlib / x/crypto v0.31.0), clearing reachable
  `crypto/x509`, `crypto/tls`, `encoding/pem`, and `net/url` advisories. A required `govulncheck`
  gate now fails CI on any reachable vulnerability across both build profiles (default and
  `-tags airgap`); gosec SAST and an npm/frontend SCA scan run alongside it as advisory checks.
- **Release-pin fetch SSRF completeness.** The server-side "Assist from release" fetch closes the
  remaining SSRF gaps (redirect/egress handling) so a crafted base cannot reach a private address.
- **Bootstrap operator-credential binding validated at pin time** (RPID/Origin), with a loud
  TOFU/MITM startup warning when a legacy unsafe binding is detected.
- **Durability + auth hardening.** The file store now fsyncs on write (no torn state on crash),
  passkey login binds the WebAuthn Origin, and the IP allocator caps its scan budget to bound a
  pathological-topology DoS.
- **Diff-aware adversarial security re-audit** of the whole pre-rc.1 delta signed off before this cut.

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

[Unreleased]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.9...HEAD
[2.0.0-beta.9]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.8...v2.0.0-beta.9
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
