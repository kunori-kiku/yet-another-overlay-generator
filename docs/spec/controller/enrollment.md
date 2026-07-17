# Node Enrollment (Phase 2 — single-use token → per-node bearer API token)

This document defines the **enrollment ceremony**: how a node moves from an out-of-band enrollment
token to an approved registry record holding its WireGuard **public** key and a freshly-issued
**per-node bearer API token** it uses to authenticate every subsequent controller call. It is the
controller-side crypto + `Store` layer (`internal/controller/enrollment.go`, plus the `EnrollmentToken`
and API-token methods on the `Store` in [persistence.md](persistence.md)); the HTTP `/enroll` endpoint
and the agent `enroll` subcommand are [controller-api.md](controller-api.md) / [agent.md](agent.md).
Everything here is **stdlib crypto only** (`crypto/sha256`, `crypto/rand`, `encoding/base64`,
`encoding/hex`) — **no `crypto/x509`, no `crypto/ed25519`, no `crypto/tls`**, and no new `go.mod`
dependency.

**Scope (plan-4.5).** Controller-side and CI-testable at the function level: the enrollment-token
contract, the per-node API-token minting, and the `Enroll` ceremony that ties them to the `Store`. See
[../../../implementation_plans/_completed/controller-panel-2026_06_08/plan-4.5-2026_06_08.md](../../../implementation_plans/_completed/controller-panel-2026_06_08/plan-4.5-2026_06_08.md)
and the parent [plan-4-2026_06_08.md](../../../implementation_plans/_completed/controller-panel-2026_06_08/plan-4-2026_06_08.md).

> **Retraction (2026-06-08).** An earlier revision of this spec specified an ephemeral **`DevCA`**
> (`NewDevCA`, `IssueClientCert`, `IssueServerCert`, `ServerTLSConfig`, `CACertPEM`, `CACertPool`) and a
> **CSR proof-of-possession** over the node's mTLS key, with `Enroll` issuing an X.509 **client
> certificate** and the node carrying an `MTLSCertFP`. **All of that is withdrawn** (see
> [controller-api.md](controller-api.md) §Retraction). There is **no CA** in the system anymore, no CSR,
> and no cert. The enrollment token is now exchanged for a **bearer API token**, not a certificate.

## The ceremony at a glance

```
operator                         node agent                         controller (Enroll)
   │                                  │                                   │
   │ 1. NewEnrollmentToken(node, ttl) │                                   │
   │    store HASH via                │                                   │
   │    CreateEnrollmentToken         │                                   │
   │ 2. hand PLAINTEXT out-of-band ──▶│                                   │
   │                                  │ 3. EnsureKey → WG keypair          │
   │                                  │    (public key only leaves host)   │
   │                                  │ 4. {enrollment_token, node_id,     │
   │                                  │     wg_public_key} ───────────────▶│
   │                                  │                                   │ 5. ConsumeEnrollmentToken
   │                                  │                                   │    (atomic burn — first)
   │                                  │                                   │ 6. NewNodeAPIToken:
   │                                  │                                   │    plaintext + hash
   │                                  │                                   │ 7. UpsertNode(approved,
   │                                  │                                   │      WG_pub, APITokenHash)
   │                                  │                                   │ 8. IssueNodeAPIToken(hash)
   │                                  │                                   │ 9. AppendAudit("enroll")
   │                                  │◀──── {api_token (plaintext once)} ─│
```

The agent ends holding a **per-node bearer API token** it presents as `Authorization: Bearer <token>`
to the controller's authenticated endpoints; the registry ends with an **approved** `Node` carrying the
node's WireGuard **public** key and the **hex SHA-256 hash** of the issued API token (never the
plaintext).

## The enrollment token

An `EnrollmentToken` ([store.go](../../../internal/controller/store.go)) authorizes **one** node to
enroll. It is **single-use, short-TTL, and node-scoped**, and the plaintext is **never stored**. (It is
distinct from the per-node API token below: the enrollment token is the one-shot *authorization to
enroll*; the API token is the *standing credential* the node receives in exchange.)

- **`NewEnrollmentToken(nodeID string, ttl time.Duration, now time.Time) (plaintext string, EnrollmentToken)`**
  — generates a `crypto/rand` plaintext token, computes `TokenHash = HashToken(plaintext) = hex(SHA-256(plaintext))`,
  and sets `ExpiresAt = now + ttl`, `NodeID = nodeID`, `ConsumedAt = nil`. (`now` is passed in, not read
  from the clock, so callers/tests are deterministic.) The operator persists the **record** (hashed) via
  `CreateEnrollmentToken` and hands the **plaintext** to the node **out-of-band** (the same trust
  assumption as copying a public key by hand in Phase 1b). A store/DB read can never recover a usable
  token — only its SHA-256 hash is on disk.
- **`CreateEnrollmentToken(ctx, t, tok)`** — the operator-side authorize step. Stores the token keyed by
  `TokenHash` within the tenant. (The HTTP `/enrollment-token` operator route is the panel-facing front
  for `NewEnrollmentToken` + `CreateEnrollmentToken`; see [controller-api.md](controller-api.md).)
- **`ConsumeEnrollmentToken(ctx, t, tokenHash, nodeID, now)`** — the **atomic burn**. Under the store
  lock it looks up the token by `tokenHash` for tenant `t`; if none, or its `NodeID != nodeID`, or `now`
  is **at or after** `ExpiresAt`, it returns `ErrTokenInvalid`; if `ConsumedAt != nil` it returns
  `ErrTokenConsumed`; otherwise it sets `ConsumedAt = &now` and returns `nil`. The whole check-and-burn
  happens under **one lock** in both `MemStore` and `FileStore`, so two concurrent enrollments racing the
  same token can never both succeed — exactly one wins, the other sees `ErrTokenConsumed`.

`HashToken(plaintext string) string` is the shared hex-SHA-256 helper used for **both** token kinds: the
controller only ever stores and compares **hashes**, never plaintext.

### Single-use is the safety property — burn before issue

`Enroll` burns the enrollment token **first**, before the API token is minted or the node is registered.
The ordering is deliberate: the enrollment token is the scarce authorization, so spending it before doing
any provisioning work means a retry **needs a fresh token**. If a post-burn step fails (an `UpsertNode`
error, an `IssueNodeAPIToken` error), the token is **already consumed** and gone — the operator must mint
a new enrollment token to retry. This is the correct fail-safe direction: a single token can never be
replayed to provision two identities, even if the ceremony aborts partway. The cost — a failed enrollment
forces a new token — is acceptable; tokens are cheap and the operator is already in the loop.

## The per-node API token

`NewNodeAPIToken(now time.Time) (plaintext, hash string)` mints the standing credential. It **mirrors
`NewEnrollmentToken`**: 32 bytes from `crypto/rand`, `base64.RawURLEncoding` for the plaintext, and
`hash = HashToken(plaintext)` (hex SHA-256). `now` is accepted for signature symmetry with the other
mint functions (deterministic callers/tests) even though the token itself carries no expiry — its
lifetime is the node's registry membership, ended by revocation, not a TTL.

- The **plaintext** is returned to the node **exactly once**, in the `/enroll` response, and is then the
  node's `Authorization: Bearer <token>` for every `/config`, `/poll`, `/report` call
  ([agent.md](agent.md), [controller-api.md](controller-api.md)).
- The **hash** is stamped onto `Node.APITokenHash` and written into the per-node reverse index
  (`IssueNodeAPIToken`, [persistence.md](persistence.md) §The per-node API-token index). The controller
  never stores the plaintext.

### Why no proof-of-possession at enroll (honest)

The withdrawn mTLS model proved possession of an mTLS private key via a CSR self-signature at enroll.
The bearer-token model has **no per-request PoP**: the enrollment token *is* the authorization, and the
API token the node receives is a secret it must hold (0600 on disk, [agent.md](agent.md)). The
trade-off — a bearer token is **replayable if leaked** — is documented in
[controller-api.md](controller-api.md) §Plain HTTP + proxy TLS; the v1 mitigations are transport
confidentiality via the reverse proxy and **immediate revocation**. As with the old model, the
**WG public key is registered as-is** and never independently proven: a node that registers a *wrong* WG
public key only breaks its **own** overlay (its peers expect a different key, so its tunnels never come
up) — that is self-defeating, not a controller compromise, and it cannot impersonate another node or
corrupt the fleet.

## The Enroll ceremony

```go
func Enroll(ctx context.Context, store Store, t TenantID, req EnrollRequest, now time.Time) (EnrollResult, error)
```

with

```go
type EnrollRequest struct {
    Token       string // the PLAINTEXT enrollment token the node presents
    NodeID      string
    WGPublicKey string // base64 WireGuard public key (registered as-is)
}

type EnrollResult struct {
    NodeID   string
    APIToken string // the PLAINTEXT per-node bearer token — returned exactly once
}
```

Note the signature has **no `*DevCA` parameter** (the CA is gone) and `EnrollRequest` has **no `CSRDER`**
(no CSR). `Enroll` hashes the presented enrollment token with `HashToken` before the burn — the
controller only ever compares hashes. The steps run in **fail-safe order**:

1. **`ConsumeEnrollmentToken(ctx, t, HashToken(req.Token), req.NodeID, now)`** — atomic burn (above). On
   `ErrTokenInvalid` / `ErrTokenConsumed` the ceremony aborts and **nothing is provisioned**.
2. **`plaintext, hash := NewNodeAPIToken(now)`** — mint the per-node bearer token + its hash. (The
   enrollment token is **already burned** at this point; any failure from here needs a fresh enrollment
   token — the single-use safety property.)
3. **`UpsertNode`** — write the registry record with `Status = NodeApproved`,
   `WGPublicKey = req.WGPublicKey` (registered as-is), `APITokenHash = hash`, `EnrolledAt = now`. The WG
   **public** key only ever reaches the registry — the zero-knowledge custody invariant of
   [persistence.md](persistence.md) / [key-custody.md](key-custody.md) holds end-to-end.
4. **`IssueNodeAPIToken(ctx, t, req.NodeID, hash)`** — write the reverse index `hash → nodeID` so the
   auth chokepoint can resolve the token on the node's next call ([persistence.md](persistence.md)).
5. **`AppendAudit(... "enroll", req.NodeID)`** — append an `"enroll"` entry for the node, chaining it
   into the tenant's hash-chained audit log.
6. Return **`EnrollResult{NodeID: req.NodeID, APIToken: plaintext}`** — the agent stores the plaintext
   bearer token (0600) and presents it on every subsequent authenticated call.

The HTTP layer keeps a **reserved-operator-name guard** *before* calling `Enroll`: a `node_id` equal to
the configured operator identity is rejected `403` so a node can never enroll as the operator
([controller-api.md](controller-api.md) §`POST /enroll`).

### Auto-approve in v1; manual-approval queue is later

In v1 a **valid enrollment token auto-approves** the node — `Enroll` sets `Status = NodeApproved`
directly. The enrollment token is the operator's authorization gate (the operator decided to mint it for
this node), so a node that presents a live token is admitted without a second human step. A
**manual-approval queue** — enroll lands the node as `NodePending` and an operator explicitly approves it
in the panel — slots in by changing the `Status` `Enroll` writes, not the token contract.

## Revocation

Revocation is **immediate and token-based**: `RevokeNodeAPIToken(ctx, t, nodeID)`
([persistence.md](persistence.md)) clears the node's `APITokenHash` and deletes the reverse-index entry,
so the **very next** authenticated request the node makes fails (`LookupNodeByAPIToken` no longer
resolves the hash → `401`). There is no TTL to wait out, no CRL to distribute, no propagation delay.
Setting a node to `NodeRevoked` is the complementary **overlay eviction**: a revoked node is excluded
from the rendered subgraph ([deploy.md](deploy.md)) and `LookupNodeByAPIToken` fail-closes a still-mapped
token whose node is `NodeRevoked` to `ErrTokenInvalid`. The two together make revocation both immediate
(token cleared) and durable (status excludes the node from future renders). A leaked-but-unrevoked token
is replayable until revoked — the honest trade-off recorded in [controller-api.md](controller-api.md);
immediate revocation is its bound.

See also [persistence.md](persistence.md) (the `EnrollmentToken` + per-node API-token Store methods, the
audit chain, and the public-keys-only registry), [key-custody.md](key-custody.md) (why only the WG public
key is ever stored), [signing.md](signing.md) (the Ed25519 + bundle-signing conventions, unchanged by
this rework), [controller-api.md](controller-api.md) (the `/enroll` HTTP endpoint + the auth chokepoint
the API token feeds), and [agent.md](agent.md) (the `enroll` subcommand + the WG keygen that produces the
public key this ceremony registers).
