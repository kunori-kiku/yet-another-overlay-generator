// Package controller is the stateful control-plane layer for the YAOG controller
// panel (Phase 2+ of the controller-panel program). It is deliberately quarantined
// from the pure, stateless compiler/renderer: those packages stay frozen and
// dependency-minimal, while all server-side state lives behind the Store interface
// defined here.
//
// Zero-knowledge custody is a hard invariant of this package: the registry holds
// each node's WireGuard PUBLIC key only — a private key MUST never reach the
// controller, its Store, or any persisted bundle. See
// docs/spec/controller/key-custody.md and docs/spec/controller/persistence.md.
//
// Phase 2 ships two stdlib-only Store implementations — MemStore (in-memory, the
// CI-exercised impl and the long-poll primitive) and FileStore (JSON on disk,
// durable for a single-tenant v1 deployment). A Postgres adapter is a documented
// future Store impl (persistence.md §Postgres); the interface makes that swap
// drop-in. No third-party dependency is introduced here.
package controller

import (
	"context"
	"errors"
	"time"
)

// TenantID scopes every Store operation. It is the structural tenant-isolation
// chokepoint: EVERY Store method takes a TenantID as its mandatory first predicate,
// and a perpetual CI gate asserts none omits it. Phase 2 is single-tenant (a
// constant from YAOG_TENANT_ID); Phase 3 multi-tenant only changes how a TenantID
// is derived from the authenticated principal — never the data-access shape.
type TenantID string

// Sentinel errors returned across Store implementations so callers can branch on
// them with errors.Is regardless of the backing store.
var (
	// ErrNotFound is returned when a requested record does not exist for the tenant.
	ErrNotFound = errors.New("controller: not found")
	// ErrNoStagedBundle is returned by PromoteStaged when nothing is staged FOR
	// THE GENERATION BEING PROMOTED — either nothing is staged at all, or the
	// staged set's provisional generation was invalidated by an interleaved
	// BumpGeneration/promote (plan-3 scoping). The remedy is identical either
	// way: stage (again), then promote.
	ErrNoStagedBundle = errors.New("controller: nothing staged for the next generation; stage (again) before promoting")
	// ErrTokenInvalid is returned by ConsumeEnrollmentToken when the token is
	// unknown, scoped to a different node, or expired.
	ErrTokenInvalid = errors.New("controller: enrollment token invalid or expired")
	// ErrTokenConsumed is returned by ConsumeEnrollmentToken when the token was
	// already burned (single-use).
	ErrTokenConsumed = errors.New("controller: enrollment token already consumed")
	// ErrChallengeInvalid is returned by ConsumeLoginChallenge when no challenge
	// matches the hash for the operator, it is expired, or it was already consumed
	// (a consumed challenge is DELETED, so a replay simply finds nothing).
	ErrChallengeInvalid = errors.New("controller: login challenge invalid or expired")
)

// NodeStatus is the lifecycle state of a registry node.
type NodeStatus string

const (
	// NodePending is a node slot created but not yet enrolled (no public key).
	NodePending NodeStatus = "pending"
	// NodeApproved is an enrolled node cleared to receive configuration.
	NodeApproved NodeStatus = "approved"
	// NodeRevoked is a node evicted from the fleet (no bundles are distributed).
	NodeRevoked NodeStatus = "revoked"
)

// Node is the controller's registry record for one fleet node. It holds the
// WireGuard PUBLIC key only — never a private key (zero-knowledge custody).
type Node struct {
	NodeID string
	// WGPublicKey is the node's WireGuard public key (base64), bound at enrollment.
	// Empty while the node slot is pending. NEVER a private key.
	WGPublicKey string
	// APITokenHash is the hex SHA-256 of the node's bearer API token, stamped by
	// IssueNodeAPIToken at enrollment. Empty while the node is pending and after a
	// RevokeNodeAPIToken. The plaintext token is NEVER stored — only this hash — so
	// a store/DB read cannot recover a usable token.
	APITokenHash string
	Status       NodeStatus
	// DesiredGeneration is the latest promoted generation that targets this node.
	DesiredGeneration int64
	// AppliedGeneration is the generation the agent last reported applying.
	AppliedGeneration int64
	// LastChecksum is the manifest checksum the agent last reported.
	LastChecksum string
	// LastHealth is the free-form health string the agent last reported alongside
	// its applied generation ("" until the first report carries one).
	LastHealth string
	LastSeen   time.Time
	EnrolledAt time.Time
	// RekeyRequested is set by the operator's fleet-wide key-rotation request
	// (POST /rekey-all) and cleared when the agent re-registers its new WireGuard
	// PUBLIC key (POST /rekey). It is a flag the agent observes via /config; it
	// carries no key material. Like every other Node field it is persisted by both
	// Store impls (it rides along on the whole-Node UpsertNode write).
	RekeyRequested bool
}

// TopologyRecord is the operator's stored topology for a tenant. The JSON is
// public-keys-only (it must not carry WireGuard private keys); Version increments
// on each PutTopology.
type TopologyRecord struct {
	Version   int64
	JSON      []byte
	UpdatedAt time.Time
}

// SignedBundle is one node's rendered, Phase-0-signed bundle at a generation.
// Files maps bundle-relative paths (install.sh, wireguard/<iface>.conf,
// checksums.sha256, bundle.sig, signing-pubkey.pem, manifest.json, …) to content.
type SignedBundle struct {
	NodeID     string
	Generation int64
	Files      map[string][]byte
	IsStaged   bool
	IsCurrent  bool
	CreatedAt  time.Time
}

// AuditEntry is one append-only, hash-chained audit record. Hash =
// hex(SHA256(canonical(entry incl. PrevHash))); PrevHash links to the prior
// entry. The chain is tamper-EVIDENT for operational visibility only — an actor
// with write access to the backing store can recompute the whole chain, so it is
// not a cryptographic anti-tamper guarantee (that is Plan 5). See audit.go.
type AuditEntry struct {
	Seq       int64
	Timestamp time.Time
	Actor     string
	Action    string
	NodeID    string
	PrevHash  string
	Hash      string
}

// OperatorCredential is the pinned OFF-HOST signer the operator uses to sign the
// membership trust-list (the keystone, plan-5.1b). It is the public half only —
// never a private key — and is the trust anchor the controller verifies a submitted
// trust-list signature against. Alg names the signing algorithm (ed25519 /
// webauthn-es256 / webauthn-eddsa); PublicKeyPEM is the PKIX ("PUBLIC KEY") PEM the
// algorithm parser consumes; RPID/Origin are the WebAuthn relying-party binding
// values (empty for raw Ed25519). Pinning a credential turns KEYSTONE ON; with none
// pinned the controller behaves exactly as before (no trust-list).
type OperatorCredential struct {
	Alg          string `json:"alg"`
	CredentialID string `json:"credential_id"`
	PublicKeyPEM string `json:"public_key_pem"`
	RPID         string `json:"rpid"`
	Origin       string `json:"origin"`
}

// StoredTrustList is the operator-signed membership trust-list at rest. TrustListJSON
// is the canonical bytes the operator signed (trustlist.Canonical of the built
// trust-list); SignatureJSON is the json.Marshal of the trustlist.SignedTrustList;
// Epoch is the monotonic membership epoch those bytes were signed at. The compiler
// embeds both byte fields verbatim into every node bundle so nodes verify membership
// offline against their pinned credential.
type StoredTrustList struct {
	TrustListJSON []byte `json:"trustlist_json"`
	SignatureJSON []byte `json:"signature_json"`
	Epoch         int64  `json:"epoch"`
}

// EnrollmentToken authorizes one node to enroll: single-use, short-TTL, and scoped
// to a NodeID. The plaintext token is NEVER stored — only TokenHash (hex SHA-256 of
// the plaintext) — so a store/DB read cannot recover a usable token.
type EnrollmentToken struct {
	TokenHash string
	NodeID    string
	ExpiresAt time.Time
	// ConsumedAt is nil until the token is burned (single-use).
	ConsumedAt *time.Time
}

// Operator is a controller operator account (operator login, plan-5.2). It is
// created out-of-band by the `yaog-server create-operator` CLI and authenticates the
// operator at POST /login. The plaintext password is NEVER stored — only
// PasswordHash, a self-describing argon2id PHC string (see password.go) — so a
// store/DB read cannot recover a usable password.
type Operator struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	// TOTPSecret is the operator's base32 TOTP shared secret for optional login 2FA
	// (plan-5.2). Empty = no 2FA enrolled. It is SYMMETRIC, so it is stored at rest
	// (unlike a passkey, which stores only a public key); TOTP gates the panel login
	// only and is NEVER a keystone signing mechanism. See totp.go.
	TOTPSecret string `json:"totp_secret,omitempty"`
	// TOTPLastUsedStep is the most recent accepted TOTP step counter, for replay
	// rejection (a code at or before this step is refused). 0 until the first use.
	TOTPLastUsedStep int64 `json:"totp_last_used_step,omitempty"`
	// LoginCredential is the operator's OPTIONAL registered WebAuthn LOGIN passkey
	// (plan-5.2). nil = none registered. It is the PUBLIC half only (a private key never
	// reaches the controller) and is used to prove possession at login against a
	// server-issued RANDOM challenge — a SIBLING to, and deliberately SEPARATE from, the
	// tenant-level keystone OperatorCredential (the membership trust anchor, which signs
	// a CONTENT-BOUND manifest). See passkey login, handler_passkey.go.
	LoginCredential *LoginCredential `json:"login_credential,omitempty"`
}

// TOTPEnabled reports whether the operator has TOTP login 2FA enrolled.
func (o Operator) TOTPEnabled() bool { return o.TOTPSecret != "" }

// PasskeyEnabled reports whether the operator has a WebAuthn login passkey registered.
func (o Operator) PasskeyEnabled() bool { return o.LoginCredential != nil }

// LoginCredential is the PUBLIC half of an operator's registered WebAuthn LOGIN passkey
// (plan-5.2). It proves possession at panel login via a server-issued RANDOM challenge,
// and is distinct from the keystone OperatorCredential (membership trust anchor). Alg is
// always a WebAuthn algorithm ("webauthn-es256" | "webauthn-eddsa") — a raw Ed25519 has
// no authenticator assertion and is rejected at registration. PublicKeyPEM is the PKIX
// ("PUBLIC KEY") PEM; RPID/Origin are the WebAuthn relying-party binding (the node-style
// rpIdHash check: sha256(RPID) == authenticatorData[0:32]); CredentialID is base64url of
// the raw credential id, returned in allowCredentials so the browser selects this key.
type LoginCredential struct {
	Alg          string `json:"alg"`
	CredentialID string `json:"credential_id"`
	PublicKeyPEM string `json:"public_key_pem"`
	RPID         string `json:"rpid"`
	Origin       string `json:"origin"`
}

// LoginChallenge is a single-use, short-TTL, operator-scoped random nonce issued for a
// passkey login (plan-5.2). The plaintext challenge (base64url) is returned to the
// browser to feed navigator.credentials.get; only ChallengeHash (hex SHA-256 of that
// base64url string) is stored — the same hash-not-plaintext discipline as enrollment
// and session tokens. It is the RANDOM-challenge analogue of the keystone's
// content-bound manifest hash: its presence in the store proves the controller issued
// it, and single-use consumption is the anti-replay.
//
// Single-use is enforced by DELETION on consume (not a ConsumedAt flag), so a completed
// or expired challenge leaves no residue — this caps store growth without a sweep. A
// challenge carries no purpose discriminator beyond Operator: the /login 2FA leg,
// passwordless begin, and the disable re-auth leg mint interchangeable per-operator
// challenges. That is safe because every issuer is gated (the 2FA leg behind a correct
// password, disable behind an authenticated session) and each only ever yields a login
// or an already-authenticated disable — never a privilege escalation.
type LoginChallenge struct {
	ChallengeHash string    `json:"challenge_hash"`
	Operator      string    `json:"operator"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// Session is a server-side operator session minted at a successful /login. The
// controller stores only TokenHash (hex SHA-256 of the bearer session token); the
// plaintext is returned to the browser exactly once and held in memory there. A
// presented session bearer resolves to its Operator while now < ExpiresAt; an
// expired session is treated as invalid (and may be lazily deleted). Logout deletes
// the session. Sessions are the operator-side analogue of per-node API tokens.
type Session struct {
	TokenHash string    `json:"token_hash"`
	Operator  string    `json:"operator"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ControllerSettings holds operator-editable, server-persisted controller settings
// that drive the one-shot agent bootstrap (plan-5.2). All fields are NON-SECRET
// (public URLs / proxy prefixes), so they are safe to persist server-side and to bake
// into the publicly-served bootstrap script. Defaults are applied by DefaultSettings.
type ControllerSettings struct {
	// PublicAgentURL is the controller's public AGENT base URL (scheme://host[:port]),
	// where nodes enroll/pull. The bootstrap passes it as the agent's --controller (the
	// controller's secret path prefix, if any, is appended by the server when it renders
	// the script). Empty until the operator configures it.
	PublicAgentURL string `json:"public_agent_url"`
	// GithubProxy is an optional prefix applied to GitHub downloads in the bootstrap
	// (e.g. "https://gh-proxy.com/", producing "<proxy>https://github.com/..."). Empty
	// = off (the default). CDN-friendly for networks where github.com is slow/blocked.
	GithubProxy string `json:"github_proxy"`
	// AgentReleaseBaseURL is where the per-arch yaog-agent binary is downloaded from;
	// the bootstrap fetches "<AgentReleaseBaseURL>/yaog-agent-linux-<arch>". Defaults to
	// the project's "releases/latest/download".
	AgentReleaseBaseURL string `json:"agent_release_base_url"`
	// Translucency is the operator panel's vibrancy preference (panel-appshell P5). It is
	// a PANEL APPEARANCE setting served via GET/POST /settings; it is deliberately NOT
	// baked into the agent bootstrap script (it has no bearing on a node). A POINTER so a
	// legacy settings.json written before this field existed (nil) is distinguishable from
	// an explicit false: WithDefaults() fills nil with true, preserving the default-on
	// appearance after an upgrade instead of silently reading false.
	Translucency *bool `json:"translucency,omitempty"`
}

// Store is the single tenant-scoped data-access chokepoint for the controller.
//
// Contract for every implementation:
//   - TenantID is a mandatory predicate on every method; data for one tenant is
//     never visible to another (enforced by tenant_isolation_test.go).
//   - No method ever stores or returns a WireGuard private key.
//   - Reads of a missing record return ErrNotFound.
//   - Only the blocking method WaitForGeneration is required to honor ctx
//     cancellation. The non-blocking point methods complete synchronously; an
//     implementation MAY check ctx.Err() up front (FileStore does, for its I/O),
//     but callers must not rely on cancellation interrupting a point read/write.
//
// Enrollment-token methods are added by plan-4.2; the HTTP/deploy wiring that
// consumes WaitForGeneration is plan-4.3.
type Store interface {
	// --- Registry ---

	// UpsertNode creates or updates a node registry record (matched by NodeID).
	UpsertNode(ctx context.Context, t TenantID, n Node) error
	// GetNode returns the node, or ErrNotFound.
	GetNode(ctx context.Context, t TenantID, nodeID string) (Node, error)
	// ListNodes returns all nodes for the tenant (stable order by NodeID).
	ListNodes(ctx context.Context, t TenantID) ([]Node, error)
	// SetAppliedGeneration records what an agent reported applying (the applied
	// generation, the manifest checksum, and the free-form health string).
	SetAppliedGeneration(ctx context.Context, t TenantID, nodeID string, gen int64, checksum, health string) error
	// TouchLastSeen records that the agent for nodeID checked in at the given time.
	TouchLastSeen(ctx context.Context, t TenantID, nodeID string, at time.Time) error

	// --- Topology (public-keys-only) ---

	// PutTopology stores a new topology version (public-keys-only JSON) and returns
	// the stored record with its assigned Version.
	PutTopology(ctx context.Context, t TenantID, json []byte) (TopologyRecord, error)
	// GetTopology returns the current topology, or ErrNotFound.
	GetTopology(ctx context.Context, t TenantID) (TopologyRecord, error)

	// --- Bundles + generation ---

	// StageBundle stores a node's bundle as the staged (not-yet-current) version.
	// Staging replaces any prior staged bundle for that node.
	StageBundle(ctx context.Context, t TenantID, b SignedBundle) error
	// PruneStagedBundles deletes staged bundles whose NodeID is NOT in keep and
	// returns the purged node IDs (stable order). It is the stage-side half of
	// promote scoping (plan-3): CompileAndStage calls it with the freshly staged
	// set so a stale staged bundle for a since-removed node cannot linger into a
	// later promote. Current (promoted) bundles are never touched.
	PruneStagedBundles(ctx context.Context, t TenantID, keep []string) (purged []string, err error)
	// PromoteStaged atomically flips the CURRENTLY staged bundles to current,
	// increments the tenant's generation, sets DesiredGeneration on each promoted
	// node that has a registry record (a node is registered at enrollment before
	// any bundle is staged for it; promote updates existing records, it does not
	// create them), and wakes any WaitForGeneration waiters. Returns the new
	// generation, or ErrNoStagedBundle when nothing is staged.
	//
	// Scoping (plan-3): only bundles whose staged (provisional) Generation equals
	// the generation being promoted (current+1) flip. A staged bundle whose
	// provisional generation was invalidated — e.g. a BumpGeneration (rekey-all)
	// or another promote landed after it was staged — is stale by construction
	// (compiled against pre-bump state) and is NOT flipped; re-stage to refresh
	// it. If nothing matches, ErrNoStagedBundle.
	PromoteStaged(ctx context.Context, t TenantID) (generation int64, err error)
	// GetCurrentBundle returns the node's current (promoted) bundle, or ErrNotFound.
	GetCurrentBundle(ctx context.Context, t TenantID, nodeID string) (SignedBundle, error)
	// CurrentGeneration returns the tenant's current generation (0 if none promoted).
	CurrentGeneration(ctx context.Context, t TenantID) (int64, error)
	// BumpGeneration atomically increments the tenant's generation and wakes any
	// WaitForGeneration waiters, WITHOUT changing any bundle: GetCurrentBundle keeps
	// returning the last promoted bundle for every node. It is a WAKE, not a deploy —
	// it lets a non-deploy signal (e.g. a fleet-wide rekey request flagged on the
	// registry) rouse parked daemon agents, which Fetch /config and observe the signal
	// rather than apply this generation's (unchanged) bundle. Returns the new
	// generation. Use PromoteStaged when a new bundle set should actually go live;
	// BumpGeneration only advances the counter so the long-poll fires.
	BumpGeneration(ctx context.Context, t TenantID) (int64, error)
	// WaitForGeneration blocks until the tenant's current generation is strictly
	// greater than afterGen, then returns it; or returns ctx.Err() if ctx is done
	// first. This is the long-poll primitive consumed by plan-4.3's /poll endpoint.
	WaitForGeneration(ctx context.Context, t TenantID, afterGen int64) (int64, error)

	// --- Enrollment tokens (added by plan-4.2) ---

	// CreateEnrollmentToken stores a single-use, node-scoped, TTL token (by its
	// hash). It is the operator-side step that authorizes one node to enroll.
	CreateEnrollmentToken(ctx context.Context, t TenantID, tok EnrollmentToken) error
	// ConsumeEnrollmentToken atomically validates and burns a token: it returns
	// ErrTokenInvalid if no token matches tokenHash for nodeID or it is expired
	// (relative to now), ErrTokenConsumed if it was already burned, otherwise it
	// marks the token consumed (ConsumedAt=now) and returns nil. Single-use is
	// enforced atomically so two concurrent enrollments cannot both succeed.
	ConsumeEnrollmentToken(ctx context.Context, t TenantID, tokenHash, nodeID string, now time.Time) error

	// --- Passkey login challenges (plan-5.2) ---

	// CreateLoginChallenge stores a single-use, operator-scoped, TTL login challenge
	// (by its hash). It is the controller-side step that issues a random nonce for a
	// passkey login (the password+passkey 2FA leg, the passwordless begin, or a
	// disable re-auth).
	CreateLoginChallenge(ctx context.Context, t TenantID, lc LoginChallenge) error
	// ConsumeLoginChallenge atomically validates and burns a login challenge by DELETING
	// it: it returns ErrChallengeInvalid if no challenge matches challengeHash, if its
	// Operator != operator, or if it is expired (relative to now); otherwise it deletes
	// the record and returns nil. Single-use is enforced atomically by the delete under
	// the store lock, so a captured assertion cannot be replayed (the record is gone) and
	// two concurrent logins cannot both consume the same challenge. An expired record
	// encountered here is also deleted (lazy GC); a wrong-operator record is left intact
	// (it may be another operator's valid challenge — not the caller's to burn).
	ConsumeLoginChallenge(ctx context.Context, t TenantID, challengeHash, operator string, now time.Time) error

	// --- Node API tokens (per-node bearer auth) ---

	// IssueNodeAPIToken stamps tokenHash onto the node's APITokenHash AND writes a
	// reverse index hash->nodeID so a presented token can be resolved in O(1). It
	// returns ErrNotFound if no node record exists for nodeID. The plaintext token
	// is never stored — only its hex SHA-256 hash. Rotation is self-cleaning: if the
	// node already carried a different APITokenHash, the prior reverse-index entry is
	// deleted before the new one is written so no orphaned (stale) token lingers in
	// the index.
	IssueNodeAPIToken(ctx context.Context, t TenantID, nodeID, tokenHash string) error
	// LookupNodeByAPIToken resolves a presented token's hash to its Node via the
	// reverse index. The lookup is self-consistent: it returns ErrTokenInvalid unless
	// the index resolves to a live node whose own APITokenHash still equals tokenHash
	// AND whose Status is NodeApproved. This rejects an unmapped hash, a stale/orphaned
	// index entry that no longer matches the node's current token, and any node that
	// is not approved (pending or revoked) — so a rotated, revoked, or non-approved
	// token can never authorize.
	LookupNodeByAPIToken(ctx context.Context, t TenantID, tokenHash string) (Node, error)
	// RevokeNodeAPIToken clears the node's APITokenHash and deletes the reverse index
	// entry, immediately invalidating the node's bearer token. It is idempotent: a
	// node with no issued token (or already revoked) is a no-op success.
	RevokeNodeAPIToken(ctx context.Context, t TenantID, nodeID string) error

	// --- Audit (append-only, hash-chained) ---

	// AppendAudit appends an entry, chaining its PrevHash/Hash to the tenant's prior
	// entry and assigning Seq. Returns the stored entry (with Seq/PrevHash/Hash set).
	AppendAudit(ctx context.Context, t TenantID, e AuditEntry) (AuditEntry, error)
	// ListAudit returns the tenant's audit entries in Seq order.
	ListAudit(ctx context.Context, t TenantID) ([]AuditEntry, error)

	// --- Keystone: operator credential + signed trust-list (plan-5.1b) ---

	// SetOperatorCredential pins (or replaces) the tenant's off-host operator signing
	// credential — the trust anchor membership signatures are verified against. Pinning
	// one turns KEYSTONE ON for the tenant.
	SetOperatorCredential(ctx context.Context, t TenantID, c OperatorCredential) error
	// GetOperatorCredential returns the tenant's pinned operator credential, or
	// ErrNotFound when none is pinned (keystone OFF — behave as today).
	GetOperatorCredential(ctx context.Context, t TenantID) (OperatorCredential, error)
	// PutSignedTrustList stores (replacing any prior) the operator-signed membership
	// trust-list for the tenant.
	PutSignedTrustList(ctx context.Context, t TenantID, s StoredTrustList) error
	// GetCurrentSignedTrustList returns the tenant's current signed trust-list, or
	// ErrNotFound when none has been signed yet.
	GetCurrentSignedTrustList(ctx context.Context, t TenantID) (StoredTrustList, error)

	// --- Operators + sessions (operator login, plan-5.2) ---

	// PutOperator creates or replaces an operator account (matched by Username). It is
	// the persistence step behind `yaog-server create-operator`. The plaintext password
	// is never passed here — Operator carries only the argon2id PHC hash.
	PutOperator(ctx context.Context, t TenantID, op Operator) error
	// GetOperator returns the operator account, or ErrNotFound.
	GetOperator(ctx context.Context, t TenantID, username string) (Operator, error)
	// ListOperators returns all operator accounts for the tenant (stable order by
	// Username). Used to detect a duplicate at create time and to list accounts.
	ListOperators(ctx context.Context, t TenantID) ([]Operator, error)
	// DeleteOperator removes an operator account. It is idempotent: a missing account
	// is a no-op success. Existing sessions are NOT cascaded here (a session expires on
	// its own TTL); a caller wanting immediate lockout deletes the sessions too.
	DeleteOperator(ctx context.Context, t TenantID, username string) error
	// AdvanceTOTPStep atomically advances the operator's TOTP replay watermark
	// (TOTPLastUsedStep) to step ONLY IF step is strictly greater than the stored
	// value, returning advanced=true when it advanced (the presented code may be
	// accepted) and false when the step was already consumed (a replay / concurrent
	// reuse). This single atomic check-and-set closes the read-modify-write TOCTOU that
	// a separate Get/Put pair would leave open under concurrent logins. Returns
	// ErrNotFound if the operator is absent.
	AdvanceTOTPStep(ctx context.Context, t TenantID, username string, step int64) (advanced bool, err error)

	// CreateSession stores a minted operator session, keyed by its TokenHash (hex
	// SHA-256 of the session bearer token; the plaintext is never stored).
	CreateSession(ctx context.Context, t TenantID, s Session) error
	// LookupSession resolves a presented session token's hash to its Session. It
	// returns ErrTokenInvalid when no session matches tokenHash OR the session has
	// expired (now is at/after ExpiresAt); an implementation MAY lazily delete an
	// expired session it encounters. This is the operator-side analogue of
	// LookupNodeByAPIToken.
	LookupSession(ctx context.Context, t TenantID, tokenHash string, now time.Time) (Session, error)
	// DeleteSession removes a session (logout / revoke). It is idempotent: a missing
	// session is a no-op success.
	DeleteSession(ctx context.Context, t TenantID, tokenHash string) error

	// --- Controller settings (bootstrap, plan-5.2) ---

	// GetSettings returns the tenant's saved controller settings, or ErrNotFound when
	// none has been saved (the caller applies DefaultSettings).
	GetSettings(ctx context.Context, t TenantID) (ControllerSettings, error)
	// PutSettings stores (replacing any prior) the tenant's controller settings.
	PutSettings(ctx context.Context, t TenantID, s ControllerSettings) error
}
