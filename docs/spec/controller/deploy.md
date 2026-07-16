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

### Active telemetry requires the off-host keystone

An admitted managed node may carry a bounded `telemetry_probes` policy. That policy becomes outbound
network activity, so it has a stronger gate than passive metrics: both `DeployPreview` and
`CompileAndStage` refuse a probe-bearing ready subgraph unless the tenant has a pinned off-host
keystone. The refusal occurs before export, allocation write-back, or staging and maps to HTTP 412
`telemetry_probes_require_keystone`.

When authorized, AgentHeld rendering emits versioned `telemetry.json` as an ordinary checksum- and
signature-covered bundle member. The agent repeats membership verification and activates it only after
a successful apply, preserving the previous policy on failure. See
[../operations/active-telemetry.md](../operations/active-telemetry.md). This gate does not apply to a
node with no probes, so existing fleets and historical bundle bytes remain compatible.

## Delta deploy — skip unchanged nodes

Render-what's-ready decides **which nodes are eligible** to stage; the **delta-skip** (plan-5) decides
which of those eligible nodes actually **need** re-staging. Without it, every Deploy re-staged every
enrolled node at a new generation, so every agent re-fetched and re-applied an **identical** bundle —
a needless fleet-wide churn (and a brief per-link re-handshake) even when nothing about a node changed.
The delta-skip makes a Deploy touch only the nodes whose config actually differs.

**The content identity — the served-bundle digest.** For each freshly compiled node bundle the
controller computes `bundleSHA256 = hex(sha256(checksums.sha256))`, and `checksums.sha256` covers
`install.sh` + **every config file** — but **not** the manifest's volatile `compiled_at`. So the digest
is **byte-stable across recompiles** when the node's actual configuration is unchanged, and changes the
moment any rendered byte does. This is the **same** digest the keystone manifest binds (§Zero-knowledge
custody / [signing.md](signing.md)), reused as the change-detection identity — no second hashing scheme.

**The per-node decision.** For each enrolled node, `CompileAndStage` compares the freshly compiled
`bundleSHA256` against `servedBundleDigest(node)` — `hex(sha256(...))` of the node's **currently served
(promoted) bundle**. If they are **equal** (and the node is not force-listed, and the skip is enabled —
below), the node is **unchanged**: it is **not** re-staged and **keeps its current generation**. It is
reported in `StageResult.UnchangedNodeIDs`; the nodes that did change are `StageResult.Staged`. Together
they are the full enrolled set for a normal deploy.

**Why keeping the generation is what avoids the re-fetch.** `PromoteStaged` stamps a new
`DesiredGeneration` only on the nodes it actually promotes. An unchanged node was never staged, so its
`DesiredGeneration` is untouched, so its agent's `/poll` never sees a newer generation and **never
re-fetches** — the fleet ends up at a **mixed generation** (changed nodes advanced, unchanged nodes held
back), which is correct: each node's generation is independent, and a node only advances when its own
config changes. A brief per-link re-handshake now happens **only** on the links whose endpoints actually
changed, not fleet-wide.

**FAIL OPEN.** `servedBundleDigest` returns `ok = false` on **any** uncertainty — the node was never
promoted (no served bundle), its `checksums.sha256` is missing, or the store read errored. In every such
case the node is treated as **changed** and **staged**. The skip is a pure optimisation; it never risks
stranding a node on stale config by skipping on doubt.

**Keystone-aware — the skip disables itself when a promote must re-pin the trust-list.** `stageSkipEnabled`
gates the whole delta-skip, and returns **false** (force a full re-stage of every node) for the two
keystone cases that change **zero** bundle bytes yet still require the promote to flip the **served
trust-list**:

- **First pin** — the keystone is on but there is **no served trust-list yet** (`GetServedTrustList` →
  `ErrNotFound`). The very first signed deploy must stage every node so the initial manifest binds them
  all.
- **Rotation** — `KeystoneRedeployRequired` (the served trust-list is signed under a rotated-away
  credential). The re-signed manifest must re-pin every node.

In both cases a per-node skip would leave the regenerated/re-signed trust-list bound to only the handful
of content-changed nodes, **stranding** the served trust-list for the rest — so the skip stands down and
the manifest binds the whole enrolled set (`digests` is populated for **all** nodes, changed or not,
whenever the keystone is on). A non-keystone tenant, or a healthy keystone past its first pin with no
pending rotation, runs the skip normally. `stageSkipEnabled` is **shared** by `CompileAndStage` and
`DeployPreview`, so a preview can never disagree with what a real Deploy would do.

**The zero-changed short-circuit MUST purge.** When **every** enrolled node matches its served bundle,
nothing is staged and there is no new promotable generation — `CompileAndStage` returns the **current**
generation with the whole set in `UnchangedNodeIDs`, and does **not** re-stage the manifest. But it must
still **purge any lingering staged bundle**: a bundle left staged by a prior *stage-without-promote*
(e.g. an off-host keystone sign-wait) is a **superseded** design, and leaving it would let the next
`/promote` flip a reverted/retracted config **live** (the beta.4–6 stale-config custody class). Because
the stage set is empty here, `PruneStagedBundles(…, staged=∅)` purges **all** staged bundles (each
audited `purge-staged`), exactly as the empty-subgraph path does. A `stage-unchanged` audit entry records
the no-op deploy.

**Force — the operator escape hatch.** `CompileAndStage` takes variadic `StageOption`s:
`WithForceAll()` re-stages **every** enrolled node even if unchanged; `WithForceNodes(ids…)` re-stages
the **named** nodes. Force is for **on-host drift / rescue only** — a node whose local config was
tampered with or lost and needs its bundle re-pushed at a fresh generation. It is **not** needed for the
ordinary cases: a genuine config change re-stages naturally (its digest moved), and keystone
rotation / first-pin auto-force a full re-stage. The per-node force is also reachable from the
node-detail page (`forceRedeployNode`), which reuses the same `deploy(force_nodes:[id])` path.

**Pre-deploy preview — a read-only dry-run.** `DeployPreview` compiles the operator's **current canvas**
(the same public-keys-only topology a Deploy pushes and stages, healed for pin collisions) and reports,
per enrolled node, whether an **unforced** Deploy **would** re-stage it (`Changed`) or skip it, plus a
`KeystoneFullRestage` flag when the skip is disabled — **without** staging anything. It shares the
**same** `stageSkipEnabled` decision and the **same** `bundleSHA256`-vs-`servedBundleDigest` identity as
the real stage, so "N updated, M unchanged" in the Deploy dialog matches the actual outcome. It is
**fail-open** (a node whose served digest can't be read previews as `Changed`) and, in the panel,
**best-effort**: a preview that fails (e.g. a newer panel against an older controller with no
`deploy-preview` route) surfaces the error but never blocks the operator from deploying anyway.

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
install time ([key-custody.md](key-custody.md), [agent.md](agent.md)). The stage resolves
`YAOG_BUNDLE_SIGNING_KEY` once, then passes that same in-memory `ConfigSigner` through render,
persisted-anchor reconciliation, and `artifacts.ExportWithSigner`. A mid-stage key-file replacement
therefore cannot split the public key embedded in `install.sh`, the pinned anchor, and the detached
`bundle.sig` ([signing.md](signing.md)).

## Canonical node identity

`node.ID` is the single identity for a per-node bundle directory, registry record, staged
`SignedBundle`, agent pull, manual kit membership, and browser/WASM ZIP entry. The controller's
temporary export round-trip reads only `<tmp>/<node.ID>/`; it never falls back to `node.Name` or
bridges between two filesystem keys. `node.Name` remains display text and an input to WireGuard
interface naming. Schema and semantic validation enforce the portable, case-fold-unique ID contract
before the exporter runs; see [../artifacts/naming.md](../artifacts/naming.md).

## Audit

A full deploy leaves a complete, hash-chained audit trail (`AppendAudit`,
[persistence.md](persistence.md) §audit hash chain). The stage-path entries, all `Actor:"operator"`:

- **`stage`** — one per invocation that actually staged ≥1 node (fleet-wide, empty `NodeID`).
- **`stage-empty`** — a zero-enrolled stage (controller-server-authority-redesign plan-3). This is the
  design-destroying-deploy shape (every node skipped), so it is now recorded rather than silent. The
  **no stored topology** path (`GetTopology` → `ErrNotFound`) still returns before any audit (nothing
  was ever there to stage); the **zero-enrolled** path audits.
- **`purge-staged`** (one per node) — when a re-stage (including a zero-node stage) drops a node that
  was staged before but is no longer in the stage set, its stale staged bundle is purged so it cannot
  go live on a later promote, and each purge is attributable.

`update-topology` and `promote` append their own entries (plan-1 closed those gaps), and a duplicate-WG-pubkey
refusal is audited on both write paths (plan-6): the enroll path appends `enroll-rejected-duplicate-key`,
the rekey path `rekey-rejected-duplicate-key`. The stage-path
audits are best-effort (post-commit): a failed audit append does not fail an already-committed stage.

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

## Fleet-wide key rotation — wake, rotate, re-register, then ONE operator Deploy

Rotating every node's WireGuard key is **operator-driven** and converges through the **same**
compile→stage→promote deploy model — never auto-deployed. It is a **four-step sequence**, and the
operator must wait for one step to finish before triggering the next:

1. **Operator: Roll keys.** `POST /rekey-all` ([controller-api.md](controller-api.md), operator port)
   flags every `NodeApproved` node with `RekeyRequested=true` (pending/revoked nodes are skipped, so the
   returned `{requested}` is the approved count) **and then `Store.BumpGeneration`s the tenant generation**
   to **WAKE** the fleet. The bump is a **WAKE, not a deploy**: it advances the generation counter (so any
   parked daemon agent's `WaitForGeneration` long-poll fires — that primitive wakes **only** on a
   generation advance) **without changing any bundle** — `GetCurrentBundle` keeps returning the last
   promoted bundle for every node. Without this bump a flagged agent would never wake to see the signal
   (the deadlock this design fixes). One `rekey-request` audit entry (actor `operator:*`, empty `node_id`)
   is appended.

2. **Each agent: rotate + re-register, SKIP apply.** A woken agent runs one
   `agent.RunControllerCycle` ([agent.md](agent.md)): it `Fetch`es `/config`, sees `rekey_requested=true`,
   `RegenerateKey`s its **local** private key, and `POST /rekey`s the **new PUBLIC key** (zero-knowledge —
   the controller never sees a private key), which **clears** the node's `RekeyRequested` flag and appends a
   per-node `rekey` audit entry (actor `agent:<id>`, `node_id=<id>`). The agent **does NOT apply** the woken
   bundle: that bundle was compiled with peers' **OLD** public keys, so applying it would be a regression.
   It instead **advances its resume watermark PAST the wake generation** so it will never re-fetch+re-apply
   that stale bundle; the next generation it applies will be **strictly greater**. (The bumped generation
   reports the OLD bundle's smaller generation on `/config` because the bundle was not re-compiled, so the
   agent resumes from the **polled wake generation**, not the fetched bundle generation — see
   [agent.md](agent.md) §watermark advance.)

3. **Operator: wait for the badges to clear.** The operator panel renders a "rotating keys" badge per node
   from `nodeJSON.rekey_requested`. The operator **waits until every badge has cleared** (every node has
   re-registered its new public key) before deploying. Deploying mid-rotation would recompile the topology
   while some nodes still carry old and others carry new public keys — a **mixed-key** render that would not
   converge. The panel's Deploy control is **disabled while any node still shows the badge** to enforce this.

4. **Operator: Deploy ONCE.** With all nodes re-registered, a single normal **compile+stage+promote**
   recompiles the fleet from the **new** public keys now in the registry and promotes a strictly-greater
   generation. Every agent applies it on its next cycle and the fleet converges on the rotated keys.

**Honest cost — a brief per-link flap.** The post-rotation Deploy is a **rolling** apply: as each agent
picks up the new generation it re-handshakes its tunnels with the new keys, so a link flaps briefly until
**both** of its endpoints have applied. Babel re-converges around each flap, so connectivity is restored
within a re-convergence interval per link rather than a fleet-wide outage. This brief, rolling per-link flap
is the **accepted cost** of a zero-knowledge rotation that never exposes a private key and never trusts the
controller with one.

## Summary

- A deploy is **compile+stage** (mechanical, reversible) then operator-gated **promote** (commits a new
  generation, wakes agents).
- **Delta-skip**: a node whose freshly compiled `bundleSHA256` (of `checksums.sha256`, excluding the
  volatile `compiled_at`) equals its **served** bundle is **not** re-staged and **keeps its generation**,
  so its agent never re-fetches — a Deploy touches only changed nodes (mixed-generation fleet).
  **Fail-open**; **disabled** for keystone first-pin / rotation (which must re-pin the whole trust-list);
  the zero-changed path **purges** lingering staged bundles. `WithForceAll` / `WithForceNodes` override
  it for on-host drift/rescue; `DeployPreview` is the read-only "N updated, M unchanged" dry-run over the
  current canvas.
- **Fleet-wide key rotation** is `POST /rekey-all` (flag approved nodes **+** `BumpGeneration` to WAKE the
  fleet, a generation advance with NO bundle change) → each agent **rotates + re-registers + skips apply**
  (advancing its watermark past the wake) → the operator **waits for every badge to clear** → **one** normal
  Deploy recompiles from the new public keys. A brief, rolling per-link flap during that Deploy is the
  accepted cost.
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
