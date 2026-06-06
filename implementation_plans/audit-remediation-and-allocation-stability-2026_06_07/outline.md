# Outline — audit-remediation-and-allocation-stability
<!-- drafted: 2026-06-07, target: main @ d5065ed (v1.2.0) -->

## 1. Mission

Remediate all 84 adversarially-verified audit findings (7 critical / 41 high / 24 medium / 12 low,
incl. 6 UX findings) and deliver **sticky-pin allocation stability** so that adding a server to a
deployed overlay never disturbs existing nodes. Specs-first: contracts are frozen as normative
text in `docs/spec/` before any code changes.

Success criteria:
- Every dossier finding (D1–D78, UX-1–6) is fixed, explicitly deferred-with-decision, or closed
  as superseded — none silently dropped.
- The G4 order-independence property test passes: compiling `[A,B]+A–B` then `[A,B,C]+A–B,A–C`
  in both edge orders reproduces byte-identical values for A–B (invariant I1).
- The default happy path (draw edge → compile → deploy) produces working tunnels; warnings reach
  the user on compile.
- All 10 PRs merged with the Plan-1 CI workflow green.

## 2. Principles (invariants the executor must respect)

Inherits ALL of `/PRINCIPLES.md` (created with this subject). Subject-specific additions:

- **[STATED, HIGH] Spec-before-code.** No code plan (2–10) starts until Plan 1's contract PR is
  merged. A code change that contradicts frozen spec text requires a spec amendment in the same
  PR, called out in the PR description. Violation example: "fixing" CompiledPort semantics
  differently than `docs/spec/data-model/edge.md` specifies.
- **[STATED, HIGH] Verified-findings-only.** The 13 refuted claims in the dossier appendix are
  NOT bugs; do not "fix" them. Violation example: making compile stop blanking non-fixed keys in
  the response (the blanking is the documented contract; the fix is pin-reuse, Plan 7).
- **[INFERRED, MED] One PR per plan**; CI must be green; no cross-plan code smuggling (a Plan-5
  security fix does not ride a Plan-4 PR).
- **[INFERRED, MED] i18n consistency.** User-facing validator/compiler messages follow the
  existing locale pattern (see commit 0bd9dd8); don't introduce English-only strings where
  zh strings exist, and fix the garbled ones (nat.go:27/35/61, cmd/compiler printf).

## 3. Current state of the world (2026-06-07)

- Branch `main` @ `d5065ed` (tag v1.2.0), clean except uncommitted: `docs/spec/` (28-file spec
  restructure), `docs/audit/2026-06-audit-dossier.md`, modified `docs/DEVELOPMENT_SPEC.md`
  (redirect stub), this folder, `STATUS.md`, `PRINCIPLES.md`. All are Plan-1 PR cargo.
- CI: only tag-triggered `.github/workflows/release.yml`. **No PR-triggered tests exist.**
  Go/npm are NOT installed locally — Plan 1 must add `ci.yml`; everything verifies in CI.
- A remote "ultraplan" cloud session may land a PR at any time (none open as of drafting;
  `gh pr list` shows only merged #1/#2). Reconcile via plan-N.5 insertion.
- Existing Go tests: `internal/{allocator,api,artifacts,compiler,model,renderer,validator}/*_test.go`
  — status on main unknown until CI first runs (insertion plan-1.5 if red).

## 4. Must-read references

Memory:
- `~/.claude/projects/-home-kunorikiku-source-yet-another-overlay-generator/memory/MEMORY.md`
- `.../memory/audit-plan-pipeline-state.md` — why docs/spec/ is uncommitted; concurrency caps.

Architecture / project docs:
- `docs/spec/README.md` — spec layout (this project's `specs/` equivalent).
- `docs/audit/2026-06-audit-dossier.md` — THE finding inventory (D1–D78, UX-1–6, themes T1–T14,
  refuted-claims appendix). Every plan cites finding IDs from here.
- Full audit outputs (session-local, for deep detail):
  `/tmp/claude-1000/-home-kunorikiku-source-yet-another-overlay-generator/cfdefb3b-d803-49f6-b4b5-70fc24446fe9/tasks/{wng67bacc,wl8iikeup,wzfd00s2i}.output`
  (deep-audit / UX / growth). NOTE: /tmp may not survive reboots; the dossier is the durable copy.

Production code (anchors verified against d5065ed):
- `frontend/src/components/canvas/TopologyCanvas.tsx:236-259` — onConnect (headline bug origin)
- `internal/compiler/peers.go:129-209` (Pass 1 allocation), `:290-306` (endpoint resolution),
  `:363-433` (reverse peer), `:443-478` (transit/LL allocation), `:492-522` (wgInterfaceName)
- `internal/compiler/compiler.go:68-129` (pipeline + CompiledPort write-back :111-126)
- `internal/api/handler.go:267-317` (generateKeys), `:380-420` (createExportZip), `:477-519`
  (self-extracting wrapper), `:210-244` (HandleDeployScript)
- `internal/renderer/deploy.go:40-90` (node info build), `:216,298,324` (installer lookup),
  `:609-624` (safeInstallerFileName), `:282-359` (bash ssh interpolation), `:507-578` (ps1)
- `internal/renderer/script.go:139-183` (Phase 0), `:337-370` (SNAT), `:424-428` (Phase 3)
- `internal/renderer/babel.go:43-56` (template), `:84-116` (interfaces+redistribute), `:160-169`
  (shouldRunBabel)
- `internal/renderer/wireguard.go:52-101` (per-peer template), `:127-130` (client AllowedIPs)
- `internal/allocator/ip.go:32-49` (stale clear), `:61-79` (skip-set loop), `:112-169`
  (allocateFromCIDR + ipToUint32)
- `internal/validator/schema.go`, `semantic.go`, `nat.go` — coverage gaps per dossier T4
- `frontend/src/stores/topologyStore.ts:224-227` (getTopology), `:343-351` (compile rehydrate),
  `:430-436` (partialize)
- `cmd/compiler/main.go:33-117` — CLI divergences

Test gates:
- Existing: `go test ./...` (all packages above have `_test.go`).
- New perpetual gates created by this subject: endpoint-resolution matrix (Plan 2), self-/32
  byte-identical babel gate (Plan 6), G4 order-independence property test (Plan 7), injection
  fixture tests (Plan 5).

Web references:
- babeld manual — `redistribute local` matches local/connected kernel routes only (basis of
  D40/D41): https://www.irif.fr/~jch/software/babeld.html (verified in-code by audit panel)
- wg-quick(8) — Table=off semantics, AllowedIPs cryptokey routing:
  https://man7.org/linux/man-pages/man8/wg-quick.8.html

## 5. Standing rules

See `/PRINCIPLES.md` (project-wide) and memory. Non-obvious restatements:
- Workflow/agent fan-outs (if any) capped at 5 concurrent — user-mandated after rate-limit
  incidents.
- The user's global CLAUDE.md secret-handling rules apply when touching key material in tests.

## 6. Decisions log

| # | When | Question | Decision |
|---|---|---|---|
| 1 | preflight | Scope | Full coverage of all 84 findings + growth feature, phased |
| 2 | preflight | route_policies (5 findings) | Remove/reserve: validator rejects non-empty as "reserved/unimplemented"; TS marked reserved; implementation is a future subject |
| 3 | preflight→corrected | routing_mode static/none | First answer "build static renderer" was based on a domain/role conflation; after correction (per-node babel-less behavior = existing client role) user chose **defer: empty→babel default, reject static/none** |
| 4 | preflight | PR strategy | Plan PR (docs+CI) + one PR per code phase |
| 5 | naming | Subject | audit-remediation-and-allocation-stability |
| 6 | midflight→corrected | Client AllowedIPs D30 | User initially doubted the finding (cross-domain "works fine" — true for non-client roles); after clarification chose **union of all domain CIDRs + transit CIDRs** |
| 7 | midflight | Sticky-pin migration | Existing `fixed_private_key` field IS the live-key capture path (documented paste procedure); one-final-rotation default otherwise; no new key-import tooling |
| 8 | postflight | Shape | 10 plans accepted |
| 9 | postflight | Stop-losses | Accepted as documented (see milestones 6, 7) |
| 10 | postflight | Out of scope | Confirmed: route_policies impl, static renderer, additive-apply installer, per-node deploy selector, IPv6 overlay — all flagged future subjects |
| 11 | postflight | Remote ultraplan PR | Insertion plan on arrival; this program is the source of truth |
| 12 | drafting | Spec location | `docs/spec/` (user-chosen layout) serves as this repo's `specs/`; flagged convention difference in implementation_plans/README.md |

## 7. Milestones

Each milestone = one plan file = one session = one PR. Detail lives in the plan files.

| M | Plan file | Goal (one line) | Verification gate | Stop-loss |
|---|---|---|---|---|
| 1 | plan-1 | Contract freeze: normative spec text (Specs A–E + amendments) + PR-triggered CI + bundle docs/spec restructure, dossier, plan folder | PR review approves spec text; ci.yml runs on the PR itself | Spec disputes resolved in PR review BEFORE any code plan starts |
| 2 | plan-2 | Headline port fix (T1) + examples | Endpoint-resolution test matrix green in CI | If reverse-endpoint fallback (UX-2) proves contentious, ship the frontend stop-stamping alone; fallback → plan-2.5 |
| 3 | plan-3 | Compile surfaces warnings (UX-1) + API/allocator hardening (panic class, timeouts, caps) | New handler tests: warnings present; IPv6 CIDR → clean 4xx; oversized body → 413 | Recover middleware conflicts with streaming export → exempt export handler, document |
| 4 | plan-4 | Artifact naming/identity unification + deploy-endpoint correctness | Collision-rejection tests; ZIP entry == deploy lookup test | If renaming ZIP entries breaks existing user tooling expectations, keep dual entries one release (raw symlink-style duplicate) and document |
| 5 | plan-5 | Script/SSH security + integrity chain | Injection fixture tests (hostile node name / ssh_host render inert); wrapper hash verification test | If %q quoting breaks legitimate alias usage, fall back to strict charset validation only + documented constraint |
| 6 | plan-6 | Routing-mode normalize/reject + Babel route correctness + client AllowedIPs union | Byte-identical self-/32 gate; renderer table-driven tests per role/mode | D40/D41 unprovable → narrow to gateway-default + extra_prefixes; domain-CIDR aggregate → plan-6.5 |
| 7 | plan-7 | Sticky-pin allocation (I1–I10) + transit/SNAT prerequisites | G4 order-independence property test; superset-stability tests; validateAllocationPins tests | Pin index reverse-mapping fragile → pins store verbatim transit IPs (no index math) → plan-7.5 redesign |
| 8 | plan-8 | Shared render/keys entrypoint (CLI parity) + install robustness | CLI-vs-API artifact equivalence test; SNAT idempotency test | If extraction destabilizes handler tests, CLI delegates via thin adapter instead of full extraction |
| 9 | plan-9 | Contract cleanup (route_policies reserved, transit_cidr UI, field validation) + frontend state bugs | Validator coverage tests; FE build+lint green | transit_cidr UI deferred to plan-10 if D12 interactions surface late |
| 10 | plan-10 | UX bridging (default domain, handles, public-address field, extra_prefixes editor) + docs sync + closure | FE build+lint; manual smoke script in PR description; closure checklist | UX scope creep → split plan-10.5; docs sync never blocks closure of code work |

## 8. Insertion-point markers (pre-declared)

- **plan-1.5** — ci.yml red on untouched main (pre-existing test failures): minimal fix-forward
  repairs before any code plan.
- **plan-6.5** — domain-CIDR aggregate announcement redesign (if Plan 6 narrows under stop-loss).
- **plan-7.5** — verbatim-IP pin fallback redesign (if reserve-then-gap-fill index math fails
  property tests).
- **plan-N.5 (any time)** — remote ultraplan PR lands: diff against this program, cherry-pick
  superior pieces, close duplicates. This program's frozen specs win contract conflicts.

## 9. Closure criteria

- [ ] All 10 PRs merged, CI green on main.
- [ ] G4 property test + endpoint matrix + injection fixtures + self-/32 gate in perpetual suite.
- [ ] Every dossier finding ID mapped to: fixed-in-PR-#N | deferred-by-decision-# | superseded.
- [ ] `docs/spec/` consistent with shipped behavior (final reconciliation pass in Plan 10).
- [ ] Stale docs (T14) fixed; DEVELOPMENT_SPEC.md stub intact.
- [ ] STATUS.md refreshed; subject folder moved to `_completed/`.
- [ ] Subject-scoped tests retired per their retirement triggers.

## 10. Plan status table

| Plan | Status | PR | Notes |
|---|---|---|---|
| plan-1 | in-review | [#3](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/3) | spec freeze + CI |
| plan-1.5 | done (awaiting CI) | — | D18 pulled forward: react-hooks/refs lint errors blocked all CI |
| plan-2 | in-review | [#4](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/4) | headline port fix |
| plan-3 | pending | — | |
| plan-4 | pending | — | |
| plan-5 | pending | — | |
| plan-6 | pending | — | |
| plan-7 | pending | — | |
| plan-8 | pending | — | |
| plan-9 | pending | — | |
| plan-10 | pending | — | |
