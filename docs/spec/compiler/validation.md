# Validation

## Pass 1: Schema Validation (`validator.ValidateSchema`)

Structural checks on the raw topology JSON:
- Required fields present (project ID/name, domain CIDR, node role, etc.)
- CIDR format validity
- Enum value validity (roles, routing modes, transport protocols)
- Port range validity (0–65535)
- No self-loops on edges

## Pass 2: Semantic Validation (`validator.ValidateSemantic`)

Cross-reference and logical checks:
- Node domain_id references exist
- Edge from/to node references exist
- Overlay IPs within domain CIDRs
- No duplicate IDs (domains, nodes, edges)
- No IP address collisions
- Listen port conflicts (same hostname)
- Isolated node detection (warning)
- NAT reachability warnings (double-NAT, no public endpoint)
- Client edge constraints (exactly one outbound, must target router/relay/gateway, must have endpoint_host)

## Validation coverage contract

The validator's job is to reject every input that cannot produce a deployable overlay *before* the
compiler or a root-executed install script ever sees it. Today the validated surface is a fraction of
the model surface (audit theme T4), so several field classes pass validation and then crash the
allocator, render non-deployable configs, or inject shell into root-executed scripts. The table below
is the **target coverage contract**: every model field, the pass that MUST validate it, and the rule.

Status column: `schema` / `semantic` = validated in that pass today; `none-yet` = no validation
exists yet (a gap to close); `n/a` = compiler-allocated, not user-supplied.

### Domain fields

| Field | Pass | Rule | Status |
|---|---|---|---|
| `id` | schema | non-empty; unique (semantic) | schema + semantic |
| `name` | schema | non-empty | schema |
| `cidr` | schema | non-empty, parseable, **IPv4-only** | partial — see below |
| `allocation_mode` | schema | enum `auto`/`manual`; empty allowed | schema |
| `routing_mode` | schema | empty normalizes to `babel`; `static`/`none` rejected as not-yet-implemented | none-yet |
| `reserved_ranges[]` | schema | each a parseable CIDR or IP | schema |
| `transit_cidr` | schema | parseable IPv4 CIDR with enough host pairs | none-yet |

> **Compliance — IPv4-only CIDR:** `validateDomainsSchema` accepts any address family
> (`net.ParseCIDR` at `schema.go:89`), so an IPv6 domain CIDR passes and then panics the IPv4-only
> allocator (`ipToUint32` slices `ip[12:16]` on a nil `To4()`, `ip.go:129,164-169` — D4/D35/D20). A
> `/0` CIDR overflows the host count (`uint32(1) << 32`, `ip.go:116`) into a multi-billion-iteration
> CPU spin (D56). Schema validation MUST add an IPv4-family guard and a CIDR-size bound (reject
> prefixes too small to allocate from, and too large to enumerate). Closed by Plan 3.

> **Compliance — `routing_mode`:** schema accepts `static`/`babel`/`none` as the valid enum
> (`schema.go:103-107`) and an empty value bypasses the enum check entirely (`schema.go:104`, D72).
> Per Decisions log #3 the contract is: empty → `babel`, and `static`/`none` are **rejected** as not
> yet implemented. Validation MUST normalize empty to `babel` and reject `static`/`none` with a
> "not yet implemented" error. Routing-mode semantics: [routing-modes.md](routing-modes.md). Closed
> by Plan 6.

### Node fields

| Field | Pass | Rule | Status |
|---|---|---|---|
| `id` | schema | non-empty; unique (semantic) | schema + semantic |
| `name` | schema + semantic | non-empty; **strict charset** (feeds WG interface + root-executed scripts); raw-name AND sanitized-name uniqueness | partial — see below |
| `hostname` | — | optional | none-yet |
| `platform` | schema | enum `debian`/`ubuntu` (warning if other) | schema |
| `role` | schema | enum `peer`/`router`/`relay`/`gateway`/`client` | schema |
| `domain_id` | schema + semantic | non-empty; references an existing domain | schema + semantic |
| `overlay_ip` | schema + semantic | parseable; inside domain CIDR; no collision | schema + semantic |
| `listen_port` | schema | range 0–65535; **effective per-peer range** also in-bounds | partial — see below |
| `mtu` | schema | sane WireGuard MTU range (else `wg-quick` rejects) | none-yet |
| `router_id` | schema | MAC-48 format (else `babeld` rejects) | none-yet |
| `capabilities` | — | derived from role; FE-supplied caps reconciled | none-yet |
| `fixed_private_key` | — | flag | n/a |
| `wireguard_private_key` | schema | parseable WG key when fixed | partial (parsed in `generateKeys`) |
| `wireguard_public_key` | — | non-empty ⇒ key-fixed (allocation-stability) | n/a |
| `public_endpoints[]` | — | host/port sanity | none-yet |
| `extra_prefixes[]` | schema | each a parseable IPv4 CIDR | none-yet |
| `ssh_alias` | schema | **strict charset** (interpolated into root/operator shell) | none-yet |
| `ssh_host` | schema | **strict charset** (interpolated into bash + PowerShell) | none-yet |
| `ssh_port` | schema | range 1–65535 | none-yet |
| `ssh_user` | schema | **strict charset** | none-yet |
| `ssh_key_path` | — | path | none-yet |

> **Compliance — node-name charset & uniqueness:** node names are interpolated unescaped into a
> root-executed `install.sh echo` (`script.go:61`, D15) and into a deploy-script heredoc that a
> single quote breaks (`deploy.go:237`, D16); names also feed `wg-<name>` interface names where two
> names sanitizing identically collide (`peers.go:492-522`, D13/D14). There is no name-charset check
> and no raw-name or sanitized-name uniqueness check anywhere. Validation MUST enforce a strict node
> name charset and reject both raw-name and sanitized-name collisions. Canonical naming and the
> uniqueness invariant: [../artifacts/naming.md](../artifacts/naming.md). Closed by Plan 5 (charset)
> and Plan 4 (uniqueness).

> **Compliance — effective listen port:** schema validates only `node.listen_port` itself
> (`schema.go:164-167`); the compiler binds per-peer interfaces at `base+offset`, which can exceed
> 65535 and is then rendered verbatim into the WG config (`peers.go:175-191`, D11). Validation MUST
> check the *effective* per-peer port range (base + max offset across that node's edges) fits in
> 0–65535. Closed by Plan 3. The co-hosted effective-range overlap check (D47) is currently
> warning-only on base ports (`semantic.go:160-181`).

> **Compliance — MTU / ssh_port / router_id / extra_prefixes / ssh fields:** none of `mtu`,
> `ssh_port`, `router_id`, `extra_prefixes`, `ssh_alias`, `ssh_host`, or `ssh_user` are validated
> anywhere end-to-end (`schema.go`/`semantic.go` never inspect them — D64/D65/D66/D67/D44). An
> out-of-range MTU or malformed router-id produces a config `wg-quick`/`babeld` rejects at deploy
> time; unvalidated SSH fields combine with unquoted interpolation into a local command-injection
> path. Validation MUST cover all of these. Closed by Plans 3 (config-correctness fields) and 5
> (SSH/charset fields).

### Edge fields

| Field | Pass | Rule | Status |
|---|---|---|---|
| `id` | schema | non-empty; unique (semantic) | schema + semantic |
| `from_node_id` | schema + semantic | non-empty; references a node | schema + semantic |
| `to_node_id` | schema + semantic | non-empty; references a node; no self-loop | schema + semantic |
| `type` | schema | enum `direct`/`public-endpoint`/`relay-path`/`candidate` | schema |
| `endpoint_host` | semantic | required for client edges | partial (client only) |
| `endpoint_port` | schema | range 0–65535 (NAT override only) | schema |
| `compiled_port` | — | compiler-written | n/a |
| `priority` | — | optional | none-yet |
| `weight` | — | optional | none-yet |
| `role` | schema + semantic | enum `primary`/`backup`/empty; at most one explicit `primary` per pair; no `backup` on client edges | planned (parallel-links plan-2) |
| `transport` | schema + semantic | enum `udp`/`tcp`; empty→`udp`; `tcp` = mimic-wrapped; semantic: both endpoints must be Linux-deployable | schema; semantic planned (mimic-tcp-transport) |
| `is_enabled` | — | flag | n/a |
| `notes` | — | free-form | n/a |

### Topology root

| Field | Pass | Rule | Status |
|---|---|---|---|
| `project.id` / `project.name` | schema | non-empty | schema |
| `domains` | schema | at least one domain | schema |
| `route_policies` | semantic | **RESERVED** — reject if non-empty | none-yet |

> **Compliance — `route_policies` RESERVED:** `route_policies` is validated nowhere (`schema.go` and
> `semantic.go` never inspect it, D62) and is consumed by no renderer. Per the binding decision
> (Decisions log #2) it is reserved. Semantic validation MUST reject a non-empty `route_policies`
> with a clear "reserved / not yet implemented" error, following the existing locale pattern (no
> English-only string where zh strings exist). See [../api/wire-contract.md](../api/wire-contract.md)
> for the wire-side reservation. Closed by Plan 9.

### Cross-entity rules (semantic)

- ID uniqueness across domains, nodes, and edges (`validateIDUniqueness`).
- Overlay-IP collision and in-CIDR membership (`validateIPSemantics`).
- Listen-port conflict on co-hosted nodes — currently a **warning** on base ports only
  (`validateListenPortConflicts`); the effective-range overlap (D47) is the gap above.
- NAT reachability — double-NAT direct links and endpoint-less inbound targets produce **warnings**
  (`validateNATReachability`). These warnings MUST reach the user on compile, not only on validate
  (see [../api/http-api.md](../api/http-api.md) compile contract).
- Client edge constraints — exactly one enabled outbound edge, target must be router/relay/gateway,
  must carry `endpoint_host`, must not be an inbound target (`validateClientEdges`).
- mimic transport (planned, mimic-tcp-transport plan-2): a `transport: "tcp"` edge requires **both
  endpoints be Linux-deployable** (platform `debian`/`ubuntu`) — mimic is an eBPF/kernel feature;
  error otherwise. The v1.3.0 `tcp` reserved-warning is REMOVED (the value is now implemented).
  Kernel-eBPF availability is an install-time check, not a compile error
  ([../artifacts/mimic.md](../artifacts/mimic.md)).
- Parallel links (planned, parallel-links plan-2):
  - **Interface-name uniqueness (N4)** — per node, across all primary and backup peer interfaces;
    collision = compile-blocking error naming the colliding pair ([naming.md](../artifacts/naming.md)).
  - **One explicit primary per pair** — at most one edge of a pair carries `role: "primary"`.
  - **Equal-cost multi-link warning** — every link of a multi-link pair resolving to the same
    effective cost expresses no failover preference ([babel.md](../artifacts/babel.md)).
  - **No-primary warning** — a pair whose links are all `role: "backup"` (e.g. after a role flip).
  - **Accidental-duplicate warning (D71 re-scope)** — a roleless same-direction second edge is
    still warned, with the message now suggesting `role: "backup"` if redundancy was intended.
