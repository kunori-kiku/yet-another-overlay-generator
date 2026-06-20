# RC1-GATE — the `v2.0.0-rc.1` go/no-go gate + release runbook

> **The single source of truth** for cutting `v2.0.0-rc.1` (plan-22 / 4.3). rc.1 is **GO** iff every
> criterion A–E below is satisfied with mechanical evidence; otherwise **NO-GO** (cut a beta.9 with the
> fix, or record a written owner-accepted-risk exception). This file owns the required-checks set, the
> owed-smoke ledger, the plan-8 decision references, the release runbook, AND the owner sign-off — one
> diffable document, no sibling files (`STATUS.md` links here; plan-19's `docs/spec/rc1/RUNBOOK.md` is the
> canonical owed-smoke reduction this references; plan-21's `docs/spec/rc1/4.2-verdict.md` is the A2
> evidence).

## Status legend

`✅ GO` (mechanically satisfied) · `⏳ OWNER` (an owner-only action remains — hardware smoke, CI dispatch,
branch-protection toggle, or the tag cut) · `☐` (not yet).

---

## A. Blocker-clean (zero confirmed blockers open)

- **A1 ✅** — the six original rc.1 blockers are fixed **with regression coverage**:
  - **B1** fleet-mux panic recovery → `internal/api/server.go:186` `recovered(mux)` wraps BOTH muxes,
    returns `apierr.CodeInternalPanic` (`:171-172`); regression: `internal/api/beta8_blockers_test.go` +
    the DAST live-wire path. (beta.8)
  - **S4/S5/S6** enrollment lifecycle → `internal/controller/enrollment.go:168-182,238` (`ErrNodeRevoked`/`reenrollApproved`),
    `handler_deploy.go:349` (purge-on-revoke), `:566-567` (TTL clamp `7*24*60*60`); regression:
    `beta8_blockers_test.go` + **`internal/dast` live-wire** `TestDAST_RevokeBlocksTokenResurrection` /
    `TestDAST_EnrollmentTokenTTLCapped`. (beta.8)
  - **F1** keystone-sign-after-refresh 401 → `frontend/src/api/controllerClient.ts:993-995` (`getTrustlist`
    via the shared `request()` helper); regression: `frontend/e2e/deploy-keystone.spec.ts` +
    `frontend/e2e/security/f1-refreshed-cookie-keystone-sign.spec.ts` (plan-21). (beta.8)
  - **C1** babeld.conf byte-stability → `internal/renderer/babel.go:128-132` (sort by `InterfaceName`);
    regression: the plan-5 conformance babeld.conf golden. (beta.8)
- **A2 ✅** — **zero NEW confirmed blockers** from Subjects 1–4. The plan-21 (4.2) re-audit verdict
  ([`docs/spec/rc1/4.2-verdict.md`](4.2-verdict.md)) is **GO**: 4 findings, all LOW, all
  fixed-or-accepted (SSRF SIIT FIXED, TS zip-slip FIXED, S3 compile-DoS accepted-residual, the go1.25.0
  stdlib CVEs FIXED by the go1.26.4 toolchain bump). Subject-1 TS cutover removed the anonymous
  S1/S2/S3/B4 air-gap surface (positive delta — plan-7). No finding meets the blocker bar
  (fleet-availability / fleet-trust / controller-mode security-correctness).

## B. Gates green AND required in CI

| # | check | CI job | green? | required-on-`main`? |
|---|-------|--------|--------|---------------------|
| **B1** | gofmt-drift + `go vet` + **`go test -race ./...`** (+ airgap profile) | `go` | ✅ | ⏳ OWNER (branch protection) |
| **B2** | `npm ci` + `npm run lint` + `npm run build` (tsc -b) | `frontend` | ✅ | ⏳ OWNER |
| **B3** | Go↔TS conformance + goldens + heal pin + codes-catalog SSOT + coverage floor | `conformance` (plan-5) | ✅ | ⏳ OWNER |
| **B4** | full-stack Playwright (operator/fleet/adversarial/responsive/**@security**) | `frontend-e2e` (plan-13) | ✅ | ⏳ OWNER |
| **B5** | `release.yml` gate **mirrors `ci.yml` `go`/`frontend` gate behavior** step-for-step (gofmt + `-race` + airgap + build) | `gate-go`/`gate-frontend` | ✅ (plan-22 Task 3) | n/a (tag-time) |
| **B6** | **`realtunnel` netns canary** — per-iface WG handshake + babel route convergence + 0%-loss overlay ping + SNAT rewrite | `realtunnel` (plan-18) | ✅ | ⏳ OWNER (distinct check, NOT `frontend-e2e`) |
| **+** | `govulncheck` REQUIRED + gosec/npm-audit advisory + DAST replay | `security-scan` (plan-21) | ✅ | recommended (4.3 ratifies) |

**`-race` ownership:** plan-22 / 4.3 OWNS the `-race` add (`ci.yml` `go` + `release.yml gate-go`); plans
5/13/18/21 explicitly defer. The suite is **green under `-race`** (verified locally; CI authoritative).

**B6 is a DISTINCT required check from B4** — `realtunnel` is a netns/kernel job, not a browser spec; the
gate is NOT satisfiable with `realtunnel` skipped. plan-18 was authored required-from-the-start (no
advisory→required flip needed; the R8 cross-plan flag is already discharged — the job has no
`continue-on-error` on its canary step).

## C. Owed manual smokes triaged (plan-19) + pitfall findings triaged (plan-16/21)

- **C1** — the **owed-smoke ledger** (canonical 8→3+1 reduction in
  [`docs/spec/rc1/RUNBOOK.md`](RUNBOOK.md)); live STATE below. Nine `STATUS.md` smokes
  (eight beta.1–7 + the Subject-2 phone-UX smoke):

  | # | owed smoke | state | covered by |
  |---|---|---|---|
  | 1 | WebAuthn login + refresh + no-localStorage-token | ✅ automated (refresh/token) + ⏳ OWNER (login ceremony, §C1 RUNBOOK) | `session.spec.ts` + `leakOracle.ts` + `login-webauthn.spec.ts` |
  | 2 | NAT sticky-pin compile→deploy→no-drift | ✅ automated (data plane: A + `realtunnel`) + ⏳ OWNER (NAT-box rewrite, §C2 RUNBOOK) | conformance + `test/realtunnel/` |
  | 3 | mimic `.deb` install on kernel ≥6.1 | ⏳ OWNER (eBPF/DKMS/XDP, §C3 RUNBOOK) | `script_mimic_test.go` (bash) |
  | 4 | self-update field smoke | ✅ automated | self-update unit/E2E |
  | 5 | panel rollout-UI smoke | ⚠️ OPEN DEP / ⏳ OWNER | no rollout E2E/vitest yet (Risk R1) — owner-run or land a spec before tag |
  | 6 | keystone rotation + reprovision + passkey rotation | ✅ automated (reconverge) + ⏳ OWNER (passkey + systemd lifecycle, §C1) | `test/realtunnel/` |
  | 7 | fleet-operability panel smoke | ✅ automated | unit + `fleet-rekey.spec.ts` |
  | 8 | pin-collision + Export/Import smoke | ✅ automated | unit + `export-import.spec.ts` |
  | 9 | phone-UX smoke (Subject 2) | ✅ automated | `frontend/e2e/responsive/` (plan-17) |

  **Irreducible owner-run residue (the rc.1 manual gate):** §C1 real WebAuthn authenticator, §C2-shrunk
  real-NAT-box endpoint-rewrite, §C3 mimic eBPF ≥6.1 — each run-and-green OR a written owner-accepted-risk
  exception. **Smoke #5** (rollout UI) is an OPEN DEPENDENCY (no automated coverage) — owner runs it OR a
  rollout spec lands before the tag.
- **C2 ✅** — the Subject-3 pitfall-hunt findings (plan-16) are triaged with **no untriaged blocker**, and
  the plan-21 re-audit re-confirmed it (A2). Each pitfall is fixed, post-rc.1-roadmapped, or accepted.

## D. Acceptance-decision resolutions (plan-8) recorded as LANDED

The gate records what plan-8 already chose; it does NOT re-open "fix OR document."

- **D1 ✅ B2 (FileStore fsync) → FIXED** — `internal/controller/filestore.go` `writeJSONAtomic`
  (`tmp.Sync()` + parent-dir fsync) + audit fsync; regression-covered. (Host-loss SPOF remains a
  separately-documented post-rc.1 limitation.)
- **D2 ✅ B3 (passkey origin) → FIXED** — `internal/api/handler_passkey.go:236-246` requires a non-empty
  Origin so `internal/trustlist/webauthn.go:170-171`'s advisory gate is authoritative for login.
- **D3 ✅ S9 (login-lockout DoS) → DOCUMENTED** — accepted property (`internal/api/loginratelimit.go`
  per-username+per-IP 429; break-glass operator token is the escape hatch); recorded in security docs.
- **D4 ✅ S10 (CSRF double-submit) → DOCUMENTED** — threat boundary (TLS proxy + exact-origin allowlist +
  no untrusted sibling subdomains + `YAOG_SECURE_COOKIE=true`); `internal/api/cookie_session.go:97`.

## E. Release mechanics ready

- **E1 ⏳** — CHANGELOG `## [Unreleased]` rolled to `## [2.0.0-rc.1] - <date>` (+ compare link, fresh
  empty `## [Unreleased]`), asserted **fixes-only** (`git log v2.0.0-beta.8..HEAD --oneline` reviewed; an
  rc carries no new features). *(Executed at tag time — see runbook step 1.)*
- **E2 ⏳ OWNER** — annotated tag `v2.0.0-rc.1` from `main` with the project identity (runbook step 4).
- **E3 ⏳ OWNER** — the release ends up **`--latest`/"Latest"** (rc.1 promoted, beta.8 demoted) — the
  **explicit 2026-06-18 owner override** of `RELEASING.md:54-56`'s prerelease/not-latest default. Belt:
  the `make_latest:` expression on the `softprops` step (plan-22 Task 4b). Suspenders: pre-create
  `--latest`.
- **E4 ⏳ OWNER** — post-tag: `release.yml` + `docker.yml` green; a published binary's `version` subcommand
  prints `v2.0.0-rc.1`; `gh release view v2.0.0-rc.1` shows `isLatest: true`.

---

## Required-status-checks set for `main` (Task 6)

Branch protection must require: **`go`, `frontend`, `conformance`, `frontend-e2e`, `realtunnel`** (and,
recommended, `security-scan`). Verify:

```bash
gh api repos/kunori-kiku/yet-another-overlay-generator/branches/main/protection \
  --jq '.required_status_checks.contexts'
```

⏳ OWNER — setting branch protection is a repo-admin action. (CI already runs all six jobs on every PR;
this makes them *blocking*.)

## Phase-9 precondition — realtunnel 20/20 bake-in + negative proof

A REQUIRED gate must not be flaky. Before tagging, run the **`realtunnel-bakein`** workflow
(`Actions → realtunnel-bakein → Run workflow`, 20 runs): require **20/20** green canary + the
`drop-snat` negative proof catches the broken wire. ⏳ OWNER (manual dispatch). Local proof: 3/3 +
negative-proof green on the dev kernel (2026-06-20). **✅ DISCHARGED 2026-06-21: CI 20/20 + negative
proof green** (Actions run 27881474085).

| date | env | bake-in | negative proof | evidence |
|------|-----|---------|----------------|----------|
| 2026-06-20 | local (kernel 6.8) | 3/3 | ✅ drop-snat caught | dev box |
| 2026-06-21 | CI `ubuntu-latest` | **✅ 20/20** | ✅ drop-snat caught | [Actions run 27881474085](https://github.com/kunori-kiku/yet-another-overlay-generator/actions/runs/27881474085) (success) |

---

## Release runbook (executed at the terminal cut — owner)

Per `RELEASING.md:27-56` with the corrected publish mechanism + the `--latest` owner override:

1. **Roll CHANGELOG** — move `## [Unreleased]` → `## [2.0.0-rc.1] - <date>`, leave a fresh empty
   `## [Unreleased]`, add the compare link at file bottom. Confirm the section is **fixes-only**
   (`git log v2.0.0-beta.8..HEAD --oneline` — no feature commits).
2. **Confirm criteria A–E green** here (incl. B6 `realtunnel` + the required-checks set).
3. **Run/owe the three hardware smokes** (RUNBOOK §C1/§C2/§C3) + smoke #5 — each passed or
   owner-accepted-risk (written rationale here + in release notes).
4. **Annotated tag** from `main`:
   ```bash
   GIT_AUTHOR_NAME=kunori-kiku GIT_AUTHOR_EMAIL=rokuyanlin@gmail.com \
   GIT_COMMITTER_NAME=kunori-kiku GIT_COMMITTER_EMAIL=rokuyanlin@gmail.com \
   git tag -a v2.0.0-rc.1 -m "v2.0.0-rc.1 — <one-line summary>"
   ```
5. **Publish as Latest** — primary (pre-create, then push so `softprops` UPDATES + keeps Latest):
   ```bash
   gh release create v2.0.0-rc.1 --latest --notes-file <notes>   # BEFORE the push
   git push origin v2.0.0-rc.1
   ```
   fallback: `git push origin v2.0.0-rc.1` then `gh release edit v2.0.0-rc.1 --latest`.
   **WRONG:** a bare `gh release create … --latest` AFTER the push collides with the auto-created release
   and errors.
6. **Watch** `release.yml` (7-target matrix + standalone agent) + `docker.yml` (GHCR) go green.
7. **Verify** a downloaded binary's `version` subcommand prints `v2.0.0-rc.1` AND
   `gh release view v2.0.0-rc.1` shows `isLatest: true` (beta.8 demoted).

**Rollback:** delete the tag (`git push origin :v2.0.0-rc.1`) + un-publish the release; **never re-point an
existing annotated tag**; re-promote beta.8 (`gh release edit v2.0.0-beta.8 --latest`) so the fleet's
Latest pointer does not dangle.

**Owner override banner:** `RELEASING.md:54-56` defaults rc.N to `--prerelease`/not-latest; the
**2026-06-18 owner decision OVERRIDES** that for rc.1 (rc.1 IS Latest, the headline soak target). The
general rule still governs `-beta.`/`-preview.` (those stay not-latest).

## What forces a beta.9 vs an owner-accepted exception

- A **new confirmed blocker** (A2 — a plan-16 pitfall or a `realtunnel` failure) → fix → beta.9 → re-run.
- A **red required gate** (B1–B6) → fix → re-run. Never tag over a red required check.
- An **owed hardware smoke that cannot run** (RUNBOOK §C1/§C2/§C3) or smoke #5 → owner-accepted-risk with
  written rationale here + in release notes. These are the ONLY categories that may cross the tag
  un-completed, and only with an explicit signature.

---

## Owner go/no-go sign-off

rc.1 is cut ONLY when every criterion above is `✅ GO` or carries a signed exception below. The
`realtunnel-bakein` 20/20 + negative proof is **✅ DONE** (CI run 27881474085, 2026-06-21). The remaining
**owner-only** actions: (1) run RUNBOOK §C1/§C2/§C3 + smoke #5 (or accept-risk); (2) set branch protection
to the required-checks set; (3) confirm A–E; (4) execute the runbook to cut + publish the tag.

```
Owner go/no-go:  ☐ GO   ☐ NO-GO
Signature: ______________________   Date: __________
Accepted-risk exceptions (if any): ____________________________________________
```

---

## Reconciliations (audit trail)

- **realtunnel advisory→required (R8):** plan-18 shipped the `realtunnel` canary as a required CI step
  from the start (no `continue-on-error` on the canary; additive scenarios are the only non-blocking part).
  The plan-20 charter exit bar + the plan-21 verdict both include `realtunnel` green (R8 discharged).
- **`-race`:** owned here; suite green under it.
- **`--latest`:** the 2026-06-18 owner override of `RELEASING.md`, encoded in the `make_latest:` expression
  + the runbook.
- **Task 7 (toolchain):** `go.mod` carries `toolchain go1.26.4` (added by plan-21 to clear the stdlib
  CVEs); `govulncheck` ships REQUIRED (not advisory — it caught real CVEs), gosec advisory. Defect #3
  (Dockerfile Go pin vs go.mod) — the toolchain directive binds the build; the `Dockerfile` base image is
  a post-rc.1 alignment note.
