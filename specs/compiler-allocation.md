# Compiler and allocation

<!-- last-verified: 2026-07-17 -->

## Responsibility

Transform a key-prepared topology into a compiled topology: enforce the schema and semantic gates,
allocate overlay addresses, infer role capabilities, derive per-link peers and client `wg0` inputs,
and write sticky link resources back into the returned topology. Rendering and export are later
components (`internal/compiler/compiler.go:180-235`, `internal/compiler/compiler.go:237-335`,
`internal/localcompile/compile.go:80-104`).

## Files

- `internal/compiler/compiler.go:15-355` — owns compiler construction, validation and
  allocation sequencing, result assembly, pin write-back, and manifest metadata.
- `internal/allocator/ip.go:13-228` — preserves valid overlay addresses and performs bounded,
  cancellable lowest-free IPv4 allocation around reserved ranges.
- `internal/compiler/peers_build.go:17-431` — groups links and reserves existing resources
  before deterministic gap-fill.
- `internal/compiler/peers_build.go:434-1040` — builds forward/reverse peers and client
  configuration inputs from the completed allocation map.
- `internal/compiler/peers_prealloc.go:7-123` — represents and extracts resources occupied by edges
  excluded from a controller subgraph.
- `internal/compiler/roles.go:41-143` — derives role semantics and normalized capabilities.
- `internal/linkid/linkid.go:23-65`, `internal/allocconst/allocconst.go:22-53`, and
  `internal/compiler/orientation.go:3-15` — single-source link identity, shared allocation constants,
  and forward/reverse resource orientation.
- `internal/normalize/pins.go:1-238` — owns deterministic migration of historical pin collisions; it
  is a pre-compiler repair boundary, not an allocator side effect.

## Inputs

`CompileAt` receives a context, topology, prepared node key map, and explicit compile timestamp;
optional compiler state supplies controller-only excluded-edge reservations and the fleet mimic
fallback default (`internal/compiler/compiler.go:121-180`). The canonical `localcompile` facade first
copies every topology collection and resolves custody through
[Render and key custody](render-keys.md), then passes those inputs to `CompileAt`
(`internal/localcompile/compile.go:61-102`).

Schema and semantic rules belong to [Model and validation](model-validation.md); the compiler runs
those two gates in order and retains their warnings only after both are valid
(`internal/compiler/compiler.go:180-197`). A controller subgraph additionally supplies resources
reserved from excluded enabled edges so new ready links cannot claim their stored ports or addresses
(`internal/controller/compile_subgraph.go:71-100`, `internal/compiler/peers_prealloc.go:30-108`).

## Outputs

`CompileResult` returns the allocated topology, per-node `PeerInfo` lists, client configurations,
validation warnings, and a compile manifest; render-owned maps are initialized but still empty at
this boundary (`internal/compiler/compiler.go:25-80`, `internal/compiler/compiler.go:312-335`). The
topology carries allocated overlay IPs, inferred capabilities, allocation-schema version, and six
oriented edge pins. Each enabled compiled edge with an endpoint host carries its effective `CompiledPort`
(`internal/compiler/compiler.go:199-235`, `internal/compiler/compiler.go:237-310`).

Local Design adopts the returned project, domains, nodes, edges, and allocation schema version; see
[Panel Design](panel-design.md) (`frontend/src/stores/topologyStore.ts:760-799`). Controller staging
merges only overlay addresses and allocation fields into the full public topology through a versioned
compare-and-set; see [Controller stage and promote](controller-stage-promote.md)
(`internal/controller/compile_subgraph.go:233-288`). [Render and key custody](render-keys.md) then
fills rendered maps, and [Artifacts and signing](artifacts-signing.md) defines the canonical bundle
set that the facade packages (`internal/localcompile/compile.go:27-32`,
`internal/localcompile/compile.go:100-105`, `internal/localcompile/compile.go:110-177`).

## Decision points (if any)

- Overlay allocation preserves an existing in-domain address, clears an out-of-domain value, and
  otherwise chooses the first free host while skipping used and reserved addresses under a hard scan
  budget and periodic context cancellation checks (`internal/allocator/ip.go:40-115`,
  `internal/allocator/ip.go:144-228`).
- All enabled non-backup edges for one unordered node pair share a primary link identity; each backup
  adds its edge ID and therefore receives an independent allocation (`internal/linkid/linkid.go:23-65`,
  `internal/compiler/peers_build.go:54-79`, `internal/compiler/peers_build.go:171-213`).
- Allocation reserves excluded-edge resources and every valid existing pin before visiting unpinned
  links in sorted link-key order. Ports are per node, transit pairs per resolved CIDR, and link-local
  pairs global; a client contributes no per-link port on its own endpoint
  (`internal/compiler/peers_build.go:215-278`, `internal/compiler/peers_build.go:280-431`).
- Role inference only normalizes capabilities upward where reachability or role requires it, except a
  client explicitly loses forwarding, relay, and inbound acceptance. Peer derivation then selects
  explicit endpoint-port overrides, keepalive, forward-only dialing, auto-reverse peers, or the
  client-side shared `wg0` projection (`internal/compiler/roles.go:106-143`,
  `internal/compiler/peers_build.go:461-610`, `internal/compiler/peers_build.go:617-690`,
  `internal/compiler/peers_build.go:926-1040`).
- Historical collision healing is deliberately outside compilation: the normalizer clears invalid
  client-side ports and strips an entire later-in-link-key-order conflicting allocation. Controller
  Save invokes it at ingestion, while preview and stage invoke it before validation/allocation
  (`internal/normalize/pins.go:29-91`, `internal/normalize/pins.go:92-238`,
  `internal/api/handler_topology.go:48-65`, `internal/controller/compile_preview.go:41-68`,
  `internal/controller/compile_stage.go:92-116`).

## Invariants

- Valid pins are reused verbatim. Because all pins are reserved before sorted lowest-free gap-fill,
  adding or reordering unpinned links cannot move an existing allocation; excluded controller edges
  participate through the same reservation sets (`internal/compiler/peers_build.go:215-278`,
  `internal/compiler/peers_build.go:280-431`).
- Link identity and resource orientation are shared leaf contracts: validation, allocation, peer
  construction, and write-back must not invent alternate pair keys or swap rules
  (`internal/linkid/linkid.go:1-18`, `internal/compiler/orientation.go:3-15`,
  `internal/compiler/compiler.go:271-309`).
- The canonical facade calls `CompileAt` with an injected timestamp, and that timestamp affects only
  manifest metadata. The compatibility `Compile` wrapper is the sole compiler entry that injects
  `time.Now`; request context may cancel work but does not select different allocations
  (`internal/compiler/compiler.go:159-180`, `internal/compiler/compiler.go:325-332`,
  `internal/localcompile/compile.go:53-60`, `internal/localcompile/compile.go:92-98`).

## Gotchas (optional)

- `CompileResult`'s WireGuard, Babel, sysctl, installer, telemetry-policy, artifact-catalog, and deploy
  maps are empty until `render.AllWith`; do not add renderer or exporter ownership here
  (`internal/compiler/compiler.go:312-335`, `internal/localcompile/compile.go:100-104`).
- Direct compiler callers do not receive collision migration automatically. Invalid or colliding pins
  still fail validation unless their owning ingestion/stage boundary first applies
  `normalize.HealCollidingPins` (`internal/compiler/compiler.go:180-191`,
  `internal/normalize/pins.go:1-10`, `internal/controller/compile_stage.go:106-116`).
- `CompileManifest.Checksum` is a truncated, Go-format-dependent display hint, not the canonical bundle
  digest and never a signing anchor (`internal/compiler/compiler.go:338-355`).
