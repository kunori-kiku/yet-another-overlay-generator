# STATUS
<!-- regenerated: 2026-06-14 (extensible-i18n-and-structural-hardening checkpoint) -->
<!-- by: post-audit fix program (autonomous) -->

## Active work

- **Subject:** `extensible-i18n-and-structural-hardening-2026_06_14` (in progress — core delivered,
  follow-through remaining). Spine: `implementation_plans/extensible-i18n-and-structural-hardening-2026_06_14/`
  (`outline.md`, `findings.md` = the 10-angle audit's 106 CONFIRMED/18 PLAUSIBLE, `design-error-envelope.md`).
- **Branch:** `main` @ `9a2fecd` (PR #76 merged), synced with origin. No open feature branch.
- **Last shipped:** plan-3 — WireGuard key-gen errors coded (the owner's reported Chinese
  pinned-pubkey error now localizes as a 400) + corrupted `allocator/ip.go` strings fixed
  (PR #76, `9a2fecd`).

### Delivered this program (7 PRs, each independently reviewed → 0 confirmed after fixes)

- **#70 plan-0** — security: `ssh_key_path` PowerShell command-injection (escape via bash/PS helpers + `sshKeyPathCharset` validation + perpetual injection-gate test).
- **#71 plan-4** — controller mode-boundary parity (export/deploy-script guards + in-flight compile mode-flip guard).
- **#72 plan-6** — frontend robustness (clear stuck shrink-modal on deploy error; tolerant API-error reader).
- **#73 plan-1** — extensible keyed i18n core: `t(lang, key, params?)` + `tError` (shape-tolerant) + fallback chain + N-language registry.
- **#74 plan-2** — `internal/apierr` typed error-code package + nested `{error:{code,message,params}}` envelope (frontend already reads it).
- **#75 plan-1.5** — full `txt`→`t` migration (410 sites via TS-AST codemod, 7 parameterized + 3 tuple→key); `txt`/`STRINGS` deleted.
- **#76 plan-3** — render key-gen errors coded + localized; corrupted allocator strings fixed.

All of the owner's explicit asks (extensible i18n; backend errors localizable; the specific Chinese
key-gen error) + all CONFIRMED audit security/robustness findings + the two highest-leverage
structural fixes (i18n architecture + error envelope) are shipped.

## Open questions / blockers

- **Release pending (user-gated).** `main` is ahead of the last tag by #70–#76 (security fix + i18n
  redesign + error envelope). Cutting a `v*` tag is outward-facing — the owner's call.
- No code blockers. `go build/vet/test ./...` + frontend `lint`/`build` green on `main`.

## Next actions

1. **plan-3.5** — code the remaining backend user-facing strings to apierr codes (validator
   schema/semantic/nat is the largest set and surfaces on validate/compile; plus compiler, auth/login/
   passkey/totp/bootstrap, cmd/compiler), add their `error.<code>` frontend catalog keys, then delete
   the transitional `writeError` delegate + `CodeLegacyUncoded` (grep-gated). Pattern is established +
   demonstrated in plan-3 / `design-error-envelope.md`. (Task #27.)
2. **plan-7/8/9** — structural triage (P7 reserved-id helper is the clearest win; the rest were rated
   minor/overstated by the audit verifiers), then docs/spec refresh for the new i18n + error envelope,
   migration note, and `/close-phase` to archive the subject.
3. **Release** when the owner wants (covers #70–#76).

## Recently closed subjects (last 3)

- `controller-server-authority-redesign-2026_06_12` (2026-06-14, delivered) — server-authoritative
  controller mode, login gate, key custody, prefix split (#59–#65).
- `panel-appshell-redesign-2026_06_09` (closed) — panel shell/routes.
- `audit-remediation-and-allocation-stability-2026_06_07` (closed).
