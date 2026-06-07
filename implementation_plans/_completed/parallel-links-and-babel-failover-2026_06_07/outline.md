# Outline — parallel-links-and-babel-failover (2026-06-07)

## 1. Mission

Allow **N parallel enabled edges between one node pair** (one primary + backups), each compiling
to its own WireGuard interface with its own port/transit/link-local allocation, so **Babel
performs cost-based failover** between them — with **zero allocation drift** for every existing
single-edge topology. Success criteria:

- A topology with primary + backup edges between A–B compiles to two distinct WG interfaces with
  distinct ports/transit pairs/link-locals and two babeld `interface` stanzas (rxcost 96-class vs
  384) on both nodes.
- The perpetual I1/I2 stability gate passes byte-identical for all pre-existing single-edge
  fixtures (the no-drift proof).
- Adding a backup edge to a deployed pair does not change any of the primary edge's pinned values.
- Frontend: backup creation is a deliberate gesture (Add-backup button), parallel links render as
  a role-chip fan, and accidental duplicates remain visually/semantically flagged.

## 2. Principles (invariants the executor must respect)

Inherits all of `/PRINCIPLES.md`. Subject-specific:

- **[STATED, HIGH] No drift for existing fleets.** Every currently-valid topology must compile to
  byte-identical allocations and interface names after this subject. Violations: re-keying the
  canonical edge away from bare `pinKey`; changing `WgInterfaceName` output for single-link peers;
  altering gap-fill order for single-edge pairs.
- **[STATED, HIGH] Reverse-pair semantics preserved.** Roleless A→B + B→A still unify into ONE
  bidirectional tunnel. Violation: emitting two interfaces for a legacy reverse-drawn pair.
- **[INFERRED, HIGH] Validator and compiler stay byte-for-byte consistent** on linkKey, unify
  rule, naming, and cost resolution (the audit program's lesson; internal/naming exists for this).
- **[STATED, MED] Multipath is opt-in via `role`.** Plain canvas drag never creates a parallel
  link; roleless same-direction duplicates remain warned (D71 re-scope, not removal).
- **[INFERRED, MED] Failover must be expressible.** Equal effective costs on all links of a pair
  = no preference; the validator must warn, never silently accept as "configured failover".

## 3. Current state of the world (2026-06-07)

- `main` @ `bb1ee78` (audit remediation #3–#12 + canvas UX #13 all merged; CI green).
- Sticky-pin allocation live (reserve-then-gap-fill, verbatim pins, `pinKey` identity).
- D63 cost chain live: `Edge.Priority/Weight → PeerInfo.LinkCost → babeld rxcost`.
- Canvas: node-level handles, pending/compiled edge states, keyed edge sync, auto-layout (PR #13).
- Today a second same-pair enabled edge is **silently dropped** (`peers.go:171-175`), warned by
  D71 (`semantic.go:788-802`).

## 4. Must-read references

Memory: `audit-plan-pipeline-state.md` (method + merge gotchas).
Investigation fleet report (4 agents, file:line complete):
`/tmp/claude-1000/-home-kunorikiku-source-yet-another-overlay-generator/cfdefb3b-d803-49f6-b4b5-70fc24446fe9/tasks/wg69aj4z8.output`
— if the tmp file is gone, the Context section of each plan file restates every load-bearing site.

Production code (line numbers at `bb1ee78`):
- `internal/compiler/peers.go:147,151-191,171-175,196-198,216-261,267,329-333,338-352,391,434,456,489,497-504,518,628-637,730-744,843-887`
- `internal/compiler/compiler.go:138-184`
- `internal/naming/naming.go:87-117`
- `internal/validator/semantic.go:192-246,319-353,480-492,525-622,687-741,788-802`
- `internal/renderer/babel.go:84,118-134` · `wireguard.go:166,198` · `script.go:570-578` ·
  `deploy.go:78-86,274-278,538-542` · `babel_presets.go` (DefaultCost; wired=96)
- `frontend/src/components/canvas/TopologyCanvas.tsx:135-152,163-196,345-371,398-411`
- `frontend/src/components/canvas/CustomEdge.tsx:49-76`
- `frontend/src/components/layout/RightPanel.tsx:55-60,766-804,829-872,885-933`
- `frontend/src/types/topology.ts:72-94` · `stores/topologyStore.ts:242-282`

Specs to amend (plan-1): `docs/spec/compiler/allocation-stability.md` (esp. 166-235),
`data-model/edge.md`, `artifacts/naming.md`, `api/wire-contract.md`, `artifacts/babel.md`,
`compiler/validation.md`.

Test gates: `internal/compiler/allocation_stability_test.go` (perpetual I1/I2 — extended, never
weakened), `internal/renderer/babel_announce_test.go` (self-/32 golden — untouched).

Web: [babeld manual](https://www.irif.fr/~jch/software/babeld.html) (multi-link costs);
[Pro Custodibus on shared WG keys](https://www.procustodibus.com/blog/2021/01/same-key-multiple-peers/)
(parallel tunnels OK on distinct devices/ports; per-interface keys = best practice → escape hatch).

## 5. Standing rules

Per `/PRINCIPLES.md` + memory. Workflow fan-outs ≤ 5 agents. Stacked-PR merges: retarget child →
merge parent → delete branch (GitHub auto-retarget races; see memory).

## 6. Decisions log

| # | Decision | Answer |
|---|---|---|
| 1 | Backup-creation gesture | Add-backup button in RightPanel edge editor; plain drag stays unique |
| 2 | Cost UX | Role toggle + presets; raw priority/weight remains for fine-tuning |
| 3 | Cap | Up to N edges per pair (no hard cap) |
| 4 | Sequence | Subject starts after PR #13 merged (main @ bb1ee78) |
| 5 | N-edge visual | Role-chip fan (★ primary / b1 / b2…); ⚠ chip for roleless same-direction extras |
| 6 | Subject name | parallel-links-and-babel-failover |
| 7 | Shape | 3 plans, 3 stacked PRs (spec → backend → frontend) |
| 8 | Unify rule | Role-as-disambiguator: roleless opposite-direction pairs unify (legacy); `role=backup` always its own link; roleless same-direction extras stay warned |
| 9 | Backup default cost | 384 (4× babeld wired 96) when no explicit priority/weight; warn on all-equal effective costs |
| 10 | Out of scope | ECMP, per-edge WG keypairs (escape-hatch note only), bundled/hybrid rendering, client multi-edge |
| 11 | Focus-transparency UX | De-emphasis is TRANSPARENCY, not hiding: non-focused elements stay rendered and clickable at very low opacity (~0.15), smoothly transitioned — present but not distracting. Node selected → everything except it + its incident edges goes transparent; edge selected → everything except it + its two endpoint nodes goes transparent; connection drag → all edges go transparent, all nodes stay fully opaque (they are the drop targets). **Clicking the canvas background clears focus → everything fades back to full opacity** (rides the existing `onPaneClick` selection clear — pin with an explicit check, do not rely on it incidentally). Never `display:none`/unmount. Plan-3. |
| 12 | Show-interfaces × backup edges | Node-card interface chips become edge-aware: resolve each compiled interface to its edge via pinned port (never by stripping `wg-` from the name — a backup interface `wg-<clean8><hash4>` would render garbage), display `<peerName>:<port>` with the same ★/bN role marker as the edge fan, real interface name in the tooltip. Resolver shared with the RightPanel per-edge fix. Plan-3. |
| 13 | linkKey canonicality refinement | Canonical = the pair's PRIMARY-CLASS link (role != backup), NOT lowest edge ID — UUIDs sort randomly, so a new backup could steal the bare pinKey and rename the deployed primary's interface. Backups are ALWAYS discriminated (pinKey#edgeID), even when alone; identity never migrates on growth. Role flips = deliberate identity change (rename; values survive via pins); warn on no-primary pairs. |

## 7. Milestones

### Plan 1 — Contract amendment (docs-only PR) → `plan-1-2026_06_07.md`
Goal: freeze linkKey scheme, unify rule, edge-aware naming, role field, cost mapping in the spec.
Hazards: spec text contradicting verbatim-pin reality. Verification: PR review. Stop-loss:
contract disagreements resolved in review before any code.

### Plan 2 — Backend per-edge link identity (Go PR) → `plan-2-2026_06_07.md`
Goal: compiler/validator/naming implement the frozen contract; perpetual gate extended.
Hazards: drift in single-edge gap-fill order; validator/compiler divergence; edgeMap reverse
resolution. Verification: CI + extended I1/I2 gate + goldens. Stop-loss: plan-2.5 / halt on gate
drift.

### Plan 3 — Frontend (PR) → `plan-3-2026_06_07.md`
Goal: Add-backup button, role chips, per-edge compiled display, focus-dim, TS/i18n.
Hazards: silent mis-attribution in compiled-values panel; accidental-duplicate confusion.
Verification: CI lint+build + manual smoke script in PR description. Stop-loss: fan clutter at
N>3 → follow-up marker, do not block.

## 8. Insertion-point markers

- **plan-2.5** — unify rule conflicts with an existing example/fixture → narrow to
  same-direction-only parallels.
- **plan-2.6** — 16-bit name-hash collisions surface in practice → widen hash to 6 hex, shrink
  clean slice; validator already errors on collision meanwhile.
- **plan-N.5** — perpetual stability gate catches drift at any point → halt merges, redesign the
  discriminator before proceeding.

## 9. Closure criteria

- [x] All 3 PRs merged, CI green on main (2026-06-07, `bb1ee78..1af83db`; every PR CI-green and
  independently review-approved 3/3 before merge).
- [x] Extended I1/I2 gate (incl. parallel fixtures) in perpetual suite — pre-existing fixtures
  untouched (verified additive-only by the #15 reviewer); passed on first CI contact.
- [x] Manual failover smoke narrative recorded in the PR #16 description.
- [x] specs consistent with shipped behavior; D71 re-scope documented; reviewer-found property-1
  wording contradiction fixed pre-merge (`2478b37`).
- [x] STATUS.md refreshed; subject archived to `_completed/`.
- [x] Test retirement dispositions recorded — **keep all** (every suite is a sub-second unit
  test; the parallel I1/I2 fixtures, linkid/naming tests, and babel two-stanza golden are
  perpetual; validator parallel_links_test stays per the cheap-to-keep rule).

Carried-forward (non-blocking reviewer notes):
- No end-to-end compile test for a pair whose ONLY edge is a backup (validator + identity paths
  covered; compile path verified by static trace) — add if compile coverage is revisited.
- Node-ID charset is not restricted from the reserved separators `|`/`#`/`->` (pre-existing,
  self-consistent either way) — optional hardening.
- Lone backup edge (no surviving primary) renders no bN marker on node chips — graceful display
  choice, revisit with bundled rendering.
- `roleChip` TS union is loosely typed (`string` for bN) — cosmetic.

## 10. Plan status table

| Plan | Status | PR | Notes |
|---|---|---|---|
| plan-1 | merged | [#14](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/14) | spec contract freeze + property-1 fix |
| plan-2 | merged | [#15](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/15) | backend link identity; perpetual gate green on first contact |
| plan-3 | merged | [#16](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/16) | frontend + focus-transparency; subject closed 2026-06-07 |
