# Plan 4.2 — Phase 2b: enrollment + per-node mTLS cert issuance

Parent: [plan-4-2026_06_08.md](plan-4-2026_06_08.md) · Prereq: 4.1 (persistence) merged. Second of
four Phase 2 sub-plans. **Controller-side crypto + Store layer only** — the HTTP surface and the
agent-side `enroll` subcommand are 4.3 (they need the HTTP transport).

## Goal

The enrollment ceremony, fully CI-testable at the function level with **stdlib crypto only**: a
single-use, short-TTL, node-scoped token authorizes a node; the controller verifies proof-of-possession
of the node's **mTLS** key, issues a per-node mTLS client cert from a dev controller-CA, records the
node's **WG public key** + cert fingerprint, and burns the token atomically.

## Key crypto fact (adopted)

WireGuard keys are **Curve25519 (X25519, a DH key)** — they **cannot produce signatures**. So
proof-of-possession is over the **mTLS keypair**, not the WG key: the agent's CSR is self-signed
(`x509.CreateCertificateRequest` signs with the mTLS private key), and verifying that signature IS the
PoP. The WG **public** key is registered alongside (used to render the node's overlay config); it is
NOT separately PoP'd — a node that registers a wrong WG public key simply produces a non-functional
overlay (its peers expect a different key), which is self-defeating, not a controller compromise. This
is documented honestly in `enrollment.md`. mTLS keys + the CA use **Ed25519** (stdlib `crypto/ed25519`
+ `crypto/x509`, consistent with `bundlesig`).

## Store extension (I write the interface; impls in the workflow)

Add to the `Store` interface (`internal/controller/store.go`) + both impls:
- `EnrollmentToken` type: `TokenHash` (hex SHA-256 of the plaintext — the plaintext is NEVER stored),
  `NodeID`, `ExpiresAt`, `ConsumedAt *time.Time`.
- `CreateEnrollmentToken(ctx, t, EnrollmentToken) error`.
- `ConsumeEnrollmentToken(ctx, t, tokenHash, nodeID string, now time.Time) error` — **atomic**: errors
  with `ErrTokenInvalid` (unknown hash / wrong node / expired) or `ErrTokenConsumed` (already burned),
  else marks `ConsumedAt=now` and returns nil. One-shot.

## Implementation (`internal/controller/enrollment.go` + extensions)

1. **Token helper:** `NewEnrollmentToken(nodeID, ttl) (plaintext string, EnrollmentToken)` — crypto/rand
   token, `TokenHash = hex(sha256(plaintext))`, `ExpiresAt = caller-now + ttl`. Operator stores the
   record (hashed) via `CreateEnrollmentToken`; the plaintext is handed out-of-band to the node and
   never persisted.
2. **Dev controller-CA:** `type DevCA`, `NewDevCA(tenant TenantID, ttl) (*DevCA, error)` — generates an
   **ephemeral** Ed25519 CA key + self-signed CA cert (key never persists → bounds breach surface,
   per design decision; on restart nodes re-enroll). `(*DevCA).IssueClientCert(csrDER []byte, nodeID
   string, ttl) (certPEM []byte, fp string, err error)` — parse CSR, **verify its self-signature
   (PoP)**, require CN == `<tenant>:<nodeID>`, issue a client cert (ExtKeyUsageClientAuth) signed by
   the CA, return cert PEM + SHA-256 fingerprint. `(*DevCA).CAPoolPEM()` for 4.3's `ClientCAs`.
3. **Enroll ceremony:** `Enroll(ctx, store Store, ca *DevCA, t TenantID, req EnrollRequest, now time.Time)
   (EnrollResult, error)` where `EnrollRequest{NodeID, CSRDER []byte, WGPublicKey string}`:
   `ConsumeEnrollmentToken` (atomic burn) → `IssueClientCert` (PoP + CN check) → `UpsertNode`(Status
   approved, WGPublicKey, MTLSCertFP, EnrolledAt=now) → `AppendAudit`("enroll", nodeID) → return
   `{ClientCertPEM, CACertPEM, Fingerprint}`. Any step failing aborts before the node is approved
   (token already burned on a post-burn failure — single-use is the safety property; document it).

## Tests (`internal/controller/enrollment_test.go` + token cases in the compat suite)

- Token: create→consume happy; consume unknown/expired/wrong-node → `ErrTokenInvalid`; double-consume →
  `ErrTokenConsumed`; both Store impls (extend the compat suite).
- DevCA: issue a cert from a real Ed25519 CSR, verify it chains to the CA (x509 Verify with the CA
  pool, ExtKeyUsageClientAuth); a CSR with a bad self-signature is rejected; a CN mismatch is rejected.
- Enroll: happy path approves the node (GetNode shows approved + WG pubkey + cert FP) and writes an
  audit entry; bad token / bad CSR / CN mismatch refused; a burned token cannot re-enroll.

## Spec `docs/spec/controller/enrollment.md`

The ceremony, the token contract (single-use/TTL/node-scoped, stored hashed, atomic burn), the
**mTLS-PoP-not-WG-PoP** honesty note (Curve25519 can't sign), the ephemeral-CA breach-bounding
rationale, and what 4.3 adds (the HTTP `/enroll` endpoint + the agent `enroll` subcommand). README index.

## Definition of done

- [ ] CI green; stdlib crypto only; no new go.mod dep. Compiler/renderer/air-gap untouched.
- [ ] Token single-use + TTL + node-scope enforced atomically on both Store impls.
- [ ] CSR-PoP verified; per-node cert issued + fingerprint recorded; full Enroll path tested.

## Out of scope (4.3 / 4.4 / Plan 5)

The HTTP `/enroll` endpoint, mTLS termination, the agent `enroll` subcommand (4.3); the frontend
enrollment UX + manual approval queue (4.4; v1 auto-approves on valid token+PoP); a persistent/real CA
or KMS-backed CA, OCSP/CRL (Plan 5; revocation is overlay eviction).
