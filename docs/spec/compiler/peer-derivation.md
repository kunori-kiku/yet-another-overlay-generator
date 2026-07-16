# Pass 3c: Peer Derivation (`DerivePeers`) — Two-Phase Algorithm

## Phase 1 — Resource Pre-allocation

1. Collapse enabled primary-class edges onto their canonical node-pair link identity; keep each
   backup edge as its own edge-ID-qualified identity.
2. Reserve every valid existing sticky allocation before choosing any new value: complete transit
   and link-local pairs, complete ordinary-link port pairs, and the one non-client port on a client
   link.
3. Visit link identities in deterministic order and gap-fill each missing resource from its pool.
   Per-node ports scan upward from the fixed fleet-wide base `51820`; transit and link-local values
   are allocated as pairs.
4. Store the resulting oriented allocation for peer construction and compiler write-back. A client
   endpoint keeps no per-link port because it uses shared `wg0`, while the router-side port and both
   address pairs remain real link state.

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

**Require-explicit-host.** Because the forward rule emits an `Endpoint` only when `endpoint_host` is
non-empty, a port-only override (`endpoint_port > 0` with an empty `endpoint_host`) would silently
produce no forward `Endpoint` — and the reverse would fall back to the peer's plain public IP — while
the panel still shows a "NAT override active" badge. Rather than drop the operator's override silently,
validation rejects that combination at the schema stage (`validation_edge_endpoint_port_without_host`;
see [../data-model/edge.md](../data-model/edge.md#endpoint_port-semantics)), so the compiler never
receives a port-only override.

### Reverse peer (to → from) endpoint fallback

When the compiler auto-generates the reverse peer (to-side dialing back to the from-side), it MUST
resolve the reverse `Endpoint` as follows:

0. **If the edge's `link_direction` is `forward`** ([../data-model/edge.md](../data-model/edge.md#link-direction)):
   the reverse `Endpoint` is suppressed ENTIRELY — neither the explicit-reverse-edge branch nor
   the public-endpoint fallback below runs. The reverse peer keeps its full `[Peer]` stanza
   (AllowedIPs, transit addressing) but never initiates, so it can never race the forward path's
   relay/accelerator endpoint via runtime roaming. (The validator's conflict rule guarantees a
   direction-bearing pair has exactly one enabled primary-class edge, but the compiler applies
   this gate deterministically regardless — validator-independent, floors unknown values to
   `both`.)
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
> the drag-target can dial (audit UX-2). Closed by Plan 2 (PR #4). The forward rule itself
> matches this spec (internal/compiler/peers.go:290-306); its only defect is being fed a bogus
> override by the frontend (see [../data-model/edge.md](../data-model/edge.md#the-backend-is-the-sole-port-authority)).

### Runtime endpoint roaming (operational note)

The rendered `Endpoint` line is only the **initial dial address**. WireGuard rewrites a peer's runtime
endpoint to the source address of the most recent authenticated packet it receives **from that peer**
("roaming"). The divergence therefore shows up on the DIALER, for a peer that is itself behind NAT:
when a peer sits behind a port-forward (you dial its front, e.g. a router DNATs `:51820` → the peer's
internal `:51820`) **plus** source NAT on the peer's outbound path (its packets egress from a different
`IP:port`), your node's `wg show` for THAT PEER reports the peer's *observed egress* — which
legitimately differs from the `Endpoint =` your `.conf` dials. In a fleet where several nodes are NAT'd
this is symmetric: each node sees its NAT'd peers roamed. It is expected WireGuard behavior, **not** a
compiler or config defect: the `.conf` carries the operator's configured (dial-front) endpoint; the
kernel's live endpoint follows the authenticated source. A mismatch between `wg show` and the
deploy-page endpoint for a peer behind DNAT+SNAT is therefore normal and needs no action. (Pinning the
runtime endpoint against roaming would require periodically re-asserting it — a deliberate future
option, not a default.)

Roaming has one harmful special case: when BOTH sides can dial (the forward edge carries a
relay/accelerator `endpoint_host` while the from-node also has plain `public_endpoints`), whichever
side handshakes first wins the single runtime endpoint slot — a faster-booting to-side dials the
from-node DIRECT and roaming then bypasses the relay path permanently. The deterministic in-product
fix is single-linking the edge (`link_direction: forward`,
[../data-model/edge.md](../data-model/edge.md#link-direction)): the reverse peer then never
initiates, so the race cannot start.

### Determinism and sticky reuse

Existing valid pins are reserved before deterministic gap filling, so an established link's port
and address pairs do not shift when unrelated edges are reordered, added, disabled, or removed.
`Endpoint` is therefore stable when it derives from the remote sticky listen port; an explicit
`endpoint_port` remains an operator-owned override. Clearing a pin, changing link identity, or
changing the applicable pool is an explicit renumber boundary. See
[allocation-stability.md](allocation-stability.md) and [ip-allocation.md](ip-allocation.md).

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

**4. Single-linked via an accelerator.** Node A reaches node B fastest through a UDP accelerator
(`accel.example` forwards to B); both nodes are ALSO plainly public. The operator sets the A → B
edge's `endpoint_host = accel.example` and `link_direction: forward`. Resolution:
- Forward (A dials B): dials `accel.example:<B's allocated port>` (or an explicit
  `endpoint_port` override) — unaffected by the direction.
- Reverse (B dials A): **suppressed** (rule 0). Without the direction, the fallback would dial
  `a.example:<A's allocated port>` — and if B handshook first, roaming would pin the tunnel to
  the direct path, bypassing the accelerator (example 1's symmetry is exactly what the race
  exploits). B's `[Peer]` for A keeps AllowedIPs and learns A's address from A's inbound
  handshake; the tunnel forms and routes both ways with A as the only initiator.

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
