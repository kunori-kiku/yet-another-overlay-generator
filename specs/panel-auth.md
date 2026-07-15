# Panel Auth — Operator Login UI + Client Auth State

<!-- last-verified: 2026-07-16 -->

## Responsibility

Authenticate a controller-panel operator by password plus an optional TOTP or login-passkey factor,
by passwordless login passkey, or by the break-glass bearer. Keep the resulting session, CSRF, and
account-security state in volatile browser memory, and drive login-passkey enrollment/removal without
confusing it with the separate tenant keystone used to sign deployments.

Controller mode is gated before the application chrome renders. `Shell` first probes the httpOnly
session cookie, shows a quiet splash while that probe is unresolved, and renders the full-page
`LoginPage` only when there is neither a restored login nor a break-glass token
(`frontend/src/components/shell/Shell.tsx:18-24,39-81`). A successful login or cookie restore then
hydrates the canvas from the server; a divergent non-empty local design is exported before the
server-authoritative copy replaces it (`frontend/src/stores/controller/sync.ts:52-101`).

## Files

- `frontend/src/components/auth/LoginPage.tsx:11-17,85-209,217-339` — the pre-shell login gate:
  username/password, conditional TOTP, passwordless passkey, connection settings, break-glass
  recovery, language/theme controls, and the local-mode escape.
- `frontend/src/components/deploy/TwoFactorSettings.tsx` — TOTP status, enrollment/confirmation,
  and disable-with-current-code UI.
- `frontend/src/components/deploy/PasskeySettings.tsx:9-20,21-57,59-118` — login-passkey status,
  prospective two-prompt registration with retry, and fresh-assertion removal.
- `frontend/src/components/deploy/WebAuthnEnrollmentNotice.tsx:4-11` — one shared warning rendered
  by both login-passkey and keystone enrollment surfaces, so their compatibility rationale and
  attestation boundary cannot drift.
- `frontend/src/components/pages/SecurityPage.tsx:8-29` — mode-split `/security`: controller account
  security and audit versus local compile history.
- `frontend/src/components/shell/UserMenu.tsx:8-20,61-108` — signed-in identity, expiry, logout,
  break-glass/local-mode state, and controller version.
- `frontend/src/stores/controllerStore.ts:1-50` — stable public Zustand hook composed from domain
  slices under one `create()+persist`; topology state remains in the independent `topologyStore`.
- `frontend/src/stores/controller/auth.ts:85-448,451-555,557-703` — password/TOTP/passkey login,
  session restore/logout, account-security status, login-passkey registration, and removal.
- `frontend/src/stores/controller/types.ts:28-31,39-87,185-190,224-237` — the shared controller-state
  contract, including the two volatile pending enrollment candidates and separate login/keystone
  ceremony flags.
- `frontend/src/stores/controller/helpers.ts:173-225` — `configOf`, `selectLoggedIn`, and
  `selectHasAuth`.
- `frontend/src/stores/controller/persist.ts:1-45` — the single auditable controller-store
  localStorage allowlist.
- `frontend/src/api/controllerClient.ts:1-30` — compatibility barrel; the implementation is split
  into domain modules under `frontend/src/api/controller/`.
- `frontend/src/api/controller/auth.ts:17-149,152-366` — login/session, TOTP, passkey management,
  passwordless begin/finish, and the snake_case-to-camelCase boundary.
- `frontend/src/api/controller/webauthnEnrollment.ts:1-26` — the shared authenticated,
  purpose-scoped enrollment-challenge route.
- `frontend/src/api/controller/transport.ts:46-60,84-148` — URL construction and the common
  Bearer/cookie/CSRF request path.
- `frontend/src/lib/webauthn.ts:51-206,211-334,338-465` — credential creation, enrollment-only UV proof,
  login assertions, manifest assertions, and typed ceremony errors.

## Inputs

- Operator input from `LoginPage`: username, password, optional six-digit TOTP, or a break-glass
  token; passwordless login still needs a username so the controller can select the account's
  credential (`frontend/src/components/auth/LoginPage.tsx:34-49,85-209,262-307`).
- Controller responses from `POST /login`, `GET /session`, the TOTP/passkey management routes, the
  unauthenticated passwordless begin/finish routes, and authenticated
  `POST /webauthn/enrollment/begin` (`internal/api/routes_controller.go:280-306`).
- Browser authenticator calls through `navigator.credentials.create()` and `.get()`; WebAuthn is
  rejected early for an IP-literal RP ID and requires a secure context or localhost
  (`frontend/src/lib/webauthn.ts:85-101,181-206,219-300,416-465`).
- The httpOnly session cookie sent by `credentials:'include'`, plus the readable CSRF value returned
  by login/session and held only in memory (`frontend/src/api/controller/transport.ts:112-148`).

## Outputs

- `configOf(state): ControllerConfig`: effective bearer `sessionToken || operatorToken` plus the
  in-memory CSRF token (`frontend/src/stores/controller/helpers.ts:173-184`). The transport adds
  `Authorization` when a bearer exists and `X-CSRF-Token` on state-changing requests when a CSRF
  value exists (`frontend/src/api/controller/transport.ts:112-148`).
- `selectLoggedIn` gates the full-page login UI; `selectHasAuth` also admits a cookie-restored
  session or break-glass bearer for operator actions (`frontend/src/stores/controller/helpers.ts:208-225`).
- Login assertions in the historical `SignedTrustList` wire shape, posted to password+passkey login,
  passwordless finish, and passkey-disable finish. Their `public_key` field is empty because the
  controller verifies the pinned account key (`frontend/src/lib/webauthn.ts:391-407,453-464`).
- A `RegisterPasskeyBody` containing the candidate public descriptor, RP/origin bindings, and a
  one-use enrollment assertion to `POST /passkey/register`
  (`frontend/src/api/controller/auth.ts:208-253`).
- Auth-derived UI state including `totpRequired`, `totpEnabled`, `passkeyRegistered`,
  `loginCeremony`, and the volatile `pendingLoginPasskeyEnrollment`
  (`frontend/src/stores/controller/types.ts:75-87,185-190,233-237`).

## Decision points

- **Login outcome branching:** a success stores session/CSRF identity and hydrates/refreshes server
  state; `passkey_required` performs the assertion in-place and resubmits while the password remains
  in the closure; `totp_required` expands the code field; a hard failure resets the TOTP step
  (`frontend/src/stores/controller/auth.ts:168-274`).
- **Cookie restore versus break-glass:** `GET /session` can return 200 for either. Only a response with
  a non-empty CSRF token sets `loggedIn`; break-glass remains an authenticated recovery path without
  masquerading as an account login (`frontend/src/stores/controller/auth.ts:346-448`).
- **Login-passkey removal is two-phase:** begin is idempotently `done` when nothing is registered;
  otherwise a fresh account-key assertion is required before deletion
  (`frontend/src/stores/controller/auth.ts:662-700`).
- **UV is prospective and enrollment-only:** registration obtains a `login`-purpose challenge,
  creates a candidate with `userVerification:'required'`, then asks that exact candidate to assert
  the server nonce with UV required. The controller verifies the candidate signature, credential ID,
  RP/origin bindings, and signed UV bit before storing it
  (`frontend/src/stores/controller/auth.ts:579-660`; `frontend/src/lib/webauthn.ts:219-334`;
  `internal/api/handler_webauthn_enrollment.go:77-121`).
- **Existing users remain compatible:** ordinary login and signing assertions ask the browser for
  `userVerification:'preferred'`, while the generic server/node verifier requires user presence and
  a valid signature but does not reject solely because UV is absent. This avoids retroactively
  locking out credentials and already-deployed fleets accepted under earlier releases
  (`frontend/src/lib/webauthn.ts:358-407,410-465`; `internal/trustlist/webauthn.go:43-95,118-146`).
- **Failed second phase retries the same credential:** after `create()` succeeds, the public
  candidate stays in volatile state if UV proof or persistence fails. A retry mints a fresh server
  nonce and calls `get()` for that candidate instead of creating a duplicate. A status read resolves
  the lost-response case when the server did commit (`frontend/src/stores/controller/auth.ts:579-660`).
- **Ceremony flags stay separated:** login, login-passkey registration, and removal use
  `loginCeremony`; keystone enrollment/signing use `enrolling`/`signing`, so an account ceremony does
  not light the deploy-authorization banner (`frontend/src/stores/controller/types.ts:224-237`).
- **Account-status projections are ordered:** TOTP and login-passkey probes are latest-started-wins
  within an authentication generation. Successful confirm/register/disable mutations invalidate
  every older probe before projecting their result, so a stale response cannot re-expose an
  enroll/replace action (`frontend/src/stores/controller/auth.ts:85-90,451-500,557-700`;
  `frontend/src/stores/controllerStore.authContext.test.ts:84-143`;
  `frontend/src/stores/controllerStore.webauthnEnrollment.test.ts:320-399`).

## Invariants

- The frontend has separate stores with different authority: `topologyStore` owns the design/canvas
  and local WASM actions; the composed `controllerStore` owns controller connection, auth, fleet,
  deploy, keystone, settings, and synchronization state; `uiStore` owns non-secret shell preferences
  (`frontend/src/stores/controllerStore.ts:1-50`; `frontend/src/stores/controller/types.ts:28-31`;
  `frontend/src/stores/uiStore.ts:1-28`).
- Authentication secrets and transient ceremonies never enter localStorage. The controller-store
  allowlist is exactly: the three endpoints, four non-secret keystone descriptor fields, mode,
  telemetry-stripped node cache, settings cache, and `lastSyncedAt`. It excludes session, CSRF,
  break-glass, TOTP material, login-passkey state, and both pending enrollment candidates
  (`frontend/src/stores/controller/persist.ts:14-45`).
- A login passkey is an account factor only. It is distinct from the tenant-wide keystone credential,
  which authorizes deployment membership; neither credential is silently reused for the other
  (`internal/api/handler_passkey.go:3-14`).
- Only WebAuthn ES256 (-7) and EdDSA (-8) candidates are accepted; unsupported algorithms fail closed
  (`frontend/src/lib/webauthn.ts:51-52,165-179,257-295`).
- New browser credentials are not persisted until their one-use enrollment assertion verifies with
  UV. Later assertions continue to require the exact pinned key, RP ID, challenge, user presence, and
  valid signature, even though UV is no longer a rejection condition
  (`internal/trustlist/webauthn.go:68-95,147-254`).

## Gotchas

- **Enrollment UV does not prove a hardware key.** YAOG deliberately requests `attestation:'none'`.
  The second prompt validates the signed UV result for this first-party enrollment ceremony, but it
  does not establish hardware provenance, non-exportability, or defeat a custom authenticated client
  that supplies a software-controlled candidate. The shared warning states that a non-UV credential
  may be duplicable and that a usable backup/synced copy can log in or sign; backup eligibility is
  independent of UV (`frontend/src/lib/webauthn.ts:241-268,303-334`;
  `frontend/src/i18n/messages/en.ts:687`).
- `frontend/src/lib/webauthn.ts` is shared by login, enrollment, and keystone signing. Login signs the decoded random
  nonce; manifest signing first hashes the decoded canonical manifest. Do not hash login challenges
  or pre-encode either challenge (`frontend/src/lib/webauthn.ts:338-407`).
- `navigator.credentials.create()` can have succeeded even when the following prompt or HTTP request
  fails. Clearing `pendingLoginPasskeyEnrollment` on that failure would re-create orphan credentials;
  it is intentionally cleared only on success, confirmed lost-response recovery, logout, or session
  loss (`frontend/src/stores/controller/auth.ts:52-71,280-321,408-445,579-660`).
- Break-glass has no operator account. Login-purpose enrollment begin is rejected, and account TOTP /
  passkey status remains unknown rather than presenting a false disabled state
  (`internal/api/handler_webauthn_enrollment.go:46-63`;
  `frontend/src/stores/controller/auth.ts:358-427,451-469,557-577`).
- TOTP enrollment deliberately displays a setup key and `otpauth://` URI without adding a QR-rendering
  dependency or sending the secret to a third party (`frontend/src/components/deploy/TwoFactorSettings.tsx`).
