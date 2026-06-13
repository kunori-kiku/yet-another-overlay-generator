# Closure — controller-server-authority-redesign

**Status: DELIVERED. Closed 2026-06-14. All seven plans (+ plan-1.5) merged to `main`
(PRs #59–#65), each independently reviewed.**

Made YAOG **controller mode server-authoritative end-to-end**: the controller's stored
design is the single source of truth, the browser cache is a disposable mirror, WireGuard
private keys are enforced never to cross the server boundary, the secret path prefix is
split per audience, and the identity / safety gaps surfaced by the live-deployment debugging
session are closed. Local/air-gap mode is unaffected.

## Shipped (PRs)

| Plan | PR | Merge | What |
|---|---|---|---|
| P1 + P1.5 | [#59](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/59) | 2778754 | Prefix split (`YAOG_OPERATOR_PATH_PREFIX` / `YAOG_AGENT_PATH_PREFIX`, clean break, fail-loud on the old env) + startup base-path log; `POST /update-topology` rejects (400) private-key-bearing payloads and stores canonical re-marshaled bytes; missing `update-topology`/`promote` audits added; server reports `agent_path_prefix` in `GET /settings` and EnrollmentFlow composes the one-liner from it. Perpetual custody test. |
| P2 | [#60](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/60) | 83fb3e0 | Bounded topology version history (N=10) + `GET /topology?version=N` / `GET /topology/versions`; both Store impls. Review-hardened: crash-orphan invisible, corrupt-entry skipped, upgrade backfill, stage write-back dedupe. |
| P3 | [#61](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/61) | 3c37048 | Orphaned-agent idle backoff (perpetual guard); promote flips only the current generation's staged bundles + purges stale staged bundles; empty-stage path purges + audits (`stage-empty`); per-tenant op lock over stage/promote/enroll/rekey. |
| P4 | [#62](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/62) | 18d267e | Full-page login gate (`components/auth/LoginPage.tsx`) before any chrome; `hydrateFromServer()` overwrites the local canvas on every login/cookie-restore; per-divergent-overwrite pre-hydration backup stash. Review-hardened: break-glass field usable, no silent data loss, semantic diff. |
| P5 | [#63](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/63) | 0833b60 | Client strip of private keys before `update-topology` (mirror of the server 400); controller-mode import placeholders secrets; controller→local switch dialog (graph survives, keys/pins/compile-history purge); shrink/empty deploy typed-confirm guard (≥50%). |
| P6 | [#64](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/64) | 2992abe | Identity invariant: one approved WG pubkey ↔ one node-id, enforced on both write paths (`Enroll` + `Rekey` via shared `CheckWGKeyUnique` under the op lock); token-mint design-membership warning; fleet "not in design" markers + post-deploy orphan list with manual revoke. |
| P7 | [#65](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/65) | 24f044e | Docs accuracy pass + breaking-change migration note (`docs/MIGRATION-controller-server-authority.md`) + closure smoke. Doc-accuracy review caught a real gap — a `/rekey` duplicate-key refusal was 409'd but **unaudited** — fixed in code (`rekey-rejected-duplicate-key` audit), not papered over. |

## Process

Per-PR: structure-aware implement → `cd frontend && npm run lint && npm run build` + Go
`vet`/`test` (local toolchain available this round + CI on each PR) → independent adversarial
review **workflow** (multi-angle find → dedup → CONFIRMED/PLAUSIBLE/REFUTED adversarial
verify) → fix the confirmed findings → merge `--delete-branch`. Every PR merged with its
confirmed findings resolved. Notable caught-and-fixed: break-glass field unusable (P4),
silent data loss on re-login (P4), enroll-dedupe TOCTOU + missing rekey dedupe (P6), custody
anti-smuggle / empty-stage-must-purge (P1/P3), and the unaudited rekey refusal (P7).

## Verification at closure

**Automated (CI green on merged `main`; `go test ./...`, `npm run lint && build`):**
- Custody — `internal/api/topology_custody_test.go` (perpetual): keyed payload 400, stored
  bytes key-free, canonical-storage anti-smuggle.
- History — `internal/controller/store_compat_test.go` (both impls): retention bound,
  newest-first, byte-exact round-trip, crash-orphan invisibility, upgrade backfill, corrupt skip.
- Agent idle + promote scoping — `cycle_idle_test.go` (perpetual), `promote_scope_test.go`.
- Identity — `enrollment_dedupe_test.go` (perpetual): one approved pubkey ↔ one id, rekey
  path (incl. the rekey-refusal audit), whitespace evasion, revoked-frees-binding.

**Live HTTP smoke (dev controller, two prefixes, 12/12 PASS):** startup log names both base
paths; operator login under the operator prefix on :8080 and 404 on :9090; bootstrap under the
agent prefix on :9090 and 404 on :8080; bare paths 404; keyed update-topology → 400, clean →
200; 12 puts retain exactly 10 versions, `?version=N` fetch + pruned-404 + malformed-400.

## What was parked / owed (not defects)

- **Live deployment migration** — the user's `overlay.kunorikiku.com` deploy still needs the
  env rename (`YAOG_CONTROLLER_PATH_PREFIX` → operator/agent pair) applied and operators
  re-logged-in. The migration note is published for it (`docs/MIGRATION-controller-server-authority.md`).
- **Manual browser two-node smoke** (carried since the keystone program; needs a browser +
  authenticator + two real nodes, cannot run headless): SC1 (cache-clear → login → hydrated
  canvas), SC5 (persisted controller mode → full-page login), SC6 (controller→local warns +
  preserves graph + purges secrets), SC8 (fleet "not in design" markers + one-click revoke),
  plus login-survives-refresh, dark/light, no token in localStorage. Code paths are
  unit-/build-verified; the end-to-end pass remains the user's to run on `http://localhost`.
- **Out of scope by decision (D10):** multi-tenant / OIDC / KMS, legacy-form light theming,
  rollback UI (history API only, no panel control), auto-revocation (manual only).

## Pointers

- Outline + decisions log: `outline.md` (this folder).
- Plan files: `plan-1` … `plan-7` (+ `plan-1.5`) in this folder.
- Migration note: `docs/MIGRATION-controller-server-authority.md`.
- Memory: [[controller-mode-redesign-decisions]] (updated to mark the redesign shipped).
- Perpetual guards (never retire): `internal/api/topology_custody_test.go`,
  `internal/controller/enrollment_dedupe_test.go`, `internal/agent/cycle_idle_test.go`.
