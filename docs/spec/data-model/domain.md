# Domain

A Domain represents an overlay address space.

| Field | Type | Description |
|---|---|---|
| `id` | string | Unique identifier |
| `name` | string | Human-readable name |
| `cidr` | string | Overlay CIDR (e.g., `10.0.0.0/24`) |
| `allocation_mode` | `"auto" \| "manual"` | IP allocation strategy |
| `routing_mode` | `"static" \| "babel" \| "none"` | Routing protocol |
| `reserved_ranges` | []string | CIDRs/IPs excluded from auto-allocation |
| `transit_cidr` | string | Point-to-point link address pool (default: `10.10.0.0/24`) |
