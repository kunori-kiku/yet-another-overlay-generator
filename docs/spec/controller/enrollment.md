# Node Enrollment (Phase 2b — single-use token + mTLS PoP + per-node cert issuance)

This document defines the **enrollment ceremony**: how a node moves from an out-of-band token to an
approved registry record holding its WireGuard **public** key and an issued mTLS client cert. It is the
controller-side crypto + `Store` layer (`internal/controller/enrollment.go`, plus the
`EnrollmentToken` methods on the `Store` in [persistence.md](persistence.md)); the HTTP `/enroll`
endpoint, mTLS termination, and the agent `enroll` subcommand are [4.3](#what-43-adds). Everything here
is **stdlib crypto only** (`crypto/ed25519`, `crypto/x509`, `crypto/sha256`, `crypto/rand`,
`encoding/pem`, `encoding/hex`) — no new `go.mod` dependency.

**Scope of Phase 2b (this milestone, plan-4.2).** Controller-side and CI-testable at the function
level: the enrollment-token contract, the dev controller-CA, and the `Enroll` ceremony that ties them
to the `Store`. There is **no HTTP surface** in this sub-plan. See
[../../../implementation_plans/controller-panel-2026_06_08/plan-4.2-2026_06_08.md](../../../implementation_plans/controller-panel-2026_06_08/plan-4.2-2026_06_08.md)
and the parent [plan-4-2026_06_08.md](../../../implementation_plans/controller-panel-2026_06_08/plan-4-2026_06_08.md).

## The ceremony at a glance

```
operator                         node agent                         controller (Enroll)
   │                                  │                                   │
   │ 1. NewEnrollmentToken(node, ttl) │                                   │
   │    store HASH via                │                                   │
   │    CreateEnrollmentToken         │                                   │
   │ 2. hand PLAINTEXT out-of-band ──▶│                                   │
   │                                  │ 3. gen mTLS keypair + self-signed  │
   │                                  │    CSR (CN = tenant:node)          │
   │                                  │    gen WG keypair (public only out)│
   │                                  │ 4. {token, CSR_DER, WG_pub} ──────▶│
   │                                  │                                   │ 5. ConsumeEnrollmentToken
   │                                  │                                   │    (atomic burn)
   │                                  │                                   │ 6. IssueClientCert:
   │                                  │                                   │    verify CSR self-sig (PoP)
   │                                  │                                   │    require CN == tenant:node
   │                                  │                                   │    sign per-node client cert
   │                                  │                                   │ 7. UpsertNode(approved,
   │                                  │                                   │      WG_pub, certFP)
   │                                  │                                   │ 8. AppendAudit("enroll")
   │                                  │◀──── {client cert, CA cert, FP} ──│
```

The agent ends holding a per-node mTLS client certificate it can present to the controller's
authenticated endpoints; the registry ends with an **approved** `Node` carrying the node's WireGuard
**public** key and the issued cert's SHA-256 fingerprint.

## The enrollment token

An `EnrollmentToken` ([store.go](../../../internal/controller/store.go)) authorizes **one** node to
enroll. It is **single-use, short-TTL, and node-scoped**, and the plaintext is **never stored**.

- **`NewEnrollmentToken(nodeID, ttl) (plaintext string, EnrollmentToken)`** — generates a
  `crypto/rand` plaintext token, computes `TokenHash = hex(SHA-256(plaintext))`, and sets
  `ExpiresAt = now + ttl`, `NodeID = nodeID`, `ConsumedAt = nil`. The operator persists the **record**
  (hashed) via `CreateEnrollmentToken` and hands the **plaintext** to the node **out-of-band** (the
  same trust assumption as copying a public key by hand in Phase 1b). A store/DB read can never recover
  a usable token — only its SHA-256 hash is on disk.
- **`CreateEnrollmentToken(ctx, t, tok)`** — the operator-side authorize step. Stores the token keyed
  by `TokenHash` within the tenant.
- **`ConsumeEnrollmentToken(ctx, t, tokenHash, nodeID, now)`** — the **atomic burn**. Under the store
  lock it looks up the token by `tokenHash` for tenant `t`; if none, or its `NodeID != nodeID`, or
  `now` is **at or after** `ExpiresAt`, it returns `ErrTokenInvalid`; if `ConsumedAt != nil` it returns
  `ErrTokenConsumed`; otherwise it sets `ConsumedAt = &now` and returns `nil`. The whole
  check-and-burn happens under **one lock** in both `MemStore` and `FileStore`, so two concurrent
  enrollments racing the same token can never both succeed — exactly one wins, the other sees
  `ErrTokenConsumed`.

### Single-use is the safety property — burn before issue

`Enroll` burns the token **first**, before the cert is issued. The ordering is deliberate: the token is
the scarce authorization, so spending it before doing any issuing work means a retry **needs a fresh
token**. If a post-burn step fails (a malformed CSR, a CN mismatch, an `UpsertNode` error), the token
is **already consumed** and gone — the operator must mint a new token to retry. This is the correct
fail-safe direction: a single token can never be replayed to provision two identities, even if the
ceremony aborts partway. The cost — a failed enrollment forces a new token — is acceptable; tokens are
cheap and the operator is already in the loop.

## Proof-of-possession — over the mTLS key, NOT the WG key (honest)

The node submits a **CSR** (`x509.CreateCertificateRequest` output, DER). That CSR is **self-signed
with the node's mTLS private key**, and verifying the CSR's self-signature **is** the
proof-of-possession: it proves the requester holds the private half of the mTLS keypair whose public
half is in the CSR.

> **Why PoP is on the mTLS key and not the WireGuard key.** WireGuard keys are **Curve25519 (X25519)**
> — a **Diffie-Hellman** key. They are DH-only and **cannot produce signatures**. There is no stdlib
> (or any) way to make a node *sign* a challenge with its WG key, so a WG-key PoP is not constructible.
> The mTLS keypair (Ed25519) **can** sign, so the CSR self-signature is the only sound PoP available,
> and it is the one this ceremony performs.

Consequently the **WG public key is registered as-is** — it is carried in the `EnrollRequest`, recorded
on the `Node`, and never independently proven. **This is honest and bounded:** a node that registers a
*wrong* WG public key only produces a **non-functional overlay** for itself — its peers expect a
different key, so its tunnels never come up. That is self-defeating, not a controller compromise: a
bad WG key cannot impersonate another node, exfiltrate a private key, or corrupt the fleet; it just
breaks the misregistering node's own connectivity. The PoP that *does* matter for control-plane
identity — possession of the mTLS key the node will authenticate with — is fully verified.

## The dev controller-CA — ephemeral by design

`DevCA` (`NewDevCA(tenant TenantID, ttl) (*DevCA, error)`) generates an **ephemeral Ed25519 CA key**
and a self-signed CA certificate **in memory**. The CA key is **never persisted** — not to the
`FileStore`, not anywhere on disk.

This is a deliberate **breach-bounding** decision, not a limitation to apologize for:

- A signing CA key on disk is the single highest-value target in the system: whoever holds it can mint
  client certs for any node. Keeping it **only in memory** means there is no at-rest CA key to steal, so
  the controller-CA's breach surface is bounded to the process's live memory.
- The trade-off is that a **controller restart loses the CA**, which invalidates every issued client
  cert, so **nodes must re-enroll** after a restart. For a single-tenant v1 dev controller this is
  acceptable — re-enrollment is the same cheap ceremony, and the operator already gates it with tokens.
- A **persistent / KMS-backed CA** (sign-only key handle, no exportable private key), OCSP/CRL, and
  cert rotation are **Plan 5** hardening. The `DevCA` establishes the issuance *contract* —
  `IssueClientCert` and `CAPoolPEM` — so Plan 5 swaps *where the CA key lives* without changing the
  ceremony.

**`(*DevCA).IssueClientCert(csrDER []byte, nodeID string, ttl) (certPEM []byte, fp string, err error)`**
parses the CSR, **verifies its self-signature (the PoP above)**, **requires `CN == "<tenant>:<nodeID>"`**
(the subject names exactly the tenant-scoped node — the same `tenant:node` identity Plan 5's mTLS
middleware will derive a `TenantID` from), then issues an X.509 **client** certificate
(`ExtKeyUsageClientAuth`) signed by the CA, valid for `ttl`. It returns the PEM-encoded cert and the
issued cert's **SHA-256 fingerprint** (hex). A bad self-signature or a CN mismatch is a **hard refusal**
— no cert is issued.

**`(*DevCA).CAPoolPEM()`** returns the CA certificate as PEM for use as the `ClientCAs` pool in
[4.3](#what-43-adds)'s mTLS termination (the controller trusts client certs that chain to this CA).

## The Enroll ceremony

`Enroll(ctx, store Store, ca *DevCA, t TenantID, req EnrollRequest, now time.Time) (EnrollResult, error)`
with `EnrollRequest{NodeID, CSRDER []byte, WGPublicKey string}` runs the steps in **fail-safe order**:

1. **`ConsumeEnrollmentToken(ctx, t, req token hash, req.NodeID, now)`** — atomic burn (above). On
   `ErrTokenInvalid` / `ErrTokenConsumed` the ceremony aborts and **nothing is provisioned**.
2. **`ca.IssueClientCert(req.CSRDER, req.NodeID, certTTL)`** — verify CSR self-signature (PoP),
   require `CN == "<t>:<req.NodeID>"`, issue the per-node client cert + fingerprint. (The token is
   **already burned** at this point; a failure here needs a fresh token — the single-use safety
   property.)
3. **`UpsertNode`** — write the registry record with `Status = NodeApproved`,
   `WGPublicKey = req.WGPublicKey` (registered as-is), `MTLSCertFP = fp`, `EnrolledAt = now`. The WG
   **public** key only ever reaches the registry — the zero-knowledge custody invariant of
   [persistence.md](persistence.md) / [key-custody.md](key-custody.md) holds end-to-end.
4. **`AppendAudit`** — append an `"enroll"` entry for the node, chaining it into the tenant's
   hash-chained audit log.
5. Return **`EnrollResult{ClientCertPEM, CACertPEM, Fingerprint}`** — the agent installs the client
   cert and pins the CA cert for its subsequent mTLS calls.

### Auto-approve in v1; manual-approval queue is 4.4

In v1 a **valid token + valid PoP auto-approves** the node — `Enroll` sets `Status = NodeApproved`
directly. The token is the operator's authorization gate (the operator decided to mint it for this
node), so a node that presents a live token and proves possession of its mTLS key is admitted without a
second human step. A **manual-approval queue** — enroll lands the node as `NodePending` and an operator
explicitly approves it in the panel — is [4.4](#what-43-adds); it slots in by changing the `Status`
`Enroll` writes, not the crypto or the token contract.

## What 4.3 adds

Plan **4.3** puts this ceremony on the wire:

- An HTTP **`POST /enroll`** endpoint that accepts `{token, CSR DER, WG public key}` and returns the
  issued client cert + CA cert (this is the **one** endpoint reachable *without* a client cert, since
  the node has none yet — it is gated by the single-use token + PoP instead).
- **mTLS termination** for every *other* controller endpoint, with `DevCA.CAPoolPEM()` wired as the
  TLS **`ClientCAs`** pool so the controller only accepts client certs that chain to its CA (the certs
  this ceremony issues).
- The agent **`enroll` subcommand** — generate the mTLS keypair + CSR, read the WG public key from the
  Phase-1b `keygen` step, call `/enroll`, and persist the returned client cert + CA pin for subsequent
  authenticated pulls/poll.

## Revocation

Revocation in this milestone is **overlay eviction**, not certificate revocation: a `Node` set to
`NodeRevoked` receives no bundles (it is excluded from the rendered subgraph — see
[persistence.md](persistence.md)), so a revoked node cannot obtain new configuration even if its mTLS
cert is still time-valid. Cryptographic revocation (OCSP/CRL, short-lived rotating certs) is **Plan 5**;
combined with the **ephemeral CA** (a restart invalidates *all* issued certs), the practical revocation
surface for v1 is small.

See also [persistence.md](persistence.md) (the `EnrollmentToken` Store methods, the audit chain, and
the public-keys-only registry), [key-custody.md](key-custody.md) (why only the WG public key is ever
stored), [signing.md](signing.md) (the Ed25519 + `crypto/x509` conventions reused here), and
[agent.md](agent.md) (the Phase-1b `keygen` that produces the WG keypair this ceremony registers).
