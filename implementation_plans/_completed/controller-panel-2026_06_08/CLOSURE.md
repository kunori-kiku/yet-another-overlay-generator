# Closure — controller-panel (2.0 program) — 2026-06-08

**Status: DELIVERED-VIA-SUCCESSOR-SUBJECTS.**

This program's mission — evolve YAOG into an agent-pull controller panel while keeping
the air-gap path byte-for-byte intact — **shipped**, but not through this folder's own
numbered plans. The work was re-decomposed into a sequence of tighter, independently
reviewed successor subjects (now archived in `_completed/`). This folder's `plan-4`/`plan-5`
milestone-level designs were partly **retracted** during that re-decomposition; see below.

## What shipped (and where)

The headline outcomes in this outline's Success Criteria are all live on `main`:

- **Zero-knowledge WG key custody** (controller stores public keys + config only),
  **Ed25519-signed bundles verified before root apply**, and a **node agent**
  (keygen → enroll → poll → verify → anti-rollback → splice key → apply → report) —
  delivered across `v1.4.0` and `v2.0.0-preview.1`.
- **Off-host keystone signing** (hardware-passkey-signed trust-list; membership changes
  require a signature the controller can't forge) — `v2.0.0-preview.2`.
- **Operator authentication** (password + TOTP 2FA + WebAuthn passkey), signing-at-rest,
  loopback-bound compose — `v2.0.0-preview.3`.
- **Dashboard app-shell + cookie-session login** — `v2.0.0-preview.4`.
- **Server-authoritative controller mode**, path-prefix audience split, login-as-data-boundary
  — successor subject `controller-server-authority-redesign-2026_06_12` (`preview.6`/`.7`).
- **Extensible keyed i18n + coded error envelope** — `extensible-i18n-and-structural-hardening-2026_06_14`.
- **Operator-customizable NAT boundary** (sticky per-edge pins, zero-knowledge compile preview)
  — `controller-nat-customization-2026_06_15` (`preview.10`).

The four perpetual gates from §9 are live: no-private-key custody guard, canonical-serializer
golden, install.sh signature-verify, and the keystone digest tests.

## What was retracted (design did NOT ship as written)

The Phase 2/3 milestone designs in `plan-4`/`plan-5` were **deliberately trimmed**; the
following are NOT in the shipped system:

- **mTLS / per-node client certs / short-lived certs + overlay eviction** — dropped in favor
  of **plain-HTTP + bearer tokens**, with trust anchored in the operator's off-host
  hardware/Bitwarden-signed trust-list rather than transport. (See the security-model keystone
  rationale.)
- **PostgreSQL registry** — single-tenant **FileStore** shipped instead (the FileStore mutex/
  poll SPOF is a documented deferral to rc.2/GA).
- **Hosted multi-tenant isolation + per-tenant KMS + OIDC/RBAC** — **struck** for the single-
  tenant scope; KMS/OIDC remain gated on provider forks, not built.

## Why this folder is archived without its own plan rows marked done

The numbered plans here (`plan-1`…`plan-5`, plus the `plan-4.x` expansions) were a planning
scaffold; execution happened under the successor subjects' own plan files and PRs (#38–#106,
across `v1.4.0`→`v2.0.0-preview.10`). The status table below is left as-authored for history;
the real shipped record lives in the successor subjects' closure READMEs and the project
`CHANGELOG.md`.

## Pointer

- Latest release at closure: `v2.0.0-preview.10`.
- Successor subjects: `controller-server-authority-redesign-2026_06_12`,
  `extensible-i18n-and-structural-hardening-2026_06_14`,
  `controller-nat-customization-2026_06_15`, `panel-appshell-redesign-2026_06_09`.
- Active follow-on: `signed-self-update-and-rc-hardening-2026_06_15` (RC hardening + signed
  self-update toward beta).

*Archived to `_completed/` by plan-1 of `signed-self-update-and-rc-hardening-2026_06_15`.*
