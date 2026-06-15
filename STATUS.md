# STATUS
<!-- regenerated: 2026-06-15 -->
<!-- by: close-phase (subject: extensible-i18n-and-structural-hardening) -->

## Active work

- **Subject:** none active. `extensible-i18n-and-structural-hardening-2026_06_14` just closed
  **DELIVERED** and moved to `implementation_plans/_completed/`. The only remaining non-archived
  folder is `implementation_plans/controller-panel-2026_06_08/` — status not assessed this session;
  a candidate for its own review/closure or a draft-the-next-subject decision.
- **Branch:** `main` @ HEAD (closure commits on `close-i18n-hardening-subject`, pending merge/release).
- **Current plan:** all-done for the closed subject.
- **Last shipped:** plan-9 closeout (outline status refresh + architecture/migration note), PR #95,
  `ef917b0`, 2026-06-14 — completing the i18n + error-envelope subject (PRs #70–#95).

## Open questions / blockers

- **Release pending (user-gated).** `main` is ahead of the last tag by the whole i18n + structural-
  hardening program (#70–#95). Cutting a `v*` tag is outward-facing — the owner's call (the user asked
  to "release", so a tag is the next outward step).
- **Two-node controller smoke owed** (manual; needs a browser + authenticator + two real nodes) —
  carried from the keystone program; code paths are unit-/build-verified.
- No code blockers. `go build/vet/test ./...` + frontend `lint`/`build` green on `main`.

## Next actions

1. **Release** — merge the closure branch, then cut the `v*` tag covering #70–#95 (i18n + error
   envelope + mode-boundary + security/robustness remediation + key-custody fixes).
2. **Two-node controller smoke** on a real deployment when convenient (the long-owed end-to-end pass).
3. **Next subject** — either review/close `controller-panel-2026_06_08`, or draft the next subject.

## Recently closed subjects (last 3)

- `extensible-i18n-and-structural-hardening-2026_06_14` (2026-06-14, **delivered**) — extensible keyed
  i18n + coded-at-source HTTP error envelope (`internal/apierr`) + validator-finding localizer; deploy
  artifacts Englishized; perpetual CJK/bijection gates; the `writeError` shim deleted; post-audit
  security/robustness/mode-boundary remediation + key-custody edge-case fixes (#70–#95).
- `controller-server-authority-redesign-2026_06_12` (2026-06-14, delivered) — server-authoritative
  controller mode, login gate, key custody, prefix split (#59–#65).
- `panel-appshell-redesign-2026_06_09` (closed) — panel shell / routes.
