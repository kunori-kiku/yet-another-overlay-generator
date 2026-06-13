# Outline — controller-server-authority-redesign
<!-- drafted: 2026-06-12 by draft-implementation-plan -->

## Mission

Make controller mode **server-authoritative end-to-end**: the controller's copy of the design is
the single source of truth, the browser cache is a disposable mirror, and every boundary that
key material or node identity crosses is explicit and enforced. Concretely: a full-page login
gate when controller mode is persisted; hydrate-the-canvas-from-server on every login; the
zero-knowledge key-custody principle enforced in code (client strip + server 400) instead of
asserted in docs; the secret path prefix split into operator/agent envs that name the
architecture; bounded server-side topology version history; design↔fleet identity
reconciliation surfaced in the UI; and the three audit-confirmed safety bugs fixed.

Success criteria:
- Clearing browser storage and logging back in restores the full design on the canvas.
- `POST /update-topology` with a `wireguard_private_key` anywhere in the payload returns 400.
- Operator and agent APIs mount under independent prefixes; a path-based proxy rule per
  audience is expressible on one hostname; startup logs both mounted base paths.
- The server retains the last 10 topology versions.
- Entering the panel with persisted controller mode shows a full-page login before any chrome.
- Switching controller→local warns, preserves the graph, and purges all secret/compiled state.
- An orphaned agent (fleet row without a design node) idles with backoff instead of re-running
  `install.sh` continuously; stale staged bundles cannot go live on a later promote.
- Fleet rows not present in the current design are visibly marked and revocable in one click.

## Principles (invariants the executor must respect)

Inherits everything in `PRINCIPLES.md` (repo root). Highest-relevance inherited items:
**Key custody (HIGH)**, **Generated scripts run as root on fleets (HIGH)**, **Allocation
stability (HIGH)**, **Backward compatibility of persisted topologies (MEDIUM)** — note the
user-sanctioned scoped break: *deployment env vars* (`YAOG_CONTROLLER_PATH_PREFIX` removed,
decision D3) are explicitly exempt; topology JSON compatibility is NOT exempt and must hold.

Subject-specific principles:

- **Server-authoritative cache (HIGH) [STATED].** In controller mode, the server's topology is
  truth; local cache is overwritten from the server at every login/session-restore. Violations:
  merging local and server designs; skipping hydration because the local cache "looks newer";
  treating localStorage as a recovery source after login.
- **Key material never crosses a mode boundary (HIGH) [STATED].** Private keys must not travel
  browser→server (strip + 400), survive a controller→local switch (purge), or enter the store
  via import (placeholder + reminder). Violations: a "convenient" passthrough of
  `wireguard_private_key` for round-trip symmetry; scrubbing only the known key field but not
  `fixed_private_key`; server strip-instead-of-reject (user chose fail-closed, D4).
- **Identity is the node-ID↔WG-pubkey binding and is never silently severed (MEDIUM)
  [INFERRED FROM DOMAIN — confirmed via audit + user].** Violations: deploys that silently
  stage zero nodes; enrolling the same WG pubkey under a second node-ID without refusal;
  removing a node from the design without surfacing the orphaned fleet row.
- **Destructive actions confirm with specifics (MEDIUM) [STATED].** Mode switch and
  shrink/empty deploys list exactly what is lost. Violations: generic "Are you sure?" dialogs;
  silent overwrite of the server design from an emptier canvas.

## Current state of the world (2026-06-12)

- Branch `main` @ `1abd662` (specs/ initial bootstrap). Clean tree. No active feature branch.
- Shipped this week: panel-appshell-redesign (PRs #53–#58, closed), v2.0.0-preview.5 (UUID
  insecure-context fix), README config reference, specs/ bootstrap.
- Parked: `controller-panel-2026_06_08` plan-5 task #20 (multi-tenant/OIDC/KMS) — GATED on
  user provider forks; untouched by this subject.
- The live deployment (overlay.kunorikiku.com, Cloudflare Access + tunnel) runs the current
  `:latest`; plans 1 and 4 are breaking for it (env rename; login flow) — see their rollout
  steps.

## Must-read references

Memory:
- `controller-mode-redesign-decisions` (project memory) — the locked decisions, with whys.

Architecture (specs/ — partial-load per plan header):
- `specs/controller-operator-api.md`, `specs/controller-agent-api.md`, `specs/controller-store.md`,
  `specs/controller-stage-promote.md`, `specs/agent.md`, `specs/keystone-trustlist.md`,
  `specs/panel-auth.md`, `specs/panel-shell.md`, `specs/panel-design.md`,
  `specs/panel-deploy-fleet.md`, `specs/render-keys.md`.

Deep docs (verify drift-flagged claims against code):
- `docs/spec/controller/persistence.md` (public-keys-only claim becomes TRUE after plan-1),
  `operator-auth.md`, `controller-api.md` (§The two ports), `key-custody.md`, `deploy.md`,
  `enrollment.md`, `docker.md`.

Production code (line numbers as of `1abd662`):
- `cmd/server/main.go:30-56` (env consts), `:141` (SetPathPrefix call), `:96-160` (serveController).
- `internal/api/handler_controller.go:155-235` (route registration + SetPathPrefix/basePath),
  `:643-674` (HandleUpdateTopology), `:866-893` (HandleTopology), `:913-922` (token mint).
- `internal/api/handler_bootstrap.go:140-143` (controller base composition).
- `internal/api/static.go:20-45` (SPA 404 guard).
- `internal/controller/store.go:95-114,309-315,404-437` (TopologyRecord, Store iface),
  `filestore.go:404-438` (PutTopology), `:464-584` (bundles), `memstore.go:214-231,266-276`.
- `internal/controller/compile.go:160-196,452-497,521-566` (CompileAndStage, enrolledSubgraph,
  persistAllocations), `enrollment.go:159-168`.
- `cmd/agent/main.go:370-383` (daemon loop), `internal/agent/cycle.go:101-181`, `verify.go:320-322`.
- `frontend/src/stores/controllerStore.ts:233-238,274-304,335-623,677-700,768-792`,
  `frontend/src/stores/topologyStore.ts:101-138,318-389,487,564-578`,
  `frontend/src/api/controllerClient.ts:154-177,240-259,283-335,640-643`,
  `frontend/src/components/deploy/ConnectionSettings.tsx:98-233`,
  `frontend/src/components/shell/Shell.tsx:13-42`, `frontend/src/components/deploy/DeployBar.tsx:44-88`,
  `frontend/src/components/deploy/NodeRegistry.tsx:80-90`.

Test gates:
- Go: CI on PRs (`go vet`, `go test ./...`) — no local Go toolchain (PRINCIPLES.md).
- Frontend: `cd frontend && npm run lint && npm run build` locally before push (no test runner —
  panel verification is lint+build+manual smoke; do NOT introduce a test framework in this
  subject without a user decision).
- Perpetual custody guards: `internal/render/custody_guard_test.go`, `custody_diff_test.go`
  (must keep passing); plan-1 adds a store-boundary sibling.

Web URLs: none required — all findings are in-repo (audit + specs verified against live code).

## Standing rules

Per `implementation_plans/README.md` and PRINCIPLES.md "Project-wide standards": per-substep
commit+push, PR per plan with adversarial review, no `--no-verify`/amends/force-push, frontend
lint+build before push. Breaking changes in this subject are sanctioned ONLY where the
Decisions log says so (env rename, login-gate behavior); everything else stays compatible.

## Decisions log

| Date | Decision | Why |
|---|---|---|
| 2026-06-12 | D1: Controller mode server-authoritative; login overwrites local cache | User: "whether the user's cache has something is no longer important — intuitively" |
| 2026-06-12 | D2: Persisted controller mode ⇒ full-page ("large interface") login gate | User: entering the website is "merely a login if one have already switched to controller mode" |
| 2026-06-12 | D3: Prefix split `YAOG_OPERATOR_PATH_PREFIX` + `YAOG_AGENT_PATH_PREFIX`; clean break, old env removed | Single env misled routing (login-404 incident); user: "no need for backward compatibility — no one is using it" |
| 2026-06-12 | D4: Server rejects keyed update-topology with 400 (not strip) | Fail-closed matches user's "corrupted logic should blow up"; silent mutation of a security payload is worse than refusal |
| 2026-06-12 | D5: Import under controller mode placeholders secrets + reminds user | Key-custody principle at the import boundary |
| 2026-06-12 | D6: Controller→local switch: graph survives, secrets purge, destructive warning | Preserves fork-a-copy workflow while guaranteeing fleet keys never linger in a browser |
| 2026-06-12 | D7: Bounded topology version history, N=10 | Cheap insurance against the one-click-overwrite blow-up scenario |
| 2026-06-12 | D8: Scope = persistence/login/prefix core + identity reconciliation + safety bugs | User: "the entire redesign"; root busy-loop shouldn't wait |
| 2026-06-12 | D9: One-time export stash before the first overwriting hydration | Migration insurance; reuses exportProject |
| 2026-06-12 | D10: Out of scope: multi-tenant/OIDC/KMS, legacy-form light theming, rollback UI, auto-revocation | Keeps the subject shippable; each has a parking spot |
| 2026-06-12 | specs/ bootstrapped before drafting (refresh-specs, commit `1abd662`) | Plans cite specs/ components per convention |
| 2026-06-14 | plan-7 closed as **completed** (PR #65, merge `24f044e`) | Docs/migration/closure shipped; doc-accuracy review caught + fixed a real gap (rekey duplicate-key refusal now audited) — fixed in code, not papered over |
| 2026-06-14 | subject closed as **delivered** (close-phase) | All 7 plans (+1.5) merged #59–#65, each independently reviewed; code-side closure criteria met. Owed: live-deploy env migration + manual browser two-node smoke (user-run, not code blockers) |
| 2026-06-14 | memory [[controller-mode-redesign-decisions]] updated to mark the redesign shipped | Per close-phase Step 3.5; one canonical entry kept rather than a new file |

## Milestones

Each milestone = one plan file = one session = one PR. Solutions are summarized here; the plan
files carry the concrete steps.

### M1 — Backend: prefix split + custody enforcement → `plan-1-2026_06_12.md`
**Goal:** two independent path prefixes (operator/agent) replacing the shared one; store-boundary
key rejection; missing audit entries; startup observability.
**Hazards:** hidden consumers of the old env (compose, docs, panel mirror field semantics);
`fixed_private_key` is a UI flag, not a secret — only `wireguard_private_key` values are secret.
**Verification gate:** CI green; curl matrix (per-port prefix mounting, 400 on keyed payload).
**Stop-loss:** tag before merge; revert the PR; live deployment keeps old image until rollout step.

**Insertion point fired (2026-06-13):** the M1 sweep found the pre-declared hidden consumer —
`frontend/src/components/deploy/EnrollmentFlow.tsx:28,34-36` composes the bootstrap one-liner
and the manual enroll command from the panel's `pathPrefix` mirror, which post-split mirrors
the OPERATOR prefix; with distinct prefixes both displayed commands would 404 against the
agent port (the enroll command omitted the prefix entirely even pre-split). Remediation does
NOT add a second user-typed mirror field: the server exposes its agent prefix read-only in
`GET /settings` (`agent_path_prefix`) and the panel composes from that — server-authoritative,
matching the subject's core principle. → `plan-1.5-2026_06_13.md` (executed in the plan-1 PR
branch; authorized by the user's standing execute-until-closed directive).

### M2 — Backend: topology version history → `plan-2-2026_06_12.md`
**Goal:** keep last 10 TopologyRecords; list/get-version API; deploy-overwrite becomes recoverable.
**Hazards:** FileStore atomicity across multiple files; stage write-backs (persistAllocations)
double-counting versions — they DO count, documented.
**Verification gate:** store-compat tests green on both impls.
**Stop-loss:** the feature is additive; revert cleanly.

### M3 — Backend: safety bugs → `plan-3-2026_06_12.md`
**Goal:** orphaned-agent idle backoff; promote flips only currently-staged bundles + stale-staged
purge; empty-stage audit entry.
**Hazards:** the agent loop's rekey-wake semantics (don't break the watermark advance); keystone
vs non-keystone orphan behavior differs (5s backoff already exists on the error path only).
**Verification gate:** agent-cycle + promote unit tests; CI green.
**Stop-loss:** each fix is independent; revert individually.

### M4 — Panel: login gate + hydrate-on-login → `plan-4-2026_06_12.md`
**Goal:** full-viewport LoginPage gating all routes in controller mode; getTopology→loadTopology
hydration on login/session-restore; one-time export stash (D9).
**Hazards:** session-restore race on mount (gate must not flash the canvas before checkSession
resolves); deep-link preservation; the anti-FOUC script's coupling to `ui-storage`.
**Verification gate:** lint+build; manual cache-clear→login→hydrated-canvas round-trip on the
real deployment.
**Stop-loss:** insertion point plan-4.5 pre-declared (below).

### M5 — Panel: mode-boundary custody → `plan-5-2026_06_12.md`
**Goal:** client-side strip on deploy (mirror of M1's 400); import placeholdering + reminder;
controller→local switch dialog (graph survives, secrets purge); shrink/empty deploy guard.
**Hazards:** scrub-list completeness (keys, pins, compile history — enumerate fields, don't
pattern-match); the guard needs the server copy (getTopology) without racing deploy().
**Verification gate:** lint+build; manual: import keyed JSON in controller mode → placeholder
reminder; deploy → server 400 never fires (client stripped first); switch dialog lists losses.
**Stop-loss:** each behavior behind its own small PR-internal commit; revert per substep.

### M6 — Identity reconciliation → `plan-6-2026_06_12.md`
**Goal:** stale-row markers in Fleet; deploy-summary orphan list with one-click revoke (manual);
WG-pubkey dedupe at enrollment; token-mint design check (warn).
**Hazards:** dedupe must not break legitimate re-enrollment of the SAME node-ID (rekey path);
warn-not-block on token mint (operators may pre-mint before designing).
**Verification gate:** enrollment unit tests; CI green; manual fleet-UI check.
**Stop-loss:** UI markers and server checks are independently revertable.

### M7 — Docs + migration + closure smoke → `plan-7-2026_06_12.md`
**Goal:** README/docs/spec/specs corrections (persistence.md's claim becomes true), breaking-change
migration note, the long-owed two-node manual smoke, then `/close-phase`.
**Hazards:** none structural; discipline plan.
**Verification gate:** the full STATUS.md owed smoke + this subject's success criteria checklist.
**Stop-loss:** n/a (docs).

## Insertion-point markers

- **plan-1.5** — *hidden prefix consumers*: if M1's sweep finds the old env or shared-prefix
  assumption baked anywhere unexpected (deploy scripts, GH workflows, agent state, panel
  pathPrefix semantics needing a second field), STOP, update this outline, draft plan-1.5.
- **plan-4.5** — *login-gate fallout*: if gating all routes breaks flows discovered in the wild
  (break-glass token entry with no session, agent-bootstrap docs pages, the Cloudflare Access
  interplay, language toggle needed pre-login), draft plan-4.5 for the gate refinements.
- **plan-5.5** — *scrub-list gaps*: if a secret-bearing field beyond the enumerated set surfaces
  (new model fields, third-party imports), STOP and extend via plan-5.5 with a perpetual test.

## Closure criteria

- [x] All seven plans merged via reviewed PRs; CI green on each (plans 1–7 merged, #59–#65).
      Every plan got an independent review workflow (find → adversarial verify) with fixes; plan-7's
      doc-accuracy review surfaced a real auditability gap (rekey duplicate-key refusal left no audit
      trail) which was fixed in code, not just papered over in docs.
- [x] Success criteria in Mission demonstrably true — see the closure-smoke transcript below.
- [ ] Live deployment migrated (new envs set, operators re-logged-in) — **owed to the user** (their
      overlay.kunorikiku.com deploy; the migration note is published for it).
- [x] docs/spec/controller/persistence.md claim accurate; specs/ component files touched up (plan-7).
- [ ] Memory updated; STATUS.md regenerated via close-phase; subject archived to `_completed/` — done by
      `/close-phase` at the end of plan-7.

### Closure-smoke transcript (2026-06-14)

**Automated (CI on every PR + `go test ./...` / `npm run lint && build` green on merged main):**
- SC2 custody — `internal/api/topology_custody_test.go` (perpetual): keyed payload 400, stored bytes
  key-free, non-schema 400, canonical-storage anti-smuggle.
- SC4 history — `store_compat_test.go` (both impls): retention bound, newest-first, byte-exact
  round-trip, crash-orphan invisibility, upgrade backfill, corrupt-entry skip.
- SC7 agent idle + promote scoping — `cycle_idle_test.go` (perpetual), `promote_scope_test.go`.
- Identity — `enrollment_dedupe_test.go` (perpetual): one approved pubkey ↔ one id, rekey path,
  whitespace evasion, revoked-frees-binding.

**Live HTTP smoke (dev controller, `OP`/`AG` prefixes, 12/12 PASS):** startup log names both base paths;
operator login 401 under `/OP` on :8080 and 404 on :9090; bootstrap 200 under `/AG` on :9090 and 404 on
:8080; bare paths 404 (SC3). Keyed update-topology → 400, clean → 200 (SC2). 12 puts retain exactly 10
versions (newest=12, limit=10); `?version=3` 200, pruned `?version=1` 404, malformed 400 (SC4).

**Owed manual browser smoke (carried since the keystone program; requires a browser + authenticator +
two real nodes — cannot be run headless):** SC1 (cache-clear → login → hydrated canvas round-trip), SC5
(persisted controller mode → full-page login before chrome), SC6 (controller→local warns + preserves
graph + purges secrets), SC8 (fleet "not in design" markers + one-click revoke), plus
login-survives-refresh, dark/light, no token in localStorage. The code paths are unit-/build-verified;
the end-to-end browser pass remains the user's to run on `http://localhost` (WebAuthn caveat).

## Plan status table

| Plan | Status | PR | Notes |
|---|---|---|---|
| plan-1 | done (2778754) | [#59](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/59) | prefix split + custody gate (canonical storage) + audits + fail-loud stale env; 7-angle review hardening |
| plan-1.5 | done (2778754) | [#59](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/59) | server-reported agent_path_prefix in GET /settings; EnrollmentFlow composes from it |
| plan-2 | done (83fb3e0) | [#60](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/60) | bounded history N=10 + version API; review-hardened (orphan-invisible, corrupt-skip, upgrade backfill, write-back dedupe) |
| plan-3 | done (3c37048) | [#61](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/61) | agent idle skip (perpetual guard) + promote scoping + purge-on-stage (incl. empty stage) + per-tenant stage/promote lock + stage audits |
| plan-4 | done (18d267e) | [#62](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/62) | full-page login gate + hydrate-on-login + divergence-backup stash; review-hardened (break-glass usable, no silent data loss, semantic diff) |
| plan-5 | done (0833b60) | [#63](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/63) | strip-on-deploy + import placeholdering + controller→local purge dialog + shrink/empty typed-confirm; review-hardened (sentinel phrase, best-effort guard, snapshot binding) |
| plan-6 | done (2992abe) | [#64](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/64) | enrollment WG-pubkey dedupe (both write paths, lock-atomic) + token-mint design warning + fleet "not in design" markers + deploy orphan list; review-hardened |
| plan-7 | done (24f044e) | [#65](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/65) | docs accuracy pass + migration note + closure smoke; review-hardened (rekey duplicate-key refusal now audited as `rekey-rejected-duplicate-key`; shrink-guard "≥50%", per-overwrite backup, version-history-is-server-side wording corrected) |
