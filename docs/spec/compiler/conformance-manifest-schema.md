# Conformance Manifest Schema

> The canonical content document the Go↔TS conformance harness freezes per fixture
> (`internal/conformance/`, program plan-5 / milestone 1.5). This doc is the schema of record; the
> code of record is `internal/conformance/manifest.go` (the struct + canonical serializer) and
> `internal/conformance/oracle.go` (`BuildManifest`, the Go oracle that produces it).

## Go is the spec

The Go compiler pipeline is the **authoritative oracle**. The harness runs a fixed-key fixture
corpus through the production compile path and freezes the resulting manifest as committed goldens
(`internal/conformance/testdata/golden/`). The TypeScript reimplementation of the local-compile
pipeline (plan-4) does not get its own spec — it must produce a **byte-identical** manifest for the
same fixture, asserted by `FirstDivergence` (the conformance-mode comparator, which reports the byte
offset + line/column + a context window of the first mismatch). When Go and TS disagree, **Go is
right by definition** and the TS port is the bug. A change to the Go pipeline that legitimately
moves bytes is re-frozen with `go test ./internal/conformance/ -update` (reviewed in the diff), and
the TS port is then obligated to match the new bytes.

The exclusion set — what the byte assertion deliberately does NOT cover — is **not re-authored
here**. It is owned upstream by [`io-contract.md`](./io-contract.md) §7 (the IN / OUT conformance
list). This harness MIRRORS that list; the manifest below excludes exactly what §7 marks OUT.

## The manifest

One canonical JSON document per fixture. The serializer (`conformance.Marshal`) emits sorted keys,
two-space indent, LF newlines, no trailing whitespace, and exactly one trailing LF (see
[Canonical-JSON rules](#canonical-json-rules)). Top-level shape:

```json
{
  "fixture":     "<corpus fixture name>",
  "verdict":     { "validator": ["<code>", ...], "apierr": ["<code>", ...] },
  "topology":    <post-write-back model.Topology, or null on a fail fixture>,
  "allocations": { "node_overlay_ips": {...}, "peers": { "<linkKey>|<owner>": {...} } },
  "files":       { "<nodeID>": { "<relpath>": "<verbatim bytes>", ... }, ... },
  "checksums":   { "<nodeID>": "<bundlesig.Canonicalize output verbatim>", ... },
  "healed_edges": [ { "id": "...", ...seven pin fields... }, ... ]
}
```

### `verdict` — the two-channel outcome

The product keeps validation findings and transport/compile failures on **compile-time-distinct Go
types and distinct channels**: a validator finding rides a 200 `ValidateResponse`
(`internal/validator/code.go`), an `apierr.Code` rides the HTTP error envelope
(`internal/apierr/apierr.go`). The manifest preserves that split so the harness can tell a
validation failure from a compile-resource failure:

- **`validator`** — the sorted, de-duplicated set of `validator.Code` strings collected by running
  `validator.ValidateSchema` + `validator.ValidateSemantic` directly on the fixture topology
  (exactly as `/api/validate` does), across BOTH `errors[]` and `warnings[]`. Populated for every
  fixture: a green fixture carries whatever warnings the validator emits; a validator-FAIL fixture
  additionally carries its error code(s).
- **`apierr`** — the sorted set of `apierr.Code` strings from the compile error envelope. **Empty
  (`[]`, never `null`) on a successful compile.** On a coded compile-resource failure (e.g.
  `compile_transit_pool_exhausted`) it holds the single unwrapped `*apierr.Error` code; the
  validator channel stays whatever the validator emitted (clean — the topology passed validation, it
  just over-subscribed a pool). A topology that FAILS validation is rejected by the compiler with a
  plain `fmt.Errorf` wrap (not an `*apierr.Error`), so its `apierr` channel correctly stays empty and
  the verdict is the validator code(s).

Both channels are always non-nil slices so the JSON shape is stable. The fail corpus
(`testdata/fail/`) is asserted to span BOTH channels by `TestGoldenFail_SpansBothChannels`, so
neither channel is ever left untested.

### `topology` — the post-write-back compiled model

The full `model.Topology` after the compile write-back: allocated `OverlayIP` per node, the seven
pinned `*` edge fields + `CompiledPort`, derived router-ids/keys. This is the model the TS port must
reproduce field-for-field — a TS `Node` that drops `router_id` reds the `router-id-pinned` fixture
here (adding `router_id?` to the TS `Node` is plan-4/plan-6's job; this corpus only makes its
absence mechanically visible). **`null` on a fail fixture** (no compiled topology exists).

### `allocations` — the keyed allocator write-back projection

The load-bearing allocator output, keyed so the projection is order-free (`null` on a fail fixture):

- **`node_overlay_ips`** — `nodeID -> allocated overlay IP`.
- **`peers`** — `"<linkid.LinkKey>|<owner nodeID>" -> PeerAllocation`. The peer set is keyed by a
  **stable link identity + the owning node ID, NEVER by the `PeerMap` append position** (that is
  edge-array order — not a contract surface). A node pair carrying one folded link keys by the bare
  `linkid.PinKey`; a parallel pair (primary + backups) disambiguates by `"<pinKey>#<interfaceName>"`
  (the interface name is itself byte-stable). Each `PeerAllocation` carries only the *allocated*
  values — `remote_node_id`, `public_key`, `overlay_ip`, `interface_name`, `listen_port`,
  `local_transit_ip`, `remote_transit_ip`, `local_link_local`, `remote_link_local` — and DROPS the
  echoed-input fields (NodeName, AllowedIPs, Endpoint, keepalive, role-derived flags).

### `files` and `checksums` — the per-node bundle byte set

- **`files`** — `nodeID -> relpath -> verbatim file content`. **Per-file by key, never a
  concatenated blob:** WG configs are keyed `nodeID:interfaceName` and Go map iteration is
  non-deterministic, so only a keyed projection is comparable. These are exactly the checksummed
  bytes. `null` on a fail fixture.
- **`checksums`** — `nodeID -> bundlesig.Canonicalize output verbatim` (the canonical
  `checksums.sha256` content; `Canonicalize` sorts paths byte-order). `null` on a fail fixture.

### `healed_edges` — the heal canary surface

The corpus-INPUT topology after `normalize.HealCollidingPins` (run over a copy, so the compile path
is untouched), projected to `{id + the seven pin fields}` and **sorted by edge ID**. Computed for
**every** fixture independent of the compile verdict, because the vitest heal canary
(`frontend/src/lib/heal.conformance.test.ts`) pins this layer — the TS `healCollidingPins` must
byte-equal the Go heal over the shared corpus — regardless of whether the full TS pipeline can
compile the fixture yet.

## Canonical-JSON rules

The serializer is the byte contract; both languages must produce identical bytes. Rules:

1. **Sorted keys.** Every object's keys are emitted in sorted order. In Go this is `encoding/json`'s
   deterministic map-key sort plus a fixed struct field order; the TS producer must sort keys the
   same way (lexicographic by UTF-16 code unit, which matches Go for the ASCII key set in use).
2. **Two-space indent**, for human-diffable goldens.
3. **LF newlines** (never CRLF), and exactly **one trailing LF** at end of document.
4. **No trailing whitespace** on any line. `Marshal` verifies this property rather than assuming it
   (`errTrailingWhitespace` is the load-bearing backstop) so a stray space can't drift silently
   between languages.
5. **Stable empty shapes.** Absent code sets render as `[]`, not `null`; the success-only projections
   render as `null` (not `{}`) on a fail fixture, so a half-built bundle can never be frozen.

## Excluded from the byte set

Mirrored from [`io-contract.md`](./io-contract.md) §7 (OUT list) — **not redefined here**:

- **`manifest.json`'s `compiled_at`** — a wall-clock timestamp; the oracle injects a fixed
  `FixedCompiledAt` for determinism, but the field is out of the conformance byte set regardless.
- **`compiler.computeChecksum`** — `sha256(fmt.Sprintf("%v", topo))[:16]`, a display-only digest with
  no TS counterpart (NOT the signed `Canonicalize` digest, which IS in `checksums`).
- **The self-extracting `tar.gz` wrapper bytes** — the harness compares bundle CONTENTS + the
  per-node `checksums` only, never the installer wrapper.
- **Random private-key material** — non-reproducible by construction; the harness asserts key
  *derivation* (X25519, pinned by `testdata/keygen_kat.json` + `TestKeygenKAT`), not generation.
  Fixtures carry fixed per-node private keys as INPUT and the harness asserts only the derived PUBLIC
  material that surfaces in the rendered files.

## Coverage floor

A sibling guard, not a manifest field: `coverage_floor.json` + `TestCoverageFloor` enforce a
per-package statement-coverage floor over the local-compile pipeline packages (`model`, `allocator`,
`validator`, `linkid`, `naming`, `normalize`, `compiler`, `renderer`, `render`, `artifacts`,
`bundlesig`, `localcompile`). The test shells out to `go test -coverprofile` per package and reds if
any drops below its frozen floor — the mechanical answer to "coverage is unbounded by construction"
(a dropped fixture or an un-exercised new branch reds the build). `internal/model` has no executable
statements (pure declarations) and is a vacuous pass at floor 0. **Re-baseline** after an intentional
coverage change: measure `go test -cover ./internal/<pkg>/` for each package, set each floor in
`coverage_floor.json` a couple points below the new value, and commit the diff (there is deliberately
no `-update` path — the floor is a reviewed value, not a snapshot).

## Local-mode export divergence

In **local mode** the compile runs entirely client-side (the TS port, plan-4/plan-6), and the
browser cannot generate WireGuard private keys with the same source of randomness the Go server uses
— nor should it: local mode is zero-knowledge by design. The conformance contract therefore asserts
**derivation, not generation** (see Excluded, above): a fixture pins fixed per-node private keys as
input, and Go and TS must derive identical public keys and identical rendered bytes from them. A
real local-mode export, where keys are freshly generated in the browser, will produce DIFFERENT
private-key bytes than a server compile of the same topology — that divergence is expected and OUT of
the byte set. What must NOT diverge is everything downstream of the keys: the allocations, the pin
write-back, the rendered configs (modulo the key material), the checksums, and the heal. Those are
what the manifest freezes and what the cross-language gate enforces.

## See also

- [`io-contract.md`](./io-contract.md) — the upstream façade + the authoritative IN/OUT
  byte-exclusion set (§7) this doc mirrors; the keygen seam (§6); the four cross-language authorities
  (§4).
- [`allocation-stability.md`](./allocation-stability.md) — the I1–I10 determinism invariants the
  corpus exercises; the conformance harness is their mechanized drift guard.
- `internal/conformance/manifest.go`, `oracle.go`, `golden_test.go`, `drift_test.go`,
  `keygen_kat_test.go`, `coverage_floor_test.go` — the code of record.
