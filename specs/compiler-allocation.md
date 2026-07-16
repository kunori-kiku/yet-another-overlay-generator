# Compiler & Allocation

<!-- last-verified: 2026-06-15 -->
<!-- 2026-06-15 (extensible-i18n closeout): error responses now coded via the internal/apierr envelope {error:{code,message,params}} — English-default message + panel-localized by error.<code>; no endpoint/flow change. -->

## Responsibility
Deterministically transform a validated topology plus a key map into a compiled topology (sticky resource allocations written back as edge pins) and per-node WireGuard peer derivations, guaranteeing byte-identical re-allocation for every pre-existing entity on recompile.

## Files
- `internal/compiler/compiler.go:1-215` — `Compile` pipeline orchestrator: validate → allocate overlay IPs → infer capabilities → derive peers → write six `pinned_*` fields + `CompiledPort` back onto edges; stamps `AllocSchemaVersion = 1` (compiler.go:18,114).
- `internal/compiler/peers.go:1-1081` — peer derivation: edge→link collapse, reserve-then-gap-fill allocation of ports/transit-IP pairs/link-local pairs, `PeerInfo` construction (forward + auto-reverse), `DeriveClientConfigs`, mimic MTU math, Babel link-cost resolution, `GenerateRouterID`.
- `internal/allocator/ip.go:1-188` — overlay IP allocation per domain CIDR: keeps existing in-range `OverlayIP` verbatim, clears out-of-CIDR values, gap-fills lowest free host honoring `ReservedRanges` (ip.go:21-82,85-171).
- `internal/linkid/linkid.go:1-66` — dependency-free link-identity authority shared with the validator: `PinKey` (sorted `a|b`, direction-agnostic, linkid.go:28-33), `LinkKey` (primary class = PinKey; backup = PinKey + `#edgeID`, linkid.go:53-59), `IsBackup` (linkid.go:64-66).

## Inputs
- `*model.Topology` — draft topology with optional sticky state riding on it: node `OverlayIP`, edge `pinned_from/to_port|transit_ip|link_local` (internal/model/topology.go:178-187). Validation runs inside `Compile` itself (compiler.go:80-89); rules live in the validator — see specs/model-validation.md.
- `keys map[string]KeyPair` (`{PrivateKey, PublicKey}` strings, peers.go:64-67) — produced by `render.GenerateKeys` (internal/render/render.go:70); see specs/render-keys.md.
- Callers: the controller stage compile on a per-node subgraph (internal/controller/compile.go — see specs/controller-stage-promote.md), the CLI `cmd/compiler`, and the in-browser WASM engine (the anonymous air-gap HTTP handlers were removed).

Entry signature: `func (c *Compiler) Compile(topo *model.Topology, keys map[string]KeyPair) (*CompileResult, error)` (compiler.go:78).

## Outputs
- `*CompileResult` (compiler.go:21-53):
  - `Topology` — copy with allocated overlay IPs, inferred capabilities (compiler.go:118-120, via `InferCapabilitiesFromRole`, internal/compiler/roles.go:107-133), and pins/`CompiledPort` written back per edge (compiler.go:139-186). This pinned topology round-trips through panel persistence and controller storage — see specs/panel-design.md, specs/controller-store.md.
  - `PeerMap map[string][]PeerInfo` — per-node interface specs (port, transit pair, link-local pair, endpoint, keepalive, interface name, mimic/MTU, link cost; peers.go:69-133).
  - `ClientConfigs map[string]*ClientPeerInfo` — client wg0 material incl. AllowedIPs = all domain CIDRs ∪ resolved transit CIDRs (peers.go:938-966,1029-1053).
  - `Warnings []validator.ValidationError` — non-fatal schema+semantic warnings surfaced to callers (compiler.go:91-95).
  - `Manifest` with a non-canonical checksum: sha256 of `fmt.Sprintf("%v", topo)` truncated to 16 hex chars (compiler.go:211-215).
- Consumed by `render.All(result, keys)` (internal/render/render.go:147) which fills the config maps — see specs/render-keys.md.

Deep docs: docs/spec/compiler/pipeline.md, allocation-stability.md, ip-allocation.md, peer-derivation.md.

## Decision points
- **Edge → link collapse (unify rule):** all enabled non-backup edges of a node pair collapse into one bidirectional link keyed by `PinKey`; each `role=="backup"` edge is its own link keyed `PinKey#edgeID` (peers.go:210-267, linkid.go:53-59). First enabled primary-class edge in array order is `primaryEdge` and fixes from/to orientation (peers.go:214-219).
- **Reserve-then-gap-fill (Spec B):** Pass 1 reserves every complete transit/link-local pair and every complete ordinary-link port pair before gap filling. A client link is the one-sided port exception: its client endpoint stays zero while its non-client endpoint's valid sticky port is reserved and reused. Any other partial resource is rejected by validation and treated as unpinned by allocation. Links then gap-fill in linkKey order, taking the lowest free slot per pool (`internal/compiler/peers_build.go`).
- **Per-resource pools:** ports are allocated independently per node, with each node scanning upward from the fixed fleet-wide `allocconst.WGListenPortBase` (51820) and failing past 65535 (peers.go:829-842); transit IPv4 pairs are per-CIDR (`domain.transit_cidr`, empty → `10.10.0.0/24`, peers.go:19,252-254), pair N = (network+2N+1, +2N+2), network/broadcast never allocated (peers.go:702-747); link-local pairs are global, `fe80::%x` hex-numbered (peers.go:864-873).
- **Endpoint port:** `edge.EndpointPort > 0` is an explicit operator NAT override used verbatim; otherwise the remote interface's allocated listen port (peers.go:516-532; mirrored into `CompiledPort`, compiler.go:172-185).
- **Auto-reverse peer:** every non-client link also emits the reverse `PeerInfo`; its endpoint resolves via the explicit reverse primary-class edge if present, else falls back to `PublicEndpoints[0].Host` + the allocated port — never `PublicEndpoints[0].Port` (peers.go:605-688, esp. 629-646).
- **Keepalive 25** when the local node cannot accept inbound or no reverse primary-class edge exists (peers.go:534-539,615-618); backup edges never count as a reverse direction (peers.go:177-199).
- **Client edges:** the client side gets no per-peer interface or port, but the non-client side owns a real per-link interface/listen port and the link retains complete transit/link-local pairs. Compiler write-back forces only the client endpoint port to zero, preserves those sticky allocations, and keeps `CompiledPort` equal to the effective dial port (explicit `endpoint_port` when present, otherwise the non-client listen port). The router receives the `IsClientPeer` `PeerInfo`; `DeriveClientConfigs` builds the client's shared `wg0` view.
- **Babel link cost:** `edge.Priority` > `edge.Weight` > backup preset 384 > 0 (= role default) (peers.go:849-862).
- **Mimic (transport=="tcp"):** sole signal for TCP shaping; MTU becomes `(node.MTU or 1420) − 12`, non-mimic MTU passes through untouched (peers.go:28-61).

## Invariants
- **Allocation stability / superset rule** (PRINCIPLES.md "Allocation stability"): recompiling a superset reproduces identical values for existing entities — pins are reused verbatim, gap-fill order depends only on linkKey, never array position, so delete/re-add of the same pair is idempotent (peers.go:201-207,341-346; docs/spec/compiler/allocation-stability.md I1-I10).
- **Backend is the sole port authority** (PRINCIPLES.md): the compiler allocates all listen ports; nonzero `endpoint_port` is only honored as an operator override and `CompiledPort` must equal the rendered endpoint's port (compiler.go:136-186).
- **Stateless compiler** (PRINCIPLES.md): all allocation state rides the topology JSON (pins + `OverlayIP` + `AllocSchemaVersion`); `Compile` mutates copies, never the input (compiler.go:103-115, ip.go:28-30).

## Gotchas
- The render-output maps in `CompileResult` (`WireGuardConfigs`, `BabelConfigs`, `SysctlConfigs`, `InstallScripts`, `DeployScripts`) are initialized **empty** by `Compile` (compiler.go:191-195) and filled afterwards by `render.All` — see specs/render-keys.md.
- The `allocations` map returned by `DerivePeers` is double-keyed: canonical `linkid.LinkKey` entries plus directed `"from->to"`/`"to->from"` aliases for primary links only (backup links get no alias); the key character sets (`|`/`#` vs `->`) are disjoint by construction (peers.go:408-419).
- Overlay IP stickiness is value-based, not pin-field-based: any `OverlayIP` inside its domain CIDR survives verbatim, but shrinking/moving the domain CIDR silently force-clears and reallocates out-of-range addresses (ip.go:32-50). The allocator is IPv4-only and rejects host spaces ≥ 32 bits as a defensive guard (ip.go:111-122,173-182).
