# Outline — extensible-i18n-and-structural-hardening
<!-- drafted: 2026-06-14 by the post-audit fix program (autonomous) -->

## Mission

Close the language, mode-correctness, and structural debt that the controller-mode redesign left
behind, around three principles the codebase does not yet honor:

1. **One message, one key, any language.** The frontend i18n module is a positional two-language
   `txt(lang, zh, en)` catalog (406 call sites, 77 `[zh,en]` tuples). It cannot take a third
   language, cannot interpolate parameters (forcing string-concat), has no fallback chain, and has
   no path to localize backend errors. Replace it with a **keyed, parameterized, fallback-aware,
   language-extensible** catalog — the user has asked for this three times.
2. **Errors carry codes, not prose.** The backend returns `{"error": "<Chinese sentence>"}`
   (`handler.go:578 writeError`). The panel shows that raw, so an English-speaking operator sees
   Chinese (the bug the user hit). Introduce a **typed error-code envelope** `{code, params,
   message}`: a stable code + structured params the panel localizes client-side, plus a
   server-rendered default `message` (English) for non-localizing clients (CLI, curl).
3. **Mode logic never leaks across the mode boundary, and structure stays clean.** Finish the
   controller/local mode-correctness work (the in-flight-compile race; export/deploy-script
   guard parity) and clear the highest-leverage **structural debt** surfaced by the whole-repo
   audit (`wb6dq4uwc`) — god-functions, leaky boundaries, duplicated logic, dead defensive code,
   inconsistent error handling, weak typing, and load-bearing paths with no test.

The user's standing latitude for this subject: **design the structure freely, no backward
compatibility required (only I use this repo), end state must be extremely clean / structural /
useful, and no main-flow functionality may be dropped.**

Success criteria:
- An English-locale operator never sees a Chinese (or otherwise wrong-language) string anywhere in
  the panel — including every backend error surfaced in the UI.
- Adding a third UI language = add one catalog file + register it; **zero** call-site edits and
  **zero** changes to the `t()` signature.
- Every user-facing message is referenced by a stable key; parameterized messages interpolate named
  params instead of concatenating; a missing translation falls back (current → English → key) and
  never renders blank/undefined.
- Backend errors are typed with stable codes; `GET`/`POST` failures return `{code, params, message}`;
  the panel localizes from `code`+`params`; CLI/curl still get a readable English `message`.
- The controller-mode key-handling boundary is leak-proof: no air-gap key-generation path
  (`compile`, `exportArtifacts`, `downloadDeployScript`, or an in-flight async write) can run
  against a controller-mode design.
- Every CONFIRMED audit finding (security, robustness) is fixed or has a recorded, justified
  deferral; the structural debt the audit confirms is cleared except where a deferral is logged.
- `go test ./...` + `go vet` green; `cd frontend && npm run lint && npm run build` green; every PR
  independently reviewed (find → adversarial verify) before merge.

## Principles (invariants the executor must respect)

Inherits everything in `PRINCIPLES.md` (repo root). Highest-relevance inherited items:
**Key custody (HIGH)**, **Generated scripts run as root on fleets (HIGH)**, **Allocation
stability (HIGH)**, **Backward compatibility of persisted topologies (MEDIUM)**.

Scoped, user-sanctioned compatibility break (this subject): **internal API shapes may break** — the
frontend↔backend error envelope changes (`{error:string}` → `{error:{code,params,message}}`), the
i18n call API changes (`txt(lang,zh,en)` → `t(key, params?)`), and tests asserting the old shapes
get migrated. The persisted-topology JSON contract is NOT exempt and must hold. Generated bundle
bytes for the air-gap path must stay byte-identical (the equivalence + custody guard tests pin this).

Subject-specific principles:

- **Message identity is a key, never a literal (HIGH) [STATED].** Every user-facing string resolves
  through `t(key, params?)` against a per-language catalog. Violations: a hardcoded zh/en literal in
  a component; passing prose to `txt`; concatenating a translated fragment with a value instead of
  interpolating a param into one keyed template.
- **Errors are coded at the source, localized at the edge (HIGH) [STATED].** A failure carries a
  stable `code` + structured `params` from where it originates; the default human `message` is
  rendered once (English) for non-localizing consumers; the panel localizes from `code`+`params`.
  Violations: a raw `fmt.Errorf("中文…")` reaching the API boundary uncoded; building a user
  sentence by string-concat in Go; the panel `.toString()`-ing an error object.
- **Language is data, not control flow (HIGH) [STATED].** The set of languages is a registry of
  catalog files; `t()` and every call site are language-count-agnostic. Violations: a positional
  tuple, a `lang === 'zh' ? … : …` ternary in a component, a `switch (lang)` anywhere outside the
  i18n core.
- **Mode logic never crosses the mode boundary (HIGH) [STATED].** No air-gap key path runs in
  controller mode and vice versa; an in-flight async operation re-checks mode before it mutates
  shared state. Violations: an `await`-resumed compile writing reconstructed keys after a
  controller switch; an export/deploy-script action lacking the guard `compile()` has.
- **Clean structure beats clever structure; never drop main flow (MEDIUM) [STATED].** Structural
  refactors must reduce duplication / sharpen boundaries / strengthen typing without removing any
  user-reachable capability that is part of the main flow (design → compile/export → deploy; login
  → hydrate → deploy; enroll → pull → apply). Violations: deleting a feature to "simplify"; a
  refactor that leaves two ways to do the same thing.

## Decision rule (how the executor resolves forks without the user)

The user is asleep and has delegated decisions. For every fork: **simulate a real user walking the
edge-case usages** (empty/missing data, logged-out/break-glass, controller vs local, first-run vs
steady state, the destructive/irreversible variant) and pick the behavior that matches the intuitive
expectation + these principles. State the choice + why in the PR/commit. Only escalate a fork to the
user if it is genuinely theirs (outward-facing/irreversible publish with no inferable default). For a
**genuinely-unclear** engineering fork (two clean designs with real trade-offs and no obvious
winner), summon a **two-Opus-agent debate** via a Workflow (one argues each side, a third
synthesizes) and decide from that rather than guessing or stalling. See memory
[[simulate-user-decide-no-prompt]].

## Current state of the world (2026-06-14)

- Branch `main` @ `7aacf50` (PR #68 merged), clean, synced with `origin/main`.
- Shipped this week: controller-server-authority-redesign (#59–#65, closed/delivered), then four
  follow-up hardening PRs — #66 login-gate, #67 operator/agent namespace split, #68
  controller-mode compile gate + English key-gen errors. v2.0.0-preview.6 cut; preview.7 status
  reconciled at closure.
- **The English key-gen errors in #68 are a stopgap** (English-translated, not localized). This
  subject does the proper fix (error-code envelope + client localization) and supersedes them.
- The whole-repo audit `wb6dq4uwc` (10 angles: sec-authz, sec-custody, sec-injection,
  mode-correctness, robust-backend, robust-frontend, i18n-userfacing, struct-backend,
  struct-frontend, struct-crosscutting; each adversarially verified) is the primary input for the
  security / robustness / structural milestones below; its CONFIRMED+PLAUSIBLE findings are
  integrated into this outline + the plan files (see `findings.md`).
- Parked/stale: `implementation_plans/controller-panel-2026_06_08` (older, superseded by the
  redesign subject; plans still marked pending). Out of scope here; flag at closure.

## Must-read references

Memory:
- [[simulate-user-decide-no-prompt]] — the decision rule above.
- [[controller-mode-redesign-decisions]] — the mode/custody principles to decide against.
- [[ultracode-use-workflow-tool]] — fan-outs of ≥3 agents go through Workflow.

Audit + findings:
- `findings.md` (this folder) — the persisted audit results (CONFIRMED + PLAUSIBLE), grouped by
  angle, plus the 6 PR#68-review PLAUSIBLE items, each mapped to a milestone.
- Workflow run `wb6dq4uwc` / `wf_0333d391-033` (result in its task output + wf JSON).

Architecture (specs/ — partial-load per plan header): `specs/panel-*.md` (i18n touches every panel
component), `specs/controller-operator-api.md`, `specs/controller-agent-api.md`, `specs/render-keys.md`,
plus the deep docs under `docs/spec/`.

Production code (line numbers as of `7aacf50`):
- i18n: `frontend/src/i18n.ts` (148 lines: `UILanguage='zh'|'en'`, `txt(lang,zh,en)`, `STRINGS`
  = 77 `[zh,en]` tuples); 406 `txt()` call sites across `frontend/src/**/*.{ts,tsx}`.
- Error envelope: `internal/api/handler.go:43` (`ErrorResponse{Error string}`), `:578`
  (`writeError`), `internal/api/handler_login.go:36`, `handler_passkey.go:48`,
  `internal/api/server.go:104` (panic→500 JSON). Frontend display: `controllerClient.ts`
  (throws on non-OK; callers write `store.error`), plus `txt()`-rendered surfaces.
- Backend Chinese (mix of comments + user-facing `Errorf`/`writeError` — `findings.md` carries the
  user-facing subset): `cmd/compiler/main.go`, `internal/allocator/ip.go`, `internal/api/{handler,
  server}.go`, `internal/artifacts/export.go`, `internal/compiler/{compiler,peers,roles}.go`,
  `internal/model/topology.go`, `internal/render/render.go` (`:86,:90,:127` untranslated),
  `internal/renderer/{babel,babel_presets,deploy,script,wireguard}.go`,
  `internal/validator/{nat,schema,semantic}.go`.
- Mode boundary: `frontend/src/stores/topologyStore.ts` — `compile()` (~551, guarded #68),
  success branch (~587-597, writes reconstructed keys), `exportArtifacts()` (~607, no guard),
  `downloadDeployScript()` (~643, no guard), `partialize` (~685-686), `canvasFromServer`.
  `frontend/src/components/design/CanvasToolbar.tsx` (Compile button local-only, #68).
- Render custody: `internal/render/render.go` (`GenerateKeys`, `AirGap`/`AgentHeld`).

Test gates:
- Go: `go test ./...` + `go vet ./...` (local toolchain go1.26.3 available) and CI on PRs.
- Frontend: `cd frontend && npm install --legacy-peer-deps` then `npm run lint && npm run build`
  before push (no test runner — verification is lint+build+manual smoke).
- Perpetual guards that MUST keep passing: `internal/render/custody_guard_test.go`,
  `custody_diff_test.go`, `equivalence_test.go`, `internal/api/topology_custody_test.go`
  (the last asserts the `{error:...}` shape — migrate its assertions with the envelope change).

## Standing rules

Per `implementation_plans/README.md` + PRINCIPLES.md: per-substep commit+push, **one PR per plan**,
each PR gets an **independent review workflow (find → adversarial verify)**; fix CONFIRMED findings,
then merge (user cadence: "after each PR, send independent review workflow/agent, and if it passes,
merge"). No `--no-verify` / `--amend` / force-push / git-config edits. Frontend installs use
`npm install --legacy-peer-deps`. Branch off `main` per plan; never commit straight to `main`. Cut
releases only when the user asks. Decisions follow the decision rule above; hard forks → 2-Opus
debate Workflow, not a user prompt (user is asleep).

## Decisions log

| Date | Decision | Why |
|---|---|---|
| 2026-06-14 | D1: Replace `txt(lang,zh,en)`/positional `STRINGS` with a **keyed catalog** — one file per language (`messages/<lang>.ts`), `t(key, params?)`, named-param interpolation, fallback chain (current→English→key), language registry; migrate all 406 call sites; remove `txt`/`STRINGS` at theme end (no shim left behind) | User asked 3× to make i18n extendable; positional 2-lang tuples can't take a 3rd language or params; clean end state per the user's "extremely clean/structural" latitude |
| 2026-06-14 | D2: Backend **error-code envelope** `{"error":{code,params,message}}` + a typed Go error (`code`+`params`+default English `message`) + a central code registry; `writeError`→coded path | The panel can't localize a raw Chinese string (the bug the user hit); codes localize client-side while CLI/curl keep a readable message |
| 2026-06-14 | D3: Migrate **all** user-facing backend error strings (validator/compiler/render/renderer/allocator/artifacts/api/cmd) to typed coded errors; English is the default `message` language | The English-only #68 fix was a stopgap; comprehensive i18n requires every surfaced error to be coded |
| 2026-06-14 | D4: Finish mode-boundary parity — guard `exportArtifacts()`+`downloadDeployScript()` like `compile()`, and abort/guard an **in-flight compile** that resolves after a controller switch | PR#68-review PLAUSIBLE items; a user mid-switch must never get keys written to a controller-mode design |
| 2026-06-14 | D5: Clear the **highest-leverage structural debt** the audit confirms (god-functions, leaky boundaries, dup logic, dead code, weak typing, missing tests), not every nit; log any deferral | User: "cover all fixes, and most structural fixes that clear most structural debts… no compromises"; but keep the subject shippable |
| 2026-06-14 | D6: No backward compatibility for internal API/i18n shapes; **never drop main-flow functionality** | User: "no backward compat needed… just don't drop functionalities that are the main flow" |
| 2026-06-14 | D7: Decisions resolved by simulate-as-user; genuinely-unclear engineering forks → 2-Opus debate Workflow, not a user prompt | User asleep; [[simulate-user-decide-no-prompt]] |
| 2026-06-14 | D8: Audit ran as background task `waaymn4es` (10 angles, 166 agents, adversarially verified) → `findings.md` (106 CONFIRMED / 18 PLAUSIBLE) | The user wanted more agents + structural focus; a duplicate relaunch (`wb6dq4uwc`) was stopped once `waaymn4es` was found complete |
| 2026-06-14 | D9: Error-envelope design LOCKED via a 2-Opus debate+synthesis (`wmhzmpy2x`) — see `design-error-envelope.md`. Nested `{error:{code,message,params}}`; stdlib-only `internal/apierr` leaf (Code+registry+`New/.With/.Wrap`, init guard, `HasCode`); coded-at-the-source (deep packages import the leaf); sentinels kept + mapped to codes at the handler seam; `writeAPIError` replaces `writeError` (transitional delegate in plan-2, deleted in final plan-3 commit). | The single most consequential fork (shapes plans 1/2/3); the user authorized debate for genuinely-unclear forks. Key reframe: **plan-1 (i18n+tError) ships before plan-2 (envelope)** so the frontend reads both shapes before the backend flips — no PR breaks the app |
| 2026-06-14 | D10: M1 split into plan-1 (core+seam, shipped) + plan-1.5 (full 406-site migration) | Keeps each PR reviewable; isolates the large mechanical sweep; satisfies the debate's "tError before plan-2" via plan-1 alone |
| 2026-06-14 | D11: **Controller-mode boundary audit** (Opus workflow `wf_3e846c65-4b2`, 43 agents, 8 surfaces → adversarial verify → critic) → 25 confirmed + 6 critic-additional, deduped to 7 themes (`findings.md` addendum). New scope: LOCAL-mode UX/persistence/transition functionality leaking into the newly-introduced CONTROLLER mode (and reverse). Backend air-gap endpoints (`/api/compile|export|deploy-script|validate`) → **mount behind operator-auth in a controller deployment** (keeps `/validate` usable as a panel preflight; closes the unauth compute/key-gen oracle). | User-reported (the Topbar "Flush" exemplar). plan-4 only covered air-gap *key* paths; this class is new. A first Haiku-defaulted run was discarded (Explore agentType pinned Haiku) and re-run on Opus for trustworthy verification. |
| 2026-06-14 | D12: Controller mode gets an explicit **Save** primitive — `saveDesign()` = `stripPrivateKeys` → `updateTopology` (persist authoritative copy + version history, NO stage/promote) → `setCanvasFromServer(true)` → `lastSyncedAt`; surfaced as a Save button distinct from Deploy + a dirty-state indicator | User asked directly. `updateTopology` already exists as the no-fleet-touch persist primitive (deploy() calls it before stage/promote); WIP is otherwise unrecoverable without going live to the fleet |
| 2026-06-14 | D13: Save conflict handling = **client-side warn** (capture server base at hydrate; re-GET before save; on divergence warn + offer re-sync-with-backup, no blind overwrite). NO backend version field / optimistic-concurrency. | Sole-operator repo today; version history is the after-the-fact backstop; client-side warn covers the realistic case without a backend contract change |
| 2026-06-14 | D14: Mode-boundary work = **3 new plans run BEFORE the i18n grind**: plan-10 (switch security parity + Save core + deploy flag), plan-11 (UI mode-gating both directions), plan-12 (backend endpoint auth). plan-3.5/7/8/9 follow. | Main-flow + security + data-loss; the user's active focus. Each is an independently-reviewed PR. |

## Milestones

Each milestone = one plan file = one session = one PR (with independent review). Milestones M1–M4
are design-locked (below). M5+ (security / robustness / structural) are finalized from `findings.md`
(the audit landed — `waaymn4es`, 106 CONFIRMED / 18 PLAUSIBLE). **Plan-N files are authored
just-in-time on each milestone's branch** (concise — `findings.md` is the shared evidence base with
per-plan finding ownership), so they stay current instead of drifting; this outline + `findings.md`
are sufficient for a fresh session to pick up any milestone.

**Execution order (decided by value + dependency):** plan-0 (security, fast) → plan-4 (mode, fast)
→ plan-6 (robustness, fast) → plan-2 (error envelope, foundational backend) → plan-1 (i18n core +
406-site migration, foundational frontend) → plan-3 (backend strings → codes; needs plan-2 + plan-1's
error catalog) → plan-7 (struct-backend triage) → plan-8 (struct-frontend triage) → plan-9
(cross-cutting + docs + closure). M2+M3 together clear most of the structural PLAUSIBLE debt
(sentinel codes, compile-error context, decodeJSON leakage, the `{error:string}` contract).

> Ordering principle: **any CONFIRMED security finding from the audit jumps to the front** (becomes
> M0, fixed first). i18n core (M1) precedes the string migrations (M2/M3) because the migration
> targets the new `t()` API. Mode-boundary (M4) is independent and can land any time. Structural
> refactors (M6+) land after the behavior fixes so reviews aren't reviewing moving targets.

### M1 — Frontend: extensible i18n core + catalog migration → `plan-1`
**Goal:** new keyed i18n module — `messages/en.ts` + `messages/zh.ts` (canonical = English; key union
derived from it), `t(key, params?)` with named-param interpolation + fallback chain (current →
English → key), a language registry (`UILanguage` becomes a registered id), and a `tError(envelope)`
helper that localizes a backend `{code,params,message}` (consumes M2's envelope; English `message`
is the fallback). Migrate all 406 `txt()` call sites + 77 `STRINGS` tuples to keys; **delete**
`txt`/`STRINGS`/positional types at the end. No string dropped, no UI text changed in meaning.
**Hazards:** the migration is large (406 sites) — partition by component/dir and verify each renders
the same text; the anti-FOUC/language-toggle coupling (`ui-storage`) must keep working; type union
must make a missing key a compile error. **Verification gate:** `npm run lint && build` green; a
grep proving zero `txt(`/`STRINGS` references remain; spot-render check per major page.
**Stop-loss:** keep `txt` as an internal adapter delegating to `t()` only DURING the migration
commits; the final commit removes it (clean end state). Candidate for a migration Workflow
(pipeline over component groups → verify).

**Split executed (2026-06-14):** to keep each PR reviewable and isolate the large mechanical sweep,
M1 ships in two:
- **plan-1 = i18n CORE + error seam** (PR shipped): the new `i18n/` module (`messages/en.ts`+`zh.ts`,
  `t(lang, key, params?)` with interpolation + fallback, `tError(body, lang)` shape-tolerant over both
  the legacy `{error:string}` and the coded `{error:{code,message,params}}` shapes, language registry),
  seeded from the 77 `STRINGS` keys, with `tError` wired into the topologyStore error reads. `txt`/`STRINGS`
  retained transitionally so the app is unchanged. **Signature note:** `t` threads `lang` explicitly
  (not a global) so components that already subscribe to `language` keep their reactivity with a 1:1
  `txt(language,…)`→`t(language,…)` transform — still keyed/extensible (adding a language = add a catalog +
  widen the union + register; no call-site/signature change).
- **plan-1.5 = full call-site migration** (the 406 `txt()` + 81 `STRINGS.*` sites across 38 files → `t`,
  then delete `txt`/`STRINGS`), via a migration Workflow with disjoint per-component key namespaces +
  central catalog assembly + the strict `MessageKey` union proving completeness at build.

### M2 — Backend: typed error-code envelope + registry → `plan-2`
**Goal:** a typed domain error (`code string`, `params map`, default English `message`, HTTP status)
+ a central code registry; `writeError`/`ErrorResponse` evolve to emit `{"error":{code,params,
message}}`; the panic→500 path and login/passkey error bodies adopt it. Define the canonical code
list. **Hazards:** `topology_custody_test.go:90` asserts a string `error` — migrate it; keep the
default `message` populated so CLI/curl stay readable. **Verification gate:** `go test ./...` green
(with migrated assertions); curl shows the envelope; the custody-rejection error still names private
keys. **Stop-loss:** envelope is additive at the type level; revert as a unit.

### M3 — Backend: migrate user-facing strings to coded errors + client localization → `plan-3`
**Goal:** convert every user-facing `Errorf`/`writeError`/`Sprintf` in validator/compiler/render/
renderer/allocator/artifacts/api/cmd (the `findings.md` i18n-userfacing list, incl. `render.go:86,90,
127`, `cmd/compiler/main.go:57`) to typed coded errors with params; add the matching keys to the
frontend error catalog so the panel localizes them; English default messages for the CLI.
**Hazards:** validator messages are detail-rich — model them as codes with params, not one generic
code; deep errors bubble through `error` returns, so wrap-with-code at the source and unwrap at the
boundary. **Verification gate:** `go test ./...` green; a representative error round-trips coded →
localized in the panel; CLI prints English. **Stop-loss:** migrate package-by-package in separate
commits; each is independently revertable. May span plan-3 + plan-3.5 if scope is large.

### M4 — Frontend: mode-boundary parity + in-flight-compile guard → `plan-4`
**Goal:** add the controller-mode guard `compile()` has to `exportArtifacts()` and
`downloadDeployScript()`; make an in-flight `compile()` re-check mode before its success branch
writes reconstructed keys (abort/no-op if mode flipped to controller mid-flight). **Hazards:** these
actions are page-gated today (unreachable in controller mode) — the fix is defense-in-depth + the
real async race; don't regress the local-mode happy path. **Verification gate:** lint+build;
reasoning/trace that a mid-compile switch can't persist keys; the local flow still compiles/exports.
**Stop-loss:** small, independent guards; revert per function.

### M0 / M5 — Security fixes (from audit) → `plan-0` (only if CONFIRMED security findings)
**Goal:** fix every CONFIRMED `sec-authz`/`sec-custody`/`sec-injection` finding first. **Finalized
from `findings.md`.** If none CONFIRMED, this milestone is dropped and noted.

### M6 — Robustness fixes (from audit) → `plan-6`
**Goal:** fix CONFIRMED `robust-backend` + `robust-frontend` findings (races, nil/undefined derefs,
swallowed errors, persistence/rehydration ordering, data-loss paths). **Finalized from `findings.md`.**

### M7 — Backend structural cleanup (from audit) → `plan-7`
**Goal:** clear the highest-leverage CONFIRMED `struct-backend` debt (package boundaries, god-
functions, dup logic, Store interface/dup, dead defensive branches, config sprawl), consistent with
the new error-code system. **Finalized from `findings.md`.**

### M8 — Frontend structural cleanup (from audit) → `plan-8`
**Goal:** clear CONFIRMED `struct-frontend` debt (component decomposition, store coupling/prop-
drilling, weak typing, dup UI logic, API-error display path) — building on M1's i18n core.
**Finalized from `findings.md`.**

### M9 — Cross-cutting + docs + closure → `plan-9`
**Goal:** the cross-cutting structural items (error contract end-to-end, namespace-split cleanliness,
**test-coverage gaps** on controller/login/custody paths, doc/spec drift), then docs/spec updates for
the new i18n + error envelope, a migration note, and `/close-phase`. **Finalized from `findings.md`.**

### M10 — Controller-mode boundary: switch-security parity + Save core → `plan-10`
**Owns:** findings addendum T1 (mode-switch parity), T2 (Save primitive + dirty + conflict-warn +
gate-flush hardening), T7 (deploy confirmed-shrink flag), A3 (translucency boundary).
**Reads from specs:** panel-auth, panel-shell, panel-deploy-fleet, panel-design (+ controller-store,
controller-operator-api for the `update-topology` contract).
**Goal:** make the controller/local mode boundary leak-proof for *persistence and transitions*, and
give controller mode a first-class persist path. (a) Factor ONE shared mode-switch helper
(`serverHeld ? flushWorkspace : purgeModeBoundaryState` + `clearModeNotices` + `setMode`) used by BOTH
`LoginPage` and `SettingsPage` (today only LoginPage flushes a server-held canvas → SettingsPage leaks
fleet IPs/SSH to `localStorage`); fork the dialog copy on `canvasFromServer`; on local→controller purge
local-only artifacts + force `hydrateFromServer` when not server-held; restore `uiStore.translucency`
on controller→local. (b) Add `controllerStore.saveDesign()` (strip → `updateTopology` → mark
server-held → `lastSyncedAt`), a dirty-state selector (current vs last-synced snapshot via the
`hydrateFromServer` comparator), and client-side conflict detection (base snapshot at hydrate; re-GET
before save; warn + backup on divergence). (c) Harden `clearServerCanvasAtGate` to back up / confirm a
*dirty* mirror before flushing. (d) `deploy()` confirmed-shrink: `loadTopology(snapshot, true)` so the
custody flag never mislabels a divergent canvas.
**Hazards:** `flushWorkspace`/`exportProject` are security primitives reused by the gate-flush +
pre-hydration-backup paths — gate at the call site, never refuse the store action unconditionally
(would reintroduce the localStorage fleet-secret leak). The dirty/conflict snapshot must use the SAME
canonicalization the existing hydrate-diff uses (`controllerStore.ts:535-539`) or it will false-positive.
Key custody (HIGH): `saveDesign` MUST strip private keys before `updateTopology` (mirror deploy()).
**Verification gate:** `cd frontend && npm run lint && npm run build` green; `go test ./... && go vet
./...` green; subject-scoped tests — switch parity (both directions × serverHeld true/false flushes vs
purges; copy forks), save round-trip (calls `update-topology` only, never stage/promote; sets
server-held), conflict path (server-changed → warn, not overwrite), gate-flush backs up a dirty mirror.
Manual: edit in controller → Save → refresh → edits survive (now server-held + re-hydrated).
**Stop-loss:** each sub-part is an independent commit; the shared-helper refactor is revertable as a unit.

### M11 — Controller-mode boundary: UI mode-gating (both directions) → `plan-11`
**Owns:** T3 (Topbar I/O cluster + Save button surfacing), T4 (local-only constructs hidden in
controller), T5 (controller-only constructs hidden in local + fleet-cache clear).
**Reads from specs:** panel-shell, panel-design, panel-deploy-fleet, panel-auth.
**Goal:** every UI affordance renders only in the mode where it is meaningful. (a) Gate the Topbar
import/export/flush cluster to `mode==='local'` and surface the plan-10 **Save** button (+ dirty
indicator) on the Design surface in controller mode. (b) Hide local-only constructs in controller mode:
AuditView "Compile History"/config-diff, DeployPage `CompilePreview`, NodeEditor "Pin private key"
toggle + pinned-key panel, the `clearHistory` button. **Keep the Validate button** (D11 keeps `/validate`
usable as a controller preflight). (c) Hide controller-only constructs in local mode: render-gate
`FleetPage` (covers NodeRegistry + EnrollmentFlow), the Overview controller section, and the
Connection/Bootstrap settings sections; clear the persisted fleet cache on the local boundary.
**Hazards:** depends on plan-10's `saveDesign` + dirty selector. Use the established `useControllerStore((s)=>s.mode)`
gating idiom (CanvasToolbar.tsx:52, UserMenu.tsx:60); deep-link reachability must be gated, not just nav
visibility. Do not strand a local-only feature with no home (nav.ts already keeps Security visible in
local for compile history — keep that working).
**Verification gate:** lint+build green; per-page spot check in both modes (no local-only control in
controller, no controller-only control in local); Save button drives `saveDesign`; deep links to
`/fleet` in local mode redirect.
**Stop-loss:** per-surface commits; each gate is independently revertable.

### M12 — Backend mode-awareness: air-gap endpoints behind operator-auth → `plan-12`
**Owns:** T6.
**Reads from specs:** controller-operator-api, controller-agent-api (+ the server bootstrap/mux wiring).
**Goal:** in a controller deployment, mount `/api/compile`, `/api/export`, `/api/deploy-script`,
`/api/validate` behind the existing operator-auth middleware (per D11) so they are not an
unauthenticated compute / key-gen oracle on the operator port — while keeping `/validate` reachable for
the panel's authenticated preflight. A pure air-gap (local) deployment leaves them open as today.
**Hazards:** the server today has no explicit "mode" concept (T6) — determine the controller-vs-air-gap
signal from the existing bootstrap/config (the same thing that decides whether controller routes mount)
rather than inventing a parallel flag; do not break the air-gap CLI/local flow (those endpoints must
stay open in a non-controller deployment). Generated-bundle byte-equivalence is unaffected (no change to
compile output).
**Verification gate:** `go test ./... && go vet ./...` green; subject-scoped Go handler tests — in a
controller deployment the four endpoints return 401 without an operator session and 200/normal with one;
in an air-gap deployment they remain open. curl matrix.
**Stop-loss:** one middleware-wiring change; revertable as a unit.

## Insertion-point markers

- **plan-1.5 — i18n migration fallout:** if the 406-site migration uncovers strings that aren't
  simple zh/en (dynamic concatenations, strings built in non-component code, pluralization needs),
  STOP, update this outline, draft plan-1.5 (e.g. add a minimal plural/format rule to the core).
- **plan-3.5 — error-migration scope overflow:** if the backend string migration is too large for
  one session, split the remaining packages into plan-3.5.
- **plan-N.5 — audit surprise:** if a CONFIRMED finding contradicts a design-locked milestone (e.g.
  a security issue in the error envelope itself), STOP, log it, draft the insertion plan.

## Closure criteria

- [ ] M1–M4 merged via reviewed PRs; CI green on each.
- [ ] Every CONFIRMED audit security + robustness finding fixed or deferral-logged; finalized
      milestones (M0/M5–M9) merged.
- [ ] Success criteria in Mission demonstrably true (English-locale has no wrong-language string;
      add-a-language = one file; coded errors localize; mode boundary leak-proof).
- [ ] `grep` proves no `txt(`/`STRINGS` remain; `go test ./...` + `go vet` green; frontend
      lint+build green.
- [ ] docs/spec updated for the new i18n + error envelope; migration note written.
- [ ] STATUS.md regenerated; subject archived to `_completed/` via `/close-phase`; memory updated.

## Plan status table

| Plan | Status | PR | Notes |
|---|---|---|---|
| plan-0 (security) | **done** (9f1e7bb) | [#70](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/70) | ssh_key_path PowerShell command-injection: escape (bash/PS helpers) + validate (sshKeyPathCharset); perpetual injection-gate test; review 0-confirmed |
| plan-4 (mode-boundary parity) | **done** (22b0936) | [#71](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/71) | export/deploy-script controller-mode guards + in-flight compile mode-flip guard; review 0-confirmed |
| plan-6 (robustness) | **done** (660f92c) | [#72](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/72) | clear pendingShrink on deploy error + tolerant readApiErrorMessage; review 0-confirmed |
| plan-1 (i18n core + error seam) | **done** (75076e3) | [#73](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/73) | keyed catalog + t(lang,key,params)/tError + fallback + N-language; tError wired into error seam. Review caught an untracked-files blocker + mixed-language fallback (both fixed) |
| plan-2 (error envelope) | **done** (86c4014) | [#74](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/74) | internal/apierr leaf (Code+registry+Error) + nested {error:{code,message,params}} flip via writeError delegate; panic+custody coded. Review fixed WithMessage contract |
| plan-1.5 (full call-site migration) | **done** (d1f0cc6) | [#75](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/75) | 410 txt sites → t via TS-AST codemod (7 parameterized, 3 tuple→key); txt/STRINGS deleted; build proves completeness; review 0-confirmed |
| plan-3 (backend errors, bounded) | **done** (9a2fecd) | [#76](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/76) | render.GenerateKeys → 4 keygen codes (incl. owner's reported pinned-pubkey error, now 400+localized) + handler writeCodedOr relay + frontend error.keygen_* + fixed corrupted allocator/ip.go strings; review 0-confirmed |
| plan-10 (controller-mode boundary CORE) | **done** ([#79](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/79), 180280b) | [#79](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/79) | shared switchToLocal() (fixes SettingsPage fleet-secret localStorage leak) + saveDesign() (updateTopology-only) + dirty-state/canonicalDesign(omitempty-mirrored) + client-side conflict warn + gate-flush dirty-backup + deploy confirmed-shrink flag + A2 re-hydrate + A3 translucency. Reviewed (16 confirmed fixed + 1 re-review nested-omitempty). |
| plan-11 (controller-mode boundary UI gating) | **pending** | — | gate Topbar import/export/flush to local + surface Save; hide local-only constructs in controller (AuditView/CompilePreview/pin-key/clearHistory; keep Validate); render-gate FleetPage/Overview-controller/Connection+Bootstrap in local + clear fleet cache. Depends on plan-10. |
| plan-12 (backend mode-awareness) | **pending** | — | mount air-gap /api/compile\|export\|deploy-script\|validate behind operator-auth in a controller deployment (keep validate usable); Go tests 401/200. |
| plan-3.5 (remaining backend strings) | **pending** | — | code validator/compiler/auth/login/passkey/totp/bootstrap/cmd user-facing strings (validator set surfaces on validate/compile) + remove the writeError shim. Follow the apierr pattern (task #27). **After plan-10/11/12** (D14). |
| plan-7 (struct-backend) | **pending** | — | triage struct-backend PLAUSIBLE (P7 reserved-id helper easy; P3/P5/P6 — verifiers rated several overstated) |
| plan-8 (struct-frontend) | **pending** | — | triage struct-frontend PLAUSIBLE (god-store, coupling — verifiers rated quantitative claims wrong / core REFUTED) |
| plan-9 (cross-cutting + docs + closure) | **pending** | — | test-coverage gaps, doc/spec for new i18n+envelope, migration note, subject close |

### Status as of 2026-06-14 (checkpoint)

**Delivered (7 PRs merged, each independently reviewed → 0 confirmed after fixes):** all of the
owner's explicit asks — extensible keyed i18n (params/fallback/N-language), backend errors made
localizable (coded envelope), and the specific Chinese key-gen error fixed — plus every CONFIRMED
audit security finding (ssh_key_path injection) and robustness finding, the mode-boundary hardening,
and the two **highest-leverage structural fixes** the audit named (the i18n architecture redesign and
the error-code envelope, which together dissolve the bulk of the structural debt: sentinel-codes,
compile-error context, the {error:string} contract, the un-extendable i18n module).

**Remaining (well-scoped, lower-leverage):**
- **plan-3.5** — code the rest of the backend user-facing strings (validator is the largest set,
  surfaces on validate/compile; plus compiler/auth/login/passkey/totp/bootstrap/cmd), then delete the
  transitional `writeError` shim. The full pattern is established + demonstrated in plan-3; this is
  mechanical follow-through. **For an English-locale operator the remaining visible gap is the
  validator's Chinese messages on a validation failure.**
- **plan-7/8/9** — structural triage. The audit's struct PLAUSIBLE items were largely rated
  "real-but-minor / overstated / core-refuted" by the adversarial verifiers; the genuine high-leverage
  structural debt was already cleared by plan-1+plan-2. P7 (reserved-id helper, a 2-site dedup) is the
  clearest easy win. Plus docs/spec refresh for the new i18n + envelope, and the subject-close ritual.

**Owed (user-gated):** a release tag covering #70–#76 (outward-facing — the owner's call), and the
docs/spec refresh for the new i18n module + error envelope.
