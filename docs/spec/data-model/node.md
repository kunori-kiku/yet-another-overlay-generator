# Node

A Node represents a physical or virtual host in the overlay.

| Field | Type | Description |
|---|---|---|
| `id` | string | Unique identifier |
| `name` | string | Human-readable name (used in WG interface names) |
| `hostname` | string | OS hostname (optional) |
| `platform` | `"debian" \| "ubuntu"` | Target OS for install script |
| `role` | `"peer" \| "router" \| "relay" \| "gateway" \| "client"` | Node role |
| `domain_id` | string | Reference to owning Domain |
| `overlay_ip` | string | Assigned overlay IP (auto or manual) |
| `listen_port` | int | WireGuard base listen port (default: 51820) |
| `mtu` | int | WireGuard MTU (0 = system default, typically 1420) |
| `router_id` | string | Babel router-id in MAC-48 format (auto-generated from SHA-256 of node ID) |
| `capabilities` | NodeCapabilities | Network capabilities |
| `fixed_private_key` | bool | Operator-pasted private key opt-in (the paste affordance; see below). No behavior keys on the flag alone — its presence implies a private key is set |
| `wireguard_private_key` | string | WG private key; **round-trips through the topology JSON by design** so a stateless compiler can re-render the node's own `Interface PrivateKey` (see below) |
| `wireguard_public_key` | string | WG public key; non-empty **without** a private key is a hard error; otherwise derived from the private key on every compile (see below) |
| `public_endpoints` | []PublicEndpoint | Public IP/port mappings for endpoint selection |
| `extra_prefixes` | []string | Additional CIDR prefixes to announce (gateway use) |
| `ssh_alias` | string | SSH config Host alias for auto-deploy |
| `ssh_host` | string | SSH host address |
| `ssh_port` | int | SSH port (default: 22) |
| `ssh_user` | string | SSH username (default: root) |
| `ssh_key_path` | string | SSH private key file path |

Role semantics are specified in [../roles/roles.md](../roles/roles.md).

## Key persistence semantics

A node's WireGuard key pair MUST be stable across recompiles once assigned (invariant I5 in
[../compiler/allocation-stability.md](../compiler/allocation-stability.md)). I5 under a **stateless
compiler** has a binding consequence: the **private key MUST round-trip through the topology JSON**.
A node persisting only its public key cannot render its own `Interface PrivateKey` on the next
compile, so the private key — not just the public key — is written back onto the node and carried in
the persisted topology (and browser localStorage). This is the same trust surface the existing
`fixed_private_key` paste path already accepts; the private key was always operator-controllable
material. See [../security/security.md](../security/security.md) — the persisted topology is secret
material.

`generateKeys` ([`internal/api/handler.go`](../../../internal/api/handler.go)) branches on the
**state of the two key fields**, not on the `fixed_private_key` flag (the flag is only the paste
affordance; its presence implies a private key is set, which is case (a) below):

- **(a) `wireguard_private_key` non-empty** (regardless of `fixed_private_key`). The compiler parses
  the private key, **derives** the public key from it, and **reuses** the pair. The derived public
  key is written back onto the node, healing a missing or stale `wireguard_public_key`. This is the
  steady state after a node has been compiled once: every subsequent compile reuses the same pair,
  so adding an unrelated node never rotates an existing node's key.
- **(b) `wireguard_private_key` empty but `wireguard_public_key` non-empty.** **Hard error.** The
  node is key-fixed (it carries a public key) but its private key is absent, and a stateless
  compiler cannot reconstruct the private key. The operator MUST either paste the live private key
  (read from the host's `/etc/wireguard/<iface>.conf`) into `wireguard_private_key`, or clear
  **both** key fields to rotate explicitly.
- **(c) both fields empty.** Genuinely new node. The compiler generates a fresh pair and writes
  **both** `wireguard_private_key` and `wireguard_public_key` back onto the node so they persist and
  round-trip. (This replaces the old blank-the-keys behavior; the next compile reuses them via case
  (a).)

**Rotation is explicit.** A key changes only when the operator clears **both** key fields (forcing a
fresh generation via case (c)) or pastes a different `wireguard_private_key` (case (a) with a new
key). Re-randomization MUST NOT occur as a side effect of any other edit.

`fixed_private_key` stays accepted as the operator paste affordance — the way an operator supplies a
node's live private key during the one-time migration described in
[../compiler/allocation-stability.md](../compiler/allocation-stability.md). Because its presence
means the private key is set, it lands in case (a); no behavior is keyed on the flag alone anymore.
Secret-handling rules apply to any pasted private key — and now to every persisted node key — they
are live credentials.

## PublicEndpoint

```go
type PublicEndpoint struct {
    ID   string `json:"id"`
    Host string `json:"host"`
    Port int    `json:"port"`
    Note string `json:"note,omitempty"`
}
```

## NodeCapabilities

| Field | Type | Description |
|---|---|---|
| `can_accept_inbound` | bool | Can receive unsolicited connections |
| `can_forward` | bool | Forwards packets between interfaces |
| `can_relay` | bool | Acts as a relay for other nodes |
| `has_public_ip` | bool | Has a publicly routable IP address |
