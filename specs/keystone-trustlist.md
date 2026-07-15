# Keystone trust-list (off-host signing)

<!-- last-verified: 2026-07-16 -->
<!-- 2026-06-15 (extensible-i18n closeout): error responses now coded via the internal/apierr envelope {error:{code,message,params}} — English-default message + panel-localized by error.<code>; no endpoint/flow change. -->

## Responsibility
Define, canonically encode, and fail-closed-verify the off-host-signed membership manifest that binds every authorized node ID to its WireGuard public key and exact bundle digest under a monotonic epoch. A controller can stage or serve arbitrary bytes, but an agent with a pinned keystone refuses to apply bytes that lack a valid signature, canonical manifest, matching member digest, and non-rollback epoch.

The keystone is an asymmetric public-key trust anchor, not a claim that every browser credential is hardware-bound. Browser WebAuthn enrollment validates a first-party UP+UV ceremony before a new public credential is stored; with attestation disabled, it does not establish authenticator provenance or key non-exportability.

## Files
- `internal/trustlist/types.go:26-127` — `Member`, `TrustList`, supported `Alg` values, detached `SignedTrustList`, trusted `PinnedCredential`, and signer/verifier interfaces.
- `internal/trustlist/canonical.go:11-67` — deterministic JSON: copied/sorted members, duplicate-node rejection, HTML escaping disabled, and exactly one trailing newline; `Challenge` is SHA-256 of those canonical bytes.
- `internal/trustlist/verify.go:11-90` — fail-closed `Verify`, sentinel errors, algorithm dispatch on the pinned credential, and the raw-Ed25519 branch.
- `internal/trustlist/webauthn.go:13-269` — stdlib-only WebAuthn assertion verification: content-bound `Verify`, random-challenge `VerifyAssertion`, enrollment-only `VerifyUserVerifiedAssertion`, challenge extraction, RP/origin/UP checks, and ES256/EdDSA signature verification.
- `internal/trustlist/ed25519.go:13-55`, `pins.go:12-57` — development/CI raw-Ed25519 signer and PKIX PEM parsers (including P-256 enforcement for ES256).
- `internal/controller/compile_stage.go:21-40,149-166,216-304` + `keystone.go:18-27,125-225` — stage-time digest calculation and unsigned manifest creation, including the staged-history monotonic epoch rule.
- `internal/controller/trustlist_sign.go:23-100` — tenant-op-locked substitution guard, signature verification, and signed staged-manifest write-back.
- `internal/controller/compile_promote.go:12-70` + `storecore_stage.go` — keystone-aware promote gate, durable exact-candidate seal, and staged-bundle/signed-manifest flip into the served slot.
- `internal/controller/store.go:243-293,666-698` — public-only `OperatorCredential`, staged/served `StoredTrustList` records, atomic served-config snapshot, and store interface.
- `internal/controller/keystone_transition.go` — tenant-locked credential CAS plus durable
  `PendingKeystoneTransition` reconciliation, including exact-once audit recovery and the
  status-read healing boundary.
- `internal/api/handler_keystone.go:63-270,290-359` + `handler_webauthn_enrollment.go:22-122` — keystone status/pin/rotation, ten-minute enrollment challenge/proof, staged manifest GET, and signature POST.
- `internal/api/handler_agent.go:87-142` + `internal/agent/verify.go:234-420` — append the served trust-list outside the checksummed bundle, then perform canonical/signature/digest/membership/epoch verification before apply.
- `frontend/src/lib/webauthn.ts:211-379,391-465` — candidate creation, enrollment proof, content-bound manifest signing, and shared assertion construction.
- `frontend/src/stores/controller/keystone.ts:29-253` + `deploy.ts:260-303` — volatile candidate retry/recovery, server-authoritative keystone hydration, and stage → sign → submit → promote panel flow.

## Inputs
- **Stage:** each enrolled node's rendered `checksums.sha256` bytes and registry WireGuard public key. The member digest is `hex(sha256(checksums.sha256))`; unchanged nodes remain in the manifest even when their bundle is delta-skipped (`internal/controller/compile_stage.go:216-255`; `internal/controller/keystone.go:18-27,125-150`).
- **Sign:** `GET /trustlist` returns standard-base64 canonical bytes plus epoch. The browser decodes the bytes and uses `SHA-256(canonical)` as the raw WebAuthn challenge; `POST /trustlist-signature` echoes the canonical bytes with the detached assertion (`internal/api/handler_keystone.go:290-333`; `frontend/src/lib/webauthn.ts:338-379`).
- **Verify:** `(TrustList, SignedTrustList, PinnedCredential)` at signature installation and promote, and the equivalent out-of-band `MembershipConfig` at the agent (`internal/controller/trustlist_sign.go:42-99`; `internal/controller/compile_promote.go:37-70`; `internal/agent/verify.go:234-295`).
- **WebAuthn trust-anchor provisioning:** authenticated `POST /webauthn/enrollment/begin` issues one `keystone`-purpose nonce; first pin or acknowledged rotation submits `{alg, credential_id, public_key_pem, rpid, origin, enrollment_proof}`. The server verifies the proof against the exact candidate before atomically storing the public descriptor. Raw Ed25519 retains its non-browser path (`internal/api/handler_webauthn_enrollment.go:39-120`; `internal/api/handler_keystone.go:145-270`).

## Outputs
- `Canonical(tl)` bytes, used both as the distributed `trustlist.json` and as the signed payload (`internal/trustlist/canonical.go:11-52`).
- Detached `SignedTrustList` JSON stored in the staged record, then copied to the served record on a valid promote and emitted as `trustlist.sig` (`internal/controller/trustlist_sign.go:86-99`; `internal/controller/storecore.go:472-501`; `internal/api/handler_agent.go:117-142`).
- A promoted generation only after the keystone gate succeeds; keystone-off tenants retain the ungated compatibility path (`internal/controller/compile_promote.go:20-70`).
- `VerifyAssertion`, `VerifyUserVerifiedAssertion`, and `AssertionChallenge` for random-nonce login/enrollment callers without duplicating the WebAuthn signature core (`internal/trustlist/webauthn.go:43-115`).

## Decision points
- **Keystone on/off:** a stored `OperatorCredential` turns it on. With no credential, stage creates no trust-list and promote delegates directly to the store. With a credential, stage writes an unsigned manifest, signing installs a verified signature, promote re-verifies it, and `/config` serves the last-promoted signed record (`internal/controller/compile_stage.go:149-160,296-304`; `internal/controller/compile_promote.go:37-70`; `internal/api/handler_agent.go:122-136`).
- **Algorithm dispatch:** the artifact's `alg` must equal the pinned `alg`, and dispatch is always on the pin. Only raw Ed25519, WebAuthn ES256, and WebAuthn EdDSA are supported; `VerifyAssertion` accepts only the two WebAuthn algorithms (`internal/trustlist/verify.go:37-69`; `internal/trustlist/webauthn.go:43-65`).
- **Monotonic epoch:** compare the new node-ID → `(wg_public_key,bundle_sha256)` map with the prior staged manifest. Identical membership reuses its epoch; any difference increments it; the first manifest starts at zero. Chaining from staged history preserves monotonicity across unpromoted re-stages (`internal/controller/keystone.go:29-65,125-195`).
- **Signature installation:** the current staged bytes are re-read under the per-tenant operation lock. Submitted bytes must match exactly, the signature must verify against the current pin, and canonical bytes/epoch remain unchanged on write-back (`internal/controller/trustlist_sign.go:23-100`).
- **Promote:** keystone-on promotion requires a present, non-empty, parseable signature that verifies against the current pin. The Store then independently requires the exact sealed next generation/node set and the same manifest hash/epoch; loose or partial records cannot promote after a restart. This controller check is early defense in depth; it does not re-derive bundle digests (`internal/controller/compile_promote.go:12-70`; `internal/controller/storecore_stage.go`).
- **Generic WebAuthn assertions:** require a non-empty RPID; at least 37 bytes of authenticator data; `type == "webauthn.get"`; exact challenge and RP-ID hash; UP; optional-origin equality when the pin contains an origin; and a valid ES256 or EdDSA signature. The signature counter is intentionally ignored for synced passkeys (`internal/trustlist/webauthn.go:118-254`).
- **Enrollment-only UV:** `VerifyUserVerifiedAssertion` first requires the assertion's credential ID to equal the candidate's, then performs the generic checks and additionally requires the signed UV flag. Login, keystone signing, promote verification, and node verification deliberately remain on the generic UP+signature path so existing credentials and deployed fleets do not acquire a retroactive requirement (`internal/trustlist/webauthn.go:68-95`).
- **Challenge lifecycle:** an enrollment nonce is 32 random bytes, valid for ten minutes, stored as a hash, and scoped to `purpose+actor`. Beginning again replaces that subject's prior live nonce and purges expired records. Proof verification precedes atomic consume, so a malformed or UP-only attempt does not destroy the nonce; concurrent valid submissions can perform crypto, but only one consumes it (`internal/api/handler_webauthn_enrollment.go:22-24,39-120`; `internal/controller/storecore.go:655-718`).
- **Pin transition concurrency and durability:** the handler classifies first pin/idempotent
  pin/rotation from a snapshot, then `CompareAndSetKeystoneCredential` serializes it against
  stage/sign/promote and requires that exact state in the Store CAS. Audited transitions first persist
  expected/next plus a fixed audit `EventID`; reconciliation appends only after the target is current
  and clears the marker only after observing that exact event. A concurrent change returns a dedicated
  conflict instead of silently changing the transition's meaning. Exact idempotent re-pins are
  compare-only, but still reconcile any older marker before returning. (`internal/api/handler_keystone.go`;
  `internal/controller/keystone_transition.go`; `internal/controller/storecore.go`.)

## Invariants
- A node acts on exactly the canonical bytes that were signed: it recomputes `Canonical(parsed)` and requires byte equality with received `trustlist.json` before verifying or using membership (`internal/trustlist/verify.go:48-54`; `internal/agent/verify.go:320-354`).
- The verifier trusts only the out-of-band pinned credential. `SignedTrustList.public_key` and `credential_id` are informational in generic signature verification; the former is never a trust input. Enrollment separately matches credential ID to prevent proof splicing (`internal/trustlist/types.go:80-112`; `internal/trustlist/webauthn.go:75-82`).
- Staging is allowed without a signature; keystone-on promotion and agent apply are fail-closed without a valid one (`internal/controller/compile_stage.go:21-40`; `internal/controller/compile_promote.go:46-70`; `internal/agent/verify.go:295-318`).
- Manifest and bundle staging share one seal-last commit marker. Signature installation can alter only the detached signature over the same sealed canonical bytes/epoch; a clean restage is required to recover any unsealed partial candidate (`internal/controller/storecore_stage.go`; `internal/controller/trustlist_sign.go:56-99`).
- Staged and served trust-list slots are distinct. `/config` reads the promoted bundle and served manifest in one store snapshot, so an in-process concurrent promote cannot expose an old-bundle/new-manifest pair (`internal/controller/store.go:259-293,680-698`; `internal/api/handler_agent.go:87-136`).
- Enrollment UV proves that the submitted assertion reported user verification for that one ceremony. It does not turn UV into a permanent credential property or a requirement on later signatures (`internal/trustlist/webauthn.go:23-25,68-95`).
- Credential-status GET is a recovery boundary. If a pin/rotation POST committed the credential but
  failed before reporting a durable audit append, the read completes the pending exact event before
  returning server truth. This supports the panel's candidate-matches-status recovery path without a
  second mutating request or a permanently missing audit (`internal/api/handler_keystone.go`;
  `internal/controller/keystone_transition.go`).

## Gotchas
- `TrustList` deliberately has no timestamp. Freshness comes from the monotonic epoch and per-member bundle digest; adding wall-clock data would make an otherwise identical GET/sign/POST payload nondeterministic (`internal/trustlist/types.go:46-60`).
- `trustlist.json` and `trustlist.sig` are appended to the served `/config` file map, never included in the bundle or its `checksums.sha256`; the manifest binds the hash of that checksum file and cannot recursively live inside it (`internal/controller/compile.go:22-32`; `internal/api/handler_agent.go:117-136`).
- The controller's promote gate verifies the signature but not every staged bundle digest. The agent is the authoritative application chokepoint: it recomputes its checksum-file digest, locates its signed member entry, verifies peers, and enforces the epoch floor (`internal/controller/compile_promote.go:26-31`; `internal/agent/verify.go:366-420`).
- The atomic served snapshot is an in-process guarantee. FileStore persists bundle and served-manifest records as separate atomic renames, so a process crash during promote can leave a transient cross-file mismatch. That shape is fail-closed at the agent's digest check and a repeated promote repairs it; generation commits last so pollers are not deliberately awakened to a half-finished flip (`internal/controller/store.go:271-287`; `internal/controller/storecore.go:488-501`).
- Origin equality is enforced when an origin is present in the pin, but on a node it is only the stored enrollment claim; the node cannot independently prove which browser origin produced a signature months later. RPID/challenge/signature binding remains cryptographic (`internal/trustlist/webauthn.go:142-153,181-208`).
- UV and credential copy/sync are independent. YAOG neither checks the WebAuthn BE/BS flags nor uses attestation (`attestation: "none"`), so the enrollment ceremony cannot establish that a key is hardware-backed or non-exportable, and cannot prevent a custom authenticated client from submitting a software key with synthetic-but-valid assertion data. The panel warns on both login-passkey and keystone enrollment surfaces (`frontend/src/lib/webauthn.ts:241-268,303-334`; `frontend/src/components/deploy/WebAuthnEnrollmentNotice.tsx:4-11`, `PasskeySettings.tsx:67`, `DeployBar.tsx:219`; `frontend/src/i18n/messages/en.ts:687`; `frontend/src/components/deploy/PasskeySettings.test.tsx:69-95`).
- Ordinary login and manifest signing still request `userVerification: "preferred"`, while enrollment proof requests `"required"`. The server accepts later UP-only assertions by design to avoid locking out existing users or rejecting manifests already valid under historical credentials (`frontend/src/lib/webauthn.ts:303-334,358-379,391-465`; `internal/trustlist/webauthn.go:68-95`).

Deep docs: `docs/spec/controller/signing.md`, `docs/spec/security/security.md`, and `docs/spec/controller/key-custody.md`.
