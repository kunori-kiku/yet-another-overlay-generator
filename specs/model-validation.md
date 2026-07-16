# Model and validation

<!-- last-verified: 2026-07-15 -->

## Responsibility

Define the canonical topology JSON contract and reject undeployable or internally inconsistent
designs before allocation, rendering, export, or deployment. Validation is split into structural
schema checks and cross-object semantic checks; findings are coded data, not HTTP errors.

## Files

- `internal/model/topology.go:3-282` defines `Topology`, project/domain/node/edge fields, deployment
  modes, direction/parallel-link policy, allocation pins, and the supported allocation schema
  version.
- `internal/validator/code.go:20-335` defines stable validation codes, English templates,
  parameterized findings, and `ValidationResult`.
- `internal/validator/schema.go:13-554` owns safe WireGuard-key validation, topology resource bounds,
  required fields, formats, ranges, enums, and normalization write-backs.
- `internal/validator/semantic.go:10-89` orchestrates the semantic pass.
- `internal/validator/semantic_{edges,naming,pins,ports}.go` contain the focused cross-object rule
  groups; `internal/validator/nat.go` owns NAT reachability.

## Canonical model

`Topology` contains project metadata, domains, nodes, edges, reserved route policies, and
`alloc_schema_version`. Nodes carry role, deployment mode, domain, optional compiler allocations,
platform/render inputs, public endpoints, optional local WireGuard key fields, and SSH deployment
metadata. Edges carry endpoints, transport and direction policy, primary/backup role, the compiled
port echo, and paired sticky allocation pins (`internal/model/topology.go:13-282`).

An empty node deployment mode means managed; `manual` means agent-less and operator-deployed. This
is orthogonal to the five network roles (`peer`, `router`, `relay`, `gateway`, `client`). Empty link
role is in the primary class; every backup edge is an independent link. Empty link direction means
both directions; `forward` permits only the drawn from-to side to initiate.

`model.CurrentAllocSchemaVersion` is currently `1`. The compiler stamps that version onto compiled
topologies, and validation fails closed if input was written with a newer allocation-pin format
(`internal/model/topology.go:3-11,30-35`). This prevents an older binary from interpreting future
pins as current semantics.

## Finding contract

A finding contains `field`, stable `code`, optional string `params`, rendered English `message`,
and `level`. `ValidationResult` keeps separate error and warning slices, and `IsValid` depends only
on errors (`internal/validator/code.go:255-309`). Codes are registered in the validator package and
map to frontend `error.<code>` catalog keys.

This channel is intentionally separate from `internal/apierr`: validation findings normally ride
a successful validation response and can be localized as a set, while an API error describes an
HTTP operation failure. New rules must add a code/template and emit parameters rather than
constructing user-facing prose at each call site.

## Pass 1: schema and normalization

`ValidateSchema` first applies root bounds before any per-entity or quadratic semantic work:

- at most 2,000 nodes;
- at most 10,000 edges;
- at most 1,000 domains;
- at most 1,000 reserved ranges per domain;
- no allocation schema version newer than this build.

Both validation passes short-circuit on this predicate; schema is the sole reporter so callers do
not receive duplicate root findings (`internal/validator/schema.go:92-186` and
`internal/validator/semantic.go:15-22`).

The schema pass then checks, among other constraints:

- required project/domain/node/edge identifiers and references-to-be-validated fields;
- IPv4 CIDR syntax and allocation-safe sizes, including transit pools and reserved ranges;
- role, deployment-mode, edge-type, transport, fallback, direction, and primary/backup enums;
- safe node IDs/names, SSH values, endpoint hosts, key paths, and mimic interface names before
  those strings reach config paths or root-run scripts;
- active telemetry as a bounded typed policy: unique stable IDs, optional printable single-line
  display names, one required `host` accepting either an IP literal or ASCII DNS hostname, TCP-only
  ports, and bounded interval/timeout values. There is no separate DNS field;
- WireGuard public keys using one strict 43-base64-characters-plus-padding pattern that rejects
  whitespace/newline injection (`internal/validator/schema.go:13-27,375-381`);
- MTU, SSH/endpoint ports, router IDs, XDP mode, extra prefixes, and self-loops.

The strict version-1 executable policy is serialized only by `probepolicy.Marshal` through private
wire DTOs that exclude controller-only display metadata. The exported `probepolicy.Policy` is a
runtime parse view: its `MarshalJSON` deliberately rejects generic `json.Marshal`, while `Parse`
continues to reject unknown fields. This prevents `name` or later topology metadata from becoming an
accidental alternate `telemetry.json` wire (`internal/probepolicy/policy.go`).

It normalizes empty `routing_mode` to `babel` and empty edge `transport` to `udp` by mutating the
indexed object so the default round-trips (`internal/validator/schema.go:243-262,458-493`). `babel`
is currently the only implemented routing mode; `static` and `none` are reserved and fail rather
than compiling a route-less overlay.

## Pass 2: semantic rules

`ValidateSemantic` builds domain/node indexes and runs cross-object checks in an explicit order
(`internal/validator/semantic.go:24-88`). The rule groups include:

- domain/node/edge references, ID uniqueness, overlay-address membership and collisions;
- normalized node-name and generated-interface-name collisions;
- isolated-node and NAT/reachability warnings/errors;
- client restrictions: no inbound edges, exactly one enabled outbound edge, a reachable
  router/relay/gateway target, endpoint requirements, no backup/direction policy, and no Babel-only
  fields;
- mimic/TCP deployability, relay-path constraints, endpoint consistency, and direction viability;
- primary folding, independent backups, redundant enabled edges, one explicit primary per pair,
  per-node interface-name uniqueness, and cost/failover warnings;
- rejection of non-empty `route_policies`, because no renderer implements them;
- sticky allocation-pin integrity.

For effective ports, the compiler uses a uniform base of `51820` and one port per deduplicated
non-client link. Validation rejects only a range whose `base + interfaceCount - 1` exceeds `65535`
(`internal/validator/semantic_ports.go:12-124`). It does **not** reject overlap between nodes said
to be co-located: with one global base every non-empty node range overlaps by construction, so such
a rule would reject all multi-node-host designs.

## Allocation pins

Pins are validated before the allocator reserves them. For each enabled link:

- transit and link-local values must be both present or both absent;
- listen-port values must be paired on ordinary links; on a client link only the non-client
  endpoint may carry the one valid sticky port;
- operator-chosen ports must be in `[1024,65535]`; auto-allocation still begins at `51820`;
- transit addresses must parse and fall inside the edge's resolved transit pool;
- a node port, transit IP, or link-local address cannot belong to two distinct links;
- forward/reverse primary-class edges may share their mirrored resources because they compile into
  one link, while each backup edge has a distinct link identity.

On client edges, only a port attached to the client endpoint is an error. The non-client endpoint's
port and complete transit/link-local pairs are validated, deduplicated, reserved, rendered, and
persisted normally. The deprecated `validation_pin_client_allocation_ignored` code remains
registered for historical catalog compatibility but is no longer emitted. The shared limits and
fallback pool live in `internal/allocconst`; validator and allocator must not copy alternate literals.

## Caller behavior

The compiler runs schema first and aborts before semantic validation if schema errors exist. It
then aborts on semantic errors and carries warnings from both passes into `CompileResult`
(`internal/compiler/compiler.go:138-155`).

The browser/WASM validation operation runs and merges both passes so the editor can present all
applicable findings in one response (`cmd/wasm/main.go:146-163`). Local CLI, controller subgraph,
and browser compile still converge on the same compiler validation path through
`internal/localcompile`.

## Custody-specific validation boundaries

The shared model supports both local/air-gap and controller workflows, so not every custody rule
belongs in generic schema validation:

- A topology `wireguard_private_key` is legal for local `AirGap` compilation, where keys round-trip.
  The controller's `update-topology` handler separately refuses any private key before storage and
  canonicalizes the checked model (`internal/api/handler_topology.go:23-65`).
- A WireGuard public key is optional at generic schema time. A new local node can have both key
  fields empty and generate a pair during compile; a managed controller node receives its key from
  enrollment. When a public key is present, it must be valid.
- A controller manual node must already have a valid, unique public key because it never enrolls.
  `internal/controller/compile_manualnode.go:19-79` enforces that admission rule for stage and
  preview, including uniqueness against approved managed nodes.
- Enrollment and rekey use the same `validator.ValidWGPublicKey` function, then enforce fleet
  identity uniqueness in the controller layer.

## Invariants and gotchas

- `fixed_private_key` is a panel affordance, not a Go key-selection switch. Go key handling follows
  the actual private/public field state: private present means reuse/derive, public-only is an
  air-gap error, both empty generates a pair. Do not add validation that assumes the boolean is
  authoritative without changing the key contract.
- Capability inference occurs later in compilation. NAT semantics that need role behavior operate
  on explicit role/model facts rather than inferred compiler capabilities.
- Warnings are non-fatal and must survive compilation; errors stop allocation and rendering.
- New optional persisted fields should preserve backward compatibility with `omitempty` and
  explicit empty/default semantics.
- Any rule mirrored for immediate frontend UX must remain subordinate to the Go validator and be
  protected by conformance/wire tests; the TypeScript side is not a second authority.
