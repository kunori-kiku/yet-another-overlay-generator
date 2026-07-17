# Wire contract (frontend ↔ backend)

This spec is the authoritative field-by-field parity contract between the three representations of a
topology: the Go model (`internal/model/topology.go`), the TypeScript type
(`frontend/src/types/topology.ts`), and the frontend editor surfaces (Zustand store +
canvas/form components). It also fixes the round-trip rules every field MUST obey across a
compile cycle.

A "wire" here is any boundary a topology crosses: serialized to JSON for an API request, returned in
a `CompileResponse`, persisted to `localStorage`, or exported/imported as a project file. Drift
between the three representations is a defect class of its own (audit theme T7); this contract makes
the target state normative so the gaps are closeable and testable.

Related specs: [edge.md](../data-model/edge.md) (port ownership), [node.md](../data-model/node.md),
[domain.md](../data-model/domain.md), [route-policy.md](../data-model/route-policy.md),
[../compiler/validation.md](../compiler/validation.md) (field validation coverage),
[../compiler/allocation-stability.md](../compiler/allocation-stability.md) (key/pin persistence),
[http-api.md](http-api.md) (the compile contract).

## Core round-trip rule

> **R0 — Every topology field returned by a compile MUST round-trip through the store.**
> The compile response carries `topology` back to the frontend. The store MUST rehydrate every
> top-level topology field it sends so that compiler-stamped values (overlay IPs, compiled ports,
> and — once landed — sticky-pin fields) survive into the next request and into `localStorage`. A
> field the backend allocates but the store drops is silently lost on the next compile, breaking
> allocation stability.

> **Compliance:** the store sends only `{ project, domains, nodes, edges }` (`getTopology`,
> `topologyStore.ts:224-227`) and rehydrates exactly those four on compile
> (`topologyStore.ts:343-351`); `localStorage` persists the same four plus `language`
> (`partialize`, `topologyStore.ts:430-436`). `route_policies` is therefore neither sent nor
> rehydrated. Per the binding decision (Decisions log #2) `route_policies` is RESERVED, so not
> sending it is acceptable for now; the rule above governs all other fields. Closed by Plan 9
> (wire-contract cleanup).

## Field parity table

Legend for **Round-trip** column: ✅ = preserved end-to-end through a compile cycle; ⚠️ = sent but
dropped or mishandled on return/import; ✗ = not transported at all.

### Topology (root)

| Field | Go (`model`) | TS (`types`) | UI editor | Round-trip | Notes |
|---|---|---|---|---|---|
| `project` | `Project` | `Project` | project form | ✅ | rehydrated on compile |
| `domains` | `[]Domain` | `Domain[]` | domain panel | ✅ | rehydrated on compile |
| `nodes` | `[]Node` | `Node[]` | node form | ✅ | rehydrated on compile |
| `edges` | `[]Edge` | `Edge[]` | canvas + edge form | ✅ | rehydrated on compile |
| `route_policies` | `[]RoutePolicy` (`topology.go:18`) | `RoutePolicy[]` (`topology.ts:8`) | none | ✗ | RESERVED — see below |

> **Compliance:** `route_policies` is declared on both sides but never sent by the frontend
> (`getTopology`, `topologyStore.ts:224-227`) and consumed by no renderer; the compiler only passes
> it through (`compiler.go:94`). It MUST be treated as RESERVED (see the rule below). Closed by
> Plan 9.

### Domain

| Field | Go (`model`) | TS (`types`) | UI editor | Round-trip | Notes |
|---|---|---|---|---|---|
| `id` | `ID` | `id` | yes | ✅ | |
| `name` | `Name` | `name` | yes | ✅ | |
| `cidr` | `CIDR` | `cidr` | yes | ✅ | MUST be IPv4 — see [../compiler/ip-allocation.md](../compiler/ip-allocation.md) |
| `description` | `Description` | `description` | yes | ✅ | |
| `allocation_mode` | `AllocationMode` | `allocation_mode` | yes | ✅ | `auto`/`manual` |
| `routing_mode` | `RoutingMode` | `routing_mode` | yes | ✅ | empty normalizes to `babel`; `static`/`none` rejected (Decisions #3) |
| `reserved_ranges` | `ReservedRanges` | `reserved_ranges` | yes | ✅ | |
| `transit_cidr` | `TransitCIDR` (`topology.go:41`) | — | none | ✗ | missing from TS type and UI |

> **Compliance:** `Domain.transit_cidr` exists in the Go model (`topology.go:41`) but is absent from
> the TypeScript `Domain` interface (`topology.ts:18-26`) and has no editor (zero references in
> `frontend/src`). The feature is unreachable from the UI and dropped on any import/round-trip that
> goes through the typed store. The TS type and (per Decisions log) optionally the UI MUST gain this
> field; until then it is documented backend-only. Closed by Plan 9.

### Node

| Field | Go (`model`) | TS (`types`) | UI editor | Round-trip | Notes |
|---|---|---|---|---|---|
| `id` | `ID` | `id` | yes | ✅ | |
| `name` | `Name` | `name` | yes | ✅ | feeds WG interface name; strict charset + raw/sanitized-collision validated (D15) |
| `hostname` | `Hostname` | `hostname` | yes | ✅ | |
| `platform` | `Platform` | `platform` | yes | ✅ | |
| `role` | `Role` | `role` | node form | ⚠️ | form cannot create `client` (D69) |
| `domain_id` | `DomainID` | `domain_id` | yes | ✅ | |
| `overlay_ip` | `OverlayIP` | `overlay_ip` | read-back | ✅ | compiler-allocated, preserved across recompile |
| `listen_port` | `ListenPort` | `listen_port` | yes | ✅ | base port; per-peer ports derived by compiler |
| `mtu` | `MTU` | `mtu` | yes | ✅ | range-validated: 0 or [576, 65535] (D64) |
| `xdp_mode` | `XDPMode` | `xdp_mode` | RightPanel select | ✅ | mimic XDP mode for tcp links; empty→`skb`; enum `skb`/`native` (validated) |
| `router_id` | `RouterID` | — | none | ⚠️ | absent from TS interface; survives via untyped import passthrough |
| `capabilities` | `Capabilities` | `capabilities` | derived | ⚠️ | FE-stamped caps can contradict role inference (D69/D54) |
| `fixed_private_key` | `FixedPrivateKey` | `fixed_private_key` | yes | ✅ | live-key capture path (Decisions #7) |
| `wireguard_private_key` | `WireGuardPrivateKey` | `wireguard_private_key` | yes | see key-blanking | |
| `wireguard_public_key` | `WireGuardPublicKey` | `wireguard_public_key` | read-back | see key-blanking | non-empty ⇒ key-fixed (target) |
| `public_endpoints` | `PublicEndpoints` | `public_endpoints` | yes | ✅ | reachability hints, NOT per-edge dial overrides |
| `extra_prefixes` | `ExtraPrefixes` | `extra_prefixes` | none | ⚠️ | no editor (UX-6); each entry IPv4-CIDR validated (D67) |
| `ssh_alias` | `SSHAlias` | `ssh_alias` | yes | ✅ | strict charset validated (D44) |
| `ssh_host` | `SSHHost` | `ssh_host` | yes | ✅ | strict charset validated (D44) |
| `ssh_port` | `SSHPort` | `ssh_port` | yes | ✅ | range-validated 1–65535 (D65) |
| `ssh_user` | `SSHUser` | `ssh_user` | yes | ✅ | strict charset validated (D44) |
| `ssh_key_path` | `SSHKeyPath` | `ssh_key_path` | yes | ✅ | |
| `telemetry_probes` | `TelemetryProbes` | `telemetry_probes` | Fleet node detail | ✅ | optional discriminated ICMP/TCP/URL policy; display `name` is non-executable metadata |
| `telemetry_devices` | `TelemetryDevices` | `telemetry_devices` | Fleet node detail | ✅ | optional `{mode:"all-eligible-v1"}` successor-policy opt-in |

`TelemetryProbe` is a discriminated wire union. Shared fields are `id`, optional `name`, optional
`interval_seconds`, and optional `timeout_milliseconds`; ICMP has `host`, TCP has `host` plus `port`,
and URL has `url` plus optional `expected_status` (zero/omitted means 200). Fields belonging to the
other destination types are omitted. `internal/wiredrift` pins these `omitempty` contracts against
the TypeScript union so an import/save/compile round trip cannot silently drop successor policy.

> **Compliance:** `Node.router_id` exists in the Go model (`topology.go:68`) but has no frontend
> editor and is absent from the TS `Node` interface (`topology.ts`); imported topologies retain it
> through untyped store writes, so it round-trips (the "unreachable from frontend" claim is refuted
> — dossier Appendix A). Backend format validation shipped in Plan 9 (PR #11). Declaring
> `router_id?: string` in the TS interface is optional future hardening, not required.

### Edge

| Field | Go (`model`) | TS (`types`) | UI editor | Round-trip | Notes |
|---|---|---|---|---|---|
| `id` | `ID` | `id` | auto | ⚠️ | `edge-${Date.now()}` collides on fast draws (D17) |
| `from_node_id` | `FromNodeID` | `from_node_id` | canvas | ✅ | |
| `to_node_id` | `ToNodeID` | `to_node_id` | canvas | ✅ | |
| `type` | `Type` | `type` | canvas | ✅ | |
| `endpoint_host` | `EndpointHost` | `endpoint_host` | canvas + form | ✅ | reachability hint; MAY be auto-stamped from `public_endpoints[0].host` |
| `endpoint_port` | `EndpointPort` | `endpoint_port` | form (NAT override) | ⚠️ | frontend MUST NOT auto-stamp — backend is sole port authority |
| `compiled_port` | `CompiledPort` | `compiled_port` | read-back | ✅ | read-only; MUST reflect the actually-dialed port |
| `priority` | `Priority` | `priority` | none | ⚠️ | no editor (D68); not read by Babel yet (D63) |
| `weight` | `Weight` | `weight` | none | ⚠️ | no editor (D68); not read by Babel yet (D63) |
| `role` | `Role` | `role?` | RightPanel select + Add-backup button | yes | `primary`/`backup`/empty; drives parallel-link identity + babel cost preset (edge.md §Parallel links) |
| `transport` | `Transport` | `transport` | RightPanel select | ✅ | `udp` plain WG; `tcp` = mimic-wrapped (no new wire field — mimic is keyless); FE labels it "TCP (mimic)" |
| `is_enabled` | `IsEnabled` | `is_enabled` | form | ✅ | |
| `notes` | `Notes` | `notes` | none | ⚠️ | no editor (D68) |

> **Compliance:** the canvas `onConnect` stamps BOTH `endpoint_host` and `endpoint_port` from the
> target's `public_endpoints[0]` (`TopologyCanvas.tsx:243-251`). Stamping `endpoint_port` turns a
> node reachability hint into a per-edge NAT dial override the backend then honors, suppressing its
> own per-peer port allocation (the headline bug, audit theme T1). Per the binding decision the
> frontend MUST NOT auto-stamp `endpoint_port`; it MAY stamp `endpoint_host` only. The backend is
> the sole port authority. Full ownership contract: [edge.md](../data-model/edge.md). Closed by
> Plan 2.

### RoutePolicy — RESERVED

| Field | Go (`model`) | TS (`types`) | UI editor | Round-trip | Notes |
|---|---|---|---|---|---|
| (all) | `RoutePolicy` | `RoutePolicy` | none | ✗ | RESERVED; rejected if non-empty |

## `route_policies` is RESERVED

`route_policies` is declared on both the Go and TS sides but is wired into no renderer and has no
editor. Per the binding decision (Decisions log #2) it is **RESERVED for a future subject**, not a
shipping feature.

- A topology with a non-empty `route_policies` array MUST be rejected by semantic validation with a
  clear "reserved / not yet implemented" error. (The validation rule and message are specified in
  [../compiler/validation.md](../compiler/validation.md).)
- The frontend MUST NOT expose an editor for `route_policies` and MUST NOT send a non-empty array.
- The TS type MUST be annotated as reserved so consumers do not build against it.
- LAN bridging / route injection (the use-class `route_policies` was meant to serve) is delivered
  through `extra_prefixes` and the routing layer, not through `route_policies`, until the feature is
  designed.

> **Compliance:** semantic validation rejects a non-empty `route_policies`
> (`validateRoutePoliciesReserved` → `CodeRoutePolicyReserved`, `semantic.go:95-97`, D62); the compiler
> only passes it through (`compiler.go:94`), so the reservation is enforced at the validation pass.

## Key-blanking round-trip semantics

The compile response deliberately blanks WireGuard private and public keys for **non-fixed** nodes.
This is by design, not a bug — the keys are an out-of-band secret that MUST NOT be persisted into the
browser's `localStorage` or re-sent in plaintext on every request.

- For a node whose key is NOT fixed, a compile MUST return that node with
  `wireguard_private_key` and `wireguard_public_key` blanked in the response `topology`. The real
  key pair lives only in the ephemeral render map used to produce that response's configs.
- For a node whose key IS fixed, the returned node carries its public key (and private key) so the
  frontend can display/preserve it.

> **Compliance:** `generateKeys` blanks both key fields for non-fixed nodes
> (`handler.go:307-314`) and stamps them for `fixed_private_key == true` nodes
> (`handler.go:272-299`). This matches the contract.

### Pin-reuse rule (target, cross-links allocation stability)

Under the sticky-pin allocation design, key identity becomes part of the persisted contract:

- A node with a **non-empty `wireguard_public_key` MUST be treated as key-fixed** and its key reused
  on recompile; only new nodes (or an explicit operator-initiated rotation) get a fresh key. This
  generalizes the existing `fixed_private_key` flag so additive growth does not rotate the whole
  fleet's keys.
- Because non-fixed keys are blanked in the compile response (above), a node that should keep its key
  across recompiles MUST carry it via the `fixed_private_key` paste procedure (Decisions log #7) or
  accept one final rotation during migration.

> **Compliance:** today only `FixedPrivateKey == true` is treated as fixed; a node with a populated
> `wireguard_public_key` but `FixedPrivateKey == false` still falls into the rotate-every-compile
> branch (`handler.go:302-314`), so its key rotates on every compile. The non-empty-public-key ⇒
> fixed rule is the target. Full mechanism, migration, and invariants I1–I10 in
> [../compiler/allocation-stability.md](../compiler/allocation-stability.md). Closed by Plan 7.

## Stale-documentation note

The repository previously claimed "TypeScript types mirror the Go model exactly." That is false
while `transit_cidr` and `router_id` are missing from the TS types and `route_policies` is wired on
neither side (audit theme T14). The parity table above is the authoritative statement of what
mirrors what; descriptive prose elsewhere MUST defer to it.
