# Plan 5.1 — off-host signing keystone (WebAuthn-signed trust-list)

Parent: [plan-5-2026_06_08.md](plan-5-2026_06_08.md). The security keystone (user-chosen 2026-06-08):
network trust anchored in the user's **off-host** key, not the controller. Every **membership/trust-list
change** is signed by the user's **WebAuthn** credential (Bitwarden passkeys OR YubiKey) and verified
**offline** by each node, so a breached controller (even holding the operator bearer token) cannot forge
membership or add a rogue peer. Two-tier over Phase-0: tier-1 = automated host-held `bundle.sig`
(integrity); tier-2 = off-host user-signed trust-list (membership authenticity). See memory
[[security-model-keystone]]. Design from the 4-agent design pass (run ws0miv55w).

## Architecture (adopted)

- **`internal/trustlist`** — new stdlib-only package, PEER of `bundlesig` (may import bundlesig one-way
  for the Ed25519 primitives; bundlesig must NOT import trustlist). The membership-authenticity authority.
- **TrustList** = `{SchemaVersion=1, Tenant, Epoch int64 (monotone membership anti-rollback), Members
  []{NodeID, WGPublicKey}, CreatedAt (advisory)}`. `Canonical(tl)` — members sorted by NodeID, duplicate
  NodeID rejected, deterministic JSON (declaration order, no HTML-escape, single trailing LF), bytes ==
  on-disk `trustlist.json`. `Challenge(tl) = SHA256(Canonical(tl))`.
- **SignedTrustList** = `{Alg ("ed25519"|"webauthn-es256"), CredentialID, PublicKey (AUDIT ONLY — node
  verifies against the PINNED credential, never this), Signature, AuthenticatorData?, ClientDataJSON?}`
  → on-disk `trustlist.sig`.
- **Signer** (pluggable): software `Ed25519Signer` (CI/dev — reuses `bundlesig.Sign/Verify`; ON-HOST,
  never the production anchor) + the real **WebAuthn** signer (browser `navigator.credentials.get`; the
  Go side only reconstructs a SignedTrustList from the assertion).
- **Verifier** (the one function nodes embed; offline, fail-closed): dispatch on `Alg` →
  - `verifyEd25519`: `bundlesig.Verify(Canonical(tl), sig, pin.Ed25519Pub)`.
  - `verifyWebAuthnES256` (security-critical, stdlib `ecdsa.VerifyASN1`): signed message =
    `authenticatorData ‖ SHA256(clientDataJSON)`; require `clientData.type=="webauthn.get"`,
    `clientData.challenge == base64url(Challenge(tl))` (content binding), `SHA256(pin.RPID)==authData[0:32]`,
    User-Present flag; verify the ASN.1-DER ECDSA sig vs the **pinned** P-256 key. **Exclude RS256**;
    dispatch on the PINNED alg only (no alg confusion); `base64.RawURLEncoding`; bounds-check authData
    (len≥37); do NOT gate on the signature counter (synced passkeys emit 0); SHA256 the EXACT received
    clientDataJSON bytes (never re-marshal). EdDSA (-8) variant via `ed25519.Verify`.
- **PinnedCredential** delivered out-of-band at agent install (extends the `--pubkey`/`PinnedPubPEM`
  seam): `{Alg, CredentialID, Ed25519Pub | ES256Pub, RPID, Origin}` — stored as PKIX-PEM / raw P-256
  point so the node never parses COSE/CBOR.

## End-to-end flow (two-tier)

1. Controller assembles the **TrustList** from the registry's `NodeApproved` members (NodeID + WGPublicKey).
2. Operator signs it **off-host**: panel `GET /trustlist` (the to-be-signed canonical bytes) → WebAuthn
   ceremony (challenge = `Challenge(tl)`) → `POST /trustlist-signature`. The controller **re-computes**
   the trust-list from its own registry and **rejects** a submitted canonical that doesn't match (closes
   the substitution gap); it stores the signed artifact + epoch.
3. `CompileAndStage`/`Export` embeds `trustlist.json` + `trustlist.sig` into each bundle **before**
   `bundlesig.Canonicalize`, so tier-1 (`checksums.sha256` + `bundle.sig`) also covers them (strip/tamper
   detected).
4. The **agent**, before trusting peers: tier-1 `VerifyBundle` (existing) → tier-2 verify the trust-list
   sig against the **node-pinned operator credential** + assert every `[Peer]`/config peer ∈ the signed
   members + `Epoch` ≥ last-applied (persisted in agent state). Fail-closed: pinned-but-missing/invalid →
   refuse (mirrors the Phase-0 pinned-but-unsigned branch).

## Decomposition (stacked PRs)

- **5.1a — `internal/trustlist` package (THIS slice, fully CI-testable).** Types, Canonical/Challenge,
  Ed25519 signer+verifier (reuse bundlesig), the WebAuthn ES256+EdDSA verifier, PinnedCredential parsing.
  Tests: canonical determinism/order-independence/dup-reject + byte-equality; Ed25519 round-trip/tamper/
  wrong-key; WebAuthn with SYNTHETIC assertions (software ES256/EdDSA key acts as the authenticator) +
  every negative (challenge mismatch, wrong type, UP missing, rpIdHash mismatch, truncated authData,
  flipped sig, alg confusion, RS256 excluded, base64url-vs-std). No browser. Heavy adversarial crypto review.
- **5.1b — controller integration (CI-testable, httptest).** Assemble TrustList from the registry;
  `/trustlist`, `/trustlist-signature` (re-compute+match), `/operator-credential` (pin) endpoints; Store
  methods (Get/SetOperatorCredential, Put/GetCurrentSignedTrustList); embed in bundles before Canonicalize.
- **5.1c — agent verification (CI-testable).** `VerifyMembership` (sig vs pinned cred + peers⊆members +
  epoch anti-rollback, persisted in state); wire `--operator-cred` / `PinnedCredPEM`; gate apply.
- **5.1d — panel WebAuthn ceremony (browser; MANUAL-verified).** Operator-credential enrollment
  (`navigator.credentials.create`, policy: synced-passkey-allowed vs device-bound/UV-required) + signing
  (`navigator.credentials.get`) replacing `controllerStore.requireUserKey()`; step-up on membership-
  changing Deploy/rekey/revoke.

## CORRECTION (2026-06-08, adversarial review of PR #36 — BLOCKER fix)

The membership-only signing of the first 5.1b/c cut was **bypassable**: the off-host signature
covered `trustlist.json` (the member list) but NOT `install.sh` — a controller-controlled byte that
runs as **root** and configures WireGuard. A breached controller (holding the operator token + the
host-held `bundle.sig` key) could append `wg set <iface> peer <rogue>` to `install.sh` and add a peer
absent from the signed members; both `VerifyBundle` and `VerifyMembership` passed. **The off-host
signature must cover what RUNS, not just the membership list.** Owner decision: **sign the bundle per
Deploy.**

Corrected design:
- The signed artifact binds, per member, the node's **bundle digest**: `trustlist.Member` gains
  `BundleSHA256` = `hex(sha256(checksums.sha256))` (checksums.sha256 covers install.sh + every config).
  Drop `CreatedAt` from the canonical bytes (it was a `time.Now()` non-determinism that broke the
  GET→sign→POST round-trip; epoch + bundle digests provide freshness/identity).
- Flow (one off-host tap per Deploy; deploys are operator-initiated clicks, so this is natural and
  strictly more secure): **stage** (render all node bundles + checksums + tier-1 bundle.sig; compute the
  to-be-signed manifest = {epoch, tenant, members[{node_id, wg_public_key, bundle_sha256}]}; store it
  staged) → **GET /trustlist** (the staged manifest canonical) → operator signs off-host → **POST
  /trustlist-signature** (re-derive + byte-match + trustlist.Verify vs the pinned cred; store the sig)
  → **promote** (refuses, keystone-ON, unless a valid off-host sig over the current staged manifest
  exists). `/config` serves `trustlist.json` (manifest) + `trustlist.sig` (off-host) alongside the
  bundle — these live OUTSIDE `checksums.sha256` (they bind the checksums digest, so they cannot be
  inside it).
- Agent: tier-1 `VerifyBundle` (bundle.sig over checksums = file integrity) → tier-2 `trustlist.Verify`
  the manifest vs the **node-pinned** credential → assert `hex(sha256(this bundle's checksums.sha256))
  == this node's member.BundleSHA256` (binds install.sh + all configs to the off-host key) → node ∈
  members → epoch ≥ last-applied. Fail-closed. (This also covers AllowedIPs/endpoints — they are in the
  signed configs.)
- Tradeoff (conscious): a user-key tap at EVERY Deploy, not only membership changes — the spike's
  "routine config = automated" does not apply when deploys are manual operator actions. Documented.

## Honest limits (state in specs)

Operator-credential **pinning bootstrap** is trust-on-pin at install (a compromised install-time pin
delivery defeats it). **No online rotation** in v1 — a lost sole credential is a fleet-lockout; strongly
recommend pinning ≥2 credentials (a backup). **Synced passkeys** widen the TCB (vault + master password).
The software Ed25519 signer is **CI/dev only** (on-host, forgeable). A revoked node keeps its last bundle
until its overlay is evicted (membership removal takes effect on the next signed trust-list + redeploy).

## DoD

- [ ] Per slice: CI green + local `go test`/`npm`; adversarial review (crypto-focused for 5.1a/5.1c).
- [ ] Keystone property holds: a breached controller cannot produce a trust-list that verifies against the
      pinned off-host credential; nodes refuse peers absent from the signed members.
