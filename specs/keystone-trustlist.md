# Keystone trust-list (off-host signing)

<!-- last-verified: 2026-06-15 -->
<!-- 2026-06-15 (extensible-i18n closeout): error responses now coded via the internal/apierr envelope {error:{code,message,params}} — English-default message + panel-localized by error.<code>; no endpoint/flow change. -->

## Responsibility
Define, canonically encode, and fail-closed-verify the off-host-signed membership manifest (trust list) that binds each node's WireGuard key and bundle digest under a monotonic epoch, so a breached controller can stage but never promote or serve content a node will trust.

## Files
- `internal/trustlist/types.go:40-127` — wire/domain types: `Member` (node_id, wg_public_key, bundle_sha256), `TrustList` (schema_version, tenant, epoch, members), `Alg` constants (`ed25519`, `webauthn-es256`, `webauthn-eddsa`), `SignedTrustList` detached artifact, `PinnedCredential` trust anchor, `Signer`/`Verifier` interfaces.
- `internal/trustlist/canonical.go:26-67` — `Canonical(tl TrustList) ([]byte, error)`: sort-by-NodeID, duplicate rejection, HTML-escaping-off JSON, one trailing `\n`; `Challenge(tl) ([]byte, error)` = SHA-256 of canonical bytes (the WebAuthn content-binding challenge).
- `internal/trustlist/verify.go:14-86` — sentinel errors; `Verify(tl, art, pin) error`, the single node-embedded verifier dispatching on `pin.Alg` (never `art.Alg`); raw Ed25519 branch via `bundlesig.Verify`.
- `internal/trustlist/webauthn.go:28-233` — stdlib-only FIDO2 assertion verification: content-bound `verifyWebAuthn` (challenge = `Challenge(tl)`), challenge-agnostic exported `VerifyAssertion(art, pin, challenge)` (reused by passkey login, see specs/panel-auth.md), `AssertionChallenge` extractor, and the 10-step core `verifyAssertion` (webauthn.go:111-219).
- `internal/trustlist/ed25519.go:20-55` — `Ed25519Signer`: software on-host signer over `Canonical(tl)`, dev/CI only.
- `internal/trustlist/pins.go:17-57` — `ParseEd25519PinPEM` / `ParseES256Pin` (PKIX PEM → key; P-256 curve enforced).
- `internal/controller/compile.go:62-103,172-177,229-256,288-342` — keystone half of stage: per-node `bundleSHA256 = hex(sha256(checksums.sha256))`, membership comparison, `stageManifest` (builds + stores the UNSIGNED staged manifest with the monotonic epoch).
- `internal/controller/compile.go:364-437` — `PromoteStaged(ctx, store, t) (int64, error)`: the promote signature gate; `pinFromOperatorCredential` builds the verifier anchor from the stored credential.
- `internal/controller/store.go:139-157,409-421` — `OperatorCredential` (public half only) and `StoredTrustList{TrustListJSON, SignatureJSON, Epoch}` persisted via `Set/GetOperatorCredential`, `Put/GetCurrentSignedTrustList` (see specs/controller-store.md).
- `internal/api/handler_controller.go:215-217,415-437,1075-1318` — operator endpoints: `POST /operator-credential` (pin → keystone ON), `GET /trustlist` (staged canonical bytes, base64 + epoch), `POST /trustlist-signature` (substitution guard + `trustlist.Verify` + store signature).
- `internal/api/handler_controller.go:530-549` — `/config` appends `trustlist.json`/`trustlist.sig` to the SERVED file map when keystone is ON (see specs/controller-agent-api.md).
- `frontend/src/lib/webauthn.ts:210-291,315-336,370-417` — off-host signer entry: `enrollOperatorCredential(rpId, origin)` (create() ceremony, SPKI→PEM, ES256/EdDSA only) and `signManifest(manifestBytes, credentialId, alg, rpId, publicKeyPEM): Promise<SignedTrustList>` over `runAssertion`.
- `frontend/src/stores/controllerStore.ts:636-733` — panel flow driving the ceremony inside deploy (see specs/panel-deploy-fleet.md).

## Inputs
- **Stage time** (see specs/controller-stage-promote.md): the enrolled-subgraph render output — each staged node's `files["checksums.sha256"]` bytes and registry WG public key (internal/controller/compile.go:229-236) — plus the prior `StoredTrustList` for the epoch rule (compile.go:303-317).
- **Sign time**: `GET /trustlist` returns the staged canonical bytes (handler_controller.go:1192-1215); the panel base64-decodes them and calls `signManifest`, which signs challenge = raw SHA-256 of those bytes via `navigator.credentials.get()` (webauthn.ts:327-336).
- **Verify time**: `(TrustList, SignedTrustList, PinnedCredential)` triplets — from `POST /trustlist-signature` (handler_controller.go:1289), from the promote gate (compile.go:402), and from the agent's offline `VerifyMembership` (internal/agent/verify.go:247-305, see specs/agent.md).
- **Trust anchor provisioning**: `POST /operator-credential` with `{alg, credential_id, public_key_pem, rpid, origin}` (handler_controller.go:1134-1185), produced by `enrollOperatorCredential`.

## Outputs
- `Canonical(tl)` bytes — simultaneously the `trustlist.json` artifact served to agents and the exact signed payload (canonical.go:11-15).
- `SignedTrustList` JSON (`trustlist.sig`) — detached signature artifact stored in `StoredTrustList.SignatureJSON` (handler_controller.go:1296-1308) and served alongside the bundle at `/config` (handler_controller.go:544-545).
- `Verify(...) error` — nil means "trust"; consumed by the controller's promote gate and sign endpoint, and by the agent before apply (specs/agent.md).
- New generation int64 from `PromoteStaged` once the gate passes (compile.go:406).
- `VerifyAssertion` + `AssertionChallenge` exports — reused by operator passkey login with a random nonce instead of a content hash (webauthn.go:38-81; specs/controller-operator-api.md, specs/panel-auth.md).

## Decision points
- **Keystone ON/OFF**: ON iff `GetOperatorCredential` finds a pinned credential. OFF → stage builds no manifest (compile.go:172-177), promote runs ungated (compile.go:367-370), `/config` serves no trust-list files, agent `VerifyMembership` is a no-op (agent/verify.go:248-251). Opt-in by construction.
- **Algorithm dispatch**: always on `pin.Alg`; `art.Alg != pin.Alg` → `ErrAlgMismatch` before any cryptography (verify.go:51-66). Only Ed25519/ES256/EdDSA exist; RS256 etc. → `ErrUnsupportedAlg`. The panel mirrors this by offering only COSE -7/-8 at create() (webauthn.ts:49-50,249-252).
- **Monotonic epoch rule**: staged manifest reuses the prior epoch iff the node_id → (wg_public_key, bundle_sha256) map is identical; otherwise prior+1; 0 when no manifest was ever stored (compile.go:288-317). Because BundleSHA256 is in the tuple, any install.sh/config change advances the epoch.
- **Promote gate**: keystone ON requires (a) a staged manifest exists, (b) `SignatureJSON` non-empty, (c) `trustlist.Verify` passes against the pinned credential — each failure is a distinct descriptive error (compile.go:374-404).
- **Substitution guard at sign time**: submitted `trustlist_json` must byte-equal the server's staged canonical bytes or `POST /trustlist-signature` returns 409 (handler_controller.go:1264-1273).
- **WebAuthn assertion checks** (webauthn.go:111-219): empty pin RPID rejected; authData ≥ 37 bytes; type must be `webauthn.get`; challenge must equal base64url(expected); rpIdHash must equal SHA-256(pin.RPID); User-Present flag required; origin check is ADVISORY; ES256 verifies DER over SHA-256(authData‖SHA-256(clientDataJSON)), EdDSA verifies the unhashed concatenation.

## Invariants
- The signed payload is always `Canonical(tl)`, never the raw distributed file; a consumer acting on membership must assert canonical-byte equality first (the Verify caller contract, verify.go:44-50; enforced by the agent at agent/verify.go:286-292 and shipped as-canonical by stageManifest, compile.go:325-340).
- The verifier trusts ONLY the out-of-band `PinnedCredential`; the artifact's `public_key`/`credential_id` fields are audit-only and never used cryptographically (types.go:84-99; webauthn.ts:409-410). Aligns with PRINCIPLES.md "Key custody" — only public halves are ever stored (store.go:131-145).
- Staging never requires a signature; promoting (and agent apply) always does when keystone is ON (compile.go:122-124,344-350) — the deploy-time chokepoint.

## Gotchas
- `TrustList` deliberately has NO timestamp field: `time.Now()` would make canonical bytes non-deterministic and break the GET-sign-POST round-trip; freshness lives in Epoch + per-member BundleSHA256 (types.go:50-54).
- `trustlist.json`/`trustlist.sig` are APPENDED to the served `/config` file map, never exported into the bundle or its `checksums.sha256` — the manifest binds that very digest, so it cannot live inside it (compile.go:199-201,30-32; handler_controller.go:530-545).
- The controller's promote gate verifies only the SIGNATURE over the staged manifest; it does NOT re-derive bundle digests. The agent is the authoritative chokepoint, re-deriving `hex(sha256(checksums.sha256))` offline and matching its own member entry (compile.go:358-363; agent/verify.go:232-238). The WebAuthn signature counter is intentionally not checked — synced passkeys emit 0 (webauthn.go:18-21,163-164).

Deep docs: docs/spec/controller/signing.md (tier-1 bundle signing vs. the keystone, see specs/artifacts-signing.md), docs/spec/security/security.md, docs/spec/controller/key-custody.md. Verify any persistence claims against live code (known drift in docs/spec/controller/persistence.md).
