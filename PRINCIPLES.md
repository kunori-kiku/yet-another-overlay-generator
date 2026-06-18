# PRINCIPLES — YAOG (Yet Another Overlay Generator)
<!-- updated: 2026-06-18 -->

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
- **Signed-artifact self-update custody.** An agent NEVER executes a self-fetched binary (its own
  replacement, or a mimic `.deb`) that it has not verified against a SHA-256 pin carried in the
  controller-signed, keystone-bound `artifacts.json` (a `bundleFiles` member, covered by
  `checksums.sha256` — `VerifyBundle` rejects a present-but-uncovered `artifacts.json`); and NEVER
  downgrades below the health-confirmed `AgentVersionFloor` (which advances ONLY after a swapped
  binary survives one clean cycle). The gh-proxy and github.com are untrusted transport. A bad
  swap must not be able to brick a node: a crash-durable breadcrumb is reconciled on every boot
  (promote on health / roll back to the prior binary / abandon at the attempt cap), bounding the
  systemd restart loop. Examples of violation: trusting the upstream `.sha256` sidecar (same
  untrusted transport as the binary); putting the pin in the unsigned `manifest.json`; changing
  `verify.go`'s signature path; advancing the floor before a health-confirmed update; an unbounded
  `Restart=always` swap loop. (Self-update touches only binaries — never WireGuard private keys, so
  the Key-custody zero-knowledge guarantee below is unaffected.)

## Project-wide standards

- **Backward compatibility of persisted topologies (MEDIUM):** topology JSON / localStorage from
  prior releases must load and compile after every change; new model fields are
  `omitempty`/optional.
- **Stateless compiler (MEDIUM):** all allocation state rides the topology JSON; no server-side
  databases or files. *Scoped exception (controller-panel 2.0):* the COMPILER/RENDERER packages stay
  pure and stateless; the new opt-in CONTROLLER (`internal/controller`, `cmd/agent`) is stateful by
  design (registry, audit log) — state + new deps are quarantined there and the air-gap path is
  unaffected. See `docs/design/controller-panel-design-spike-2026_06_07.md`.
- **Protect the working self-/32 Babel announce path (MEDIUM):** it is the one announce mechanism
  verified to work; changes near `babel.go` redistribute logic must prove it byte-identical.
- **Minimal dependencies (LOW):** Go stdlib `net/http` only; sole external dep is
  `golang.zx2c4.com/wireguard/wgctrl`. *Scoped exception (controller-panel 2.0):* new deps
  (Postgres driver, OIDC, KMS client) are permitted ONLY inside `internal/controller` / `cmd/agent`;
  the compiler/renderer dep set stays frozen, and signing uses stdlib `crypto/ed25519`.
- **Key custody (HIGH, controller fleets):** for nodes managed by the controller, WireGuard PRIVATE
  keys are generated and held agent-side and NEVER reach the controller, its DB, or its bundles
  (zero-knowledge custody). The controller stores public keys only. This downgrades I5's *mechanism*
  (private-key round-trip in JSON) to public-key-only for controller fleets while preserving I5's
  *guarantee* (stable key, identified by public key). The air-gap path's I5 behavior is unchanged.
- **Local toolchain:** Go IS installed locally (`$HOME/.local/go/bin`, go1.26.x) as of 2026-06-08 — run
  `go build ./... && go vet ./... && go test ./...` before pushing; CI remains the authoritative gate.
  npm/node (v20) are available too: run `cd frontend && npm run lint && npm run build` (the build uses
  `tsc -b`, stricter than a bare `tsc --noEmit`) before pushing frontend changes.
- **No `--no-verify`, no amends, no force-push.**

## Execution discipline (process invariants — HIGH risk to outcomes)

Owner directive (2026-06-18). These govern HOW work is executed, not just what ships.

- **No shims, monkey-patches, or ugly workarounds.** A fix lands at its structural root, never as a
  band-aid that defers the real problem. If the clean fix is large, it is still the fix — surface the
  cost, do not paper over it. A workaround that buys a green check now in exchange for hidden debt and a
  dirtier structure is a violation.
- **Structure-aware, hygienic code in every block.** Each change matches the surrounding code's idiom,
  naming, comment density, and package boundaries, and leaves the structure more logical, not less. Code
  hygiene (no CJK/English comment mixing, no truncated stubs, no dead code) is a first-class acceptance
  criterion, not a deferred follow-up.
- **No scope compromise to "close" work.** A plan or subject is closed only when its full
  Definition-of-done is met — comprehensive in scope and depth. Never narrow scope, skip a sub-item, or
  downgrade a deliverable merely to reach a stopping point. If scope genuinely cannot be met, STOP and
  surface it (authorize an insertion-point plan-N.5); do not silently shrink it.
- **Every PR is independently reviewed before merge, then re-reviewed after fixes.** Run an independent
  review (a review workflow) per PR; the review scope is four lenses — correctness, completeness
  (adherence to the plan/subject), code hygiene, and code structure. Fix all findings, then re-review
  until clean. Finish the whole subject before stopping. (MEMORY: review-each-pr-before-merge.)

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
