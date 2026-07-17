# Render and key custody

<!-- last-verified: 2026-07-17 -->

## Responsibility

Own WireGuard key preparation under AirGap or AgentHeld custody and render a compiled topology into
WireGuard, Babel, sysctl, install, telemetry-policy, artifact-catalog, and deploy-helper text maps
(`internal/render/render.go:34-238,279-445`).

## Files

- `internal/render/render.go:34-238` — defines custody, the private-key placeholder, injected keygen,
  and key preparation.
- `internal/render/render.go:279-445` — orchestrates all primitive renderers into `CompileResult` maps.
- `internal/renderer/wireguard.go:42-139` — renders per-link interfaces and client `wg0`.
- `internal/renderer/babel.go:98-251` and `internal/renderer/sysctl.go:16-40` — render routing and
  kernel settings.
- `internal/renderer/script.go:210-275,388-430,501-558` — renders install scripts, custody splice,
  and transit-pool source-address handling.
- `internal/renderer/deploy.go:51-90` — selects ordinary SSH helpers or AgentHeld fail-closed guidance.

## Inputs

The canonical `localcompile` facade first supplies a copied topology, custody mode, and injected
key generator to `GenerateKeysWith`; after `compiler-allocation` it supplies the result, resolved
fetch settings, and the same optional signer snapshot to `AllWith`
(`internal/localcompile/compile.go:58-104`). Primitive renderers consume derived peer/client maps,
node/domain policy, and prepared key pairs (`internal/render/render.go:318-445`).

## Outputs

Key preparation returns a node-ID key map and writes only the custody-appropriate public/private
fields onto the copied topology (`internal/render/render.go:155-178,180-238`). Rendering fills the
result's WireGuard, Babel, sysctl, install, deploy, artifacts, and mutually exclusive telemetry-policy
maps; `artifacts-signing` owns their canonical packaging and integrity metadata
(`internal/render/render.go:318-445`).

## Decision points (if any)

- AgentHeld treats a registered public key as authoritative, clears any private field, and emits the
  deliberately invalid `PRIVATEKEY_PLACEHOLDER`; a node with no usable public half fails
  (`internal/render/render.go:45-56,159-190`).
- AirGap reuses and normalizes a supplied private key, rejects a public-only node because the private
  half cannot be reconstructed, or generates and writes back a fresh pair when both fields are empty
  (`internal/render/render.go:192-238`).
- Node role and custody select client `wg0` versus per-peer interfaces/installers, version-1 versus
  successor telemetry policy, and ordinary deploy helpers versus agent-only guidance
  (`internal/render/render.go:327-445`).

## Invariants

- Controller/AgentHeld rendering never returns a real private key; only the node-side installer
  replaces the exact placeholder in the copied configuration after integrity verification
  (`internal/render/render.go:159-190`, `internal/renderer/script.go:222-258`).
- `localcompile` copies every topology collection before key/default/allocation writeback, so the
  canonical pipeline returns normalized output without mutating its caller
  (`internal/localcompile/compile.go:58-69`).
- An injected signer snapshot supplies the install-script verification key, and the same object flows
  to `artifacts-signing`; nil keeps the unsigned render byte-compatible
  (`internal/render/render.go:304-316,348-365`, `internal/localcompile/compile.go:95-104`).

## Gotchas (optional)

- Custody preparation intentionally precedes schema/compiler work in `localcompile`; diagrams and
  alternative entry points must preserve that order (`internal/localcompile/compile.go:58-98`).
- AgentHeld deployment helpers do not execute `install.sh` directly because that would bypass the
  off-host keystone membership gate (`internal/renderer/deploy.go:59-69`).
- The installer derives its SNAT source pool from the node domain's `transit_cidr` and falls back to
  the shared allocation constant; do not hard-code a second fallback
  (`internal/renderer/script.go:388-430`).
