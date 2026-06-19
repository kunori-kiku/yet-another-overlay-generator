// X25519 known-answer test (KAT) — THE crypto-correctness gate for the TS keygen seam. A wrong-but-
// plausible public key would pass every shape check yet fail a real WireGuard handshake on hardware, so
// this pins @noble/curves x25519 derivation byte-for-byte against the Go oracle's vectors.
//
// Source of truth: internal/conformance/testdata/keygen_kat.json (Go-authored, RFC 7748 section 6.1 +
// clamp-boundary vectors). public_b64 is the base64.StdEncoding X25519 public key, proven byte-equal to
// the default wgtypesKeygen over 10k inputs by keygen_equivalence_test.go on the Go side. Asserting
// @noble against THIS file makes plans 3/4/5 all pin one X25519 derivation definition.
//
// The clamp_equivalence vectors prove @noble's INTERNAL clamp matches Go: a raw private key and its
// X25519-clamped form (key[0] &= 248; key[31] &= 127; key[31] |= 64) MUST derive the identical public
// key. This is the property the seam relies on by calling getPublicKey (which clamps) WITHOUT a hand
// pre-clamp — a double-clamp or a skipped-clamp port would fail here.

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { describe, expect, it } from 'vitest';

import { derivePublic, parseAndNormalize } from './keygen';

// thisDir = frontend/src/compiler; three '..' hops reach the repo root (compiler → src → frontend →
// root), mirroring the heal.conformance.test.ts loader idiom so both read the shared Go corpus the same
// way.
const thisDir = dirname(fileURLToPath(import.meta.url));
const repoRoot = join(thisDir, '..', '..', '..');
const katPath = join(repoRoot, 'internal/conformance/testdata/keygen_kat.json');

interface KatVector {
  name: string;
  doc: string;
  private_b64: string;
  public_b64: string;
}

interface ClampEquivVector {
  name: string;
  doc: string;
  raw_b64: string;
  clamped_b64: string;
  public_b64: string;
}

interface KatFile {
  note: string;
  vectors: KatVector[];
  clamp_equivalence: ClampEquivVector[];
}

const kat = JSON.parse(readFileSync(katPath, 'utf8')) as KatFile;

describe('keygen X25519 KAT (Go-authored RFC 7748 + clamp-boundary vectors)', () => {
  // Sanity: the corpus loaded and is non-empty, so a silently-empty fixture can't make the suite a
  // vacuous pass.
  it('loaded the Go KAT corpus with vectors', () => {
    expect(kat.vectors.length).toBeGreaterThan(0);
    expect(kat.clamp_equivalence.length).toBeGreaterThan(0);
  });

  // Every vectors[] entry: derivePublic(private_b64) === public_b64. This is the core assertion that
  // @noble's getPublicKey reproduces the Go/crypto-ecdh public key byte-for-byte.
  for (const v of kat.vectors) {
    it(`derivePublic matches Go for ${v.name}`, () => {
      expect(derivePublic(v.private_b64)).toBe(v.public_b64);
    });
  }

  // Every clamp_equivalence[] entry: BOTH the raw and the pre-clamped private key derive the SAME
  // expected public key, proving @noble clamps internally exactly as Go does (so the seam correctly does
  // not pre-clamp).
  for (const v of kat.clamp_equivalence) {
    it(`raw and clamped derive identical public key for ${v.name}`, () => {
      expect(derivePublic(v.raw_b64)).toBe(v.public_b64);
      expect(derivePublic(v.clamped_b64)).toBe(v.public_b64);
    });
  }
});

describe('keygen parseAndNormalize round-trip', () => {
  // FAITHFULNESS to Go's StdEncoding: an UNPADDED 32-byte key is NOT valid base64.StdEncoding — Go's
  // base64.StdEncoding.DecodeString rejects it ("illegal base64 data") because the encoded length is not
  // a multiple of 4. A lenient atob would silently decode it, so parseAndNormalize MUST throw to stay
  // byte-exact to wgtypes.ParseKey (no TS-accepts-where-Go-rejects). This is the flipped assertion: the
  // unpadded variant is a REJECTION, not a canonicalizable input.
  it('throws on an unpadded 32-byte key (Go StdEncoding rejects it)', () => {
    const canonical = kat.vectors[0].private_b64; // 44-char padded base64-std of 32 bytes
    const unpadded = canonical.replace(/=+$/, ''); // strip the trailing '=' pad: NOT valid StdEncoding
    expect(unpadded).not.toBe(canonical);
    expect(() => parseAndNormalize(unpadded)).toThrow();
  });

  // The ONE non-canonical encoding Go's StdEncoding DOES accept is one carrying embedded '\n' / '\r':
  // the decoder skips those bytes. parseAndNormalize must decode such a variant and re-emit the
  // newline-free canonical form, identical to wgtypes.ParseKey(priv).String() — proving the seam matches
  // Go's newline tolerance (no TS-rejects-where-Go-accepts).
  it('re-encodes a newline-laden key to the canonical newline-free base64-std form', () => {
    const canonical = kat.vectors[0].private_b64;
    // Splice a '\n' and a '\r' into the middle — Go ignores both; the canonical output carries neither.
    const withNewlines =
      canonical.slice(0, 16) + '\n' + canonical.slice(16, 32) + '\r' + canonical.slice(32);
    expect(withNewlines).not.toBe(canonical);
    expect(parseAndNormalize(withNewlines)).toBe(canonical);
  });

  // A space-laden key is REJECTED: Go's StdEncoding skips ONLY '\n'/'\r', never a space (or tab). This
  // pins the asymmetry that the BLOCKER fix introduced — space is not whitespace the decoder tolerates.
  it('throws on a space-laden key (Go StdEncoding rejects space)', () => {
    const canonical = kat.vectors[0].private_b64;
    const withSpace = canonical.slice(0, 16) + ' ' + canonical.slice(16);
    expect(() => parseAndNormalize(withSpace)).toThrow();
  });

  // An already-canonical key round-trips to itself (idempotence), and the normalized output still derives
  // the same public key as the original — the air-gap case-a invariant that normalize-then-derive is
  // stable.
  it('is idempotent on an already-canonical key and preserves the derived public key', () => {
    const canonical = kat.vectors[0].private_b64;
    expect(parseAndNormalize(canonical)).toBe(canonical);
    expect(derivePublic(parseAndNormalize(canonical))).toBe(kat.vectors[0].public_b64);
  });
});
