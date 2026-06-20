# `test/realtunnel` — real-tunnel netns integration tier (plan-18 / 3.6)

This is the **MANDATORY rc.1-gating** integration tier. Every other YAOG test asserts on *bytes* —
the configs the compiler renders, golden-compared. This tier asserts on the *kernel*: it brings up
the configs `cmd/compiler` actually generates (per-peer WireGuard interfaces + `babeld` + the
**unmodified** `install.sh` activation sequence) inside per-node containers and then checks that the
overlay **works** — tunnels handshake, babel converges routes to peers' overlay IPs, packets ping end
to end, and the SNAT transit→overlay source rewrite fires. It is the only place the data plane is
exercised, so a regression that renders valid-but-non-functional configs (a reordered `install.sh`,
a broken SNAT rule, a dropped endpoint) is caught here and nowhere else.

It is **test-only** and lives behind `//go:build linux && integration`, so it is invisible to the
default build, `go vet ./...`, `go test ./...`, and every other CI job. Zero production code.

## Execution model (Option B — unmodified `install.sh` under real systemd)

Each node becomes its own `systemd-nspawn` container booting real `systemd` (`--boot`), on a shared
underlay bridge (the "internet" the WireGuard `Endpoint`s dial, deliberately disjoint from the
overlay CIDRs so a route to an overlay IP can only exist via a formed tunnel + babel). The node's
exported bundle is bind-mounted read-only and brought up by running **the exact `install.sh` that
ships to operators** — no extract-and-run, no command rewriting. `--volatile=overlay` keeps each
boot's writes in tmpfs so the base rootfs is reused, never mutated.

Because the activation is the unmodified script, the harness's assertions *assume* the script's
command shapes (`dummy0`, `wg-quick up`, `babeld -c`, the SNAT rule). `template_pin_test.go` greps a
freshly-rendered `install.sh` for those shapes and goes **red** if `internal/renderer/script.go`
drifts — forcing the script and this harness to be reconciled in the same PR. That test needs no root.

## Prerequisites

- **root** (the suite `t.Skip`s cleanly without it — never a false failure)
- the **WireGuard kernel module** (`sudo modprobe wireguard`)
- `systemd-nspawn` (`sudo apt-get install systemd-container`)
- a **base rootfs** carrying `systemd` + `wireguard-tools` + `babeld` + `iproute2` +
  `iptables`/`nftables` + `iputils-ping`, pointed to by `REALTUNNEL_ROOTFS`. Build it once:

  ```bash
  sudo debootstrap --variant=minbase --components=main,universe \
    --include=systemd,systemd-sysv,udev,dbus,wireguard-tools,babeld,iproute2,iptables,nftables,iputils-ping,kmod \
    noble /tmp/yaog-rt-rootfs http://archive.ubuntu.com/ubuntu/
  export REALTUNNEL_ROOTFS=/tmp/yaog-rt-rootfs
  ```

  (Locally, swap the mirror for one that is reachable from your network.)

If any prerequisite is missing the relevant test **skips** with a message naming exactly what to fix.

## Running

```bash
# Compile as your user (reuses the module cache), run as root (root needs no Go toolchain):
go test -c -tags integration -o /tmp/realtunnel.test ./test/realtunnel/
sudo REALTUNNEL_ROOTFS=/tmp/yaog-rt-rootfs /tmp/realtunnel.test -test.v

# Or in one shot (root must be able to find `go` + the module cache):
sudo REALTUNNEL_ROOTFS=/tmp/yaog-rt-rootfs go test -tags integration ./test/realtunnel/...

# The no-root template-shape pin alone:
go test -tags integration -run TestTemplateShapePin ./test/realtunnel/...

# Additive scenarios (opt-in, non-gating): C3 + relay + nat-hub
sudo REALTUNNEL_ROOTFS=/tmp/yaog-rt-rootfs REALTUNNEL_SCENARIOS=all /tmp/realtunnel.test -test.v

# Negative proof (red-proof the gate has teeth — GREEN when the fault is caught):
sudo REALTUNNEL_ROOTFS=/tmp/yaog-rt-rootfs REALTUNNEL_NEGATIVE=drop-snat /tmp/realtunnel.test \
  -run TestNegativeProof -test.v
```

## The MVV floor vs the additive tier

**Required floor (gates rc.1) — `TestSimpleMeshCanary`** brings up the shipped `examples/simple-mesh`
(3 routers, full mesh) and asserts, on the kernel:

| # | Assertion | Helper |
|---|-----------|--------|
| a | per-interface WireGuard handshake | `requireHandshakes` |
| b | babel-converged kernel route to every node's `OverlayIP/32` | `requireRouteConvergence` |
| c | end-to-end overlay ping, 0% loss | `requireOverlayPing` |
| d | SNAT transit→overlay source rewrite (rule installed **and** functionally rewriting) | `requireSNATRewrite` |

`TestNspawnLifecycle` (the boot mechanism) and `TestTemplateShapePin` (anti-drift) also run on the
required path. Every wait is a **bounded poll** (hard timeout, fails loud) — never a fixed `sleep`.

**Additive tier (opt-in via `REALTUNNEL_SCENARIOS`, never gates rc.1):**

- **`c3`** — `TestC3OneDirectional`. The C3 reverse-endpoint regression guard
  (`testdata/c3-onedir/`). Two NAT-side peers dial one hub with no reverse edges: the
  `public_endpoints`-bearing peer (`has_public_ip=false`) MUST get a **populated** reverse `Endpoint`
  (the plan-8 `HasPublicIP` normalization in `roles.go` fired); the genuinely-unreachable peer (no
  `public_endpoints`) MUST get an **empty** one (correct one-directional). Revert the C3 fix and the
  rendered-config assertion goes red. The kernel run proves both tunnels still form (the peers dial
  in) and the overlay routes.
- **`relay`** — `TestRelayTopology` (`examples/relay-topology`). Two NAT peers with no direct edge
  reach each other only **through the relay** (relay forwarding + babel transit). All-pairs.
- **`nat-hub`** — `TestNatHub` (`examples/nat-hub`). Same shape with a **router** hub instead of a
  relay (exercises the router role's forwarding/announce derivation). All-pairs.

## Adding a scenario

1. Add (or reuse) a topology fixture — an `examples/<name>/topology.json` or a purpose-built
   `testdata/<name>/topology.json`.
2. Add a `Test<Name>(t)` in `scenarios_test.go` gated on `requireScenario(t, "<key>")`, then
   `bringUp(...)`, `onFailDump(t, sc)`, and the assertions.
3. If the topology does **not** provide full reachability (e.g. a point-to-point hub with no
   transit), pass a custom `reachPredicate` to `requireRouteConvergence` / `requireOverlayPing`
   instead of `allPairs`, so you assert only the paths the topology actually provides.
4. Document its key in the table above and in `RC1-GATE.md` if it ever becomes part of a gate.

## Tier-2 / Tier-3 upgrade path

This is **Tier 1** (single runner, `systemd-nspawn`, simple-mesh required). If a future need arises:

- **Tier 2** — add real NAT boxes / packet loss / asymmetric routing between the bridges to exercise
  endpoint-rebind + keepalive recovery (the residual plan-19 smoke, distinct from the C3 footprint).
- **Tier 3** — multi-runner / real-VM matrix for kernel-version coverage (e.g. mimic eBPF on ≥6.1,
  plan-19 §C3).

If Phase 5 ever surfaces a netns blocker that forces an assertion to demote (e.g. the conntrack/nat
hook is unavailable on a runner kernel), record the blocker + demotion in `RC1-GATE.md` and here.

## CI

`ci.yml` → job **`realtunnel`** (required, once per PR: canary + lifecycle + template-pin; additive
scenarios run non-blocking via `continue-on-error`). `release.yml` → **`gate-realtunnel`** mirrors it
so a tag can't ship code that would have failed PR CI. `realtunnel-bakein.yml` (manual dispatch) runs
the 20× non-flake bake-in + the negative proof — the rc.1 precondition recorded in `RC1-GATE.md`. All
three share `.github/actions/realtunnel-setup` so their setup stays identical.
