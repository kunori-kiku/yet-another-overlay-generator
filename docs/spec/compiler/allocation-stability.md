# Allocation Stability & Growth

> **Status: DRAFT — normative text pending.** This component spec is being authored from the
> verified allocation-stability audit (2026-06). The invariants below define what the allocator
> and peer-deriver MUST guarantee so that an already-deployed overlay can grow (add nodes/edges)
> without disturbing existing servers. Current behavior does not yet satisfy most of them.

## Planned normative invariants

- **I1 — Superset stability**: recompiling a superset topology MUST reproduce identical allocated
  values (overlay IP, listen port, transit pair, link-local pair, WireGuard public key) for every
  pre-existing entity.
- **I2 — Order independence**: allocated values MUST NOT depend on array position of nodes/edges
  or on the enable/disable/re-add history of other entities.
- **I3 — Identity binding**: every allocated value MUST be bound to a stable entity identity and
  honored on the next compile, not recomputed from ordinal position.
- **I4 — Additive growth**: adding an entity MUST require redeploying only that entity and its
  direct neighbors; all other bundles MUST be byte-stable.
- **I5 — Key persistence**: a node's WireGuard public key MUST be stable across recompiles once
  assigned; re-randomization only for new nodes or explicit operator-initiated rotation.
- **I6 — Headroom / pool exhaustion**: port-space and transit-pool capacity MUST be defined and
  exhaustion surfaced as a clear error before silent collision.
- **I7 — Validated pins / explicit renumber**: pinned values MUST be validated pre-compile;
  renumbering MUST be explicit, never a side effect of adding an unrelated entity.
- **I8 — Change observability**: allocated values MUST be visible in the persisted topology, and
  bundles byte-deterministic for unchanged inputs, so changed nodes are identifiable.
- **I9 — GC / no leak**: deleting an entity MUST free its pinned values for reuse.
- **I10 — Schema versioning**: the allocation scheme MUST carry a version so future changes can
  migrate prior allocations.

**Scope note:** I1–I10 guarantee byte-stable configs and additive deployment scope. Zero tunnel
disruption on a peered node additionally requires an additive install-script apply path and a
per-node deploy selector (see [../artifacts/install-script.md](../artifacts/install-script.md)
and [../artifacts/deploy-scripts.md](../artifacts/deploy-scripts.md)) — referenced dependencies,
out of this spec's scope.
