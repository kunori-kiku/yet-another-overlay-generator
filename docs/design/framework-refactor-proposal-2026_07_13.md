# YAOG Framework Assessment & Refactor Proposal

<!-- authored: 2026-07-13 -->
<!-- method: 24-agent repo-wide debt sweep + 21-agent judged design panel; WASM feasibility verified locally -->

> **What this is.** A repository-wide assessment of code-quality / framework debt, and a proposed
> refactor framework to eliminate it. Produced by two multi-agent workflows: a 24-agent sweep (14
> subsystem readers + 8 cross-cutting lenses → synthesis → skeptic pass) and a 21-agent design panel
> (5 competing target architectures, each scored by a 3-judge rubric, then synthesized). The
> load-bearing feasibility claim (the pure pipeline compiles to `js/wasm`) was verified directly.
>
> **This is a proposal, not accepted work.** No code has changed. It is written to be turned into
> `implementation_plans/` subjects phase-by-phase, at the owner's pace, in the existing rc cadence.

---

## Executive summary

YAOG is a **well-architected system with a clean, invariant-respecting pure core wrapped in an
under-decomposed, duplication-heavy stateful shell. This is NOT a rewrite situation.** The boundaries
that matter for security — the `//go:build airgap` split, the pure-vs-stateful quarantine, the auth
chokepoint, `Store`-as-sole-persistence-gateway — are real, documented, and should be preserved and
*hardened*, not torn out. The debt is concentrated and mostly **mechanical**.

The single largest structural fact is the **~17K-LOC dual Go/TS compiler**: two full implementations of
one algorithm kept byte-identical by a ~2–3K-LOC conformance apparatus. It is invariant-justified
(in-browser local mode with no backend) and conformance-gated — so it is *intentional-but-costly*, not
a bug — but it imposes a permanent six-way lockstep tax on every compiler change and the TS mirror
already "wags the dog."

**Recommended framework: _WASM-Unified Core + Machine-Gated Paydown_.** Radical at the root, fully
incremental in execution:

- **Eliminate the dual compiler at its root** by compiling the *existing* pure Go pipeline to
  `GOOS=js GOARCH=wasm` and running that in the browser. "local == controller" becomes a **compile-time
  property**, gated forever by a headless WASM-vs-golden check — instead of a hand-maintained mirror.
  **Feasibility verified: the pure pipeline builds clean to `js/wasm` today** with the local go1.26.4
  toolchain, and the keygen path is wasm-compatible.
- **Convert every remaining boundary from convention into a machine-gate**: an arch-test dependency
  ratchet (pure core imports nothing outward), a `ShellToken` type on the render→root-shell seam, a
  drift gate for the ungated wire-DTO / `omitempty` mirrors, and single-sourced field/file lists.
- **Pay down the stateful god-objects along seams that already exist**, with the highest-risk move —
  giving the `Store` a single shared behavioral core — sequenced **last**, behind its own file splits
  and an expanded custody smoke.

Every phase is an independently shippable, green, pausable PR. The two directory-reorg proposals
(five-domain verticals; concentric `git mv` relocation) were **rejected by the panel as churn-theater**
— the clarity comes from deduplication and machine-gates, which an arch-test delivers *in place*.

---

# Part A — The assessment

### Your three questions, answered

**Is the framework logically clear?** *Split-brain.* The pure compile pipeline
(`model → validator → allocator → compiler → renderer → artifacts`, via the single `localcompile`
façade) is genuinely clear and legible. Clarity then falls off a cliff on the stateful side: the
`Store`, the compile-and-stage orchestrator, the agent self-update lifecycle, and the two frontend
stores each fuse 6–10 concerns with the algorithm smeared across free functions and call-site ordering
rather than owning types. A newcomer can reason about *how a topology compiles*; they cannot reason
about *how a deploy stages, promotes, and reconciles* without reading four files at once.

**Are the boundaries clear?** The **macro** boundaries are exemplary. The debt is entirely at the
**seams**: `internal/model` (a pure leaf) now houses agent/controller runtime contracts to dodge import
cycles; `normalize/heal` lives *outside* the pipeline and is **silently missing on the airgap compile
path** (a colliding-pin topology compiles differently on the anonymous vs controller path); the Go
`omitempty` wire contract is hand-mirrored inside a Zustand store; controller wire DTOs are hand-mirrored
to TypeScript with **no drift gate**; and shell-safety is enforced by per-site *convention* with a
dangerous raw/quoted twin field (`NodeName` vs `NodeNameQuoted`) at the render→root-shell seam.

**Is the file structure logical?** *Bimodal.* The pure core is cleanly decomposed. The stateful half is
dominated by god-files. The frontend has **no consistent organizing axis** (entity/feature/route/singleton
folders coexist; `lib/` is a 35-file junk drawer). `render`/`renderer` differ by one character. Names lie
(`handler_deploy.go` holds registry/audit/enrollment handlers). Plus **test-corpus sprawl** (two golden
corpora byte-freeze the *same* 25 topologies; a vitest dir-allowlist silently drops misplaced tests).

### The debt, ranked

| # | Theme | Severity | The core of it |
|---|-------|----------|----------------|
| 1 | **`Store`: two hand-mirrored impls, zero shared logic** | 🔴 critical | 46-method interface; `MemStore` (957) + `FileStore` (2307) *independently re-encode every custody/allocation rule*; a 1559-line compat test polices the drift; telemetry has **already diverged**; `MemStore` (all tests) doesn't exercise the overlay path that ships. |
| 2 | **God-objects at every stateful/UI boundary** | 🔴 critical | `compile.go`, `filestore.go`, `controllerStore.ts` (2086), `controllerClient.ts`, `peers.go` (a ~620-line function) each fuse 6–10 concerns along seams that already exist. |
| 3 | **Copy-paste from unextracted abstractions** | 🟠 high | 6-tuple orientation swap 4–5×; link-grouping loop 3×; SNAT rule 4×; 40 handlers hand-roll the same guard+identity+relay preamble; a 28-branch `errors.Is` ladder across 7 files; a form-field markup 51×. |
| 4 | **Hand-mirrored contracts with no drift gate** | 🟠 high | Wire DTOs defined 3× under 3 naming conventions; `omitempty` re-implemented in the store; five `*_OMITEMPTY` lists. *(They round-trip fine today — the debt is the missing gate, which has bitten repeatedly.)* |
| 5 | **The dual Go/TS compiler (~17K LOC)** | 🟠 high | *Intentional-but-costly.* A permanent six-way lockstep tax; the TS mirror wags the dog (Go precomputes booleans because `template.ts` has no `eq`). |
| 6 | Pure/stateful seams leak | 🟡 med | model houses runtime contracts; **heal missing on airgap**; a pure policy rides a render struct; FE stores double as the service layer. |
| 7 | Error-mapping & fail-open by convention | 🟡 med | 71 sites degrade to a generic `internal_500`; the agent uses 0 typed codes vs 143 `fmt.Errorf`; fail-open governed by 122 comments + 30 silent `_ =` writes. |
| 8 | Comment archaeology | 🟡 med | 715 `plan-N`/PR/beta refs in production comments; 233 hardcoded Go line-number citations rotting in the TS mirror; `sysctl.go` ships half-deleted CJK fragments into every node's `/etc/sysctl.d`. |
| 9 | File/folder taxonomy has no axis | 🟡 med | `render`/`renderer`; lying names; `lib/` junk drawer; FE folder chaos. |
| + | **Shell-safety is a SEAM (root-RCE class)** | 🟠 high | No structural guard on "no unescaped user text to a root shell"; `shq` applied at only ~20 hand-picked sites; the `NodeName`/`NodeNameQuoted` twin is command-substitution injection one identifier away. |
| + | **`export.go` triplicates the bundle file-set** | 🟠 high | A file written-but-unlisted ships **unsigned/unchecksummed** (tamper surface); listed-but-unwritten fails `sha256sum -c` on the node. |
| + | **Test-corpus sprawl** | 🟡 med | Two golden corpora over the same 25 topologies; vitest dir-allowlist = green-by-invisibility; 10 fixture sets. |

### What to preserve (intentional, do not "fix")

The security-load-bearing macro boundaries (airgap tag, purity quarantine, custody chokepoint,
`Store`-as-gateway) are correct. The dual compiler is a *defensible* choice — the recommendation removes
it only because a single-source alternative (WASM) is now verified feasible, not because the mirror is
wrong. Two facts the skeptic pass corrected and that this proposal respects: the FE `@noble` dependency
is **not** a minimal-deps-core violation (that invariant scopes to the pure Go core); and the wire DTOs
have **not** wire-drifted (identical JSON tags round-trip) — the debt is the missing gate.

---

# Part B — The recommended framework

## _WASM-Unified Core + Machine-Gated Paydown_

**Organizing law** (grafted from the runner-up "Kernel + Neutral-Spec" proposal): *duplication is
permitted only where an invariant forces it, and every forced duplication is machine-gated by codegen
or a red-build drift check — never held by convention.* Today's codebase built exactly this discipline
for the compiler mirror (the conformance corpus) but never extended it to the wire DTOs, the shell
seam, or the field lists. This framework finishes the job — and, where WASM makes the duplication
*unnecessary*, deletes it.

### Target tree (the deltas that matter)

```
cmd/
  wasm/                         NEW  GOOS=js GOARCH=wasm main; syscall/js shim over localcompile → web/yaog.wasm
internal/
  runtimecontract/              NEW  agent↔controller contracts (Condition, MimicOutcome, breadcrumb) evicted from model
  model/                        topology schema ONLY (leaf restored)
  allocconst/                   + WGListenPortBase (51820) hoisted here — one source the allocator + WASM read
  render/  (absorbs renderer)   kills the 1-char twin; *.sh.tmpl go:embedded; ShellToken types every root-shell field
  localcompile/                 the frozen contract; normalize.Heal folded IN (closes the airgap silent gap)
  artifacts/export.go           one BundleFileSet = the sole write-plan AND checksum manifest
  controller/
    store.go                    46-method interface (chokepoint, unchanged externally)
    kv.go  storecore.go         NEW  ~10-primitive KV port + the behavioral core (every custody rule ONCE)
    memkv.go filekv.go          thin adapters; filestore.go 2307 → filestore_{io,audit,telemetry}.go
    compile_{stage,promote,subgraph,preview,manualnode}.go   (compile.go 990 split)
  api/
    adapter.go  errmap.go       NEW  typed handler adapter + central sentinel→apierr.Code table
    handler_{topology,stage_promote,enrollment,audit,rekey}.go   (handler_deploy.go's 13 handlers → real homes)
  arch/layers_test.go           NEW  go/packages import-rule ratchet (allow-list only shrinks)
  DELETE internal/conformance/  at Phase 5 (after re-homing keygen KAT + coverage-floor; WASM gate becomes the oracle)
frontend/src/
  wasm/wasmEngine.ts            NEW  loads web/yaog.wasm + version-pinned wasm_exec.js
  DELETE compiler/              at Phase 5 (~10.6K LOC: validator.ts, peers.ts, renderers/*, escape.ts, keygen.ts, …)
  services/                     controllerClient.ts 1410 → per-domain modules
  stores/controller/            controllerStore.ts 2086 → {auth,fleet,deploy,keystone,settings,sync} slices; persist.ts = ONE custody gate
  ui/Field.tsx                  NEW primitive (kills the 51× form-field copy-paste)
  lib/deployMode.ts             ONE validated descriptor (collapses VITE_LOCAL_ONLY + VITE_YAOG_LOCAL_ENGINE)
  package.json                  DROP @noble/curves, @noble/hashes, jszip (subsumed by crypto/ecdh + archive/zip in WASM)
```

### The migration — 11 independently-shippable phases

Ordered by **debt-eliminated ÷ risk**, front-loaded with near-zero-risk wins; the radical WASM cutover
is add-alongside-then-flip-then-delete; the custody-critical `Store` core is **last**.

| Phase | Goal | Risk | Ships because… |
|------:|------|------|----------------|
| **0** | **Ratchet + hygiene** — arch-test + FE `no-restricted-import` (allow-list only shrinks); fix lying names; delete false comments + the `sysctl.go` CJK fragments; `deployMode.ts`; recursive vitest glob; merge the two golden corpora | near-zero | pure rename/delete + new tests; nothing executes differently |
| **1** | **Pre-WASM contract hardening** — fold `Heal` into `localcompile` (+ a colliding-pin fixture); evict runtime contracts from `model`; hoist `51820`; single-source `export.go` | low–med | small green PRs; conformance corpus regenerated to a *reviewed* byte-diff; closes a real correctness divergence + an unsigned-file surface |
| **2** | **Stateful god-file splits** (no logic change) — `filestore`, `compile`, api handlers, `controllerStore` slices, `controllerClient` services | low | mechanical motion; each split an independent PR *(pure-core mirror files deferred to Phase 5)* |
| **3** | **WASM add-alongside + PERMANENT gate** — `cmd/wasm` + `wasmEngine.ts` behind a flag; a headless CI gate asserts **WASM == Go golden == TS** over the full corpus | med | additive; TS stays default; the permanent gate answers "never trust same-source==same-target" |
| **4** | **Flip WASM default + soak** — WASM is the local engine; TS a one-flag fallback | med, reversible | one flag flip; a working local engine throughout |
| **5** | **Delete the TS twin** (~10.6K LOC) + `conformance/`; shed `@noble`/`jszip`; **now** split `semantic.go`/`peers.go` (no parity tax) | med, irreversible | WASM==Go is gate-enforced; the 233 line-refs die with the file; re-home keygen KAT first |
| **6** | **Go-only renderer hygiene** — `go:embed` the bash templates; `ShellToken` types the shell seam; restore `shellcheck` | low–med | cheap now (no TS mirror to keep byte-identical); goldens + shellcheck catch drift |
| **7** | **Error framework** — `adapter.go` + `errmap.go`; migrate the 40 handlers + top relay sites onto coded-at-source errors | low | `identity()` becomes structurally un-skippable; FE i18n join unchanged |
| **8** | **`Store` behavioral core (KEYSTONE, LAST)** — `kv.go` + `storecore.go` implement every rule once; `MemStore` finally exercises the shipping telemetry overlay | **highest** | interface identical; expand-then-retire the compat test; gated on a real-host keystone-rotation + passkey custody smoke |
| **9** | **Airgap-server retirement (gated, may defer)** — re-point `e2eserver`/`dast` first, then delete the anonymous compile/validate/export routes | med, irreversible | sequenced after WASM is the proven local engine so there's never a window without one |
| **10** | **FE polish + drift lock-in** — `Field.tsx`; finish slicing; **drift gate** for wire DTOs + `*_OMITEMPTY` lists; dead-code sweep | low | the last silent-drift class becomes fail-closed |

### How every hard invariant is preserved

1. **Deployable configs** — WASM runs the *same* Go renderer as the controller → byte-identical **by
   execution**, and a permanent headless gate executes `yaog.wasm` against the Go golden corpus forever.
2. **Allocation stability** — same Go allocator in WASM; deterministic integer/string output (no floats,
   no timestamps); the run-twice golden is inherited; the `Node.ID`-identity change that *would* threaten
   this is explicitly deferred out of scope.
3. **No unescaped user text to a root shell** — *strengthened*: after Phase 5 there is one Go renderer;
   Phase 6's `ShellToken` types every render→shell field. Honest framing: `text/template` is
   context-blind, so the guarantee is "this value passed through `shq`," enforced by the type +
   `field_safety_test` + distinct `ShellQuoted`/`ShellRaw` constructors (not a naive twin deletion).
4. **Backend sole port authority** — same allocator; `51820` single-sourced; WASM local mode is
   design-*preview* only and never deploys/stages.
5. **Signed-update custody** — untouched by WASM (deploy/staging/self-update stay server-side); the
   `storecore` keeps the promote/served-vs-staged predicate + anti-rollback in one function; the single
   `BundleFileSet` makes written == listed == signed == checksummed *structurally* true.
6. **Zero-knowledge key custody** — WASM keygen uses the already-proven `crypto/ecdh` seam; one keygen
   impl replaces the twins; the FE `partialize` allowlist stays one gate in `persist.ts`.
7. **Pure/minimal-dep core** — the WASM shim adds zero Go deps (stdlib only); the FE *sheds* `@noble` +
   `jszip`; `arch/layers_test.go` makes the quarantine a red build.
8. **Backward-compat topologies** — `model` schema unchanged; the split is a type-move with JSON tags
   unchanged.
9. **Two-deployment story** — *strengthened*: local mode becomes a static SPA + `yaog.wasm` (more
   backend-free than today); the airgap server is retained as the e2e/DAST boot target until Phase 9,
   whose retirement is **gated on owner confirmation** that no one runs it as a hosted service.

### Explicitly out of scope (honest boundaries)

- **The i18n Go-validator-code ↔ EN/ZH catalog coupling survives** — WASM unifies *compute*, not the
  message layer; `i18n_catalog_sync_test.go` stays a required gate.
- **`Node.Name`-derived artifact identity** (rename == redeploy; the 3-namespace collision subsystem) is
  **deferred** — fixing it changes generated bytes on live fleets; if ever done it must freeze existing
  nodes' keys and give only *new* nodes stable-ID identity, as its own gated migration.
- **Architectural (non-clean-code) items are untouched**: FileStore single-file SPOF / no-HA,
  bootstrap-TOFU, the pinned-endpoint option.
- A full FE component-tree re-foldering is out of scope (only stores/services slicing + `Field` land).
- The FE regex-based error framework stays semantically distinct from Go `apierr`.
- **Accepted residual debt**: a new `wasm_exec.js` ↔ toolchain pin (far cheaper than the six-way lockstep,
  but real given govulncheck-forced bumps); and a WASM live-preview latency characterization (mitigate
  with debounce; controller-mode preview stays server-side).

### Before → after

> **Today:** a split-brain tree held together by a ~17K-LOC hand-mirrored compiler + a ~2–3K conformance
> apparatus + a 259-line drift manifest + 233 rotting line-refs + a JS-escaped bash twin, with real but
> *reviewer-enforced* boundaries.
>
> **End state:** the compiler exists **once** (Go), run in the browser as `web/yaog.wasm`, parity gated
> forever by a headless check. The mirror + its lockstep tax + `@noble`/`jszip` + the anonymous airgap
> attack surface are gone. Every remaining boundary is a **red build, not a convention** (arch-test
> ratchet; `ShellToken`; drift gate; single-source lists). The `Store` is one behavioral core over a KV
> port, so `MemStore` finally exercises the shipping path. **A newcomer can point to exactly one place
> for the compile algorithm, the persistence rules, the sentinel→HTTP-code map, the shell-escape seam,
> and the bundle file-set.** The macro boundaries are preserved and hardened; the mechanical debt is
> dissolved.

---

# Part C — Alternatives considered

Five complete target architectures were generated from distinct philosophies and each scored by a
3-judge rubric (debt-eliminated / invariant-safety / migration-incrementality / clarity-payoff /
effort-realism, /10):

| Proposal | Score | Verdict |
|----------|:----:|---------|
| **Unify-compiler-via-WASM** | **8.07** | **Chosen as the spine.** Attacks the named strategic-duplication lever at the root; feasibility verified. Judges flagged 3 correctable holes (all grafted-in). |
| Kernel + Neutral-Spec (machine-gate every forced mirror) | 8.00 | Its *law* became the organizing principle; its DTO/shell/list gates are grafted. |
| Layered-Clean (enforced dependency rule + ports/adapters) | 7.87 | Its **arch-test ratchet** is grafted as Phase 0 — minus its concentric `git mv` relocation (rejected as churn). |
| Paydown-in-Place (amortize on the existing architecture) | 7.87 | Its **migration discipline** governs the whole sequence (front-load by risk; Store last); it also corrected the telemetry-reconciliation direction. |
| Domain-Vertical Reorg (five bounded domains) | 7.20 | Its single `BundleFileSet` + `render`/`renderer` merge are grafted; the **five-domain directory reorg is rejected** — the clarity lives in the dedup, not the verticals, which create new custody-seam ambiguity. |

The synthesis is deliberately not any single proposal: it takes the highest-scoring as the spine and
grafts the best idea from each runner-up, while rejecting the two directory-reorg framings the judges
judged to be churn without invariant payoff. Two safety corrections the votes surfaced are baked in:
preserve FileStore's deliberate volatile telemetry overlay (do **not** force MemStore's durable-write —
a regression two proposals stated backwards), and the honest `ShellToken` framing (typed, not magically
context-aware).

---

# Part D — Executing this

**Sequencing for a budget-constrained solo owner.** Phases 0–2 are near-zero-risk and independently
valuable — they can ship in the current rc cadence with no commitment to the WASM cutover. Treat each
phase (or a small group) as one `implementation_plans/` subject drafted with `draft-implementation-plan`
and executed with the existing per-PR review discipline. The WASM decision gate is **after Phase 2**:
Phases 0–2 stand on their own even if WASM is never adopted; Phases 3–5 are where the strategic bet is
placed (and are reversible until Phase 5's deletion).

**Two decisions needed from the owner before those phases:**

1. **Phase 9 (airgap-server retirement):** does anyone run the `-tags airgap` server as a *hosted*
   local-design service? If yes, invariant 9's edge is touched and Phase 9 should be dropped, keeping the
   airgap build permanently. If it is only ever an e2e/DAST boot target and a build-time attack-surface
   deletion, Phase 9 proceeds.
2. **`Node.ID` identity (deferred):** confirm this stays out of scope. Fixing rename==redeploy is
   genuinely valuable but changes live-fleet bytes and is its own gated subject — not part of this
   debt-paydown.

**Immediate no-regret starting point:** Phase 0. It installs the enforcement spine (the arch-test that
makes every later boundary claim a red build) and clears actively-harmful debt (the `sysctl.go` CJK
fragments shipped to production hosts, the false doc headers, the green-by-invisibility vitest config)
with zero behavior change.

---

## Appendix — methodology & confidence

- **Sweep:** 22 analysis agents (14 subsystem readers + 8 cross-cutting lenses) over the full repo,
  each grounding findings in `file:line` evidence and classifying debt vs intentional-but-costly vs
  intentional-ok; a synthesis deduped 148 raw findings into ranked themes; a skeptic pass corrected 6
  missing themes + 4 overstatements (folded into Part A).
- **Design panel:** 5 target architectures × a 3-judge rubric + a synthesis, grounded in the corrected
  inventory.
- **Verification:** the load-bearing WASM claim was checked directly —
  `GOOS=js GOARCH=wasm go build ./internal/{localcompile,compiler,renderer,validator,allocator,artifacts,normalize,render,model,…}`
  succeeds on the current tree.
- **Confidence:** high on the assessment (evidence-grounded, cross-checked); high on Phases 0–2 and
  6–8/10 (mechanical, invariant-preserving, standard practice); the WASM cutover (3–5) is verified
  feasible but carries the normal risk of a runtime swap — which is exactly why it is add-alongside +
  permanently-gated + reversible-until-deletion rather than a rewrite.
