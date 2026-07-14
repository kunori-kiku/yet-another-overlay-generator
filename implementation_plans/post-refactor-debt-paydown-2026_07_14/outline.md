# post-refactor-debt-paydown — residual debt, ship-correctness, and the security edges the last sweep missed

> Subject opened 2026-07-14 (owner request: "sweep the entire repository down to each file and scour
> for code/doc that are stale or whose logic is not clean that makes the development of this repo hard
> or understanding of this repo hard with workflows, and draft a subject that refactors. The production
> logic is also a thing to be considered.").
> **EXECUTED autonomously to closure (2026-07-14).** 13 of 14 plans merged to `main` (PRs #277–#292,
> each built → independently workflow-reviewed → fixed → re-reviewed → CI-green → merged); **plan-6
> (WebAuthn UV) is HELD as draft PR #282 pending an owner decision** (see Decisions log #7). The
> 10-agent adversarial review-at-last returned GO-WITH-FIXES; both findings were fixed (#291, #292)
> and the review loop is closed. Per-PR cadence used throughout: build → independent workflow review
> (correctness / completeness / hygiene / structure) → fix → re-review → CI green → merge.
>
> **This subject is the direct successor to `framework-refactor-2026_07_13`** (which shipped the
> WASM-unified core + machine-gates + god-file splits, PRs #260–#275). It targets what that program
> **deferred, left untouched, or newly created** — verified by a fresh comprehensive sweep that was
> explicitly briefed on everything framework-refactor shipped, so nothing here re-treads it.

## How this subject was assessed

Produced by two multi-agent workflows on `main` @ `915f672` (both anchored on a "do NOT re-report
what framework-refactor already shipped" brief):

1. **A 30-agent repo-wide debt sweep** — 15 deep readers (every backend package, every frontend area,
   docs, planning-state, build/CI, and a dedicated production-logic correctness lens) → a dedup/cluster
   synthesis → an adversarial skeptic pass per theme. 77 raw findings → **14 ranked themes** (the 2
   FE-restructure themes came back `overstated`/churn and are held out of scope, matching the
   framework-refactor design panel's rejection of directory-reorg churn).
2. **A 7-agent security-correctness gap-pass** closing the sweep's own flagged coverage gaps (bundle
   signing, WebAuthn, telemetry store, agent concurrency, build-reality-at-HEAD, keystone custody).
   Key **negative evidence: no trust-root bypass, no key leak, no shipped CVE — the controller/agent-
   managed paths are sound in every area.** The defects live in the *mirrors and edges*: the standalone
   `install.sh` verifier, an un-enforced UV flag, a self-test/reconcile comparator mismatch, and one
   unlocked custody handler. **4 confirmed defects + 2 lows.**

Both HIGH-security findings and both HIGH clean-code findings were **independently re-verified by hand**
(byte/line-accurate) before landing here.

## Mission

Eliminate the residual and newly-introduced debt that `framework-refactor` left behind — **fixing real
production/security defects first**, then finishing the structural paydown in the subsystems it did not
touch, then closing the doc/planning-state drift that misleads every resuming session — **without a
rewrite and without re-treading shipped work.**

**Success criteria:**
- **The WASM local engine actually ships** (it is the default+only in-browser engine, yet no shipped
  artifact contains `yaog.wasm` today) and its load path is fault-tolerant, gated by a red build.
- **The four confirmed correctness/security defects are fixed at root** and pinned by a test: the
  standalone-`install.sh` signed-set bypass, the self-update semver-vs-exact wedge, the WebAuthn UV
  downgrade, and the `HandleTrustListSignature` custody-lock gap.
- **`deploy.go` stops being an inferior second implementation** of the node teardown (it silently orphans
  mimic + drifts on SNAT) and joins the `ShellToken`/`go:embed` template regime (finishing plan-6.5).
- **The subsystems framework-refactor skipped are decomposed:** `cmd/agent/main.go`'s root
  self-replacing daemon loop (now untestable), the ~580-line `derivePeersWithDomains`, and the
  mis-named `handler_bootstrap.go`.
- **The last convention-held contracts become machine-gates** (camelCase controller wire DTOs) and
  the `Field` primitive adoption finishes.
- **The planning-state + docs stop lying:** the 6 delivered-but-unarchived subjects are archived,
  STATUS/MEMORY/CHANGELOG are reconciled, and the retired-airgap / deleted-TS-compiler prose and rotted
  citations are purged.
- **Every hard invariant below is preserved** (several hardened). No phase ships red or bricks a fleet.

## Principles (invariants the executor MUST respect)

Inherits **all** of [`PRINCIPLES.md`](../../PRINCIPLES.md) — deployable configs; allocation stability
(recompile = byte-identical for pre-existing entities); no unescaped user text to a root shell;
backend sole port authority; signed-artifact self-update custody; zero-knowledge key custody;
pure/stateless minimal-dep compiler; backward-compat persisted topologies; and the execution-discipline
rules (no shims, structure-aware hygienic code, no scope compromise, per-PR independent review → fix →
re-review). On top, this subject's own tripwires:

1. **Security fixes must not narrow the working path to close the attack path.** [HIGH] The
   `install.sh` coverage guard, the UV gate, and the custody lock must fail *closed* without breaking a
   legitimate deploy/login/rotation. *Violation:* a UV gate that locks out an already-enrolled
   authenticator with no migration; an `artifacts.json` guard that rejects a legitimately-signed
   no-catalog bundle.
2. **Behavior-preserving splits change zero generated bytes.** [HIGH] The `derivePeersWithDomains`
   split, the agent-daemon extraction, and the `deploy.go` templating must reproduce byte-identical
   goldens / rendered scripts (the `deploy.go` teardown *correctness* fix is a deliberate, reviewed
   byte change — isolated to the mimic + SNAT lines and pinned by a new fixture). *Violation:* a blind
   `-update` of goldens instead of a reviewed byte-diff; an allocation renumber under the peers split.
3. **The ship fix is proven by a red build, not by inspection.** [HIGH] After plan-2, a missing
   `yaog.wasm` in a shipped `dist` must FAIL the release, not 404 at runtime. *Violation:* wiring the
   build without the presence assertion — the exact gap that shipped this defect (CI proved the wasm
   *correct* while nothing proved it *shipped*).
4. **Doc/state edits are the only writes in the hygiene tier.** [MED] Tier-4 plans touch docs, planning
   folders, comments, CHANGELOG, and CI comments — never behavior. *Violation:* "while I'm here" logic
   changes folded into a doc-drift PR.
5. **The macro boundaries stay preserved and hardened** (inherited from framework-refactor's invariant
   set): the pure/stateful quarantine (arch-test), the auth chokepoint (adapter), the `Store`
   single-gateway, the `ShellToken` root-shell seam, the signed-bundle custody chain. This subject
   *extends* these gates (agent mux → adapter; `shQuote` → `ShellToken`; camelCase DTOs → drift gate);
   it never relaxes one. *Violation:* a new handler bypassing the adapter; a new render field bypassing
   `ShellToken`.

## Current state of the world (2026-07-14)

- Branch: `main` (clean) @ `915f672`. Last shipped: `v2.0.0-rc.5` (GitHub Latest — telemetry-history +
  delta-deploy). `framework-refactor` COMPLETE (14 phases, #260–#275) but its folder is **not yet
  archived** (this subject's plan-12 archives it with the other five stragglers).
- Backend ~68K Go LOC (incl. tests) / ~26 packages. Frontend ~22K TS/TSX LOC. `web/yaog.wasm` +
  `frontend/public/yaog.wasm` are **gitignored build artifacts, built only in CI** (never in the release
  or Docker pipelines — the plan-2 ship gap).
- **Verified at HEAD (gap-pass build-reality lens):** `go build ./...` + `go vet ./...` clean;
  `GOOS=js GOARCH=wasm go build ./cmd/wasm` succeeds; `test/realtunnel` + `cmd/e2eserver` +
  `cmd/e2eagent` still compile; real shipped toolchain is **go1.26.5** (GO-2026-5856 fixed;
  `govulncheck` gate intact — the release.yml `1.26.4` comment is stale, not a real vuln).
- CI: 7 required checks, branch protection LIVE on `main` (a `ci.yml` display-name edit must update
  protection in the SAME PR). `GOTOOLCHAIN=local` locally (go1.26.4 at `$HOME/.local/go/bin`).

## Must-read references

- **The sweep + gap-pass detail (this subject's evidence base):**
  `implementation_plans/post-refactor-debt-paydown-2026_07_14/ASSESSMENT.md` (the 14 themes with
  file:line evidence + the 6 security/correctness findings + the clean-area negative evidence).
- **The predecessor:** `implementation_plans/_completed/…/framework-refactor-2026_07_13/outline.md`
  (once archived by plan-12; today at `implementation_plans/framework-refactor-2026_07_13/`) +
  `docs/design/framework-refactor-proposal-2026_07_13.md` (what it deliberately deferred / left out of
  scope — this subject picks up several of those threads, but NOT the architectural ones below).
- **Project invariants:** `PRINCIPLES.md`.
- **Architecture:** `docs/spec/README.md`; `docs/spec/controller/{signing,key-custody,persistence,
  deploy}.md`; `docs/spec/artifacts/{install-script,mimic,deploy-scripts}.md`;
  `docs/spec/compiler/peer-derivation.md`.
- **Memory:** `review-each-pr-before-merge`, `worktrees-when-review-workflows-run`,
  `frontend-ci-uses-tsc-b`, `bootstrap-repins-operator-cred-by-default`, `framework-refactor-shipped`.

## Standing rules

Per `PRINCIPLES.md` + memory: per-PR independent workflow review → fix → re-review; reviews
checkout-free (`git show <ref>:<path>`); branch work in isolated worktrees when a background review
runs; verify locally (`go build/vet/test` incl. `-race`; `cd frontend && npm run build` [`tsc -b`] +
`npx vitest run` + `npm run lint`; `gofmt -l`) before pushing; **regenerate goldens to a REVIEWED
byte-diff, never a blind `-update`**; a `ci.yml` display-name change updates branch protection in the
same PR; no `--no-verify`, no amends, no force-push.

## Decisions log (2026-07-14)

1. **Subject shape ACCEPTED provisionally (owner AWAY during the scoping questions).** I proceeded on
   the recommended option for each of four decisions; each is marked PROVISIONAL and must be confirmed
   on the owner's return (re-ask if still material):
   - **Ship-breaker urgency (T1):** fixed-first as an early plan, within the normal rc cadence, **no
     out-of-band release** — the owner runs controller mode (server-side compile), so the live blast
     radius is the local-design SPA + the panel's in-browser Validate, not fleet deploys.
   - **Scope:** **comprehensive — all 4 tiers** (matches the explicit "comprehensive" ask +
     PRINCIPLES' no-scope-compromise), **excluding** the FE component-tree/`lib/` re-folder (T13), which
     the skeptic pass judged churn-not-worth-it.
   - **Security coverage gaps:** **ran the focused gap-pass now** (owner asked to weigh production
     logic); its 2 HIGH-security + 1 HIGH-availability + 1 MED findings are folded into Tier-1.
   - **Name:** `post-refactor-debt-paydown`.
2. **The 2 FE-restructure themes (topologyStore full-slice; god-component decomposition; `lib/`
   refolder) are OUT OF SCOPE** as standalone work — the skeptic + the framework-refactor design panel
   both class a directory/component reorg as churn without invariant payoff. The *genuine* FE items
   (finish `Field` adoption; the pin-collision safety dup) are kept (plan-11); a `topologyStore` slice
   is an OPTIONAL sub-item only if a natural seam falls out, never a goal.
3. **WebAuthn UV enforcement (plan-6) carries an owner DECISION GATE.** The fix (require the `0x04` UV
   bit) is correct and small, but hard-enforcing it could lock out an already-enrolled UV-incapable
   authenticator (a bare security key with no PIN/biometric). The skeptic dispositioned this
   "separate-security-audit" for exactly that reason. It is kept in-scope but plan-6 STOPS for owner
   confirmation ("do any enrolled operator authenticators lack UV?") before choosing hard-enforce vs.
   enforce-on-UV-capable-with-re-enrollment.
4. **Architectural (non-clean-code) items stay OUT OF SCOPE** (inherited deferrals from
   framework-refactor — each its own future gated subject, NOT folded here): `Node.ID` stable-artifact
   identity (changes live-fleet bytes), the FileStore single-file SPOF / no-HA, bootstrap-TOFU
   first-fetch pinning, the pinned-endpoint anti-roaming option.
5. **Plans-only at draft;** execution on the owner's go. This plans PR merges on CI (docs-only); the
   full multi-agent review regime applies to every EXECUTION PR.
6. **Pre-execution review COMPLETE (workflow `wld2zgik5`, 2026-07-14): verdict GO-WITH-FIXES.** All 14
   plans target real HEAD-confirmed defects (nothing flawed); plan-1/plan-2 sound; the other 12 corrected
   per [`REVIEW-CORRECTIONS.md`](REVIEW-CORRECTIONS.md) (AUTHORITATIVE — supersedes drafted specifics on
   conflict). Locked resolutions:
   - **plan-3** (BLOCKER): fix ALL FOUR `10.10.0.0/24` exact-match SNAT delete sites
     (`deploy.go:310/370/482/568`) + add mimic teardown to BOTH `renderBashDeploy` AND `renderPS1Deploy`
     (a `HasMimic` flag from `peerMap`, placed OUTSIDE the `!IsClient` block); MIRROR `install.sh.tmpl:35-42`
     VERBATIM (no invented `bpftool`/XDP-detach — that is a THIRD divergent teardown, the exact drift this
     plan kills); capture a `RenderDeployScripts` characterization golden as a RED gate BEFORE the templating
     half; restore the `field_safety_test` `reflect.Map`/`reflect.Interface` recursion hardening (T4); the
     PowerShell templating half defers to **plan-3.5**.
   - **plan-5** (BLOCKER): a NEW `internal/controller` sign method holding ONE `lockTenantOps` acquisition
     spanning read+substitution-guard+Verify+`PutSignedTrustList` (`lockTenantOps` is unexported to
     `internal/api`; serializing only the write leaves the `(M_old,B_new)` read-vs-restage window open);
     promote crash-atomicity → **DOC-NOTE** (skeptic disposition); member-2 → implement a durable-only
     `GetNodeRecord` read (do NOT relabel the real leak "keep it volatile"); `writeJSONL` LOG-and-continue
     on a post-`Write` `Close()` error (never early-return).
   - **plan-9** (BLOCKER): `field_safety` coverage of bootstrap fields is INFEASIBLE (`renderBootstrapScript`
     is an imperative `fmt.Fprintf` in `internal/api`, no struct/template; `internal/api` does not import
     `internal/renderer`) → descope to an api-local `renderer.ShellQuoted()` primitive-share + a unit test,
     DROP the field_safety claim; per-handler adapter disposition (`HandlePoll` keeps `opRaw` + the 204
     long-poll contract; `HandleEnroll` stays pre-auth hand-rolled); frame the agent-mux change as
     structural hygiene, NOT a security fix. Sequence AFTER plan-3 (shared `shelltoken.go`); **plan-9.5**
     marker for the `OP_FLAGS` unquoted-word fields.
   - **plan-10** step-2 → a SCOPED i18n orphan-sweep ONLY (delete the confirmed-dead
     `error.ts_topology_validation_failed`; NO naive bidirectional assertion — the `⊆` is deliberate; drop
     the "arch allow-list" item). Retarget the drift gate to the snake_case `*JSON`/`*Wire` interfaces (NOT
     camelCase `types/controller.ts`); enumerate ALL Go DTO sources (incl. `settingsJSON`).
   - **Sequencing/ownership:** plan-14 AFTER plan-2; plan-9 AFTER plan-3; the `TestControllerHTTP_AirGapOpen`
     rename + the `release.yml:18` toolchain comment are owned SOLELY by plan-13 (plan-10/plan-14 are pure
     pointers); coverage orphans pinned (localEngine vestige → plan-11; field_safety recursion → plan-3;
     orphan i18n key + `edgeDirection.ts` stale citations → plan-10/plan-13). A ci.yml ship-assertion that
     adds a required check needs the same-PR branch-protection PATCH (full list, `app_id:15368`).
7. **EXECUTION COMPLETE (2026-07-14): 13/14 merged, plan-6 held.** Executed per-PR with the full
   review regime; mechanical-heavy plans (3/5/7/8/9/10/11/12/13/14) were delegated to focused
   general-purpose agents and each diff independently re-verified by hand (byte-identity gate for the
   plan-8 peers split; `-race` for plan-4/5/7; the non-vacuity mutation test for the plan-10 drift gate;
   golden-diff-empty for the behavior-preserving splits). The 10-agent **review-at-last** (workflow,
   GO-WITH-FIXES) surfaced two real findings — a stray `./wasm` binary accidentally committed by a bare
   `go build ./cmd/wasm` (fixed #291, `/wasm` gitignored) and a client+tcp mimic-teardown gap in
   `deploy.go` (fixed #292, `HasMimic` derived from the topology edge for clients) — both fixed and
   re-verified; the loop is closed. **plan-6 remains HELD** per decision 3: `verifyAssertion` also runs
   node-side in `VerifyMembership` on every config fetch, so hard-enforcing the UV flag fleet-bricks
   config fetches if any signed manifest was not UV-signed — SAFE only if every enrolled operator
   authenticator does UV; the owner must confirm before #282 merges, else the fix routes through
   plan-6.5 (per-credential enforce + re-enroll). **The subject is deliberately NOT archived to
   `_completed/` while plan-6 is owed.**

## Milestones (one plan file each — ordered by payoff-per-risk: fixes → paydown → hygiene)

### Tier 1 — Correctness & security fixes (small, high-value; fixed first)

- **plan-1 — Standalone-verifier signed-set hardening (HIGH security).** Mirror the agent's
  `verify.go:225-229` `artifacts.json` coverage guard into `install.sh.tmpl` (and `client-install.sh.tmpl`
  if it reads `artifacts.json`): after the `bundle.sig`/`sha256sum -c` verify, refuse a present-but-
  unlisted `artifacts.json` before any `.deb` pin is read. Fix the false "already integrity-verified
  above" comment (`install.sh.tmpl:368`). **Stop-loss:** a legitimately-signed *no-catalog* bundle has
  no `artifacts.json` at all → the guard is presence-conditional (exactly like `verify.go`), never a
  hard require. Pin with a new golden/script test proving an injected unlisted `artifacts.json` is
  refused. **Verify:** goldens byte-diff (only the guard lines added); `go test ./internal/renderer/…`;
  a shell-level assertion. Risk: **low**.

- **plan-2 — WASM local engine: ship it everywhere + fault-tolerant load (HIGH, ship-breaker).**
  release.yml `build-frontend`: add `setup-go` + `npm run build:wasm` (→ populates `frontend/public/`)
  BEFORE `build`/`build:local`; Dockerfile: build `web/yaog.wasm` in the Go stage and `COPY` it into
  `frontend/public/` before the node build; add a **red-build assertion** that `dist/{yaog.wasm,
  wasm_exec.js}` + `dist-local/…` exist (`if-no-files-found`-class). Make `ensureWasm()` reset
  `loadPromise` on rejection (`wasmEngine.ts:60-65`) + the sibling in `lib/localEngine.ts`, with a
  recovery vitest. Turn the orphaned `YAOG_WASM_SOAK` webkit+firefox soak into a real scheduled (or
  documented-manual) job. Fix the stale `wasmEngine.ts:6,51` "default (TS) build" / `compiler/localEngine.ts`
  comments. **Verify:** a local `npm run build` then assert `dist/yaog.wasm` present; CI dry-run.
  Risk: **low** (config + a 2-line guard).

- **plan-3 — `deploy.go` teardown correctness + finish plan-6.5 shell templating (HIGH).** Add the
  missing mimic teardown (`systemctl stop/disable mimic@…`, XDP/TC program removal) and the
  transit-CIDR-aware SNAT delete to the `--uninstall` builder (`deploy.go:279-321`), mirroring the
  install.sh teardown (`script.go`, D38/D39) rather than the hard-coded `10.10.0.0/24` exact-match at
  `:310`. Then convert the 686-line dual-shell `fmt.Sprintf` builder to `go:embed` `*.sh.tmpl` /
  `*.ps1.tmpl` behind the `ShellToken` seam (the last hand-built shell string outside plan-6's regime).
  **Stop-loss:** the teardown byte-change is deliberate + fixture-pinned; the templating conversion is
  byte-identical to the current output for the non-teardown lines (reviewed diff). Risk: **med**
  (dual-shell token seam needs care — the plan-6.5 reason it was deferred).

- **plan-4 — Agent self-update correctness + brick-bound durability (HIGH availability).** Fix the
  exact-string version compare at `selfupdate.go:338/377/415` to use the semver comparator the self-test
  already uses (`:264` `compareVersions`, `version.go:59`), so a `v`-less operator target
  (`"2.0.0"` vs released `BuildVersion "v2.0.0"`) can no longer pass the swap then permanently wedge the
  channel (floor never advances, in-flight guard blocks all future updates, abandon records the running
  version). Add the T5 durability items (regression test for the wedge; the reconcile/finalize/rollback
  paths). **Verify:** `go test -race ./internal/agent/…`; a regression test reproducing the wedge then
  proving it cleared. Risk: **low** (contained; existing test seams).

- **plan-5 — Controller store + keystone-sign serialization correctness (MED, custody).** Serialize
  `HandleTrustListSignature` (`handler_keystone.go:297-367`) under `lockTenantOps` (as Enroll / Rekey /
  Stage / Promote already are) so a concurrent re-stage cannot leave a stale signed manifest paired with
  fresh bundles (the fail-closed `(M_old,B_new)` strand). Address the T6 cluster: the promote
  crash-atomicity window and the telemetry-durable overlay-leak partition (preserve FileStore's
  deliberate volatile overlay — do NOT force a durable write). Fix the low `writeJSONL` close-error
  requeue-dup (`telemetry_history.go:295-301`). **Stop-loss:** lock-ordering — audit for a deadlock
  against the existing `lockTenantOps` holders before wrapping. Risk: **med** (lock scope).

- **plan-6 — WebAuthn User-Verification enforcement (HIGH security; owner decision gate).** Add
  `flagUserVerified = 0x04` and require it (fail-closed) in the shared `verifyAssertion`
  (`webauthn.go:163-167`) — both ceremonies already request `userVerification:'required'`
  (`webauthn.ts:254,391`), so the server is the missing enforcement authority; a single gate fixes login
  + 2FA + keystone signing. Update `verify_assertion_test.go:27` to `UP|UV` + add a negative test.
  **DECISION GATE (per decision 3):** before hard-enforcing, confirm no enrolled operator authenticator
  is UV-incapable; if any is, the fix becomes enforce-on-capable + a re-enrollment path, not a blanket
  lockout. Risk: **low code / med policy** (STOP for owner input).

### Tier 2 — Structural paydown (the subsystems framework-refactor did not touch)

- **plan-7 — Agent daemon decomposition + tests for the root self-replacing loop (MED).** Extract the
  controller-mode run loop + heartbeat + coalescing kick from `cmd/agent/main.go`
  (`runRun:488` → `runControllerMode:626-863` + `runHeartbeat:874` + `tryKick:864`) into a testable
  unit (an `internal/agent` type), leaving `main.go` a thin CLI dispatcher; per-subcommand files for
  `keygen`/`kit`/`enroll`/`reprovision`. Add the tests the current god-`main` makes impossible (the
  self-replace control loop is un-covered today). **Stop-loss:** behavior-preserving; the daemon's
  observable output/telemetry unchanged. Risk: **med** (the decomposition is the risk; it unlocks the
  tests).

- **plan-8 — Compiler core: split `derivePeersWithDomains` + extract the link-grouping iterator +
  retire dead orientation (MED).** Split the ~580-line function (`peers_build.go:58-640`) into named
  helpers along its existing phases; extract the link-grouping loop the assessment found duplicated;
  delete the dead orientation code confirmed unreferenced. **Stop-loss:** STRONGLY gated —
  `allocation_stability_test` + the full golden corpus + the WASM conformance gate must stay byte-green;
  any diff is a bug in the split, never a fixture change. Risk: **med** (high friction, but hard-gated).

- **plan-9 — `internal/api` dedup + `handler_bootstrap` split + extend the adapter to the agent mux
  (LOW–MED).** Split the mis-named `handler_bootstrap.go` (it fuses Settings + Bootstrap + validators)
  into `handler_settings.go` + `handler_bootstrap.go`; route the local `shQuote:442` through the
  `ShellToken` seam (a seam bypass today); extend the structural auth-adapter's guarantee to the agent
  mux handlers; dedup the residual guard/identity/relay preambles the adapter did not absorb. **Verify:**
  `controller_http_test.go` + `no_anonymous_compute_test.go` stay green; the adapter's structural test
  covers the agent mux. Risk: **low** (mechanical, well-covered).

### Tier 3 — Machine-gate completion + tight FE

- **plan-10 — Wire-DTO camelCase drift gate + conformance-name honesty + bidirectional gates (LOW–MED).**
  Extend `internal/wiredrift` (or a sibling AST gate) to the camelCase controller wire DTOs the current
  drift gate does not cover; make any one-directional gate (i18n / arch allow-list) catch BOTH
  directions where it should; correct any dishonest conformance/test names. **Verify:** the new gate is
  proven non-vacuous (mutate → red). Risk: **low**.

- **plan-11 — Finish plan-10 `Field` adoption + the real pin-collision safety dup (LOW–MED).** Migrate
  the residual hand-rolled form-field sites to `ui/Field.tsx` (framework-refactor plan-10 migrated 30;
  the sweep found stragglers); dedup the genuine pin-collision safety logic the FE duplicates.
  **OPTIONAL** (not a goal, only if a clean seam falls out): a narrow `topologyStore` slice. The T13
  full FE re-folder stays OUT (decision 2). **Verify:** vitest + e2e green; no rendered-output change.
  Risk: **low**.

### Tier 4 — Doc / planning-state hygiene (low risk, high friction-relief; no behavior change)

- **plan-12 — `implementation_plans/` + STATUS archival reconciliation (LOW).** `git mv` the six
  delivered-but-unarchived subjects into `_completed/` with status prefixes (framework-refactor,
  agent-feedback, beta9-smoke-hardening, pre-rc1, theme-and-mimic-fixes, beta16-smoke-hardening — fix
  theme-and-mimic's outline status table first); fix the mixed-controller-local-mode outline (its plans
  2/3/4 are merged/shipped as beta.15, tracked as "drafted" in three places; refresh its broken
  post-split anchors); reconcile STATUS.md (it calls framework-refactor both COMPLETE and DRAFTED; prune
  ~130 lines of falsified IN-PROGRESS time-capsules to the live items; add the missing beta16 entry) +
  MEMORY.md; append the 7 missing CHANGELOG footer compare-links (beta.10–16). **Verify:** STATUS/MEMORY
  self-consistent; `_completed/` matches STATUS's "archived" claims. Risk: **low** (bookkeeping).

- **plan-13 — Purge retired-airgap / deleted-TS-compiler prose + rotted citations (LOW).** Re-run
  `refresh-specs` against HEAD (or delete `specs/airgap-api.md` + strip the AIRGAP nodes/cross-refs the
  execute skill loads as ground truth); rewrite README (the headline bullet still sells the deleted TS
  compiler + build-tag airgap two lines above saying they were removed), CLAUDE.md (still documents the
  retired `//go:build airgap` boundary + four anonymous routes as live), RELEASING.md (build-version in
  future tense though live since beta.1), and the `localcompile`/`edgecase`/`keygen`/`io-contract`
  doc-comments (framed around the gone TS twin, incl. a factually-wrong live-caller list); rename the
  lying `TestControllerHTTP_AirGapOpen`; replace frozen absolute line-number citations with stable
  symbol references; fix the stale `release.yml:18` `go1.26.4` toolchain comment; add the missing agent
  self-update/keystone/telemetry lifecycle to the agent spec. **Verify:** no doc references a deleted
  route/handler/file as live; citations resolve. Risk: **low** (prose only — invariant 4).

- **plan-14 — Release/Docker pipeline hygiene + misc straggler cleanup (LOW).** The T8 pipeline items
  not folded into plan-2 (latest-tag policy, base-image ↔ go.mod toolchain alignment, dead scripts) +
  the T14 misc (confirmed dead code, remaining lying names, cosmetic stragglers). Opportunistic; batch
  what has not landed elsewhere. **Verify:** CI green; no behavior change. Risk: **low**.

## Insertion-point markers (likely plan-N.5 triggers)

- **plan-3.5** — the `deploy.go` templating follow-up deferred by plan-3 (which shipped Part A, the
  teardown *correctness* fix — all 4 SNAT sites + mimic teardown in both shells + the characterization
  golden + the `field_safety` map/interface recursion; and, in the final-review follow-up, the **client+tcp
  mimic derivation** — `RenderDeployScripts` now derives a client's `HasMimic` from the topology edge, since
  peerMap holds no client PeerInfo): the go:embed / `ShellToken` templating (Part B) — the PowerShell shell
  context has no `ShellQuoted`/`ShellRaw` constructor yet, so characterize + extend the seam rather than
  force a lossy conversion.
- **plan-6.5** if the owner reports an enrolled UV-incapable authenticator — draft the
  enforce-on-capable + re-enrollment migration rather than a blanket UV lockout.
- **plan-8.5** if the `derivePeersWithDomains` split surfaces a golden byte-diff that is NOT a split
  bug but a latent ordering dependency — STOP, characterize, reconcile with the owner before touching a
  fixture (allocation stability is HIGH).
- **plan-9.5** if the bootstrap `OP_FLAGS` RPID/Origin fields (spliced UNQUOTED by design, word-split into
  flags) need a shell-context seam the `ShellQuoted`/`ShellRaw` quoted-arg constructors cannot model —
  characterize the third context before forcing a lossy conversion.
- **plan-12.5** if archiving a subject folder reveals its tests were never migrated per the close ritual
  — migrate them first; do not `git mv` over a test-migration gap.

## Closure criteria

- [x] **13 of 14 plans merged**, each workflow-reviewed → fixed → re-reviewed → CI-green (#277–#290,
      +#291/#292 review follow-ups). plan-6 (WebAuthn UV) is **the owner-parked tier** — HELD as draft
      #282 pending the UV-capability decision (decision 3/7).
- [x] **The confirmed correctness/security defects are fixed + regression-pinned** (standalone
      `install.sh` signed-set guard; the self-update semver wedge; the trust-list-sign custody lock;
      the `deploy.go` mimic/SNAT teardown); **the WASM engine ships**, red-build asserted in both the
      release and Docker pipelines. ⏸ **The UV decision gate is the one open item** (plan-6, owner).
- [x] `deploy.go`'s teardown correctness landed (mimic teardown + CIDR-agnostic SNAT in both shells;
      the go:embed/`ShellToken` **templating half** is carried to plan-3.5 — PowerShell has no
      `ShellToken` constructor yet); `cmd/agent/main.go` (→ `ControllerLoop`), `derivePeersWithDomains`
      (byte-identical split), and `handler_bootstrap.go` are decomposed; the wire-DTO drift gate +
      finished `Field` adoption are green.
- [x] The 6 stale subjects are archived; STATUS/MEMORY/CHANGELOG reconciled; no doc references a deleted
      airgap route / TS-compiler file / rotted citation as live.
- [x] Every hard invariant above is demonstrably preserved (goldens byte-verified; no fleet regression;
      no security regression — the review found NO trust-root bypass / key leak / shipped CVE).
- [x] STATUS + memory closeout done. **Subject NOT archived to `_completed/` yet — deliberately held
      in `implementation_plans/` until plan-6 (WebAuthn UV) resolves with the owner** (then plan-6
      merges or plan-6.5 is drafted, and the subject archives per the close-phase ritual).

## Plan status table

| # | Plan | Tier | Status | PR |
|---|------|------|--------|-----|
| 1 | Standalone-verifier signed-set hardening | 1 | ✅ merged | #277 |
| 2 | WASM ship-everywhere + fault-tolerant load | 1 | ✅ merged | #278 |
| 3 | deploy.go teardown correctness (→ plan-3.5 templating) | 1 | ✅ merged | #279 (+#292 client-mimic follow-up) |
| 4 | Agent self-update correctness + durability | 1 | ✅ merged | #280 |
| 5 | Controller store + keystone-sign serialization | 1 | ✅ merged | #281 |
| 6 | WebAuthn UV enforcement (decision gate) | 1 | ⏸ **HELD** — owner gate | #282 (draft, OPEN) |
| 7 | Agent daemon decomposition + loop tests | 2 | ✅ merged | #283 |
| 8 | Compiler: split derivePeersWithDomains | 2 | ✅ merged | #284 |
| 9 | api dedup + handler_bootstrap split + agent-mux adapter | 2 | ✅ merged | #285 |
| 10 | Wire-DTO drift gate + i18n orphan + name honesty | 3 | ✅ merged | #286 |
| 11 | Finish Field adoption + pin-collision dup | 3 | ✅ merged | #287 |
| 12 | implementation_plans/ + STATUS archival reconcile | 4 | ✅ merged | #288 |
| 13 | Purge airgap/TS-compiler prose + rotted citations | 4 | ✅ merged | #289 |
| 14 | Release/Docker pipeline hygiene + misc | 4 | ✅ merged | #290 |

> **Post-execution follow-ups (final-review findings, both merged):** #291 removed a stray `./wasm`
> build binary a bare `go build ./cmd/wasm` had dropped at the repo root + gitignored `/wasm`; #292
> tore down a **client + tcp** node's mimic on `--uninstall` (peerMap carries no client PeerInfo, so
> `RenderDeployScripts` now derives the client's `HasMimic` from the topology edge). See the plan-3.5
> insertion marker for the deferred PowerShell templating half.
>
> **13 of 14 plans merged; plan-6 HELD for the owner's WebAuthn-UV decision.** The subject stays in
> `implementation_plans/` (NOT archived to `_completed/`) until plan-6 resolves — see Closure criteria.
