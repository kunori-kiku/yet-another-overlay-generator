// Auth slice: connection config + operator identity (password login, TOTP / passkey second
// factors, session probe/restore, logout) and the login-passkey self-service management. Moved
// verbatim from the single controllerStore.ts create() literal.

import type { ControllerSet, ControllerGet } from './types';
import type { ControllerConfig } from '../../api/controllerClient';
import { configOf, localizeError, tLocal, clearServerCanvasAtGate, serverKeystoneReset } from './helpers';
import {
  login as ctlLogin,
  logout as ctlLogout,
  getSession,
  getTOTPStatus,
  enrollTOTP as ctlEnrollTOTP,
  confirmTOTP as ctlConfirmTOTP,
  disableTOTP as ctlDisableTOTP,
  getPasskeyStatus,
  registerPasskey as ctlRegisterPasskey,
  disablePasskeyBegin,
  disablePasskeyFinish,
  passkeyLoginBegin,
  passkeyLoginFinish,
} from '../../api/controllerClient';
import { enrollOperatorCredential, assertLogin } from '../../lib/webauthn';
import { useTopologyStore } from '../topologyStore';
import { useUiStore } from '../uiStore';

export function createAuthSlice(set: ControllerSet, get: ControllerGet) {
  return {
    // Default connection config (see DESIGN: operator defaults to :8080, agent to :9090).
    baseURL: 'http://localhost:8080',
    pathPrefix: '',
    agentBaseURL: 'http://localhost:9090',
    operatorToken: '',

    sessionToken: '',
    operatorName: null,
    sessionExpiresAt: null,
    csrfToken: '',
    loggedIn: false,
    controllerVersion: '',

    totpRequired: false,
    totpEnabled: null,
    passkeyRegistered: null,

    loginCeremony: false,

    setConfig: (partial: Partial<ControllerConfig & { agentBaseURL: string }>) => set(partial),

    // Operator password login (plan-5.2): POST /login to obtain a session token, held in memory
    // only. On success, immediately refresh the fleet view. The session takes precedence over a
    // break-glass token (see configOf). On failure, echo the controller's raw error (401
    // invalid username or password / 429 too many attempts).
    login: async (username: string, password: string, totp?: string) => {
      // Idempotency guard (plan-16 / 3.4): drop a re-entrant submit while a login is in flight
      // (the submit button disables on `loading`, but a synthetic re-click bubbles past it). A
      // duplicate login POST would otherwise burn a second rate-limit attempt.
      if (get().loading) return;
      set({ loading: true, error: null });
      try {
        const outcome = await ctlLogin(configOf(get()), username, password, totp);
        if (outcome.kind === 'passkey_required') {
          // Password correct but a passkey is required: pop the authenticator in place and
          // resubmit with the assertion (the password is still in the closure). The signing
          // flag drives the "touch your security key" prompt. The whole 2FA passkey step is
          // transparent to the UI — the login form needs no passkey input, the store completes
          // the ceremony automatically.
          const ch = outcome.challenge;
          if (!ch.credentialId || !ch.alg) {
            set({ error: tLocal('controllerStore.passkeyRequiredNoneRegistered'), loading: false });
            return;
          }
          set({ loginCeremony: true });
          try {
            const assertion = await assertLogin(
              ch.challenge,
              ch.credentialId,
              ch.alg,
              ch.rpid || window.location.hostname,
            );
            const after = await ctlLogin(configOf(get()), username, password, undefined, assertion);
            if (after.kind === 'success') {
              set({
                sessionToken: after.result.sessionToken,
                csrfToken: after.result.csrfToken,
                loggedIn: true,
                operatorName: after.result.operator,
                sessionExpiresAt: after.result.expiresAt,
                controllerVersion: after.result.controllerVersion,
                totpRequired: false,
                loginCeremony: false,
                loading: false,
              });
              await get().hydrateFromServer();
              await get().refresh();
              await get().loadTOTPStatus();
              await get().loadPasskeyStatus();
              return;
            }
            // A passkey resubmit should either succeed or throw; anything else is unexpected.
            set({ error: tLocal('controllerStore.passkeyDidNotComplete'), loginCeremony: false, loading: false });
          } catch (err) {
            set({
              error: localizeError(err, 'error.generic'),
              loginCeremony: false,
              loading: false,
            });
          }
          return;
        }
        if (outcome.kind === 'totp_required') {
          // Password correct but a second-factor code is required: let the login form collect a
          // TOTP code and retry. The backend returns the same totp_required for both "missing
          // code" and "wrong code" (no oracle); but locally we know whether a code was
          // submitted — if a code was sent and is still required, it was wrong/expired, so show
          // a gentle hint (the user is already in the 2FA step, so this is not info disclosure).
          // The first time (no code) writes no error, just expands the code box.
          const submittedCode = !!(totp && totp.trim() !== '');
          set({
            totpRequired: true,
            error: submittedCode
              ? tLocal('controllerStore.totpNotAccepted')
              : null,
            loading: false,
          });
          return;
        }
        set({
          sessionToken: outcome.result.sessionToken,
          csrfToken: outcome.result.csrfToken,
          loggedIn: true,
          operatorName: outcome.result.operator,
          sessionExpiresAt: outcome.result.expiresAt,
          controllerVersion: outcome.result.controllerVersion,
          totpRequired: false,
          loading: false,
        });
        await get().hydrateFromServer();
        await get().refresh();
        // Fetch this account's 2FA / passkey status (for the "account security" area to echo).
        // A failure does not block login.
        await get().loadTOTPStatus();
        await get().loadPasskeyStatus();
      } catch (err) {
        // Hard failure (wrong password / 429 lockout / network / 500, all thrown before
        // reaching "second-factor required"): reset totpRequired, back to a pure password form
        // — avoiding the mismatched prompt of "wrong username or password" while still showing
        // the code box. The next attempt that genuinely needs a second factor (correct
        // password) will cleanly re-trigger totp_required.
        set({
          error: localizeError(err, 'error.generic'),
          totpRequired: false,
          loading: false,
        });
      }
    },

    // Reset the second-factor step (see the interface comment): clear totpRequired only; the
    // code input's local value is cleared by the component.
    resetTOTPChallenge: () => set({ totpRequired: false }),

    // Logout: best-effort POST /logout to revoke the server session, then clear the local
    // session + fleet view regardless of success (local logout must take effect even if the
    // network/server revocation fails).
    logout: async () => {
      try {
        // Whether there is an in-memory session or a cookie session (loggedIn), call the server
        // revocation + clear the cookie.
        if (get().sessionToken || get().loggedIn) {
          await ctlLogout(configOf(get()));
        }
      } catch {
        // A revocation failure does not block local logout (the session still expires on the
        // server by its TTL).
      }
      // Capture the sync snapshot before clearing: the set() below sets lastSyncedSnapshot to
      // null, and set is synchronous, so a later get().lastSyncedSnapshot would read null —
      // then the gate's dirty check would treat any non-empty server canvas as dirty, so each
      // logout would wrongly trigger a backup download (plan-10 review). Save the baseline first.
      const snap = get().lastSyncedSnapshot;
      set({
        sessionToken: '',
        csrfToken: '',
        loggedIn: false,
        operatorName: null,
        sessionExpiresAt: null,
        controllerVersion: '',
        // Clear the 2FA session state: reset totpRequired, return totpEnabled to "unknown", so
        // the next operator who logs in with a password re-fetches their own account's status
        // (TwoFactorSettings's guarded effect re-fires when it is null).
        totpRequired: false,
        totpEnabled: null,
        passkeyRegistered: null,
        nodes: [],
        audit: [],
        auditVerified: false,
        // Drop any open preview dialog / in-flight preview / preview-error banner: it is a
        // transient, session-scoped operator action and must never survive a session change.
        deployPreview: null,
        deployPreviewing: false,
        deployPreviewError: null,
        // Clear settings too, so a different operator signing in re-fetches them
        // (the guarded loadSettings effect re-fires on settings===null).
        settings: null,
        error: null,
        // The sync snapshot and conflict flag are derived from the current session/server
        // design: clear them on logout too; the next login's hydrate rebuilds them.
        lastSyncedSnapshot: null,
        lastSyncedTopology: null,
        saveConflict: false,
        // Server-authoritative keystone status is session-derived: reset to "unknown" on logout
        // so the next operator re-probes (never inherits a stale "enrolled" / redeploy banner).
        ...serverKeystoneReset,
      });
      // Security: if the post-logout canvas is a server secret mirror, wipe it immediately
      // (memory + persist also clears localStorage). Otherwise, while logged out, anyone could
      // read the fleet's public IPs and SSH targets out of the canvas/localStorage. Local
      // original work (canvasFromServer=false) is untouched — that is the user's own data.
      // Reuse clearServerCanvasAtGate so the three flush points (logout / session loss /
      // partialize) use the same predicate rather than each expanding it. Pass the snap captured
      // before logout (not the already-nulled get().lastSyncedSnapshot) so the dirty check is
      // accurate.
      clearServerCanvasAtGate(get().mode, snap);
      // A3: the session ended, so the appearance returns to the local preference — the
      // server-pushed fleet translucency should not linger at the logout/login gate.
      useUiStore.getState().restoreLocalTranslucency();
    },

    // Restore the logged-in state after a refresh (P5): GET /session probes the current session
    // via the httpOnly cookie. On a hit set loggedIn + identity + expiry + csrfToken
    // (subsequent state-mutating requests carry X-CSRF-Token off it); on a miss (401/403) clear
    // the logged-in state. A probe failure (network/not configured) also clears loggedIn.
    // Restores only the logged-in state — does not proactively fetch the fleet (the persisted
    // cache colors it instantly; the user presses "connect / refresh" for live state).
    checkSession: async () => {
      try {
        const info = await getSession(configOf(get()));
        // Authed (a cookie session OR a break-glass token both answer 200): refresh the
        // server-authoritative keystone status so the panel never renders a premature/false
        // "Not enrolled" on mount. Best-effort; null info (401/403) leaves it unprobed.
        if (info) {
          // controllerVersion is server truth on every authed probe (genuine cookie session OR
          // break-glass Bearer), so capture it here rather than in the login-only branch below.
          // It is NOT cleared in the break-glass branch (empty csrf) below — break-glass is authed,
          // just "not a login" — only on a genuine session loss (the `!info` else here / the catch).
          set({ controllerVersion: info.controllerVersion });
          await get().hydrateKeystoneStatus();
        } else {
          set({ controllerVersion: '' });
        }
        // Only a GENUINE cookie session counts as "logged in". GET /session also answers
        // 200 for a break-glass Bearer token (it authenticates operator routes), but
        // break-glass mints no session/CSRF cookie, so its probe returns an EMPTY
        // csrf_token. Gate on a non-empty csrf_token to keep break-glass a recovery path
        // (selectHasAuth still enables Deploy via operatorToken), preserving the
        // "break-glass is not a login" invariant.
        if (info && info.csrfToken !== '') {
          const wasLoggedIn = get().loggedIn;
          set({
            loggedIn: true,
            operatorName: info.operator,
            sessionExpiresAt: info.expiresAt || null,
            csrfToken: info.csrfToken,
          });
          // Server-authoritative hydration (D1): session restore overwrites the local canvas.
          // Two triggers:
          //   (1) the logged-in state goes false→true (mount / refresh restore) — a first entry
          //       always fetches;
          //   (2) logged in but the canvas is not a server mirror (!canvasFromServer) — this is
          //       exactly the "while logged in, local→controller and back" scenario (plan-10 /
          //       A2): the Shell's mode-flip effect calls checkSession again, at which point
          //       wasLoggedIn is still true, so the old logic would not re-fetch and stale local
          //       state would masquerade as the server design. With this condition added,
          //       re-entering controller always re-fetches from the server authority.
          // In steady state (logged in + canvas already a server mirror) both conditions are
          // false, so no needless repeat overwrite.
          if (!wasLoggedIn || !useTopologyStore.getState().canvasFromServer) {
            await get().hydrateFromServer();
          }
        } else {
          // Session lost: capture the baseline before clearing (same ordering fix as logout) so
          // the gate uses the live baseline to judge dirty accurately.
          // Also flush the server-authoritative keystone status (lockstep with logout) so a stale
          // enrolled/redeploy status can't render before the next probe.
          // NOTE: controllerVersion is NOT cleared here — this else also runs for a break-glass
          // token (info present, empty csrf), which is authed and should keep the version. A
          // genuine logout (info null) already cleared it in the `!info` branch above.
          const lostSnap = get().lastSyncedSnapshot;
          set({ loggedIn: false, csrfToken: '', lastSyncedSnapshot: null, lastSyncedTopology: null, saveConflict: false, ...serverKeystoneReset });
          clearServerCanvasAtGate(get().mode, lostSnap);
          useUiStore.getState().restoreLocalTranslucency(); // A3: back at the login gate, use the local appearance preference
        }
      } catch {
        const lostSnap = get().lastSyncedSnapshot;
        set({ loggedIn: false, controllerVersion: '', lastSyncedSnapshot: null, lastSyncedTopology: null, saveConflict: false, ...serverKeystoneReset });
        clearServerCanvasAtGate(get().mode, lostSnap);
        useUiStore.getState().restoreLocalTranslucency();
      }
    },

    // Fetch this account's TOTP status. On a 403 (a break-glass token has no account) or a
    // network error, keep totpEnabled=null (the UI prompts "log in with a password to manage
    // 2FA" off it) without polluting the global error.
    loadTOTPStatus: async () => {
      try {
        set({ totpEnabled: await getTOTPStatus(configOf(get())) });
      } catch {
        set({ totpEnabled: null });
      }
    },

    // Begin enroll: mint a not-yet-activated secret + otpauth URI and return it for the
    // component to display (not persisted before confirmation, and the global state is
    // unchanged). Errors are thrown to the caller, displayed in place by TwoFactorSettings.
    enrollTOTP: async () => {
      return ctlEnrollTOTP(configOf(get()));
    },

    // Confirm enroll: activate 2FA with the secret from enroll + a current code. On success
    // totpEnabled=true. On failure (e.g. wrong code) the error is thrown to the caller and
    // displayed in place by the component.
    confirmTOTP: async (secret: string, code: string) => {
      await ctlConfirmTOTP(configOf(get()), secret, code);
      set({ totpEnabled: true });
    },

    // Disable 2FA: requires the current code (to stop a hijacked session from removing the
    // second factor outright). On success totpEnabled=false.
    disableTOTP: async (code: string) => {
      await ctlDisableTOTP(configOf(get()), code);
      set({ totpEnabled: false });
    },

    // Passwordless passkey login: begin gets the challenge → assertLogin pops the authenticator
    // → finish exchanges it for a session. On failure (no passkey / assertion failed /
    // cancelled) it is displayed in place. On success, refresh the view + fetch the
    // account-security status.
    loginWithPasskey: async (username: string) => {
      set({ loading: true, error: null });
      try {
        const cfg = configOf(get());
        const ch = await passkeyLoginBegin(cfg, username);
        if (!ch.credentialId || !ch.alg) {
          // Empty allow_credentials = this username has no registered passkey (the backend
          // returns a decoy).
          set({ error: tLocal('controllerStore.noPasskeyRegistered'), loading: false });
          return;
        }
        set({ loginCeremony: true });
        let assertion;
        try {
          assertion = await assertLogin(
            ch.challenge,
            ch.credentialId,
            ch.alg,
            ch.rpid || window.location.hostname,
          );
        } finally {
          set({ loginCeremony: false });
        }
        const result = await passkeyLoginFinish(cfg, username, assertion);
        set({
          sessionToken: result.sessionToken,
          csrfToken: result.csrfToken,
          loggedIn: true,
          operatorName: result.operator,
          sessionExpiresAt: result.expiresAt,
          controllerVersion: result.controllerVersion,
          loading: false,
        });
        await get().hydrateFromServer();
        await get().refresh();
        await get().loadTOTPStatus();
        await get().loadPasskeyStatus();
      } catch (err) {
        set({
          error: localizeError(err, 'error.generic'),
          loading: false,
          loginCeremony: false,
        });
      }
    },

    // Fetch this account's login-passkey status. On a 403 (a break-glass token has no account)
    // or any error, keep it null.
    loadPasskeyStatus: async () => {
      try {
        set({ passkeyRegistered: await getPasskeyStatus(configOf(get())) });
      } catch {
        set({ passkeyRegistered: null });
      }
    },

    // Register a login passkey: reuse the keystone's create() ceremony
    // (enrollOperatorCredential gets the SPKI + alg), then POST /passkey/register to store the
    // public key. Only the public key leaves the authenticator. loginCeremony drives the "touch
    // your security key" prompt (does not trigger DeployBar's deploy banner). Errors are thrown
    // to the caller, displayed in place by PasskeySettings (consistent with TwoFactorSettings's
    // local errors).
    registerPasskey: async () => {
      const rpId = window.location.hostname;
      const origin = window.location.origin;
      set({ loginCeremony: true });
      try {
        const cred = await enrollOperatorCredential(rpId, origin);
        await ctlRegisterPasskey(configOf(get()), {
          alg: cred.alg,
          credentialId: cred.credentialId,
          publicKeyPEM: cred.publicKeyPEM,
          rpId,
          origin,
        });
        set({ passkeyRegistered: true, loginCeremony: false });
      } catch (err) {
        set({ loginCeremony: false });
        throw err;
      }
    },

    // Disable the login passkey (two-stage): begin gets a re-authentication challenge →
    // assertLogin → finish deletes the credential. A fresh assertion is required to stop a
    // hijacked session from removing the factor outright. begin returning done means there was
    // no passkey to begin with (idempotent). Errors are thrown to the caller, displayed in place
    // by PasskeySettings.
    disablePasskey: async () => {
      set({ loginCeremony: true });
      try {
        const cfg = configOf(get());
        const begin = await disablePasskeyBegin(cfg);
        if (begin.kind === 'done') {
          set({ passkeyRegistered: false, loginCeremony: false });
          return;
        }
        const ch = begin.challenge;
        if (!ch.credentialId || !ch.alg) {
          set({ loginCeremony: false });
          throw new Error(tLocal('controllerStore.cannotDisableNoCredential'));
        }
        const assertion = await assertLogin(
          ch.challenge,
          ch.credentialId,
          ch.alg,
          ch.rpid || window.location.hostname,
        );
        await disablePasskeyFinish(cfg, assertion);
        set({ passkeyRegistered: false, loginCeremony: false });
      } catch (err) {
        set({ loginCeremony: false });
        throw err;
      }
    },
  };
}
