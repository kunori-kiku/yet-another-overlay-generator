# Model and validation

<!-- last-verified: 2026-07-17 -->

## Responsibility

Own the canonical topology JSON model, structural normalization and validation, cross-object
semantic validation, and the stable finding contract returned to validation callers
(`internal/model/topology.go:13-35`, `internal/validator/schema.go:92-186`,
`internal/validator/semantic.go:10-88`, `internal/validator/code.go:264-318`).

## Files

- `internal/model/topology.go:13-200` — defines topology, project, domain, node, active-telemetry,
  deployment-mode, public-endpoint, and capability JSON fields and defaults.
- `internal/model/topology.go:202-326` — defines edge role/direction defaults, executable edge input,
  sticky allocation pins, and the reserved route-policy shape.
- `internal/validator/schema.go:92-186` — owns root resource/version guards and the structural pass
  orchestration.
- `internal/validator/schema.go:198-304`, `internal/validator/schema.go:306-478`, and
  `internal/validator/schema.go:480-575` — validate and normalize domains, nodes, and edges.
- `internal/validator/semantic.go:10-88` — indexes the model and orders the cross-reference,
  collision, reachability, client, link, port, pin, and reserved-feature rule groups.
- `internal/validator/semantic_naming.go:10-89`, `internal/validator/semantic_edges.go:14-609`, and
  `internal/validator/nat.go:9-109` — own identity, role/link, and reachability semantics.
- `internal/validator/semantic_pins.go:13-239` and `internal/validator/semantic_ports.go:12-125` —
  validate sticky-pin identity/completeness and effective per-node port bounds.
- `internal/validator/code.go:264-318` — defines parameterized findings, separate error/warning
  collections, and fail-closed code registration.
- `internal/wiredrift/drift_test.go:294-346` — directly gates frontend `omitempty` bookkeeping and
  persisted allocation-pin names against the Go model tags.

## Inputs

The input is one `model.Topology`: required project, domain, node, and edge collections plus optional
route policies and allocation-schema version. Nodes declare one of five roles, four explicit
capability flags, and an orthogonal deployment mode whose empty value means managed
(`internal/model/topology.go:13-35`, `internal/model/topology.go:61-106`,
`internal/model/topology.go:169-200`, `internal/model/topology.go:227-326`,
`internal/validator/schema.go:331-344`).

The browser-authored mirror and design lifecycle are described in [Panel design](panel-design.md);
its topology interfaces mirror these snake-case fields, while the Go tags remain the wire authority
(`frontend/src/types/topology.ts:1-125`, `internal/wiredrift/drift_test.go:294-346`). Active telemetry
is present in the node DTO but its typed executable-policy and rollout contract belongs to
[Telemetry policy](telemetry-policy.md); schema delegates that validation and rejects policy on
manual nodes (`internal/model/topology.go:128-167`, `internal/validator/schema.go:346-364`).

## Outputs

Each pass returns `ValidationResult`, containing coded, field-addressed, parameterized errors and
warnings with an English fallback message; only a non-empty error set makes the pass invalid
(`internal/validator/code.go:264-318`). The schema pass also writes the explicit `babel` routing and
`udp` transport defaults back into the supplied topology
(`internal/validator/schema.go:243-262`, `internal/validator/schema.go:480-515`).

The semantic pass adds relationship and topology findings without allocating resources or deriving
runtime configuration (`internal/validator/semantic.go:24-88`). Allocation, capability inference,
peer derivation, and compiled write-back belong to
[Compiler and allocation](compiler-allocation.md) (`internal/compiler/compiler.go:181-229`).

## Decision points (if any)

- A topology exceeding node, edge, domain, or per-domain reserved-range bounds, or carrying a newer
  allocation schema, is rejected at the root before entity loops or quadratic semantic work
  (`internal/validator/schema.go:97-171`).
- Schema decides field presence, enum, format, range, and safe defaulting; semantic validation then
  decides references, uniqueness, reachability, client/link compatibility, and pin consistency
  (`internal/validator/schema.go:174-186`, `internal/validator/semantic.go:24-88`).
- Role is restricted to `peer`, `router`, `relay`, `gateway`, or `client`. Reachability validation
  uses declared capability flags plus the relay-role fact because downstream capability inference
  has not run yet (`internal/validator/schema.go:331-344`, `internal/validator/nat.go:9-20`).

## Invariants

- Findings originate from registered codes plus keyword parameters; code registration is
  fail-closed, and warnings never make `ValidationResult.IsValid` false
  (`internal/validator/code.go:264-318`).
- `model.Topology` JSON tags are the Go authority. A new or changed `omitempty` field or sticky pin
  must update the frontend canonicalization lists in the same change or the wiredrift gate fails
  (`internal/wiredrift/drift_test.go:294-346`,
  `frontend/src/stores/controller/helpers.ts:82-113`).
- Semantic rules inspect the model as validated; they do not infer compiler capabilities or allocate
  replacements for invalid pins (`internal/validator/nat.go:9-20`,
  `internal/validator/semantic.go:82-86`, `internal/validator/semantic_pins.go:52-120`).

## Gotchas (optional)

- `ValidateSchema` is intentionally mutating: empty routing mode and transport become explicit
  defaults, so a caller requiring input purity must validate a copy
  (`internal/validator/schema.go:204-208`, `internal/validator/schema.go:243-262`,
  `internal/validator/schema.go:480-515`).
- `ValidateSemantic` silently short-circuits the root guard because `ValidateSchema` is the canonical
  reporter; a caller that runs only the semantic pass will not receive oversize or future-version
  findings (`internal/validator/semantic.go:15-22`, `internal/validator/schema.go:141-171`).
- `route_policies` remains a reserved model field, but every non-empty value is currently a semantic
  error because no renderer implements it (`internal/model/topology.go:27-28`,
  `internal/validator/semantic_edges.go:14-26`).
