# Keystone trust list

<!-- last-verified: 2026-07-17 -->

## Responsibility

Own the off-host membership authority that binds node identity, WireGuard public key, bundle digest,
and monotonic epoch to deterministic bytes signed by a pinned raw-Ed25519 or WebAuthn credential
(`internal/trustlist/types.go:26-112`, `internal/trustlist/canonical.go:11-67`).

## Files

- `internal/trustlist/canonical.go:11-67` and `internal/trustlist/verify.go:11-90` — canonicalize and
  fail-closed verify membership artifacts.
- `internal/trustlist/webauthn.go:13-269` — verifies content-bound and random-challenge assertions,
  including enrollment-only user verification.
- `internal/controller/keystone.go:18-225` — derives bundle digests and builds monotonic staged manifests.
- `internal/controller/trustlist_sign.go:23-103` — guards and verifies signature installation.
- `internal/controller/keystone_transition.go:13-269` — makes credential CAS plus audit recovery one
  logical transition.
- `internal/api/handler_keystone.go:63-346` and
  `internal/api/handler_webauthn_enrollment.go:22-122` — expose credential/status/signing ceremonies.

## Inputs

`controller-stage-promote` supplies the complete deployment-ready node set, each public key, and
`hex(sha256(checksums.sha256))`; prior staged membership determines the next epoch
(`internal/controller/keystone.go:18-65,125-225`). Browser credentials are admitted only after an
authenticated actor-and-purpose-scoped server challenge proves UP+UV for the exact public candidate
(`internal/api/handler_webauthn_enrollment.go:39-120`). Raw Ed25519 retains its out-of-band path
(`internal/api/handler_keystone.go:145-270`).

## Outputs

Stage stores canonical unsigned `trustlist.json`; signature installation attaches a verified detached
artifact without changing its canonical bytes or epoch. Promotion moves that exact sealed pair to the
served slot, and `controller-agent-api` appends `trustlist.json`/`trustlist.sig` outside the bundle
checksum set (`internal/controller/trustlist_sign.go:42-100`,
`internal/controller/compile_promote.go:32-70`, `internal/api/handler_agent.go:111-132`).

## Decision points (if any)

- With no pinned credential, stage/promotion use the compatibility path; with a pin, unsigned stage is
  allowed but promotion and node apply require a valid signed manifest
  (`internal/controller/compile_stage.go:149-166,296-319`,
  `internal/controller/compile_promote.go:32-70`).
- Verification first requires the artifact algorithm to match the trusted pin, then dispatches only
  on that pin to raw Ed25519, WebAuthn ES256, or WebAuthn EdDSA
  (`internal/trustlist/verify.go:37-69`).
- UV is required only while enrolling a new browser login passkey or keystone credential. Ordinary
  login, later signing, promotion, and node verification keep generic UP+signature acceptance so
  existing users are not retroactively locked out (`internal/trustlist/webauthn.go:43-95`).

## Invariants

- Agents act only on bytes that re-canonicalize exactly to the received `trustlist.json`, then verify
  the signature against the separately pinned credential and recompute their bundle digest
  (`internal/agent/verify.go:315-420`).
- Credential pin/rotation uses a durable expected/next/event marker; stage, sign, promote, bootstrap,
  and status reconcile the exact audit event before using the new trust anchor
  (`internal/controller/keystone_transition.go:13-121,156-269`).
- The signed member set includes unchanged deployment-ready nodes even when their bundle delta-skips,
  and identical membership reuses its epoch while any identity/digest change increments it
  (`internal/controller/keystone.go:29-65,125-225`).

## Gotchas (optional)

- User verification proves one ceremony, not that a credential is hardware-bound or non-copyable;
  attestation is not used and later signatures do not inherit UV as a permanent property
  (`internal/trustlist/webauthn.go:23-25,68-95`, `frontend/src/lib/webauthn.ts:241-268`).
- The submitted signing bytes must match the currently staged canonical bytes under the tenant lock;
  a stale browser assertion cannot be attached after restaging
  (`internal/controller/trustlist_sign.go:42-100`).
- `trustlist.json` has no wall clock and cannot live inside the checksum set whose digest it binds;
  freshness comes from the epoch and exact per-member digest
  (`internal/trustlist/types.go:40-60`, `internal/api/handler_agent.go:111-132`).
