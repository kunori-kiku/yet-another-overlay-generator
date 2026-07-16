// Auth slice: connection config + operator identity (password login, TOTP / passkey second
// factors, session probe/restore, logout) and the login-passkey self-service management. Moved
// verbatim from the single controllerStore.ts create() literal.

import type { ControllerSet, ControllerGet } from './types';
import type { ControllerConfig, LoginPasskeyStatus } from '../../api/controllerClient';
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
  beginWebAuthnEnrollment,
  controllerErrorCode,
} from '../../api/controllerClient';
import {
  assertLogin,
  createWebAuthnCredentialCandidate,
  proveWebAuthnCredentialEnrollment,
  type WebAuthnCredentialCandidate,
} from '../../lib/webauthn';
import { useTopologyStore } from '../topologyStore';
import { useUiStore } from '../uiStore';

function authGenerationIsCurrent(get: ControllerGet, generation: number): boolean {
  return get().authGeneration === generation;
}

// Every successful authentication path establishes the same server-derived state. Keep the
// ordered hydration and generation guards centralized so password, passkey-second-factor, and
// passwordless login cannot drift as another account-scoped probe is added.
async function hydrateAuthenticatedContext(get: ControllerGet, generation: number): Promise<void> {
  if (!authGenerationIsCurrent(get, generation)) return;
  await get().hydrateFromServer();
  if (!authGenerationIsCurrent(get, generation)) return;
  await get().refresh();
  if (!authGenerationIsCurrent(get, generation)) return;
  await get().loadTOTPStatus();
  if (!authGenerationIsCurrent(get, generation)) return;
  await get().loadPasskeyStatus();
}

// Account-scoped and ceremony-scoped state must never cross an authentication-context
// boundary. Keeping this reset shape in one place prevents logout, session loss, identity
// replacement, and controller-target changes from drifting apart as new flags are added.
const authContextReset = {
  totpRequired: false,
  totpEnabled: null,
  passkeyRegistered: null,
  pendingLoginPasskeyEnrollment: null,
  pendingKeystoneEnrollment: null,
  loginCeremony: false,
  enrolling: false,
  signing: false,
  loading: false,
  saving: false,
  previewing: false,
  deployPreview: null,
  deployPreviewing: false,
  deployPreviewError: null,
  pendingShrink: null,
  lastFleetSyncedAt: null,
} as const;

function loginPasskeyStatusMatchesCandidate(
  status: LoginPasskeyStatus,
  candidate: WebAuthnCredentialCandidate,
): boolean {
  return status.registered
    && status.alg === candidate.alg
    && status.credentialId === candidate.credentialId
    && status.publicKeyPEM === candidate.publicKeyPEM
    && status.rpId === candidate.rpId
    && status.origin === candidate.origin;
}

export function createAuthSlice(set: ControllerSet, get: ControllerGet) {
  // Auth generation separates controller/account identities. These per-resource sequences order
  // account-status probes inside one identity and are invalidated by successful mutations, so an
  // older response cannot re-expose an enroll/replace action after newer server truth won.
  let totpStatusRequestSequence = 0;
  let passkeyStatusRequestSequence = 0;

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
    pendingLoginPasskeyEnrollment: null,

    authGeneration: 0,

    loginCeremony: false,

    setConfig: (partial: Partial<ControllerConfig & { agentBaseURL: string }>) => {
      const state = get();
      const endpointChanged =
        ('baseURL' in partial && partial.baseURL !== state.baseURL)
        || ('pathPrefix' in partial && partial.pathPrefix !== state.pathPrefix);
      const authTargetChanged = endpointChanged
        || ('operatorToken' in partial && partial.operatorToken !== state.operatorToken);
      if (!authTargetChanged) {
        set(partial);
        return;
      }
      set({
        ...partial,
        ...authContextReset,
        authGeneration: state.authGeneration + 1,
        // A browser session token/CSRF pair is scoped to its controller endpoint. Never send an
        // old endpoint's bearer to a newly typed host/path; the cookie probe establishes the new
        // target's identity. A simultaneously supplied replacement break-glass token is kept.
        ...(endpointChanged ? {
          sessionToken: '',
          csrfToken: '',
          loggedIn: false,
          operatorName: null,
          sessionExpiresAt: null,
          controllerVersion: '',
          operatorToken: partial.operatorToken ?? '',
          operatorCredentialId: null,
          operatorCredentialAlg: null,
          operatorRpId: null,
          operatorPublicKeyPEM: null,
          nodes: [],
          audit: [],
          auditVerified: false,
          settings: null,
          lastDeploy: null,
          lastSyncedAt: null,
          lastSyncedSnapshot: null,
          lastSyncedTopology: null,
          saveConflict: false,
          saving: false,
          pendingShrink: null,
          deployPreview: null,
          deployPreviewing: false,
          deployPreviewError: null,
          ...serverKeystoneReset,
        } : {}),
      });
      if (endpointChanged) {
        clearServerCanvasAtGate(state.mode, state.lastSyncedSnapshot);
        useUiStore.getState().restoreLocalTranslucency();
      }
    },

    // Operator password login (plan-5.2): POST /login to obtain a session token, held in memory
    // only. On success, immediately refresh the fleet view. The session takes precedence over a
    // break-glass token (see configOf). On failure, echo the controller's raw error (401
    // invalid username or password / 429 too many attempts).
    login: async (username: string, password: string, totp?: string) => {
      // Idempotency guard (plan-16 / 3.4): drop a re-entrant submit while a login is in flight
      // (the submit button disables on `loading`, but a synthetic re-click bubbles past it). A
      // duplicate login POST would otherwise burn a second rate-limit attempt.
      if (get().loading) return;
      const authGeneration = get().authGeneration;
      const cfg = configOf(get());
      set({ loading: true, error: null });
      try {
        const outcome = await ctlLogin(cfg, username, password, totp);
        if (!authGenerationIsCurrent(get, authGeneration)) return;
        if (outcome.kind === 'passkey_required') {
          // Password correct but a passkey is required: pop the authenticator in place and
          // resubmit with the assertion (the password is still in the closure). loginCeremony
          // drives the "touch your security key" prompt. The whole 2FA passkey step is
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
            if (!authGenerationIsCurrent(get, authGeneration)) return;
            const after = await ctlLogin(cfg, username, password, undefined, assertion);
            if (!authGenerationIsCurrent(get, authGeneration)) return;
            if (after.kind === 'success') {
              set({
                ...authContextReset,
                authGeneration: authGeneration + 1,
                sessionToken: after.result.sessionToken,
                csrfToken: after.result.csrfToken,
                loggedIn: true,
                operatorName: after.result.operator,
                sessionExpiresAt: after.result.expiresAt,
                controllerVersion: after.result.controllerVersion,
              });
              const establishedGeneration = get().authGeneration;
              await hydrateAuthenticatedContext(get, establishedGeneration);
              return;
            }
            // A passkey resubmit should either succeed or throw; anything else is unexpected.
            set({ error: tLocal('controllerStore.passkeyDidNotComplete'), loginCeremony: false, loading: false });
          } catch (err) {
            if (!authGenerationIsCurrent(get, authGeneration)) return;
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
          ...authContextReset,
          authGeneration: authGeneration + 1,
          sessionToken: outcome.result.sessionToken,
          csrfToken: outcome.result.csrfToken,
          loggedIn: true,
          operatorName: outcome.result.operator,
          sessionExpiresAt: outcome.result.expiresAt,
          controllerVersion: outcome.result.controllerVersion,
        });
        const establishedGeneration = get().authGeneration;
        await hydrateAuthenticatedContext(get, establishedGeneration);
      } catch (err) {
        if (!authGenerationIsCurrent(get, authGeneration)) return;
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

    // Logout clears the local session/fleet synchronously, then performs a best-effort server
    // revocation. The network continuation owns no local state: a hung old-controller request
    // cannot keep secrets visible or later erase a session established against a new endpoint.
    logout: async () => {
      const prior = get();
      const cfg = configOf(prior);
      const hadSession = !!(prior.sessionToken || prior.loggedIn);
      // Capture the sync snapshot before clearing: the reset sets lastSyncedSnapshot to null, and
      // the gate needs the live baseline to avoid a spurious backup of an unchanged server canvas.
      const snap = prior.lastSyncedSnapshot;
      // Invalidate ceremonies and clear every session-derived view before the first await. Local
      // logout is therefore immediate even when the revocation request hangs or the server is down.
      set((state) => ({
        ...authContextReset,
        authGeneration: state.authGeneration + 1,
        sessionToken: '',
        csrfToken: '',
        loggedIn: false,
        operatorName: null,
        sessionExpiresAt: null,
        controllerVersion: '',
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
      }));
      // Security: if the post-logout canvas is a server secret mirror, wipe it immediately
      // (memory + persist also clears localStorage). Otherwise, while logged out, anyone could
      // read the fleet's public IPs and SSH targets out of the canvas/localStorage. Local
      // original work (canvasFromServer=false) is untouched — that is the user's own data.
      // Reuse clearServerCanvasAtGate so the three flush points (logout / session loss /
      // partialize) use the same predicate rather than each expanding it. Pass the snap captured
      // before logout (not the already-nulled get().lastSyncedSnapshot) so the dirty check is
      // accurate.
      clearServerCanvasAtGate(prior.mode, snap);
      // A3: the session ended, so the appearance returns to the local preference — the
      // server-pushed fleet translucency should not linger at the logout/login gate.
      useUiStore.getState().restoreLocalTranslucency();
      try {
        // Whether there was an in-memory bearer or only a cookie session, ask the original
        // endpoint to revoke it and clear its cookie. This continuation deliberately performs no
        // set(): a later auth context is outside its authority.
        if (hadSession) {
          await ctlLogout(cfg);
        }
      } catch {
        // A revocation failure does not roll back local logout; the server session expires by TTL.
      }
    },

    // Restore the logged-in state after a refresh (P5): GET /session probes the current session
    // via the httpOnly cookie. On a hit set loggedIn + identity + expiry + csrfToken
    // (subsequent state-mutating requests carry X-CSRF-Token off it); on a miss (401/403) clear
    // the logged-in state. A probe failure (network/not configured) also clears loggedIn.
    // Restores only the logged-in state — does not proactively fetch the fleet (the persisted
    // cache colors it instantly; the user presses "connect / refresh" for live state).
    checkSession: async () => {
      const authGeneration = get().authGeneration;
      const cfg = configOf(get());
      try {
        const info = await getSession(cfg);
        if (!authGenerationIsCurrent(get, authGeneration)) return;
        // Only a GENUINE cookie session counts as "logged in". GET /session also answers
        // 200 for a break-glass Bearer token (it authenticates operator routes), but
        // break-glass mints no session/CSRF cookie, so its probe returns an EMPTY
        // csrf_token. Gate on a non-empty csrf_token to keep break-glass a recovery path
        // (selectHasAuth still enables Deploy via operatorToken), preserving the
        // "break-glass is not a login" invariant.
        if (info && info.csrfToken !== '') {
          const wasLoggedIn = get().loggedIn;
          const identityChanged = !wasLoggedIn || get().operatorName !== info.operator;
          set({
            ...(identityChanged ? {
              ...authContextReset,
              authGeneration: authGeneration + 1,
              // If a cookie establishes a different identity, discard any in-memory bearer
              // belonging to the prior one and continue on the cookie + fresh CSRF token.
              sessionToken: '',
              ...serverKeystoneReset,
            } : {}),
            loggedIn: true,
            operatorName: info.operator,
            sessionExpiresAt: info.expiresAt || null,
            csrfToken: info.csrfToken,
            controllerVersion: info.controllerVersion,
          });
          const activeGeneration = get().authGeneration;
          // Status belongs to the now-established identity. The action has its own generation
          // check, so a concurrent logout cannot repopulate stale credential state.
          await get().hydrateKeystoneStatus();
          if (!authGenerationIsCurrent(get, activeGeneration)) return;
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
          const lostSnap = get().lastSyncedSnapshot;
          set((state) => ({
            ...authContextReset,
            authGeneration: state.authGeneration + 1,
            sessionToken: '',
            loggedIn: false,
            operatorName: null,
            sessionExpiresAt: null,
            csrfToken: '',
            controllerVersion: info?.controllerVersion ?? '',
            lastSyncedSnapshot: null,
            lastSyncedTopology: null,
            saveConflict: false,
            ...serverKeystoneReset,
          }));
          clearServerCanvasAtGate(get().mode, lostSnap);
          useUiStore.getState().restoreLocalTranslucency(); // A3: back at the login gate, use the local appearance preference
          // A configured break-glass bearer is authenticated for keystone recovery even though it
          // is deliberately not a login session. Re-probe only after resetting the old account's
          // status so this recovery context gets its own server truth.
          if (info) await get().hydrateKeystoneStatus();
        }
      } catch {
        if (!authGenerationIsCurrent(get, authGeneration)) return;
        const lostSnap = get().lastSyncedSnapshot;
        set((state) => ({
          ...authContextReset,
          authGeneration: state.authGeneration + 1,
          sessionToken: '',
          csrfToken: '',
          loggedIn: false,
          operatorName: null,
          sessionExpiresAt: null,
          controllerVersion: '',
          lastSyncedSnapshot: null,
          lastSyncedTopology: null,
          saveConflict: false,
          ...serverKeystoneReset,
        }));
        clearServerCanvasAtGate(get().mode, lostSnap);
        useUiStore.getState().restoreLocalTranslucency();
      }
    },

    // Fetch this account's TOTP status. On a 403 (a break-glass token has no account) or a
    // network error, keep totpEnabled=null (the UI prompts "log in with a password to manage
    // 2FA" off it) without polluting the global error.
    loadTOTPStatus: async () => {
      const authGeneration = get().authGeneration;
      const requestSequence = ++totpStatusRequestSequence;
      const cfg = configOf(get());
      try {
        const enabled = await getTOTPStatus(cfg);
        if (
          authGenerationIsCurrent(get, authGeneration)
          && requestSequence === totpStatusRequestSequence
        ) set({ totpEnabled: enabled });
      } catch {
        if (
          authGenerationIsCurrent(get, authGeneration)
          && requestSequence === totpStatusRequestSequence
        ) set({ totpEnabled: null });
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
      const authGeneration = get().authGeneration;
      await ctlConfirmTOTP(configOf(get()), secret, code);
      if (authGenerationIsCurrent(get, authGeneration)) {
        totpStatusRequestSequence += 1;
        set({ totpEnabled: true });
      }
    },

    // Disable 2FA: requires the current code (to stop a hijacked session from removing the
    // second factor outright). On success totpEnabled=false.
    disableTOTP: async (code: string) => {
      const authGeneration = get().authGeneration;
      await ctlDisableTOTP(configOf(get()), code);
      if (authGenerationIsCurrent(get, authGeneration)) {
        totpStatusRequestSequence += 1;
        set({ totpEnabled: false });
      }
    },

    // Passwordless passkey login: begin gets the challenge → assertLogin pops the authenticator
    // → finish exchanges it for a session. On failure (no passkey / assertion failed /
    // cancelled) it is displayed in place. On success, refresh the view + fetch the
    // account-security status.
    loginWithPasskey: async (username: string) => {
      if (get().loading || get().loginCeremony) return;
      const authGeneration = get().authGeneration;
      const cfg = configOf(get());
      set({ loading: true, error: null });
      try {
        const ch = await passkeyLoginBegin(cfg, username);
        if (!authGenerationIsCurrent(get, authGeneration)) return;
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
          if (authGenerationIsCurrent(get, authGeneration)) set({ loginCeremony: false });
        }
        if (!authGenerationIsCurrent(get, authGeneration)) return;
        const result = await passkeyLoginFinish(cfg, username, assertion);
        if (!authGenerationIsCurrent(get, authGeneration)) return;
        set({
          ...authContextReset,
          authGeneration: authGeneration + 1,
          sessionToken: result.sessionToken,
          csrfToken: result.csrfToken,
          loggedIn: true,
          operatorName: result.operator,
          sessionExpiresAt: result.expiresAt,
          controllerVersion: result.controllerVersion,
        });
        const establishedGeneration = get().authGeneration;
        await hydrateAuthenticatedContext(get, establishedGeneration);
      } catch (err) {
        if (!authGenerationIsCurrent(get, authGeneration)) return;
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
      const authGeneration = get().authGeneration;
      const requestSequence = ++passkeyStatusRequestSequence;
      const cfg = configOf(get());
      try {
        const status = await getPasskeyStatus(cfg);
        if (
          authGenerationIsCurrent(get, authGeneration)
          && requestSequence === passkeyStatusRequestSequence
        ) {
          set({ passkeyRegistered: status.registered });
        }
      } catch {
        if (
          authGenerationIsCurrent(get, authGeneration)
          && requestSequence === passkeyStatusRequestSequence
        ) set({ passkeyRegistered: null });
      }
    },

    // Register a login passkey: begin a server challenge, create the candidate when there is no
    // pending one, then ask that exact candidate for the UV-bearing enrollment assertion before
    // POST /passkey/register. If proof/persistence fails, keep the public descriptor in volatile
    // state so the next click retries it rather than creating an orphan duplicate. loginCeremony
    // drives the prompt without triggering DeployBar's deploy banner.
    registerPasskey: async () => {
      // The button is disabled while a ceremony is active, but guard the action too: a synthetic
      // re-entry must not mint two authenticator credentials before React can re-render.
      if (get().loading || get().loginCeremony || get().passkeyRegistered === true) return;
      const authGeneration = get().authGeneration;
      const cfg = configOf(get());
      const rpId = window.location.hostname;
      const origin = window.location.origin;
      let postAttempted = false;
      let submittedCandidate: WebAuthnCredentialCandidate | null = null;
      set({ loginCeremony: true });
      try {
        const challenge = await beginWebAuthnEnrollment(cfg, 'login');
        if (!authGenerationIsCurrent(get, authGeneration)) return;
        let cred = get().pendingLoginPasskeyEnrollment;
        if (!cred) {
          cred = await createWebAuthnCredentialCandidate(rpId, origin, challenge);
          if (!authGenerationIsCurrent(get, authGeneration)) return;
          set({ pendingLoginPasskeyEnrollment: cred });
        }
        const enrollmentProof = await proveWebAuthnCredentialEnrollment(cred, cred.rpId, challenge);
        if (!authGenerationIsCurrent(get, authGeneration)) return;
        submittedCandidate = cred;
        postAttempted = true;
        await ctlRegisterPasskey(cfg, {
          alg: cred.alg,
          credentialId: cred.credentialId,
          publicKeyPEM: cred.publicKeyPEM,
          rpId: cred.rpId,
          origin: cred.origin,
          enrollmentProof,
        });
        if (!authGenerationIsCurrent(get, authGeneration)) return;
        // The mutation result is newer than every status probe that began before it committed.
        passkeyStatusRequestSequence += 1;
        set({ passkeyRegistered: true, pendingLoginPasskeyEnrollment: null, loginCeremony: false });
      } catch (err) {
        if (!authGenerationIsCurrent(get, authGeneration)) return;
        set({ loginCeremony: false });
        const code = controllerErrorCode(err);
        const shouldReconcile = code === 'login_credential_changed'
          || (postAttempted && code === null && submittedCandidate !== null);
        if (shouldReconcile) {
          const requestSequence = ++passkeyStatusRequestSequence;
          let status: LoginPasskeyStatus | null = null;
          try {
            status = await getPasskeyStatus(cfg);
          } catch {
            // The original registration error is the useful one; status is best-effort.
          }
          if (!authGenerationIsCurrent(get, authGeneration)) return;
          const statusIsCurrent = requestSequence === passkeyStatusRequestSequence;

          if (code === 'login_credential_changed') {
            // A different tab/session won the compare-and-set race. Never retain this candidate
            // as a one-click "retry": doing so would turn the next click into an unacknowledged
            // replacement of the winner. Refresh what we can and require a fresh explicit flow.
            set({
              ...(statusIsCurrent ? { passkeyRegistered: status?.registered ?? null } : {}),
              pendingLoginPasskeyEnrollment: null,
            });
          } else if (statusIsCurrent && status && submittedCandidate) {
            set({ passkeyRegistered: status.registered });
            if (loginPasskeyStatusMatchesCandidate(status, submittedCandidate)) {
              set({ pendingLoginPasskeyEnrollment: null });
              return; // exact public descriptor proves our POST committed despite response loss
            }
            if (status.registered) {
              // Some other credential is now authoritative. Do not reuse this candidate as an
              // implicit replacement even though the transport failure itself was uncoded.
              set({ pendingLoginPasskeyEnrollment: null });
            }
          }
        }
        throw err;
      }
    },

    // Disable the login passkey (two-stage): begin gets a re-authentication challenge →
    // assertLogin → finish deletes the credential. A fresh assertion is required to stop a
    // hijacked session from removing the factor outright. begin returning done means there was
    // no passkey to begin with (idempotent). Errors are thrown to the caller, displayed in place
    // by PasskeySettings.
    disablePasskey: async () => {
      if (get().loading || get().loginCeremony) return;
      const authGeneration = get().authGeneration;
      const cfg = configOf(get());
      set({ loginCeremony: true });
      try {
        const begin = await disablePasskeyBegin(cfg);
        if (!authGenerationIsCurrent(get, authGeneration)) return;
        if (begin.kind === 'done') {
          passkeyStatusRequestSequence += 1;
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
        if (!authGenerationIsCurrent(get, authGeneration)) return;
        await disablePasskeyFinish(cfg, assertion);
        if (!authGenerationIsCurrent(get, authGeneration)) return;
        passkeyStatusRequestSequence += 1;
        set({ passkeyRegistered: false, loginCeremony: false });
      } catch (err) {
        if (!authGenerationIsCurrent(get, authGeneration)) return;
        set({ loginCeremony: false });
        throw err;
      }
    },
  };
}
