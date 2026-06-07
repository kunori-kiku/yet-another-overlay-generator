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
relationship it has with `wg-quick` and `babeld`. mimic is installed from the node's distribution
(Debian 13+, `.deb` for Debian 12 / Ubuntu 24.04, Arch AUR, OpenWrt experimental). Because YAOG
does not distribute mimic, mimic's GPL-2.0 license imposes no obligation on YAOG's own code.

## Deployment model

- **Attaches to the egress NIC**, not the WireGuard interface: one `mimic@<egress>` systemd unit
  reading `/etc/mimic/<egress>.conf`. The egress NIC is the node's default-route interface, detected
  at install time (`ip route show default`), not known at compile time.
- **One filter per mimic link**, keyed on that link's allocated listen port. mimic's filter form is
  `"{local|remote}={ip}:{port}"` (IPv6 in brackets); a node aggregates one filter per local mimic
  listen port into its single egress config. (Exact directive/file syntax is taken from mimic's
  source during implementation; this spec fixes the model, not the byte format.)
- **MTU −12** on each mimic WireGuard interface, emitted explicitly in the `.conf`
  (`(node MTU or 1420) − 12`).
- **Kernel/eBPF required**: the `mimic` kernel module is loaded by the packaged systemd unit's
  `Requires=modprobe@mimic.service` (verified in `mimic@.service`), so enabling `mimic@<egress>`
  pulls the module in at start and at boot — an explicit `/etc/modules-load.d` entry is not
  required. The installer additionally emits an advisory eBPF/bpffs check; the **authoritative gate**
  is that `systemctl enable --now mimic@<egress>` runs under `set -euo pipefail`, so on a kernel
  without eBPF the service fails to start and the install aborts (a clear, hard failure).
- **Ordering**: mimic is provisioned and `mimic@<egress>` is started **before** `wg-quick up`, so the
  shaping is in place when the tunnel comes up. Uninstall stops/disables `mimic@<egress>`, removes
  its config, and detaches.

## Verification

Real-host smoke: two Linux nodes joined by one `tcp` edge → deploy → `mimic@<egress>` active, the
WireGuard handshake completes through it, and `tcpdump` on the egress shows TCP-shaped packets where
the WG flow would otherwise be UDP.
