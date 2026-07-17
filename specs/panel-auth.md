# Panel auth

<!-- last-verified: 2026-07-17 -->

## Responsibility

Own the controller-mode sign-in gate, volatile operator authentication state, account-factor settings,
browser WebAuthn ceremonies, authenticated request plumbing, and the controller store's browser
persistence boundary (`frontend/src/components/shell/Shell.tsx:18-24`,
`frontend/src/stores/controller/auth.ts:38-74`, `frontend/src/stores/controller/persist.ts:14-45`).

## Files

- `frontend/src/components/auth/LoginPage.tsx:18-57`,
  `frontend/src/components/auth/LoginPage.tsx:85-209`, and
  `frontend/src/components/auth/LoginPage.tsx:217-339` — collect connection settings, password/TOTP
  or passwordless-passkey login, and the separately buffered break-glass token.
- `frontend/src/components/pages/SecurityPage.tsx:8-29` — mounts controller account security controls.
- `frontend/src/components/deploy/TwoFactorSettings.tsx:19-117` and
  `frontend/src/components/deploy/TwoFactorSettings.tsx:128-245` — manage the component-local TOTP
  enrollment secret, confirmation code, status, and disable flow.
- `frontend/src/components/deploy/PasskeySettings.tsx:21-118` — manages login-passkey status,
  registration retry, removal, and local ceremony errors.
- `frontend/src/components/deploy/WebAuthnEnrollmentNotice.tsx:4-12` and
  `frontend/src/components/deploy/webauthnEnrollmentPolicy.ts:1-10` — provide the shared prospective
  enrollment warning and its account-status display rule.
- `frontend/src/stores/controller/auth.ts:88-718` — owns connection/auth state, login/session lifecycle,
  TOTP actions, and login-passkey actions.
- `frontend/src/api/controller/auth.ts:103-222` and `frontend/src/api/controller/auth.ts:224-415` —
  map login, session, TOTP, and login-passkey HTTP contracts into frontend types.
- `frontend/src/api/controller/transport.ts:46-60` and
  `frontend/src/api/controller/transport.ts:84-148` — build operator URLs and apply the common cookie,
  bearer, CSRF, and coded-error behavior.
- `frontend/src/api/controller/webauthnEnrollment.ts:1-26` — obtains an authenticated, purpose-scoped
  enrollment challenge.
- `frontend/src/lib/webauthn.ts:209-334` and `frontend/src/lib/webauthn.ts:382-466` — create credential
  candidates and produce enrollment or ordinary login assertions.
- `frontend/src/stores/controller/persist.ts:1-57` — defines the single controller-store localStorage
  allowlist and local-only merge guard.

## Inputs

[Panel shell](panel-shell.md) invokes `checkSession`, waits for that probe, and shows `LoginPage` only
when neither a session nor break-glass context opens the controller gate
(`frontend/src/components/shell/Shell.tsx:39-81`).
The [controller operator API](controller-operator-api.md) supplies login outcomes, session identity,
account-factor state, and purpose-scoped enrollment challenges through the typed client boundary
(`frontend/src/api/controller/auth.ts:38-60`, `frontend/src/api/controller/auth.ts:103-166`,
`frontend/src/api/controller/auth.ts:361-407`,
`frontend/src/api/controller/webauthnEnrollment.ts:6-25`). The
[keystone trust-list component](keystone-trustlist.md) invokes the same browser create-and-prove
ceremony with the distinct `keystone` purpose (`frontend/src/stores/controller/keystone.ts:103-149`).

## Outputs

Successful password, password-plus-factor, or passwordless-passkey login establishes the current
operator identity, expiry, controller capabilities, and volatile session/CSRF state, then runs the
full authenticated-context hydration (`frontend/src/stores/controller/auth.ts:41-50,188-269`,
`frontend/src/stores/controller/auth.ts:517-562`). Cookie-session restore establishes the same
identity/CSRF/version fields but deliberately hydrates only keystone status and, when needed, the
server-authoritative topology; Fleet and account-factor refreshes wait for their owning surfaces
(`frontend/src/stores/controller/auth.ts:358-414`).
`configOf` selects the session bearer ahead of break-glass, and the shared transport includes cookies,
adds that bearer when present, and adds CSRF to state-changing requests
(`frontend/src/stores/controller/helpers.ts:209-219`, `frontend/src/api/controller/transport.ts:112-135`).
Account settings emit TOTP enroll/confirm/disable requests and login-passkey public descriptors plus
signed assertions; an unconfirmed TOTP secret remains component-local
(`frontend/src/components/deploy/TwoFactorSettings.tsx:28-87`,
`frontend/src/api/controller/auth.ts:192-220`, `frontend/src/api/controller/auth.ts:269-315`).

## Decision points (if any)

- Password login branches into a passkey ceremony, a TOTP input step, or a successful authenticated
  context; hard failure collapses the TOTP step (`frontend/src/stores/controller/auth.ts:179-282`).
- A session probe counts as an account login only when it returns a CSRF value; an authenticated
  break-glass response remains a recovery context without account identity
  (`frontend/src/stores/controller/auth.ts:358-441`).
- Candidate creation and the immediate enrollment proof require user verification, while ordinary
  login assertions request it as preferred so historical non-UV credentials remain usable
  (`frontend/src/lib/webauthn.ts:241-268`, `frontend/src/lib/webauthn.ts:303-334`,
  `frontend/src/lib/webauthn.ts:391-439`).

## Invariants

- Session and break-glass bearers, CSRF, TOTP material, live telemetry, and pending ceremonies never
  enter localStorage; the explicit allowlist contains only connection endpoints, public keystone
  handles, mode, telemetry-stripped nodes, settings, and a non-Fleet sync timestamp
  (`frontend/src/stores/controller/persist.ts:14-45`).
- Endpoint, credential, login, logout, session-loss, and identity boundaries advance or check the auth
  generation so stale asynchronous work cannot repopulate a later context
  (`frontend/src/stores/controller/auth.ts:34-74`, `frontend/src/stores/controller/auth.ts:119-173`,
  `frontend/src/stores/controller/auth.ts:289-356`).
- A login passkey is an account factor and the keystone is a separate deployment credential; the
  browser client shares ceremony mechanics but uses distinct enrollment purposes
  (`frontend/src/components/deploy/PasskeySettings.tsx:9-20`,
  `frontend/src/api/controller/webauthnEnrollment.ts:1-19`).

## Gotchas (optional)

- Enrollment requests no attestation: required UV proves the first-party ceremony, not hardware
  provenance or non-exportability, so both credential surfaces render one warning
  (`frontend/src/lib/webauthn.ts:264-268`, `frontend/src/lib/webauthn.ts:303-307`,
  `frontend/src/components/deploy/WebAuthnEnrollmentNotice.tsx:4-12`,
  `frontend/src/components/deploy/DeployBar.tsx:203-232`).
- Credential creation may succeed before UV proof or persistence fails; login and keystone flows keep
  only the public candidate in volatile state so Retry reuses it rather than creating a duplicate
  (`frontend/src/stores/controller/auth.ts:595-675`, `frontend/src/stores/controller/keystone.ts:103-190`).
- Browser WebAuthn requires an available secure context and rejects IP-literal RP IDs; the client
  raises actionable guidance before invoking the opaque browser failure
  (`frontend/src/lib/webauthn.ts:75-101`, `frontend/src/lib/webauthn.ts:179-206`,
  `frontend/src/lib/webauthn.ts:219-227`).
