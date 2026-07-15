# Compiler I/O Contract — `internal/localcompile`

The **frozen, reproducible input→output contract** of the local compile path: the schema /
semantic validation, IP allocation, capability inference, peer derivation, the renderers,
`render.All`, and the artifacts byte set that `artifacts.Export` writes to disk — all behind
the single Go façade `internal/localcompile.Compile`.

This document is the **authority** the in-browser Go/WASM engine builds on (the same
`internal/localcompile.Compile` façade compiled to `web/yaog.wasm`) and the **WASM conformance gate**
(`scripts/wasm-conformance-gate.mjs`) enforces. Its golden corpus
(`internal/localcompile/testdata/contract/`) is the **authoritative byte-freeze**: the exact bytes
today's pipeline emits for a fixed set of fixtures with fixed keys and a fixed clock.

> Scope: this contract freezes the current shared behavior. It does not independently define
> `router_id` semantics; it records the `RouterID` write-back the pipeline performs. When an
> intentional artifact-contract change lands, the Go golden corpus and fresh WASM output are
> regenerated and reviewed together so the CLI, browser, and controller do not acquire divergent
> implementations.

---

## 1. The façade

```go
func localcompile.Compile(req CompileRequest) (CompileArtifacts, error)
```

`Compile` is a **pure function**: no environment read, no `time.Now`, no filesystem access, no
global state. Every non-deterministic and environment-coupled input is lifted into an explicit
`CompileRequest` field, so an identical request always yields a byte-identical result. This purity
is proven by `TestContractGolden_PureFunction` (run-twice-assert-equal over every fixture). It is
also pure with respect to its **input**: it clones the topology's node/edge slices, so the
pipeline's in-place write-backs never touch the caller's `req.Topology` — the written-back topology
(keys + allocated pins/IPs) is the returned `CompileArtifacts.Topology`
(`TestCompile_InputTopologyUnmutated`).

**Context is orthogonal to the contract.** The frozen `CompileRequest` carries no Go `context`
(the WASM engine shares the same context-free seam), and the pure `Compile(req)` / `CompileResult(req)`
entry points compile under `context.Background()`. The live Go caller that wants a request-bounded,
cancellable allocator scan (the controller subgraph compile) uses the
`CompileResultCtx(ctx, req)` sibling — `ctx` affects neither the allocated values nor the rendered
bytes, so it is deliberately not a request field.

### `CompileRequest` (topology-in)

| Field | Type | Meaning |
|-------|------|---------|
| `Topology` | `model.Topology` | The only required input. |
| `Custody` | `render.KeyCustody` | `AirGap` (local/CLI — private keys round-trip through the topology JSON) or `AgentHeld` (controller — zero-knowledge custody, only public keys persist; see `docs/spec/controller/key-custody.md`). |
| `Keygen` | `localcompile.Keygen` | The WireGuard key-derivation seam (see §6). `nil` ⇒ the default `wgtypesKeygen` (byte-identical to today). |
| `SigningKey` | `bundlesig.ConfigSigner` | The optional tier-1 bundle signer. It is the **interface** (not a `*bundlesig.Signing` pointer) — a `nil` interface means "unsigned", the byte-identical no-signing path; the interface avoids Go's typed-nil gotcha so a plain `SigningKey == nil` test is safe. **Never read from `YAOG_BUNDLE_SIGNING_KEY` by the façade** (that would break purity); the caller constructs the signer and injects it. |
| `Fetch` | `render.FetchSettings` | The typed channel of install-time fetch pins (mimic GitHub-`.deb` fallback, agent self-update catalog). Its **zero value** means "no catalog configured", which MUST leave `install.sh` and the signed bundle byte-identical. Replaces the in-pipeline `FetchSettingsFromEnv` read. |
| `CompiledAt` | `time.Time` | The explicit compile clock, replacing the compiler's internal `time.Now()`. Feeds **only** `manifest.json`'s `compiled_at`, which is **OUT** of the conformance byte set (see §7). |
| `Reserved` | `*compiler.ReservedAllocations` | Controller subgraph path only: the allocation resources (ports / transit IPs / link-locals) occupied by edges outside a subgraph, so gap-fill allocates around them. `nil` (the default) ⇒ a full compile, behavior unchanged. |

### `CompileArtifacts` (artifacts-out)

| Field | Type | Meaning | Conformance |
|-------|------|---------|-------------|
| `Topology` | `*model.Topology` | The compiled topology with allocator write-backs applied: the seven `model.Edge` pin fields + `OverlayIP` + `RouterID` per node. | allocated values **IN**; echoed input **n/a** |
| `Files` | `map[string]map[string]string` | Per-node bundle set: `nodeID -> relpath -> content` (see §3). | **IN** |
| `Deploy` | `map[string]string` | Project-level custody-aware helpers (`deploy-all.sh` / `deploy-all.ps1`): operational SSH scripts for AirGap, fail-closed guidance stubs for AgentHeld. | **IN** |
| `Checksums` | `map[string]string` | `nodeID -> checksums.sha256` content, via `bundlesig.Canonicalize` (see §2). | **IN** |
| `Signatures` | `map[string]string` | `nodeID -> bundle.sig` (**bare** base64 of the Ed25519 signature over the node's canonical checksums). Present only when `SigningKey != nil`. The on-disk `bundle.sig` the exporter writes is this same base64 **plus a trailing newline** — a file-representation detail; the signed/digest-bound bytes (the node's `checksums.sha256`) are identical. | **IN** (when signing on) |
| `SigningPubPEM` | `[]byte` | PKIX (`PUBLIC KEY`) PEM of the verifying key, identical per node. Present iff signing on. | **IN** (when signing on) |
| `Warnings` | `[]validator.ValidationError` | Non-fatal schema/semantic findings. | informational |
| `Manifest` | `compiler.CompileManifest` | The compile summary. `CompiledAt` (timestamp) and `Checksum` (display-only digest) are **OUT** (see §7). | mixed |

The shape mirrors what `artifacts.Export` writes to disk. The exporter consumes a rendered
`compiler.CompileResult` rather than this struct, but both paths call the same
`artifacts.BundleFiles` member-set authority and canonicalizer; the filesystem layout is
presentation, while member identity and bytes are contract surfaces.

---

## 2. Canonical serialization — `bundlesig.Canonicalize`

The single authority for per-node bundle integrity is `internal/bundlesig.Canonicalize(files
map[string]string) []byte` (`internal/bundlesig/bundlesig.go`). It is the byte-exact content of a
node's `checksums.sha256` file and the message that `bundle.sig` signs. Every consumer reproduces
it byte-for-byte:

1. For every `(path, content)` pair, compute `sha256(content)`.
2. Emit one line per path in `sha256sum` binary-mode form: `"%x  %s\n"` — 64 lowercase hex digits,
   **two** spaces, the path, a single `\n` (LF, no CR).
3. **Sort the lines by path in raw byte order** (Go `sort.Strings`, byte-wise — the TS comparator
   must compare by code unit / byte, not by locale).

The output is deterministic and independent of map iteration order. A signature is **always** over
`Canonicalize`'s output and **never** over `compiler.computeChecksum` (a non-canonical
`fmt.Sprintf("%v")` digest unsafe to sign — see §7). See `docs/spec/controller/signing.md`.

---

## 3. Per-node bundle file shape (`CompileArtifacts.Files`)

For each node, `Files[nodeID]` maps a stable **relpath** to file content. These relpaths are the
checksummed set — exactly the bytes `Checksums` and (when signing on) `Signatures` cover:

| Relpath | Present on | Notes |
|---------|------------|-------|
| `wireguard/<iface>.conf` | every node | One per per-peer interface; the **client** role's single tunnel is `wireguard/wg0.conf`. `<iface>` = `naming.WgInterfaceNameForEdge` output (§4). |
| `babel/babeld.conf` | non-client nodes | Babel router config. Omitted for the client role (the client exception: single `wg0`, no Babel). |
| `sysctl/99-overlay.conf` | every node | Forwarding / `rp_filter` settings. |
| `install.sh` | every node | Install / uninstall script (verifies root, splices keys, applies the SNAT fix). |
| `README.txt` | every node | Human usage and custody guidance. It is a member so an untrusted delivery cannot rewrite the AgentHeld application instructions without invalidating integrity. |
| `artifacts.json` | when a catalog is configured | The controller-signed mimic/agent-update pins. **Omitted entirely when no catalog is configured**, so a non-catalog bundle stays byte-identical. A signed member (its pins inherit the bundle's signature + keystone digest). |

`bundle.sig` / `signing-pubkey.pem` (when signing on), `checksums.sha256`, and `manifest.json` are
**not** members of the checksummed set: the first two are the authenticity layer over it,
`checksums.sha256` is the digest list itself, and the manifest is compile metadata. They are
represented separately rather than in `Files`.

This per-node set is **single-sourced**: both the in-memory `CompileArtifacts.Files`
(`localcompile.ArtifactsFromResult`) and the on-disk bundle (`artifacts.Export`) build it through
the one `artifacts.BundleFiles(result, nodeID)` helper, so the relpath keys + set membership (incl.
the `artifacts.json` D4 guard) can never drift between the two. (The helper lives in `artifacts`, a
sink package — `apierr`/`bundlesig`/`compiler` only — which `localcompile` imports freely; the
reverse direction would cycle, since `render`'s tests depend on `artifacts` and `localcompile`
depends on `render`.)

---

## 4. The four cross-language authorities

The WASM engine compiles these from the same Go source; the **WASM conformance gate** asserts the
in-browser (`web/yaog.wasm`) output is byte-equal to the frozen Go golden.

### 4.1 `internal/linkid` — link identity

The single authority for "what counts as the same link" (`internal/linkid/linkid.go`). Shared by
the peer-derivation compiler and the semantic validator (hoisted into a leaf to break the
compiler→validator import cycle).

- **`PinKey(a, b)`** — canonical pair identity: the two node IDs sorted and joined with `"|"`.
  Direction-agnostic: `PinKey(A, B) == PinKey(B, A)` (invariant I3).
- **`LinkKey(e)`** — per-edge link identity: `PinKey(from, to)` for the primary class
  (`role != "backup"`, so all non-backup edges of a pair fold into one link), and
  `PinKey(from, to) + "#" + e.ID` for a backup edge (each backup is its own link).
- **`IsBackup(e)`** — `e.Role == "backup"`; empty role and `"primary"` are both primary class.

### 4.2 `internal/naming` — portable node IDs and interface names

The single authority for portable node-directory IDs and WireGuard interface names
(`internal/naming/naming.go`; [artifact naming](../artifacts/naming.md)). It is a stdlib-only leaf
package shared by validation, export, and rendering.

- **`ValidPortableNodeID(nodeID)`** — accepts only the canonical cross-platform bundle-directory
  contract: `[A-Za-z0-9._-]+`, at most 240 ASCII bytes, no trailing dot, Windows device basename,
  or project-helper collision.
- **`PortableNodeIDKey(nodeID)`** — ASCII-lowercase collision key. Semantic validation rejects two
  node IDs that share it, because a Windows extraction would alias their directories.
- **`WgInterfaceName(remoteName)`** — `"wg-"` + cleaned name (non-`[a-z0-9-]` → `-`, underscore
  maps to `-`); ≤15 chars returned as-is, otherwise
  `"wg-" + clean[:8] + sha256(remoteName)[:4]` (15-char budget).
- **`WgInterfaceNameForEdge(remoteName, edgeID, backup)`** — for `backup == false` returns
  `WgInterfaceName(remoteName)` byte-identical (zero rename for existing fleets); for `backup ==
  true` returns `"wg-" + clean[:8] + sha256(remoteName + "|" + edgeID)[:4]` unconditionally, folding
  the edge ID into the hash so parallel backups diverge. **This is the babel sort key's source** (§5).

### 4.3 `internal/normalize` — pin-collision heal

`HealCollidingPins` (`internal/normalize/pins.go`) repairs the "pin occupied by two different links"
corruption: an edge whose pinned port / transit IP / link-local collides with another **enabled**
edge of a **different** `LinkKey` has its whole allocation stripped (re-allocated fresh next compile).
It is the inverse of the semantic validator's cross-link dedup and the browser-side mirror of
`frontend/src/lib/normalizeEdges.ts`. Discriminator: claims are processed in **`LinkKey`-sorted order**
(mirroring the allocator's reserve-first gap-fill); the first claimant keeps the slot, every later
different-link claimant is stripped as a unit. Result is always collision-free, deterministic, and a
stable fixed point. `canonicalIP(value)` (`net.ParseIP(value).String()` when parseable, else the raw
value) defines the heal's notion of "same address" and **mirrors `internal/validator/semantic.go`'s
`canonicalIP`** exactly — what the heal strips is precisely what the validator flags.

### 4.4 The model pin-tag set — `model.Edge`

The **seven** JSON tags on `model.Edge` (`internal/model/topology.go`) that the compiler writes back,
the frontend persists to localStorage, and the round-trip preserves verbatim:

```
compiled_port
pinned_from_port        pinned_to_port
pinned_from_transit_ip  pinned_to_transit_ip
pinned_from_link_local  pinned_to_link_local
```

`frontend/src/lib/normalizeEdges.ts`'s `PIN_FIELDS` array mirrors this exact set. **There is no Go
symbol named `PIN_FIELDS`** — the Go authority is the struct-tag set itself; the TS `PIN_FIELDS`
constant is the mirror, and the drift-guard (plan-5) pins them equal. Each resource is a pair: an
edge is either fully pinned (both ends) or not at all; a single-ended pin is rejected by the
validator.

---

## 5. Babel line-ordering rule (the as-shipped C1 freeze)

`babeld.conf`'s **`interface` lines and `redistribute … /32` client lines** are emitted in the order
of the node's peer slice **sorted by `InterfaceName`**, where `InterfaceName` =
`naming.WgInterfaceNameForEdge(remoteName, edgeID, backup)` (§4.2). This is the as-shipped beta.8 C1
fix (`internal/renderer/babel.go`, `sortedPeers := append([]compiler.PeerInfo(nil), peers...)` sorted
by `InterfaceName`, consumed by both emission loops). The peer slice is *built* in topology
edge-array order (`peers.go` Pass 2); the **renderer's sort** is what makes the output depend on link
identity, not edge-array position.

**The babel renderer sorts the peer slice by the `InterfaceName` key before emitting.** This chains
the babel ordering to the `naming` cross-language authority so the output is byte-stable under an
edge reorder. Pinned by `internal/renderer/babel_test.go`
`TestRenderBabelConfig_StableUnderPeerReorder` (renderer-level) and by the compiler-level
edge-reorder fixture pair in the golden corpus (full-pipeline).

> **Documented non-guarantee (a C1-class residual, not fixed here).** The compiler-level
> edge-reorder fixture pair confirms `babeld.conf`, `sysctl/99-overlay.conf`, and every
> `wireguard/<iface>.conf` are byte-stable under a benign edge reorder. It **also reveals** that
> `install.sh` and the deploy scripts (`deploy-all.sh`/`.ps1`) still enumerate per-peer interfaces /
> nodes in peer-slice (edge-array) order, so those files — and therefore the per-node
> `checksums.sha256` that covers `install.sh` — are **not** byte-stable under a wholesale array
> reversal. The C1 fix that shipped in beta.8 sorts only the babel renderer's peer slice. Fixing the
> script renderer's interface enumeration to sort by `InterfaceName` like babel does is a roadmap
> item; this freeze plan introduces no intentional byte change and only documents the residual. The
> golden test asserts byte-equality over only the C1-covered surface (babel/sysctl/wireguard).

---

## 6. The keygen seam + fixed-key requirement

WireGuard key derivation is decoupled from `wgtypes`/`wgctrl` (the browser/WASM blocker) behind the
`Keygen` interface (`internal/localcompile/keygen.go`):

```go
type Keygen interface {
    DerivePublic(privB64 string) (pubB64 string, err error)        // base64 public for a base64 X25519 private
    Generate() (privB64, pubB64 string, err error)                 // fresh X25519 pair (air-gap case-c)
    ParseAndNormalize(privB64 string) (canonicalPrivB64, err error) // wgtypes' privateKey.String() round-trip
}
```

- **`wgtypesKeygen`** (default) wraps today's exact `wgtypes` calls — production stays byte-identical.
- **`ecdhKeygen`** is the stdlib `crypto/ecdh` X25519 reference implementation, proven byte-equal to
  `wgtypesKeygen` over 10k random inputs on `DerivePublic` and `ParseAndNormalize`
  (`keygen_equivalence_test.go`). It gives the in-browser WASM engine a `wgctrl`-free,
  stdlib-anchored definition — the `js/wasm` Go build the browser runs.

**Fixed-key requirement for conformance.** Random private-key material can never be byte-asserted, so
the conformance corpus pins **only public-key *derivation***: every fixture ships **fixed per-node
private keys** (air-gap, case-a) or **fixed public keys** (AgentHeld), so the `Generate()` (random,
case-c) path is never exercised under conformance. Under `AgentHeld` custody the rendered
`[Interface] PrivateKey` is the sentinel `PrivateKeyPlaceholder` and **no private key reaches the
bundle** — zero-knowledge custody (principle P2). Under `AirGap` the fixed private key legitimately
round-trips through the topology JSON (invariant I5) and appears, deterministically, in the rendered
config.

---

## 7. IN / OUT conformance list

What the cross-language byte-equality assertions cover, and what they deliberately exclude.

**IN (must be byte-identical native Go ↔ Go/WASM):**
- Every rendered file in `CompileArtifacts.Files` — `wireguard/<iface>.conf`, `babel/babeld.conf`,
  `sysctl/99-overlay.conf`, `install.sh`, `README.txt`, and `artifacts.json` (when a catalog is
  configured).
- The project-level custody-aware `Deploy` helpers (`deploy-all.sh` / `deploy-all.ps1`). AirGap
  output is operational; AgentHeld output must remain a non-executing guidance stub.
- The per-node `Checksums` (`checksums.sha256`, via `Canonicalize`).
- The allocated values written back onto the topology: `OverlayIP` per node and the seven pin fields
  per edge (allocated ports / transit IPs / link-locals — §4.4).
- WireGuard public-key **derivation** (`DerivePublic` / `ParseAndNormalize`), proven via the
  X25519 equivalence test and asserted indirectly through the rendered configs.
- `bundle.sig` (`Signatures`) and `signing-pubkey.pem` (`SigningPubPEM`) **when signing is on** — the
  controller relies on these, so the signing path is byte-frozen too.

**OUT (excluded — no TS counterpart required; masked in the golden corpus):**
- **Random private-key material** — non-reproducible by construction; conformance asserts derivation,
  not generation (§6).
- **`manifest.json`'s `compiled_at`** — a wall-clock timestamp, sourced from
  `CompileManifest.CompiledAt` (← `CompileRequest.CompiledAt`). Display/provenance only.
- **`manifest.json`'s `checksum`** — `compiler.computeChecksum` is `sha256(fmt.Sprintf("%v", topo))[:16]`,
  a **non-canonical, display-only** digest with no TS counterpart and explicitly NOT the signed
  member hash (that authority is `Canonicalize`, §2). Install scripts and agents do not verify this
  metadata field. It is masked in the golden corpus.

`manifest.json` itself is reconstructed by the `artifacts.Export` adapter at write time from the
deterministic identity fields plus these two OUT fields; it is not a member of `CompileArtifacts.Files`.

The browser/WASM preview ZIP container is also presentation rather than a byte-frozen field. Its
contents are contract-bound: ID-keyed `Files`, matching `Checksums`, and the matching root `Deploy`
helpers from one compile are written into the same archive.

---

## 8. Inherited invariants

The compiler-level guarantees this contract sits on top of are normative in
`docs/spec/compiler/allocation-stability.md` (invariants **I1–I10**): superset stability (I1), order
independence (I2), identity binding (I3), additive growth (I4), key persistence (I5), headroom /
pool exhaustion (I6), validated pins and explicit renumber (I7), change observability (I8), garbage
collection / no leak (I9), and schema versioning (I10). The pin write-back, the `linkid` link
identity, and the `normalize` heal all serve these. This contract **freezes** the byte output those
invariants produce; it does not redefine them.

---

## 9. The golden corpus

`internal/localcompile/testdata/contract/` is the authoritative byte-freeze, asserted by
`internal/localcompile/contract_golden_test.go`:

- `topologies/*.json` — ≥8 self-describing fixtures (`{name, doc, custody, signing, topology}`)
  spanning the byte-exactness surface: single primary link; parallel primaries (unify); a backup link
  (distinct `LinkKey`); the client role (single `wg0`, no babel); multi-domain; a pinned-pins
  round-trip (I7); the edge-reorder pair (compiler-level C1); a `router_id`-pinned node (write-back
  freeze); a `transport:tcp`/mimic node; a signing-on fixture (`bundle.sig`/`signing-pubkey.pem`);
  and an AgentHeld mesh.
- `golden/*.golden` — the committed expected output (the masked projection of `CompileArtifacts`).
- `signing/test-signing-key.pem` — a **throwaway** test Ed25519 key (PKCS#8 PEM), NOT any production
  key. The signing-on fixture loads it and injects it via `CompileRequest.SigningKey` (the façade
  never reads `YAOG_BUNDLE_SIGNING_KEY`).

Each fixture runs through the façade with `ecdhKeygen` + the fixed `CompiledAt`; the masked artifacts
(`compiled_at`/`checksum` dropped) must byte-equal the committed golden. Regenerate after an
**intentional** contract re-version with `go test ./internal/localcompile/ -run TestContractGolden
-update`, review the diff, and commit; a plain `go test` (the gate + CI) never touches the goldens.
