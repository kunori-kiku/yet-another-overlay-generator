# Pass 3c: Peer Derivation (`DerivePeers`) — Two-Phase Algorithm

## Phase 1 — Resource Pre-allocation

For each enabled, unique node pair:
1. Allocate a transit IP pair from `10.10.0.0/24` (sequential: `10.10.0.1/2`, `10.10.0.3/4`, ...)
2. Allocate an IPv6 link-local pair (`fe80::1/2`, `fe80::3/4`, ...)
3. Allocate listen ports for both ends: `base_port + per_node_offset++`
4. Store in `pairAllocation` map (keyed both directions)

## Phase 2 — PeerInfo Construction

For each enabled edge:
1. Look up the pre-allocated resources
2. Resolve endpoint: see [Endpoint resolution](#endpoint-resolution) below
3. Compute PersistentKeepalive: 25s if the initiator cannot accept inbound OR there is no reverse edge
4. Generate WireGuard interface name: `wg-<remote_name>` (max 15 chars, Linux limit)
5. Set AllowedIPs to `0.0.0.0/0, ::/0` (per-peer model — routing handled by Babel)
6. Auto-generate the reverse peer (unless target is a client)

**Client handling:** Client nodes get a single `wg0` interface via `DeriveClientConfigs`, not
per-peer interfaces.

## Endpoint resolution

> **Normative.** This section defines how an edge produces the rendered WireGuard `Endpoint`
> line, in both the forward and reverse directions. It is the contract home for audit theme T1
> (port/endpoint ownership). The data-model side — who may write `endpoint_port` and what
> `compiled_port` means — is specified in [../data-model/edge.md](../data-model/edge.md#port-and-endpoint-ownership).

### Forward edge (from → to)

For each enabled edge, the compiler MUST resolve the from-side `Endpoint` as follows:

1. An `Endpoint` MUST be emitted **if and only if** `endpoint_host` is non-empty. An edge with an
   empty `endpoint_host` produces no `Endpoint` line (the from-side waits passively; it can only
   establish if the to-side dials it).
2. The dialed port MUST be:
   - `endpoint_port` verbatim when `endpoint_port > 0` (explicit operator NAT/port-forward
     override); otherwise
   - the **remote interface's auto-allocated listen port** (the port the to-side binds for this
     link).
3. The emitted endpoint is `host:port` (IPv6 hosts bracketed: `[host]:port`).

The backend is the sole port authority: the auto path MUST use the compiler-allocated remote
listen port, never any port carried in the edge's `endpoint_host` hint or in the target node's
`public_endpoints`.

### Reverse peer (to → from) endpoint fallback

When the compiler auto-generates the reverse peer (to-side dialing back to the from-side), it MUST
resolve the reverse `Endpoint` as follows:

1. **If a reverse edge exists** (`to → from`) with a non-empty `endpoint_host`: resolve exactly as
   the forward rule above — `endpoint_port` override if `> 0`, otherwise the from-side
   interface's auto-allocated listen port.
2. **Else, if no reverse edge exists (or its `endpoint_host` is empty) and the from-node has a
   public endpoint** (`fromNode.public_endpoints` is non-empty): the reverse peer MUST dial
   `fromNode.public_endpoints[0].host` at the **from-side interface's auto-allocated listen
   port**. It MUST NOT use `public_endpoints[0].port` — that port is the node's reachability
   hint, not the per-link listen port, and using it here recreates the headline bug on the
   server side.
3. **Else:** the reverse peer emits no `Endpoint` (it can only establish if the from-side dials
   it, e.g. when the from-side has its own forward endpoint).

This makes a single drawn edge between two publicly-reachable nodes symmetric: the from-side dials
the to-side via the forward rule, and the to-side dials the from-side via the fallback — both
without any operator action beyond marking each node publicly reachable.

> **Compliance:** the reverse peer currently emits an `Endpoint` only when an explicit reverse
> edge with a non-empty host exists (internal/compiler/peers.go:378-396); there is no
> `fromNode.public_endpoints` fallback, so one drawn edge yields an asymmetric config where only
> the drag-target can dial (audit UX-2). Closed by Plan 2 (PR #2). The forward rule itself
> matches this spec (internal/compiler/peers.go:290-306); its only defect is being fed a bogus
> override by the frontend (see [../data-model/edge.md](../data-model/edge.md#the-backend-is-the-sole-port-authority)).

### Determinism caveat

The auto-allocated listen ports referenced above are assigned by positional counters over edge
array order in Phase 1, so the *value* of an auto-allocated port (and thus a non-overridden
`Endpoint`) is order-dependent and can shift on reorder / delete-re-add until sticky-pin
allocation lands. The resolution *rule* in this section is order-independent; only the underlying
port values are not yet stable. See [allocation-stability.md](allocation-stability.md) and
[ip-allocation.md](ip-allocation.md) for the stability contract and the pin mechanism that
closes this.

### Worked examples

**1. Default — two public nodes, single edge.** Nodes A and B are both publicly reachable
(`public_endpoints[0].host = a.example`, `b.example`). The operator draws one edge A → B and sets
no `endpoint_port`. Phase 1 allocates listen ports (say A→B link: A binds `51820`, B binds
`51820`). Resolution:
- Forward (A dials B): `endpoint_host = b.example`, `endpoint_port = 0` ⇒ dials `b.example:51820`
  (B's auto-allocated port). `compiled_port = 51820`.
- Reverse (B dials A): no reverse edge, but A has a public endpoint ⇒ fallback dials
  `a.example:51820` (A's auto-allocated port, **not** `public_endpoints[0].port`).
- Result: a symmetric working tunnel from one edge, zero per-edge port entry.

**2. Explicit NAT override.** Node B sits behind a router that DNATs external `51900` → B's
internal `51820`. The operator sets the A → B edge's `endpoint_port = 51900`. Resolution:
- Forward (A dials B): `endpoint_port = 51900 > 0` ⇒ dials `b.example:51900` verbatim.
  `compiled_port = 51900` (reflects the override).
- Reverse (B dials A): unaffected by B's inbound NAT; resolves via the rules above using A's
  allocated port.

**3. Parallel edges into one hub.** Three spokes A, B, C each draw an edge to hub H, none with an
`endpoint_port`. Phase 1 allocates H a **distinct** listen port per link (H binds `51820` for the
A link, `51821` for the B link, `51822` for the C link — `base + per_node_offset++`). Resolution:
- A dials H at H's A-link port (`h.example:51820`); B dials H at `h.example:51821`; C dials H at
  `h.example:51822`. Each tunnel targets a distinct listening port, so all three establish.
- Contrast the headline bug: if every edge inherited the same `endpoint_port` (e.g. all stamped
  `51820` from H's `public_endpoints`), only one tunnel could ever establish.

## Transit IP Allocation

Sequential from `10.10.0.0/24`:
```
Pair 0: 10.10.0.1, 10.10.0.2
Pair 1: 10.10.0.3, 10.10.0.4
Pair N: 10.10.0.(2N+1), 10.10.0.(2N+2)
```

IPv6 link-local follows the same pattern: `fe80::1/2`, `fe80::3/4`, ...

## WireGuard Interface Naming

```
wg-<lowercase_remote_name>  (max 15 chars, Linux kernel limit)
```
Non-alphanumeric characters (except `-`) are replaced with `-`.

## PersistentKeepalive Logic

Set to `25` (seconds) when:
- The initiating node (`from`) cannot accept inbound connections, OR
- There is no reverse edge (i.e., the remote node has no edge pointing back)

This ensures NAT-traversal keepalive for nodes behind NAT.
