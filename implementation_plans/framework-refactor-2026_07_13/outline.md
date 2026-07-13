# framework-refactor — WASM-Unified Core + Machine-Gated Paydown

> Subject opened 2026-07-13 (owner request: "sweep the entire repository and find all code-quality /
> framework issues... propose a framework that could refactor our current code upon — this could be
> radical, to eliminate all former engineering debts").
> **DRAFTED, plans-only at draft time — execution starts on the owner's go.**
> Execution per-PR: build → independent workflow review (correctness / completeness / hygiene /
> structure) → fix → re-review → CI green → merge. Each phase is one branch/PR (or a small group).
>
> **Full source of truth for the assessment + the framework:** [`docs/design/framework-refactor-proposal-2026_07_13.md`](../../docs/design/framework-refactor-proposal-2026_07_13.md)
> (produced by a 24-agent repo-wide debt sweep + a 21-agent judged design panel; the WASM feasibility
> claim was verified directly). This outline is the executable spine; the proposal is the rationale.

## Mission

Eliminate concentrated, mostly-mechanical engineering debt in a fundamentally sound codebase **without a
rewrite**, by (a) removing the single largest structural liability — the ~17K-LOC hand-mirrored Go/TS
compiler — **at its root** by compiling the *existing* pure Go pipeline to `GOOS=js GOARCH=wasm` and
running it in the browser; (b) converting every remaining boundary from reviewer-convention into a
**machine-gate** (arch-test import ratchet, `ShellToken` shell seam, wire-DTO/`omitempty` drift gate,
single-sourced field/file lists); and (c) paying down the stateful god-objects along seams that already
exist, with the highest-risk `Store` behavioral-core unification sequenced **last**.

**Success criteria:**
- The compile/validate/render algorithm exists **once** (Go), run in the browser as `web/yaog.wasm`,
  with parity gated *forever* by a headless WASM-vs-golden CI check.
- `frontend/src/compiler/` (~10.6K LOC), `internal/conformance/`, the drift manifest, the 233 rotting
  Go-line-number citations, the JS-escaped bash twin, and the `@noble`/`jszip` FE deps are **deleted**.
- Every remaining boundary is a **red build**: the pure-core import rule, the root-shell escape seam,
  the wire-DTO/`omitempty` mirrors, and the single-source file/field lists.
- The `Store` is **one** behavioral core over a thin KV port; `MemStore` (all tests) exercises the
  shipping telemetry-overlay path; the 1559-line compat test mostly retires.
- The god-files (`filestore.go`, `compile.go`, `controllerStore.ts`, `controllerClient.ts`,
  `semantic.go`, `peers.go`) are split along their real seams.
- **Every hard invariant below is preserved** (several hardened). No phase ships red or bricks a fleet.

## Principles (invariants the executor MUST respect)

Inherits **all** of [`PRINCIPLES.md`](../../PRINCIPLES.md) (deployable configs; allocation stability;
no unescaped user text to a root shell; backend sole port authority; signed-update custody;
zero-knowledge key custody; pure/stateless minimal-dep compiler; backward-compat topologies; the
execution-discipline rules — no shims, structure-aware, no scope compromise, per-PR review). On top,
this subject adds the **invariant-preservation contract** every phase is checked against
(all `[STATED]`, from the owner-accepted proposal):

1. **Parity is proven by execution + a permanent gate, never by construction.** [HIGH] Once WASM runs
   the pure pipeline, "local == controller" holds because it is the *same Go code*, and a permanent
   headless CI gate executes `yaog.wasm` against the Go golden corpus forever. *Violation:* deleting the
   TS twin or the conformance corpus before the permanent WASM-vs-golden gate is green and soaked;
   trusting "same source ⇒ same target" without the gate.
2. **Deployable configs / allocation stability survive the WASM move byte-for-byte.** [HIGH] Output is
   deterministic integer/string (no floats, no timestamps). *Violation:* a `GOARCH=wasm`
   map-order/float/time quirk reaching output uncaught; a blind `-update` of goldens instead of a
   reviewed byte-diff.
3. **The root-shell seam is typed, not conventional.** [HIGH] A `ShellToken` newtype types every render
   field that reaches a root shell; `field_safety_test` enumerates every template-consumed string;
   distinct `ShellQuoted`/`ShellRaw` constructors preserve the two real escaping contexts. *Violation:*
   a naive deletion of the `NodeName`/`NodeNameQuoted` twin without the typed replacement; a bare
   interpolation of user-controlled text into a template shell position.
4. **Backend stays the sole port authority.** [HIGH] Same Go allocator in WASM and controller; `51820`
   single-sourced in `allocconst`; WASM local mode is design-**preview** only and never deploys/stages.
   *Violation:* the FE stamping ports; a second copy of the listen-port base.
5. **Signed-update custody lives in ONE predicate.** [HIGH] The `storecore` keeps the promote /
   served-vs-staged / monotonic-anti-rollback predicate in one function; the single `BundleFileSet`
   makes written == listed == signed == checksummed *structurally*. *Violation:* re-forking the
   served-vs-staged predicate across store impls; a file written but not in the signed/checksummed set.
6. **Zero-knowledge key custody is preserved and single-sourced.** [HIGH] WASM keygen uses the proven
   `crypto/ecdh` seam; one keygen impl; the FE `partialize` allowlist stays ONE gate in
   `stores/controller/persist.ts` even after slicing. *Violation:* a private key reaching the
   controller/bundles; fragmenting the localStorage custody allowlist across slices.
7. **The pure/stateful quarantine becomes a red build.** [MEDIUM] `internal/arch/layers_test.go`
   enforces pure-core-imports-nothing-outward; the `runtimecontract` eviction of **`model.Condition` only**
   removes today's reverse type-placement leak through `model` (`MimicOutcome*`/`MimicBreadcrumbPath` stay
   model-resident — the pure-core renderer consumes them; evicting them would BREAK this invariant).
   *Violation:* a new edge from the pure core to a stateful package (incl. a `renderer→runtimecontract`
   edge if the mimic types were wrongly moved); padding the arch-test allow-list (CI must fail when it
   GROWS). *Note:* the reverse type-placement (stateful types DEFINED in `model`) needs a placement check
   distinct from the import-direction test.
8. **Backward-compat of persisted topologies.** [MEDIUM] `model` schema JSON tags unchanged; splits are
   type-moves. *Violation:* renaming a JSON field; a model change that fails to load a prior topology.
9. **The two-deployment story survives (and strengthens).** [HIGH] Local mode becomes a static SPA +
   `yaog.wasm` (more backend-free than today); controller mode unchanged; the airgap server is retained
   as the e2e/DAST boot target until Phase 9. *Violation:* a window where there is no working local
   engine; deleting the airgap server before e2e/DAST are re-pointed.

## Current state of the world (2026-07-13)

- Branch: `main` (clean). Last shipped: `v2.0.0-rc.5` (GitHub Latest; the telemetry-history +
  delta-deploy subject, PRs #249–#258, just archived to `_completed/`).
- Backend: ~31.8K non-test Go LOC / 111 files / 25 packages; ~37.5K test LOC. Frontend: ~27.5K non-test
  LOC. The `frontend/src/compiler/` TS mirror is ~8.8K LOC / 21 files.
- **Verified:** `GOOS=js GOARCH=wasm go build ./internal/{localcompile,compiler,renderer,validator,allocator,artifacts,normalize,render,model,naming,linkid,allocconst}`
  succeeds on the current tree. `internal/render` imports `wgctrl/wgtypes` (types only — wasm-compatible;
  the keygen/`crypto/ecdh` seam + `keygen_equivalence_test.go` exist).
- CI: 6 required checks, branch protection LIVE on `main` (a `ci.yml` `name:` edit must update
  protection in the SAME PR). Toolchain go1.26.5 (`GOTOOLCHAIN=local` locally). `govulncheck` is a
  required gate.

## Must-read references

- **This subject's rationale:** `docs/design/framework-refactor-proposal-2026_07_13.md` (assessment +
  the 11-phase framework + alternatives/scoreboard + invariant mapping).
- **Project invariants:** `PRINCIPLES.md`.
- **Architecture (the intended design to preserve):** `docs/spec/README.md` (the reading guide);
  `docs/spec/compiler/io-contract.md` (the frozen `localcompile.Compile` cross-language contract);
  `docs/spec/controller/{persistence,deploy,signing,key-custody,agent,controller-api}.md`;
  `docs/spec/operations/deployment-topology.md` (the `//go:build airgap` boundary).
- **The god-files (per-phase Read-first lists name exact lines):** `internal/controller/filestore.go`
  (2307), `internal/controller/store.go` (735), `internal/controller/memstore.go` (957),
  `internal/controller/compile.go` (990), `internal/renderer/script.go` (1999),
  `internal/compiler/peers.go` (1385), `internal/validator/semantic.go` (1201),
  `internal/api/handler_deploy.go` (770); `frontend/src/stores/controllerStore.ts` (2086),
  `frontend/src/api/controllerClient.ts` (1410), `frontend/src/compiler/*`.
- **Test gates:** `internal/conformance/` + `internal/localcompile/testdata` goldens; the drift manifest;
  `internal/regression/`; `frontend` vitest + the `*.conformance.test.ts` mirror; `frontend/e2e`.
- **Memory:** `review-each-pr-before-merge`, `worktrees-when-review-workflows-run`,
  `frontend-ci-uses-tsc-b`, `bootstrap-repins-operator-cred-by-default`, and the shipped-subject notes.

## Standing rules

Per `PRINCIPLES.md` + memory: per-PR independent workflow review → fix → re-review; reviews
checkout-free; branch work in isolated worktrees when a background review runs; verify locally
(`go build/vet/test` incl. `-race` + airgap; `cd frontend && npm run build` [`tsc -b`] + vitest + lint;
`gofmt -l`) before pushing; **regenerate goldens to a REVIEWED byte-diff, never a blind `-update`**; a
`ci.yml` display-name change updates branch protection in the same PR; no `--no-verify`, no amends, no
force-push.

## Decisions log (2026-07-13)

1. **Assessment method + proposal shape ACCEPTED as-is** (owner). 24-agent sweep + 21-agent judged
   panel; the recommended framework "WASM-Unified Core + Machine-Gated Paydown" (11 phases) is adopted.
2. **The WASM lever is kept** (not the shrink-the-mirror consolation) because feasibility was VERIFIED,
   not asserted. The panel's three WASM-judge holes are baked in as fixes: the permanent WASM-vs-golden
   gate (Phase 3); re-home keygen KAT + coverage-floor before deleting `conformance/` (Phase 5); a
   gated, harness-re-pointed airgap retirement (Phase 9).
3. **Phase 9 (airgap-server retirement) PROCEEDS** (owner): the `-tags airgap` server is build/test-only,
   never hosted → deleting the anonymous compile/validate/export/deploy-script routes is an
   attack-surface win. (If that ever changes, drop Phase 9 and keep the airgap build permanently.)
4. **`Node.Name → Node.ID` stable-artifact-identity refactor stays DEFERRED / out of scope** (owner) —
   it changes generated bytes on live fleets and stresses invariants 2 + 8; it deserves its own gated
   subject if ever done.
5. **Structure: ONE subject, plan-0 … plan-10** (one plan per phase). Natural split points if the owner
   later wants separate subjects: `{0–2 hygiene+hardening}` / `{3–5 WASM cutover}` / `{6–8,10 paydown}` /
   `{9 airgap retire}`. **The WASM strategic-bet gate is after Phase 2** — Phases 0–2 stand alone even if
   WASM is never adopted.
6. **Two safety corrections from the judge votes are locked:** the `Store` telemetry reconciliation
   PRESERVES FileStore's deliberate volatile overlay (do NOT force MemStore's durable-write — a
   regression two proposals stated backwards); and the `ShellToken` seam is honestly typed
   (`ShellQuoted`/`ShellRaw`), not magically context-aware.
7. **Plans-only at draft;** execution on the owner's go. This plans PR merges on CI (docs-only); the full
   multi-agent review regime applies to every EXECUTION PR.

## Milestones (one plan file each)

Ordered by debt-eliminated ÷ risk. See each `plan-N` for concrete moves, Read-first lists, verification,
and stop-loss. Every phase is independently shippable + green + pausable.

- **plan-0 — Ratchet + zero-behavior hygiene.** Install the enforcement spine (arch-test import ratchet +
  FE `no-restricted-import`, allow-list only shrinks) and clear actively-harmful debt (lying file names,
  false Go doc/header comments, `deployMode.ts` unifying the engine flags, the green-by-invisibility
  vitest dir-allowlist). Zero behavior change / zero golden diff. **Near-zero risk.** (The `sysctl.go` CJK
  comment cleanup — a RENDERED-output change — moved to plan-6, which makes it Go-only + cheap.)
- **plan-1 — Pre-WASM contract hardening.** Fold `normalize.HealCollidingPins` INTO `localcompile.Compile`
  (close the confirmed airgap silent-heal gap; + a colliding-pin fixture); evict **`model.Condition` only**
  into `internal/runtimecontract` (the mimic types stay in `model` — the pure-core renderer consumes them);
  hoist `51820` into `allocconst`. **Low–med risk.** (The `artifacts/export.go` single-source was split to
  **plan-1.5** during execution — a distinct custody concern.)
- **plan-1.5 — Single-source the export bundle file-set (custody).** Make `export.go`'s written / listed /
  checksummed / signed all derive from ONE `BundleFiles` source (a `bundleFileMode` helper + iterate the
  map + `allFiles` = sorted keys), so a member can never ship written-but-unlisted (unsigned). Checksummed
  set byte-identical; `manifest.json`'s `files` becomes deterministically sorted. **Low risk.**
- **plan-1.6 — Lock the air-gap handler pre-heal (regression test).** A handler-level regression test
  proving the plan-1 air-gap `airGapRequest` pre-heal actually heals a colliding-pin topology to a
  SUCCESS (vs the loud rejection the compiler's safety net gives), so the fix cannot silently regress to
  the original divergence. (The conformance fail-fixture `heal-collision-reenable` already locks the
  compiler's loud-reject half — unchanged.) **Low risk.**
- **plan-2 — Stateful god-file splits (no logic change).** `filestore.go` → io/audit/telemetry;
  `compile.go` → stage/promote/subgraph/preview/manualnode; api handlers → real homes;
  `controllerStore.ts` → slices (persist.ts = ONE custody gate); `controllerClient.ts` → per-domain
  services. Pure-core mirror files (`semantic.go`, `peers.go`) DEFERRED to plan-5. **Low risk.**
- **plan-3 — WASM add-alongside + PERMANENT gate.** `cmd/wasm` + `web/yaog.wasm`; `wasmEngine.ts` behind
  a flag; a headless CI gate asserting WASM == Go golden == TS over the full corpus; pin `wasm_exec.js`
  to the toolchain. Additive; TS stays default. **Med risk.**
- **plan-4 — Flip WASM default + soak.** Default the local engine to WASM; TS a one-flag fallback for one
  release; the three-way gate stays green. **Med, reversible.**
- **plan-5 — Delete the TS twin + shed deps + free the pure-core splits.** Delete `frontend/src/compiler/`
  (~10.6K) + `internal/conformance/` (after re-homing keygen KAT + coverage-floor; the WASM gate is the
  sole oracle); drop `@noble/curves`, `@noble/hashes`, `jszip`; NOW split `semantic.go` + `peers.go`
  (no parity tax; the 233 line-refs die with the files). Update branch-protection check names in the same
  PR that removes the conformance job. **Med, irreversible — after WASM soaked.**
- **plan-6 — Go-only renderer hygiene.** `go:embed` the install/uninstall/deploy templates as
  `render/templates/*.sh.tmpl`; reduce `script.go`/`deploy.go` to config-struct builders; introduce the
  `ShellToken` seam (`ShellQuoted`/`ShellRaw` + `field_safety_test`); restore `shellcheck`; clean the
  deferred `sysctl.go` CJK comment fragments. **Low–med.**
- **plan-7 — Error framework.** `api/adapter.go` (method-guard + auto-`identity()` + coded return) +
  `api/errmap.go` (central sentinel→`apierr.Code` table); migrate the 40 handlers + top controller/agent
  relay sites onto coded-at-source errors. **Low risk.**
- **plan-8 — Store behavioral core (KEYSTONE, highest risk, LAST).** `kv.go` (~10-primitive port) +
  `storecore.go` (every custody/allocation rule ONCE); `memkv`/`filekv` become thin adapters; PRESERVE
  FileStore's volatile-telemetry-overlay semantics as the shared model; expand-then-retire the compat
  test; gate merge on a real-host keystone-rotation/restart + passkey custody smoke. **Highest risk.**
- **plan-9 — Airgap-server retirement (gated).** Re-point `cmd/e2eserver` + `internal/dast` at the
  WASM/controller path FIRST; then delete `handler_airgap.go`/`airgap_routes.go`/`airgap_stubs.go`,
  collapse to one server build, drop the `backend` local-engine escape hatch, fix the boot-fail message.
  **Med, irreversible — after WASM is the proven local engine.**
- **plan-10 — FE polish + drift lock-in.** `ui/Field.tsx` across the 51 form sites; finish stores/services
  slicing; add the drift gate (shared struct via codegen OR `drift_manifest` entry + test) for the
  controller wire DTOs + the `omitempty`/`PIN_FIELDS`/`*_OMITEMPTY` lists; dead-code sweep. **Low risk.**

## Insertion-point markers (likely plan-N.5 triggers)

- **plan-3.5** if a `GOARCH=wasm` determinism quirk (map iteration reaching output, a stdlib path
  diverging under js/wasm) surfaces in the three-way gate — characterize + pin it before proceeding to
  the flip; NEVER delete the TS twin until the gate is green + soaked.
- **plan-5.5** if deleting `conformance/` orphans a gate other than keygen-KAT / coverage-floor (e.g. a
  fixture consumed cross-package) — re-home it first; the WASM-vs-golden gate must remain the sole,
  fail-closed compiler oracle.
- **plan-8.5** if the `storecore` extraction reveals a custody/anti-rollback rule that MemStore and
  FileStore implement with a genuine (not incidental) semantic difference beyond telemetry — STOP,
  surface it, reconcile with the owner before collapsing (this is the fleet-stranding-risk phase).
- **plan-9.5** if an e2e/DAST harness cannot be cleanly re-pointed off the airgap server — keep the
  airgap build and defer Phase 9 rather than strand the harness.

## Closure criteria

- [ ] All 11 phases merged (or the owner explicitly parks Phase 9 / a later phase), each
      workflow-reviewed → fixed → re-reviewed → CI-green.
- [ ] The permanent WASM-vs-golden gate is a required check; `frontend/src/compiler/` +
      `internal/conformance/` are deleted; `@noble`/`jszip` are gone from `package.json`.
- [ ] `internal/arch/layers_test.go` + the FE import ratchet + the `ShellToken` `field_safety_test` +
      the wire-DTO/`omitempty` drift gate are all green required checks.
- [ ] The `Store` is one behavioral core; the compat test is retired to its residual; a real-host
      keystone-rotation + passkey custody smoke passed (owner).
- [ ] Every hard invariant above is demonstrably preserved (goldens byte-verified; no fleet regression).
- [ ] STATUS + memory closeout; subject archived to `_completed/`.

## Plan status table

| # | Plan | Status | PR |
|---|------|--------|-----|
| 0 | Ratchet + zero-behavior hygiene | ✅ merged | #260 |
| 1 | Pre-WASM contract hardening | ✅ merged | #261 |
| 1.5 | Single-source the export bundle file-set (custody) | ✅ merged | #262 |
| 1.6 | Lock the air-gap handler pre-heal (regression test) | ✅ merged | #263 |
| 2 | Stateful god-file splits (no logic change) | ✅ merged | #264 |
| 3 | WASM add-alongside + PERMANENT gate | ✅ merged | #266 |
| 4 | Flip WASM default + soak | ✅ merged | #270 |
| 5 | Delete the TS twin (deps + conformance re-home) | ✅ merged | #271 |
| 5b | Pure-core splits (semantic.go→5, peers.go→3, orientation extracted) | ✅ merged | this PR |
| 6 | Go-only renderer hygiene (ShellToken seam) | 🔄 in review | this PR |
| 7 | Error framework (handler adapter + sentinel→code table) | ✅ merged | #265 |
| 8 | Store behavioral core (KEYSTONE, last) | ✅ merged | #269 |
| 9 | Airgap-server retirement (gated) | pending | — |
| 10 | FE polish + drift lock-in | pending | — |
