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

## Install ladder (distro → pinned GitHub `.deb`)

The generated `install.sh` installs mimic with a fallback ladder (`internal/renderer/script.go`,
the `.HasMimic` block), so a node on a distro that does not yet package mimic still comes up:

1. **Distro package first.** If `mimic` is in the node's repositories (Debian 13+, Arch AUR,
   OpenWrt experimental, …), the package manager installs it — unchanged from before.
2. **Pinned GitHub `.deb` fallback** (apt/dpkg systems only — Debian 12 / Ubuntu 24.04, where
   mimic is not yet packaged). The installer derives `<codename>-<arch>` from `/etc/os-release` +
   `dpkg --print-architecture`, reads the matching pin from `artifacts.json`
   (`.mimic.release_url` + `.mimic.debs["<codename>-<arch>"].{asset,sha256}`), downloads
   `${GH_PROXY}${release_url}/${asset}` with `curl --proto '=https,http'`, and **verifies the
   bytes against the pinned SHA-256 with `sha256sum -c` before `apt-get install`**. mimic's `.deb`
   builds its eBPF module via DKMS, so kernel headers + dkms + gcc are pulled in first. The whole
   script runs under `set -euo pipefail`, so a hash mismatch or a missing pin **fails closed** —
   no unverified `.deb` is ever installed. A non-apt distro without the package errors out clearly
   rather than silently skipping mimic.

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
  `"bookworm-amd64"`) to `{asset, sha256}`. The stage/promote path threads these into
  `render.FetchSettings`, which emits `artifacts.json` into each node's signed bundle.
- **Air-gap / local mode** — there is no controller, so supply the same pins out-of-band
  (plan-7). Point `YAOG_ARTIFACT_CATALOG` at a JSON file in the **same shape as the emitted
  `artifacts.json`** (`{ "schema": 1, "mimic": { "version", "release_url", "debs": { "<codename>-<arch>": {"asset","sha256"} } } }`)
  — a controller-emitted `artifacts.json` round-trips directly. `YAOG_GITHUB_PROXY` sets the
  mirror prefix and `YAOG_MIMIC_VERSION` overrides the version label. `cmd/compiler` also exposes
  `--artifact-catalog` / `--gh-proxy` / `--mimic-version` layered over those env vars (flag wins).

> **Runbook (per mimic release):** 1) open the mimic GitHub release page and note the release
> base URL (`.../releases/download/<tag>`); 2) for each supported `<codename>-<arch>` download the
> `.deb` and compute `sha256sum <file>`; 3) record `version`, `release_url`, and one
> `debs["<codename>-<arch>"] = {asset: "<filename>", sha256: "<hex>"}` per asset; 4) in controller
> mode save those to settings, or in air-gap mode write them into the catalog JSON. A node whose
> `<codename>-<arch>` has no pin falls through to a clear error (it never installs an unpinned
> `.deb`).

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
- **One filter per mimic link**, keyed on that link's allocated listen port. mimic's filter form is
  `"{local|remote}={ip}:{port}"` (IPv6 in brackets); a node aggregates one filter per local mimic
  listen port into its single egress config. (Exact directive/file syntax is taken from mimic's
  source during implementation; this spec fixes the model, not the byte format.)
- **MTU −12** on each mimic WireGuard interface, emitted explicitly in the `.conf`
  (`(node MTU or 1420) − 12`).
- **Kernel/eBPF required**: the `mimic` kernel module is loaded by the packaged systemd unit's
  `Requires=modprobe@mimic.service` (verified in `mimic@.service`), so enabling `mimic@<egress>`
  pulls the module in at start and at boot — an explicit `/etc/modules-load.d` entry is not
  required. The installer emits an explicit eBPF/bpffs gate plus a branch on the unit-start result;
  on a kernel without eBPF the link either fails closed (the install aborts — a clear, hard failure)
  or falls back to plain UDP, per the per-link policy below.
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
  eBPF/bpffs), `ebpf_load_failed` (`mimic@<egress>` failed to start), `install_failed` (distro pkg +
  pinned `.deb` both unavailable), and `fell_back_to_udp` (skipped mimic, link up as UDP); the success
  case is `active`. A `.deb` download/integrity FAILURE always fails closed regardless of policy — we
  never proceed past a failed SHA-256 verify, nor silently mask it as a de-cloak.
- **Breadcrumb → Node Condition.** The installer writes the outcome to a small JSON marker at
  `/var/lib/yaog-agent/mimic-status.json` (`model.MimicBreadcrumbPath`), keyed by the closed Go
  constants `model.MimicOutcome*` — never raw stderr. The agent reads it each cycle and emits a
  structured `mimic` Node Condition (`KernelTooOld` / `EbpfLoadFailed` / `InstallFailed` /
  `FellBackToUDP` / `Active`) with a curated one-line message, so the panel shows *why* mimic is down
  without a log dump. A fallback is always a `warn` condition (it de-cloaks the link — surface it).
- **Deployable in both branches.** On fallback the link comes up as plain UDP (endpoint/port are
  mimic-independent; the MTU−12 conf is conservative-safe for UDP), and any half-applied mimic filter
  is de-provisioned so no orphaned shaping survives.

## Verification

Real-host smoke: two Linux nodes joined by one `tcp` edge → deploy → `mimic@<egress>` active, the
WireGuard handshake completes through it, and `tcpdump` on the egress shows TCP-shaped packets where
the WG flow would otherwise be UDP.
