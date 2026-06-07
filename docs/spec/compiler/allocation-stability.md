# Allocation Stability & Growth

This spec defines the contract the IP allocator and peer-deriver MUST satisfy so that an
already-deployed overlay can grow — add nodes, add edges, re-enable a disabled link — without
disturbing the configuration of servers that did not change. The governing principle is to
generalize the proven overlay-IP pattern (the allocator already preserves a node's `overlay_ip`
across recompiles: [ip-allocation.md](ip-allocation.md), `internal/allocator/ip.go:61-79`) to
**every** allocated value: listen ports, transit IP pairs, and IPv6 link-local pairs.

The mechanism is **sticky/pinned allocation**: each per-peer value, once chosen, is written back
onto its Edge as a `pinned_*` field, persisted through the existing compile write-back +
localStorage round-trip (no new transport), and reused verbatim on the next compile. WireGuard
keys are stabilized by the companion rule in [../data-model/node.md](../data-model/node.md): a node
that already carries a `wireguard_public_key` is treated as key-fixed and its key is reused.

> **Compliance:** current code does not satisfy most of this spec. Listen ports, transit pairs,
> and link-locals are allocated by positional counters (`transitPairIndex`, `nodePortOffset`) over
> `topo.Edges` order with nothing binding a value to a link identity
> (`internal/compiler/peers.go:129-209,443-478`); the only stable value is `overlay_ip`. Keys
> rotate on every compile for non-fixed nodes (`internal/api/handler.go:308-314`). Closed by the
> sticky-pin allocation plan (outline milestone 7, Plan 7).

## Normative invariants (I1–I10)

These ten invariants are the acceptance contract. Each MUST hold for every compile of a topology
that is a superset of (or otherwise derived from) a previously compiled one.

### I1 — Superset stability

Recompiling a topology that is a superset of a previously compiled topology MUST reproduce
**identical** allocated values — overlay IP, listen port, transit IP pair, IPv6 link-local pair,
and WireGuard public key — for every entity that existed in the earlier compile.

> **Violation example:** compile `[A, B]` with edge `A–B`, then add node `C` and edge `A–C` and
> recompile. If `A`'s listen port for the `A–B` link, or the `A–B` transit pair, changes, I1 is
> violated and `B` must be redeployed even though nothing about `B` changed.

### I2 — Order independence

An allocated value MUST NOT depend on the array position of a node or edge, nor on the
enable/disable/delete/re-add history of any **other** entity. Two topologies that are equal as
sets MUST produce equal allocations regardless of the order in which nodes and edges appear in the
arrays.

> **Violation example:** the order-independence property test compiles `[A, B] + A–B` then
> `[A, B, C] + A–B, A–C` with the edges in both orders. Positional counters over `topo.Edges`
> make the `A–B` transit pair depend on whether `A–B` or `A–C` is listed first; the byte-identical
> `A–B` requirement fails. Reserve-all-pins-first then gap-fill (below) makes this hold by
> construction.

### I3 — Identity binding

Every allocated value MUST be bound to a stable entity identity and honored on the next compile,
not recomputed from ordinal position. The identity of a per-peer allocation is the **link key**
`linkKey(edge)` (defined below) — which reduces to the canonical pair key `pinKey(a, b)` for a
pair's primary link; the identity of overlay IP and key is the node ID.

> **Violation example:** deriving a transit pair as `2 * transitPairIndex + 1` ties the value to
> loop position. Deleting an earlier edge shifts every later edge's index by one and renumbers
> their transit pairs even though those links are unchanged.

### I4 — Additive growth

Adding an entity MUST require redeploying only that entity and its direct neighbors; every other
node's bundle MUST be byte-stable. Adding `C` connected via `A` MUST leave `B` with a
byte-identical bundle and therefore require **zero action on `B`**.

> **Violation example:** adding `C` reshuffles `A`'s per-peer ports such that the `A–B` interface
> moves from `wg0`'s base port to `base+1`. `B`'s peer config still dials the old port; the tunnel
> to `B` must be reconfigured. The byte-stable-`B` property is the acceptance test for I4.

### I5 — Key persistence

A node's WireGuard key pair MUST be stable across recompiles once assigned. A node whose
`wireguard_public_key` is non-empty MUST be treated as key-fixed: its existing key is reused, and
no fresh key is generated. Fresh keys MUST be generated only for genuinely new nodes (no public
key yet) or for an explicit, operator-initiated rotation. See
[../data-model/node.md](../data-model/node.md) and [../security/security.md](../security/security.md).

> **Violation example:** the non-fixed branch generates a random key on every compile and blanks
> the node's stored key (`internal/api/handler.go:308-314`), so the key lives only in the
> ephemeral render map. Every recompile rotates the key of every non-fixed node, forcing a
> full-fleet redeploy. This is the dominant cause of the "no room to grow" symptom (dossier T13).

### I6 — Headroom and pool exhaustion

The capacity of every allocation pool MUST be defined, and exhaustion MUST surface as a clear,
attributable compile error **before** any silent collision or out-of-range value is emitted.

- **Listen ports:** a node's per-peer ports start at its base `listen_port` (default `51820`) and
  the highest assigned port MUST NOT exceed `65535`.
- **Transit pool:** a `/24` transit CIDR yields at most 127 usable pairs (pair index 0..126,
  drawn from hosts `.1`–`.254`); pair index 127 would require the `.255` broadcast and MUST be
  rejected, not emitted.
- **Link-local pool:** `fe80::/10` is effectively unbounded for practical fleet sizes; pairs MUST
  be formatted and parsed in a single consistent radix (see I3 / [pin validation](#pin-validation)).

> **Violation example:** `base + offset` listen ports are rendered verbatim with no `65535` bound
> (`internal/compiler/peers.go:174-191`); a high-degree hub silently emits an illegal port that
> `wg-quick` rejects at deploy time rather than at compile time.

### I7 — Validated pins and explicit renumber

Pinned values MUST be validated before they are reserved (see [pin validation](#pin-validation)).
Renumbering an existing entity MUST be an explicit operator action — clearing the relevant
`pinned_*` fields — and MUST NEVER occur as a side effect of adding, removing, or reordering an
**unrelated** entity.

> **Violation example:** a stale pin that references a transit IP outside the (now narrowed)
> `transit_cidr` is silently honored, emitting an out-of-pool address; or the compiler "fixes" the
> collision by renumbering a neighboring link the operator never touched. Both are forbidden:
> invalid pins are rejected with an error, and renumbering is opt-in only.

### I8 — Change observability

Every allocated value MUST be observable in the persisted topology (round-tripped onto the Edge as
`pinned_*` and onto the Node as `wireguard_public_key`), and bundles MUST be byte-deterministic for
unchanged inputs, so that an operator (or a diff tool) can identify exactly which nodes changed.

> **Violation example:** allocated ports/transit IPs that exist only in the in-memory `PeerMap` and
> a write-only `compiled_port` cannot be diffed; the operator cannot tell which bundles actually
> changed and conservatively redeploys everything.

### I9 — Garbage collection / no leak

Deleting an entity MUST free its pinned values for reuse. When an edge is removed, its `pinned_*`
values leave the topology with it; the freed ports/transit/link-local slots MUST become available
to gap-fill for future links. Pins MUST NOT leak (occupy a slot for a link that no longer exists).

> **Violation example:** a global reservation table that is never pruned keeps a deleted edge's
> transit pair reserved forever, prematurely exhausting a small custom transit pool.

### I10 — Schema versioning

The allocation scheme MUST carry a version (`alloc_schema_version` on the topology, see
[../data-model/topology.md](../data-model/topology.md)) so that a future change to the pinning
format can detect and migrate prior allocations rather than silently mis-reading them.

> **Violation example:** a future change to the link-local format reads an old topology's pins as
> if they were the new format, producing wrong addresses with no error. The version field lets the
> compiler branch on or migrate the older scheme.

## Sticky-pin mechanism

### Pinned fields

Six `pinned_*` fields are added to the Edge model. They are written back by the compiler and
persisted (localStorage + compile round-trip) exactly like `overlay_ip` is for nodes. JSON names:

| JSON field | Holds | Pool |
|---|---|---|
| `pinned_from_port` | `from` node's listen port for this link | base `listen_port` .. 65535 |
| `pinned_to_port` | `to` node's listen port for this link | base `listen_port` .. 65535 |
| `pinned_from_transit_ip` | `from` end of the transit IP pair | domain `transit_cidr` |
| `pinned_to_transit_ip` | `to` end of the transit IP pair | domain `transit_cidr` |
| `pinned_from_link_local` | `from` end of the IPv6 link-local pair | `fe80::/10` |
| `pinned_to_link_local` | `to` end of the IPv6 link-local pair | `fe80::/10` |

All six are optional and omitempty; an edge with none of them set is **unpinned** and will be
gap-filled. An edge MUST be either fully pinned (the full set of the values applicable to it) or
fully unpinned for a given resource; partial pins for a single resource (e.g. `pinned_from_transit_ip`
set but `pinned_to_transit_ip` empty) MUST be rejected (see [pin validation](#pin-validation)).

> **Compliance:** the Edge struct currently has no `pinned_*` fields
> (`internal/model/topology.go:112-139`); only `compiled_port` (write-only) exists. Closed by Plan 7.

### Canonical link key — `pinKey(a, b)`

The identity of a per-peer allocation is the unordered pair of its two node IDs. Define

```
pinKey(a, b) = min(a, b) + "|" + max(a, b)        // string min/max of node IDs
```

`pinKey` is direction-agnostic: `pinKey(A, B) == pinKey(B, A)`. The compiler MUST use `pinKey` —
not the directed `from->to` string — to identify a link's reserved values, so that reversing an
edge's draw direction does not change its allocation.

### Link identity with parallel edges — `linkKey(edge)`

A node pair MAY carry multiple enabled edges (one primary link plus backups for Babel cost-based
failover — see [../data-model/edge.md](../data-model/edge.md) §Parallel links). Link identity
generalizes from the pair to the edge:

```
linkKey(edge) = pinKey(from, to)                       // edge.role != "backup"  (primary class)
linkKey(edge) = pinKey(from, to) + "#" + edge.ID       // edge.role == "backup"
```

**Unify rule (primary class).** All enabled edges of a pair whose `role` is not `backup` form the
pair's **primary class** and collapse to ONE link entity — exactly today's semantics: a roleless
`A→B` + `B→A` pair is one bidirectional tunnel, and the primary edge (first enabled primary-class
edge in `topo.Edges` order) keeps today's selection rule. Additional same-direction primary-class
edges remain accidental duplicates: warned, and mapped to the primary link for write-back (their
historical behavior). Every `role: "backup"` edge is its **own link** regardless of direction.

**Stability properties (normative):**

1. **Single-edge reduction.** A pair with one enabled edge has `linkKey == pinKey` — fresh-compile
   gap-fill order and values are byte-identical to the pre-parallel-links compiler. This is the
   no-drift guarantee for every existing fleet, pinned by the perpetual stability gate.
2. **Verbatim-pin immunity.** Pinned values are reserved from each edge's own `pinned_*` fields
   verbatim; linkKey governs only gap-fill ordering, validator grouping, and unification. No
   identity re-keying can move an already-pinned value.
3. **Identity never migrates on growth.** Backups are ALWAYS discriminated by their own edge ID —
   even when a backup is the pair's only edge. Adding or removing a backup therefore never changes
   any other link's linkKey or interface name. (A lowest-edge-ID canonicality rule was considered
   and rejected: edge UUIDs sort randomly, so a new backup could steal the bare pinKey and rename
   the deployed primary's interface.)
4. **Role flips are deliberate identity changes.** Setting `role: "backup"` on a previously
   primary-class edge moves it to a discriminated linkKey and an edge-aware interface name
   ([naming.md](../artifacts/naming.md)). Its pinned values survive (property 2), but the
   interface renames — the operator changed what the link *is*. The validator warns when a role
   flip leaves a pair with no primary-class link.
5. **I9 with parallel edges.** Delete/re-add idempotence holds per link identity: re-adding a
   deleted primary-class link (same pair) reproduces its clean gap-fill values as before;
   re-adding a backup mints a new edge ID → a new identity → a fresh gap-fill. Documented and
   accepted: backups are positional only in their own resources, never in anyone else's.

> **Compliance:** current pre-allocation keys both directions in a `from->to` / `to->from` map and
> dedupes by an `addedPairs` set (`internal/compiler/peers.go:130,146-152,205-208`), which is
> direction-tolerant for dedup but is still positional for the values it assigns. Plan 7 replaces
> the positional assignment with `pinKey`-anchored reserve-then-gap-fill.

### Reserve-all-pins-first, then gap-fill

Pass 1 of peer derivation MUST run in two ordered stages. Order independence (I2) holds **by
construction** because every pinned value is reserved before any unpinned value is chosen, and the
gap-fill choice for an unpinned link does not depend on array order (see identity-ordered gap-fill).

1. **Collect.** Enumerate all enabled edges and group by `pinKey(from, to)`. Within a pair,
   collapse the primary class (all edges with `role != "backup"`) to a single link entity under
   the unify rule above; every `role: "backup"` edge becomes its own link entity keyed by
   `linkKey(edge)`.
2. **Validate pins.** Run the [pin validation](#pin-validation) rules over every `pinned_*` value.
   Reject the compile on any violation. No reservation happens until validation passes.
3. **Reserve pins.** For every link that carries pins, insert its pinned ports, transit pair, and
   link-local pair into the per-pool reservation sets (per-node port set; per-domain transit set
   keyed by `transit_cidr`; global link-local set). This stage reserves **all** pins across the
   whole topology before stage 4 begins.
4. **Gap-fill unpinned.** For every link with no pins (a new or re-added link), choose the lowest
   free values not already reserved, using the identity-ordered gap-fill below. Write the chosen values
   back as `pinned_*` so they become sticky on the next compile.
5. **Write back.** Stamp the chosen/honored values onto the Edge's `pinned_*` fields and onto the
   read-only `compiled_port` for UI display.

Because stage 3 reserves every pin before stage 4 runs, a newly added link can never be handed a
value an existing link already owns, and an existing link's values never move. This is the
mechanism behind I1, I3, and I4.

### Identity-ordered gap-fill (delete/re-add idempotence)

When gap-filling an unpinned link, the order in which candidate links are assigned MUST be
deterministic in `linkKey` (e.g. iterate unpinned links sorted by `linkKey`, and within a pool
pick the lowest free slot), **not** in array position. For single-edge pairs `linkKey == pinKey`,
so the sort order — and therefore every assigned value — is unchanged from the pre-parallel-links
compiler. The rationale is **delete/re-add idempotence**:
deleting an edge and re-adding the same link (same node pair) MUST, on the next compile, reproduce
the same values it would have received from a clean gap-fill of that `pinKey`, independent of the
deletion/re-addition history of unrelated links.

> **Violation example without hash-seeding:** if gap-fill assigned values in `topo.Edges` order, a
> deleted-then-re-added `A–C` edge would land at whatever array slot the UI appended it to, picking
> up a different free port than it had before — even though no other link changed. Seeding the
> gap-fill order on `pinKey` makes the re-added link's value a pure function of the link identity
> and the current set of reservations, satisfying I2 and I9.

## Pin validation

Pin validation runs in Pass 1 stage 2 (before any reservation) and is part of the semantic
validation contract ([validation.md](validation.md)). The following rules MUST be enforced; each
failure is a compile-blocking error, not a warning.

| Rule | Condition | Error |
|---|---|---|
| **Duplicate pin** | two distinct **linkKeys** reserve the same port-on-a-node, the same transit IP, or the same link-local (forward/reverse edges of one link share values legitimately; parallel links of the same pair are distinct linkKeys and MUST NOT share) | reject: pinned value reused across links |
| **Out-of-CIDR transit** | a `pinned_*_transit_ip` value is not inside the link's domain `transit_cidr` | reject: pinned transit address outside pool |
| **Out-of-range port** | a `pinned_*_port` is `< 1` or `> 65535`, or below the node's base `listen_port` | reject: pinned port out of range |
| **Stale base** | a pin references a pool that no longer applies — e.g. `transit_cidr` was narrowed so the pinned transit IP is now out of pool, or the node's `listen_port` base moved above the pinned port | reject: stale pin; operator must clear it to renumber (I7) |
| **Partial pin** | for a single resource on one link, one end is pinned and the other is empty | reject: pins must be set as a complete pair |
| **Client-edge pin** | an edge whose `from` or `to` is a `client`-role node carries `pinned_*_port` (clients use a single `wg0`, not per-peer listen ports — see [peer-derivation.md](peer-derivation.md)) | reject: client edges have no per-peer port pins |

> **Compliance:** none of these checks exist today — there are no pins to validate
> (`internal/validator/semantic.go`). Added with the pin fields in Plan 7. Stale-base detection
> reuses the same idea the allocator already applies to overlay IPs (clearing an `overlay_ip` that
> falls outside its domain CIDR, `internal/allocator/ip.go:32-49`), generalized to pins.

## Key persistence

Stated normatively in [../data-model/node.md](../data-model/node.md) and
[../security/security.md](../security/security.md); summarized here because it is the I5 mechanism.

- A node with a **non-empty `wireguard_public_key`** is treated as **key-fixed**: the compiler
  reuses the existing key pair and MUST NOT generate a fresh one.
- A node with **no public key** is new: the compiler generates a fresh key pair and persists the
  resulting public key back onto the node so the next compile sees it as fixed.
- Key rotation is an explicit operator action only: clearing the node's key fields (or pasting a
  new private key) triggers regeneration. Adding an unrelated node MUST NOT rotate any key.

> **Compliance:** `generateKeys` currently keys off the boolean `fixed_private_key` flag and, for
> non-fixed nodes, generates a random key every compile and blanks the node's stored key
> (`internal/api/handler.go:267-317`, specifically `:302-314`). The persistence rule shifts the
> trigger to "public key present" so growth is non-disruptive. Closed by Plan 7.

## Migration

The persistence rule changes how an existing deployment recompiles: under the old behavior the live
private key lives only on the deployed host (`/etc/wireguard/*.conf`), not in the topology JSON, so
the first compile under the new rule has no public key to reuse and would otherwise rotate. Per the
[Decisions log](../../../implementation_plans/audit-remediation-and-allocation-stability-2026_06_07/outline.md)
(decision 7), migration uses the **existing `fixed_private_key` field** — no new key-import tooling
is introduced. An operator chooses one of two one-time paths:

1. **Paste live keys (zero rotation).** For each already-deployed node, read the live private key
   from that host's `/etc/wireguard/<iface>.conf` (`PrivateKey = ...`), set the node's
   `fixed_private_key: true`, and paste the value into `wireguard_private_key`. The next compile
   derives the matching public key and reuses it; no node's key changes, so no redeploy is needed
   for key reasons. Handle pasted keys per the operator's secret-handling rules — they are live
   credentials.
2. **One final rotation (default).** Accept a single fleet-wide key rotation on the first compile
   under the new scheme: every node receives a fresh key, the public keys are persisted, and from
   that compile onward keys are sticky. This is one redeploy of the whole fleet, after which growth
   is zero-touch.

Either path is a **one-time** migration. After it, I5 holds and subsequent compiles reuse keys.

## Scope note

I1–I10 plus the sticky-pin mechanism guarantee **byte-stable bundles and additive deployment
scope**: adding `C` via `A` leaves `B`'s bundle byte-identical (I4), so `B` needs no action. They
do **not** by themselves guarantee zero packet-level disruption during the apply step on a node
that *is* being reconfigured. Full zero-touch growth additionally requires two capabilities that
are **REQUIRED DEPENDENCIES but out of scope for this spec** (future subjects):

- an **additive-apply install path** that brings up only the new interface instead of
  down-all/up-all (today's install script bounces every tunnel —
  [../artifacts/install-script.md](../artifacts/install-script.md)); and
- a **per-node deploy selector** so the operator can deploy only the changed/new nodes instead of
  the whole fleet ([../artifacts/deploy-scripts.md](../artifacts/deploy-scripts.md)).

Both are flagged in the dossier (T13) and the outline (decision 10) as future subjects. This spec's
contract is satisfied when the **generated bundles** are byte-stable for unchanged nodes; the apply
and deploy-selection mechanics are tracked separately.
