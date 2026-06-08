# Controller Compile / Stage / Promote (Phase 2 — plan-4.3a, the deploy model)

This document defines how the controller turns a tenant's stored, **public-keys-only** topology plus
its **enrolled registry** into signed, per-node bundles that a node agent can pull and apply. It
covers the **compile → stage → promote** model, the **render-what's-ready** policy that decides which
nodes and edges are rendered, and the decision to **reuse the frozen compiler/renderer/exporter**
rather than reimplement them. It is the deploy half of the controller: it sits between the registry
that [enrollment.md](enrollment.md) populates and the bundles that [signing.md](signing.md) signs and
[agent.md](agent.md) pulls, persisted through the `Store` of [persistence.md](persistence.md) under the
custody contract of [key-custody.md](key-custody.md).

**Scope of this milestone (plan-4.3a).** This document and the `CompileAndStage` step
(`internal/controller/compile.go`) are the **compile/stage core**: pure, in-process, no HTTP. The
operator-facing **HTTP endpoints** (`/stage`, `/promote`, `/config`, `/poll`, `/report`) and the
**plain-HTTP server** that **bearer-token-authenticates** them are [controller-api.md](controller-api.md)
(plan-4.5); the **node-agent integration** (the agent's keygen→enroll→pull→verify→apply loop wired against
the live controller) and the **end-to-end** tests are [agent.md](agent.md) (plan-4.5). The promote half
of the model is the existing `Store.PromoteStaged` ([persistence.md](persistence.md)); this milestone
produces the staged bundles it flips. See
[../../../implementation_plans/controller-panel-2026_06_08/plan-4-2026_06_08.md](../../../implementation_plans/controller-panel-2026_06_08/plan-4-2026_06_08.md).

## The compile / stage / promote model

A deploy is a **two-phase, operator-gated** transition over a tenant's generation counter:

1. **Compile + stage** (`CompileAndStage`, this document). The controller loads the stored topology,
   selects the enrolled subgraph, drives the frozen pipeline to render and sign per-node bundles, and
   **stages** each bundle at the **next** generation (`CurrentGeneration + 1`). Staging is reversible
   and invisible to agents: a staged bundle is not yet `current`, so `GetCurrentBundle` and the `/poll`
   long-poll do not surface it. Re-running `CompileAndStage` replaces the prior staged set for the same
   prospective generation — the operator can stage, inspect, and re-stage freely before committing.

2. **Promote** (`Store.PromoteStaged`, [persistence.md](persistence.md)). The operator commits the
   staged set: promote **atomically** flips all staged bundles to `current`, increments the tenant's
   generation, stamps `DesiredGeneration` on each promoted node's registry record, and wakes any
   `WaitForGeneration` waiters (the `/poll` primitive). Only at promote does a new generation become
   visible to agents.

The split is deliberate: rendering is mechanical and cheap to redo, but **flipping what the fleet
applies is a decision**. The operator decides when a staged generation goes live; the controller never
self-promotes. This is why `CompileAndStage` returns the **staged generation** (not the current one) in
its `StageResult` — it tells the operator what would go live, not what is live.

### Generation arithmetic

`CompileAndStage` reads `Store.CurrentGeneration` (0 if nothing has ever been promoted) and stages at
`current + 1`. It does **not** advance the counter — promote does. So repeatedly staging without
promoting always targets the **same** prospective generation; the counter moves only when the operator
commits. A fresh tenant with one enrollment, staged once and never promoted, has current generation 0
and a staged generation 1; after promote, current becomes 1.

## The render-what's-ready policy

The controller renders **only the enrolled subgraph** of the stored topology — never the full design.
This is what lets an operator design the whole intended fleet up front, then bring nodes online
incrementally: each deploy renders exactly the part of the design that is **ready**, and the rest fills
in on later deploys as more nodes enroll.

**The enrolled-subgraph filter.**

- **Node admission.** A topology node is included **iff** its registry record is `NodeApproved` **and**
  has a non-empty `WGPublicKey`. A node that is `NodePending` (slot created, not yet enrolled),
  `NodeRevoked`, or has no public key is **excluded**. The included node's `WireGuardPublicKey` is set
  from the **registry** value (authoritative — the agent holds the matching private key), and any stray
  `WireGuardPrivateKey` carried on the stored topology node is **cleared** before rendering, preserving
  zero-knowledge custody (see below).
- **Edge dropping.** An edge is kept **iff both** its `FromNodeID` and `ToNodeID` are in the enrolled
  set. An edge whose far end has not enrolled is **omitted** from this render — the near node's bundle
  simply does not yet carry that peer interface. When the far end later enrolls and the operator
  re-deploys, the edge reappears in **both** nodes' bundles. Nothing about the design is lost; the
  peering simply **activates on a later deploy**.
- **Reporting.** Excluded nodes are returned in `StageResult.SkippedUnenrolled` (node IDs), so the
  operator sees exactly who is waiting on enrollment. Included nodes are returned in
  `StageResult.Staged` (node IDs).
- **Empty cases.** No stored topology (`ErrNotFound`) → an empty `StageResult` with no error (nothing to
  stage yet). Zero enrolled nodes → an empty `StageResult` (with `SkippedUnenrolled` populated) and no
  error — staging nothing is a benign no-op, not a failure.

**Idempotent fill-in — via allocation write-back.** Allocation stability (invariant I10, see
[../compiler/allocation-stability.md](../compiler/allocation-stability.md)) does **not** come for free
here: the IP/transit/port allocators are sequential, so compiling a *growing* subgraph would renumber
existing nodes unless their allocations are pinned. `CompileAndStage` therefore **persists the compiled
pins back into the stored topology** after each run — per-node `overlay_ip` and the per-edge transit
IPs, link-locals, and ports (never any key material; the stored topology stays public-keys-only). The
**next** `CompileAndStage` finds those pins in the stored topology and the compiler **sticky-pins**
them (reusing existing values, allocating only genuinely new entities). The result: re-deploying after
a new enrollment reproduces the **same** allocations for the already-staged nodes/edges and only
**adds** the newly-ready ones — incremental enrollment never perturbs a node that was already live.

**Client readiness.** A `client` role requires exactly one enabled outbound edge (the compiler treats
a clientless-edge as a hard error). So an enrolled client whose dial target (its router/relay/gateway)
is **not yet enrolled** is itself treated as **not ready**: it is reported in `SkippedUnenrolled` and
not staged, exactly like an unenrolled node, and activates on a later deploy once its target enrolls.
This keeps render-what's-ready honest for clients — a client enrolling before its router never fails
the whole stage.

## Reusing the frozen pipeline

`CompileAndStage` **reuses** the existing, tested pipeline end-to-end and reimplements **none** of it.
The compiler, renderer, and exporter stay **frozen and dependency-minimal** (the quarantine boundary of
[persistence.md](persistence.md)); the controller is a **caller**, not a fork:

```
render.GenerateKeys(&subgraph, render.AgentHeld)   // zero-knowledge key prep
compiler.NewCompiler().Compile(&subgraph, keys)    // pure topology → peer configs
render.All(result, keys)                            // pure compiled data → bundle bytes
artifacts.Export(result, tmpDir)                    // write per-node dirs (+ sign if env set)
```

This is the **same** path the air-gap CLI and HTTP API take — the controller adds only the subgraph
filter in front and the stage write-back behind. The payoff is no duplication and no refactor: the
standing custody/equivalence/signing tests in `internal/render` and `internal/artifacts` already cover
the bytes the controller stages, so the controller inherits their guarantees for free.

**The temp-dir round-trip.** `artifacts.Export` writes to a **filesystem** directory (one subdir per
node), so `CompileAndStage` exports to an `os.MkdirTemp` directory, reads each node's subdir back into a
`map[string][]byte` (via `filepath.Walk`, skipping directories, keyed by the bundle-relative
**slash** path through `filepath.ToSlash`), and removes the temp dir on return. This temp-dir
round-trip is a **deliberate, conservative choice**: it reuses the frozen exporter verbatim instead of
adding an in-memory export path that would duplicate the exporter's directory layout, checksum, and
signing logic. An **in-memory `Export`** (returning the file map directly, no disk) is a documented
**possible later optimization** — it would shave the round-trip but is not needed for correctness, and
it would have to reproduce the exporter's output byte-for-byte to stay safe.

## Zero-knowledge custody, preserved

The compile/stage path holds the zero-knowledge invariant end-to-end. `GenerateKeys` runs in
**`render.AgentHeld`** custody: it emits `PRIVATEKEY_PLACEHOLDER` for each node's own private half and
**never** returns a real private key, using each node's `WireGuardPublicKey` (which the controller set
from the registry) to render the fleet from public keys alone. The registry holds **public keys only**
([persistence.md](persistence.md) §Zero-knowledge custody), and the subgraph builder clears any stray
private key off a node before rendering, so a private key cannot reach a staged bundle even if an
imported topology carried one. The agent splices its locally-held private key into the placeholder at
install time ([key-custody.md](key-custody.md), [agent.md](agent.md)). **Signing** is the Phase-0
env-var path **inside** `artifacts.Export`: when `YAOG_BUNDLE_SIGNING_KEY` is set, Export writes a
`bundle.sig` + `signing-pubkey.pem` into each node dir ([signing.md](signing.md)); the controller does
not sign separately.

## The `node.Name` vs `node.ID` mapping (the 4.2 wart)

There are **two** identifiers for a node, and they live in different layers:

- **`node.Name`** is the topology node's human-facing name. The exporter names each node's directory
  (`<outDir>/<node.Name>/`) by it, so it is the key the **filesystem round-trip** uses.
- **`node.ID`** is the node's stable identity in the **registry** and the **agent** — it is what
  `SignedBundle.NodeID`, `Store.StageBundle`, `Store.GetNode`, and the agent's pull are keyed by.

This `Name`-vs-`ID` split is the **documented plan-4.2 wart**: the air-gap exporter predates the
registry and keys by name, while the controller and agent key by ID. `CompileAndStage` **bridges** the
two by mapping each enrolled node's exported directory (`node.Name`) back to its `node.ID` **via the
topology** (the controller holds both fields on every `model.Node`), then staging the bundle under the
ID. The bridge lives only in the controller; the frozen exporter is not changed to know about IDs.

## Audit

`CompileAndStage` appends **one** audit entry per invocation **that actually stages something**:
`{Actor: "operator", Action: "stage", NodeID: ""}` — fleet-wide (empty `NodeID`) because a stage covers
the whole enrolled subgraph, not a single node. The two benign no-op early returns — **no stored
topology** and **no enrolled node** — return before the audit append (they staged nothing, so there is
nothing to record). The entry is hash-chained like every other (`AppendAudit`,
[persistence.md](persistence.md) §audit hash chain). Promote appends its own entry separately.

## Revocation — clear the token + evict from the subgraph

Revoking a node is **two complementary moves**, both driven by a **single operator call**: the operator
route **`POST /revoke`** ([controller-api.md](controller-api.md), operator port) takes `{node_id}` and
performs the pair atomically — there is no separate "clear the token" and "set revoked" call for the
operator to forget one of. (`404` if the node is unknown; on success it returns `{node_id, revoked:true}`
and appends a `{Action:"revoke", NodeID:<node_id>, Actor:operator}` audit entry.) The two moves it makes:

1. **Clear the bearer token (immediate).** `/revoke` calls `RevokeNodeAPIToken(ctx, t, nodeID)`
   ([persistence.md](persistence.md) §The per-node API-token index, [enrollment.md](enrollment.md)
   §Revocation), which clears the node's `APITokenHash` and deletes its reverse-index entry, so the node's
   **very next** `/config` / `/poll` / `/report` call fails authentication at the chokepoint
   ([controller-api.md](controller-api.md)) — the bearer token **stops resolving immediately**. There is no
   TTL, no CRL, no propagation delay — control-plane access stops at once. This replaces the withdrawn mTLS
   model's certificate revocation, which could only be approximated (a still-time-valid client cert kept
   working until the ephemeral CA rotated).
2. **Evict from the rendered subgraph (durable).** `/revoke` also sets the node `NodeRevoked` (preserving
   the node's other registry fields), which excludes it from every subsequent `CompileAndStage` (§The
   render-what's-ready policy: a `NodeRevoked` node is **not** admitted, and edges to it are dropped), so
   even a node that somehow still held a credential could obtain **no new configuration**, and its peers
   stop carrying the peer interface to it on the next deploy. `LookupNodeByAPIToken` also fail-closes at the
   lookup chokepoint — a token whose node is no longer `NodeApproved` (e.g. `NodeRevoked`) or whose stored
   `APITokenHash` no longer matches the presented token resolves to `ErrTokenInvalid` — so the two moves
   reinforce each other and a stale/orphaned index entry can never authorize.

Together: clearing the token is the **immediate** cut (the node can no longer authenticate), and
`NodeRevoked` is the **durable** eviction (the node disappears from future renders and its peers
re-converge without it). The honest bound is that a token leaked **before** revocation is replayable until
the revoke lands ([controller-api.md](controller-api.md) §the honest trade-off); immediate revocation is
how that window is closed.

## Summary

- A deploy is **compile+stage** (mechanical, reversible) then operator-gated **promote** (commits a new
  generation, wakes agents).
- The render-what's-ready policy renders **only** approved nodes with a public key, **drops** edges to
  unenrolled peers, and **fills them in** on re-deploy — incremental fleet bring-up without perturbing
  live nodes.
- The frozen compiler/renderer/exporter are **reused** through a temp-dir round-trip (no duplication, no
  refactor; in-memory Export is a possible later optimization).
- **Zero-knowledge** is preserved: AgentHeld placeholder keys, public-keys-only registry, signing inside
  Export.
- The controller bridges the **`node.Name`-vs-`node.ID`** mapping via the topology.
- **Revocation** is **clear the bearer token** (immediate cut) **+** `NodeRevoked` eviction from the
  rendered subgraph (durable).
- **HTTP endpoints + bearer-token auth + two plain-HTTP ports** are [controller-api.md](controller-api.md)
  (plan-4.5); **agent integration + e2e** are [agent.md](agent.md) (plan-4.5).
