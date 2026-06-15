# STATUS
<!-- regenerated: 2026-06-15 -->
<!-- by: close-phase (subject: controller-nat-customization) + post-release update (v2.0.0-preview.10) -->

## Active work

- **Subject:** none active. `controller-nat-customization-2026_06_15` just closed **DELIVERED** and
  moved to `implementation_plans/_completed/`. The only remaining non-archived folder is
  `implementation_plans/controller-panel-2026_06_08/` — status not assessed; a candidate for its own
  review/closure or a draft-the-next-subject decision.
- **Branch:** `main` @ `b32a10e` — only `main` remains (all merged work branches deleted:
  `controller-nat/*` + the stale `plan-*` from the prior subject).
- **Released:** `v2.0.0-preview.10` (2026-06-15) — #97 + the controller-nat subject (#98–#106);
  `preview.9` already shipped the i18n + structural-hardening program through #96.
- **Current plan:** all-done for the closed subject.
- **Last shipped:** controller-nat-customization — server-authoritative compile + sticky
  operator-settable NAT port/IP per edge + `listen_port` removal (PRs #98–#104), plus the
  effective-range-overflow locator follow-up (#105) and closeout (#106), `899ff54`, 2026-06-15.
  Reviewed all-Opus twice (comprehensive `wpxkwp77i` → 5 should-fix + 3 nits fixed per-PR;
  re-review `wlpo7gsfk` → GO).

## Open questions / blockers

- **Live NAT round-trip owed (manual).** The subject's end-to-end closure criterion — Compile → edit
  internal port + transit IP on a port-restricted NAT VPS → Save → deploy → confirm the agent listens
  on the chosen port/IP and the forward survives a re-deploy (no drift). Needs a browser + a real NAT
  VPS; code paths are unit/build-verified.
- **Two-node controller smoke owed** (manual; browser + authenticator + two real nodes) — carried from
  the keystone program; code paths are unit/build-verified.
- No code blockers. `go build/vet/test ./internal/...` + frontend `lint`/`build` green on `main`.

## Next actions

1. **Live NAT round-trip + two-node controller smoke** on a real deployment when convenient.
2. **Next subject** — either review/close `controller-panel-2026_06_08`, or draft the next subject.

## Recently closed subjects (last 3)

- `controller-nat-customization-2026_06_15` (2026-06-15, **delivered**) — controller mode made
  server-authoritative + operator-customizable at the NAT boundary: sticky operator-settable per-edge
  NAT port + transit IP, server-authoritative compile-preview (zero-knowledge), per-node `listen_port`
  removed (fixing the always-firing co-hosted overlap rule). PRs #98–#106; released `v2.0.0-preview.10`.
- `extensible-i18n-and-structural-hardening-2026_06_14` (2026-06-14, delivered) — extensible keyed
  i18n + coded-at-source HTTP error envelope (`internal/apierr`) + validator-finding localizer; deploy
  artifacts Englishized; perpetual CJK/bijection gates; post-audit security/robustness/mode-boundary +
  key-custody remediation (#70–#95).
- `controller-server-authority-redesign-2026_06_12` (2026-06-14, delivered) — server-authoritative
  controller mode, login gate, key custody, prefix split (#59–#65).
