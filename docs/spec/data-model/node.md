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
| `fixed_private_key` | bool | Whether to preserve WG private key across compiles |
| `wireguard_private_key` | string | WG private key (only with fixed_private_key) |
| `wireguard_public_key` | string | WG public key (derived) |
| `public_endpoints` | []PublicEndpoint | Public IP/port mappings for endpoint selection |
| `extra_prefixes` | []string | Additional CIDR prefixes to announce (gateway use) |
| `ssh_alias` | string | SSH config Host alias for auto-deploy |
| `ssh_host` | string | SSH host address |
| `ssh_port` | int | SSH port (default: 22) |
| `ssh_user` | string | SSH username (default: root) |
| `ssh_key_path` | string | SSH private key file path |

Role semantics are specified in [../roles/roles.md](../roles/roles.md).

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
