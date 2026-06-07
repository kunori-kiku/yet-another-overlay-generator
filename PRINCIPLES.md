# PRINCIPLES — YAOG (Yet Another Overlay Generator)
<!-- updated: 2026-06-07 -->

Project-wide invariants. Every outline's Principles section inherits from these and may add
subject-specific ones on top. The execute-implementation-plan skill loads this file during its
principle-risk assessment.

## Hard principles (HIGH risk — silent violation invalidates results)

- **Generated configs must be deployable.** Every rendered artifact must be accepted by its
  consumer (`wg-quick`, `babeld`, `sysctl`, `bash`). A compile that reports success but produces a
  dead or rejected config is the worst failure class. Examples of violation: emitting
  `Table = off` with no route installer active; ports > 65535; placeholder keys; malformed
  router-id passed through.
- **Allocation stability (superset rule).** Recompiling a superset topology MUST reproduce
  identical allocated values (overlay IP, ports, transit pairs, link-locals, keys) for every
  pre-existing entity. Examples of violation: order-dependent counters renumbering existing
  links; re-randomizing a node's WireGuard key on recompile.
- **Generated scripts run as root on fleets.** No unescaped user-controlled text may reach a
  shell context in install/deploy scripts; integrity must anchor in Go-emitted constants, not in
  files the payload itself carries. Examples of violation: interpolating `ssh_host` or node name
  via bare `%s` into bash; self-extracting installer with no payload hash.
- **Backend is the sole port authority.** The frontend never allocates ports and never
  auto-stamps `endpoint_port`; a nonzero `endpoint_port` is an explicit operator NAT override
  only. Examples of violation: copying `public_endpoints[0].port` onto an edge at draw time.

## Project-wide standards

- **Backward compatibility of persisted topologies (MEDIUM):** topology JSON / localStorage from
  prior releases must load and compile after every change; new model fields are
  `omitempty`/optional.
- **Stateless compiler (MEDIUM):** all allocation state rides the topology JSON; no server-side
  databases or files.
- **Protect the working self-/32 Babel announce path (MEDIUM):** it is the one announce mechanism
  verified to work; changes near `babel.go` redistribute logic must prove it byte-identical.
- **Minimal dependencies (LOW):** Go stdlib `net/http` only; sole external dep is
  `golang.zx2c4.com/wireguard/wgctrl`.
- **Local toolchain absence:** Go and npm are NOT installed on the dev machine; all
  verification runs in CI on PRs.
- **No `--no-verify`, no amends, no force-push.**

## Domain context

YAOG is a declarative control plane and code generator for WireGuard+Babel overlay networks:
a React/Vite designer compiles a topology JSON via a Go backend into per-node config bundles
(per-peer WG interfaces, babeld.conf, sysctl, root-executed install/deploy scripts). Users
deploy the generated artifacts to real fleets over SSH — so generation bugs become outages,
security bugs become root compromises, and allocation instability becomes fleet-wide redeploys.
Catastrophic failure modes: silently dead overlays, wrong-identity deploys, command injection,
fleet key rotation. Cosmetic failure modes: stale UI labels, preview formatting.

Analogous prior art: wg-quick config conventions, Tailscale's stable-identity model (keys/IPs
persist per node), Terraform's plan/apply determinism expectations (recompile = no diff for
unchanged inputs).
