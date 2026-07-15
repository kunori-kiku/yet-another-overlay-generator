// Keystone (off-host operator signing) client routes + wire types.
//
// These mirror internal/trustlist/types.go (SignedTrustList) and the keystone routes in
// internal/api/handler_keystone.go. The byte-level contract — every base64url field is RFC4648
// url-alphabet WITHOUT padding (Go base64.RawURLEncoding), the challenge binding, and the rpid
// binding — lives in ../../lib/webauthn.ts, which is the single place that builds these structs.

import { request, postJSON, ControllerError, type ControllerConfig } from './transport';

// WebAuthnAlg is the keystone signing algorithm. Only ES256 and EdDSA WebAuthn
// assertions are accepted by the node verifier; RS256 etc. are rejected.
export type WebAuthnAlg = 'webauthn-es256' | 'webauthn-eddsa';

// Wire constants for the two accepted algorithms (kept here so both the client
// and the webauthn helper import the literal from one place).
export const AlgWebAuthnES256 = 'webauthn-es256' as const;
export const AlgWebAuthnEdDSA = 'webauthn-eddsa' as const;

// SignedTrustList is the historical wire name for a WebAuthn assertion. It normally carries the
// detached signature over a deploy's canonical manifest, and the same shape carries random-
// challenge login and enrollment assertions. Field names + base64url encodings match Go exactly.
export interface SignedTrustList {
  alg: WebAuthnAlg;
  credential_id: string; // base64url(rawId)
  public_key: string; // pinned PKIX PEM (audit only; node verifies the PINNED key)
  signature: string; // base64url(response.signature) — ES256 is ASN.1 DER
  authenticator_data: string; // base64url(response.authenticatorData)
  client_data_json: string; // base64url(response.clientDataJSON)
}

// trustListResponseJSON shape from GET /trustlist: the canonical manifest bytes
// (STANDARD base64) to be signed, plus the membership epoch they carry.
interface TrustListResponseJSON {
  trustlist_json: string; // standard base64 of the canonical manifest bytes
  epoch: number;
}

// The panel-facing GET /trustlist result: trustlistJson is STANDARD base64 (the
// caller base64-decodes it to recover the canonical bytes whose SHA-256 is the
// WebAuthn challenge). A null return means the keystone is OFF for the tenant
// (404 = no operator credential pinned / nothing staged to sign).
export interface TrustListToSign {
  trustlistJson: string;
  epoch: number;
}

// operatorCredentialRequestJSON shape for POST /operator-credential: the pinned
// off-host signing credential. public_key_pem is the PKIX "PUBLIC KEY" PEM; rpid
// MUST equal the rp.id used at create() time (the verifier binds SHA256(rpid) to the
// assertion rpIdHash). New/rotated browser credentials also carry enrollmentProof.
export interface OperatorCredentialBody {
  alg: WebAuthnAlg;
  credentialId: string; // base64url(rawId)
  publicKeyPEM: string; // PKIX "PUBLIC KEY" PEM
  rpId: string; // location.hostname
  origin: string; // location.origin
  enrollmentProof: SignedTrustList; // candidate's UV assertion over a one-use server challenge
  // rotate ACKNOWLEDGES that this pin REPLACES a different already-pinned credential (which
  // strands every node until re-provisioned + redeployed). The controller refuses a changed
  // credential without it (409 keystone_rotation_requires_ack). Ignored on a first/idempotent pin.
  rotate?: boolean;
}

// OperatorCredentialPinResult is the POST /operator-credential result: rotated true only when the
// pin REPLACED a different credential; unchanged true on an idempotent re-pin; redeployRequired
// true when (after a rotation) the served fleet is still signed under the old key.
export interface OperatorCredentialPinResult {
  ok: boolean;
  rotated: boolean;
  unchanged: boolean;
  redeployRequired: boolean;
}

// OperatorCredentialStatus is the SERVER-authoritative keystone status (GET /operator-credential):
// the panel reflects THIS, never a browser-local cache, so a cleared browser can never falsely
// read "Not enrolled". It carries only non-secret public identifiers. publicKeyPEM is the
// credential's PUBLIC PEM (audit-only, already baked into every node bundle): a cleared/fresh
// browser recovers it — with credentialId/alg/rpId — to re-prompt the authenticator WITHOUT
// re-pinning (plan-3 signing-handle auto-recovery). Only public descriptor material is returned;
// YAOG does not receive plaintext private-key material or establish provider non-exportability.
export interface OperatorCredentialStatus {
  pinned: boolean;
  alg: string;
  credentialId: string;
  rpId: string;
  origin: string;
  fingerprint: string;
  publicKeyPEM: string;
  redeployRequired: boolean;
}

// getTrustlist fetches the STAGED membership manifest the operator must sign
// (operator-only). It returns the canonical bytes as STANDARD base64 plus the
// epoch — the panel base64-decodes trustlistJson and signs SHA-256 of those bytes.
//
// A 404 means the keystone is OFF (no operator credential pinned, or nothing
// staged): the handler returns 404 from GET /trustlist when there is no staged
// manifest, and the keystone is only ON once a credential is pinned. We map 404
// to null so deploy() can promote directly (today's behavior) when the keystone
// is off, and only run the signing ceremony when a manifest comes back.
export async function getTrustlist(cfg: ControllerConfig): Promise<TrustListToSign | null> {
  try {
    const res = await request(cfg, 'trustlist', { method: 'GET' });
    const data = (await res.json()) as TrustListResponseJSON;
    return { trustlistJson: data.trustlist_json, epoch: data.epoch };
  } catch (err) {
    // request() throws ControllerError on non-2xx; 404 = keystone OFF (no operator
    // credential pinned, or nothing staged) → null so deploy() promotes directly.
    // Match on the typed status, not the message. (Previously this used a raw fetch
    // WITHOUT credentials:'include', so a refreshed cookie-only operator session 401'd
    // and could not keystone-sign on Deploy on a keystone-ON tenant — F1.)
    if (err instanceof ControllerError && err.status === 404) {
      return null;
    }
    throw err;
  }
}

// postTrustlistSignature submits the operator's off-host signature over the
// staged manifest (operator-only). trustlistJson is the STANDARD base64 of the
// exact bytes signed (server-side substitution guard) and signed is the
// SignedTrustList assembled by ../../lib/webauthn.ts. A non-2xx (e.g. 400 verify
// failure, 409 manifest changed, 412 no credential pinned) throws as usual.
export async function postTrustlistSignature(
  cfg: ControllerConfig,
  body: { trustlistJson: string; signed: SignedTrustList },
): Promise<void> {
  await postJSON(
    cfg,
    'trustlist-signature',
    JSON.stringify({ trustlist_json: body.trustlistJson, signed: body.signed }),
  );
}

// postOperatorCredential pins the off-host operator signing credential, turning
// the keystone ON for the tenant (operator-only). The body's PEM must parse for
// the declared alg server-side (a malformed pin is a 400). rpid/origin carry the
// WebAuthn relying-party binding the node enforces.
export async function postOperatorCredential(
  cfg: ControllerConfig,
  body: OperatorCredentialBody,
): Promise<OperatorCredentialPinResult> {
  const res = await postJSON(
    cfg,
    'operator-credential',
    JSON.stringify({
      alg: body.alg,
      credential_id: body.credentialId,
      public_key_pem: body.publicKeyPEM,
      rpid: body.rpId,
      origin: body.origin,
      enrollment_proof: body.enrollmentProof,
      rotate: body.rotate ?? false,
    }),
  );
  const d = (await res.json()) as {
    ok?: boolean;
    rotated?: boolean;
    unchanged?: boolean;
    redeploy_required?: boolean;
  };
  return {
    ok: d.ok ?? false,
    rotated: d.rotated ?? false,
    unchanged: d.unchanged ?? false,
    redeployRequired: d.redeploy_required ?? false,
  };
}

// getOperatorCredentialStatus reads the SERVER-authoritative keystone status (GET
// /operator-credential, operator-only). It is the source of truth for the panel's "enrolled"
// display — a browser-local cache must never decide it (a cleared browser would falsely read
// "Not enrolled" and invite a fleet-stranding re-pin).
export async function getOperatorCredentialStatus(
  cfg: ControllerConfig,
): Promise<OperatorCredentialStatus> {
  const res = await request(cfg, 'operator-credential', { method: 'GET' });
  const d = (await res.json()) as {
    pinned?: boolean;
    alg?: string;
    credential_id?: string;
    rpid?: string;
    origin?: string;
    fingerprint?: string;
    public_key_pem?: string;
    redeploy_required?: boolean;
  };
  // Security-sensitive tri-state boundary: an absent/malformed `pinned` value is UNKNOWN, not
  // keystone-off. Callers deliberately retain their prior/null status on this error, so the panel
  // cannot expose the dangerous no-keystone workflow without an explicit false from the server.
  if (typeof d.pinned !== 'boolean') {
    throw new Error('operator credential status response is missing boolean pinned');
  }
  return {
    pinned: d.pinned,
    alg: d.alg ?? '',
    credentialId: d.credential_id ?? '',
    rpId: d.rpid ?? '',
    origin: d.origin ?? '',
    fingerprint: d.fingerprint ?? '',
    publicKeyPEM: d.public_key_pem ?? '',
    redeployRequired: d.redeploy_required ?? false,
  };
}
