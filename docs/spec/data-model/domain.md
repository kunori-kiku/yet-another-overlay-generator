# Domain

A Domain represents an overlay address space.

| Field | Type | Description |
|---|---|---|
| `id` | string | Unique identifier |
| `name` | string | Human-readable name |
| `cidr` | string | Overlay CIDR (e.g., `10.0.0.0/24`) |
| `allocation_mode` | `"auto" \| "manual"` | IP allocation strategy |
| `routing_mode` | `"babel" \| "static" \| "none" \| ""` | Routing protocol. See normalization rule below. |
| `reserved_ranges` | []string | CIDRs/IPs excluded from auto-allocation |
| `transit_cidr` | string | Point-to-point link address pool (default: `10.10.0.0/24`) |

## `routing_mode` normalization and rejection

`babel` is the only implemented routing mode. The validator MUST normalize and gate the field:

- An empty `routing_mode` MUST be normalized to `babel` during validation, written back so the
  value round-trips (a topology submitted with `""` returns carrying `babel`).
- `static` and `none` are reserved and MUST be rejected with a localized not-yet-implemented error
  until a route installer exists for them. Empty values MUST be normalized to `babel` before the
  enum check rather than bypassing it.

The full enum contract — including why `Table = off` and babeld `redistribute local` rules depend
on the mode being `babel` — lives in [../compiler/routing-modes.md](../compiler/routing-modes.md).

> **Compliance:** the schema validator accepts `static`, `babel`, and `none` as equally valid and
> skips the check for an empty value (`internal/validator/schema.go:103-107`), so non-babel and
> empty modes pass validation but render no working routes (dossier D2/D72). Closed by Plan 6
> (PR #6).
