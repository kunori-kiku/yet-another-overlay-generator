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
| `fixed_private_key` | bool | Operator-pasted private key opt-in (key persistence + migration; see below) |
| `wireguard_private_key` | string | WG private key (carried only when `fixed_private_key` or pasted for migration) |
| `wireguard_public_key` | string | WG public key; **non-empty ⇒ node is key-fixed and the key is reused** (see below) |
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
[../compiler/allocation-stability.md](../compiler/allocation-stability.md)). The persistence
trigger is the **presence of a public key**, not a boolean flag:

- **Key-fixed (non-empty `wireguard_public_key`).** The node already has a key. The compiler MUST
  reuse the existing key pair and MUST NOT generate a fresh one. This makes growth non-disruptive:
  adding an unrelated node never rotates an existing node's key.
- **New (empty `wireguard_public_key`).** The node has no key yet. The compiler generates a fresh
  key pair and MUST persist the resulting public key back onto the node, so the next compile sees
  it as key-fixed.
- **Explicit rotation only.** A key changes only when the operator explicitly clears the node's key
  fields (forcing regeneration) or pastes a new `wireguard_private_key`. Re-randomization MUST NOT
  occur as a side effect of any other edit.

`fixed_private_key` is the operator opt-in for **supplying** a private key: when set with a
non-empty `wireguard_private_key`, the compiler derives the matching public key from it. It is the
field used to paste a node's live private key during the one-time migration described in
[../compiler/allocation-stability.md](../compiler/allocation-stability.md). Secret-handling rules
apply to any pasted private key — it is a live credential.

> **Compliance:** `generateKeys` currently keys off the boolean `fixed_private_key` and, for
> non-fixed nodes, generates a random key on every compile while blanking the node's stored key
> (`internal/api/handler.go:267-317`, esp. `:302-314`) — so non-fixed keys rotate every compile.
> The "non-empty public key ⇒ reuse" rule is closed by the sticky-pin allocation work
> (see [../compiler/allocation-stability.md](../compiler/allocation-stability.md)).

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
