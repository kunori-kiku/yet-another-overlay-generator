# STATUS
<!-- regenerated: 2026-06-15 -->
<!-- by: close-phase (subject: controller-nat-customization) -->

## Active work

- **Subject:** none active. `controller-nat-customization-2026_06_15` just closed **DELIVERED** and
  moved to `implementation_plans/_completed/`. The only remaining non-archived folder is
  `implementation_plans/controller-panel-2026_06_08/` — status not assessed; a candidate for its own
  review/closure or a draft-the-next-subject decision.
- **Branch:** `main` @ HEAD (closure commits on `controller-nat/subject-closeout`, pending merge).
- **Current plan:** all-done for the closed subject.
- **Last shipped:** controller-nat-customization — server-authoritative compile + sticky
  operator-settable NAT port/IP per edge + `listen_port` removal (PRs #98–#104), plus the
  effective-range-overflow locator follow-up (#105), `899ff54`, 2026-06-15. Reviewed all-Opus twice
  (comprehensive `wpxkwp77i` → 5 should-fix + 3 nits fixed per-PR; re-review `wlpo7gsfk` → GO).

## Open questions / blockers

- **Live NAT round-trip owed (manual).** The subject's end-to-end closure criterion — Compile → edit
  internal port + transit IP on a port-restricted NAT VPS → Save → deploy → confirm the agent listens
  on the chosen port/IP and the forward survives a re-deploy (no drift). Needs a browser + a real NAT
  VPS; code paths are unit/build-verified.
- **Release pending (user-gated).** `main` is ahead of the last tag by the i18n + structural-hardening
  program (#70–#95) AND now the controller-nat subject (#98–#105). Cutting a `v*` tag is outward-facing
  — the owner's call.
- **Two-node controller smoke owed** (manual; browser + authenticator + two real nodes) — carried from
  the keystone program; code paths are unit/build-verified.
- No code blockers. `go build/vet/test ./internal/...` + frontend `lint`/`build` green on `main`.

## Next actions

1. **Merge the closure branch** (`controller-nat/subject-closeout`: archives the subject folder +
   regenerates this file).
2. **Live NAT round-trip + two-node controller smoke** on a real deployment when convenient.
3. **Release** — cut the `v*` tag covering #70–#105 when the owner is ready.
4. **Next subject** — either review/close `controller-panel-2026_06_08`, or draft the next subject.

## Recently closed subjects (last 3)

- `controller-nat-customization-2026_06_15` (2026-06-15, **delivered**) — controller mode made
  server-authoritative + operator-customizable at the NAT boundary: sticky operator-settable per-edge
  NAT port + transit IP, server-authoritative compile-preview (zero-knowledge), per-node `listen_port`
  removed (fixing the always-firing co-hosted overlap rule). PRs #98–#105.
- `extensible-i18n-and-structural-hardening-2026_06_14` (2026-06-14, delivered) — extensible keyed
  i18n + coded-at-source HTTP error envelope (`internal/apierr`) + validator-finding localizer; deploy
  artifacts Englishized; perpetual CJK/bijection gates; post-audit security/robustness/mode-boundary +
  key-custody remediation (#70–#95).
- `controller-server-authority-redesign-2026_06_12` (2026-06-14, delivered) — server-authoritative
  controller mode, login gate, key custody, prefix split (#59–#65).
