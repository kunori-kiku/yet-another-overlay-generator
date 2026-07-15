# Changelog

All notable changes to YAOG (Yet Another Overlay Generator) are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Pre-1.0 `v2.0.0` is currently in a `preview → beta → rc → GA` ramp; see
[`RELEASING.md`](RELEASING.md) for the release process and tag conventions.

## [Unreleased]

### Fixed

- **Multi-platform release-image verification now executes each exact child manifest digest.** The
  rc.7 publication run exposed that reusing one parent index digest for sequential amd64/arm64
  `docker run --platform` checks fails on Docker's classic image store even though both children are
  valid. The verifier and its mock now require the platform-specific child digest, and a fail-closed
  recovery workflow can finish an immutable post-boundary RC/GA transaction from its original verified
  run, artifacts, tag, and image without rebuilding or overwriting the version.

## [2.0.0-rc.7] - 2026-07-16

**Release candidate.** A compatibility, custody, and release-integrity hardening release over
`v2.0.0-rc.6`. New browser login and keystone credentials prove User Verification at enrollment,
while ordinary assertions deliberately retain the existing signature + binding + User Presence
contract so an upgrade cannot lock out existing operators or invalidate the fleet's served manifest.
The same candidate closes adjacent crash-consistency, private-file custody, exact-artifact, browser
concurrency, and publication-recovery gaps found during the structural and security review.

### Security
- **WebAuthn User Verification (UV) is now proven server-side when a browser credential is enrolled.**
  Login-passkey and keystone-passkey enrollment each begin with an authenticated, one-use challenge
  scoped to the operator and purpose. After `navigator.credentials.create()`, the new credential
  immediately signs that challenge in a second `navigator.credentials.get()` ceremony; the controller
  verifies the signature, exact credential ID, RP/origin binding, and UV flag before storing the public
  credential. The panel warns that an assertion without UV proves possession only, so someone holding
  the authenticator or a usable copy can act as the operator; WebAuthn backup/sync eligibility is a
  separate property from UV.
- **The pre-release blanket UV assertion gate was removed before rc.7 to protect existing users.**
  Existing credentials and trust-list signatures were enrolled or produced without a server-side UV
  acceptance requirement; imposing one retroactively could lock an operator out and could make upgraded
  nodes reject the fleet's currently served manifest. Ordinary login, 2FA, keystone signing, and
  node-side membership verification therefore still require a valid signature, expected challenge,
  RP/origin binding, and User Presence, but do not reject solely because an assertion lacks UV. The
  browser requests UV as `preferred` for ordinary assertions and `required` only for enrollment proof;
  new browser credentials prove UV during enrollment without changing the contract under existing users.
  No fleet re-sign is required. (#282 follow-up)
- **Controller and agent trust transitions are now crash-consistent and fail closed.** A staged deploy
  is authorized by an exact durable set seal and commits its tenant generation last; interrupted
  promotions cannot be served, replaced, or accidentally authorized by a later generation-only wake.
  Keystone pin/rotation uses a durable transition marker and reconciles its exact audit event before
  bootstrap, preview, stage, sign, promote, or status can use the new credential. On each node, a
  cross-process state lease covers rekey/apply/self-update, custody files are descriptor-checked and
  size-bounded, and a `PendingApply` write-ahead record makes the verified bundle, action, trust-anchor
  bindings, compiled-at floor, and membership epoch durable before root `install.sh` may run. A crash
  or final-state write failure therefore retains the prior last-known-good record while preventing a
  rollback or same-version substitution on recovery. Exact retries re-sync the original intent, and a
  Linux installer guardian retains the same filesystem lease after a Go-parent crash until the root
  script exits, preventing a restarted daemon/manual kit from overlapping it. Self-update state reads
  and terminal writes now fail closed without erasing a pending apply or prematurely deleting the
  rollback binary. Windows keeps portable agent commands but explicitly refuses Linux `install.sh`
  application instead of claiming an unsafe cross-process lock transfer.
- **File-backed controller state now has an explicit local custody boundary.** FileStore rejects
  symlink/non-directory roots and unsafe Unix ownership or write permissions, creates tenant and
  collection directories privately, replaces records through random same-directory `0600` temporary
  files, fsyncs file and directory commit points, and durably syncs deletes. Windows keeps equivalent
  path/reparse and replace-through semantics while documenting that installation/service ACLs remain
  the platform ownership boundary.
- **Manual AgentHeld installs now have one verified execution path.** `yaog-agent kit apply` captures a
  bounded directory/ZIP snapshot, rejects traversal, aliases, case-fold collisions, special files and
  file/parent conflicts, verifies bundle integrity plus out-of-band keystone membership, materializes a
  fresh trusted tree, and only then invokes its copied installer. `kit verify` shares the same
  materialization preflight. The unsafe no-keystone path requires an explicit long acknowledgement and
  is refused once node state has ever verified keystone membership.
- **Release publication is provenance-checked and recoverable.** Tag builds require one annotated
  SemVer tag at the approved `main` commit, re-run the full release gates, produce exactly 22 named
  assets, and audit every archive member and binary target/version/VCS record before upload. Versioned
  multi-architecture images are adopted only when their labels, runtime version, platforms, source
  revision, and digest match. GitHub Release upload remains private until assets and images are sealed;
  idempotent finalizers then converge the verified image digest and GitHub Latest pointer. Third-party
  actions in the publication workflows and `govulncheck` are version/commit pinned.

### Fixed
- **Concurrent operator mutations and stale browser responses no longer overwrite newer state.** Login
  passkey, TOTP, and keystone changes use field-scoped compare-and-set operations; the panel attaches
  requests to an authentication/controller generation and applies only the latest authoritative status
  response. A failed-but-committed enrollment is reconciled by exact public descriptor rather than
  silently creating another credential.
- **Artifact publication is an exact-tree transaction.** Export writes a validated sibling tree and
  swaps it into place, so removed nodes and obsolete signing sidecars cannot survive a new export.
  Failure before the swap preserves the prior tree; failure to delete the now-private old backup is
  reported as a cleanup warning after a successful commit rather than a false export failure.
- **Generated deployment helpers reject resource-exhaustion and path ambiguity before extraction.**
  Bash and PowerShell enforce archive/member/expanded-size ceilings, canonical portable names, exact
  member types, and duplicate/case-collision rules before copying a node-ID-keyed bundle.

### Changed
- **Portable node IDs are now the sole artifact-directory identity.** Validation rejects device names,
  reserved helper names, trailing-dot aliases, overlong segments, and case-fold collisions before any
  renderer or exporter can map two logical nodes onto one Windows/Unix path. Display names remain
  presentation data rather than filesystem identity.
- **Controller staging, persistence, agent custody, and release verification are split into focused
  framework modules with shared primitives and adversarial compatibility tests.** This removes parallel
  validation/write paths while preserving the canonical localcompile/render pipeline and existing
  WebAuthn runtime acceptance contract.

### Documentation
- Reconciled the README, bilingual operator wiki, controller/agent/API specifications, architecture
  cache, release runbook, and historical plan status with enrollment-only UV, verified manual apply,
  durable promotion/audit/apply boundaries, portable artifact naming, and the exact release transaction.

## [2.0.0-rc.6] - 2026-07-15

**Release candidate.** A correctness, security, and structural-debt release over `v2.0.0-rc.5` — no new
features. It closes the residual and newly-introduced debt the framework-refactor left behind, led by a
**ship-breaker fix**: the WASM local engine (the default in-browser design engine) was never built into
the release or Docker pipelines, so a shipped panel had no `yaog.wasm` and local design failed to load.
Thirteen reviewed plans (PRs #277–#292), each independently reviewed and adversarially verified before
merge; assessed by a repo-wide debt sweep + a focused security-correctness pass whose **negative
evidence** is the reassuring headline — **no trust-root bypass, no key leak, no shipped CVE**; the
defects lived in the mirrors and edges, not the controller/agent-managed trust paths.

### Security
- **Standalone `install.sh` signed-set hardening.** The self-extracting installer verified the bundle
  signature and `sha256sum -c`'d the checksum list, but did not reject a **present-but-unlisted
  `artifacts.json`** before reading the `.deb` pins from it — so an attacker-injected catalog could steer
  a root `.deb` install despite a valid bundle signature. The installer now mirrors the agent verifier's
  coverage guard (fail-closed, and only when an `artifacts.json` is present — a legitimately-signed
  no-catalog bundle is unaffected). (#277)

### Fixed
- **WASM local engine now ships in every artifact (ship-breaker).** `yaog.wasm` + `wasm_exec.js` are now
  built into both the release pipeline and the Docker image (previously CI built the wasm only for the
  conformance gate, so no shipped `dist` contained it and in-browser local design 404'd on load). A
  red-build assertion now fails the release if the wasm is missing from `dist`/`dist-local`, and the
  panel's wasm loader resets its load promise on failure so a transient load error can be retried instead
  of wedging. (#278)
- **`deploy.go --uninstall` tears down mimic and no longer drifts on the SNAT delete.** The SSH deploy
  scripts' uninstall path now stops/disables the `mimic@` unit and removes its config (previously it
  orphaned the boot-persistent mimic unit + its root eBPF program), and deletes the overlay SNAT rules by
  matching the live rule set instead of a hard-coded `10.10.0.0/24` (so a non-default transit CIDR is torn
  down correctly). Both the bash and PowerShell renderers are covered, including a client node whose sole
  link is `transport: tcp`. (#279, #292)
- **Agent self-update compares versions by semver, not exact string.** A `v`-less operator target (e.g.
  `2.0.0` against a released `v2.0.0`) could pass the swap and then permanently wedge the update channel;
  reconciliation now routes through the same semver comparator the self-test uses, and the artifact
  download is bounded by a size cap. (#280)
- **Trust-list signing is serialized under one tenant lock.** Installing an operator trust-list signature
  now holds a single tenant-ops lock across read → substitution-guard → verify → write, closing a window
  where a concurrent re-stage could pair a stale signed manifest with fresh bundles; controller store
  reads that must be durable no longer read through the volatile telemetry overlay. (#281)

### Changed
- **Structural debt paydown (behavior-preserving, no rendered-output change).** The agent controller-mode
  daemon was extracted into a testable `ControllerLoop`; the ~580-line `derivePeersWithDomains` was split
  into named helpers **byte-identically** (goldens + allocation-stability + the WASM conformance gate
  unchanged); `handler_bootstrap.go` was split and the agent request mux routed through the structural
  auth adapter; the controller wire DTOs gained a non-vacuous drift gate (Go ↔ TS snake_case parity, run
  under `go test`); the `Field` form primitive adoption was finished; and the release/Docker pipelines
  were aligned to the go1.26 toolchain with a dead-code sweep. (#283–#287, #290)

### Documentation
- Archived six delivered-but-unarchived subjects and reconciled `STATUS.md` / `CHANGELOG` / mixed-mode
  planning state; purged retired air-gap and deleted-TS-compiler prose plus rotted line-number citations
  from the README, `CLAUDE.md`, and the specs, and renamed a mislabeled test. (#288, #289)

## [2.0.0-rc.5] - 2026-07-13

**Release candidate.** Adds two operator-facing capabilities over `v2.0.0-rc.4`: **per-node resource
history with node-detail CPU / RAM / load charts**, and **delta deploy** — a Deploy now re-stages only
the nodes whose config actually changed instead of churning the whole fleet. It also fixes the
underlying telemetry-framework defect behind the recurring "a new metric only fires at deploy time, then
freezes" class. Eight reviewed plans (PRs #249–#256), each independently reviewed and adversarially
verified before merge.

### Added
- **Node-detail CPU / RAM / load history charts.** The node-detail page charts each node's host-resource
  history over a chosen **time range** and **granularity**: CPU %, memory-used %, and the load averages.
  Built on a reusable, series-generic `TimeSeriesChart` (Recharts, lazy-loaded so the ~105 kB charting
  dependency is code-split out of the initial bundle and loads only when a node-detail page is viewed).
  (#253)
- **`cpu_pct` host-resource metric.** The agent's `/telemetry` heartbeat now reports CPU utilisation as a
  stateful `/proc/stat` jiffies delta between consecutive beats. The first beat after an agent (re)starts
  carries no `cpu_pct` — a deliberate gap, never a fabricated `0`. (#249)
- **Retained resource history + query API.** The controller keeps a bounded, append-only per-node history
  of the `resource` metric (in-memory append on the heartbeat path — never a disk write — with an
  off-heartbeat flush to per-node JSONL and amortized compaction). An operator-gated `node-history` query
  aggregates raw samples server-side into bucketed avg/min/max, omitting empty buckets (gaps stay gaps),
  flooring the step at ~1 s and widening it so a response never exceeds 1000 buckets. Retention is
  configurable: a per-node sample cap (default ≈ 20160 ≈ 7 days at the 30 s heartbeat; `0` disables
  history, which the charts render as a "history off" state; a persisted `0` survives a controller
  restart). (#251, #252)
- **Delta deploy — skip unchanged nodes.** A Deploy skips any enrolled node whose freshly compiled bundle
  is byte-identical to the one it is already serving (identity = SHA-256 of `checksums.sha256`, which
  excludes the volatile `compiled_at`). A skipped node keeps its current generation, so its agent never
  re-fetches and the fleet settles at a mixed generation where only changed nodes advance — a per-link
  re-handshake now happens only on links whose endpoints changed, not fleet-wide. Fail-open (a node whose
  served bundle can't be read re-stages), and disabled for keystone first-pin / rotation (which must
  re-pin the whole trust-list). (#254)
- **Force redeploy + pre-deploy preview.** A pre-deploy preview shows "N updated, M unchanged" computed
  over the current canvas as a read-only dry-run (it stages nothing) and never hard-blocks a deploy — a
  preview that can't be fetched (e.g. a newer panel against an older controller) surfaces the error but
  still lets you Deploy anyway. **Force redeploy** (per-node and fleet-wide, plus a per-node "Force
  redeploy this node" on the node-detail page) overrides the skip for on-host drift / rescue. (#255)

### Fixed
- **Telemetry freshness — the framework defect behind "only fires at deploy time".** Observability now
  has a single metrics producer: `metrics` ride the `Sampler` **heartbeat** (the apply-time `/report`
  carries conditions only, never metrics), and the agent's apply loop sends a coalescing **post-apply
  kick** so a fresh heartbeat lands immediately after each apply rather than up to a full interval later.
  A new metric can no longer be bolted onto `/report` and then freeze between deploys. (Conditions remain
  dual-write — `/report` at apply-time + the heartbeat live, last-writer-wins — which the kick keeps
  fresh.) (#250)

### Documentation
- New `docs/spec/operations/telemetry-history.md` (the sampler, the retention store + its
  heartbeat-never-writes-disk invariant, the query API), delta-deploy sections in the controller deploy
  spec, the `resource`/`cpu_pct` + post-apply-kick notes in the controller-api spec, and bilingual wiki
  coverage (§5.5 delta deploy / preview / Force, §5.8 resource metric + history charts). (#256)

## [2.0.0-rc.4] - 2026-07-07

**Release candidate.** Fixes the mimic fleet issues the `v2.0.0-rc.3` soak surfaced during live
debugging: the DKMS module failed to build **even on a current Debian kernel** because mimic-dkms's
build needs `bubblewrap` + `dwarves` (which it doesn't declare and YAOG didn't install); the panel's
`mimic` condition was a frozen deploy-time snapshot; flipping a node's last `tcp` link to `udp` never
stopped the stale `mimic@`; and mimic over an L7 relay can't work. Four reviewed plans (PRs #241–#244),
each independently reviewed and adversarially verified before merge.

### Fixed
- **mimic build dependencies.** The installer now installs `bubblewrap` (the `bwrap` build sandbox) and
  `dwarves` (`pahole`, for BTF generation) alongside `dkms`/`gcc`/`linux-headers` — in the mimic
  provisioning step *and* the `_mimic_module_ready` module-build retry. mimic-dkms's DKMS build needs
  both but declares neither, so the module failed to build even on a current kernel with headers present
  (`make: bwrap: No such file` → Error 127, then `pahole: not found`). This is the rc.3-soak build
  failure; a node that already has the binary but an unbuilt module now rebuilds on redeploy. (#241)
- **`tcp → udp` de-provisions mimic.** The install-path Phase 0 now unconditionally stops any stale
  `mimic@` unit and removes `/etc/mimic/*.conf` before re-provisioning. The mimic teardown was
  previously `--uninstall`-only, so flipping a node's last `tcp` link to `udp` left the old `mimic@`
  running — it kept shaping traffic WireGuard now sent as plain UDP and the link stayed broken. (#241)
- **Live `mimic` Node Condition.** The agent re-probes the actual unit (`systemctl is-active
  mimic@<egress>`, timeout-guarded) each heartbeat and reports a live `Stopped` warning when mimic
  should be running but the unit isn't — so a `systemctl stop`, crash, or flap shows in the panel
  instead of a frozen deploy-time "active". (#242)

### Added
- **Relay-path mimic warning.** An enabled `transport: tcp` edge whose `type` is `relay-path` raises the
  `validation_edge_mimic_relay_path` design-time **warning** (not a hard error): mimic (fake-TCP) needs
  a direct L3/L4 path — an L7 / UDP-accelerator relay terminates and re-originates the connection so the
  reverse fake-TCP leg is RST'd — use `transport: udp` for relayed edges. (#243)

### Documentation
- The mimic spec + bilingual wiki document the build deps, the unconditional teardown, the live
  condition, a new "Direct path required (no L7 relay)" section, and a `mimic_fallback: udp`
  unilateral-split caveat (the clean fix for a node that genuinely can't build mimic is `transport:
  udp` on both ends). (#244)

## [2.0.0-rc.3] - 2026-07-06

**Release candidate.** Fixes the mimic (`transport: tcp`) **runtime** defect the `v2.0.0-rc.2` soak
surfaced on the live fleet: after rc.2's two-package install fix, a `transport: tcp` link on a
stale-kernel node still failed — `mimic run` looped on exit 22 (`is the Mimic kernel module loaded?`)
because the `mimic-dkms` module was never built (the node's kernel is behind its repo's point release,
so `linux-headers-$(uname -r)` was pruned and DKMS stayed at `added`). Root cause: `_mimic_provision`
declared success on `command -v mimic` (the userspace **binary**) alone, never verifying the DKMS
**kernel module** built/loaded — so it proceeded to a broken start, and on a `mimic_fallback: udp` link
the false-success silently skipped the UDP fallback. Four reviewed plans (PRs #235–#238), each
independently reviewed and adversarially verified before merge.

### Fixed
- **mimic module build/load verification.** `_mimic_provision`'s success gate is now that the DKMS
  kernel module is genuinely loadable (`lsmod` / `modprobe` / a `dkms autoinstall` retry), not merely
  that the `mimic` binary exists. When the module can't be built/loaded (the stale-kernel /
  pruned-headers case) the installer classifies a distinct `module_unavailable` outcome and honors the
  link's `mimic_fallback` policy — `udp` degrades to plain UDP, `none` fails closed with a clear
  *"reboot into the current kernel"* message — instead of the cryptic exit-22 loop. This also closes
  the silent no-degrade on `udp`-policy links (the false-success used to skip the fallback). (#235)
- **Orphaned-lock cleanup.** Before `systemctl restart mimic@<egress>`, the installer clears a wedged
  unit (`stop` + `rm -f /run/mimic/*.lock` + `reset-failed` + `modprobe`), so an orphaned `/run/mimic`
  lock from an uncleanly-exited prior instance (a `failed to lock … File exists` → exit-17 loop) can't
  wedge the restart. (#235)

### Added
- **`ModuleUnavailable` mimic Node Condition** (a `warn` with a reboot hint) + the `module_unavailable`
  breadcrumb outcome, so the panel shows *why* mimic is down. (#235)
- **Pre-deploy "can this node run mimic" probe.** A pure-sysfs agent heuristic
  (`metrics["mimic_capability"]` → ready / buildable / unbuildable) warns in the node editor when a
  node's kernel can't build the module — before you deploy, not after a failed apply. (#236)
- **Native-XDP support is now always-visible** in the node editor (not only once `native` is
  selected), so you can see whether a NIC supports native before choosing it. (#236)
- **Per-node egress-interface override** (`Node.mimic_egress_interface`, e.g. `wan0`; empty =
  auto-detect) for multi-homed / policy-routing nodes where the WireGuard egress isn't the default
  route. The override rides the signed `install.sh`; a schema validator guards the interface name. (#237)

## [2.0.0-rc.2] - 2026-07-04

**Release candidate.** Fixes the mimic (`transport: tcp`) install defect the `v2.0.0-rc.1` soak
surfaced on the live fleet: a `transport: tcp` link failed to deploy on Debian 12 / Ubuntu 24.04
(`install.sh exit: exit status 100`), taking the affected nodes' tunnels down. Root cause — upstream
`hack3ric/mimic` ships **two** packages (`mimic` + `mimic-dkms`, the latter `Provides` the
`mimic-modules` the first `Depends` on), but YAOG's catalog could pin only one, so `apt` could not
satisfy the dependency; and the unguarded `apt-get` under `set -euo pipefail` aborted the whole node
apply before the fallback-to-UDP logic ran. Five reviewed plans (PRs #228–#232), each independently
reviewed and adversarially verified before merge — the review caught (and this ships the fix for) a
real redeploy+reboot de-cloak in the native-XDP auto-downgrade.

### Fixed
- **Two-package mimic install.** The catalog now pins `{asset, sha256, dkms_asset, dkms_sha256}` per
  `<codename>-<arch>` and the installer fetches, SHA-256-verifies, and `dpkg`s **both** the `mimic`
  and `mimic-dkms` packages together, so `Depends: mimic-modules` resolves (artifacts.json schema
  1→2, back-compatible — a legacy `{asset,sha256}`-only catalog still loads). (#228)
- **Fail-degradable provisioning.** The mimic provisioning block is now a shell function that returns
  on any failure instead of a `set -e` abort, so every failure (missing pin, download/checksum
  failure, unsatisfiable `Depends: mimic-modules`, or a DKMS build failure on a stale kernel — reboot
  into the current kernel to build the module) degrades per the link's `mimic_fallback` policy —
  plain UDP under `udp`, a categorized breadcrumb + exit under `none` — rather than bricking the node
  apply. `mimic@<egress>` is now `enable`d + **`restart`ed** (not a no-op `enable --now`), so a
  redeploy re-applies the freshly-written config. (#228, #230)
- **Assist reliability.** A gh-proxy `.sha256` sidecar miss now retries the direct GitHub URL before
  giving up, and an empty/garbage fetched SHA is treated as a miss (never saved). (#229)

### Added
- **Panel two-package catalog.** Discover pairs a `mimic-dkms` asset to its `mimic` sibling under one
  `<codename>-<arch>` row; Assist fetches both sidecars; a row missing its module companion warns.
  (#229)
- **Native-XDP auto-downgrade.** A failed `xdp_mode: native` attach auto-downgrades to `skb` and the
  link still comes up, with the achieved mode surfaced as the `NativeDowngradedSkb` mimic Node
  Condition (status `ok`). (#230)
- **Native-XDP pre-deploy capability probe.** The agent reports each node's egress-NIC native-XDP
  capability (a pure-sysfs heuristic → `metrics["native_xdp"]`) so the node editor warns before
  `native` is selected on a NIC that can't do it. (#231)

## [2.0.0-rc.1] - 2026-07-03

**Release candidate.** Promotes the soaked beta line to release-candidate status with **zero code
changes since `v2.0.0-beta.18`** (the delta is this gate paperwork) — an rc carries no new features
by policy, and this one carries no new fixes either: everything it ships already soaked as beta.18
on the owner's live fleet.

The go/no-go gate ([`docs/spec/rc1/RC1-GATE.md`](docs/spec/rc1/RC1-GATE.md)) is **GO** with every
criterion satisfied: blocker-clean through two security audits plus the beta.17 hardening pass and
the beta.18 link-directionality fix (kernel-proven); all six CI gates green **and now required via
branch protection on `main`**; the owed-smoke ledger fully discharged by sustained live-fleet
operation across beta.9–beta.18, capped by the owner's clean 2026-07-03 fleet smokes; the
realtunnel 20/20 bake-in + negative proof on record. Deferred to rc.2/GA (documented, unchanged):
FileStore host-loss SPOF, bootstrap-TOFU first-fetch, pinned-endpoint anti-roaming, the
`EDGE_OMITEMPTY` `mimic_fallback` canonicalization gap.

## [2.0.0-beta.18] - 2026-07-03

Per-edge **link directionality**: a single-linked edge now deterministically kills the reverse-peer
race in which the auto-reverse peer dials the from-node's plain public IP, wins WireGuard's single
runtime-endpoint slot on a faster boot, and permanently bypasses a relay/accelerator path via
endpoint roaming — the residual behind the live fleet's "NAT override goes direct" symptom that
beta.17 diagnosed as roaming. Three reviewed plans (#221–#223), each independently reviewed and
adversarially verified before merge; proven end-to-end on a real kernel.

### Added
- **`link_direction` on edges** (`""`≡`both` default / `"forward"`): a `forward` single-linked
  edge's reverse peer keeps its full `[Peer]` stanza (AllowedIPs, transit addressing, Babel
  routing, return traffic) but carries **no `Endpoint` line**, so it can never initiate and never
  race the forward dial. Default `both` compiles byte-identical for every existing topology (zero
  churn across every pre-existing success golden; fixture-15's fail golden was deliberately
  extended to cover the new codes); allocation is provably direction-blind —
  toggling the field moves no port, transit IP, link-local, or pin. There is deliberately **no
  `"reverse"` value** (one spelling — dual spellings would tax every future direction-aware rule):
  single-linking the other way is an explicit edge flip in the editor. (#221)
- **Editor + canvas UX**: the edge editor gains a direction select labeled with the real node names
  (`A ⇄ B` / `A → B` / `B → A`); choosing `B → A` performs a visible **flip** — from/to swap, the
  pin pairs mirror (allocation-stable), stale dial fields clear, and the newly-dialed node's public
  host prefills. Doubly-linked edges show a **reverse-dial readout** (where the to→from dial
  resolves: explicit reverse edge / from-node public endpoint / passive), mirroring the compiler's
  resolution exactly. Single-linked edges carry a `→` chip on the canvas; doubly-linked edges
  render exactly as before. The edge label pill now actually selects the edge (its cursor always
  suggested it), mirroring React Flow's own selection semantics. (#222)
- **Validation (4 new codes, both compilers, en+zh)**: invalid enum value; direction on a
  pair-folding edge (would be silently ignored); `forward` without an `endpoint_host` (provably
  dead link); direction on a client edge. All loud errors, mirroring the beta.17
  require-explicit-host precedent; the panel additionally sanitizes out-of-enum values to `both`
  on its own load paths. (#221)
- **Real-kernel proof**: realtunnel scenario `c4` — with BOTH routers dialable, the suppressed
  side's rendered config carries no `Endpoint` line and the tunnel still forms from the dialer's
  inbound handshake alone, converging and routing both ways (CI additive tier). (#223)
- **Docs**: normative `edge.md` §Link direction + `peer-derivation.md` resolution rule 0 and the
  roaming note's harmful special case; bilingual wiki guidance ("when to single-link —
  accelerators & relays"). (#223)

### Fixed
- **The TS validator never mirrored `validation_edge_mimic_fallback_invalid`** (a pre-existing
  Go↔TS gap discovered during this work): a bad `mimic_fallback` passed in-browser Validate but
  failed the Go compile. Mirrored and now exercised by the conformance corpus. (#221)

## [2.0.0-beta.17] - 2026-07-02

A pre-rc.1 hardening pass: nine reviewed plans closing the security + robustness gaps surfaced in the
pre-rc.1 audit, each independently reviewed and adversarially verified before merge. Headlined by a
**CRITICAL** self-update keystone-bypass fix. Owner fleet smoke gates promotion to Latest and the
subsequent `rc.1` cut.

### Security
- **CRITICAL — self-update keystone membership is verified on the deferred-retry swap path.** The
  agent's deferred self-update retry swapped a downloaded binary after verifying only the bundle
  signature, skipping the keystone membership gate the primary apply path enforces. The retry now runs
  `VerifyBundle` **then** `VerifyMembership` (fail-closed; a no-op when keystone is off), matching the
  primary path — so a downloaded binary can never be applied without the same custody check. (plan-2)
- **WireGuard public keys are validated at every ingress.** A strict base64 Curve25519 pattern
  (43 chars + `=`, regex-anchored so an embedded newline can't slip through `base64.DecodeString`)
  rejects malformed keys at schema, enrollment (before the single-use token burns), rekey, and
  manual-node validation — closing a config-injection vector into the root-run install script. (plan-4)
- **Agent routes are hardened against DoS.** A per-node fixed-window request-rate limit on the agent
  mux; boundary caps on `/report` + `/telemetry` payloads (conditions/metrics count, key + value size,
  condition-field size); an in-memory FileStore telemetry overlay so a 30s heartbeat no longer fsyncs a
  whole-record rewrite; and a trusted-proxy-aware client-IP (`X-Forwarded-For` honored only from
  `YAOG_TRUSTED_PROXIES`, right-to-left skipping trusted hops) so the per-IP limiters work behind a
  reverse proxy. (plan-5)
- **The bootstrap agent binary is SHA-256-pinned.** The one-shot bootstrap verifies the downloaded
  agent binary against the operator's configured per-arch pin (fail-closed `sha256sum -c`) **before**
  install, closing the first-contact binary trust-on-first-use; an unpinned arch warns loudly and
  proceeds (the operator's explicit, visible choice). (plan-6)
- **Node IDs are charset-validated.** A node ID reaches path/file/interface-name sinks (the deploy
  script filename spliced into a root shell, the manual-bundle `Content-Disposition`), so it now rejects
  spaces, slashes, and shell metacharacters at the compile root. (plan-7)

### Added
- **`agent kit verify`.** A hand-installing operator can verify an already-downloaded manual-node bundle
  (Ed25519 signature + per-file checksums + keystone membership) **before** `sudo bash install.sh` — the
  same fail-closed gate a managed agent applies. Reads only public material; no controller contact.
  (plan-8)
- **Host resource telemetry.** The agent emits host load + memory via the existing Sampler framework
  (pure `/proc` reads, best-effort); the node detail page shows a live load/memory readout (live-only,
  stripped from the persisted cache like the per-peer link detail). (plan-10)

### Fixed
- **"Update failed" is now a distinguishable, reasoned, persistent state.** An abandoned self-update
  surfaces as an **error** condition (distinct from the transient `Blocked` warning), carries a curated
  failure reason durable across applies, and drops the panel's now-misleading best-effort caveat for the
  authoritative structured condition. (plan-9)
- **A port-only NAT override is rejected, not silently dropped.** An edge with an `endpoint_port`
  override but no `endpoint_host` — which the compiler would silently drop while the panel still showed
  a "NAT override active" badge — is now a clean validation error (require-explicit-host), and the
  frontend keeps the two fields coupled. Also documents WireGuard endpoint **roaming**: a `wg show`
  endpoint that differs from the `.conf` for a peer behind DNAT+SNAT is expected behavior, not a defect.
  (plan-1)

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

[Unreleased]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-rc.7...HEAD
[2.0.0-rc.7]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-rc.6...v2.0.0-rc.7
[2.0.0-rc.6]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-rc.5...v2.0.0-rc.6
[2.0.0-rc.5]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-rc.4...v2.0.0-rc.5
[2.0.0-rc.4]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-rc.3...v2.0.0-rc.4
[2.0.0-rc.3]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-rc.2...v2.0.0-rc.3
[2.0.0-rc.2]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-rc.1...v2.0.0-rc.2
[2.0.0-rc.1]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.18...v2.0.0-rc.1
[2.0.0-beta.18]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.17...v2.0.0-beta.18
[2.0.0-beta.17]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.16...v2.0.0-beta.17
[2.0.0-beta.16]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.15...v2.0.0-beta.16
[2.0.0-beta.15]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.14...v2.0.0-beta.15
[2.0.0-beta.14]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.13...v2.0.0-beta.14
[2.0.0-beta.13]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.12...v2.0.0-beta.13
[2.0.0-beta.12]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.11...v2.0.0-beta.12
[2.0.0-beta.11]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.10...v2.0.0-beta.11
[2.0.0-beta.10]: https://github.com/kunori-kiku/yet-another-overlay-generator/compare/v2.0.0-beta.9...v2.0.0-beta.10
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
