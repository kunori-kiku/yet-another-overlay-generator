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

### Signer seam and operation snapshot (`ConfigSigner`)

The render, anchor, and export layers do not reload signing state independently. A production
compile/stage/preview/export operation resolves one `bundlesig.ConfigSigner` snapshot and threads
that same interface through every phase:

- `ConfigSigner.Sign(message []byte) ([]byte, error)` — a raw 64-byte Ed25519 detached signature
  over the canonical `checksums.sha256` bytes.
- `ConfigSigner.PublicKeyPEM() []byte` — the PKIX public-key PEM pinned into `install.sh`.

The default backend is the in-process Ed25519 key `*Signing` loaded from
`YAOG_BUNDLE_SIGNING_KEY`. `LoadConfigSignerFromEnv` returns an **explicit nil interface** when
signing is off (never a typed-nil `*Signing`), so call-site `!= nil` checks stay correct. Legacy
entry points retain an environment-loading shim, while production callers use explicit-signer
variants. This prevents a key-file rotation between render and export from embedding one public key
in `install.sh` but signing with another. The interface lives in `bundlesig` (stdlib-only), so a
future **host-isolated backend** — HashiCorp
Vault / OpenBao transit (stdlib REST), GCP Cloud KMS, or a YubiHSM, all Ed25519-capable so the
node-side `openssl` verify path stays unchanged — plugs in by implementing the same interface in
its own package, with no change to the call sites. The `Sign` error return exists for such
networked backends; the in-process signer's error is always nil. The off-controller keystone
separately requires an operator-held signature over trust-list membership: a WebAuthn assertion for
the browser path or a raw Ed25519 signature for the CLI-compatible path. The controller persists
only that credential's non-secret descriptor/public key and receives signed proof rather than
credential private-key material, which bounds a controller-only takeover. YAOG does not collect
WebAuthn attestation or prove that a browser credential is hardware-backed or non-exportable;
software and synced credentials may be copied by their provider. Within that threat model, this
tier-1 bundle key needs exfiltration resistance, so the software default (protected at rest by
`systemd-creds` / `0600`) is proportionate and a remote KMS is strictly optional.

## Opt-in configuration

Signing is controlled by one environment variable resolved once at operation start:

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

## Persisted signing anchor (controller — no silent downgrade)

If a later controller operation **drops or swaps** `YAOG_BUNDLE_SIGNING_KEY`, it could otherwise
silently revert a previously signed fleet to hash-only (or to a different key) — a downgrade the
system detects rather than ignores. The **private** key
stays off the controller's persisted state by design (a server-state compromise must not be able to
lift it; `internal/bundlesig`), but the **public** key is not secret, and persisting it strengthens
the model rather than weakening it.

So the controller (`controller.enforceSigningAnchor`, run inside `CompileAndStage` before any bundle
is produced) pins the signing **public** key per tenant as a non-secret `SigningAnchor`
(FileStore `signing-anchor.json`) and reconciles it on every stage:

| persisted anchor | `YAOG_BUNDLE_SIGNING_KEY` | result |
|---|---|---|
| none | present | **pin** the pubkey (trust-on-first-use), sign |
| none | absent  | never-signed fleet — allowed (hash-only, back-compat) |
| present | present, **same** pubkey | normal signed stage |
| present | **absent** | refuse — `CodeSigningKeyMissing` (412); would silently downgrade to unsigned |
| present | present, **different** | refuse — `CodeSigningKeyMismatch` (409) |

**Rotation / recovery:** `YAOG_BUNDLE_SIGNING_KEY_ROTATE` (truthy) lets one stage RE-PIN the anchor to
the current key — an intentional rotation, or recovery after the prior key was lost. Set it for one
deploy, then **unset** it (leaving it on disables the change-detection guard). This is controller-only:
the air-gap export path (`cmd/compiler`) has no persistent state, so it is unchanged —
it still signs iff the env key is set.

## At-rest protection of the signing key (operator requirement)

The file at `YAOG_BUNDLE_SIGNING_KEY` is a **private** Ed25519 key. Anyone who can read it can forge
config bundles that pass the `install.sh` signature check, so it MUST be protected like any private
key. The off-controller keystone bounds the blast radius — a forged bundle alone still cannot alter
the credential-signed membership a node verifies — so this tier-1 key needs **exfiltration
resistance**, not HSM-grade isolation. For the browser path, this statement assumes the operator's
WebAuthn provider/account is not also compromised; YAOG does not attest that the credential is
hardware-backed or non-exportable. Concretely:

- **Permissions.** Own the file by the controller's service user and `chmod 600` (or `400`). It must
  never be group- or world-readable. Treat a key that has been on a loosely-permissioned path as
  compromised and rotate it (re-key, re-export, re-pin the public key into `install.sh`).
- **Prefer keeping the plaintext off persistent disk — systemd.** Use
  [`systemd-creds`](https://www.freedesktop.org/software/systemd/man/systemd-creds.html): encrypt the
  key once (`systemd-creds encrypt key.pem key.cred`), reference it with `LoadCredential=` /
  `SetCredentialEncrypted=` in the unit, and point `YAOG_BUNDLE_SIGNING_KEY` at
  `${CREDENTIALS_DIRECTORY}/<name>`. systemd decrypts it into a per-service `0600` tmpfs at start, so
  the cleartext key never lands on durable storage and is scoped to the one service.
- **Containers / compose.** Do not bake the key into the image and do not commit it. Mount it
  read-only with restrictive ownership, or (better) deliver it via the orchestrator's secret store
  (Docker/Swarm secrets, Kubernetes Secrets, a mounted vault agent) rather than a bind-mounted file
  on the host. It is excluded from the repo via `.gitignore`/`.dockerignore`.
- **Loading is fail-closed.** `LoadSigningFromEnv` returns an error (the export aborts) if the path is
  set but unreadable or unparsable, so a misconfigured key never silently ships unsigned bundles.

The long-term removal of the at-rest key entirely is the **KMS/HSM path**: a Vault/OpenBao-transit or
Cloud-KMS backend implements the `ConfigSigner` seam (above) with a sign-only handle, so no private
key material ever lives on the controller host. Until then, the controls above are the requirement.

## New bundle artifacts (when signing is enabled)

Added next to the existing per-node files (see
[../artifacts/export-bundle.md](../artifacts/export-bundle.md)):

- **`bundle.sig`** — `base64(rawSignature)`: the Base64 encoding of the 64-byte Ed25519 signature
  over the `checksums.sha256` canonical bytes. Detached; `checksums.sha256` itself is the signed
  payload.
- **`signing-pubkey.pem`** — `MarshalPublicKeyPEM(pub)`: the PKIX PEM public key, shipped so
  `openssl` (and any operator) can verify `bundle.sig` against `checksums.sha256` out of band, and
  so the install-time verifier has a file form to feed `openssl pkeyutl -pubin`.

Both files are present **only** when signing is enabled. An `install.sh` that was rendered with
signing enabled carries the embedded verifying key and therefore **requires** `bundle.sig` (see
below); an `install.sh` rendered without signing has no verify block and uses the hash-only path.

## Install-time verification order

When the script was rendered signed, `install.sh` verifies the signature **before** the existing
`sha256sum -c`, so a recipient never even computes the hash list of an unsigned-by-the-expected-key
bundle:

1. Write the **embedded** verifying public key — a Go-emitted value baked into `install.sh` at
   generation time, *not* the shipped `signing-pubkey.pem` (which an attacker rewriting the bundle
   could swap) — to a temp file.
2. Decode `bundle.sig` (Base64) to a raw 64-byte signature file.
3. `openssl pkeyutl -verify -pubin -inkey <pub.pem> -rawin -sigfile <sig> -in checksums.sha256`
   (`-rawin` makes `openssl` hash-and-verify the file directly under Ed25519's PureEdDSA rules,
   matching `crypto/ed25519`).
4. **Only on success**, run the existing `sha256sum --status -c checksums.sha256` to confirm each
   file matches its signed digest.

**Mandatory signature (downgrade protection).** Because a signed `install.sh` embeds the verifying
key, it *knows* it was signed at generation time. A **missing** `bundle.sig` is therefore treated as
signature-stripping tamper, not as an unsigned bundle: the script **refuses to proceed** rather than
fall through to the bare `sha256sum -c` (which an attacker could satisfy with rewritten files +
rewritten checksums). Likewise, if `bundle.sig` is present but `openssl` is missing or its build
lacks Ed25519 / `-rawin` support (**OpenSSL 3.0+** is required for `pkeyutl -rawin`), the script
**fails loudly with a nonzero exit** — it never silently downgrades to hash-only when a signature
was expected. An `install.sh` rendered **without** signing (opt-in off) has no verify block and
behaves exactly as today: hash-only `sha256sum -c`. Full ordering in
[../artifacts/install-script.md](../artifacts/install-script.md).

## Packaging does not add a second trust object

Current exports package complete per-node directories; the retired self-extracting tar wrapper is
not part of the artifact contract. There is therefore one signed object per node:
`bundle.sig` over the exact `checksums.sha256` bytes. Packaging a directory into a ZIP does not
introduce another signing layer. `deploy-all` uploads the complete directory, and its inner
`install.sh` performs the mandatory signature/checksum gates before host mutation. See
[../artifacts/deploy-scripts.md](../artifacts/deploy-scripts.md).

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

The **real out-of-band pin** is a trust anchor delivered with the agent at install time, independent
of the bundle being verified. The agent verifies against that pinned public credential, and
membership-changing trust-list updates require an operator signature over the exact content — a
WebAuthn assertion or raw Ed25519 signature, according to the pinned algorithm. Only the public
descriptor/key and signed proof cross the controller API; this design does not claim a browser
credential is hardware-backed or non-exportable.
