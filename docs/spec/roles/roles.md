# Node Roles and Semantics

Each role has specific network semantics that determine capabilities, Babel behavior, and
AllowedIPs strategy.

| Role | IP Forward | Accept Inbound | Runs Babel | Babel Announce | AllowedIPs Mode |
|---|---|---|---|---|---|
| **peer** | No | No | Yes | Self /32 only | point-to-point |
| **router** | Yes | If has public IP | Yes | Self + Domain CIDR + extra prefixes | point-to-point |
| **relay** | Yes | Yes (always) | Yes | Self + Domain CIDR + extra prefixes | relay-all (domain CIDR) |
| **gateway** | Yes | If has public IP | Yes | Self + Domain CIDR + extra + default route | gateway (domain + 0.0.0.0/0) |
| **client** | No | No | No | None | client (single wg0) |

## Capability Inference

The compiler automatically infers certain capabilities from role:
- `router`: `can_forward = true`
- `relay`: `can_forward = true`, `can_relay = true`, `can_accept_inbound = true`
- `gateway`: `can_forward = true`
- `client`: all capabilities forced to `false`
