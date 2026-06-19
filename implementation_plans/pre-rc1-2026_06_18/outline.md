# Outline — Pre-rc.1 program (YAOG `v2.0.0-rc.1` push)

> The program spine for the 22-plan stack that drives YAOG from `v2.0.0-beta.8` to a tag-able
> `v2.0.0-rc.1`. Each `plan-<N>-2026_06_18.md` in this folder is one milestone; this file is the
> mission, principles, current-state snapshot, decisions log, sequencing, and live status table that
> every executing session reads FIRST. Authoritative number→subject index and the cross-plan coherence
> arbitrations live in `docs/spec/rc1/plans/00-program-plan.md` (the master draft); this outline mirrors
> them for the executor and adds the plan-file pointers + status table.

---

## 1. Mission

Drive YAOG from `v2.0.0-beta.8` to a tag-able `v2.0.0-rc.1` by executing four subjects **in a locked
order**, each gating the next, ending in a single explicit rc.1 gate. The spine is: **SUBJECT 1**
(refactor + residual security/correctness sweep, then the harness-FIRST Go↔TS migration so local-mode
can become fully browser-resident without ever risking byte drift) → **SUBJECT 2** (phone UX on the
shipped app-shell, touching no compiler/model/Go code) → **SUBJECT 3** (browser E2E rig + adversarial
corpus + the MANDATORY real-tunnel netns tier) → **SUBJECT 4** (a diff-aware adversarial re-audit of the
frozen tree, feeding the rc.1 go/no-go). The owner directive (2026-06-18) locks the subject order and two
non-negotiables: the Go↔TS **conformance harness (plan-5 / 1.5) is built FIRST** and the migration is
**isolated** from the active PR stream; and **plan-18 / 3.6 real-tunnel integration is MANDATORY**, a hard
rc.1 blocker, not advisory.

**Success criteria (mission-level):**
- The four subjects merge in order (1→2→3→4); SUBJECT 4 audits a FROZEN tree (the TS port, the phone
  surface, and the E2E/real-tunnel rig are all inside the diff it sweeps).
- The Go↔TS conformance harness (plan-5) is **green and a permanent required CI check** before ANY
  TypeScript compiler code (plan-4) merges — harness-FIRST, drift never ships.
- The real-tunnel netns tier (plan-18) is **green and a required CI check**: generated WireGuard+Babel
  configs bring up real tunnels and routes converge (a hard rc.1 blocker).
- The beta.8 blockers (B1/F1/C1/S4/S5/S6) stay closed and regression-locked.
- The residual Go-side rc.1 security/correctness sweep (plan-8) lands: S1 FULL (cap + ctx), C2, C3, B2
  FIXED, B3 FIXED, S9/S10 DOCUMENTED.
- rc.1 ships as a GitHub `--latest` tag (an OWNER OVERRIDE 2026-06-18 of `RELEASING.md`'s prerelease
  default; beta.8 is demoted from `Latest`); plan-22 is the sole tag authority.

---

## 2. Principles (invariants the executor must respect)

Project-wide source: **`PRINCIPLES.md`** (loaded by execute-implementation-plan during principle-risk
assessment). Every principle below inherits from that file; the program adds the migration-specific ones.
Tag = `[STATED]` (written in PRINCIPLES.md / the master plan / owner directive) or `[INFERRED FROM
DOMAIN]`. Risk = HIGH/MED/LOW (silent-violation blast radius).

- **P1 — Byte-stable incremental deploys / allocation stability (superset rule).** `[STATED]` **HIGH.**
  Recompiling a superset topology MUST reproduce identical allocated values (overlay IP, ports, transit
  pairs, link-locals, keys) for every pre-existing entity; rendered config files must be byte-identical
  under cosmetic input churn (e.g. edge reorder — the beta.8 **C1** babel-sort fix). *Violation:*
  order-dependent counters renumber existing links; babeld.conf line order tracks edge-array position;
  re-randomizing a node's key on recompile. (Investigation I2/I8; guarded by plan-3 contract + plan-5
  harness + `babel_test.go:16`.)
- **P2 — Zero-knowledge key custody.** `[STATED]` **HIGH.** For controller-managed nodes, WireGuard
  PRIVATE keys are generated and held agent-side and NEVER reach the controller, its DB, or its bundles;
  the controller stores public keys only. The TS port and the harness assert **public-key derivation
  only** (fixed per-node private keys in fixtures; X25519 KAT). *Violation:* a private key round-trips
  into a bundle/manifest; the TS keygen emits or logs a private key; a fixture pins a private key as a
  conformance output.
- **P3 — Controller-only backend after migration.** `[STATED]` **HIGH.** Post-migration (plan-6 cutover
  soaked), local design/validate/compile/export run entirely client-side; the anonymous air-gap compute
  routes are removed from the default/controller build (plan-7), leaving the backend controller-only.
  *Violation:* shipping the anonymous `/api/compile` surface in the default build after cutover; making
  the controller depend on the air-gap compute path. (NOTE the build-tag nuance in Decision D-air-gap:
  the routes survive behind `-tags airgap` as an oracle/dev build — they are removed from the DEFAULT
  build, not deleted outright.)
- **P4 — Go↔TS conformance (no drift).** `[STATED]` **HIGH.** The TS compiler reimplementation
  (plan-4) and the Go compiler MUST stay byte-identical forever across the frozen contract corpus.
  The harness (plan-5) is the oracle and a PERMANENT required gate — not a one-time cutover check.
  *Violation:* a TS renderer emits a differently-ordered/spaced file than Go; `normalizeEdges.ts` drift
  ships uncaught (the report's already-extant precedent); a TS commit lands before the harness is green.
- **P5 — No silent precision / output-equivalence trades.** `[INFERRED FROM DOMAIN]` **HIGH.**
  "Equivalent" output is NOT acceptable where the contract says "byte-identical": JSON key order, integer
  formatting, trailing newlines, CIDR canonicalization, sort stability all matter because the consumer is
  `wg-quick`/`babeld`/a signed bundle digest. *Violation:* a renderer that produces semantically-equal
  but byte-different output and a test that asserts only semantic equality; masking a field in the golden
  corpus that is actually load-bearing (e.g. `bundle.sig` digest) to make a flaky test pass.
- **P6 — Single source of truth for shared constants/fields.** `[STATED]` **MED.** Each shared constant
  or model field has exactly ONE owning home. Transit/alloc constants single-source into the NEW
  `internal/allocconst` leaf (plan-8, Decision D-transit). The FE error-code names derive from the i18n
  catalog (the FE single-source), NOT a fourth codes.ts mirror (plan-5 catalog-sync guard). The
  `router_id` TS field is added once, by plan-9 (Decision D-routerid). *Violation:* two plans move the
  same const to two packages; a codes.ts codegen creates a fourth code-name mirror; `router_id` re-added
  by plan-4 instead of consumed.
- **P7 — Generated configs must be deployable; scripts run as root.** `[STATED]` **HIGH.** Every rendered
  artifact must be accepted by its consumer; no unescaped user-controlled text reaches a shell context;
  integrity anchors in Go-emitted constants. The real-tunnel tier (plan-18) is the end-to-end proof.
  *Violation:* `Table = off` with no route installer; ports > 65535; placeholder keys; bare `%s`
  interpolation of `ssh_host`/node-name into bash; a self-extracting installer with no payload hash.
- **P8 — Signed-artifact self-update custody.** `[STATED]` **HIGH.** An agent NEVER executes a
  self-fetched binary it has not verified against a SHA-256 pin in the controller-signed, keystone-bound
  `artifacts.json`; never downgrades below the health-confirmed `AgentVersionFloor`. (Beta.8 territory;
  the re-audit and fleet-lifecycle smokes must not regress it.) *Violation:* trusting an upstream
  `.sha256` sidecar; putting the pin in unsigned `manifest.json`; advancing the floor before a
  health-confirmed update.
- **P9 — Backward compatibility of persisted topologies.** `[STATED]` **MED.** Topology JSON /
  localStorage from prior releases must load and compile after every change; new model fields are
  `omitempty`/optional. The `router_id` field and any normalization (plan-9 `is_enabled`) must not break
  an older saved topology.
- **P10 — Migration isolation; harness-first ordering.** `[STATED]` **HIGH.** The 1.3→1.5→1.4→1.6 stream
  forks onto a dedicated `feat/ts-compiler` branch OFF the active PR stream; `drift_risk` is FALSE today
  and the program keeps it FALSE by never letting TS land ahead of the harness. plan-3 uses
  wrap-not-move discipline (a façade; it does not relocate `peers.go`/`semantic.go` bodies). *Violation:*
  merging TS into the active stream; plan-3 moving compiler bodies instead of wrapping; plan-4 landing
  before plan-5 is a required green gate.

---

## 3. Current state of the world (2026-06-18)

- **On `main`** (the canonical branch); the program's work branches fork from it / from the beta.8 tag.
- **`v2.0.0-beta.8` shipped as GitHub `Latest`** — tag at `e765da1` (rolled), PR #136, fix commit
  `c335be0`. It is the diff baseline floor for SUBJECT 4's sweep (plan-21 records a single captured BASE
  — the beta.8 tag or the Subject-1 branch point, never the loose `c335be0`).
- **The 6 beta.8 blockers are CLOSED** and must stay closed (regression-locked, green-stays-green):
  | ID | What shipped | Anchor | Now guarded by |
  |----|--------------|--------|----------------|
  | **B1** | `recoverPanics` wraps operator + agent muxes (not just air-gap) → coded 5xx | `server.go:185-200` + `:219`/`:248` | plan-22 owns `CodeInternalPanic` pin; plan-16 regression-locks |
  | **F1** | `getTrustlist` via shared `request()` → refreshed cookie-session operator can keystone-sign | `controllerClient.ts:971-986` | plan-14 owns post-reload regression; plan-15/16 reference |
  | **C1** | babeld.conf peer slice sorted by `InterfaceName` → byte-stable under edge reorder | `babel.go:116-185`; `babel_test.go:16` | plan-3 verify-only; plan-5 makes it byte-assertable cross-language |
  | **S4** | revoked-resurrection / same-id hostile re-enroll guard | `enrollment.go` (Enroll guard) | plan-15 regression-locks |
  | **S5** | enrollment-token purge-on-revoke | `handler_controller.go` (HandleRevoke) | plan-15 regression-locks |
  | **S6** | enrollment-token TTL server-side cap | `handler_controller.go:1377-1378` | plan-16 / plan-8 reference |
- The keystone/trust regression suite (`internal/regression/keystone_regression_test.go`, 9 adversarial
  black-box scenarios, no `t.Skip`, in CI) is the single highest-value tested asset and stays the floor.
- **NOT YET rc.1.** The residual rc.1 work is exactly what this stack plans: the rest of the Go-side
  security/correctness sweep (S1 allocator compile-DoS, C2/C3, B2/B3, S9/S10), the harness-first TS local-
  mode migration, phone UX, full-stack simulation, and the final re-audit + gate.

---

## 4. Must-read references

- **Memory:** `~/.claude/projects/-home-kunorikiku-source-yet-another-overlay-generator/memory/MEMORY.md`
  (project facts, Go-toolchain availability, recent releases) + the per-subject notes it links.
- **Project instructions:** repo `CLAUDE.md` (architecture, dev commands) + global `~/.claude/CLAUDE.md`
  (secret-handling, MCP retry cap).
- **Investigation grounding:** `docs/spec/rc1/investigation-report.md` (the headline verdict + S1–S12,
  B1–B4, C1–C3 findings with file:line) and `docs/spec/rc1/investigation-findings.json` (the structured
  finding corpus).
- **Master roadmap + coherence:** `docs/spec/rc1/plans/00-program-plan.md` (authoritative number→subject
  index, the six resolved coherence gaps G1–G6, file-ownership table) and
  `docs/spec/rc1/_coherence_and_questions.json` (the gaps + the open owner questions).
- **Principles:** `PRINCIPLES.md` (project-wide invariants).
- **Key code anchors (for the executor, by area):** allocator DoS `internal/allocator/ip.go:143`;
  schema caps `internal/validator/schema.go:76,160-163,214-227`; peer derivation
  `internal/compiler/peers.go` (transit const `:21`, gap-fill `:938`); babel sort `internal/renderer/
  babel.go:116-185`; air-gap routes `internal/api/server.go:101-120,84`; panic recovery
  `internal/api/server.go:185-200`; controller god-file `internal/api/handler_controller.go`
  (`handleOperatorCredentialPin :1690`, S6 TTL `:1377`); SSRF `internal/api/release_pins.go`; passkey
  origin `internal/api/webauthn.go:170`; FE types `frontend/src/types/topology.ts` (`router_id` Go side
  `internal/model/topology.go:87`); FE store `frontend/src/stores/topologyStore.ts`; FE air-gap calls
  `frontend/src/api/controllerClient.ts:971-986`; the already-drifting `frontend/src/lib/normalizeEdges.ts`.
- **CI workflows:** `.github/workflows/ci.yml` (job `go`) and `.github/workflows/release.yml` (job
  `gate-go`) — the `-race` add, `govulncheck`, the `frontend-e2e` / `realtunnel` required-check wiring
  are ratified in plan-22.

---

## 5. Standing rules

- **Follow the memory.** The MEMORY.md notes are load-bearing (Go-toolchain location + GOPROXY mirror,
  release history, the "bootstrap re-pins operator-cred by default" / "FE CI uses tsc -b" gotchas). Read
  before improvising near any of those areas.
- **Review each PR before merge.** Run an independent review workflow per PR, re-review after fixes,
  structure-aware clean-code; finish the whole subject before stopping. (MEMORY: review-each-pr-before-merge.)
- **Verify the frontend with `npm run build`** (tsc -b, the stricter mode CI runs), NOT bare
  `tsc --noEmit`, before pushing FE changes. Run `cd frontend && npm run lint && npm run build`.
- **Verify Go changes locally where possible** (`go build ./... && go vet ./... && go test ./...`,
  `-race` as a local spot-check) but CI is the authoritative gate; the toolchain CDN is sinkholed here so
  modules go through the `goproxy.cn` mirror (persisted in `~/.bashrc`).
- **No `--no-verify`, no amends, no force-push.** Per-substep commit + push (execute-implementation-plan).
- **Improvise ONLY when both principle risk AND implementation risk are low.** For higher-risk decisions,
  STOP and ask the owner to authorize an insertion-point plan-N.5 (see §8).

---

## 6. Decisions log (program checkpoints)

Decisions the owner has LOCKED (carry into execution unconditionally):

- **D1 — Full program before rc.1.** All four subjects gate rc.1; the gate is run once at the end
  (plan-22). LOCKED.
- **D2 — Local-mode-fully-frontend is a TypeScript reimplementation, NOT Go→WASM.** A pure
  side-effect-free `frontend/src/compiler/` library (plan-4), consumed by the store cutover (plan-6).
  LOCKED.
- **D3 — Conformance harness FIRST; migration ISOLATED.** plan-5 green + permanent required CI gate
  BEFORE any TS (plan-4) merges; the 1.3→1.5→1.4→1.6 stream forks onto `feat/ts-compiler` off the active
  PR stream; `drift_risk` is FALSE and must stay FALSE. LOCKED (owner directive 2026-06-18).
- **D4 — plan-18 / 3.6 real-tunnel integration is MANDATORY** — a hard rc.1 blocker and its own required
  CI status check (`realtunnel`), broken OUT of the `frontend-e2e` grouping. Overrides the 3.6 file's
  on-disk "advisory" framing (coherence gap G2). LOCKED (owner directive 2026-06-18).
- **D5 — Single combined plan folder + write-then-review.** All 22 plans live in this one
  `pre-rc1-2026_06_18/` folder; each is written then reviewed against the master index. LOCKED.
- **D6 — rc.1 ships as GitHub `--latest` (PROMOTE rc.1; beta.8 demoted from `Latest`).** plan-22 cuts rc.1
  as `--latest` (or post-tag `gh release edit v2.0.0-rc.1 --latest` + demote beta.8 with
  `gh release edit v2.0.0-beta.8 --latest=false`). LOCKED 2026-06-18 as an explicit **OWNER OVERRIDE** of
  `RELEASING.md`'s `--prerelease`/NON-latest default for release-candidate tags.
- **D-coherence (G1, G3, G4 resolutions — LOCKED by the master index, reconcile the per-plan files):**
  - **D-air-gap (G1):** Air-gap removal MECHANISM = **keep the routes behind a `-tags airgap` build**, do
    NOT plain-DELETE. The default/controller build stops registering them (the security delta); the
    `-tags airgap` build retains them as the local-design oracle + the boot target for plan-13's
    `--mode airgap` E2E and plan-21's `-tags airgap` DAST. plan-7's SUMMARY "plain DELETE" is overruled
    by its own FILE + the two consumers.
  - **D-routerid (G3):** **plan-9 OWNS** the `router_id?` field in `frontend/src/types/topology.ts` + the
    NodeEditor UI (lands EARLY, outside the isolated TS stream). plan-4 CONSUMES ("confirm present, added
    by plan-9"). plan-3 freezes `router_id` in the Go-side `model`/`io-contract.md` only (the Go field
    exists at `topology.go:87`); plan-3 does NOT add the TS field. plan-6 verifies the local compile
    round-trips it (NOT "plan-5 adds it" — that was doubly wrong).
  - **D-transit (G4):** **plan-8 OWNS all alloc-constant single-sourcing**, target = a NEW leaf
    `internal/allocconst` (`defaultTransitCIDR` + `backupDefaultLinkCost` + `minPinnedPort`). plan-2
    touches NO shared constant and NO `peers.go` body; plan-2's Phase 1.2 const move to `internal/model`
    is STRUCK. `deploy.go`'s hardcoded `10.10.0.0/24` SNAT literals are a SEPARATE behavior-bug follow-up
    under plan-8.
  - **D-handlersplit (G6):** **plan-2 splits `handler_controller.go` FIRST, then plan-8 re-anchors** its
    `handleOperatorCredentialPin` (:1690) + S6 TTL (:1377) edits onto the new sibling file (mirrors the
    1.1→1.8 ordering for `peers.go`/`semantic.go`/`script.go`).
  - **D-S1-full (R4):** plan-8 lands **S1 allocator compile-DoS FULL (cap + `context.Context`) NOW** as an
    rc.1 blocker; only S3's cursor optimization rides with plan-7. Do NOT defer S1-ctx — that is the
    single owner-facing reclassification footgun.

**Decisions resolved 2026-06-18** (the owner has now signed off on the three previously-flagged knobs;
they are LOCKED into the LOCKED list above — no remaining "pending"/"recommended-but-unconfirmed" framing):

- **[RESOLVED → LOCKED] Air-gap removal mechanism = build-tag (NOT plain DELETE).** The four air-gap
  compute routes (`/api/validate`, `/api/compile`, `/api/export`, `/api/deploy-script`) STAY in the
  codebase behind `//go:build airgap`; the DEFAULT/controller build stops registering them (the security
  delta — no unauthenticated compute oracle ships in the controller), while an `-tags airgap` build
  RETAINS them as the local-design oracle + the boot target for plan-13's `--mode airgap` E2E and
  plan-21's `-tags airgap` DAST. This was already the outline's lean; now LOCKED (see D-air-gap above).
  plan-7's SUMMARY "plain DELETE" is overruled.
- **[RESOLVED → LOCKED] Shared allocation-constant home = NEW `internal/allocconst` leaf, OWNED BY
  plan-8.** `defaultTransitCIDR` + `backupDefaultLinkCost` + `minPinnedPort` single-source into the new
  leaf package; plan-2 moves/owns NO shared allocation constant (its Phase 1.2 const move to
  `internal/model` is STRUCK — plan-2 keeps only its god-file split + the type-only Artifact/InstallFetch
  hoists into `internal/model`, which are unrelated value types). Already the outline's lean; now LOCKED
  (see D-transit above).
- **[RESOLVED → LOCKED] F2 / `router_id` TS-field owner = plan-9 owns, plan-4 consumes.** Closes the
  1.4:22 "1.3 adds" + 1.6:30 "1.5 adds" inconsistencies (see D-routerid above). LOCKED.
- **[RESOLVED → LOCKED] rc.1 release mechanics = GitHub `--latest` (PROMOTE rc.1).** OWNER OVERRIDE
  2026-06-18 of `RELEASING.md`'s `--prerelease`/NON-latest default: plan-22 PROMOTES rc.1 to `--latest`
  and DEMOTES beta.8 from `Latest`. Supersedes the outline's prior `--prerelease`/non-latest
  recommendation (see D6 above).

Other genuinely-open knobs flagged in the per-plan files (recommendations recorded there, not blocking
the spine): B2 fsync fix-vs-document, B3 origin enforce-vs-accept, S9/S10 accept-vs-temper (all plan-8 /
plan-22 D-decisions, recommended FIX/DOCUMENT respectively); plan-18 netns Option-A-subset vs systemd
container execution mode; plan-21 npm-audit advisory-vs-required threshold; plan-13 frontend-e2e
required-from-day-one vs advisory-until-20×-green.

**Decision (2026-06-19) — local-engine default-ON is folded into plan-7 (the soak gate is waived, replaced by the green harness).**
plan-6 shipped the local-engine seam **default-OFF** and deferred the default-ON flip + dead-fetch removal to its
"post-soak" Phase 7. But plan-7's whole purpose — gating the air-gap compute routes off the DEFAULT controller build —
requires local mode to be browser-resident (it must NOT depend on the server routes), and task #49 / the program spine
run plan-7 right after plan-6 with **"No real-world soak gate."** Reconciliation: the plan-5 conformance harness (green,
required) is the program's *mechanical* drift guarantee that REPLACES real-world soak (the harness-FIRST thesis). So
plan-7 completes plan-6's deferred Phase 7 as its Phase 0.5 — **flip `localEngineEnabled()` default-ON (`!== 'backend'`),
remove the dead local-mode `fetch` branches** (retain `readApiErrorMessage` for controller validate) — making local mode
browser-resident by default, THEN gates the now-unused routes behind `//go:build airgap`. The `VITE_YAOG_LOCAL_ENGINE=backend`
escape hatch is retained (works against a `-tags airgap` server). Reversible via the flag; justified by the green harness +
the expanded corpus (validator 93/97, all render/alloc branches byte-pinned). rc.1 is itself the soak vehicle.

**Findings discovered during plan-3 execution (2026-06-18; recorded so they are not lost — none block
plan-3, which is a no-byte-change freeze):**

- **C5 — `install.sh` / `deploy-all.{sh,ps1}` / `checksums.sha256` are NOT edge-reorder-stable.** The
  beta.8 C1 fix sorted ONLY the babel renderer's peer slice; the compiler-level edge-reorder golden
  fixture (owner flag 1) proved `babeld.conf` / `sysctl` / every `wireguard/<iface>.conf` ARE byte-stable
  under a benign edge reorder, but the install/deploy scripts still enumerate per-peer interfaces/nodes in
  edge-array order, so a wholesale edge reversal churns those files + the per-node `checksums.sha256` that
  covers `install.sh`. This is a C1-CLASS residual beyond the shipped babel fix. plan-3 freezes + documents
  it as an explicit non-guarantee (`io-contract.md` §5; the golden assertion is scoped to the C1-covered
  surface). **Disposition:** a NEW roadmap fix (sort the script/deploy renderers' interface enumeration by
  `InterfaceName`, mirroring babel) — a fast-follow or a plan-7/plan-16 rider; NOT a plan-3 byte change.
  Practical blast radius is small (normal incremental edits append; they do not reverse the edge array).
- **Corpus role-coverage gap → plan-5.** The plan-3 golden corpus (12 fixtures) matches the plan's
  fixture list but uses only `router` (+ one `client`) roles, no `gateway`/`relay`/`peer`, no
  `extra_prefixes`, no IPv6 overlay CIDR, no edgeless node. Those carry distinct babel/capability byte
  surfaces. **Disposition:** plan-5 owns the cross-language **coverage floor** — widen the corpus there
  (or via a plan-5.5) before plan-4 ports the TS compiler.
- **Signer double-resolution (latent, fast-follow).** On the air-gap export path the bundle signer is
  resolved twice from the SAME `YAOG_BUNDLE_SIGNING_KEY` read (the façade's `req.SigningKey` for the
  install.sh-embedded pubkey + `artifacts.Export` re-reading env for `bundle.sig`). They agree in
  practice; collapsing Export to consume the façade's already-computed signatures is a fast-follow, out of
  plan-3's no-byte-change scope.

---

## 7. Milestones (one bullet per plan; goal + the sequencing spine)

**SUBJECT 1 — refactor + security (S1).** Two parallel tracks: clean-tree prep + rc.1 blockers on the
normal branch; the harness-FIRST TS migration on the isolated `feat/ts-compiler` branch.

- **plan-1 (1.1)** `plan-1-2026_06_18.md` — Code-hygiene normalization: CJK→English comments + test
  strings, JSX-comment forms, stub godoc fill. *FIRST mover on shared files; lands before plan-2/8/9
  touch them.*
- **plan-2 (1.2)** `plan-2-2026_06_18.md` — Backend structural redesign: god-file splits (incl.
  `handler_controller.go`), type-only hoists (Artifact/InstallFetch → `internal/model`), package
  boundaries. *After plan-1; splits `handler_controller.go` BEFORE plan-8 re-anchors (D-handlersplit).*
- **plan-3 (1.3)** `plan-3-2026_06_18.md` — Compiler extraction + frozen I/O contract: `internal/
  localcompile` façade + `docs/spec/compiler/io-contract.md` + Keygen seam + `testdata/contract/` golden
  corpus. *BEFORE plan-4 and plan-5; wrap-not-move; Phase 3 = verify-only for C1.*
- **plan-4 (1.4)** `plan-4-2026_06_18.md` — TypeScript compiler reimplementation: `frontend/src/compiler/`
  pure side-effect-free library (model/validators/allocator/peers/renderers/keygen/export/index).
  *AFTER plan-5 is green+required; consumes plan-9's `router_id`; `peers.ts` ported LAST.*
- **plan-5 (1.5)** `plan-5-2026_06_18.md` — Go↔TS conformance harness + CI gate: `internal/conformance/`
  byte-equality harness + catalog-sync guard + KAT + drift manifest + vitest runner. *AFTER plan-3,
  BEFORE plan-4 — harness FIRST; green + permanent required gate is the M2 gate.*
- **plan-6 (1.6)** `plan-6-2026_06_18.md` — Local-mode rewire: rewire `topologyStore.ts` local-mode
  validate/compile/exportArtifacts/downloadDeployScript to call plan-4's library; `VITE_YAOG_LOCAL_ENGINE`
  flag default OFF; canary; cutover. *AFTER plan-4; does NOT remove air-gap routes.*
- **plan-7 (1.7)** `plan-7-2026_06_18.md` — Backend shrink + deployment split: stop registering anonymous
  air-gap routes in the default build (keep behind `-tags airgap`, D-air-gap) + `VITE_LOCAL_ONLY` static
  build. *IN-PROGRAM, pre-rc.1: the tail of Subject 1 — AFTER plan-6 (which need only have LANDED, not a
  real-world beta soak; correctness is gated by plan-5 conformance being green+required), and BEFORE
  Subject 4's re-audit, because the shrunken anonymous surface is an audit INPUT. plan-8's S3 cursor
  optimization is timed to ride alongside this air-gap removal but is plan-8's code, not plan-7's.*
- **plan-8 (1.8)** `plan-8-2026_06_18.md` — Remaining security + compiler-correctness fixes: S1 FULL
  (cap+ctx) NOW, C2 heal-on-reenable, C3 endpoint-derived HasPublicIP, B2 fsync FIX, B3 login-origin FIX,
  S9/S10 DOCUMENT, alloc-const single-sourcing → `internal/allocconst`. *Normal branch, in parallel;
  after plan-1 comments + plan-2 handler split.*
- **plan-9 (1.9)** `plan-9-2026_06_18.md` — FE↔Go model drift reconciliation: `router_id?` TS field +
  NodeEditor UI, `is_enabled` normalization, F3 drift-guard handoff to plan-5. *EARLY off main, outside
  the isolated TS stream, so plan-4 CONSUMES (not re-adds) `router_id`.*

  *S1 spine (execution order, ALL gating rc.1 — nothing parked):* `plan-1 → plan-9 → plan-2 → plan-8 →
  plan-3 → plan-5 → plan-4 → plan-6 → plan-7`, then Subject-1 closure. **plan-8 lands BEFORE plan-3** so
  plan-3 freezes the I/O contract + golden corpus (and plan-4 ports the TS compiler) against plan-8's
  FIXED compiler behavior (C2 heal-on-reenable, C3 endpoint-derived HasPublicIP) rather than the pre-fix
  bugs — avoiding a corpus re-freeze + TS re-port. **plan-5 green+required before any plan-4 TS merges**
  (locked harness-first). plan-7 is the tail of Subject 1, BEFORE Subject 4's re-audit (the shrunken
  anonymous surface is an audit input).

**SUBJECT 2 — phone UX (S2).** Built on the shipped app-shell; no Go/compiler/model surface.

- **plan-10 (2.1)** `plan-10-2026_06_18.md` — Responsive operator surfaces: operator-page responsive pass;
  consumes plan-11's primitives; owns the off-canvas sidebar drawer consumer.
- **plan-11 (2.2)** `plan-11-2026_06_18.md` — App-shell mobile adaptation: shared
  `useMediaQuery`/`Drawer`/`Sheet` primitive + overlay-correctness contract + `lg=1024` boundary
  (`LG_QUERY`). *FIRST in S2; sole owner of `useMediaQuery.ts`.*
- **plan-12 (2.3)** `plan-12-2026_06_18.md` — Canvas small-screen handling: canvas read-only gate
  (`CanvasGate.tsx`) below lg; consumes plan-11's hook.

  *S2 spine:* `plan-11 (primitive) → plan-10 (operator pages) + plan-12 (canvas gate)`. SUBJECT 1 must
  merge first.

**SUBJECT 3 — full-stack simulation / pitfall hunt (S3).** E2E rig + adversarial corpus + the MANDATORY
real-tunnel tier.

- **plan-13 (3.1)** `plan-13-2026_06_18.md` — E2E harness foundation: Playwright rig + `cmd/e2eserver`
  (`--mode controller` | `--mode airgap`) + `cmd/e2eagent` two-boot harness + fixtures + CI job. *FIRST
  in S3; all scenario plans consume its scaffold.*
- **plan-14 (3.2)** `plan-14-2026_06_18.md` — Operator-flow smokes: login/session/deploy/export/import/
  revoke specs (owns the F1 post-reload keystone-sign regression).
- **plan-15 (3.3)** `plan-15-2026_06_18.md` — Fleet-lifecycle smokes: enroll/rekey/clear-rekey/
  reprovision/heal specs (regression-locks S4/S5).
- **plan-16 (3.4)** `plan-16-2026_06_18.md` — Edge-case & adversarial-usage hunt: adversarial Go corpus/
  fuzz/DoS (Engine B) + Playwright adversarial specs (Engine A); supplies the class-tagged bringup corpus
  plan-18 consumes; regression-locks B1.
- **plan-17 (3.5)** `plan-17-2026_06_18.md` — Phone/responsive smokes: device-emulation +
  visual-regression verifying SUBJECT 2. *After plans 10–12.*
- **plan-18 (3.6)** `plan-18-2026_06_18.md` — **Real-tunnel integration (MANDATORY rc.1 gate):** netns
  `realtunnel` tier brings up generated WG+Babel, asserts per-interface handshake + babel route
  convergence + 0%-loss overlay ping + SNAT rewrite; 20× bake-in before tag. *Required CI check; consumes
  plan-16's bringup fixtures.*
- **plan-19 (3.7)** `plan-19-2026_06_18.md` — Residual manual-smoke list: triage owed manual smokes →
  irreducible hardware-only residue; feeds plan-22's ledger. *LAST in S3, AFTER plan-18 subtracts the
  data-plane legs.*

  *S3 spine:* `plan-13 (harness) → {plan-14, plan-15, plan-16, plan-17, plan-18} → plan-19`. plan-13
  before 14–18; plan-17 after S2; plan-18 before plan-22's gate.

**SUBJECT 4 — re-audit (LAST) + gate (S4).** Audits a frozen tree.

- **plan-20 (4.1)** `plan-20-2026_06_18.md` — Re-audit charter (post-refactor): surface taxonomy, exit
  bar EB1–EB8 (incl. EB7 realtunnel), methodology.
- **plan-21 (4.2)** `plan-21-2026_06_18.md` — Diff-aware adversarial sweep: DAST + diff-derived threat
  re-derivation + SCA against the frozen tree (single captured BASE); SUBJECT-4 verdict.
- **plan-22 (4.3)** `plan-22-2026_06_18.md` — rc.1 gate criteria (`RC1-GATE.md`): the go/no-go gate; sole
  tag authority; owns the `-race` CI add, `govulncheck` REQUIRED, and the `frontend-e2e` + `realtunnel`
  required-check wiring.

  *S4 spine:* `plan-20 (charter) → plan-21 (sweep) → plan-22 (gate; tags rc.1)`.

---

## 8. Insertion-point markers (where a plan-N.5 might be needed)

These are the program's top failure modes; if execution hits one and the fix is NOT both low-principle-
and low-implementation-risk, STOP and ask the owner to authorize an insertion-point plan-N.5 (per
execute-implementation-plan).

- **After plan-5, before plan-4 — drift the harness can't yet catch.** If plan-5's corpus turns out to
  miss a class of output divergence (e.g. an un-fixtured renderer path, or a JSON field the harness masks
  that is actually load-bearing), a **plan-5.5** widens the corpus/coverage floor BEFORE plan-4 ports
  against it. Harness gaps are the #1 program risk (R1).
- **Inside plan-4 — `peers.ts` (the 1215-line risk concentrate).** If the densest port (transit/port
  gap-fill, two-phase derivation) cannot be made byte-identical without a contract change, a **plan-4.5**
  re-freezes the contract + corpus rather than shipping an "equivalent" divergence (violates P4/P5).
- **Inside plan-18 — real-WG-in-CI flake.** If the netns tier cannot reach a 20×-green determinism bar
  within the risk budget, a **plan-18.5** narrows the MVV floor (single-host netns, simple-mesh + the C3
  fixture) or moves to the systemd-container execution mode — but it stays MANDATORY (D4). Real-tunnel
  flake is program risk R2.
- **After plan-21 — a late HIGH in SUBJECT 4.** If the diff-aware sweep surfaces an open HIGH, a
  **plan-21.5** (or a beta.9 + gate re-run) is required before plan-22 can tick the gate; plan-22 budgets
  +0.5 session for this (R6).
- **File-ownership collision surfaces mid-execution.** If two plans are found editing the same const/file
  in conflicting ways beyond the resolved G1/G4/G6, STOP — the resolution belongs in the master index,
  not improvised (R5).

---

## 9. Closure criteria

rc.1 is tag-able iff ALL hold (the full gate is owned by plan-22 / `RC1-GATE.md`; this is the executive
checklist):

- [ ] **Subjects merged in order** 1→2→3→4; SUBJECT 4 swept a frozen tree.
- [ ] **A1 — beta.8 blockers stay closed.** B1/F1/C1/S4/S5/S6 regression-locked (plan-14/15/16 specs green).
- [ ] **A2 — SUBJECT-1 rc.1 security/correctness landed.** plan-8: S1 FULL (cap+ctx), C2, C3, B2 FIXED,
      B3 FIXED; S9/S10 DOCUMENTED (D1–D4 recorded as LANDED).
- [ ] **B3 — `1.5` conformance harness GREEN + a permanent required check** (the Go↔TS byte-equality
      gate; guards plan-3's corpus + `normalizeEdges.ts` regardless of whether the TS port slips).
- [ ] **B4 — `frontend-e2e` GREEN + required** (plan-13 rig + plans 14–17 scenarios).
- [ ] **B6 — `realtunnel` GREEN + required** (plan-18, MANDATORY): per-interface WG handshake; babel
      route convergence to every `OverlayIP/32`; end-to-end overlay ping 0% loss; SNAT transit→overlay
      rewrite; 20× bake-in. **A distinct required status check, broken OUT of `frontend-e2e`.**
- [ ] **B5 — CI mirrors are true.** `-race` non-optional in `ci.yml` job `go` AND `release.yml` `gate-go`
      over `./...`; `govulncheck` REQUIRED; `gosec`/`npm audit` ADVISORY (plan-22 ratifies).
- [ ] **C — pitfall + audit triage:** plan-16 findings + plan-21 sweep findings triaged; zero open HIGH;
      plan-20 charter surfaces (N1–N4) dispositioned.
- [ ] **C1 — residual smokes:** plan-19 `RUNBOOK.md` reduces the owed-smoke ledger to the irreducible
      hardware-only residue (authenticator firmware/UX + mimic eBPF/DKMS/XDP + one narrow real-NAT
      property); run-or-explicitly-accepted.
- [ ] **D — acceptance decisions LANDED:** B2 FIXED, B3 FIXED, S9 DOCUMENTED, S10 DOCUMENTED.
- [ ] **Release mechanics:** rc.1 cut as GitHub `--latest` (OWNER OVERRIDE 2026-06-18 of `RELEASING.md`'s
      prerelease default); beta.8 demoted from `Latest` (plan-22 sole tag authority).

---

## 10. Plan status table

All 22 rows initialized `pending`. Update `status` as each plan lands (pending → in-progress → delivered/
partial/parked/abandoned, per close-phase).

> **SUBJECT 1 (S1 / plans 1–9) COMPLETE — all delivered to `main` 2026-06-19** (PRs #137–#145). The
> local-mode→browser migration shipped end-to-end: the TS compiler is byte-exact vs the Go oracle
> (conformance harness = required CI gate, both build profiles gated), local mode is browser-resident
> by default, and the controller backend is shrunk to controller-only with the air-gap compute oracle
> retained behind `//go:build airgap`.
>
> **SUBJECT 2 (S2 / plans 10–12) COMPLETE — all delivered to `main` 2026-06-19** (PR #147; one combined
> branch `feat/phone-ux-subject2`, independent 3-lens review GO/0-blockers → non-blocking findings fixed at
> root → CI green → merged). Phone UX shipped: reusable off-canvas `Drawer` primitive + `useMediaQuery`
> (plan-11, Contingency B — owns the primitive AND the sidebar consumer); descriptor-spine responsive
> operator surfaces (plan-10); small-screen read-only design-canvas gate with editing hard-disabled below
> `lg` (plan-12). Frontend-only; no backend/contract change. Owed-by-design: manual phone smokes
> (a11y/boundary/login-gate/sidebar matrix) — validated under Subject 3's device-emulation E2E harness.
> NEXT: Subject 3 (full-stack E2E simulation / pitfall-hunt, plans 13–19; plan-13 harness FIRST,
> real-tunnel plan-18 MANDATORY before rc.1).

| plan | milestone | subject | status | depends-on |
|------|-----------|---------|--------|------------|
| plan-1  | 1.1 | S1 | delivered (PR #137) | — (FIRST mover; before plan-2/8/9 on shared files) |
| plan-2  | 1.2 | S1 | delivered (PR #139) | plan-1 |
| plan-3  | 1.3 | S1 | delivered (PR #141) | plan-1 (clean tree), plan-8 (freeze FIXED C2/C3 behavior) |
| plan-4  | 1.4 | S1 | delivered (PR #143) | plan-5 (green+required), plan-3, plan-9 |
| plan-5  | 1.5 | S1 | delivered (PR #142) | plan-3 |
| plan-6  | 1.6 | S1 | delivered (PR #144) | plan-4 |
| plan-7  | 1.7 | S1 | delivered (PR #145) | plan-6 (tail of S1; before Subject 4 re-audit) |
| plan-8  | 1.8 | S1 | delivered (PR #140) | plan-1 (comments), plan-2 (handler split); lands BEFORE plan-3 |
| plan-9  | 1.9 | S1 | delivered (PR #138) | plan-1 (EARLY off main; supports plan-4) |
| plan-10 | 2.1 | S2 | delivered (PR #147) | plan-11; SUBJECT 1 |
| plan-11 | 2.2 | S2 | delivered (PR #147) | SUBJECT 1 (FIRST in S2) |
| plan-12 | 2.3 | S2 | delivered (PR #147) | plan-11 |
| plan-13 | 3.1 | S3 | pending | SUBJECT 2 (FIRST in S3) |
| plan-14 | 3.2 | S3 | pending | plan-13 |
| plan-15 | 3.3 | S3 | pending | plan-13 (alongside plan-14) |
| plan-16 | 3.4 | S3 | pending | plan-13 |
| plan-17 | 3.5 | S3 | pending | plan-13; SUBJECT 2 (plans 10–12) |
| plan-18 | 3.6 | S3 | pending | plan-13, plan-16 (bringup fixtures); MANDATORY before plan-22 |
| plan-19 | 3.7 | S3 | pending | plan-18 (LAST in S3) |
| plan-20 | 4.1 | S4 | pending | SUBJECT 3 (frozen tree) |
| plan-21 | 4.2 | S4 | pending | plan-20 |
| plan-22 | 4.3 | S4 | pending | plan-21, plan-5, plan-8, plan-18, all S3 (sole tag authority) |
