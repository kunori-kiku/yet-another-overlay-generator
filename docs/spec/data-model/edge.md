# Edge

An Edge represents a unidirectional connection intent ("from actively connects to to").

| Field | Type | Description |
|---|---|---|
| `id` | string | Unique identifier |
| `from_node_id` | string | Source node ID |
| `to_node_id` | string | Destination node ID |
| `type` | `"direct" \| "public-endpoint" \| "relay-path" \| "candidate"` | Connection type |
| `endpoint_host` | string | Target endpoint IP/hostname |
| `endpoint_port` | int | User-specified port (0 = use compiler-allocated port) |
| `compiled_port` | int | Read-only: actual port allocated by compiler |
| `priority` | int | Connection priority |
| `weight` | int | Connection weight |
| `transport` | `"udp" \| "tcp"` | Transport protocol |
| `is_enabled` | bool | Whether this edge is active |
| `notes` | string | Free-form notes |

Port allocation behavior (how `endpoint_port` interacts with the compiler-allocated per-peer
listen ports) is specified in [../compiler/peer-derivation.md](../compiler/peer-derivation.md).
