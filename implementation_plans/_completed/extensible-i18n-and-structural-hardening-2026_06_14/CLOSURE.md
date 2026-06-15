# Closure — extensible-i18n-and-structural-hardening

**Status: DELIVERED. Closed 2026-06-14. Every executable plan merged to `main`
(PRs #70–#95), each independently reviewed.**

Made **every operator-facing string in YAOG localizable or English** — no Chinese reaches an
English-locale operator on any channel. Built an extensible keyed UI i18n core, a typed
coded-at-source HTTP error envelope (`internal/apierr`), and a validator-finding localizer;
Englishized the deploy artifacts (install scripts / CLI) which have no localization channel;
and added perpetual gates that fail the build/tests on any regression. Folded in the
post-audit security/robustness/mode-boundary remediation and the owner's key-custody
edge-case fixes along the way.

## Shipped (PRs)

| Plan | PR(s) | What |
|---|---|---|
| plan-0 (security) | [#70](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/70) | `ssh_key_path` PowerShell command-injection: escape (bash/PS) + validate (`sshKeyPathCharset`); perpetual injection-gate test. |
| plan-4 (mode parity) | [#71](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/71) | export/deploy-script controller-mode guards + in-flight-compile mode-flip guard. |
| plan-6 (robustness) | [#72](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/72) | clear `pendingShrink` on deploy error + tolerant `readApiErrorMessage` (non-JSON proxy bodies). |
| plan-1 (i18n core) | [#73](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/73) | keyed catalog + `t(lang,key,params?)`/`tError` + fallback ladder + N-language; error seam wired (shape-tolerant before the backend flips). |
| plan-2 (error envelope) | [#74](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/74) | `internal/apierr` stdlib leaf (Code+registry+Error) + nested `{error:{code,message,params}}`; panic+custody coded. |
| plan-1.5 (full migration) | [#75](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/75) | 410 `txt()` sites → `t()` via TS-AST codemod; `txt`/`STRINGS` deleted; build proves completeness. |
| plan-3 (backend errors, bounded) | [#76](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/76) | `render.GenerateKeys` → 4 keygen codes (incl. the owner's reported pinned-pubkey error, now 400+localized) + `writeCodedOr` relay; fixed corrupted `allocator/ip.go` strings. |
| plan-10 (mode-boundary CORE) | [#79](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/79) | shared `switchToLocal()` (fixes a SettingsPage fleet-secret localStorage leak) + `saveDesign()` + dirty-state/`canonicalDesign` (omitempty-mirrored) + client conflict-warn + gate-flush dirty-backup + deploy confirmed-shrink flag. |
| plan-11 (mode-boundary UI gating) | [#80](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/80) | gate Topbar import/export/flush to local + surface Save; mode-split SecurityPage; Connection/Bootstrap controller-only; `RequireControllerMode` route guard; gate pin-key (NodeEditor + NodeForm twin) + SSH editor. |
| plan-12 (backend mode-awareness) | [#81](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/81) | air-gap compute routes behind `operatorAuth` in controller mode (`gateAirgap`); `/api/health` public; `/validate` still usable. |
| plan-3.5a (validator channel) | [#82](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/82) | validator's 89 Chinese findings → local `Code` system + `tValidationError`; the single biggest wrong-language gap closed. |
| plan-3.5b (HTTP envelope) | [#83](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/83)–[#88](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/88), [#90](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/90), [#93](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/93) | every wire error coded end-to-end (CLI/banner · coded relay · deep compile-constraints · render/export buckets · handler.go entrypoints · **full controller surface 110 sites** · auth cluster 65 sites incl. the `authenticateNode` producer-fix) then the `writeError`/`CodeLegacyUncoded`/`WithMessage` shim **DELETED** (grep-gated). ~80 codes; granularity locked by a 6-Opus debate. |
| plan-3.5c (deploy artifacts) | [#92](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/92) | install-script + self-extracting-installer shell content Englishized (no localization channel). |
| key-custody edge cases | [#89](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/89), [#91](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/91) | local-import stranded pubkey-only clearing (`clearStrandedKeys`, keeps valid round-trip keypairs) + symmetric `switchToController`; controller-import drops ALL design keys (`dropAllKeys`) since the agent registry is authoritative. |
| plan-7 (struct-backend) | [#94](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/94) | reserved-id helper (`isReservedNodeID`, 2-site dedup). |
| plan-9 (closeout docs) | [#95](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/95) | outline status refresh + decisions D15–D17 + `i18n-error-envelope-architecture.md`. |

## Process

Per-PR: structure-aware implement → `cd frontend && npm run lint && npm run build` + Go
`vet`/`test` locally + CI on each PR → for the architectural PRs, an independent adversarial
review **workflow** (multi-angle find → dedup → CONFIRMED/REFUTED verify) → fix confirmed
findings → merge `--delete-branch`. The ~110-site controller surface and the 65-site auth
chokepoint were converted with count-asserted Python assemblers (zero-miss) and Opus-reviewed
(status-preservation table; no-auth-bypass proof on the `authenticateNode` producer-fix). The
plan-3.5b error-code **granularity** was locked by a 6-Opus debate (brainstorm×3 → critic×2 →
synthesize) before any code was written.

## Verification at closure

**Automated (CI green on merged `main`; `go test ./...` + `go vet` + `npm run lint && build`):**
- **Perpetual i18n gates** — `TestNoChineseInWireAndDeploySurfaces` (api: no CJK in Go
  string-literal position across api/compiler/render/artifacts/renderer), `TestNoChineseInValidatorSource`,
  `TestRegistryEnglishOnly` (apierr + validator), `TestRegistryBijection` (apierr const==registry==allCodes).
- **Build-enforced parity** — `MessageKey = keyof typeof en`; `zh.ts` typed `Record<keyof typeof en, string>`.
- **Envelope semantics** — `TestWriteCodedOr` (source code wins its status; un-coded → bucket),
  `handler_compile_coded_test` (422 coded envelope + `{cidr}` param), `export_unsafename_test` (400 + `{name}`).
- **Custody** — `clearStrandedKeys`/`dropAllKeys` (reviewed: keeps valid keypairs, no footgun);
  the perpetual `topology_custody_test` still green.
- **Shim-gone grep gate** — `grep 'writeError(' internal/ cmd/` = zero call sites;
  `grep 'CodeLegacyUncoded|WithMessage'` = zero (compiler-enforced).

## What was parked / owed (not defects)

- **Two-node controller smoke** (carried since the keystone program; needs a browser +
  authenticator + two real nodes, cannot run headless): the end-to-end pass remains the owner's
  to run. All code paths are unit-/build-verified.
- **plan-8 (struct-frontend) — triaged to NO refactor** (D16): the audit's controllerStore
  "god-store" + bidirectional-coupling items were rated by the adversarial verifiers as
  real-but-minor / quantitatively wrong / core-REFUTED; a 177-field-store split on refuted
  justification is negative-EV, and the i18n/error debt it cited was already cleared by
  plan-1/2/3.5. The lazy-`getState` store cycle is a deliberate documented runtime-only seam.
- **specs/ = Light edit** (D19), not a heavy `/refresh-specs` — the change shapes error
  responses, not the architectural story.

## Pointers

- Outline + decisions log (D1–D19): `outline.md` (this folder).
- Architecture + migration note: `i18n-error-envelope-architecture.md` (this folder).
- Error-envelope design (2-Opus debate): `design-error-envelope.md`; plan-3.5b granularity
  (6-Opus debate): `plan-3.5b-2026_06_14.md`. Audit: `findings.md`.
- Plan files: `plan-10/11/12`, `plan-3.5`, `plan-3.5b` in this folder (earlier plan-0…3/1.5
  shipped from the outline directly).
- Memory: [[i18n-error-envelope-shipped]] (3 channels, coded-at-source, gates, key-custody rules);
  related [[controller-mode-redesign-decisions]].
- Perpetual guards (never retire): `internal/api/i18n_gate_test.go`,
  `internal/validator/code_test.go`, `internal/apierr/apierr_test.go`.
