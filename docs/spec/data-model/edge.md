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
| `transport` | `"udp" \| "tcp"` | Transport protocol |
| `is_enabled` | bool | Whether this edge is active |
| `notes` | string | Free-form notes |
| `pinned_*` | int/string | Read-write allocation pins (see [Allocation pins](#allocation-pins)) |

The endpoint resolution rule (how `endpoint_host`/`endpoint_port` produce the rendered
WireGuard `Endpoint` line, and how the reverse peer dials back) is specified in
[../compiler/peer-derivation.md](../compiler/peer-derivation.md).

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
> `base+offset`, internal/compiler/peers.go:174-191). Closed by Plan 2 (PR #2): the frontend
> stamps `endpoint_host` only.

### `endpoint_port` semantics

| `endpoint_port` value | Meaning | Port the from-side dials |
|---|---|---|
| `0` or absent | Auto. The compiler dials the remote interface's auto-allocated listen port. | remote allocated listen port |
| nonzero | Explicit operator NAT / port-forward override. The operator asserts that the remote node is reachable at this external port (e.g. a router DNATs `:51900` → the node's internal `:51820`). | the override value, verbatim |

A nonzero `endpoint_port` is an explicit operator decision and MUST be honored verbatim. It is
the *only* sanctioned reason to set the field. It MUST NOT be produced as a side effect of
selecting a node, drawing an edge, or copying a node's `public_endpoints`.

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
> differs from the rendered `Endpoint`. Closed by Plan 2 (PR #2).

## Field contract

| Field | Owner (writer) | Direction | Round-trip rule |
|---|---|---|---|
| `id`, `from_node_id`, `to_node_id`, `type` | frontend | FE → BE | preserved unchanged |
| `endpoint_host` | operator (via frontend) | FE → BE | preserved; frontend MAY auto-stamp from `public_endpoints[0].host` |
| `endpoint_port` | operator (via frontend) | FE → BE | explicit override only; frontend MUST NOT auto-stamp; `0`/absent ⇒ auto |
| `compiled_port` | compiler | BE → FE | read-only output; never sent back as input |
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
recompiled superset topology reproduces byte-identical values for every pre-existing edge. The
canonical pin identity and the reserve-all-pins-first-then-gap-fill algorithm are specified in
[../compiler/allocation-stability.md](../compiler/allocation-stability.md).

> **Compliance:** the Edge model currently has no `pinned_*` fields
> (internal/model/topology.go:110-139); listen ports, transit pairs and link-locals are derived
> from positional counters over `topo.Edges` order, so any reorder/delete-re-add shifts them.
> Added and closed by Plan 7 (PR #7); cross-referenced from
> [../compiler/allocation-stability.md](../compiler/allocation-stability.md).
