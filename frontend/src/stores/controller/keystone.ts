// Keystone slice: the off-host operator signing credential (local non-secret descriptor cache +
// server-authoritative status) plus enroll / hydrate-status / cancel-rotate. Moved verbatim from
// the single controllerStore.ts create() literal; isRecoverableWebAuthnAlg (used only by
// hydrateKeystoneStatus) is co-located as a module-private helper.

import type { ControllerSet, ControllerGet } from './types';
import {
  postOperatorCredential,
  getOperatorCredentialStatus,
  controllerErrorCode,
  AlgWebAuthnES256,
  AlgWebAuthnEdDSA,
  type WebAuthnAlg,
} from '../../api/controllerClient';
import { configOf, localizeError, serverKeystoneReset, selectHasLocalSigningKey } from './helpers';
import { enrollOperatorCredential } from '../../lib/webauthn';

// isRecoverableWebAuthnAlg narrows a SERVER-reported alg string to a WebAuthn signing alg the
// BROWSER can sign with (ES256 / EdDSA). A raw-ed25519 (CLI) keystone signs entirely off-host, so
// its descriptor is NOT browser-recoverable — deploy() cannot tap an authenticator for it, and the
// signing-handle auto-recovery (hydrateKeystoneStatus, plan-3) must leave it alone.
function isRecoverableWebAuthnAlg(alg: string): alg is WebAuthnAlg {
  return alg === AlgWebAuthnES256 || alg === AlgWebAuthnEdDSA;
}

export function createKeystoneSlice(set: ControllerSet, get: ControllerGet) {
  return {
    operatorCredentialId: null,
    operatorCredentialAlg: null,
    operatorRpId: null,
    operatorPublicKeyPEM: null,

    // SERVER-authoritative keystone status (not persisted): null = not yet probed ("checking").
    ...serverKeystoneReset,

    signing: false,
    enrolling: false,

    // KEYSTONE enroll (plan-5.1d): pin the off-host operator signing credential (passkey /
    // YubiKey), turning the keystone on. Flow: navigator.credentials.create()
    // (getPublicKey/getPublicKeyAlgorithm get the SPKI + COSE alg, avoiding CBOR) → POST
    // /operator-credential to pin the PKIX PEM + credential_id + rpid(=location.hostname) +
    // origin to the controller. rpid must equal create()'s rp.id — nodes verify
    // SHA256(rpid)==the assertion's rpIdHash. On success only the non-secret
    // credential_id/alg/rpId is left in localStorage, to set allowCredentials for later
    // signatures.
    enrollOperator: async (opts?: { rotate?: boolean }) => {
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
        const cred = await enrollOperatorCredential(rpId, origin);
        await postOperatorCredential(configOf(get()), {
          alg: cred.alg,
          credentialId: cred.credentialId,
          publicKeyPEM: cred.publicKeyPEM,
          rpId,
          origin,
          rotate,
        });
        set({
          operatorCredentialId: cred.credentialId,
          operatorCredentialAlg: cred.alg,
          operatorRpId: rpId,
          operatorPublicKeyPEM: cred.publicKeyPEM,
          enrolling: false,
          pendingKeystoneRotate: false,
        });
        // Re-probe server truth (pinned + fingerprint + redeploy-required) and refresh the fleet.
        await get().hydrateKeystoneStatus();
        await get().refresh();
      } catch (err) {
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
      }
    },

    // hydrateKeystoneStatus probes GET /operator-credential and sets the SERVER-authoritative
    // serverOperator* fields. Best-effort: a transport/auth failure leaves the fields as-is (so a
    // transient blip never flips a known status to a false "Not enrolled"). See interface note.
    hydrateKeystoneStatus: async () => {
      try {
        const st = await getOperatorCredentialStatus(configOf(get()));
        set({
          serverOperatorPinned: st.pinned,
          serverOperatorAlg: st.pinned ? st.alg : null,
          serverOperatorFingerprint: st.pinned ? st.fingerprint : null,
          serverRedeployRequired: st.pinned && st.redeployRequired,
        });
        // Signing-handle auto-recovery (plan-3): when the server holds a WebAuthn credential but
        // THIS browser has no local signing descriptor (cleared cache / fresh device), recover the
        // NON-SECRET descriptor (credentialId + alg + rpId + public PEM, which the server now
        // serves) into the EMPTY local slots so deploy() can re-prompt the authenticator for a tap
        // — no fleet-stranding re-pin. The private key never leaves the authenticator; this only
        // restores public material the node bundles already carry. Guards: (1) only WebAuthn algs
        // can browser-sign — a raw-ed25519 CLI keystone is left untouched; (2) FILL-EMPTY-ONLY (the
        // per-field ?? + the selectHasLocalSigningKey gate) never clobbers a freshly-enrolled local
        // cache (enrollOperator sets the local fields before calling hydrate, so a populated slot is
        // authoritative).
        if (
          st.pinned &&
          isRecoverableWebAuthnAlg(st.alg) &&
          !!st.credentialId &&
          !!st.publicKeyPEM
        ) {
          const s = get();
          if (!selectHasLocalSigningKey(s)) {
            set({
              operatorCredentialId: s.operatorCredentialId ?? st.credentialId,
              operatorCredentialAlg: s.operatorCredentialAlg ?? st.alg,
              operatorRpId: s.operatorRpId ?? (st.rpId || null),
              operatorPublicKeyPEM: s.operatorPublicKeyPEM ?? st.publicKeyPEM,
            });
          }
        }
      } catch {
        // Leave the prior status untouched — a probe failure is not evidence of "not enrolled".
      }
    },

    cancelKeystoneRotate: () => set({ pendingKeystoneRotate: false }),
  };
}
