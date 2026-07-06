# mimic TCP-shaping transport

When an edge sets `transport: "tcp"` ([../data-model/edge.md](../data-model/edge.md) §TCP transport),
its WireGuard link(s) are wrapped by [mimic](https://github.com/hack3ric/mimic): an eBPF (TC/XDP)
program that rewrites the UDP header to look like TCP in place, and restores it on the far end.
WireGuard is otherwise unchanged — it still dials the real `peer:port`.

## Positioning (normative — do not overclaim)

mimic is a **connectivity/performance tool for UDP-hostile networks**: it bypasses UDP QoS
throttling and UDP port blocking, and recovers throughput on paths that degrade UDP (upstream's
stated purpose is "bypass UDP QoS and port blocking"). It is **NOT a censorship- or
DPI-circumvention tool**: it does not resist active probing or sophisticated DPI. State-grade
censorship resistance needs a different, more intricate engine (reality/vless class) and is out of
scope. UI, docs, and spec MUST NOT describe mimic as anti-censorship.

## Keyless

mimic has no password, pre-shared key, or authentication — the transform is purely structural.
WireGuard provides all encryption and authentication. Therefore **no secret material is added** to
the topology: `transport: "tcp"` is the entire signal on the edge; there is no `pinned_mimic_*`
field. mimic is protocol-shaping, not confidentiality (see [../security/security.md](../security/security.md)).

## Not bundled

YAOG ships no mimic binary. It generates mimic's config and the install command only — the same
relationship it has with `wg-quick` and `babeld`. Because YAOG does not distribute mimic, mimic's
GPL-2.0 license imposes no obligation on YAOG's own code.

## Direct path required (no L7 relay)

mimic's fake-TCP transform needs **L3/L4 packet transparency end to end**: the shaped packets must
reach the far end unmodified so its eBPF can restore them. An **L7 / UDP-accelerator relay** that
terminates and re-originates the connection (a gost/realm-style relay doing DNAT+SNAT) breaks this —
the reverse fake-TCP leg is `RST`'d — so a link that traverses such a relay must use `transport: udp`,
not `tcp`.

YAOG warns at design time: an **enabled** `transport: tcp` edge whose `type` is `relay-path` produces
the `validation_edge_mimic_relay_path` validation warning advising `transport: udp`. It is a
**warning, not a hard error** — deploy is not blocked (a relay may in fact be L3/L4-transparent), but
the common relayed-link mistake is surfaced.

## Install ladder (distro → pinned GitHub `.deb`)

The generated `install.sh` installs mimic with a fallback ladder (`internal/renderer/script.go`,
the `.HasMimic` block), so a node on a distro that does not yet package mimic still comes up:

1. **Distro package first.** If `mimic` is in the node's repositories (Debian 13+, Arch AUR,
   OpenWrt experimental, …), the package manager installs it — unchanged from before.
2. **Pinned GitHub `.deb` fallback** (apt/dpkg systems only — Debian 12 / Ubuntu 24.04, where
   mimic is not yet packaged). Upstream `hack3ric/mimic` ships **two** packages per
   `<codename>-<arch>`: `mimic` (userspace) **and** `mimic-dkms` (the DKMS eBPF module, which
   `Provides` the virtual package `mimic-modules` that the `mimic` package `Depends` on). The
   installer derives `<codename>-<arch>` from `/etc/os-release` + `dpkg --print-architecture`, reads
   **both** pins from `artifacts.json` (`.mimic.release_url` +
   `.mimic.debs["<codename>-<arch>"].{asset,sha256,dkms_asset,dkms_sha256}`), downloads each over
   `${GH_PROXY}${release_url}/…` with `curl --proto '=https,http'`, **verifies each against its
   pinned SHA-256 with `sha256sum -c`**, and installs **both together**
   (`apt-get install ./mimic.deb ./mimic-dkms.deb`) — installing only `mimic` cannot satisfy
   `Depends: mimic-modules` and apt aborts (the rc.1 live-fleet `exit status 100`). The `mimic-dkms`
   package builds the module via DKMS, so kernel headers + dkms + gcc — plus **`bubblewrap`** and
   **`dwarves`** — are `_pm_install`ed first (in the provisioning step *and* again in the
   `_mimic_module_ready` module-build retry). mimic-dkms's DKMS build needs `bwrap` (the bubblewrap
   build sandbox) and `pahole` (from `dwarves`, for BTF generation) but declares **neither** as a
   dependency, so without them the build fails **even on a current kernel with headers present**
   (`make: bwrap: No such file` → Error 127, then `pahole: not found`); YAOG installs them defensively
   (upstream `mimic-dkms` should `Depend` on them). A node
   whose running kernel is behind its repo's current point release (so `linux-headers-$(uname -r)`
   is no longer in the repo) cannot build the module until it **reboots into the current kernel** —
   until then the link degrades per its `mimic_fallback` policy (below).

   The provisioning block is a shell **function that returns non-zero on any failure** (no `set -e`
   abort inside), so the caller honors the link's `mimic_fallback` policy — degrade to plain UDP
   (`udp`) or fail closed with a categorized breadcrumb (`none`) — instead of aborting the whole
   node apply before the fallback logic runs. Success is gated on the DKMS **kernel module actually
   loading** (`lsmod` / `modprobe`, with a `dkms autoinstall` retry) — **not** merely on the `mimic`
   binary existing — so a node whose module never built (the stale-kernel case above) is classified
   `module_unavailable` and honors policy, instead of proceeding to a cryptic `mimic run` exit-22 loop.
   Every download is SHA-256-verified before dpkg; no unverified `.deb` is ever installed. A non-apt
   distro without the package errors out clearly.

   `mimic@<egress>` is then `enable`d (for boot) and **`restart`ed** (not a no-op `enable --now`
   start), so a redeploy RE-APPLIES the freshly-written `/etc/mimic/<egress>.conf` — WireGuard is
   down during this phase, so the restart is not disruptive.

If no mimic catalog was configured for the deploy there is no `artifacts.json` (see the air-gap
default below), and the fallback step prints a clear error instead of guessing a download.

## Trust chain for the pinned `.deb`

The GitHub `.deb` is fetched over untrusted transport (github.com / a `GH_PROXY` mirror), so its
integrity rests entirely on the pin — and the pin rides the **same signature the rest of the
bundle already has**, adding no new trust primitive:

```
sha256 pin  ∈  artifacts.json  ∈  bundleFiles  ∈  checksums.sha256  ∈  bundle.sig (Ed25519)  ∈  keystone trust-list
```

`artifacts.json` is a `bundleFiles` member (`internal/artifacts/export.go`), so its bytes are in
the canonical `checksums.sha256` the controller signs and the keystone trust-list binds
(specs/artifacts-signing.md, specs/keystone-trustlist.md). The agent verifies `bundle.sig` and the
keystone membership **before** `install.sh` runs, and `install.sh` reads the pin from
`$SCRIPT_DIR/artifacts.json` only after that verification — so reading the pin is not itself a
trust boundary, and the `GH_PROXY` mirror cannot substitute a different `.deb` without failing the
SHA-256 check. `GH_PROXY` is a deploy-network preference baked into `install.sh` (shell-escaped),
deliberately kept OUT of the signed catalog so changing the mirror does not churn the bundle
digest. The agent self-update block of `artifacts.json` is reserved for the agent's own binary and
is covered in `agent-selfupdate.md` (created in plan-9, this directory).

## Populating the catalog (manual, per release)

The catalog is **manual** for beta (no controller→GitHub automation, D1): an operator copies the
exact asset filenames + SHA-256s from a mimic GitHub release into the catalog. Both modes feed the
identical `artifacts.json`:

- **Controller mode** — set the operator-editable `ControllerSettings` fields
  (`internal/controller/store.go`): `MimicVersion`, `MimicReleaseBase` (the release base URL the
  `.deb` is fetched from), and `MimicDebs` — a map keyed `"<codename>-<arch>"` (e.g.
  `"bookworm-amd64"`) to `{asset, sha256, dkms_asset, dkms_sha256}` — the userspace `mimic` pin
  AND its `mimic-dkms` companion (both required on split-package distros). The panel's **Discover
  from release** pairs the two `.deb` assets for one `<codename>-<arch>` into a single row (its
  `deriveKey` recognizes the `-dkms` asset); **Assist** fetches both `.sha256` sidecars and, on a
  gh-proxy miss, retries the direct GitHub URL. The stage/promote path threads these into
  `render.FetchSettings`, which emits `artifacts.json` into each node's signed bundle.
- **Air-gap / local mode** — there is no controller, so supply the same pins out-of-band
  (plan-7). Point `YAOG_ARTIFACT_CATALOG` at a JSON file in the **same shape as the emitted
  `artifacts.json`** (`{ "schema": 2, "mimic": { "version", "release_url", "debs": { "<codename>-<arch>": {"asset","sha256","dkms_asset","dkms_sha256"} } } }`).
  The schema bumped 1→2 for the `dkms_*` companion; a legacy schema-1 (`{asset,sha256}`-only)
  catalog still loads (back-compat, the loader rejects only a schema NEWER than supported) but
  installs only `mimic` and so fails on split-package distros — degradable under `mimic_fallback=udp`
  — a controller-emitted `artifacts.json` round-trips directly. `YAOG_GITHUB_PROXY` sets the
  mirror prefix and `YAOG_MIMIC_VERSION` overrides the version label. `cmd/compiler` also exposes
  `--artifact-catalog` / `--gh-proxy` / `--mimic-version` layered over those env vars (flag wins).

> **Runbook (per mimic release):** 1) open the mimic GitHub release page and note the release
> base URL (`.../releases/download/<tag>`); 2) for each supported `<codename>-<arch>` note BOTH the
> `<codename>_mimic_<ver>_<arch>.deb` and its `<codename>_mimic-dkms_<ver>_<arch>.deb`, and compute
> `sha256sum <file>` for each; 3) record `version`, `release_url`, and one
> `debs["<codename>-<arch>"] = {asset, sha256, dkms_asset, dkms_sha256}` per `<codename>-<arch>`;
> 4) in controller mode use **Discover from release** (it pairs the two assets into one row) +
> **Assist** (it fetches both `.sha256` sidecars), or in air-gap mode write them into the catalog
> JSON. A node whose `<codename>-<arch>` has no pin — or no `dkms_*` companion — degrades per its
> `mimic_fallback` policy rather than installing a broken/unpinned set.

## Air-gap byte-identity (D4)

With **no** catalog configured (zero `render.FetchSettings`), `artifacts.json` is **omitted**
entirely and `install.sh` carries no fetch branch, so the signed bundle is **byte-identical** to a
pre-mimic-catalog deploy. This is the air-gap byte-identity HIGH principle, enforced by the
perpetual `internal/render` equivalence/signing gates: configuring a catalog is purely additive,
and the default deploy is unchanged.

## Deployment model

- **Attaches to the egress NIC**, not the WireGuard interface: one `mimic@<egress>` systemd unit
  reading `/etc/mimic/<egress>.conf`. The egress NIC is the node's default-route interface, detected
  at install time (`ip route show default`), not known at compile time.
- **XDP attach mode is per-node and operator-controlled** via `Node.xdp_mode`. The generated config
  writes `xdp_mode = <mode>`: the default (empty → `skb`) uses **generic/SKB XDP**, which works on
  virtually all NICs including VPS virtio NICs that lack native-XDP driver support — so the overlay
  comes up out of the box without NIC detection. An operator who knows a node's NIC supports
  driver-level XDP MAY set `xdp_mode: "native"` for higher throughput. YAOG does **not** auto-detect
  native capability (reliably probing it from a shell script is fragile, and mimic already probes
  internally); the explicit per-node override is the supported mechanism. Validator accepts only
  empty/`skb`/`native`.
- **Two filter families per node**, all OR'ed by mimic's whitelist (`"{local|remote}={ip}:{port}"`,
  IPv6 bracketed):
  - `local=<egress_ip>:<listen_port>` — one per local mimic listen port. Catches the **listen**
    direction (peers that dial in to us). `<egress_ip>` is the default-route source detected at
    install time (`ip route get`).
  - `remote=<peer_ip>:<peer_port>` — one per mimic peer this node **dials** (the peer's known
    endpoint, resolved to an IP at install time). Because the peer endpoint is **route-independent**,
    this filter matches the obfuscated flow even when the kernel picks a different local source IP than
    the egress probe found — the fix for the failure mode where a single guessed `local=` IP diverges
    from WireGuard's real on-the-wire source on a **multi-homed / secondary-IP / policy-routed** host
    (mimic's match is an exact lookup with no wildcard, so a one-octet IP mismatch shapes nothing and
    the link silently drops to plain UDP).
  - **Loopback guard**: a `local=` egress IP that resolves to `127.0.0.0/8` or `::1` (e.g. `1.1.1.1`
    null-routed) is rejected rather than written as a guaranteed-dead filter; the node reports the
    `egress_unresolved` breadcrumb and applies its per-link fallback policy (UDP or fail-closed).
  - **Known residual limitations**: (a) mimic attaches one unit on the *default-route* egress
    interface; a peer whose route egresses a *different* interface (policy routing / a dedicated WAN) is
    not covered — per-peer egress-interface attach is a future item. (b) A **dual-stack hostname**
    endpoint is resolved to a single IP at install time (`getent`, first result) independently of which
    family WireGuard actually dials, so the `remote=` filter can key on the non-dialed family; the
    `local=` lines still cover the listen direction. **For mimic links, prefer IP-literal endpoints**
    (or a single-family hostname) so the `remote=` filter is unambiguous.
  (Exact directive/file syntax is taken from mimic's source; this spec fixes the model, not the byte format.)
- **MTU −12** on each mimic WireGuard interface, emitted explicitly in the `.conf`
  (`(node MTU or 1420) − 12`).
- **Kernel/eBPF required**: the `mimic` kernel module is loaded by the packaged systemd unit's
  `Requires=modprobe@mimic.service` (verified in `mimic@.service`), so enabling `mimic@<egress>`
  pulls the module in at start and at boot — an explicit `/etc/modules-load.d` entry is not
  required. The installer emits an explicit eBPF/bpffs gate plus a branch on the unit-start result;
  on a kernel without eBPF the link either fails closed (the install aborts — a clear, hard failure)
  or falls back to plain UDP, per the per-link policy below.
- **Phase-0 teardown (unconditional).** Every install-path deploy first `systemctl disable --now`s any
  stale `mimic@*.service` instance and removes `/etc/mimic/*.conf` **before** re-provisioning.
  Previously mimic teardown lived only in the `--uninstall` path, so flipping a node's **last** `tcp`
  link to `udp` (the node no longer runs mimic) never stopped the old `mimic@` — it kept shaping
  packets WireGuard was now sending as plain UDP and the link stayed broken. With the unconditional
  teardown a `tcp→udp` transition cleanly de-provisions mimic, while a node that still has a mimic link
  re-provisions in Phase 3.
- **Ordering**: mimic is provisioned and `mimic@<egress>` is started **before** `wg-quick up`, so the
  shaping is in place when the tunnel comes up. Uninstall stops/disables `mimic@<egress>`, removes
  its config, and detaches.

## UDP fallback (per-link policy)

Mimic needs a recent kernel (eBPF). When it cannot be provisioned, a link can either **fail closed**
(preserving mimic's censorship-evasion guarantee) or **fall back to plain UDP** (so a too-old kernel
does not block connectivity). This is a **per-link policy** (`Edge.mimic_fallback`: `""` inherit /
`"udp"` / `"none"`) inheriting a **fleet-wide default** (`ControllerSettings.mimic_fallback_default`,
shipped `"none"`); the compiler resolves the effective per-link value into `PeerInfo.MimicFallback`.

- **Per-node resolution (all-udp-or-fail-closed).** mimic provisioning is per-NODE (one shared
  `mimic@<egress>` unit serves all the node's mimic ports), but the policy is per-link. The installer
  falls back to UDP only when **every** mimic link on the node resolves to `"udp"`; a single `"none"`
  link forces fail-closed for the whole node, so a `"none"` link is never silently de-cloaked by a
  sibling `"udp"` link.
- **Failure categories.** The installer detects, with explicit checks: `kernel_too_old` (no
  eBPF/bpffs), `install_failed` (the two-package `.deb` install — distro or pinned GitHub — could not
  complete: a missing pin, a failed download/checksum, an unsatisfiable `Depends: mimic-modules`),
  `module_unavailable` (the `.deb` installed the `mimic` **binary** but the DKMS **kernel module** did
  not build/load for the running kernel — the dominant real case is a **stale kernel** whose
  `linux-headers-$(uname -r)` were pruned from the repo, so DKMS is stuck at `added`). Provisioning's
  success gate is that the module is genuinely loadable (`lsmod` / `modprobe` / a `dkms autoinstall`
  retry), **not** merely that `command -v mimic` succeeds — the binary-only check was the rc.2 defect
  that let a node with an unbuilt module proceed to a cryptic `mimic run` exit-22 loop. Also
  `ebpf_load_failed` (`mimic@<egress>` failed its eBPF attach at start),
  `egress_unresolved` (no routable default-route source IP — empty or loopback — so a `local=` filter
  could never match), `native_downgraded_skb` (a requested `xdp_mode=native` attach failed →
  auto-retried and active in **skb** mode; see below), and `fell_back_to_udp` (skipped mimic, link up
  as UDP); the success case is `active`. A `.deb` whose SHA-256 verify FAILS is **never installed**
  (integrity is absolute); every failure (including `module_unavailable`) then follows the link's
  policy — `udp` degrades to plain UDP, `none` fails closed — but a corrupt/tampered `.deb` is never
  dpkg'd either way.
- **Breadcrumb → Node Condition.** The installer writes the outcome to a small JSON marker at
  `/var/lib/yaog-agent/mimic-status.json` (`model.MimicBreadcrumbPath`), keyed by the closed Go
  constants `model.MimicOutcome*` — never raw stderr. The agent reads it each cycle and emits a
  structured `mimic` Node Condition (`KernelTooOld` / `InstallFailed` / `ModuleUnavailable` /
  `EbpfLoadFailed` / `EgressUnresolved` / `FellBackToUDP` / `NativeDowngradedSkb` / `Stopped` / `Active`) with a
  curated one-line message, so the panel shows *why* mimic is down (or in skb) without a log dump. A
  UDP fallback is a `warn` condition (it de-cloaks the link — surface it); `ModuleUnavailable` is a
  `warn` with a *"reboot into the current kernel, or set mimic_fallback=udp"* hint (a stale kernel
  can't build the module); `NativeDowngradedSkb` is `ok` (mimic IS active — only the requested XDP mode
  changed). **The condition is not a frozen deploy-time snapshot:** each heartbeat the agent re-probes
  the live unit with `systemctl is-active mimic@<egress>` (timeout-guarded), and when the breadcrumb
  says mimic should be running (outcome `active` / `native_downgraded_skb`) but the unit is **not**
  active now, it reports a live `Stopped` `warn` instead of a stale `Active` — so a `systemctl stop`,
  a crash, or a flap shows up in the panel.
- **Robust (re)start.** Before `systemctl restart mimic@<egress>`, the installer clears a wedged unit
  (`systemctl stop` + `rm -f /run/mimic/*.lock` + `systemctl reset-failed` + `modprobe mimic`) so an
  **orphaned `/run/mimic` lock** from an uncleanly-exited prior instance (a `failed to lock … File
  exists` → exit-17 loop, systemd rate-limiting the retries) can't wedge the restart. A node has
  exactly one mimic egress, so clearing all `/run/mimic` locks while the unit is stopped is safe.
- **Deployable in both branches.** On fallback the link comes up as plain UDP (endpoint/port are
  mimic-independent; the MTU−12 conf is conservative-safe for UDP), and any half-applied mimic filter
  is de-provisioned so no orphaned shaping survives.

> **Caveat — `udp` fallback can SPLIT a link.** Fallback is resolved **per node** at apply time, so it
> can leave a link asymmetric: if one end degrades to plain UDP while the other end still built mimic
> and keeps shaping, the shaping end sends fake-TCP the UDP end cannot decode and the link **dies**.
> `mimic_fallback: udp` is therefore a safety net against a *hard outage*, **not** a clean bilateral
> solution. For a node that genuinely cannot build mimic, the clean fix is `transport: udp` on that
> edge (both ends); the live `mimic` condition (a `Stopped` / `ModuleUnavailable` warn) and the
> relay-path warning above now help surface which node to fix.

## Native XDP mode (skb default, native opt-in, auto-downgrade)

mimic attaches its eBPF program in either **skb** (generic XDP — portable, the default) or **native**
(driver-mode XDP — faster, but requires NIC driver support many VPS virtio NICs lack). `Node.xdp_mode`
selects it (`""`/`"skb"` → skb; `"native"` → native), written into `/etc/mimic/<egress>.conf`.

- **Deploy-time auto-downgrade.** When `xdp_mode=native` and the `mimic@<egress>` attach fails, the
  installer rewrites the config to `skb`, resets the failed unit, and **retries once** — a NIC without
  native XDP comes up in skb instead of failing the deploy. The achieved mode surfaces as the
  `native_downgraded_skb` breadcrumb → the `NativeDowngradedSkb` Node Condition (status `ok`). Because
  `mimic@` is `restart`ed each deploy (not a no-op start), the downgrade RE-EVALUATES every deploy, so
  the on-disk config never drifts back to a stale `native` a reboot would start from and fail.
- **Pre-deploy capability heuristics (always-visible).** The agent reports two best-effort,
  pure-inspection signals (no shell, no build, no live-NIC attach) so the panel can warn **before** a
  deploy rather than after a failed apply:
  - `metrics["native_xdp"]` = `{capability, driver, kernel}` — the egress NIC's native-XDP capability
    (`/proc/net/route` iface → `/sys/class/net/<if>/device/driver` → `/proc/sys/kernel/osrelease`):
    `supported` / `unsupported` / `conditional` (virtio_net) / `unknown`. The node editor shows it
    **always** (not only once `native` is selected), so an operator sees support up front.
  - `metrics["mimic_capability"]` = `{capability, kernel}` — whether this node can build/load the mimic
    module at all (`/proc/modules` loaded → `/lib/modules/<k>/modules.dep` built →
    `/lib/modules/<k>/build` headers present): `ready` / `buildable` / `unbuildable`. `unbuildable` (a
    stale kernel with pruned headers — the fleet case) warns in the node editor when the node has a tcp
    link, foreseeing the `module_unavailable` outcome before deploy.
  The DEFINITIVE answers stay the deploy-time conditions (`ModuleUnavailable` / the achieved XDP mode);
  the heuristics are advisory.

## Egress interface (auto-detect + per-node override)

mimic binds to the node's **egress interface** — by default auto-detected as the default-route NIC
(`ip route show default … dev`), with the egress IP from `ip route get 1.1.1.1 … src`.
`Node.mimic_egress_interface` overrides it (empty = auto-detect, byte-identical to today): set it (e.g.
`wan0`) on a multi-homed / policy-routing node where the WireGuard egress is NOT the default route.
When overridden the installer binds that interface (shq-escaped) and derives the egress IP from it
(`ip -o -4 addr show dev <iface>`); the Phase-0 cleanup uses the same override so it stops the right
`mimic@<iface>` unit. A schema validator rejects an implausible name (`^[A-Za-z0-9._-]{1,15}$`, Go +
TS byte-equal). The value is a design-time input rendered into the **signed** `install.sh` (covered by
`checksums.sha256` → `bundle.sig` → keystone), so it cannot be tampered without failing the signature
(`TestExport_EgressOverrideIsSigned`).

## Verification

Real-host smoke: two Linux nodes joined by one `tcp` edge → deploy → `mimic@<egress>` active, the
WireGuard handshake completes through it, and `tcpdump` on the egress shows TCP-shaped packets where
the WG flow would otherwise be UDP.
