# Model & Validation

<!-- last-verified: 2026-06-15 -->
<!-- 2026-06-15 (extensible-i18n closeout): validation findings now carry Code+Params on the 200 ValidateResponse, panel-localized via tValidationError; English-default; no validation-logic change. -->

## Responsibility
Define the canonical topology JSON data model and reject invalid topologies in two passes (schema, then semantic) before any allocation, rendering, or deployment consumes them.

## Files
- `internal/model/topology.go:1-203` — struct definitions: `Topology`, `Project`, `Domain`, `Node`, `Edge` (+ allocation pins), `RoutePolicy`, edge-role constants.
- `internal/validator/schema.go:1-365` — Pass 1: per-object field/format/enum checks, charset guards, and normalization writebacks. Exports `ValidationError`/`ValidationResult` (schema.go:37-63).
- `internal/validator/semantic.go:1-1161` — Pass 2: cross-object checks — references, uniqueness, port ranges, client rules, parallel-link rules, allocation-pin rules, mimic platform rules.

## Inputs
- `*model.Topology` deserialized from operator-authored topology JSON. Producers: the panel editor (see specs/panel-design.md), the air-gap HTTP API (`internal/api/handler.go:95-108`, see specs/airgap-api.md), and the controller compile path (see specs/controller-stage-promote.md).
- Entry points: `ValidateSchema(topo *model.Topology) *ValidationResult` (internal/validator/schema.go:67) and `ValidateSemantic(topo *model.Topology) *ValidationResult` (internal/validator/semantic.go:15).
- Shared link-identity semantics from `internal/linkid` (`LinkKey`/`PinKey`/`IsBackup`, semantic.go:596-601) and name normalization from `internal/naming` (semantic.go:240,252).

## Outputs
- `*ValidationResult` with `Errors`/`Warnings`; `IsValid()` (schema.go:61-63) gates compilation: the compiler runs both validators as Pass 1/2 and aborts on errors (`internal/compiler/compiler.go:80-89`, see specs/compiler-allocation.md); `/api/validate` returns the merged result without aborting (internal/api/handler.go:106-118, see specs/airgap-api.md).
- Normalization writebacks that mutate the input topology: empty `routing_mode` → `"babel"` (schema.go:145-147), empty `transport` → `"udp"` (schema.go:333-335) — iteration is by index/pointer so the writeback persists and round-trips (schema.go:100-103,306-308).
- `model.Topology` itself is the lingua-franca type consumed downstream by compiler-allocation, render-keys, controller-store, and the agent (see those specs).

## Decision points (if any)
- **Routing mode**: only `"babel"` passes; `"static"`/`"none"` are reserved and rejected so a dead overlay never compiles (schema.go:150-159; docs/spec/compiler/routing-modes.md).
- **route_policies are reserved**: any non-empty array is a hard error — no renderer consumes them (semantic.go:88-93).
- **Domain CIDR**: must parse, be IPv4, and be no larger than /8 (schema.go:114-132); same IPv4 guard on `reserved_ranges` (schema.go:162-182) and node `extra_prefixes` (schema.go:276-286).
- **Shell-injection charsets**: node names restricted to `[A-Za-z0-9 ._-]` (schema.go:16,195-200); `ssh_host`/`ssh_alias`/`ssh_user` restricted to `[A-Za-z0-9._:@-]` (schema.go:21,290-301).
- **Client rules**: clients accept no inbound edges, dial only router/relay/gateway, need `endpoint_host`, and must have exactly one enabled outbound edge (semantic.go:454-521); backup edges may not touch clients (semantic.go:1056-1072).
- **Parallel links**: empty/`"primary"` role collapses to one link per pair, each `"backup"` edge is its own link (topology.go:123-128); at most one explicit primary per pair (semantic.go:1029-1051); per-node WireGuard interface-name uniqueness, invariant N4 (semantic.go:942-1013); warnings for equal-cost parallel links and all-backup pairs (semantic.go:1091-1160); duplicate same-direction primary-class edges warn (semantic.go:865-884).
- **Allocation pins** (invariant I7): per edge, each pinned resource must be pair-complete or absent, ports within `[node base, 65535]`, transit IPs inside the edge's resolved transit pool, no cross-link duplicates, and no per-peer pins on client edges (semantic.go:628-696 plus helpers 700-822).
- **mimic (`transport=="tcp"`)**: both endpoints must be deployable Linux (debian/ubuntu or empty platform) (semantic.go:553-575); `xdp_mode` only `skb`/`native` (schema.go:226-232).
- **Effective port ranges**: per-node `[base, base+interfaces-1]` may not exceed 65535 nor overlap with co-hosted nodes' ranges (semantic.go:330-428).

## Invariants
- Pins are validated before any resource is reserved, and pin/cost/pool constants mirror the compiler exactly — `defaultListenPort` 51820 (semantic.go:291), `defaultTransitCIDR` 10.10.0.0/24 (semantic.go:580), `backupDefaultLinkCost` 384 (semantic.go:890) must stay in lock-step with `internal/compiler/peers.go`; this underwrites PRINCIPLES.md "Allocation stability (superset rule)" (docs/spec/compiler/allocation-stability.md).
- Charset and range checks are the compile-time half of PRINCIPLES.md "Generated configs must be deployable" and "Generated scripts run as root" — names/SSH fields are interpolated into root-run scripts, MTU/ports/router-id outside accepted ranges would render undeployable artifacts (schema.go:12-34,248-271).
- New model fields are `omitempty` so persisted topologies from prior releases keep loading (PRINCIPLES.md "Backward compatibility of persisted topologies"; e.g. topology.go:24,86-90,178-187).

## Gotchas (optional)
- `wireguard_private_key` (topology.go:89) round-trips through topology JSON **by design** on the air-gap path (docs/spec/data-model/node.md); neither validator rejects nor strips it. docs/spec/controller/persistence.md's "public-keys-only" claim is NOT enforced in the model or validators — only the controller's `enrolledSubgraph` projection clears it (internal/controller/compile.go:443-497, see specs/controller-stage-promote.md).
- `fixed_private_key` (topology.go:86) is consumed by no Go code outside the struct declaration: key-handling branches on the *state* of the two key fields, not the flag (see specs/render-keys.md; docs/spec/data-model/node.md §key cases). The flag is only the panel's paste affordance.
- `ValidateSemantic` also calls `validateNATReachability`, defined in `internal/validator/nat.go:36` — same package and pipeline, but outside this component's three named files.
- Capability inference has NOT run at semantic-validation time, so NAT/reachability logic must special-case the relay role (internal/validator/nat.go:9-18); `alloc_schema_version` (topology.go:24) is stamped by the compiler (internal/compiler/compiler.go:113-115), not checked here.
