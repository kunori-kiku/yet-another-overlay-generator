# Subject: signed-self-update-and-rc-hardening-2026_06_15

## Mission

Take YAOG from `v2.0.0-preview.10` to a tagged **beta.1** (set as the latest release), then a **beta.2**,
by delivering — in one planned subject — three intertwined streams:

1. **mimic from GitHub** — replace the apt-only mimic install (which fails on Debian images lacking the
   package) with a distro-first → pinned-`.deb`-from-GitHub → SHA-256-verify ladder.
2. **signed agent self-update** ("both now": the agent binary AND its dependencies like mimic), staged so
   the *observable* half (version reporting) ships in beta.1 and the *mutable* half (the binary swap +
   canary rollout) ships in beta.2.
3. **RC hardening** — the full RC-readiness survey (Units A+B+C): legal/docs/CI, controller-mode UX &
   resilience, backend robustness & input-safety (full validator coverage).

**Success criteria:**
- beta.1 tagged (latest release): all RC blockers closed (LICENSE/NOTICE [done], CHANGELOG, wiki redirect,
  localized controller errors, honest docs, knowable version), mimic installs from GitHub with verified
  pins, agents *report* their version, full validator table, gofmt+CI clean.
- beta.2 tagged: agents self-update their own binary via canary-then-fleet rollout, every fetched artifact
  verified against a signed in-bundle pin; a bad swap cannot brick a fleet.
- rc.1 is a later owner call once the two hardware smokes pass and beta soak is clean.

## Principles (inherits `PRINCIPLES.md`; subject-specific additions)

Inherit all of `PRINCIPLES.md` (esp. the HIGH "scripts run as root" custody tier and the air-gap
byte-identity invariant). This subject ADDS one HIGH principle (plan-9 amends `PRINCIPLES.md` with it):

- **Signed-artifact self-update custody (HIGH) [STATED].** An agent NEVER executes a self-fetched binary
  (its own replacement, or a mimic `.deb`) that it has not verified against a SHA-256 pin carried in the
  controller-signed, keystone-bound `artifacts.json` (a `bundleFiles` member); and NEVER downgrades below
  `AgentVersionFloor`. The gh-proxy and github.com are untrusted transport. *Violation examples:* trusting
  the upstream `.sha256` sidecar (same untrusted transport); putting a pin in the unsigned `manifest.json`;
  changing `verify.go`'s signature path; advancing the floor before a health-confirmed update.
- **Air-gap byte-identity (HIGH) [INFERRED+STATED].** Threading `FetchSettings` with a zero value, and an
  empty mimic/agent catalog, MUST leave `install.sh` and the signed bundle byte-identical to today.
  *Violation example:* a non-zero default field, or emitting `artifacts.json` when the catalog is empty.
- **Zero-knowledge custody unchanged (HIGH) [STATED].** Self-update touches binaries, never WireGuard
  private keys; the perpetual no-private-key custody guard + AgentHeld↔AirGap diff stay green.

## Current state of the world (2026-06-15)

- `main` @ post-`v2.0.0-preview.10`; controller-nat subject shipped + closed; `b32a10e`+ closure.
- Branch `rc-hardening` (off main, pushed): Apache-2.0 `LICENSE` + `NOTICE` (© 2026 kunori-kiku) committed —
  this is plan-1's first deliverable, already done.
- Trust chain verified (design + refine workflows, against `rc-hardening`@`2c52f26`): `bundleFiles` =
  WG confs + babeld.conf + 99-overlay.conf + `install.sh` (`internal/artifacts/export.go:141-159`);
  `manifest.json` is deliberately EXCLUDED from the checksum set (export.go:135); keystone binding
  `Member.BundleSHA256 = hex(sha256(checksums.sha256))` (`internal/controller/compile.go:288`); agent
  enforces both `VerifyBundle` + `VerifyMembership` (`internal/agent/verify.go:112-339`). Adding
  `artifacts.json` to `bundleFiles` inherits the entire chain with NO change to `bundlesig`/`trustlist`/
  `verify.go`.
- The mimic bug is live: `internal/renderer/script.go:432` `ensure_cmd mimic mimic`.
- `render.All(result, keys)` (`render.go:149`) has **4** callers: `handler.go:159`, `handler.go:203`,
  `compile.go:147` (the controller stage-promote path — the ONLY caller that gets a non-zero `fs`),
  `cmd/compiler/main.go:78` (air-gap CLI — stays flag/catalog-driven, plan-7).

## Must-read references

**Memory:** [[agent-self-update-signed-verification]] (the directive + mimic facts), [[controller-nat-port-ip-customization-plan]],
[[controller-mode-redesign-decisions]], [[i18n-error-envelope-shipped]], [[workflow-agents-share-working-tree]].

**Upstream syntheses (this session's workflows, full text in the task outputs):** RC-readiness survey
`wiakgi4v5`; self-update/mimic design `wfhuw2hd8`; comprehensiveness refine `wxajvgzp5`.

**Specs (two trees — `Reads from specs:` resolves against the FLAT root `specs/` cache, per
`specs/README.md` and the execute skill's loader `<repo-root>/specs/<component>.md`, which STOPs on a
missing file):** root `specs/` — `controller-stage-promote`, `controller-agent-api`, `controller-store`,
`keystone-trustlist`, `model-validation`, `artifacts-signing`, `render-keys`,
`panel-{auth,shell,deploy-fleet,design}`, `airgap-api`, `compiler-allocation`. Deep prose lives in
`docs/spec/`: `PRINCIPLES.md`; `docs/spec/artifacts/mimic.md`;
`docs/spec/controller/{agent,deploy,signing,persistence,bootstrap,controller-api}.md`;
`docs/spec/compiler/validation.md` (the coverage contract); `docs/spec/api/wire-contract.md` + `http-api.md`.

**Production code (file:line):** `internal/render/render.go:149`; `internal/artifacts/export.go:135,141,159,166`;
`internal/renderer/script.go:11,87,432,442,641-669,792,1353`; `internal/agent/verify.go:112-339`,
`state.go:34-58`, `cycle.go`, `agent.go`, `controller_client.go:91-94`; `cmd/agent/main.go:46,289`;
`internal/controller/store.go:74,281-302`, `settings.go:20-34`, `compile.go:147,288`;
`internal/api/handler_bootstrap.go:104-118,183,295,324`, `handler_controller.go:390-405`;
`internal/controller/filestore.go:48,903,1561,1574`; `internal/api/server.go:179-224`
(listeners/timeouts + `/enroll` no-auth) + `cmd/server/main.go:178-179` (serve goroutines — SIGTERM→Shutdown wires here);
`frontend/src/api/controllerClient.ts` (raw-error throws), `frontend/src/components/deploy/NodeRegistry.tsx`,
`frontend/src/i18n/messages/{en,zh}.ts`; `.github/workflows/release.yml`, `Dockerfile`, `.github/workflows/ci.yml`.

**Test gates:** `internal/render/equivalence_test.go`, `signing_test.go` (byte-identity); the four keystone
digest tests (`internal/agent/verify_test.go`, `keystone_test.go`, `internal/controller/compile_keystone_test.go`,
`internal/api/controller_keystone_test.go`); `internal/renderer/script_mimic_test.go`; the perpetual
custody guard + AgentHeld↔AirGap diff.

## Standing rules

Per `PRINCIPLES.md` + memory: commit env `GIT_AUTHOR_NAME=kunori-kiku GIT_AUTHOR_EMAIL=rokuyanlin@gmail.com`
(+ committer); `git commit -F` (never `-m`); no `--no-verify`/`--amend`/force-push; `npm install
--legacy-peer-deps`; **Go is NOT installed locally (PRINCIPLES.md:48)** — `gofmt -w`/`go test` correctness
is asserted in CI, not on the dev box (go at `/snap/bin/go` works for local sanity where available);
stacked branches, per-plan PR + green gate; serialize branch work vs. read-only workflows.

## Decisions log

- 2026-06-15 — **Preflight:** subject = `signed-self-update-and-rc-hardening`; RC scope = ALL of Units
  A+B+C; wiki = redirect for rc.1 (banner-scope both `wiki.md` + `wiki-zh.md`); release = beta.1 first
  (set latest), rc.1 a later owner call; license = Apache-2.0 (done).
- 2026-06-15 — **Post-flight:** (a) beta.1 EXCLUDES the self-update swap — beta.1 = hardening + mimic +
  version *reporting*; the binary swap + canary rollout = beta.2. (b) D2 self-update default =
  **canary-then-fleet** (staged subset, operator-promoted). (c) D5 validator = **full none-yet table**.
  (d) D9 = `release.yml` publishes per-arch agent SHA now; bootstrap-TOFU hole documented-deferred to rc.2.
- 2026-06-15 — **Adopted recommendations (design/refine):** D1 mimic catalog = manual for beta (no
  controller→GitHub automation); D3 `min_version` bumped only on bundle/wire-format breaks; D4 air-gap =
  OMIT `artifacts.json` when catalog/version inputs are zero; D6 `artifacts.json` = `bundleFiles` member
  (signed), never a `/config`-only append; D7 `BuildVersion` via `-X main.BuildVersion=<tag>` ldflags in
  `release.yml`+`Dockerfile`; D8 pin-field validation strict at POST (semver, 64-hex SHA, http(s) base).
- 2026-06-15 — **Plan self-review** (`wwy805xww`, all-Opus, 75 agents): NO-GO→GO after M1–M3 doc fixes —
  `Reads from specs:` slugs rewritten to the FLAT root `specs/` cache (the execute loader's target, which
  STOPs on a missing file; `implementation_plans/README.md` corrected to match); plan-6/plan-1 validator
  scope corrected (mtu/router_id/extra_prefixes/ssh_*/route_policies ALREADY shipped on `main` → plan-1
  flips the stale doc rows, plan-6 closes only the genuinely-missing `endpoint_host` + `public_endpoints[].host`
  charset + `transit_cidr`); outline `render.All` caller corrected (`compile.go:147` carries the non-zero
  `fs`). Should-fix anchor corrections (server.go→internal/api/server.go, LoadSigningFromEnv, schema-stamp,
  release.yml-already-publishes, semver comparator, agent.release_url source) folded into the plans.
- 2026-06-16 — **beta.2 shipped + SUBJECT CLOSED (plan-10):** plans 9–10 merged (PRs #117–#118);
  tagged `v2.0.0-beta.2` (GitHub *latest*). The RISKY CORE (plan-9, signed self-update) went through
  a 4-dimension deep review (12 confirmed defects — genuine fleet-brick + anti-downgrade bugs caught
  before merge), a re-review (caught R1-1 still broken), a round-2 fix, and a final verification
  (all_fixes_correct=true). Self-update field smoke recorded **owed (owner-accepted risk)** — no live
  fleet available; the mechanism is extensively unit-tested + thrice-reviewed. Deferred to rc.2/GA:
  the bootstrap-TOFU hole, the FileStore SPOF mutex/poll fix, the full wiki rewrite. rc.1 is a later
  owner call once the owed smokes pass and beta soak is clean. CLOSURE.md written; subject archived
  to `_completed/`.
- 2026-06-16 — **beta.1 shipped (plan-8 partial close):** plans 1–8 merged (PRs #109–#115); tagged
  `v2.0.0-beta.1` and set as the GitHub *latest* release. Each PR passed an independent multi-agent
  review before merge (plan-6 caught 1 MAJOR audit-log torn-line brick + 6 MINOR/NIT, all fixed +
  re-reviewed; plan-7 reviewed clean). The three beta.1 hardware smokes (two-node controller WebAuthn
  login/hydration, NAT sticky-pin round-trip, mimic GitHub-`.deb` on a real Debian host) are recorded
  **owed (owner-accepted risk)** — no hardware/authenticator/host available in the execution
  environment. Subject stays OPEN for beta.2 (plans 9–10): the self-update *swap* + canary rollout.

## Milestones (PR per plan; gate each; see plan-N file for detail)

### beta.1 milestone (plans 1–8)
- **plan-1** RC paperwork & trust (Unit A). Hazard: don't pre-flip validation.md rows plan-6 closes. Gate:
  CI gofmt-clean, links resolve. Stop-loss: revert a doc commit.
- **plan-2** `render.FetchSettings` plumbing (design PR-1). Hazard: a non-zero default breaks byte-identity.
  Gate: `equivalence_test`+`signing_test` byte-identical with zero `fs`. Stop-loss: → plan-2.5.
- **plan-3** mimic `.deb` install + `artifacts.json` (design PR-2). Hazard: keystone digest churn. Gate: four
  keystone tests absorb the one-time digest change; no-mimic install.sh unchanged. Stop-loss: → plan-3.5.
- **plan-4** agent version reporting + build-version injection + observability (design PR-3, observable half).
  Hazard: empty `BuildVersion` if ldflags missing. Gate: `yaog-agent version` works; badge shows; report
  carries `agent_version`. Stop-loss: revert if any apply-path file touched.
- **plan-5** controller-mode UX & resilience (Unit B). Rebases on plan-4 (en/zh + NodeRegistry). Gate:
  no raw `<status> <JSON>` in UI; en/zh lockstep; ErrorBoundary present.
- **plan-6** backend robustness & FULL input validation (Unit C + D5 full table). Gate: charset validators
  reject injection hosts without moving byte-identity fixtures; graceful drain; `/enroll` throttles;
  HEALTHCHECK passes; count-bound rejects oversized topologies.
- **plan-7** docs + air-gap catalog fallback (design PR-5). Gate: air-gap omits `artifacts.json` when inputs
  unset (byte-identity holds); docs link-check; pin runbook reproduces.
- **plan-8** beta.1 closure & release. Gate: 2 hardware smokes run or recorded owed; beta.1 annotated tag +
  GitHub release set `latest`; STATUS updated (subject partial — beta.2 pending).

### beta.2 milestone (plans 9–10)
- **plan-9** agent self-update mechanism + canary-then-fleet (design PR-4, the RISKY CORE). Hazard: R1 brick-
  a-fleet. Gate: table tests (noop/downgrade-refuse/floor-refuse/hash-mismatch/self-test-fail/happy);
  breadcrumb-reconcile (promote/rollback/attempt-cap); cross-fs fallback. Stop-loss: → plan-9.5.
- **plan-10** beta.2 closure & release. Gate: self-update field smoke (canary self-update + badge flip +
  tampered-hash refuse); beta.2 tag; full close-phase (CLOSURE.md, archive, STATUS regen).

## Insertion-point markers (drafted ONLY if hit)
- **plan-2.5** — if zero-value `FetchSettings` threading is not byte-identical (a caller or
  `buildInstallScriptConfig` perturbs template data). Trigger: `equivalence_test`/`signing_test` red.
- **plan-3.5** — if `artifacts.json` entering `bundleFiles` breaks keystone digest binding in a way the four
  keystone tests can't cleanly absorb (e.g. air-gap omit needs a per-mode branch in export.go's node loop).
- **plan-9.5** — if bounding the `Restart=always` loop needs a systemd unit-file change (`StartLimitBurst`)
  that ripples back into the bootstrap renderer (`handler_bootstrap.go`). The most probable wall.

## Closure criteria

**beta.1 (plan-8):** all 6 RC blockers closed; mimic installs from GitHub verified; agents report version;
full validator table live; gofmt+CI green; air-gap byte-identity green; honest docs; beta.1 tagged latest;
2 hardware smokes run or recorded owed.

**beta.2 / subject close (plan-10):** canary self-update works + verified-before-exec + floor + bounded
restart loop + documented manual recovery; PRINCIPLES.md amended (self-update custody HIGH); CLOSURE.md;
subject archived to `_completed/`; beta.2 tagged. rc.1 remains a later owner call.

## Plan status table

| Plan | Milestone | Status | Commit |
|------|-----------|--------|--------|
| plan-1 | beta.1 | done | `bb6fe75` (PR #109 — RC paperwork & trust-doc honesty) |
| plan-2 | beta.1 | done | `9c7f398` (PR #110 — render.FetchSettings plumbing, byte-identical) |
| plan-3 | beta.1 | done | `2b2eb71` (PR #111 — mimic GitHub-.deb + signed artifacts.json) |
| plan-4 | beta.1 | done | `e3a24f4` (PR #112 — agent version reporting + build-version) |
| plan-5 | beta.1 | done | `93b26bf` (PR #113 — controller-mode UX & resilience) |
| plan-6 | beta.1 | done | `3a41653` (PR #114 — backend robustness & full input validation) |
| plan-7 | beta.1 | done | `f129b1b` (PR #115 — air-gap mimic catalog + honest mimic docs) |
| plan-8 | beta.1 | done | beta.1 tagged `v2.0.0-beta.1` (GitHub latest); CHANGELOG/STATUS rolled (PR #116) |
| plan-9 | beta.2 | done | `d45fc02` (PR #117 — signed agent self-update + canary-then-fleet) |
| plan-10 | beta.2 | done | beta.2 tagged `v2.0.0-beta.2` (GitHub latest); subject closed (PR #118) |
