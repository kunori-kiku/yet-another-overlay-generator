# Outline — mimic-tcp-transport (2026-06-07)

## 1. Mission

Give `transport: "tcp"` real meaning: an edge marked `tcp` has its WireGuard link(s) wrapped by
[mimic](https://github.com/hack3ric/mimic) (eBPF UDP→fake-TCP), so the overlay works and performs
on **UDP-hostile networks** — paths that throttle UDP (QoS), block UDP ports, or degrade UDP
throughput — by making WireGuard traffic look like TCP on the wire. The install script provisions
mimic from the node's distro, configures it for the link's port, and lowers MTU; WireGuard is
otherwise unchanged.

**Scope boundary (explicit):** mimic is a connectivity/performance tool for UDP-restrictive
networks, **NOT a censorship/DPI-circumvention tool.** It does not resist active probing or
sophisticated DPI; defeating state-grade censorship needs a more intricate engine (reality/vless
class) and is out of scope for this subject. Don't describe mimic as anti-censorship anywhere.

Success:
- A `tcp` edge between two Linux nodes compiles to mimic-marked interfaces (MTU −12) and an install
  script that installs mimic, detects the egress NIC, writes its config + filters, and brings
  `mimic@<egress>` up before `wg-quick`.
- Non-`tcp` topologies compile byte-identically (no drift).
- `transport: "tcp"` no longer warns (v1.3.0 reserved-warning removed) — it does something real.
- mimic pairs with parallel links: a plain primary + a `tcp` backup gives Babel cost-based failover
  onto the TCP-shaped path when the plain UDP one is throttled or blocked.

## 2. Principles (subject-specific; inherits /PRINCIPLES.md)

- **[STATED, HIGH] No drift.** Non-mimic interfaces and all existing topologies render
  byte-identically; mimic only adds behavior to `tcp` edges. Pinned by the perpetual gate.
- **[STATED, HIGH] Don't bundle mimic.** YAOG generates mimic config + the install command only —
  same relationship as `wg-quick`/`babeld`. No binary in the release; GPL-2.0 stays the operator's
  installed package, not YAOG's distributed code.
- **[VERIFIED, HIGH] mimic is keyless.** No password/PSK exists; the transform is structural and
  WireGuard provides all crypto. Do NOT invent a secret field. mimic is protocol-shaping, not
  encryption — and not authentication.
- **[STATED, HIGH] Honest scope.** Position mimic as UDP-restriction handling (QoS/port-block/
  throughput), never as censorship circumvention. Docs/UI/spec must not overclaim.
- **[INFERRED, HIGH] Generated scripts run as root.** mimic install/modprobe/systemd lines obey the
  existing shell-escaping + integrity rules; the eBPF/kernel requirement is checked, failing clearly.
- **[INFERRED, MED] Deployable artifacts.** A mimic config the kernel/mimic rejects is the worst
  failure class — validate kernel-eBPF availability at install time and the both-ends-Linux
  precondition at compile time.

## 3. Current state (2026-06-07)

- `main` @ `9bae3f2`; **v1.3.0 released**. `transport` enum is `{udp, tcp}`; `tcp` currently
  normalizes-valid but emits a reserved warning (PR #17) and renders as UDP.
- The transport `<select>` editor already exists (`RightPanel.tsx:831`).
- Per-peer interface model with allocated listen ports; parallel links + Babel cost failover live.

## 4. Verified mimic facts (from docs/source, 2026-06-07)

- eBPF TC/XDP UDP→fake-TCP; purpose (upstream's words): "bypass UDP QoS and port blocking". In
  Debian 13+, `.deb` for Debian 12 / Ubuntu 24.04, Arch AUR, OpenWrt (experimental). GPL-2.0-only.
  ~2.2 Gbps over WG.
- **Keyless** — no auth/password ([README](https://github.com/hack3ric/mimic)).
- Attaches to the **egress interface**: `mimic@<iface>` systemd unit; config `/etc/mimic/<iface>.conf`;
  CLI `mimic run <iface> -f "{local|remote}={ip}:{port}"` (IPv6 in brackets); `modprobe mimic`
  (+ `/etc/modules-load.d/mimic.conf`); `xdp_mode = skb|native` config directive.
- **MTU −12** for tunnels.
- **KNOWN UNKNOWN for plan-2:** exact config-file directive format and the `mimic@` unit's
  config-discovery. The executing session MUST read the repo's `docs/getting-started.md`,
  `docs/mimic.1.md`, and the packaged `eth0.conf.example` before writing the renderer.

## 5. Must-read references

Memory: [[parallel-links-subject-closed]], [[audit-plan-pipeline-state]] (method + merge gotchas).
Code (line numbers @ 9bae3f2):
- `internal/model/topology.go` Edge (transport field; NO new field needed)
- `internal/compiler/peers.go` `PeerInfo` struct (add `Mimic bool`, per-interface `MTU`),
  `deriveLinkCost` (unaffected), `DeriveClientConfigs`/`ClientPeerInfo` (~879, client mimic + MTU)
- `internal/renderer/wireguard.go:154` (per-peer MTU uses `node.MTU` → switch to `peer.MTU`),
  `:137` (client MTU)
- `internal/renderer/script.go` install template: Phase 1 `ensure_pkg`/`require_bin` (~280-316),
  systemd unit pattern (~335), `wg-quick up` phase (~490), `WgIfaceInfo`/`InstallScriptConfig`
  (~26-40, ~572-595), uninstall phases (~76-150)
- `internal/validator/schema.go` transport block (the v1.3.0 reserved warning to REMOVE),
  `internal/validator/semantic.go` (add mimic constraints)
- `frontend/src/components/layout/RightPanel.tsx:831` (transport option label)
Specs to amend: `data-model/edge.md`, NEW `artifacts/mimic.md`, `artifacts/install-script.md`,
`security/security.md`, `compiler/validation.md`, `api/wire-contract.md`.
Web: [mimic repo](https://github.com/hack3ric/mimic), [getting-started](https://github.com/hack3ric/mimic/blob/master/docs/getting-started.md),
[mimic.1](https://github.com/hack3ric/mimic/blob/master/docs/mimic.1.md), [Debian #1087937](https://bugs.debian.org/cgi-bin/bugreport.cgi?bug=1087937).

## 6. Decisions log

| # | Decision | Answer |
|---|---|---|
| 1 | Pursue | Draft + execute now as the next subject |
| 2 | Enum | Repurpose `tcp` value → means mimic; UI label "TCP (mimic)"; zero migration |
| 3 | Provisioning | Auto-install from distro (apt/pacman) in the install script + kernel-eBPF check |
| 4 | Secret | **DROPPED — mimic is keyless** (verified). No `pinned_mimic_secret`; `tcp` is the whole signal |
| 5 | Client edges | **Include clients now**; stop-loss: non-client-first via plan-2.5 if the wg0 path is messy |
| 6 | Attach model | mimic on the egress NIC (`mimic@<egress>`), egress detected at install time; one filter per mimic listen port |
| 7 | Positioning | UDP-restriction tool (QoS/port-block/throughput), **NOT censorship circumvention** — corrected mid-draft; honest scope everywhere |

## 7. Milestones

### Plan 1 — Contract amendment (docs-only PR) → `plan-1-2026_06_07.md`
Freeze: `tcp` = mimic (keyless, no new field); NEW `artifacts/mimic.md` (positioned as UDP-hostile-
network handling, NOT anti-censorship); install-script phase ordering + egress detection; security
note (protocol-shaping not encryption/auth; WG provides crypto; no secret); validation (remove
reserved warning; both-ends-Linux; kernel-eBPF at install). Stop-loss: resolve in review before code.

### Plan 2 — Backend mimic support (Go PR) → `plan-2-2026_06_07.md`
Compiler (mark mimic + per-interface MTU−12, clients included), renderer (per-iface MTU; script.go
mimic install/config/service/uninstall after reading mimic source), validator (drop warning + add
constraints), tests. Stop-loss: client path messy → plan-2.5 non-client-first; mimic config schema
differs → plan-2.6.

### Plan 3 — Frontend + docs sync (PR) → `plan-3-2026_06_07.md`
Relabel transport option "TCP (mimic)" + hint (works on UDP-restricted/throttling networks;
both-ends, Linux-only, MTU auto-lowered; NOT for censorship); wiki/docs transport note. No secret
UI (keyless). Stop-loss: none material.

## 8. Insertion-point markers

- **plan-2.5** — client-edge mimic deferred (ship non-client first).
- **plan-2.6** — mimic config schema differs materially from the port-filter model assumed here.

## 9. Closure criteria

- [ ] 3 PRs merged, CI green on main.
- [ ] Perpetual no-drift gate green (non-mimic byte-identical).
- [ ] Real-host smoke narrative (two Linux nodes, one `tcp` edge, mimic up, WG handshake through it,
  TCP-shaped packets on the wire) recorded in the plan-2 PR.
- [ ] specs match shipped behavior; v1.3.0 reserved-warning removed; positioning honest (no
  anti-censorship language).
- [ ] STATUS refreshed; subject archived to `_completed/`.

## 10. Plan status table

| Plan | Status | PR | Notes |
|---|---|---|---|
| plan-1 | pending | — | spec contract freeze |
| plan-2 | pending | — | backend mimic support |
| plan-3 | pending | — | frontend label + docs |
