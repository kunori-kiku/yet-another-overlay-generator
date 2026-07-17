# Node

A Node represents a physical or virtual host in the overlay.

| Field | Type | Description |
|---|---|---|
| `id` | string | Unique identifier |
| `name` | string | Human-readable name (used in WG interface names) |
| `hostname` | string | OS hostname (optional) |
| `platform` | `"debian" \| "ubuntu"` | Target OS for install script |
| `role` | `"peer" \| "router" \| "relay" \| "gateway" \| "client"` | Node role |
| `deployment_mode` | `"managed" \| "manual" \| ""` | Controller delivery mode, orthogonal to role. Empty is managed. Managed nodes become deployment-ready through approved enrollment; manual nodes carry a validated public key in topology and never enroll. |
| `domain_id` | string | Reference to owning Domain |
| `overlay_ip` | string | Assigned overlay IP (auto or manual) |
| `listen_port` | int | WireGuard base listen port (default: 51820) |
| `mtu` | int | WireGuard MTU (0 = system default, typically 1420) |
| `xdp_mode` | `"skb" \| "native" \| ""` | mimic XDP attach mode for `transport: "tcp"` links; empty = `skb` (generic XDP, compatible with NICs lacking native-XDP support, the default). `native` is faster but needs driver support. See [../artifacts/mimic.md](../artifacts/mimic.md) |
| `router_id` | string | Babel router-id (MAC-48 or IPv4 form); operator-settable in the panel for non-client roles, else auto-generated from the SHA-256 of the node ID when empty (meaningless for `client`, which is warned). FE/Go field parity is guarded by the conformance drift manifest (milestone 1.5) |
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
| `telemetry_probes` | []TelemetryProbe | Optional managed-node ICMP, TCP, or URL checks. ICMP/TCP use an IP-or-DNS `host` (TCP also requires `port`); URL uses its separately typed absolute HTTP(S) `url` and expected success status. An unfinished destination may remain in a saved draft, but a ready node cannot preview/stage until it is completed or removed. |
| `telemetry_devices` | *TelemetryDevicePolicy | Optional managed-node automatic device discovery. The only current mode is `all-eligible-v1`; the operator opts in once and the agent discovers eligible local disks, filesystems, and GPUs rather than receiving a hand-authored device list. |

Role semantics are specified in [../roles/roles.md](../roles/roles.md).

## Active telemetry model

```go
type TelemetryProbe struct {
    ID                  string `json:"id"`
    Name                string `json:"name,omitempty"`
    Type                string `json:"type"`
    Host                string `json:"host,omitempty"`
    Port                int    `json:"port,omitempty"`
    URL                 string `json:"url,omitempty"`
    ExpectedStatus      int    `json:"expected_status,omitempty"`
    IntervalSeconds     int    `json:"interval_seconds,omitempty"`
    TimeoutMilliseconds int    `json:"timeout_milliseconds,omitempty"`
}

type TelemetryDevicePolicy struct {
    Mode string `json:"mode"` // "all-eligible-v1"
}
```

Probe `id` is stable and unique within the node. `name` is optional controller/Fleet presentation
metadata only: it is excluded from executable bundle policy and agent reports. Executable/history
identity is the ID plus exact typed destination—`type + host + port` for ICMP/TCP, or
`type + url + expected_status` for URL. ICMP forbids a port; TCP requires one. URL probes do not
overload `host`, use a fixed GET at runtime, and treat status 200 as success when
`expected_status` is omitted. The actual returned status is live result context, not topology and
not a chart series; latency and availability are charted.

All active telemetry is managed-node policy authorized at Deploy, not an arbitrary command surface.
Manual nodes cannot carry it. The renderer keeps the strict version-1 `telemetry.json` member for
legacy ICMP/TCP-only policy and uses the mutually exclusive version-2 `telemetry-policy.json` when
URL or device fields are present. Both project `name` away and are checksum/keystone covered before
activation. `telemetry_devices` is deliberately a small opt-in selector; discovered inventory and
numeric samples are runtime telemetry and never topology fields. Bounds, defaults, destination
validation, agent capability rollout, and chart behavior are specified in
[../operations/active-telemetry.md](../operations/active-telemetry.md).

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

**Controller custody (AgentHeld).** The cases above describe the **AirGap** custody mode (the
default for the air-gap CLI and HTTP API). When a node is rendered by the controller in **AgentHeld**
mode, the node persists **only** `wireguard_public_key`: the private key lives agent-side and never
reaches the controller, and the renderer emits a placeholder for the node's own `Interface
PrivateKey`. Case (b) — public key without a private key — is then the *normal* path rather than a
hard error. The air-gap cases are unchanged. See
[../controller/key-custody.md](../controller/key-custody.md).

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
