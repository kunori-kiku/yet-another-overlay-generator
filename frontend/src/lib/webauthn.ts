// WebAuthn (FIDO2) ceremony helpers for the keystone operator signing flow
// (plan-5.1d). The operator pins an OFF-HOST credential (a passkey / YubiKey)
// once at enrollment, then signs every deploy's membership manifest with it.
//
// The bytes produced here must verify byte-for-byte against the Go node verifier
// in internal/trustlist/webauthn.go. The exact wire contract is:
//
//   SignedTrustList (POST /trustlist-signature `signed` field):
//     { alg, credential_id, public_key, signature,
//       authenticator_data, client_data_json }
//   — alg          : "webauthn-es256" | "webauthn-eddsa"
//   — credential_id: base64url(rawId)                       (RawURLEncoding)
//   — public_key   : the pinned PKIX PEM (audit only, never trusted by node)
//   — signature    : base64url(response.signature)          (RawURLEncoding)
//                    ES256 returns ASN.1 DER (ecdsa.VerifyASN1 on the Go side)
//   — authenticator_data: base64url(response.authenticatorData)
//   — client_data_json  : base64url(response.clientDataJSON)
//
//   Operator credential PIN (POST /operator-credential at enrollment):
//     { alg, credential_id, public_key_pem, rpid, origin }
//   — public_key_pem: PKIX "PUBLIC KEY" PEM wrapping the SPKI DER returned by
//                     cred.response.getPublicKey()
//   — rpid          : location.hostname (node checks SHA256(rpid)==authData
//                     rpIdHash, so rp.id at create() MUST equal this)
//   — origin        : location.origin (advisory on the node)
//
// CHALLENGE BINDING: the node checks clientData.challenge ==
// base64url(SHA256(Canonical(manifest))). The browser base64url-encodes the
// challenge ArrayBuffer into clientDataJSON automatically, so we pass the RAW
// SHA-256 digest of the DECODED canonical manifest bytes as the challenge — we
// do NOT pre-encode it.
//
// All base64 / base64url conversions and PEM wrapping are done by hand so this
// module pulls in no new npm dependency.

import { AlgWebAuthnES256, AlgWebAuthnEdDSA } from '../api/controllerClient';
import type { WebAuthnAlg, SignedTrustList } from '../api/controllerClient';

// COSE algorithm identifiers returned by getPublicKeyAlgorithm(). We accept only
// ES256 (-7) and EdDSA (-8); anything else (notably RS256 = -257) is rejected so
// the pinned credential always matches one of the two algorithms the node
// verifier dispatches on.
//
// ES256 is the PRIMARY, production path: it is universally supported by passkeys
// and YubiKeys, and is the one to trust. EdDSA is best-effort — OKP/Ed25519
// getPublicKey() support varies by platform/browser, and the live ceremony
// cannot be exercised in CI (no authenticator) — but both verify against the
// node when present, and the authenticator picks the first it supports.
const COSE_ES256 = -7;
const COSE_EDDSA = -8;

// WebAuthnError is the typed error this module throws. `kind` lets the UI react
// differently to a user cancel/timeout (NotAllowedError) vs. an unsupported
// platform vs. a rejected algorithm — without string-matching messages.
export type WebAuthnErrorKind =
  | 'unsupported' // navigator.credentials / WebAuthn not available
  | 'cancelled' // user dismissed the prompt or it timed out (NotAllowedError)
  | 'unsupported-algorithm' // authenticator returned a non-ES256/EdDSA key
  | 'no-public-key' // getPublicKey() returned null (no SPKI available)
  | 'invalid-rp-id' // RP ID is an IP literal (e.g. panel opened at http://127.0.0.1)
  | 'failed'; // any other ceremony failure

export class WebAuthnError extends Error {
  readonly kind: WebAuthnErrorKind;
  constructor(kind: WebAuthnErrorKind, message: string) {
    super(message);
    this.name = 'WebAuthnError';
    this.kind = kind;
  }
}

// WebAuthn forbids IP-address RP IDs: per spec the RP ID must be a registrable
// domain, and 'localhost' is the only non-domain browsers special-case. When the
// panel is opened at http://127.0.0.1:PORT (or [::1]), location.hostname is an IP
// literal, so navigator.credentials.create()/.get() reject it with an opaque
// "This is an invalid domain." Catch it up front and tell the operator exactly how
// to fix it — browse to http://localhost:PORT, which resolves to the same loopback
// container but presents a valid RP ID.
// `host` is always a window.location.hostname value, which the URL parser has
// already normalized — IPv4 shorthand (127.1, 2130706433) is expanded to a
// dotted-quad before it reaches us, so the strict 4-octet regex still catches it.
function isIpLiteralHost(host: string): boolean {
  const h = host.replace(/^\[|\]$/g, ''); // strip IPv6 brackets if location kept them
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(h)) return true; // IPv4 dotted-quad
  if (h.includes(':')) return true; // IPv6 — a colon never appears in a DNS hostname
  return false;
}

function assertRegistrableRpId(rpId: string): void {
  if (!isIpLiteralHost(rpId)) return;
  const port =
    typeof location !== 'undefined' && location.port ? `:${location.port}` : '';
  throw new WebAuthnError(
    'invalid-rp-id',
    `WebAuthn can't use the IP address "${rpId}" as its domain. Open the panel at ` +
      `http://localhost${port} (not an IP address like 127.0.0.1 or ::1), or use a ` +
      `real hostname behind a reverse proxy, then register the passkey again.`,
  );
}

// The result of pinning an operator credential: everything the panel needs to
// (a) POST /operator-credential and (b) persist enough to drive later signings.
export interface EnrolledOperatorCredential {
  alg: WebAuthnAlg;
  credentialId: string; // base64url(rawId)
  publicKeyPEM: string; // PKIX "PUBLIC KEY" PEM
}

// --- base64 / base64url helpers (hand-rolled, no deps) ---

// base64url-encode bytes with the RFC4648 url alphabet and NO padding — matches
// Go's base64.RawURLEncoding, which every base64url field on the wire uses.
function bytesToBase64Url(bytes: Uint8Array): string {
  let bin = '';
  for (let i = 0; i < bytes.length; i++) {
    bin += String.fromCharCode(bytes[i]);
  }
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

// Standard base64 (with padding) of bytes — used only for the PKIX PEM body,
// which is standard base64 per RFC 7468.
function bytesToBase64Std(bytes: Uint8Array): string {
  let bin = '';
  for (let i = 0; i < bytes.length; i++) {
    bin += String.fromCharCode(bytes[i]);
  }
  return btoa(bin);
}

// Wrap SPKI DER bytes into a PKIX "PUBLIC KEY" PEM block with 64-char lines, the
// shape ParseES256Pin / ParseEd25519PinPEM expect on the Go side.
function spkiToPEM(spki: Uint8Array): string {
  const b64 = bytesToBase64Std(spki);
  const lines: string[] = [];
  for (let i = 0; i < b64.length; i += 64) {
    lines.push(b64.slice(i, i + 64));
  }
  return `-----BEGIN PUBLIC KEY-----\n${lines.join('\n')}\n-----END PUBLIC KEY-----\n`;
}

// --- random challenge / user id (enrollment only) ---

// A fresh random n-byte buffer, explicitly backed by an ArrayBuffer (not a
// SharedArrayBuffer) so it satisfies the BufferSource the WebAuthn options want.
// Used for the enrollment challenge (NOT content-bound: enrollment proves
// possession, not authorization of any bytes) and for a random user.id handle.
function randomBytes(n: number): Uint8Array<ArrayBuffer> {
  const b = new Uint8Array(new ArrayBuffer(n));
  crypto.getRandomValues(b);
  return b;
}

// Map a COSE algorithm identifier to our wire Alg, rejecting anything but
// ES256 / EdDSA (RS256 and friends are excluded fail-closed).
function algFromCOSE(cose: number): WebAuthnAlg {
  switch (cose) {
    case COSE_ES256:
      return AlgWebAuthnES256;
    case COSE_EDDSA:
      return AlgWebAuthnEdDSA;
    default:
      throw new WebAuthnError(
        'unsupported-algorithm',
        `authenticator returned COSE algorithm ${cose}; only ES256 (-7) and EdDSA (-8) are supported`,
      );
  }
}

// Guard: WebAuthn must be available in this context (secure origin + a browser
// that exposes PublicKeyCredential).
function assertWebAuthnAvailable(): void {
  if (
    typeof navigator === 'undefined' ||
    !navigator.credentials ||
    typeof PublicKeyCredential === 'undefined'
  ) {
    throw new WebAuthnError(
      'unsupported',
      'WebAuthn is not available in this browser (a secure https/localhost context is required)',
    );
  }
}

// Normalize a thrown DOMException/Error into a typed WebAuthnError. A
// NotAllowedError is the browser's signal for a user cancel OR a timeout — we
// surface it as 'cancelled' so the UI can prompt a retry rather than show a
// scary failure.
function toWebAuthnError(err: unknown, fallback: string): WebAuthnError {
  if (err instanceof WebAuthnError) {
    return err;
  }
  if (err instanceof DOMException && err.name === 'NotAllowedError') {
    return new WebAuthnError('cancelled', 'the security-key prompt was dismissed or timed out');
  }
  const msg = err instanceof Error ? err.message : String(err);
  return new WebAuthnError('failed', `${fallback}: ${msg}`);
}

// --- enrollment: pin an off-host operator credential ---

// enrollOperatorCredential drives navigator.credentials.create() to mint a new
// off-host signing credential, then extracts its SPKI public key + COSE alg via
// the MODERN WebAuthn API (getPublicKey / getPublicKeyAlgorithm), avoiding CBOR
// attestation parsing entirely.
//
// rpId MUST equal the rpid you POST to /operator-credential (the node verifier
// checks SHA256(rpid)==authData rpIdHash), so callers pass location.hostname.
// origin is recorded for the advisory origin check on the node.
export async function enrollOperatorCredential(
  rpId: string,
  origin: string,
): Promise<EnrolledOperatorCredential> {
  assertWebAuthnAvailable();
  // Reject an IP-literal RP ID with actionable guidance before the browser throws
  // its opaque "invalid domain" (panel opened at http://127.0.0.1 instead of localhost).
  assertRegistrableRpId(rpId);

  // Sanity-check the caller's recorded origin against the live browser origin: the
  // credential is created in THIS context, and that same origin is what the node's
  // (advisory) origin check compares clientData.origin against later. A mismatch
  // means the caller is about to PIN an origin the browser won't actually present,
  // which would trip the node's advisory check on every signed deploy.
  if (typeof location !== 'undefined' && origin !== location.origin) {
    throw new WebAuthnError(
      'failed',
      `origin mismatch: pinning ${origin} but this context is ${location.origin}`,
    );
  }

  const options: PublicKeyCredentialCreationOptions = {
    // Enrollment challenge is NOT content-bound (it only proves the
    // authenticator is present); a fresh random value defeats replay.
    challenge: randomBytes(32),
    rp: {
      // id MUST equal the rpid posted to the controller; the node binds
      // SHA256(rpid) against the assertion's rpIdHash at verify time.
      id: rpId,
      name: 'YAOG',
    },
    user: {
      id: randomBytes(16),
      name: 'YAOG operator',
      displayName: 'YAOG operator',
    },
    // ES256 first, EdDSA second — ES256 + EdDSA ONLY (no RS256). The node
    // verifier dispatches only on these two; the authenticator picks the first
    // it supports.
    pubKeyCredParams: [
      { type: 'public-key', alg: COSE_ES256 },
      { type: 'public-key', alg: COSE_EDDSA },
    ],
    authenticatorSelection: {
      userVerification: 'required',
    },
    timeout: 120000,
    attestation: 'none',
  };

  let cred: PublicKeyCredential | null;
  try {
    cred = (await navigator.credentials.create({
      publicKey: options,
    })) as PublicKeyCredential | null;
  } catch (err) {
    throw toWebAuthnError(err, 'failed to enroll signing credential');
  }
  if (!cred) {
    throw new WebAuthnError('failed', 'no credential was created');
  }

  const response = cred.response as AuthenticatorAttestationResponse;
  if (typeof response.getPublicKey !== 'function') {
    throw new WebAuthnError(
      'unsupported',
      'this browser does not expose getPublicKey() on the attestation response',
    );
  }

  const alg = algFromCOSE(response.getPublicKeyAlgorithm());
  const spki = response.getPublicKey();
  if (!spki) {
    throw new WebAuthnError('no-public-key', 'the authenticator did not return a public key');
  }

  return {
    alg,
    credentialId: bytesToBase64Url(new Uint8Array(cred.rawId)),
    publicKeyPEM: spkiToPEM(new Uint8Array(spki)),
  };
}

// --- signing: produce a content-bound SignedTrustList over a manifest ---

// signManifest drives navigator.credentials.get() to sign the EXACT canonical
// manifest bytes the controller staged, returning a SignedTrustList ready for
// POST /trustlist-signature.
//
// CONTENT BINDING: challenge = SHA-256(manifestBytes). manifestBytes are the
// DECODED canonical bytes (the caller base64-decodes the standard-base64
// trustlist_json from GET /trustlist before calling this). The browser
// base64url-encodes this digest into clientDataJSON.challenge, which the node
// compares against base64url(SHA256(Canonical(manifest))) — so this is the
// proof the operator authorized THESE exact bytes.
//
// rpId MUST equal the rpid that was pinned at enrollment (the node checks
// SHA256(rpid)==authData rpIdHash). We pass it EXPLICITLY rather than letting the
// browser default rpId to the caller's effective domain: the implicit default
// only happens to match when enroll and sign run from the same hostname, and a
// mismatch would surface as an opaque node-side rpIdHash failure (a deploy-time
// 400) instead of a clear browser error. Pinning it makes the binding explicit.
//
// publicKeyPEM is the pinned PEM (audit-only field on the wire); pass the value
// stored at enrollment.
export async function signManifest(
  manifestBytes: Uint8Array,
  credentialId: string,
  alg: WebAuthnAlg,
  rpId: string,
  publicKeyPEM: string,
): Promise<SignedTrustList> {
  assertWebAuthnAvailable();

  // challenge = raw SHA-256 of the decoded canonical manifest bytes. Pass it
  // RAW — the browser base64url-encodes it into clientDataJSON for us; do NOT
  // pre-encode it (that would double-encode and break the content binding).
  const digest = await crypto.subtle.digest('SHA-256', manifestBytes as BufferSource);
  return runAssertion(
    new Uint8Array(digest),
    credentialId,
    alg,
    rpId,
    publicKeyPEM,
    'failed to sign the deploy manifest',
  );
}

// --- login: produce a random-challenge SignedTrustList (operator passkey login) ---

// assertLogin drives navigator.credentials.get() to prove possession of a registered
// LOGIN passkey over a server-issued RANDOM challenge (the sibling of signManifest's
// content-bound manifest hash). It is used by both the password+passkey 2FA leg and the
// passwordless flow. challengeB64 is the base64url nonce from the controller (passkey_
// required / begin); credentialId + alg + rpId come from the same response. The returned
// SignedTrustList's public_key is empty: the controller verifies against the PINNED
// credential it stored, never this audit-only field.
export async function assertLogin(
  challengeB64: string,
  credentialId: string,
  alg: WebAuthnAlg,
  rpId: string,
): Promise<SignedTrustList> {
  // The login challenge is a RANDOM nonce, not content-bound — decode it to the raw
  // bytes the browser signs over (do NOT hash it, unlike signManifest).
  return runAssertion(
    base64UrlToBytes(challengeB64),
    credentialId,
    alg,
    rpId,
    '',
    'failed to complete the passkey login',
  );
}

// runAssertion is the shared navigator.credentials.get() ceremony: it asserts the pinned
// credential over `challenge` (raw bytes the browser base64url-encodes into
// clientDataJSON) and assembles the SignedTrustList wire struct. Both signManifest
// (challenge = manifest hash) and assertLogin (challenge = random nonce) delegate here;
// only the challenge bytes and the audit-only public_key differ.
async function runAssertion(
  challenge: Uint8Array,
  credentialId: string,
  alg: WebAuthnAlg,
  rpId: string,
  publicKeyPEM: string,
  failMessage: string,
): Promise<SignedTrustList> {
  assertWebAuthnAvailable();
  // Same IP-literal guard as enrollment: .get() rejects an IP RP ID identically, so
  // surface the actionable "use localhost" message instead of the opaque browser error.
  assertRegistrableRpId(rpId);

  // Restrict the assertion to the pinned credential so the browser uses exactly the key
  // the controller has on file. Pin rpId explicitly (do not rely on the effective-domain
  // default), matching the rpid the node binds SHA256(rpid)==rpIdHash against.
  const options: PublicKeyCredentialRequestOptions = {
    challenge: challenge as BufferSource,
    rpId,
    allowCredentials: [{ type: 'public-key', id: base64UrlToBytes(credentialId) as BufferSource }],
    userVerification: 'required',
    timeout: 120000,
  };

  let cred: PublicKeyCredential | null;
  try {
    cred = (await navigator.credentials.get({ publicKey: options })) as PublicKeyCredential | null;
  } catch (err) {
    throw toWebAuthnError(err, failMessage);
  }
  if (!cred) {
    throw new WebAuthnError('failed', 'no assertion was returned');
  }

  const response = cred.response as AuthenticatorAssertionResponse;
  return {
    alg,
    credential_id: bytesToBase64Url(new Uint8Array(cred.rawId)),
    // Audit-only on the wire; the node/controller verifies against the PINNED key, never
    // this field. signManifest echoes the enrolled PEM; login leaves it empty.
    public_key: publicKeyPEM,
    // ES256 assertions are ASN.1 DER (ecdsa.VerifyASN1 on the Go side); pass
    // response.signature through verbatim, base64url-encoded.
    signature: bytesToBase64Url(new Uint8Array(response.signature)),
    authenticator_data: bytesToBase64Url(new Uint8Array(response.authenticatorData)),
    client_data_json: bytesToBase64Url(new Uint8Array(response.clientDataJSON)),
  };
}

// base64url (no padding) -> bytes. Mirrors Go's base64.RawURLEncoding.Decode;
// used to turn the stored credential_id back into an allowCredentials id.
function base64UrlToBytes(s: string): Uint8Array {
  const b64 = s.replace(/-/g, '+').replace(/_/g, '/');
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) {
    out[i] = bin.charCodeAt(i);
  }
  return out;
}
