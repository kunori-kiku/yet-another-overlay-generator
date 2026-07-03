# link-directionality — outline

> Durable spine for the `link-directionality-2026_07_03` subject. A fresh session picks up the whole
> program from this file. Drafted 2026-07-03 via `draft-implementation-plan` (investigation by three
> parallel Explore agents + direct reads; owner checkpointed at preflight/post-flight — see the
> Decisions log). Ships as the interim **`v2.0.0-beta.18`**, after which `pre-rc1-hardening` plan-11
> cuts `v2.0.0-rc.1`.

## 1. Mission

Add a per-edge **`link_direction`** field (`""`≡`both` default / `forward`; no stored `reverse` —
D11: one spelling, the editor's "to(A)" choice flips the edge instead) so a
single-linked edge's suppressed side keeps its full `[Peer]` stanza (AllowedIPs, return traffic,
Babel routing) but never carries a dial `Endpoint` — killing the reverse-peer race in which the
auto-reverse peer dials the from-node's plain public IP, wins the WireGuard handshake when it boots
first, and permanently bypasses the operator's relay/accelerator path via endpoint roaming.

**Success criteria:**
- [ ] `link_direction` lands in the model + BOTH compilers + BOTH validators, byte-exact (drift
      manifest, golden corpora, i18n sync all green); default `both` compiles **byte-identical**
      (zero churn across all 20 pre-existing goldens).
- [ ] Every direction misconfiguration is a **loud validator error** (4 new codes after D11), never
      a silently dead link or a direction silently ignored by pair-folding.
- [ ] The panel exposes the field on edge click, labeled with real node names + arrows
      (`A ⇄ B` / `A → B` / `B → A`), with a directional canvas style; existing configs auto-convert
      (absence ≡ both) and garbage values sanitize to both on load.
- [ ] A realtunnel netns scenario proves the suppressed side emits no `Endpoint =` line AND the
      tunnel still forms from the inbound handshake.
- [ ] Docs: `edge.md` + `peer-derivation.md` + bilingual wiki updated.
- [ ] Shipped as reviewed **`v2.0.0-beta.18`** (owner promotes to Latest); owner fleet smoke handed
      off; then rc.1 (pre-rc1-hardening plan-11).
- [ ] Every PR independently workflow-reviewed (correctness / completeness / hygiene / structure) →
      fixed → re-reviewed clean → CI green before merge.

## 2. Principles (invariants the executor MUST respect)

Inherits `PRINCIPLES.md` (repo root) in full. Load-bearing for THIS subject, with negative space:

- **Allocation stability (superset rule)** `[STATED — PRINCIPLES.md]` **(HIGH).** Toggling
  `link_direction` moves ZERO allocated values (ports, transit pairs, link-locals, pins); only
  `Endpoint` lines may differ. Link identity is already direction-agnostic
  (`internal/linkid/linkid.go` PinKey). *Violation:* threading direction into Pass 1 / the
  allocator; making `CompiledPort` write-back direction-dependent.
- **Go↔TS byte-exact conformance** `[STATED — repo conformance harness]` **(HIGH).** Every semantic
  lands in both compilers/validators in the SAME PR — the drift manifest (`validator_codes`,
  `fe_field_lists.EDGE_OMITEMPTY`), the golden corpora, and `TestI18nCatalogSync`/`Parity` red a
  split immediately. *Violation:* a Go-only validator code; a TS-only sanitize rule that changes
  compile output.
- **Generated configs must be deployable** `[STATED — PRINCIPLES.md]` **(HIGH).** A direction with
  no possible dialer is a loud validator ERROR (the forward-no-endpoint rule), never a silently
  dead link.
  *Violation:* letting `forward` + empty `endpoint_host` compile (the forward peer only ever dials
  `edge.endpoint_host` — verified `peers.go:756-771`).
- **Backend is the sole port authority** `[STATED — PRINCIPLES.md]` **(HIGH).** The UI writes only
  `link_direction` (and, on the D11 "to(A)" flip, swaps from/to + mirrors pins + clears the stale
  endpoint fields); it never stamps ports.
- **Backward compatibility of persisted topologies** `[STATED — PRINCIPLES.md]` **(MEDIUM).**
  Absent field ≡ both (`omitempty`); prior localStorage/JSON loads + compiles byte-identically;
  unknown values sanitize to both on panel load.
- **Process invariants** `[STATED — PRINCIPLES.md §Execution discipline]` **(HIGH):** no
  shims/monkey-patches; structure-aware hygienic code; no scope compromise to close work; every PR
  independently reviewed then re-reviewed after fixes; reviews checkout-free
  ([[worktrees-when-review-workflows-run]]).

## 3. Current state of the world (2026-07-03)

- **Latest shipped:** `v2.0.0-beta.17` (GitHub Latest, tag on `907c0a5`; 29 assets). `main` green at
  `68ad08e`. Owner fleet smoke of beta.17 still owed (gates rc.1, not this subject).
- **This subject ships beta.18** (owner: "beta.18 first, then rc.1"). rc.1 = `pre-rc1-hardening`
  plan-11 (refresh `docs/spec/rc1/RC1-GATE.md`, tag; rc.1 self-promotes to Latest).
- **Execution pacing:** owner directed a **5-hour timer between plan-drafting and execution**
  (allowance refresh); plans execute after the wake-up.
- Each plan executes on its own feature branch off `main`; PRs merge via the normal gate
  (`go` / `frontend` / `conformance` / `frontend-e2e` / `realtunnel` / `security-scan`).
- Local toolchain: Go `$HOME/.local/go/bin` (go1.26.4); npm/node v20. CI is authoritative.

## 4. Must-read references

**Memory:** [[pre-rc1-hardening-shipped]] (beta.17 + the NAT/roaming diagnosis that seeded this),
[[review-each-pr-before-merge]], [[worktrees-when-review-workflows-run]],
[[frontend-ci-uses-tsc-b]], [[three-item-program-2026_06_25]] (mimic `remote=`/beta.14 context).

**Project docs:** `PRINCIPLES.md`, `docs/spec/data-model/edge.md`,
`docs/spec/compiler/peer-derivation.md`, `docs/spec/compiler/allocation-stability.md`.

**Reads from specs:** `model-validation`, `compiler-allocation`, `panel-design` (+ `render-keys`
for renderer context). `specs/README.md` stamp 2026-06-12 — current (< 30 days at draft).

**Production code (per-plan `Read first` lists carry exact line numbers):**
- Model: `internal/model/topology.go:157-233` ⇄ `frontend/src/types/topology.ts:82-112`;
  `frontend/src/stores/controllerStore.ts:126` (`EDGE_OMITEMPTY`).
- Compiler: `internal/compiler/peers.go:755-778,850-943` ⇄ `frontend/src/compiler/peers.ts:496-805`
  (forward endpoint :627-637, keepalive :639-646, reverse block :714-801).
- Validators: `internal/validator/code.go:66-77`, `schema.go:489-518`, `semantic.go` (siblings:
  `detectDuplicateEnabledEdges:807`, `validateSinglePrimaryPerPair:983`, `validateClientEdges:403`),
  `nat.go:42-109` ⇄ `frontend/src/compiler/validator.ts:33-136,643-708,1036-1090,1622-1727`.
- Sanitize: `frontend/src/lib/normalizeEdges.ts:71-139`; call sites
  `frontend/src/stores/topologyStore.ts:659` (loadTopology) + `:1028-1032` (onRehydrateStorage).
- Conformance: `internal/conformance/golden_test.go:23-54`, `drift_test.go:38-46`,
  `testdata/fail/15-schema-node-edge-field-rejects.json`,
  `internal/localcompile/testdata/contract/topologies/` (success corpus, shared),
  `02-parallel-primaries.json` (the explicit A→B + B→A pair fixture).
- Renderer (verified NO changes needed): `internal/renderer/wireguard.go:79-85` (omit-when-empty),
  `internal/renderer/script.go:1099-1102` (mimic skips endpoint-less peers).
- Controller (verified NO changes needed): `internal/controller/compile.go:650-655` (wholesale edge
  copy), `internal/normalize/pins.go:203-211` (heal touches only pins).
- UI: `frontend/src/components/design/aside/EdgeEditor.tsx` (select pattern :326-345; node
  resolution :56-58,:81-83; endpoint cluster :197-281),
  `frontend/src/components/canvas/CustomEdge.tsx` (:23-46 data, :119-222 pill, :224-239 markers),
  `frontend/src/components/canvas/TopologyCanvas.tsx:262-296` (flowEdges memo).
- Realtunnel: `test/realtunnel/scenarios_test.go:17-40,66-85`, `scenario.go:288-311`
  (`reverseEndpointPresent`), `.github/workflows/ci.yml:200-207` (additive tier).

**Test gates:** `go test -race ./...` + `-tags airgap`; conformance job (goldens + drift +
`TestI18nCatalogSync`/`Parity` + coverage floor + `npm run conformance`); `frontend` job
(`npm run lint && npm run build` — tsc -b); `frontend-e2e` (grep-invert visual corpus);
`realtunnel` (canary required; additive tier non-blocking); `security-scan`.

**Web URLs:** none required — WireGuard roaming semantics were established from first principles +
owner's live `wg show` evidence earlier in this program; no external claims are load-bearing.

## 5. Standing rules

See memory ([[review-each-pr-before-merge]], [[worktrees-when-review-workflows-run]]) and
`PRINCIPLES.md §Project-wide standards`. Non-obvious repo specifics: a new `validator.Code` needs
drift-manifest regen + `error.<code>` keys in BOTH `en.ts`+`zh.ts` + the TS Code/registry mirror +
a fail fixture (coverage floor); install-script/renderer changes need BOTH golden corpora regen
(not applicable here — renderer untouched); betas need manual `gh release edit --latest`.

## 6. Decisions log

| # | When | Question | Decision |
|---|---|---|---|
| D1 | pre-draft (owner) | Release path | **beta.18 first, then rc.1** (interim beta, owner smoke gates Latest/rc.1) |
| D2 | preflight (owner) | Feature design | Owner's spec verbatim: per-edge field, default doubly-linked, panel field on edge click labeled doubly/to(A)/to(B), existing configs auto-convert, sanitize on load |
| D3 | post-flight (owner) | Subject name | **`link-directionality`** |
| D4 | post-flight (owner) | Plan shape | **4 plans accepted** (core → panel UX → proof+docs → release); split-plan-1 rejected (a Go/TS split cannot stay CI-green) |
| D5 | post-flight (owner) | Scope boundary | **Confirmed:** NO per-edge reverse-endpoint host/port fields (draw the edge the other way + `forward`); NO keepalive changes; pinned-endpoint anti-roaming feature stays deferred to rc.2 |
| D6 | draft (executor call) | Wire values | `""`≡`"both"`, `"forward"`, `"reverse"` accepted (MimicFallback tri-state precedent); UI clears the field for doubly-linked so untouched exports stay byte-identical |
| D7 | draft (executor call) | Error vs warning | All 5 semantic rules are **errors** — same failure class as beta.17's require-explicit-host (owner chose loud errors for silently-dropped overrides) |
| D8 | draft (executor call) | Canvas visual for `both` | **Unchanged** (zero cosmetic churn); single-linked edges get a direction chip (`data-testid`) + dial-side arrow marker |
| D9 | draft (executor call) | Go-side sanitize | Go `normalize` intentionally NOT coercing invalid values — validators are the loud gate; panel-load sanitize covers the user path |
| D10 | post-approval (owner) | Execution pacing | Materialize + merge the plan folder now; **sleep 5 hours**; execute plan-1 on wake |
| D11 | mid-plan-1 (owner) | Reverse spelling | **Drop `reverse` from the model — one spelling.** The owner asked whether `reverse` canonicalizes to a flipped `forward` at compile time (worried about dual-spelling logic traps; two NAT special-branches had already materialized as evidence). Decision: single-linking is ALWAYS the drawn from→to direction (`""`≡`both` / `forward` only); the editor's "to(A)" choice performs an EXPLICIT flip (swap from/to + mirror the six pins — allocation-stable, link identity + interface names are direction-agnostic — + prefill endpoint_host from the dialed node's endpoint picker). Deleted vs the draft: 2 validator codes (reverse_unreachable, reverse_endpoint_ignored), both NAT special-branches, the forward-endpoint compiler gate. Trade-off accepted: a single-linked edge holds an explicit host copy (stale-snapshot warning covers drift) instead of dynamically following node endpoints |
| D12 | mid-plan-1 (executor) | Discovered adjacent defect | The TS validator never mirrored Go's `validation_edge_mimic_fallback_invalid` (Go 102 vs TS 101 codes) and no fixture exercised it — a bad `mimic_fallback` passed in-browser Validate but failed Go compile. Fixed in plan-1 (TS check + registry + fixture-15 exercise); flagged to review. The sibling `EDGE_OMITEMPTY` list also lacks `mimic_fallback` — noted as follow-up, NOT changed (different blast radius: canonical-diff behavior) |

## 7. Milestones

### plan-1 — Core direction semantics, both compilers + validators → `plan-1-2026_07_03.md`
**Goal (as amended by D11/D12):** the field + the reverse-endpoint suppression gate + 4 validation
codes + sanitize + conformance (+ the D12 mimic_fallback TS mirror), one PR, byte-exact.
**Hazards:** pair-folding silently ignoring direction (→ conflict rule); the TS
compile write-back dropping the field; drift/i18n gates. **Verification:** full local suite +
zero-churn golden assertion + new fixtures green in both languages. **Stop-loss:** if TS echo
drops the field in an edge-rebuilding path → plan-1.5 (fix the echo, never the fixture).

### plan-2 — Panel UX → `plan-2-2026_07_03.md`
**Goal (as amended by D11):** EdgeEditor select (real node names + arrows; the "to(A)" choice is
an explicit edge FLIP — swap from/to, mirror pins, clear stale endpoint fields, prefill the host
picker), direction chip + end marker, sanitize-visible behavior, e2e. **Hazards:** e2e locator
fragility (use `data-testid`); the flip's pin mirroring must be pure + allocation-stable.
**Verification:** lint/build/vitest/e2e. **Stop-loss:** visual-corpus regressions → deliberate
re-baseline (plan-2.5), never loosened locators.

### plan-3 — Behavioral proof + docs → `plan-3-2026_07_03.md`
**Goal:** realtunnel `c4` scenario (suppressed side has no `Endpoint =`, tunnel still forms) +
`edge.md`/`peer-derivation.md`/bilingual wiki. **Hazards:** netns kernel surprises (inert keepalive
vs NAT timeout). **Verification:** scenario green locally under `REALTUNNEL_SCENARIOS`; CI additive
tier. **Stop-loss:** kernel-behavior surprise → plan-3.5 (document + adjust semantics with the
owner BEFORE release).

### plan-4 — Release v2.0.0-beta.18 → `plan-4-2026_07_03.md`
**Goal:** CHANGELOG roll, tag, release workflow green, promote to Latest, STATUS/memory closeout,
owner smoke handoff. **Hazards:** release.yml gate drift (mirror ci.yml's required set — beta.9
lesson). **Verification:** 29-asset release + `BuildVersion` stamp. **Stop-loss:** a red release
workflow → fix at root, re-tag from the green tip (never force-push a tag).

## 8. Insertion-point markers

- **plan-1.5** — TS topology echo drops `link_direction` (e.g. an edge-rebuilding path in
  `frontend/src/compiler/index.ts:197-236`): fix the echo; the conformance manifest's topology
  section is the detector.
- **plan-2.5** — the marker change regresses visual-corpus snapshots (non-blocking tier):
  re-baseline deliberately via a reviewed `--update-snapshots` run.
- **plan-3.5** — netns proof exposes kernel-behavior surprises around the suppressed side's inert
  keepalive: document, decide with the owner, adjust before beta.18.

## 9. Closure criteria

- [ ] All 4 plans merged (each reviewed → fixed → re-reviewed → CI green).
- [ ] `v2.0.0-beta.18` released; assets + version stamp verified; promoted to Latest (owner pattern).
- [ ] STATUS.md + project memory updated; owner smoke script handed off.
- [ ] Subject folder stays in `implementation_plans/` until the owner smoke passes (then archived to
      `_completed/` alongside the rc.1 cut).

## 10. Plan status table

| Plan | Title | Status | PR |
|---|---|---|---|
| plan-1 | Core direction semantics (both compilers + validators) | **done** (reviewed → fixed → re-reviewed clean → merged) | #221 |
| plan-2 | Panel UX (EdgeEditor + canvas + e2e) | **done** (reviewed → fixed → re-reviewed → residuals fixed → final verify clean → merged) | #222 |
| plan-3 | Behavioral proof + docs (realtunnel c4 + spec + wiki) | in review | — |
| plan-4 | Release v2.0.0-beta.18 + closeout | pending | — |
