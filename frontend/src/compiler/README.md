# `frontend/src/compiler/` — the pure TypeScript local/air-gap compiler

This directory is a **pure, side-effect-free TypeScript library** that reimplements the YAOG
local / air-gap compile path in the browser: design → validate → compile → render → export, with no
network round-trip to the Go backend. It is the leaf-first mirror of the Go pipeline
(`internal/{validator,allocator,compiler,renderer,render}` behind the `internal/localcompile` façade),
so local mode runs entirely client-side and the deployed server becomes controller-only.

It is a **library only**. It mutates no store, reads no feature flag, opens no network connection, and
reads no clock it isn't handed. Store wiring, the `VITE_YAOG_LOCAL_ENGINE` flag, the dev canary, and the
production cutover are **plan-6** — none of that lives here.

---

## Public API surface

`index.ts` is the **only** module a consumer (plan-6's store rewire) imports. Everything else under this
directory is internal. The four entry points mirror the four backend air-gap routes byte-for-byte in
**behaviour** and in **response shape**, so plan-6 wires them with **no shape translation**:

| Entry point | Mirrors | Returns | Store call site (`topologyStore.ts`) |
|---|---|---|---|
| `compile(topo, custody?)` | `POST /api/compile` (`HandleCompile`) | `CompileResult` (rich oracle output) | `compile` (`:744`) — assigns `compileResult: CompileResponse` |
| `validate(topo)` | `POST /api/validate` (`HandleValidate`) | `ValidateResponse` = `{ valid, errors, warnings }` | `validate` (`:708`) — assigns `validateResult: ValidateResponse` |
| `exportArtifacts(topo)` | `POST /api/export` | `Promise<Blob>` (per-node bundle ZIP) | `exportArtifacts` (`:811`) — `res.blob()` |
| `deployScript(topo, format)` | `POST /api/deploy-script?format=sh\|ps1` | `string` (one deploy script) | `downloadDeployScript` (`:859`) — `res.blob()` |

Plus two pure helpers and the conformance/export builders:

- **`toCompileResponse(result): CompileResponse`** — projects the rich `CompileResult` `compile()` returns
  into the snake_case `CompileResponse` shape `/api/compile` returns (`handler.go:169-178`), so plan-6
  wires `compile(topo)` → `toCompileResponse(...)` and assigns the result with no hand-written rename. The
  rich `CompileResult` stays available (the export path and the conformance harness consume it directly).
- **`exportArtifacts` / `buildFiles` / `buildChecksums` / `canonicalize` / `bundleFiles`** (from
  `export.ts`) — the per-node bundle ZIP plus the in-memory file-set / checksum builders the conformance
  harness compares byte-for-byte against the Go golden.
- **`generateRouterID`** (from `peers.ts`).

### Why `compile()` returns `CompileResult`, not `CompileResponse`

`CompileResult` (camelCase, plus `peerMap` / `clientConfigs` / `artifactsJSON`) is a **superset** of the
wire `CompileResponse` (snake_case, fewer fields). It is the *oracle* output: the conformance harness and
the export builders (`buildFiles` / `exportArtifacts`) consume `result.topology` / `result.peerMap` /
`result.wireGuardConfigs` / `result.deployScripts` directly. `toCompileResponse()` does the lossless
projection to the store shape, so plan-6 still gets "no shape translation" while the rich result remains
available to everything that needs it.

### Purity contract

Every entry point operates on a deep-enough **copy** of its input (the schema pass normalizes
`routing_mode`/`transport` *on the copy*, never on the caller's object) and consumes no ambient state. The
one caller-supplied impurity is `exportArtifacts`' `compiledAt` clock, which only stamps `manifest.json`'s
`compiled_at` — a field **excluded** from the conformance byte set, so a varying timestamp never reds the
harness.

---

## Module map (internal)

| Module | Mirrors (Go) |
|---|---|
| `model.ts` | the frozen wire model (`../types/topology.ts`) + compile-output types (`compiler.go:23-62`) |
| `linkid.ts` | `internal/linkid` (`PinKey`/`LinkKey`/`IsBackup`) — also the canonical impl `normalizeEdges.ts` imports |
| `naming.ts` | `internal/naming` (synchronous SHA-256 via `@noble/hashes`, no async in the core) |
| `cidr.ts` | the IP/uint32 math behind allocation (`>>> 0` everywhere) |
| `capabilities.ts` | `roles.go` (`InferCapabilitiesFromRole` / role semantics) |
| `escape.ts` | `internal/renderer/escape.go` (`bashSingleQuote` = the `shq` template func) |
| `keygen.ts` | the plan-3 three-op `Keygen` seam (`@noble/curves` x25519, internal clamp) |
| `errors.ts` | `internal/apierr` code carrier (`CompileError`) |
| `validator.ts` | `internal/validator` (`ValidateSchema` + `ValidateSemantic`) |
| `allocator.ts` | `internal/allocator/ip.go` |
| `peers.ts` | `internal/compiler/peers.go` (Pass 1 reserve-then-gap-fill + Pass 2 PeerInfo) |
| `renderers/*.ts` | `internal/renderer/{wireguard,babel,sysctl,script,deploy}.go` (+ `template.ts` chomp subset) |
| `export.ts` | `internal/artifacts/export.go` + `bundlesig.Canonicalize` |
| `index.ts` | `internal/compiler/compiler.go CompileAt` orchestration + the public API |

---

## The maintenance contract — Go is the ORACLE

**The Go pipeline is the authoritative specification. It is the oracle, forever.** This TypeScript port
has no independent authority: its only definition of "correct" is "byte-identical to what the Go pipeline
produces." If the two ever disagree, the Go output is right and this port is wrong — a TS-local compile and
a controller (Go) recompile silently disagreeing on a port / IP / rendered byte is worse than a crash,
because it ships a tunnel that won't hand-shake.

This is stated in the spec: `docs/spec/compiler/io-contract.md` calls the golden corpus
(`internal/localcompile/testdata/contract/`) the **"authoritative byte-freeze"**; `docs/spec/compiler/`
(`pipeline.md`, `validation.md`, `ip-allocation.md`, `peer-derivation.md`, …) describes the pipeline the
Go code implements and this port mirrors.

### The conformance harness is the drift gate

`internal/conformance/` is the Go↔TS conformance harness (plan-5). It runs the **same fixture corpus**
through both the Go oracle and this TS library and asserts byte/value equality of:

- the compiled topology (every allocated port / transit IP / link-local / overlay IP / derived pubkey),
- every rendered file (WireGuard / Babel / sysctl / install scripts / deploy scripts), and
- the per-node `checksums.sha256`.

It runs in CI as a **required check** on every PR (Go or TS) via the `conformance` job in
`.github/workflows/ci.yml`:

- the Go oracle + golden + drift + KAT + the i18n catalog-sync (`go test ./internal/conformance/ ...`
  plus `TestI18nCatalogSync` / `TestI18nCatalogParity`), and
- the TS half: `npm run conformance` (= `vitest run --config vitest.config.ts`), whose `include` glob
  `['src/**/*.conformance.test.ts', 'src/compiler/**/*.test.ts']` collects every
  `src/compiler/*.conformance.test.ts` (alloc / renderers / export / validator) plus the leaf-primitive
  unit tests (KAT, CIDR, schema).

### When you change the Go pipeline, you must:

1. **Refresh the plan-5 fixtures / goldens.** Any *intentional* change to allocation, peer derivation, a
   renderer template, validation codes, or the export bundle changes the Go golden output. Regenerate the
   golden corpus (e.g. `go test ./internal/localcompile/ -run TestContractGolden -update`, plus the
   conformance harness's own `-update` paths), **review the diff**, and commit it. A plain `go test` (the
   gate and CI) never rewrites a golden — it only asserts against the committed freeze.
2. **Make the parallel TS change here** so this port reproduces the new Go behaviour byte-for-byte. A Go
   change without a matching TS change reds the conformance harness — **that red is the point**: it is the
   drift gate catching the two implementations diverging.
3. **Keep the codes in lockstep.** There is intentionally **no generated `codes.ts`** (the codegen was
   superseded). The canonical code strings live in the Go `validator.Code` / `apierr.Code` source and the
   FE `error.<code>` i18n catalog; `internal/i18n_catalog_sync_test.go` (`TestI18nCatalogSync`) enforces
   that every Go code has a catalog entry. A new Go code without a catalog entry reds CI.

The drift manifest (`internal/conformance/drift_manifest.json`, asserted by `drift_test.go`) additionally
pins the cross-site invariants — e.g. the transit CIDR literal `10.10.0.0/24` must agree at all three Go
sites (`peers.go`, `semantic.go`, `script.go`) and in the TS port. See
`docs/spec/compiler/conformance-manifest-schema.md` for the manifest format and re-baselining procedure.

**In one line:** every Go pipeline change refreshes the plan-5 fixtures and may require a parallel TS
change; the conformance harness is the gate that makes drift impossible to merge silently.
