# Panel Auth — Operator Login UI + Client Auth State

<!-- last-verified: 2026-06-14 -->

> **controller-server-authority-redesign (plans 4–6):** the login form is now a
> **full-page gate** in `components/auth/LoginPage.tsx` (brand, password+TOTP,
> passwordless passkey, break-glass Recovery disclosure, connection settings, language
> toggle, "switch to local mode" escape) — NOT a section inside ConnectionSettings.
> `Shell` renders it (after a session-probe splash) whenever
> `mode==='controller' && !loggedIn && operatorToken===''`; see specs/panel-shell.md.
> On a successful login or cookie-restore the store calls `hydrateFromServer()`
> (`GET /topology`→`loadTopology`, overwriting the local canvas; controller mode is
> server-authoritative) with a pre-hydration export stash of a differing non-empty local
> design — fired on EVERY divergent overwrite, not once per browser (the once-per-browser
> design was rejected in review because it silently discards undeployed edits from the
> second login on). ConnectionSettings now holds only the connection endpoints +
> refresh; UserMenu hosts the signed-in identity + sign-out. The line citations below
> predate the move and are approximate.

## Responsibility
Authenticate the panel operator against the controller (password + optional TOTP / login-passkey second factor, passwordless passkey, break-glass token) and hold the resulting session/CSRF state in memory so every operator API call carries the right credentials.

## Files
- `frontend/src/components/deploy/ConnectionSettings.tsx:1-263` — login form on /settings: username/password (122-197), conditional 6-digit TOTP field (160-171), "Sign in with passkey" button + touch-key hint (205-217), break-glass token input (221-233), signed-in identity + sign-out (98-119).
- `frontend/src/components/deploy/TwoFactorSettings.tsx:1-249` — TOTP enrolment card: guard-effect status fetch (33-37), enroll → show setup key + otpauth URI → confirm-with-code (42-78, 168-241), disable-with-current-code (80-92).
- `frontend/src/components/deploy/PasskeySettings.tsx:1-113` — login-passkey card: status fetch (27-31), register (33-40), two-phase remove requiring a fresh assertion (42-49, 79-94).
- `frontend/src/components/pages/SecurityPage.tsx:9-18` — /security route composing TwoFactorSettings + PasskeySettings (plus audit views, see specs/panel-shell.md).
- `frontend/src/stores/controllerStore.ts:52-127,163-188,335-623,779-791` — the auth slice: session/CSRF/2FA/passkey state, `configOf` effective-bearer (163-170), selectors (174-188), `login`/`logout`/`checkSession` (335-485), TOTP actions (489-514), `loginWithPasskey`/passkey management (518-623), persistence allowlist (779-791).
- `frontend/src/api/controllerClient.ts:18-94,232-335,343-540` — auth wire layer: `ControllerConfig` (18-30), `LoginOutcome`/`PasskeyChallenge` (50-86), shared `request()` attaching Bearer + X-CSRF-Token + `credentials:'include'` (240-259), `login()` (283-335), TOTP routes (362-391), passkey routes (417-497), `getSession` (517-533), `logout` (537-540).
- `frontend/src/lib/webauthn.ts:55-99,210-291,347-429` — browser WebAuthn ceremonies: typed `WebAuthnError` (55-70), IP-literal RP-ID guard (82-99), `enrollOperatorCredential` create() ceremony (210-291), `assertLogin` (347-363) delegating to the shared `runAssertion` get() ceremony (370-417).
- `frontend/src/components/shell/UserMenu.tsx:7-9` — top-right account popover; still a placeholder ("contents filled by later phases"), holds no auth state.

## Inputs
- Operator keystrokes: username/password/TOTP code, break-glass token (`ConnectionSettings.tsx:30-32,225-231`).
- Controller operator API responses (see specs/controller-operator-api.md): `POST /login` 401 bodies carrying `totp_required`/`passkey_required` flags (`controllerClient.ts:307-322`), `LoginResponseJSON` session+CSRF (`controllerClient.ts:88-94`), `GET /session` probe (`controllerClient.ts:517-533`), TOTP enroll secrets (`controllerClient.ts:347-373`), passkey challenges (`controllerClient.ts:61-86`).
- panel-shell: `Shell` fires `checkSession()` on mount and on switching to controller mode (`frontend/src/components/shell/Shell.tsx:20-22`) — see specs/panel-shell.md.
- The browser authenticator via `navigator.credentials.create()/get()` (`webauthn.ts:262,396`); httpOnly session + CSRF cookies set by the server, sent automatically by `credentials:'include'`.

## Outputs
- `configOf(state): ControllerConfig` — effective bearer `sessionToken || operatorToken` plus `csrfToken` (`controllerStore.ts:163-170`); consumed by every operator request the panel makes (refresh/deploy/settings — see specs/panel-deploy-fleet.md). `request()` turns it into `Authorization: Bearer` and `X-CSRF-Token` on state-changing methods (`controllerClient.ts:240-259`).
- Selectors gating downstream UI: `selectLoggedIn` (`controllerStore.ts:174-178`) drives the login-form/identity toggle; `selectHasAuth` (`controllerStore.ts:184-188`) enables Deploy/Roll-keys in DeployBar (`frontend/src/components/deploy/DeployBar.tsx:34`).
- `SignedTrustList` login assertions (`assertLogin`, `webauthn.ts:347-363`) posted to `/login` (2FA leg), `/login/passkey/finish`, and `/passkey/disable` re-auth (`controllerClient.ts:288-291,475-497,451-454`).
- `RegisterPasskeyBody` (public key only) to `POST /passkey/register` (`controllerStore.ts:574-592`, `controllerClient.ts:423-436`).
- Auth-derived UI flags: `totpRequired`, `passkeyRegistered`, `totpEnabled`, `loginCeremony` (`controllerStore.ts:83-89,127`).

## Decision points
- **Login outcome branching** (`controllerStore.ts:335-422`): `success` → store session+CSRF, then refresh fleet + 2FA/passkey status; `totp_required` → expand the code field (re-prompt message only if a code was already submitted, 384-398); `passkey_required` → run the assertion ceremony inline and resubmit with the password still in closure (339-383). Any hard failure resets `totpRequired` (412-421).
- **Cookie-session restore** (`controllerStore.ts:463-485`): `checkSession` marks `loggedIn` only when the `GET /session` probe returns a NON-EMPTY `csrf_token` — a break-glass Bearer also answers 200 but mints no cookie/CSRF, and must not count as a login (472-481).
- **Bearer precedence**: in-memory session token wins over break-glass token (`controllerStore.ts:163-170`); after a refresh both are empty and the httpOnly cookie authenticates alone (`controllerClient.ts:21-25`).
- **Passkey disable is two-phase**: begin returns `done` (idempotent, nothing registered) or a challenge requiring a fresh assertion (`controllerStore.ts:597-623`, `controllerClient.ts:441-448`).
- **Ceremony-flag routing**: login/register/remove passkey set `loginCeremony`; keystone deploy-signing uses separate `signing`/`enrolling` flags so login ceremonies never light DeployBar's "authorize this deploy" banner (`controllerStore.ts:117-127`).

## Invariants
- Credentials never touch localStorage: `sessionToken`, `csrfToken`, `operatorToken`, and TOTP secrets are excluded from the persist `partialize` allowlist (`controllerStore.ts:779-791`); only endpoints and the non-secret pinned-credential identifiers persist.
- TOTP and the login passkey are LOGIN factors only, never a signing mechanism (`TwoFactorSettings.tsx:13`, `docs/spec/controller/operator-auth.md`); the network trust anchor is the keystone signing credential — see specs/keystone-trustlist.md and `docs/spec/controller/signing.md`.
- Only COSE ES256 (-7) and EdDSA (-8) credentials are accepted, fail-closed (`webauthn.ts:156-168`); private keys never leave the authenticator — only SPKI public keys go over the wire (`webauthn.ts:280-290`). Cross-ref PRINCIPLES.md "Key custody".

## Gotchas
- `webauthn.ts` is SHARED with keystone deploy signing: `assertLogin` signs the raw base64url-decoded server nonce, while `signManifest` signs SHA-256 of manifest bytes — do not hash the login challenge (`webauthn.ts:353-356,327`). The login assertion's `public_key` field is intentionally empty (audit-only; the server verifies against its pinned key, `webauthn.ts:344-346,408-410`).
- `registerPasskey` reuses `enrollOperatorCredential` (the keystone create() ceremony) but produces a DIFFERENT credential for a different purpose; the two are not interchangeable (`controllerStore.ts:570-592`, `PasskeySettings.tsx:6-9`).
- TOTP enrolment deliberately ships no QR code: the secret is shown as a grouped base32 setup key + otpauth URI because rendering QR would add a dependency or leak the secret to a third-party service (`TwoFactorSettings.tsx:10-13`).
- Break-glass paths return 403 from `/totp/status` and `/passkey/status` (no account); the store maps that to `null` ("unknown") and the cards show "sign in with your password to manage" instead of an error (`controllerStore.ts:489-495,562-568`).
