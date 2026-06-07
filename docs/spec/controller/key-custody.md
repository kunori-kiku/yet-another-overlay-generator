# Key Custody (Phase 1a — zero-knowledge split-render)

This document defines **how WireGuard private keys are (not) handled** when a fleet is rendered by
the controller. It is the code half of the zero-knowledge custody decision in the
[controller-panel design spike](../../design/controller-panel-design-spike-2026_06_07.md): for
controller-managed nodes, private keys are generated and held **agent-side** and never reach the
controller, its database, or its bundles. The controller stores **public keys only**.

## Two custody modes

`render.GenerateKeys(topo, custody)` (`internal/render/render.go`) selects the model:

- **`AirGap`** — the historical, default behavior for the air-gap CLI (`cmd/compiler`) and the
  existing HTTP API. Private keys round-trip through the topology JSON so a stateless recompile
  reproduces them (invariant **I5**, key stability). A node with a public key but no private key is a
  **hard error** (the stateless compiler cannot reconstruct the private key). This path is **frozen
  and byte-for-byte unchanged**.
- **`AgentHeld`** — zero-knowledge custody for controller fleets. `GenerateKeys` **never returns a
  real private key**. For each node it uses the registered public key and emits
  `PrivateKeyPlaceholder` for the private half:
  - public key present → use it; private half = placeholder.
  - public key absent but a private key is present (e.g. an air-gap topology imported into the
    controller) → derive the public half, **discard** the private key (clear it on the node), private
    half = placeholder.
  - neither present → **hard error**: the agent must register a public key before the controller can
    render the node.

## The placeholder contract

`PrivateKeyPlaceholder` is the literal string **`PRIVATEKEY_PLACEHOLDER`**. It is emitted on the
node's own `[Interface] PrivateKey =` line (per-peer WG configs and the client `wg0` config). It is
intentionally **not valid base64**, so no WireGuard key parser can mistake it for a key.

The placeholder propagates without any renderer/compiler/validator change, because a node's private
key appears in exactly **one** place — its own `[Interface]`:

- the compiler never parses the private key (peer configs reference peers' **public** keys, and the
  client config copies the private-key field verbatim);
- the WG renderer emits `PrivateKey = {{ .PrivateKey }}` verbatim;
- the validators do no key-format validation.

So the `keys` map carries the placeholder from `GenerateKeys` straight through compile and render. The
agent (Phase 1b) splices its **locally-held** private key into the placeholder before the config is
applied — see [../artifacts/install-script.md](../artifacts/install-script.md) and the agent spec.

Everything else in the bundle (peer public keys, transit IPs, ports, MTU, Babel, sysctl, install.sh,
checksums, `bundle.sig`) is identical to the AirGap render for the same topology. A perpetual diff
test (`internal/render/custody_diff_test.go`) pins this: AgentHeld output equals AirGap output line
for line except the node's own `PrivateKey` line.

## Invariant I5 under AgentHeld

I5 (stable key, identified by public key) is **preserved**, with the *mechanism* downgraded from
private-key round-trip to public-key-only: the node holds one stable private key for its lifetime; the
controller persists the matching public key and renders against it every time. Recompiling reproduces
identical peer configuration because the public key (and therefore every derived value) is stable.
What changes is only that the controller never sees the private key.

## Guarantee and guard

The zero-knowledge guarantee — **no controller-rendered bundle ever contains a parseable WireGuard
private key** — is enforced by a perpetual CI gate (`internal/render/custody_guard_test.go`): it
renders a public-only fleet in `AgentHeld` mode and asserts every emitted `PrivateKey =` line carries
only the placeholder. This gate never retires; it is the standing guard against a split-render
regression reintroducing a key vault.

See also [../security/security.md](../security/security.md) (custody in the threat model) and
[signing.md](signing.md) (the bundle is signed over the same public-key-only content).
