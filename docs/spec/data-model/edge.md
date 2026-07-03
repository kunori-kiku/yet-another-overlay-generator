# Edge

An Edge represents a unidirectional connection intent ("from actively connects to to").

| Field | Type | Description |
|---|---|---|
| `id` | string | Unique identifier |
| `from_node_id` | string | Source node ID |
| `to_node_id` | string | Destination node ID |
| `type` | `"direct" \| "public-endpoint" \| "relay-path" \| "candidate"` | Connection type |
| `endpoint_host` | string | Target endpoint IP/hostname the from-side dials |
| `endpoint_port` | int | Explicit operator NAT/port-forward override (0 / absent = compiler auto-allocates) |
| `compiled_port` | int | Read-only: the port the from-side actually dials (reflects an override when set) |
| `priority` | int | Connection priority |
| `weight` | int | Connection weight |
| `role` | `"primary" \| "backup" \| ""` | Link role for parallel links (see [Parallel links](#parallel-links-primary--backups)); empty = primary class |
| `link_direction` | `"both" \| "forward" \| ""` | Per-edge dial-direction policy; empty ≡ `both` (today's behavior). `forward` = single-linked: only from→to initiates — see [Link direction](#link-direction). There is deliberately no `"reverse"` value (one spelling; flip the edge instead) |
| `transport` | `"udp" \| "tcp"` | `udp` = plain WireGuard. `tcp` = the link is wrapped by **mimic** (eBPF UDP→fake-TCP) for UDP-hostile networks — see [TCP transport (mimic)](#tcp-transport-mimic). No new field: `tcp` is the whole signal (mimic is keyless) |
| `is_enabled` | bool | Whether this edge is active. The one intentionally non-omitempty Edge bool: a missing value is the Go zero (`false`/disabled); the panel normalizes it to a concrete boolean at the import boundary so a hand-edited file cannot smuggle `undefined` past the type system |
| `notes` | string | Free-form notes |
| `pinned_*` | int/string | Read-write allocation pins (see [Allocation pins](#allocation-pins)) |

The endpoint resolution rule (how `endpoint_host`/`endpoint_port` produce the rendered
WireGuard `Endpoint` line, and how the reverse peer dials back) is specified in
[../compiler/peer-derivation.md](../compiler/peer-derivation.md).

## Parallel links (primary + backups)

A node pair MAY carry multiple enabled edges. Semantics (normative; identity contract in
[../compiler/allocation-stability.md](../compiler/allocation-stability.md) §linkKey):

- All edges of a pair with `role != "backup"` form the **primary class** and compile to ONE link
  (one WireGuard tunnel) — a roleless `A→B` + `B→A` pair remains today's single bidirectional
  tunnel. At most one edge of a pair may carry the explicit `role: "primary"` label (validator).
- Every `role: "backup"` edge compiles to its **own link**: its own WireGuard interface
  (edge-aware name, [naming.md](../artifacts/naming.md)), its own listen ports, transit pair, and
  link-locals — all owned per-edge in that edge's `pinned_*` fields.
- Babel performs failover between a pair's links by per-interface cost
  ([babel.md](../artifacts/babel.md) §Link cost resolution): backups default to a higher rxcost
  (384) than the primary, so routes shift to a backup only when the primary link dies or degrades
  past the cost gap.
- A roleless second edge in the SAME direction as an existing primary-class edge is an accidental
  duplicate: warned (suggesting `role: "backup"` if redundancy was intended) and mapped to the
  primary link for write-back — the historical behavior.
- Edges to or from a `client`-role node cannot be parallel: clients keep the exactly-one-enabled-
  outbound-edge rule (single `wg0`, no Babel).

## TCP transport (mimic)

`transport: "tcp"` wraps the link's WireGuard traffic with [mimic](https://github.com/hack3ric/mimic),
an eBPF program that rewrites UDP packets to look like TCP on the wire. (mimic attaches to the node's
**egress NIC**, not the WG interface — a `local=` filter per mimic listen port + a `remote=` filter
per dialed peer; see [../artifacts/mimic.md](../artifacts/mimic.md) for the deployment model.) Full contract:
[../artifacts/mimic.md](../artifacts/mimic.md).

- **Purpose: UDP-hostile networks** — paths that throttle UDP (QoS), block UDP ports, or degrade
  UDP throughput. It is **not** a censorship/DPI-circumvention feature; it does not resist active
  probing or deep packet inspection. Do not describe or use it as anti-censorship.
- **Keyless — no new field.** mimic carries no password/PSK; WireGuard provides all encryption and
  authentication. `transport: "tcp"` is the entire signal; nothing else is stored on the edge.
- **Per-interface, MTU −12.** Each mimic interface keeps its own allocated listen port; the
  compiler lowers that interface's MTU by 12 bytes (mimic's overhead). Non-mimic interfaces and all
  `udp` edges are unaffected (byte-identical output).
- **Both ends must be Linux with eBPF.** mimic is an eBPF/kernel feature; a `tcp` edge whose
  endpoint is not Linux-deployable is a validation error ([../compiler/validation.md](../compiler/validation.md)).
  Kernel-eBPF availability is checked at install time.
- **Pairs with parallel links.** A plain `udp` primary + a `tcp` backup lets Babel fail over to the
  TCP-shaped path when the plain UDP one is throttled or blocked.

## Link direction

> **Normative.** `link_direction` is the per-edge dial-direction POLICY: it gates only which of the
> link's two `[Peer]` stanzas receives a dial `Endpoint`. It NEVER touches allocation — ports,
> transit pairs, link-locals, and every `pinned_*` field are direction-blind and byte-identical
> under any value (link identity is direction-agnostic;
> [../compiler/allocation-stability.md](../compiler/allocation-stability.md)).

**Why it exists (the reverse-peer race).** A doubly-linked edge `A→B` emits a forward peer (A
dials `endpoint_host`) AND an auto-reverse peer in which B dials A's `public_endpoints[0]` — the
plain direct address ([../compiler/peer-derivation.md](../compiler/peer-derivation.md#reverse-peer-to--from-endpoint-fallback)).
WireGuard keeps ONE runtime endpoint per peer and roams it to the source of the last authenticated
inbound packet, so whichever side handshakes first wins: if B boots faster, B→A establishes
*direct*, A roams onto B's real source, and an intended A→relay/accelerator→B path is bypassed
permanently. Single-linking the edge removes the race deterministically.

- **`""` / `"both"` (default):** both sides may initiate — exactly today's behavior; every
  existing topology compiles byte-identically.
- **`"forward"`:** only from→to initiates. The reverse peer keeps its full `[Peer]` stanza
  (AllowedIPs, transit addressing, Babel routing, return traffic) but carries **no `Endpoint`
  line**, so it can never dial and never race the forward path. The kernel learns the peer's
  address from the inbound handshake (standard WireGuard).
- **There is no `"reverse"` value** (decision D11: ONE spelling — a second spelling of
  "single-linked toward the from-node" would force every direction-aware rule to handle both
  forever). To single-link the other way, **flip the edge**: the editor's "to(A)" choice swaps
  `from`/`to`, mirrors the three `pinned_*` pairs (allocation-stable — each node keeps its own
  values; interface names are unchanged because each side names the REMOTE), clears the stale dial
  fields, and prefills `endpoint_host` from the newly-dialed node's public endpoints. The drawn
  arrow therefore always equals the dial direction.

**Validation (all errors, both validators):**

| Code | Rule |
|---|---|
| `validation_edge_link_direction_invalid` | value ∉ {`""`, `both`, `forward`} (schema) |
| `validation_edge_link_direction_conflict` | direction ≠ both on an enabled primary-class edge whose node pair has any other enabled primary-class edge — pair-folding would silently ignore the folded edge's direction. Backup edges are their own links and are exempt |
| `validation_edge_link_direction_forward_no_endpoint` | `forward` with an empty `endpoint_host` — the forward peer only ever dials the edge's host and the reverse dial is suppressed, so no side could initiate (provably dead link) |
| `validation_edge_link_direction_client_edge` | direction ≠ both on a client-touching edge — a client link's dial semantics are fixed (the client always dials the router) |

The panel additionally sanitizes out-of-enum values to `both` on its own load paths (file import,
server hydrate, localStorage rehydrate) so foreign/garbled stored data degrades to today's
behavior instead of tripping the validator.

## Port and endpoint ownership

> **Normative.** This section defines the authority contract for ports and endpoints. It is the
> contract home for audit theme T1 (port/endpoint ownership confusion).

### The backend is the sole port authority

The compiler MUST be the only component that allocates WireGuard listen ports and decides the
port each peer dials. No other component — and in particular the frontend — may compute,
infer, or write a dial port into an Edge.

- The frontend MUST NOT auto-populate `endpoint_port`. On edge creation it MAY stamp
  `endpoint_host` (the reachability hint copied from the target node's
  `public_endpoints[0].host`) but MUST leave `endpoint_port` at `0`/absent so the backend
  allocates the listen port the remote interface actually binds.
- The frontend MUST NOT treat `public_endpoints[].port` as a dial port. A node's
  `public_endpoints[].port` is a *reachability hint about the node*, not the per-link port a
  peer dials; conflating the two is the headline defect.
- `compiled_port` is read-only output. The frontend MUST display it as compiler output and MUST
  NOT feed it back as an `endpoint_port` override.

> **Compliance:** the frontend currently stamps `endpoint_port: preferredEndpoint?.port` from
> `public_endpoints[0]` at edge-draw time (frontend/src/components/canvas/TopologyCanvas.tsx:251),
> so the backend dials a port nothing listens on (the remote interface binds the auto-allocated
> `base+offset`, internal/compiler/peers.go:174-191). Closed by Plan 2 (PR #4): the frontend
> stamps `endpoint_host` only.

### `endpoint_port` semantics

| `endpoint_port` value | Meaning | Port the from-side dials |
|---|---|---|
| `0` or absent | Auto. The compiler dials the remote interface's auto-allocated listen port. | remote allocated listen port |
| nonzero | Explicit operator NAT / port-forward override. The operator asserts that the remote node is reachable at this external port (e.g. a router DNATs `:51900` → the node's internal `:51820`). | the override value, verbatim |

A nonzero `endpoint_port` is an explicit operator decision and MUST be honored verbatim. It is
the *only* sanctioned reason to set the field. It MUST NOT be produced as a side effect of
selecting a node, drawing an edge, or copying a node's `public_endpoints`.

**A nonzero `endpoint_port` REQUIRES a non-empty `endpoint_host` (require-explicit-host).** A port
cannot be dialed without a host, and the compiler's `Endpoint`-line derivation resolves the dial host
from `endpoint_host` — so a port-only override (`endpoint_port > 0`, `endpoint_host = ""`) would be
silently dropped (the from-side emits no forward `Endpoint` at all, and only the reverse peer dials —
falling back to the peer's plain public IP) while the panel still shows a "NAT override active" badge. Validation therefore
rejects it at the schema stage (`validation_edge_endpoint_port_without_host`); the frontend keeps the
two fields coupled (unsetting the host clears the port; the badge requires a host) so the state is
never created in the first place. Set an explicit `endpoint_host` alongside the port, or clear the port.

### `compiled_port` semantics

`compiled_port` is the port the from-side will actually dial for this edge — i.e. the value that
appears (with `endpoint_host`) in the rendered WireGuard `Endpoint` line. The compiler MUST stamp
it on every enabled edge that has a non-empty `endpoint_host`:

- when `endpoint_port == 0`: `compiled_port` = the remote interface's auto-allocated listen port;
- when `endpoint_port > 0`: `compiled_port` = `endpoint_port` (the override).

`compiled_port` MUST equal the port carried in the rendered `Endpoint`. The two MUST NOT diverge.

> **Compliance:** the write-back currently sets `compiled_port` to the allocated remote listen
> port unconditionally and never reflects an `endpoint_port` override
> (internal/compiler/compiler.go:111-126), so for an overridden edge the UI shows a port that
> differs from the rendered `Endpoint`. Closed by Plan 2 (PR #4).

## Field contract

| Field | Owner (writer) | Direction | Round-trip rule |
|---|---|---|---|
| `id`, `from_node_id`, `to_node_id`, `type` | frontend | FE → BE | preserved unchanged |
| `endpoint_host` | operator (via frontend) | FE → BE | preserved; frontend MAY auto-stamp from `public_endpoints[0].host` |
| `endpoint_port` | operator (via frontend) | FE → BE | explicit override only; frontend MUST NOT auto-stamp; `0`/absent ⇒ auto |
| `compiled_port` | compiler | BE → FE | read-only output; never sent back as input |
| `link_direction` | operator (via frontend) | FE → BE | preserved unchanged; empty ≡ `both`; panel sanitizes out-of-enum to absent on load |
| `priority`, `weight`, `transport`, `is_enabled`, `notes` | frontend | FE → BE | preserved unchanged |
| `pinned_*` | compiler (write) + frontend (persist) | BE ↔ FE | round-tripped verbatim (see below) |

## Allocation pins

To make incremental growth non-disruptive, the compiler binds each link's allocated resources to
the edge via six read-write pin fields, written by the compiler and round-tripped by the frontend
(the same write-back + localStorage persistence path already used for `overlay_ip` and
`compiled_port`):

| Field | Pins |
|---|---|
| `pinned_from_port` | from-side interface listen port |
| `pinned_to_port` | to-side interface listen port |
| `pinned_from_transit_ip` | from-side transit IP |
| `pinned_to_transit_ip` | to-side transit IP |
| `pinned_from_link_local` | from-side IPv6 link-local |
| `pinned_to_link_local` | to-side IPv6 link-local |

**Round-trip rule.** On compile the backend MUST stamp these fields with the allocated values.
The frontend MUST persist them and send them back unchanged on the next compile. A subsequent
compile MUST honor a present pin rather than recompute the value from array position, so a
recompiled superset topology reproduces byte-identical values for every pre-existing edge.
Ownership is **per edge**: each parallel link's pins live on its own edge and are reserved
verbatim, independent of every other edge of the same pair. The link identity and the
reserve-all-pins-first-then-gap-fill algorithm are specified in
[../compiler/allocation-stability.md](../compiler/allocation-stability.md).

> **Compliance:** the Edge model currently has no `pinned_*` fields
> (internal/model/topology.go:110-139); listen ports, transit pairs and link-locals are derived
> from positional counters over `topo.Edges` order, so any reorder/delete-re-add shifts them.
> Added and closed by Plan 7 (PR #9); cross-referenced from
> [../compiler/allocation-stability.md](../compiler/allocation-stability.md).
