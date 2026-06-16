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
- Effective per-peer port range in-bounds (`base`+offset must not overflow past 65535)
- Isolated node detection (warning)
- NAT reachability warnings (double-NAT, no public endpoint)
- Client edge constraints (exactly one outbound, must target router/relay/gateway, must have endpoint_host)

## Validation coverage contract

The validator's job is to reject every input that cannot produce a deployable overlay *before* the
compiler or a root-executed install script ever sees it. The beta hardening pass (audit theme T4)
closed the field classes that previously passed validation and then crashed the allocator, rendered
non-deployable configs, or injected shell into root-executed scripts; the `none-yet` rows that remain
are optional or compiler-derived fields, not safety gaps. The table below is the authoritative
coverage contract: every model field, the pass that validates it, and the rule.

Status column: `schema` / `semantic` = validated in that pass today; `none-yet` = no validation
exists yet (an optional/derived field, or a deferred gap); `n/a` = compiler-allocated, not
user-supplied.

### Domain fields

| Field | Pass | Rule | Status |
|---|---|---|---|
| `id` | schema | non-empty; unique (semantic) | schema + semantic |
| `name` | schema | non-empty | schema |
| `cidr` | schema | non-empty, parseable, **IPv4-only**, prefix not shorter than /8 | schema |
| `allocation_mode` | schema | enum `auto`/`manual`; empty allowed | schema |
| `routing_mode` | schema | empty normalizes to `babel`; `static`/`none` rejected as not-yet-implemented | schema |
| `reserved_ranges[]` | schema | each a parseable CIDR or IP | schema |
| `transit_cidr` | schema | parseable IPv4 CIDR, IPv4-only, /8–/30 (enough host pairs) | schema |

> **Compliance — IPv4-only CIDR:** `validateDomainsSchema` rejects a non-IPv4 domain CIDR
> (`net.ParseCIDR` then `ipNet.IP.To4() == nil` → `CodeDomainCIDRNotIPv4`, `schema.go:152-157`),
> closing the IPv4-only-allocator crash (`ipToUint32` slices `ip[12:16]` on a nil `To4()`,
> `ip.go:129,164-169` — D4/D35/D20). It also bounds the prefix size: a CIDR shorter than `/8`
> (e.g. `/0`, which would overflow the host count `uint32(1) << 32`, `ip.go:116`, D56) is rejected as
> too large to enumerate (`CodeDomainCIDRTooLarge`, `schema.go:160-162`). The per-link `transit_cidr`
> carries the analogous IPv4 guard plus a `/8`–`/30` size band (`schema.go:214-227`).

> **Compliance — `routing_mode`:** an empty `routing_mode` is normalized to `babel` and written back
> to the topology so the value round-trips explicitly (`schema.go:177-179`, D2/D72); `static` and
> `none` are **rejected** as not-yet-implemented (`CodeDomainRoutingModeUnimplemented`,
> `schema.go:185-186`) rather than rendered into a routing-less dead overlay, and any other value is
> rejected as invalid (`CodeDomainRoutingModeInvalid`, `schema.go:188`). Routing-mode semantics:
> [routing-modes.md](routing-modes.md).

### Node fields

| Field | Pass | Rule | Status |
|---|---|---|---|
| `id` | schema | non-empty; unique (semantic) | schema + semantic |
| `name` | schema + semantic | non-empty; **strict charset** (feeds WG interface + root-executed scripts); raw-name AND sanitized-name uniqueness | schema + semantic |
| `hostname` | — | optional | none-yet |
| `platform` | schema | enum `debian`/`ubuntu` (warning if other) | schema |
| `role` | schema | enum `peer`/`router`/`relay`/`gateway`/`client` | schema |
| `domain_id` | schema + semantic | non-empty; references an existing domain | schema + semantic |
| `overlay_ip` | schema + semantic | parseable; inside domain CIDR; no collision | schema + semantic |
| `mtu` | schema | sane WireGuard MTU range (else `wg-quick` rejects) | schema |
| `xdp_mode` | schema | enum `skb`/`native`; empty allowed (→`skb`) | schema |
| `router_id` | schema | MAC-48 format (else `babeld` rejects) | schema |
| `capabilities` | — | derived from role; FE-supplied caps reconciled | none-yet |
| `fixed_private_key` | — | flag | n/a |
| `wireguard_private_key` | schema | parseable WG key when fixed | partial (parsed in `generateKeys`) |
| `wireguard_public_key` | — | non-empty ⇒ key-fixed (allocation-stability) | n/a |
| `public_endpoints[].host` | schema | **strict charset** (rendered into the per-peer WireGuard config `Endpoint =` parsed by root's wg-quick); `.Port` is an unrendered reachability hint (the reverse-endpoint fallback uses the allocated listen port), so it is not validated | schema |
| `extra_prefixes[]` | schema | each a parseable IPv4 CIDR | schema |
| `ssh_alias` | schema | **strict charset** (interpolated into root/operator shell) | schema |
| `ssh_host` | schema | **strict charset** (interpolated into bash + PowerShell) | schema |
| `ssh_port` | schema | range 1–65535 | schema |
| `ssh_user` | schema | **strict charset** | schema |
| `ssh_key_path` | schema | path charset (forbids shell metacharacters) | schema |

> **Compliance — node-name charset & uniqueness:** node names are interpolated unescaped into a
> root-executed `install.sh echo` (`script.go:61`, D15) and into a deploy-script heredoc that a
> single quote breaks (`deploy.go:237`, D16); names also feed `wg-<name>` interface names where two
> names sanitizing identically collide (`peers.go:492-522`, D13/D14). Schema validation enforces a
> strict name charset (`nodeNameCharset` — letters, digits, space, `.`, `_`, `-` only →
> `CodeNodeNameIllegalChars`, `schema.go:241-244`); semantic validation rejects raw-name duplicates
> (`CodeNodeNameDuplicate`, `semantic.go:228`), installer-name collisions
> (`CodeNodeNameInstallerCollision`, `semantic.go:237`), and sanitized interface-name collisions
> (`CodeNodeNameInterfaceCollision`, `semantic.go:247`). Canonical naming and the uniqueness
> invariant: [../artifacts/naming.md](../artifacts/naming.md).

> **Compliance — effective per-peer port range:** the compiler binds each peer interface at
> `base+offset`, which can exceed 65535 and would then be rendered verbatim into the WG config
> (D11). Validation checks the *effective* per-peer port range (base + max offset across that
> node's edges) fits in 0–65535 (`validateEffectivePortRanges` → `CodeNodeEffectivePortRangeOverflow`).
> The per-node `listen_port` field — and its schema range check — were REMOVED in
> the controller-nat subject: in the per-peer interface model a single node-level listen port is
> meaningless (each edge gets its own interface + auto-allocated port from the uniform base 51820).
> The co-hosted effective-range overlap check (D47) was likewise removed: under a uniform base it
> false-flagged every multi-node-per-host deployment, so co-hosted nodes now validate clean.

> **Compliance — MTU / ssh_port / router_id / extra_prefixes / ssh fields:** all of these are
> validated at the schema pass (D64/D65/D66/D67/D44). `mtu` must be 0 (system default) or within
> [576, 65535] (`CodeNodeMTUOutOfRange`, `schema.go:286`); `ssh_port`, when set, within 1–65535
> (`CodeNodeSSHPortOutOfRange`, `schema.go:293`); `router_id`, when set, must be MAC-48 or an IPv4
> address (`CodeNodeRouterIDInvalid`, `schema.go:299-301`); each `extra_prefixes[]` entry must be a
> parseable IPv4 CIDR (`CodeNodeExtraPrefixInvalid`/`NotIPv4`, `schema.go:307-313`); and `ssh_alias`,
> `ssh_host`, `ssh_user` must match the strict SSH charset (`sshFieldCharset` →
> `CodeNodeSSH*IllegalChars`, `schema.go:319-326`), closing the unquoted-interpolation
> command-injection path. Without these guards an out-of-range MTU or malformed router-id would
> produce a config `wg-quick`/`babeld` rejects only at deploy time.

### Edge fields

| Field | Pass | Rule | Status |
|---|---|---|---|
| `id` | schema | non-empty; unique (semantic) | schema + semantic |
| `from_node_id` | schema + semantic | non-empty; references a node | schema + semantic |
| `to_node_id` | schema + semantic | non-empty; references a node; no self-loop | schema + semantic |
| `type` | schema | enum `direct`/`public-endpoint`/`relay-path`/`candidate` | schema |
| `endpoint_host` | schema + semantic | **strict charset** (schema; rendered into the per-peer WireGuard config `Endpoint =` parsed by root's wg-quick) + required for client edges (semantic) | schema + semantic |
| `endpoint_port` | schema | range 0–65535 (NAT override only) | schema |
| `compiled_port` | — | compiler-written | n/a |
| `priority` | — | optional | none-yet |
| `weight` | — | optional | none-yet |
| `role` | schema + semantic | enum `primary`/`backup`/empty; at most one explicit `primary` per pair; no `backup` on client edges | schema + semantic |
| `transport` | schema + semantic | enum `udp`/`tcp`; empty→`udp`; `tcp` = mimic-wrapped; semantic: both endpoints must be Linux-deployable | schema + semantic |
| `is_enabled` | — | flag | n/a |
| `notes` | — | free-form | n/a |

### Topology root

| Field | Pass | Rule | Status |
|---|---|---|---|
| `project.id` / `project.name` | schema | non-empty | schema |
| `domains` | schema | at least one domain | schema |
| `route_policies` | semantic | **RESERVED** — reject if non-empty | semantic |
| `nodes` / `edges` (count) | schema | DoS bound: ≤ `maxTopologyNodes` (2000) / `maxTopologyEdges` (10000); short-circuits both passes before the O(n²) semantic checks (distinct from the HTTP body-size cap) | schema |
| `alloc_schema_version` | schema | forward-compat fail-closed: reject `> model.CurrentAllocSchemaVersion` (a topology from a newer YAOG whose pin format would be misread); absent/0 restamps to current | schema |

> **Compliance — topology-root size & schema-version (plan-6):** `HandleValidate` runs BOTH
> `ValidateSchema` and `ValidateSemantic` unconditionally, so an unbounded topology is a DoS surface on
> `/validate` as well as `/compile`. `topologyExceedsBounds` is checked at the TOP of both passes and
> short-circuits (`schema.go`): the schema pass reports the coded error
> (`CodeTopologyTooManyNodes`/`CodeTopologyTooManyEdges`/`CodeTopologySchemaVersionUnsupported`), the
> semantic pass guards silently (no duplicate). The schema-version guard fails closed against a future
> pin format rather than silently misreading it as v1 (I10); the canonical max lives in
> `model.CurrentAllocSchemaVersion` because `compiler` imports `validator` (so the validator cannot
> import the compiler's constant).

> **Compliance — `route_policies` RESERVED:** `route_policies` is declared on both the Go and TS
> sides but consumed by no renderer and exposed by no editor entry (D10/D37/D62). Per the binding
> decision (Decisions log #2) it is reserved: semantic validation rejects a non-empty `route_policies`
> with a clear "reserved / not yet implemented" coded error (`validateRoutePoliciesReserved` →
> `CodeRoutePolicyReserved`, `semantic.go:95-97`) rather than compiling a topology that silently
> diverges from the user's intent. See [../api/wire-contract.md](../api/wire-contract.md) for the
> wire-side reservation.

### Cross-entity rules (semantic)

- ID uniqueness across domains, nodes, and edges (`validateIDUniqueness`).
- Overlay-IP collision and in-CIDR membership (`validateIPSemantics`).
- Effective per-peer port-range overflow on each node — `base`+offset across that node's edges
  must fit in 0–65535 (`validateEffectivePortRanges`). (The former per-node listen-port conflict
  and co-hosted effective-range overlap (D47) checks were removed with `node.listen_port`; see the
  compliance note above.)
- NAT reachability — double-NAT direct links and endpoint-less inbound targets produce **warnings**
  (`validateNATReachability`). These warnings MUST reach the user on compile, not only on validate
  (see [../api/http-api.md](../api/http-api.md) compile contract).
- Client edge constraints — exactly one enabled outbound edge, target must be router/relay/gateway,
  must carry `endpoint_host`, must not be an inbound target (`validateClientEdges`).
- mimic transport (`transport: "tcp"`): a `transport: "tcp"` edge requires **both
  endpoints be Linux-deployable** (platform `debian`/`ubuntu`) — mimic is an eBPF/kernel feature;
  error otherwise. The v1.3.0 `tcp` reserved-warning is REMOVED (the value is now implemented).
  Kernel-eBPF availability is an install-time check, not a compile error
  ([../artifacts/mimic.md](../artifacts/mimic.md)).
- Parallel links:
  - **Interface-name uniqueness (N4)** — per node, across all primary and backup peer interfaces;
    collision = compile-blocking error naming the colliding pair ([naming.md](../artifacts/naming.md)).
  - **One explicit primary per pair** — at most one edge of a pair carries `role: "primary"`.
  - **Equal-cost multi-link warning** — every link of a multi-link pair resolving to the same
    effective cost expresses no failover preference ([babel.md](../artifacts/babel.md)).
  - **No-primary warning** — a pair whose links are all `role: "backup"` (e.g. after a role flip).
  - **Accidental-duplicate warning (D71 re-scope)** — a roleless same-direction second edge is
    still warned, with the message now suggesting `role: "backup"` if redundancy was intended.
