// WireGuard key-derivation seam — the TypeScript mirror of the plan-3-frozen three-operation Keygen
// interface (render.go:66-78). This is the browser-side replacement for the Go wgtypesKeygen
// (render.go:86-114), which wraps wgtypes.ParseKey/.PublicKey()/.String(). The three operations are:
//
//   - derivePublic(privB64)      — wgtypes.ParseKey(priv).PublicKey().String() (render.go:88-94)
//   - generate()                 — wgtypes.GeneratePrivateKey() + .String()/.PublicKey().String()
//                                   (render.go:97-104)
//   - parseAndNormalize(privB64) — wgtypes.ParseKey(priv).String() (render.go:106-113), the air-gap
//                                   case-a re-canonicalization persisted back onto the node.
//
// X25519 derivation goes through @noble/curves x25519.getPublicKey — the DOCUMENTED API, which clamps
// the scalar INTERNALLY (key[0] &= 248; key[31] &= 127; key[31] |= 64) exactly as crypto/ecdh and
// wgtypes do. We do NOT pre-clamp and do NOT call scalarMultBase: a hand-clamp before a getPublicKey
// that also clamps risks a double-clamp divergence. Clamp/derivation correctness is pinned by
// keygen.kat.test.ts against the Go-authored RFC 7748 + clamp-boundary vectors, not by this prose.
//
// All base64 here is STANDARD (with padding), NOT url — mirroring Go's base64.StdEncoding, which
// wgtypes.ParseKey decodes and Key.String() encodes (wgtypes/types.go:126/154). webauthn.ts's base64
// helpers are RawURLEncoding (url alphabet, no padding) and MUST NOT be reused here.
//
// The seam intentionally throws PLAIN Errors on bad input, mirroring wgtypes.ParseKey returning a bare
// error. The consumer (render.go GenerateKeys, :170-218) is what wraps that into apierr codes
// (keygen_privkey_parse_failed, keygen_pinned_pubkey_no_privkey, keygen_generate_failed) — that
// wrapping is a later-phase compile concern, NOT this leaf's responsibility. The public-only-no-private
// hard error (render.go case-b, :209) likewise lives in the compile pass, not here.

import { x25519 } from '@noble/curves/ed25519.js';

// KEY_LEN is the WireGuard key length in bytes (wgtypes.KeyLen = 32, wgtypes/types.go:72). Both private
// and public X25519 keys are exactly this many raw bytes.
const KEY_LEN = 32;

// base64StdDecode decodes a standard-base64 string to raw bytes, FAITHFULLY to Go's
// base64.StdEncoding.DecodeString — NOT just "atob, which raises on malformed input". atob is too
// lenient on two axes where it would silently accept a key Go REJECTS: it tolerates unpadded base64
// and (in some engines) embedded whitespace such as space/tab. Go's StdEncoding decoder, by contrast,
// IGNORES embedded '\n' and '\r' (the only whitespace it skips) but REJECTS space, tab, and every other
// non-alphabet byte, and REQUIRES correct '=' padding so the encoded length is a multiple of 4. Verified
// against base64.StdEncoding directly: a space, a tab, and an unpadded 32-byte key are all rejected;
// embedded '\n'/'\r' are accepted.
//
// So we reproduce that grammar exactly: strip ONLY '\n' and '\r' (the bytes Go's decoder skips), then
// require the remainder to be standard-alphabet chars with 0–2 trailing '=' and a length that is a
// multiple of 4 — rejecting anything else (space, tab, unpadded, mid-string padding) BEFORE handing the
// cleaned string to atob. That makes this byte-exact to Go in BOTH directions (no TS-accepts/Go-rejects
// and no TS-rejects/Go-accepts divergence). It throws on a malformed string, matching wgtypes.ParseKey's
// "failed to parse base64-encoded key" error path (wgtypes/types.go:128).
const STD_BASE64_RE = /^[A-Za-z0-9+/]*={0,2}$/;

function base64StdDecode(s: string): Uint8Array {
  // Go's StdEncoding decoder skips embedded '\n' and '\r' (and ONLY those); strip them first.
  const cleaned = s.replace(/[\n\r]/g, '');
  if (cleaned.length % 4 !== 0 || !STD_BASE64_RE.test(cleaned)) {
    throw new Error('illegal base64 data');
  }
  const bin = atob(cleaned);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) {
    out[i] = bin.charCodeAt(i);
  }
  return out;
}

// base64StdEncode encodes raw bytes to a standard-base64 string (with padding), mirroring Go's
// base64.StdEncoding.EncodeToString. String.fromCharCode over the byte values builds the latin1 string
// btoa expects; btoa emits the standard alphabet (+/ and = padding) — byte-identical to Go's
// Key.String() (wgtypes/types.go:154).
function base64StdEncode(bytes: Uint8Array): string {
  let bin = '';
  for (let i = 0; i < bytes.length; i++) {
    bin += String.fromCharCode(bytes[i]);
  }
  return btoa(bin);
}

// parsePrivateKey decodes a base64-std private key and validates it is exactly 32 bytes, mirroring
// wgtypes.ParseKey (wgtypes/types.go:125-133): decode, then reject any length != KeyLen. The two error
// shapes (bad base64 vs wrong size) match the wgtypes branches; the consumer surfaces them as
// keygen_privkey_parse_failed with the detail attached.
function parsePrivateKey(privB64: string): Uint8Array {
  const raw = base64StdDecode(privB64);
  if (raw.length !== KEY_LEN) {
    throw new Error(`wgtypes: incorrect key size: ${raw.length}`);
  }
  return raw;
}

// derivePublic returns the base64-std public key for a base64-std X25519 private key. Mirrors
// wgtypesKeygen.DerivePublic = wgtypes.ParseKey(priv).PublicKey().String() (render.go:88-94): parse +
// validate 32 bytes, X25519 scalar-base-mult (getPublicKey clamps internally), re-encode std base64.
export function derivePublic(privB64: string): string {
  const priv = parsePrivateKey(privB64);
  const pub = x25519.getPublicKey(priv);
  return base64StdEncode(pub);
}

// generate returns a fresh (priv, pub) X25519 key pair, both base64-std. Mirrors
// wgtypesKeygen.Generate = wgtypes.GeneratePrivateKey() + .String()/.PublicKey().String()
// (render.go:97-104, the air-gap case-c brand-new pair). crypto.getRandomValues fills 32 cryptographic
// random bytes for the private scalar; getPublicKey derives the public half (clamping internally). The
// local path never emits the AgentHeld PrivateKeyPlaceholder — that custody mode is server-only.
export function generate(): { priv: string; pub: string } {
  const priv = new Uint8Array(KEY_LEN);
  crypto.getRandomValues(priv);
  const pub = x25519.getPublicKey(priv);
  return { priv: base64StdEncode(priv), pub: base64StdEncode(pub) };
}

// parseAndNormalize round-trips a base64-std private key to its canonical base64-std form. Mirrors
// wgtypesKeygen.ParseAndNormalize = wgtypes.ParseKey(priv).String() (render.go:106-113): decode +
// validate 32 bytes, then re-encode the SAME raw bytes with standard base64. This re-canonicalizes a
// non-canonical-but-valid encoding to wgtypes' exact String() form, which the air-gap case-a write-back
// persists onto the node. The ONLY non-canonical input Go's StdEncoding (and hence this decoder) accepts
// is one carrying embedded '\n'/'\r', which the decoder skips before re-encoding to the newline-free
// canonical form; an unpadded or space/tab-laden variant is REJECTED (it is not valid StdEncoding), so it
// never reaches this round-trip. It is a pure encoding round-trip — it does NOT clamp or otherwise mutate
// the key bytes.
export function parseAndNormalize(privB64: string): string {
  const raw = parsePrivateKey(privB64);
  return base64StdEncode(raw);
}
