// Keystone slice: the off-host operator signing credential (local non-secret descriptor cache +
// server-authoritative status) plus enroll / hydrate-status / cancel-rotate. Moved verbatim from
// the single controllerStore.ts create() literal; isRecoverableWebAuthnAlg (used only by
// hydrateKeystoneStatus) is co-located as a module-private helper.

import type { ControllerSet, ControllerGet } from './types';
import {
  postOperatorCredential,
  beginWebAuthnEnrollment,
  getOperatorCredentialStatus,
  controllerErrorCode,
  AlgWebAuthnES256,
  AlgWebAuthnEdDSA,
  type WebAuthnAlg,
} from '../../api/controllerClient';
import { configOf, localizeError, serverKeystoneReset } from './helpers';
import {
  createWebAuthnCredentialCandidate,
  proveWebAuthnCredentialEnrollment,
} from '../../lib/webauthn';

// isRecoverableWebAuthnAlg narrows a SERVER-reported alg string to a WebAuthn signing alg the
// BROWSER can sign with (ES256 / EdDSA). A raw-ed25519 (CLI) keystone signs entirely off-host, so
// its descriptor is NOT browser-recoverable — deploy() cannot tap an authenticator for it, and the
// status hydrator clears any incompatible browser handle while retaining the raw public server
// descriptor for the explicit off-host/manual-kit workflows.
function isRecoverableWebAuthnAlg(alg: string): alg is WebAuthnAlg {
  return alg === AlgWebAuthnES256 || alg === AlgWebAuthnEdDSA;
}

function authGenerationIsCurrent(get: ControllerGet, generation: number): boolean {
  return get().authGeneration === generation;
}

const browserKeystoneHandleReset = {
  operatorCredentialId: null,
  operatorCredentialAlg: null,
  operatorRpId: null,
  operatorPublicKeyPEM: null,
} as const;

interface BrowserKeystoneHandle {
  operatorCredentialId: string;
  operatorCredentialAlg: WebAuthnAlg;
  operatorRpId: string;
  operatorPublicKeyPEM: string;
}

function recoverableServerHandle(st: {
  pinned: boolean;
  alg: string;
  credentialId: string;
  rpId: string;
  publicKeyPEM: string;
}): BrowserKeystoneHandle | null {
  if (
    !st.pinned
    || !isRecoverableWebAuthnAlg(st.alg)
    || st.credentialId.trim() === ''
    || st.rpId.trim() === ''
    || st.publicKeyPEM.trim() === ''
  ) {
    return null;
  }
  return {
    operatorCredentialId: st.credentialId,
    operatorCredentialAlg: st.alg,
    operatorRpId: st.rpId,
    operatorPublicKeyPEM: st.publicKeyPEM,
  };
}

function localHandleMatches(
  state: ReturnType<ControllerGet>,
  handle: BrowserKeystoneHandle,
): boolean {
  return state.operatorCredentialId === handle.operatorCredentialId
    && state.operatorCredentialAlg === handle.operatorCredentialAlg
    && state.operatorRpId === handle.operatorRpId
    && state.operatorPublicKeyPEM === handle.operatorPublicKeyPEM;
}

export function createKeystoneSlice(set: ControllerSet, get: ControllerGet) {
  // Auth generation separates controller/session identities. This additional sequence orders
  // concurrent status probes inside one identity: only the most recently-started probe may
  // reconcile server truth into the browser handle. Enrollment invalidates older probes as soon as
  // its POST succeeds, before publishing the newly-enrolled local descriptor.
  let keystoneStatusRequestSequence = 0;

  return {
    operatorCredentialId: null,
    operatorCredentialAlg: null,
    operatorRpId: null,
    operatorPublicKeyPEM: null,
    pendingKeystoneEnrollment: null,

    // SERVER-authoritative keystone status (not persisted): null = not yet probed ("checking").
    ...serverKeystoneReset,

    signing: false,
    enrolling: false,

    // KEYSTONE enroll (plan-5.1d): begin a server nonce → create() a candidate → get() a
    // UV-bearing assertion from that exact candidate → POST /operator-credential. If the second
    // phase fails, retain the public descriptor in volatile memory for a no-duplicate retry.
    // getPublicKey/getPublicKeyAlgorithm extract SPKI + COSE alg without CBOR. rpid must equal
    // create()'s rp.id — nodes verify SHA256(rpid)==the assertion's rpIdHash. On success the
    // non-secret credential ID/algorithm/RP ID/public PEM invocation handle may be persisted for
    // later assertions; the provider retains the private key and YAOG receives no plaintext copy.
    enrollOperator: async (opts?: { rotate?: boolean }) => {
      // The UI disables its button, but keep the action itself idempotent under a synthetic double
      // click so two create() ceremonies cannot produce duplicate authenticator credentials.
      if (get().enrolling) return;
      const authGeneration = get().authGeneration;
      const cfg = configOf(get());
      const rotate = opts?.rotate === true;
      // Guard the fleet-stranding re-pin: when a credential is ALREADY pinned on the server and
      // the operator has not explicitly confirmed a rotation, do NOT start the WebAuthn ceremony
      // — arm pendingKeystoneRotate so the UI demands confirmation first (a rotation strands every
      // node until each is re-provisioned out of band AND a fresh deploy is signed).
      if (get().serverOperatorPinned === true && !rotate) {
        set({ pendingKeystoneRotate: true, error: null });
        return;
      }
      // rp.id must be the registrable domain (location.hostname); WebAuthn is unavailable in a
      // non-secure context.
      const rpId = window.location.hostname;
      const origin = window.location.origin;
      set({ enrolling: true, error: null });
      try {
        const challenge = await beginWebAuthnEnrollment(cfg, 'keystone');
        if (!authGenerationIsCurrent(get, authGeneration)) return;
        let cred = get().pendingKeystoneEnrollment;
        if (!cred) {
          cred = await createWebAuthnCredentialCandidate(rpId, origin, challenge);
          if (!authGenerationIsCurrent(get, authGeneration)) return;
          set({ pendingKeystoneEnrollment: cred });
        }
        const enrollmentProof = await proveWebAuthnCredentialEnrollment(cred, cred.rpId, challenge);
        if (!authGenerationIsCurrent(get, authGeneration)) return;
        await postOperatorCredential(cfg, {
          alg: cred.alg,
          credentialId: cred.credentialId,
          publicKeyPEM: cred.publicKeyPEM,
          rpId: cred.rpId,
          origin: cred.origin,
          enrollmentProof,
          rotate,
        });
        if (!authGenerationIsCurrent(get, authGeneration)) return;
        // A status request started before this successful mutation is stale even if its response
        // arrives next. Invalidate it before installing the new descriptor; the immediate hydrate
        // below starts a still-newer authoritative probe.
        keystoneStatusRequestSequence += 1;
        set({
          operatorCredentialId: cred.credentialId,
          operatorCredentialAlg: cred.alg,
          operatorRpId: cred.rpId,
          operatorPublicKeyPEM: cred.publicKeyPEM,
          pendingKeystoneEnrollment: null,
          enrolling: false,
          pendingKeystoneRotate: false,
        });
        // Re-probe server truth (pinned + fingerprint + redeploy-required) and refresh the fleet.
        await get().hydrateKeystoneStatus();
        if (!authGenerationIsCurrent(get, authGeneration)) return;
        await get().refresh();
      } catch (err) {
        if (!authGenerationIsCurrent(get, authGeneration)) return;
        const hadPendingCandidate = get().pendingKeystoneEnrollment !== null;
        // A race (the server gained a credential between our status probe and this POST) surfaces
        // as the rotation-ack refusal — arm the confirmation instead of a raw error.
        if (controllerErrorCode(err) === 'keystone_rotation_requires_ack') {
          set({
            enrolling: false,
            pendingKeystoneRotate: true,
            error: null,
          });
          return;
        }
        set({
          error: localizeError(err, 'error.generic'),
          enrolling: false,
        });
        await get().hydrateKeystoneStatus();
        if (!authGenerationIsCurrent(get, authGeneration)) return;
        if (hadPendingCandidate && get().pendingKeystoneEnrollment === null) {
          set({ error: null }); // server truth confirmed the POST committed despite response loss
        }
      }
    },

    // hydrateKeystoneStatus probes GET /operator-credential and sets the SERVER-authoritative
    // serverOperator* fields. Best-effort: a transport/auth failure leaves the fields as-is (so a
    // transient blip never flips a known status to a false "Not enrolled"). See interface note.
    hydrateKeystoneStatus: async () => {
      const authGeneration = get().authGeneration;
      const requestSequence = ++keystoneStatusRequestSequence;
      const cfg = configOf(get());
      try {
        const st = await getOperatorCredentialStatus(cfg);
        if (
          !authGenerationIsCurrent(get, authGeneration)
          || requestSequence !== keystoneStatusRequestSequence
        ) return;

        const state = get();
        const serverHandle = recoverableServerHandle(st);
        const pending = state.pendingKeystoneEnrollment;
        const pendingCommitted = !!(
          pending
          && st.pinned
          && st.credentialId === pending.credentialId
          && st.alg === pending.alg
          && st.publicKeyPEM === pending.publicKeyPEM
          && st.rpId === pending.rpId
          && st.origin === pending.origin
        );

        // Reconcile the server status and browser signing handle in one state transition. A
        // complete WebAuthn server tuple is authoritative and replaces any stale/partial local
        // tuple wholesale; per-field filling could splice an old credential ID/algorithm together
        // with a rotated key's PEM/RP ID. A non-browser-signable status clears the incompatible
        // browser handle while retaining the server's public fields for manual-kit workflows.
        const reconciliation = {
          serverOperatorPinned: st.pinned,
          serverOperatorAlg: st.pinned ? st.alg : null,
          serverOperatorRpId: st.pinned ? (st.rpId || null) : null,
          serverOperatorOrigin: st.pinned ? (st.origin || null) : null,
          serverOperatorPublicKeyPEM: st.pinned ? (st.publicKeyPEM || null) : null,
          serverOperatorFingerprint: st.pinned ? st.fingerprint : null,
          serverRedeployRequired: st.pinned && st.redeployRequired,
          ...(
            serverHandle
              ? (localHandleMatches(state, serverHandle) ? {} : serverHandle)
              : browserKeystoneHandleReset
          ),
          ...(pendingCommitted
            ? { pendingKeystoneEnrollment: null, pendingKeystoneRotate: false }
            : {}),
        };
        set(reconciliation);
        // A POST can commit server-side while its response is lost. If server truth now exactly
        // matches the volatile candidate, enrollment did succeed. The atomic reconciliation above
        // both installs that exact server descriptor and clears the retry marker. A mismatched
        // pending candidate stays separate so using it still requires explicit rotation consent.
      } catch {
        // Leave the prior status untouched — a probe failure is not evidence of "not enrolled".
      }
    },

    cancelKeystoneRotate: () => set({ pendingKeystoneRotate: false }),
  };
}
