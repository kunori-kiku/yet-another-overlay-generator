# pre-rc1-hardening — outline

> Durable spine for the `pre-rc1-hardening-2026_07_02` subject. A fresh session picks up the whole
> program from this file. Drafted 2026-07-02 via `draft-implementation-plan` (write-then-review; the
> owner's documented planning convention for this repo — no formal plan-mode toggle).

## 1. Mission

Close the road from `v2.0.0-beta.16` (current GitHub Latest) to **`v2.0.0-rc.1`** by fixing two
confirmed criticals and landing the audited security + functionality hardening, comprehensively and
per-PR-reviewed, with **no scope compromise**.

The two criticals were surfaced 2026-07-02: (1) the owner hit a **NAT per-edge endpoint override that
"sometimes goes direct to the other IP"** during a live smoke; (2) a 3-agent pre-rc.1 security audit
found the **deferred self-update *retry* path bypasses the off-host keystone** (`VerifyMembership`
skipped) → attacker-servable root RCE — a `v2.0.0-beta.15` regression against a HARD principle.

**Success criteria:**
- [ ] The NAT override is honored deterministically (no silent "direct to the other IP"); root-caused
      from a reproduction, fixed across compiler + store + validation, byte-exact in both compilers.
- [ ] The self-update retry path verifies keystone membership before any binary swap (mirrors the
      apply path); a keystone-ON node refuses a membership-failing retry swap (regression-tested).
- [ ] Both criticals ship as a reviewed interim **`v2.0.0-beta.17`** (owner promotes to Latest).
- [ ] The four audited security scopes land: WG public-key validation, agent-route DoS hardening,
      bootstrap binary pinning, node-ID + trusted-proxy client-IP.
- [ ] The three functionality additions land: `agent kit verify`, reliable failed-update state,
      richer telemetry metrics.
- [ ] The stray committed `agent` binary is removed and `.gitignore`d.
- [ ] `RC1-GATE.md` refreshed; every automatable criterion green; `v2.0.0-rc.1` cut (owner-gated
      hardware smokes + branch protection remain the owner's terminal actions).
- [ ] Every PR independently workflow-reviewed (4 lenses) → fixed → re-reviewed clean → CI green.

## 2. Principles (invariants the executor MUST respect)

Inherits `PRINCIPLES.md` (repo root) in full. Load-bearing for THIS subject, with the negative space
that lets a session recognize a violation even when a plan is silent:

- **Signed-artifact self-update custody** `[STATED — PRINCIPLES.md]` **(HIGH).** An agent NEVER
  executes a self-fetched binary not verified against the SHA-256 pin in the **controller-signed,
  keystone-bound** `artifacts.json`. *Violation:* verifying only `VerifyBundle` (tier-1 integrity)
  without `VerifyMembership` (keystone binding) on any swap-deciding path — which is exactly the
  plan-2 bug. The retry path MUST bind the pin to the off-host key when keystone is ON.
- **Backend is the sole port authority** `[STATED — PRINCIPLES.md]` **(HIGH).** The frontend never
  allocates ports and never auto-stamps `endpoint_port`; a nonzero `endpoint_port` is an explicit
  operator NAT override only. *Violation for plan-1:* "fixing" the NAT bug by having the frontend
  stamp a port, or copying `public_endpoints[0].port` onto an edge. Resolving the dial *host* from a
  node's `public_endpoints` is NOT a port action and is permitted.
- **Generated configs must be deployable** `[STATED — PRINCIPLES.md]` **(HIGH).** A rendered artifact
  a consumer rejects (or a peer that can't be dialed) is the worst failure class. *Violation:* the
  NAT override producing an empty/unreachable `Endpoint` (the current bug); a WG key that breaks
  `wg-quick` parsing.
- **Generated scripts / configs run as root; no unescaped user-controlled text** `[STATED]`
  **(HIGH).** *Violation for plan-4:* the WG public key (rendered via non-escaping `text/template`)
  carrying a newline that injects `[Peer]`/directive lines into *other* nodes' root-parsed `wg`
  confs. plan-7: a node ID with shell/path metacharacters.
- **Zero-knowledge key custody** `[STATED — PRINCIPLES.md]` **(HIGH).** WireGuard PRIVATE keys are
  agent-held and NEVER reach the controller/DB/bundles. *Violation:* any plan touching enroll/rekey/
  manual-node/kit paths that lets a private key surface. `agent kit verify` (plan-8) must stay
  read-only over public material.
- **Allocation stability (superset rule)** `[STATED — PRINCIPLES.md]` **(HIGH).** Recompiling a
  superset reproduces identical allocated values. *Violation for plan-1:* the endpoint-resolution fix
  perturbing transit IPs / ports / link-locals. The fix changes only the rendered `Endpoint` line;
  pins must be byte-identical.
- **TS↔Go byte-exact conformance** `[INFERRED FROM DOMAIN — the plan-5 conformance harness]`
  **(HIGH).** The in-browser TS compiler/validator and the Go compiler/validator must produce
  canonical-equal output. *Violation:* fixing `peers.go` (plan-1) or adding a validator code (plan-4)
  without mirroring `peers.ts` / `validator.ts` and regenerating BOTH golden corpora + the drift
  manifest. A new `validator.Code`/`apierr.Code` also requires an `error.<code>` i18n key in
  `en.ts` + `zh.ts` (both CI gates).
- **Observability is neither a DoS vector nor a custody path** `[INFERRED FROM DOMAIN]` **(MEDIUM).**
  *Violation for plan-5/9/10:* letting `/telemetry` force a per-beat fsync'd whole-record rewrite under
  the global lock (DoS), or carrying `applied_generation`/checksum on `/telemetry` (custody leak into
  the observability path).
- **Backward-compat of persisted topologies** `[STATED — PRINCIPLES.md]` **(MEDIUM).** New model
  fields are `omitempty`/optional; prior localStorage/JSON must still load + compile.

**Process invariants (HIGH — from PRINCIPLES.md §Execution discipline, re-affirmed by the owner
2026-07-02):** no shims/monkey-patches (root fixes only); structure-aware hygienic code in every block;
no scope compromise to close work; every PR independently reviewed (4 lenses: correctness /
completeness / hygiene / structure) then re-reviewed after fixes. See [[review-each-pr-before-merge]],
[[worktrees-when-review-workflows-run]].

## 3. Current state of the world (2026-07-02)

- **Latest shipped:** `v2.0.0-beta.16` (GitHub Latest; tag `4a50ec7`) — status-path smoke-hardening.
- **Branch:** plans drafted on `docs/pre-rc1-hardening-plans` (off `main`, tag `4a50ec7`). Each plan
  executes on its own feature branch off `main`.
- **rc.1 gate:** `docs/spec/rc1/RC1-GATE.md` — every automatable criterion already green; the terminal
  rc.1 cut is owner-gated (hardware smokes + branch protection + signature). This subject adds the
  fixes that must precede that cut.
- **Local toolchain:** Go `$HOME/.local/go/bin` (go1.26.x); npm/node v20. Verify with
  `go build ./... && go vet ./... && go vet -tags airgap ./... && go test -race ./... && go test -tags airgap ./...`
  and `cd frontend && npm run lint && npm run build && npx vitest run`. CI is authoritative.

## 4. Must-read references

**Memory:** [[three-item-program-2026_06_25]] (beta.14–16 history; self-update plan-8 context),
[[pre-rc1-program-sequence]] (the road to rc.1), [[review-each-pr-before-merge]],
[[worktrees-when-review-workflows-run]], [[frontend-ci-uses-tsc-b]],
[[bootstrap-repins-operator-cred-by-default]].

**Project docs:** `PRINCIPLES.md`, `docs/spec/rc1/RC1-GATE.md`, `docs/spec/rc1/RUNBOOK.md`,
`CLAUDE.md`.

**Production code (per-plan `Read first` lists carry exact line numbers):**
- NAT: `internal/compiler/peers.go:749-940` (fwd/rev endpoint), `internal/compiler/compiler.go:200-247`
  (CompiledPort write-back), `frontend/src/compiler/peers.ts:621-760`,
  `frontend/src/compiler/index.ts:197-236`, `frontend/src/stores/topologyStore.ts:41-49,477-551`
  (ALLOCATION_PIN_FIELDS, updateEdge, mergeServerAllocations, reconcileEdgeEndpoints),
  `frontend/src/components/design/aside/EdgeEditor.tsx:60-275`,
  `frontend/src/components/canvas/TopologyCanvas.tsx:520-548`.
- Self-update: `internal/agent/selfupdate_retry.go`, `cmd/agent/main.go:498-536,554-561,648-660`,
  `internal/agent/agent.go:118-190`, `internal/agent/verify.go:146,240-320`.
- Security scopes: `internal/renderer/wireguard.go:7,78`, `internal/controller/compile.go:490-557`,
  `internal/controller/enrollment.go:110,196,212-301`, `internal/validator/schema.go`,
  `frontend/src/compiler/validator.ts`, `internal/api/loginratelimit.go:14-17,149-157`,
  `internal/controller/filestore.go:616-645`, `internal/controller/store.go:139-148`,
  `internal/api/handler_bootstrap.go:54,379-389,490-499`, `internal/renderer/script.go:505-563`,
  `internal/api/handler_deploy.go:23-93`, `internal/agent/telemetry.go`,
  `internal/api/wire_controller.go`.

**Spec cache (`specs/<component>`):** `compiler-allocation`, `model-validation`, `panel-design`,
`agent`, `artifacts-signing`, `keystone-trustlist`, `controller-agent-api`,
`controller-operator-api`, `controller-store`, `controller-stage-promote`, `render-keys`,
`panel-auth`, `panel-deploy-fleet`.

**Test gates:** CI jobs `go` (incl. `-race` + airgap), `frontend`, `conformance` (drift manifest +
goldens + codes-catalog SSOT + i18n sync), `frontend-e2e` (incl. `@security`), `realtunnel`,
`security-scan` (govulncheck). Golden corpora: `internal/localcompile/testdata/contract/golden` +
`internal/conformance/testdata/golden`. Drift: `internal/conformance/drift_manifest.json`.

## 5. Standing rules

Per `PRINCIPLES.md` §Project-wide standards + §Execution discipline (git author `kunori-kiku
<rokuyanlin@gmail.com>`; no `--no-verify`/amend/force-push; `Claude-Session` trailer; verify locally
before push; CI authoritative). Reviews are checkout-free in isolated worktrees
([[worktrees-when-review-workflows-run]]). A new validator/apierr code = regen drift manifest + add
`error.<code>` i18n (en+zh). An install/bootstrap/mimic-script change = regen BOTH golden corpora +
drift + the TS mirror.

## 6. Decisions log

- **2026-07-02 preflight — sequencing:** *Fast-track criticals first* — ship the 2 criticals as an
  interim reviewed **beta.17**, then continue the hardening program to rc.1. (Rationale: a confirmed
  root-RCE keystone bypass is live on beta.16; mirrors the beta.8 blocker-hotfix precedent.)
- **2026-07-02 preflight — security rc.1 scope:** include ALL FOUR — WG public-key validation,
  agent-route DoS hardening, bootstrap binary pinning, node-ID + proxy client-IP. (The critical
  self-update keystone fix is in regardless.)
- **2026-07-02 preflight — functionality rc.1 scope:** include `agent kit verify`, reliable
  failed-update state, richer telemetry metrics. **Excluded → rc.2:** FileStore encrypted
  backup/restore + HA.
- **2026-07-02 subject name:** `pre-rc1-hardening`.
- **2026-07-02 post-flight — shape:** accept the 11-plan structure as-is.
- **2026-07-02 post-flight — NAT semantics:** **reproduce-first** — plan-1 builds the repro (from the
  owner's affected topology if supplied, else constructed candidates) BEFORE committing to
  resolve-from-public-endpoint (recommended default) vs require-explicit-host. Recorded as a midflight
  decision plan-1 must resolve + record here before writing the compiler fix.
- **2026-07-02 post-flight — rc.2 deferrals confirmed:** FileStore backup/restore + HA; blind
  digest-signing ceremony hardening; attacker-resistant anti-rollback; bootstrap operator-cred
  out-of-band delivery (keep the runbook fingerprint-compare step; plan-6 closes the adjacent
  *binary*-TOFU now).
- **2026-07-02 midflight (plan-1) — NAT semantics = REQUIRE EXPLICIT HOST** (owner): an
  explicitly-set endpoint host must be authoritative; a port override with no resolvable host is a
  validation error, NOT a silent drop nor a fallback to the node's original public IP.
- **2026-07-02 midflight (plan-1) — refined symptom + repro (redirects the hunt):** owner set a
  *totally different* endpoint IP + port on a link and the deployed config used the node's ORIGINAL
  public IP ("did not notice the port though"). In-package `DerivePeers` repro established: the compiler
  HONORS a custom host on the direction it is set (`A→B` custom → A's peer-for-B = `custom-nat:51900`);
  the REVERSE peer uses `PublicEndpoints[0]` = the original IP (the intended, tested fallback,
  `peers.go:897`); a PORT-ONLY override (host empty) is dropped forward + reverse→original. So "fell back
  to the original" is EITHER **(a)** the reverse direction was observed (B's config for A — a by-design
  fallback, not a compiler bug), OR **(b)** the custom host was lost before compile (a frontend store /
  controller-mode save round-trip dropping the edit). **DISAMBIGUATION PENDING (owner):** which node's
  deployed config showed the original IP (the NEAR end you edited vs the FAR end of the link), whether it
  was after a Deploy or just a preview, or the link's JSON. plan-1's investigation is redirected from
  "compiler port-only" to "explicit host not honored: reverse-fallback (a) vs store round-trip (b)"; the
  require-explicit-host semantics above governs the compiler side either way.

## 7. Milestones

Ordered: criticals (1–2) → interim beta.17 (3) → security hardening (4–7) → functionality (8–10) →
rc.1 gate (11). Plans 4–10 are largely file-disjoint and may be built in any order after beta.17;
plan-9/10 both touch `/telemetry` + `RecordTelemetry` + the panel, so build plan-5 (telemetry bounds/
coalescing) BEFORE 9/10 to freeze the write path.

### plan-1 — NAT per-edge endpoint override "goes direct to the other IP" (CRITICAL)
- **Goal.** Make a NAT override deterministically honored; kill the silent fallback to the peer's
  plain public IP.
- **Proposed solution.** Reproduce first (record the confirmed trigger in the Decisions log), then fix
  at root: (a) compiler endpoint resolution in `peers.go` + `peers.ts` — a port-only override resolves
  the dial host from the target node's `public_endpoints[0]`, and the forward path gains a
  public-endpoint fallback symmetric with the reverse (no `endpoint_port` auto-stamp — port authority
  stays backend-only); (b) the frontend host/port coupling — `reconcileEdgeEndpoints` and the
  "unset host" dropdown must not leave a port set with the host gone; (c) validation — an override
  port with no resolvable host is a clean error (deployable-config). Regen BOTH goldens + drift;
  mirror byte-for-byte.
- **Hazards.** Shared TS+Go compiler → every topology's rendered `Endpoint` is in blast radius;
  allocation pins must stay byte-identical; the "NAT override active" badge must agree with the
  compiler after the fix.
- **Verification.** New regression fixtures (reverse fallback; port-only override; reconcile) through
  both compilers, byte-equal; `go test -race`, conformance green, `npm run build` + vitest.
- **Stop-loss.** If the repro reveals a mechanism outside this cluster, STOP → outline update →
  plan-1.5.

### plan-2 — self-update retry keystone bypass (CRITICAL)
- **Goal.** The deferred self-update retry must not swap a binary without keystone membership
  verification when keystone is ON.
- **Proposed solution.** Give `RetryDeferredSelfUpdate` a membership-verifying fetch that mirrors the
  apply path (`agent.go:131`: `VerifyBundle` + `VerifyMembership(MembershipConfig{NodeID,
  OperatorCredPEM, OperatorCredAlg, OperatorRPID, OperatorOrigin}, prevEpoch)`), built from the
  `operatorCred` already read at `main.go:502`. Keep the bundle-only `verifiedFetch` for the
  post-swap health-gate (`ReconcileSelfUpdatePromote`). No-op when keystone OFF (dev unaffected). Fix
  the false "custody model is unchanged" comment.
- **Hazards.** Don't double-verify or break the health-gate; the epoch anti-rollback must carry
  through; keystone-OFF must stay a clean no-op.
- **Verification.** Unit test: keystone-ON node with an armed `SelfUpdateBlocked` REFUSES a retry
  swap whose bundle fails membership; keystone-OFF still retries; the apply path unchanged.
- **Stop-loss.** None expected; contained single-path fix.

### plan-3 — interim release `v2.0.0-beta.17` (+ hygiene)
- **Goal.** Ship the two criticals fast; remove the stray committed binary.
- **Proposed solution.** `git rm --cached agent` + add build-artifact names to `.gitignore`; roll
  CHANGELOG (`beta.16..HEAD`); local verify suite; tag `v2.0.0-beta.17`; release.yml + docker.yml
  green; owner promotes to Latest.
- **Verification.** Published `version` prints `v2.0.0-beta.17`; assets + sidecars present.
- **Stop-loss.** Red release gate → fix → re-cut from green tip (never re-point a tag).

### plan-4 — WG public-key format validation
- **Goal.** Reject a malformed WG public key at every entry point; kill config-injection into peers'
  root-parsed `wg` confs.
- **Proposed solution.** base64/32-byte Curve25519 validation at `validateManualNodes`, `Enroll`,
  `Rekey`, and schema (Go `schema.go` + TS `validator.ts`). New `validator.Code` → drift regen +
  `error.<code>` i18n (en+zh). Reuse the existing key-parse idiom (`wgtypes.ParseKey` / `crypto/ecdh`).
- **Hazards.** Conformance drift + i18n sync gates; must accept every currently-valid key (no
  false-positives on live topologies).
- **Verification.** Go + TS validator tests (valid/invalid/newline-injection); drift + i18n green.
- **Stop-loss.** If a live topology carries a technically-invalid-but-working key, STOP → decide
  grandfather vs reject → plan-4.5.

### plan-5 — agent-route DoS hardening
- **Goal.** An authenticated node cannot DoS the controller via `/telemetry` (or other agent routes).
- **Proposed solution.** Per-node (bearer-identity) rate limiter on the agent mux (mirror the
  `enrollLimiter`/`loginLimiter` idiom, defaults that never throttle a healthy 30s heartbeat + poll);
  bound telemetry conditions count + metrics size; decouple telemetry durability from the per-beat
  critical path (keep LastSeen/conditions fresh in memory; do not fsync-rewrite the whole node record
  every beat) WITHOUT weakening custody-field durability.
- **Hazards.** Must not throttle legitimate traffic; the MemStore vs FileStore split; custody fields
  (AppliedGeneration/keys/audit) keep their durability.
- **Verification.** Load/limit unit tests; a telemetry storm no longer forces per-beat fsync;
  custody-field writes still durable.
- **Stop-loss.** If write-coalescing risks losing a custody write, STOP → narrow the decoupling to
  the telemetry-only fields → plan-5.5.

### plan-6 — bootstrap agent-binary SHA-256 pinning
- **Goal.** Close the unpinned first-binary TOFU: the bootstrap script verifies the downloaded agent
  binary against the controller's `AgentBins` SHA-256 pin before running it.
- **Proposed solution.** Bake a fail-closed `sha256sum -c -` step into the generated bootstrap script
  (mirror the mimic `.deb` pattern in `script.go:505-563`) using the pins the controller already
  holds (`handler_bootstrap.go:54,379-389`). Regen any bootstrap golden corpus.
- **Hazards.** Golden-corpus regen; must not break the `--proto` fallback semantics the operator
  relies on; keep the OOB operator-cred delivery as the documented rc.2 item.
- **Verification.** Bootstrap-script golden regen; a tampered binary is rejected (script-level test).
- **Stop-loss.** If the pin isn't available at bootstrap time for some flow, STOP → plan-6.5.

### plan-7 — node-ID validation + trusted-proxy client-IP
- **Goal.** Charset-validate node IDs at ingest; honor a trusted `X-Forwarded-For` so per-IP limits
  don't collapse behind a reverse proxy.
- **Proposed solution.** An allow-list node-ID validator (safe charset derived from its consumers:
  interface names, filenames, Content-Disposition); a configurable trusted-proxy setting gating XFF/
  X-Real-IP parsing in `clientIP` (never trust XFF blindly).
- **Hazards.** Node-ID validation must accept existing IDs; a new validator code → drift + i18n.
- **Verification.** Validator tests; `clientIP` tests with/without the trusted-proxy setting.
- **Stop-loss.** If existing node IDs violate the chosen charset, STOP → grandfather vs migrate →
  plan-7.5.

### plan-8 — `agent kit verify`
- **Goal.** Give a manual (hand-installed) node operator an agent-side verify gate before running
  `install.sh`.
- **Proposed solution.** `agent kit verify --bundle <dir|zip> [--operator-cred …]` runs the agent's
  `VerifyBundle` + `VerifyMembership` over the downloaded bundle; clear pass/fail exit codes; never
  touches private keys.
- **Verification.** `kit_test.go` cases: valid bundle passes; tampered `install.sh`/checksums fails;
  membership-mismatch fails; zero-knowledge preserved.
- **Stop-loss.** None expected.

### plan-9 — reliable persistent failed-update state
- **Goal.** The panel's "update failed" signal is authoritative, not best-effort/transient.
- **Proposed solution.** Investigation showed a DURABLE `selfupdate:Abandoned` condition already flows
  end-to-end (`AbandonedAgentVersion` is preserved across applies + persisted + mapped `→failed`). The
  real gaps: `Abandoned` and `Blocked` are both `Warn` (indistinguishable), the failure reason is
  dropped, and the panel's best-effort caveat is dishonest for structured conditions. Fix (Option A):
  elevate `Abandoned` to `ConditionStatusError`, add a durable curated `AbandonedReason`, gate the panel
  caveat on the absence of a structured condition. Zero new codes (conditions are open strings).
- **Hazards.** Shares `/telemetry` + `RecordTelemetry` + the panel with plan-5/10 — build after
  plan-5.
- **Verification.** Agent state test + wire test + panel chip test.
- **Stop-loss.** If a dedicated field is needed, additive `omitempty` only.

### plan-10 — richer telemetry metrics
- **Goal.** Emit latency/resource metrics via the existing Sampler framework (currently
  conditions-only).
- **Proposed solution.** `controller.Node.Telemetry` already exists + persists (both stores) + serves
  (beta.12), so this is ONLY a new agent `Sampler` (host load/mem from `/proc`, no shell) + panel
  readout — no persist/wire work. WireGuard exposes no RTT (handshake-age lives in `wireguard_peers`);
  scope to host resource. `resource` stripped live-only (custody honesty). Metrics stay OFF the
  deploy-custody path (never `applied_generation`/checksum on `/telemetry`).
- **Hazards.** `/telemetry` shared with plan-5/9; keep bounded (plan-5's caps apply).
- **Verification.** Sampler test; persist/round-trip test; panel render test.
- **Stop-loss.** None expected; additive.

### plan-11 — rc.1 gate refresh + release
- **Goal.** Refresh `RC1-GATE.md`, roll CHANGELOG, cut `v2.0.0-rc.1` (owner-gated terminal actions).
- **Proposed solution.** Update the gate's criteria/evidence to include this subject's fixes; confirm
  A–E green; execute the release runbook (annotated tag → `--latest` → verify). Owner retains the
  hardware smokes + branch protection + signature.
- **Verification.** Per `RC1-GATE.md` §E; `isLatest: true`; `version` prints `v2.0.0-rc.1`.
- **Stop-loss.** A red required gate or a new confirmed blocker → fix → re-run (never tag over red).

## 8. Insertion-point markers

- **plan-1.5** — the NAT repro reveals a mechanism outside the identified cluster (e.g. a third store
  path, or a controller-subgraph edge-copy issue), needing its own fix before the compiler change.
- **plan-4.5 / plan-7.5** — a live topology carries a WG key or node ID the new validator would
  reject; decide grandfather-vs-reject before the gate lands.
- **plan-5.5** — telemetry write-coalescing cannot be done without risking a custody-field write;
  narrow the decoupling to telemetry-only fields.

## 9. Closure criteria

- [ ] All 11 plans delivered + merged; each independently workflow-reviewed → fixed → re-reviewed → CI
      green.
- [ ] beta.17 shipped + promoted (owner); rc.1 gate refreshed; rc.1 cut (owner terminal actions).
- [ ] Both criticals regression-tested; the NAT repro + chosen semantics recorded in the Decisions log.
- [ ] No new deprecated/stale entries; no shims; both compilers byte-exact; drift + i18n + goldens green.
- [ ] STATUS.md refreshed; subject `git mv`'d to `_completed/`; memory updated.

## 10. Plan status table

| Plan | Title | Status |
|------|-------|--------|
| plan-1 | NAT endpoint-override fix (critical) | pending |
| plan-2 | self-update retry keystone bypass (critical) | pending |
| plan-3 | interim release beta.17 + hygiene | pending |
| plan-4 | WG public-key format validation | pending |
| plan-5 | agent-route DoS hardening | pending |
| plan-6 | bootstrap agent-binary SHA-256 pinning | pending |
| plan-7 | node-ID validation + trusted-proxy client-IP | pending |
| plan-8 | agent kit verify | pending |
| plan-9 | reliable failed-update state | pending |
| plan-10 | richer telemetry metrics | pending |
| plan-11 | rc.1 gate refresh + release | pending |
