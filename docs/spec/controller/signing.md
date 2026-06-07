# Bundle Signing (Phase 0 authenticity)

This document defines the **bundle-signing primitive**: a canonical serialization of a per-node
bundle and a detached Ed25519 signature over it. Phase 0 adds *authenticity* (a recipient can prove
the bundle came from the holder of a specific signing key) on top of the existing *integrity*
(tamper-detection via `checksums.sha256`). It is opt-in and back-compatible: with no signing key
configured, bundles are byte-identical to today's hash-only output.

This is the signing primitive **reused by every later controller phase** (split-render, agent-pull,
KMS, stage→promote). Later phases extend *what* is signed (e.g. a bound `{tenant, node, version,
expiry}` header) and *who holds the key* (per-tenant KMS), but the canonical-serialization and
Ed25519 contract defined here are the stable foundation. See
[../../design/controller-panel-design-spike-2026_06_07.md](../../design/controller-panel-design-spike-2026_06_07.md).

## Why a new canonical serialization

The signed object is **not** the compiler's `computeChecksum`
(`internal/compiler/compiler.go`). That value is `fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%v", topology))))`
truncated to 16 hex chars — a `%v` dump of an in-memory Go struct. It is non-canonical (Go map
ordering, struct-field formatting, and `%v` rules are not a stable wire format), truncated, and
covers the *topology object* rather than the *rendered bundle bytes* an operator actually runs.
**Signing over `computeChecksum` would sign the wrong thing and would not be reproducible.** It
exists only for the human-facing manifest `checksum` field and stays there; it is never the signing
input.

Instead, the signing input is a deterministic normalization of the per-node `checksums.sha256`
content, which already enumerates every shipped file (rendered WireGuard / Babel / sysctl configs
**and `install.sh` itself**, per D24). Signing this digest list signs, transitively, every byte of
every file in the bundle: change any file → its SHA-256 changes → the canonical bytes change → the
signature no longer verifies.

## Canonical serialization contract

Implemented by `internal/bundlesig.Canonicalize(files map[string]string) []byte`
(leaf package, stdlib only). Given a map from bundle-relative path to file content:

1. For each `(path, content)` pair, compute `sha256(content)`.
2. Emit one line per file in **`sha256sum` format**:
   `"<64-hex-lowercase-sha256><two spaces><path>\n"`.
3. **Sort lines by `path` in byte (lexicographic) order.**
4. Use **LF (`\n`) only** as the line separator, including a **trailing newline** after the last
   line.

The resulting byte string is **deterministic and order-independent**: the same file set always
produces the same bytes regardless of map-iteration order. This byte string **is exactly the
content of the `checksums.sha256` file** — Phase 0 makes `checksums.sha256` the canonical,
sorted, trailing-LF serializer and signs precisely those bytes. (Before Phase 0, `checksums.sha256`
was assembled in nondeterministic map order with no trailing newline; the format is unchanged from
`sha256sum`'s perspective — two spaces, hex then path — so `sha256sum -c` still consumes it, but it
is now sorted and stable. See [../artifacts/export-bundle.md](../artifacts/export-bundle.md).)

> The two-space separator is the GNU coreutils `sha256sum` convention (a single space would mark
> "text mode" with `*` for binary; two spaces denotes the default mode). Preserving it keeps
> `sha256sum -c checksums.sha256` working unchanged on the node.

## Signature contract

`internal/bundlesig` provides, stdlib `crypto/ed25519` only:

- `Sign(canonical []byte, priv ed25519.PrivateKey) []byte` — a **raw 64-byte** Ed25519 signature
  over the canonical bytes (detached; the canonical bytes are not embedded in the signature).
- `Verify(canonical, sig []byte, pub ed25519.PublicKey) bool` — true iff `sig` is a valid Ed25519
  signature of `canonical` under `pub`.
- `LoadPrivateKeyPEM([]byte) (ed25519.PrivateKey, error)` — parse a PKCS#8 PEM Ed25519 private key.
- `MarshalPublicKeyPEM(ed25519.PublicKey) []byte` — emit a PKIX/SubjectPublicKeyInfo PEM public
  key, the form `openssl` consumes with `-pubin`.

Ed25519 is chosen for being in the Go standard library, deterministic (no per-signature randomness
to mis-handle), small (32-byte keys, 64-byte signatures), and verifiable by stock `openssl` on the
node — no third-party crypto dependency on either side.

## Opt-in configuration

Signing is controlled by a single environment variable read at export time:

- **`YAOG_BUNDLE_SIGNING_KEY`** — filesystem path to an Ed25519 **private** key in PKCS#8 PEM.
  - **Unset or empty** → no signing. Bundles are hash-only, byte-identical to pre-Phase-0 output
    (apart from `checksums.sha256` now being sorted + trailing-LF). This is the default and the
    back-compat / air-gap-unchanged guarantee.
  - **Set** → the key is loaded via `LoadPrivateKeyPEM`; for **every per-node bundle** the exporter
    computes `Canonicalize(bundleFiles)` (the `checksums.sha256` bytes), signs them, and writes the
    two new artifacts below. The corresponding public key (`MarshalPublicKeyPEM`) is also embedded
    into `install.sh` as a Go-emitted constant so a recipient who has only `install.sh` already
    carries the verifying key.

Phase 0's key is **operator-configured and single** (one key for the whole export). Per-tenant keys
and KMS-held sign-only handles arrive in Phase 3; this variable is the Phase 0 stop-gap, not the
long-term custody model.

## New bundle artifacts (when signing is enabled)

Added next to the existing per-node files (see
[../artifacts/export-bundle.md](../artifacts/export-bundle.md)):

- **`bundle.sig`** — `base64(rawSignature)`: the Base64 encoding of the 64-byte Ed25519 signature
  over the `checksums.sha256` canonical bytes. Detached; `checksums.sha256` itself is the signed
  payload.
- **`signing-pubkey.pem`** — `MarshalPublicKeyPEM(pub)`: the PKIX PEM public key, shipped so
  `openssl` (and any operator) can verify `bundle.sig` against `checksums.sha256` out of band, and
  so the install-time verifier has a file form to feed `openssl pkeyutl -pubin`.

Both files are present **only** when signing is enabled. When absent, the bundle is unsigned and
`install.sh` falls back to the hash-only path.

## Install-time verification order

When `bundle.sig` is present, `install.sh` verifies the signature **before** the existing
`sha256sum -c`, so a recipient never even computes the hash list of an unsigned-by-the-expected-key
bundle:

1. Write the embedded/shipped public key to a temp file (`signing-pubkey.pem`).
2. Decode `bundle.sig` (Base64) to a raw 64-byte signature file.
3. `openssl pkeyutl -verify -pubin -inkey <pub.pem> -rawin -sigfile <sig> -in checksums.sha256`
   (`-rawin` makes `openssl` hash-and-verify the file directly under Ed25519's PureEdDSA rules,
   matching `crypto/ed25519`).
4. **Only on success**, run the existing `sha256sum --status -c checksums.sha256` to confirm each
   file matches its signed digest.

If `bundle.sig` is present but `openssl` is missing or its build lacks Ed25519 support, the script
**fails loudly with a nonzero exit** — it never silently downgrades to hash-only when a signature
was expected. If `bundle.sig` is absent (unsigned bundle / opt-in off), the script behaves exactly
as today: hash-only `sha256sum -c`. Full ordering in
[../artifacts/install-script.md](../artifacts/install-script.md).

## Honest limitation — Phase 0 authenticity is relative to an out-of-band pin

Phase 0's authenticity guarantee is **only as strong as the operator's trust in the verifying public
key**, and Phase 0 ships that key *inside the bundle* (`signing-pubkey.pem`, and embedded in
`install.sh`). Therefore:

> If the operator obtains the bundle from an **untrusted source** and trusts whatever public key
> arrives with it, an attacker who can rewrite the bundle can **also swap in their own public key**
> and re-sign with the matching private key. The signature would then verify against the attacker's
> bundled key, and `install.sh` would proceed. In that threat model the signature proves only
> *internal consistency* of the bundle, not provenance.

The signature is genuinely meaningful when the verifying key is pinned **out of band** — the
operator compares `signing-pubkey.pem` (or the embedded constant) against a key fingerprint they
obtained through a separate trusted channel before running `install.sh`. For the **air-gapped /
operator-built** path this holds naturally: the operator built the bundle and configured
`YAOG_BUNDLE_SIGNING_KEY` themselves, so the key is implicitly pinned.

The **real out-of-band pin** — a trust anchor delivered with the agent at install time, independent
of the bundle being verified — arrives in **Phase 1b/3** (the agent verifies against a key pinned at
install, and membership-changing trust-list updates require a human hardware-key signature). Phase 0
deliberately establishes the *signing mechanism* and *verification ordering* so those later phases
only have to harden *where the trust anchor comes from*, not reinvent how signing works.
