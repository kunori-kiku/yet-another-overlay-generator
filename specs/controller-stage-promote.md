# Controller stage and promote

<!-- last-verified: 2026-07-17 -->

## Responsibility

Own the deployment transaction from a saved topology snapshot to a promoted generation: select the
deployment-ready subgraph, drive the shared AgentHeld compile path, conditionally persist allocation
pins, publish one exact staged candidate, and make promotion the only go-live transition
(`internal/controller/compile_subgraph.go:21-106`, `internal/controller/compile_stage.go:76-196`,
`internal/controller/compile_promote.go:12-73`).

## Files

- `internal/controller/compile.go:51-75` — defines the staged, unchanged, skipped, policy-omitted,
  and provisional-generation result contract.
- `internal/controller/compile.go:122-157` — defines served-bundle identity and the shared keystone
  first-pin/rotation exception to delta skipping.
- `internal/controller/compile_subgraph.go:21-218` — projects readiness and calls the canonical
  `localcompile` facade in AgentHeld custody.
- `internal/controller/compile_subgraph.go:233-288` — merges compiled allocation pins into the full
  topology through a versioned compare-and-set.
- `internal/controller/compile_manualnode.go:19-79` — validates the public identities asserted for
  manual nodes before they enter bundles or membership.
- `internal/controller/compile_stage.go:22-339` — owns force/policy options and the mutating stage
  sequence.
- `internal/controller/compile_preview.go:41-145` — runs the read-only counterpart with the same
  readiness, digest, and full-restage decisions.
- `internal/controller/compile_promote.go:12-73` — applies the optional off-host signature gate and
  delegates the already-sealed flip to the Store.
- `internal/controller/tenantlock.go:28-45` — serializes topology Save with multi-call deployment
  operations for one tenant.

## Inputs

`CompileAndStage` receives a tenant, timestamp, and optional force or telemetry-policy deployment
mode, then loads the versioned public topology, node registry, and controller settings
(`internal/controller/compile_stage.go:47-80`, `internal/controller/compile_stage.go:92-134`).
Managed readiness requires an approved registry node with a public key; manual readiness uses its
validated topology public key. A client is withheld until its enabled target is also ready, and
edges with either endpoint absent are omitted (`internal/controller/compile_subgraph.go:127-218`).

Executable policy preparation comes from [Telemetry policy](telemetry-policy.md), bundle bytes and
tier-one signatures come from [Artifacts and signing](artifacts-signing.md), and the optional
membership record comes from [Keystone and trust lists](keystone-trustlist.md)
(`internal/controller/compile_stage.go:121-123`, `internal/controller/compile_stage.go:198-217`,
`internal/controller/compile_stage.go:302-320`).

## Outputs

Stage returns exact node-ID lists for changed, unchanged, not-ready managed, and compatibility-
projected nodes. Its generation is `current+1` only when a non-empty candidate was published;
zero-change deployment returns the current generation (`internal/controller/compile.go:51-75`,
`internal/controller/compile_stage.go:276-300`, `internal/controller/compile_stage.go:332-338`).

The candidate passed to `ReplaceStagedSet` contains only changed bundles at one provisional
generation plus an optional manifest binding the full ready fleet, including unchanged nodes
(`internal/controller/compile_stage.go:228-274`, `internal/controller/compile_stage.go:302-320`).
After the keystone gate, Store promotion writes current bundles, desired generations, and the signed
served trust list before committing the new generation last (`internal/controller/compile_promote.go:40-73`,
`internal/controller/storecore_stage.go:466-499`). Persistence mechanics and served snapshots belong
to [Controller store](controller-store.md).

## Decision points (if any)

- No stored topology is a benign no-op. An existing topology with no ready nodes clears the active
  staged set before signer resolution, so obsolete bytes cannot remain promotable because a signing
  key is unavailable (`internal/controller/compile_stage.go:92-100`,
  `internal/controller/compile_stage.go:135-151`).
- A freshly compiled checksum digest equal to the currently served digest is unchanged unless force
  was selected or keystone first-pin/rotation requires a complete restage. Missing or unreadable
  comparison data fails toward staging (`internal/controller/compile.go:122-157`,
  `internal/controller/compile_stage.go:182-190`, `internal/controller/compile_stage.go:243-274`).
- If every ready node is unchanged, stage clears any older candidate and emits no promotable
  generation; otherwise one `ReplaceStagedSet` call publishes the exact changed set
  (`internal/controller/compile_stage.go:276-300`, `internal/controller/compile_stage.go:314-338`).
- With no pinned credential, promotion uses the compatibility path. With one, the staged manifest
  must carry a non-empty signature that verifies against the current pin before Store promotion is
  attempted (`internal/controller/compile_promote.go:40-73`).

## Invariants

- Compilation never imports private custody: the ready projection replaces managed keys from the
  registry, clears every topology private key, and invokes `localcompile` with `render.AgentHeld`
  (`internal/controller/compile_subgraph.go:130-148`, `internal/controller/compile_subgraph.go:194-208`,
  `internal/controller/compile_subgraph.go:85-106`).
- Topology Save and stage share the tenant operation lock; allocation write-back additionally checks
  the originally loaded version. A concurrent Save therefore remains intact and stale staging returns
  `ErrTopologyChanged` before export or staged-set mutation (`internal/controller/tenantlock.go:37-45`,
  `internal/controller/compile_subgraph.go:241-288`, `internal/controller/compile_stage.go:191-217`,
  `internal/controller/storecore.go:225-251`).
- Loose files are never deployment authority: replacement invalidates the old seal first and writes
  the exact generation/node-set/manifest seal last; promotion revalidates it and commits generation
  only after current bundles and the served trust list (`internal/controller/storecore_stage.go:234-374`,
  `internal/controller/storecore_stage.go:420-499`).

## Gotchas (optional)

- A partial stage write is intentionally inert until a clean restage overwrites/prunes it and
  publishes a new seal; the error matches both `ErrIncompleteStagedSet` and `ErrNoStagedBundle`
  (`internal/controller/storecore_stage.go:40-42`, `internal/controller/storecore_stage.go:276-370`,
  `internal/controller/storecore_stage.go:434-463`).
- If promotion crashes after copying live records but before generation commits, later stage and
  generation mutations fail with `ErrUncommittedPromotion`; retrying the exact sealed promotion is
  the recovery path because staged inputs remain until the commit point
  (`internal/controller/storecore_stage.go:56-91`, `internal/controller/storecore_stage.go:420-422`,
  `internal/controller/storecore_stage.go:466-505`).
- Post-commit staged cleanup is best-effort. Its remaining seal is stale or explicitly historical,
  so it cannot authorize another promotion (`internal/controller/storecore_stage.go:501-521`).
