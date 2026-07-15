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
	"encoding/json"
	"errors"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
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
	// ErrIncompleteStagedSet is returned (wrapped together with ErrNoStagedBundle) when staged
	// records exist but their durable staged-set seal is absent or no longer matches them. This is
	// the fail-closed crash shape of ReplaceStagedSet: a partial candidate can remain on disk, but it
	// is never promotable until a clean re-stage publishes a fresh seal last.
	ErrIncompleteStagedSet = errors.New("controller: staged set is incomplete or does not match its seal")
	// ErrTokenInvalid is returned by ConsumeEnrollmentToken when the token is
	// unknown, scoped to a different node, or expired.
	ErrTokenInvalid = errors.New("controller: enrollment token invalid or expired")
	// ErrTokenConsumed is returned by ConsumeEnrollmentToken when the token was
	// already burned (single-use).
	ErrTokenConsumed = errors.New("controller: enrollment token already consumed")
	// ErrChallengeInvalid is returned by ConsumeAssertionChallenge when no challenge
	// matches the hash for the subject, it is expired, or it was already consumed
	// (a consumed challenge is DELETED, so a replay simply finds nothing).
	ErrChallengeInvalid = errors.New("controller: assertion challenge invalid or expired")
	// ErrOperatorCredentialChanged is returned by CompareAndSetOperatorCredential when the
	// keystone changed after the caller classified a pin/rotation request. The caller must
	// refresh server truth and re-evaluate the transition; silently retrying could turn a first
	// pin into a rotation without the required acknowledgement.
	ErrOperatorCredentialChanged = errors.New("controller: operator credential changed concurrently")
	// ErrLoginCredentialChanged is returned when a login passkey changed after a handler loaded
	// the operator and before it attempted the field-scoped update. Retrying must re-authenticate
	// against current state; a stale disable must never clear a newly-registered passkey.
	ErrLoginCredentialChanged = errors.New("controller: login credential changed concurrently")
	// ErrTOTPStateChanged is returned when a TOTP enrollment/disable ceremony was verified against
	// a state that changed before its field-scoped write. Retrying must re-read and re-verify; a
	// stale whole-account write must never resurrect or erase either TOTP or a login passkey.
	ErrTOTPStateChanged = errors.New("controller: TOTP state changed concurrently")
	// ErrPendingKeystoneTransitionConflict means a durable keystone transition marker names a
	// target credential that is neither the current credential nor the marker's expected prior
	// state. The protocol cannot safely infer whether an out-of-band writer skipped its recovery
	// boundary, so it retains the marker and refuses another transition for manual diagnosis.
	ErrPendingKeystoneTransitionConflict = errors.New("controller: pending keystone transition conflicts with current credential")
	// ErrKeystoneAuditRequired is returned by the controller-level keystone transition boundary
	// when a caller attempts a real trust-anchor mutation without the exact audit identity the
	// write-ahead protocol requires. Only an exact compare-only no-op may omit the audit entry.
	ErrKeystoneAuditRequired = errors.New("controller: a keystone credential mutation requires an audit entry")
	// ErrUncommittedPromotion is returned when FileStore contains a live-state record written by
	// an interrupted promotion whose generation commit marker has not landed yet. Agent-serving
	// reads and unrelated stage/generation mutations fail closed until PromoteStaged is retried and
	// generation.json commits.
	ErrUncommittedPromotion = errors.New("controller: promoted configuration is not committed")
	// ErrDuplicateWGKey is returned by Enroll when the presented WireGuard public
	// key is already approved under a DIFFERENT node-id (plan-6: one approved WG
	// pubkey ↔ one node-id — the duplicate-fleet-rows vector). Same-id re-enroll
	// (reinstalled host, fresh token) is unaffected.
	ErrDuplicateWGKey = errors.New("controller: WireGuard public key already enrolled under a different node id")
	// ErrInvalidWGKey is returned by Enroll/Rekey when the presented WireGuard public
	// key is not a valid Curve25519 key (32 bytes of standard base64). It is rejected
	// up front, before the enrollment token is burned, so a malformed value never
	// reaches the registry or a rendered peer config.
	ErrInvalidWGKey = errors.New("controller: WireGuard public key is not a valid base64/32-byte Curve25519 key")
	// ErrNodeRevoked is returned by Enroll when the claimed node-id exists and is
	// revoked: a revoked node-id must not be silently resurrected by a still-valid
	// enrollment token. The operator deletes the node to reuse the id.
	ErrNodeRevoked = errors.New("controller: node id is revoked; delete it before re-enrolling")
	// ErrNoPinnedCredential is returned by InstallTrustListSignature when no operator
	// credential is pinned (keystone OFF), so there is no anchor to verify a signature
	// against. The api layer maps it to CodeNoPinnedCredential (412).
	ErrNoPinnedCredential = errors.New("controller: no operator credential is pinned")
	// ErrNoStagedManifest is returned by InstallTrustListSignature when nothing has been
	// staged yet (no manifest to sign). The api layer maps it to CodeNoStagedManifest (404).
	ErrNoStagedManifest = errors.New("controller: no membership manifest is staged")
	// ErrStagedManifestMismatch is returned by InstallTrustListSignature when the submitted
	// canonical bytes do not byte-equal the CURRENTLY-staged manifest — a re-stage moved the
	// staged manifest since the operator fetched it, so the submitted signature is over stale
	// bytes. Rejecting here (under the tenant op lock) is the substitution guard that stops a
	// signed M_old from ever pairing with a freshly staged B_new. Mapped to
	// CodeStagedManifestMismatch (409).
	ErrStagedManifestMismatch = errors.New("controller: submitted manifest does not match the staged manifest")
	// ErrManifestSignatureInvalid is returned (wrapped) by InstallTrustListSignature when the
	// off-host signature does not verify against the pinned credential. Mapped to
	// CodeManifestSignatureInvalid (400).
	ErrManifestSignatureInvalid = errors.New("controller: manifest signature does not verify against the pinned credential")
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
	// LastAgentVersion is the agent build version reported alongside the last apply ("" until a
	// version-aware agent reports). Observability only (plan-4); the binary-swap floor is plan-9.
	LastAgentVersion string
	// Conditions is the structured feedback set the agent last reported, each server-stamped with
	// ObservedAt on receipt (plan-1). Nil until a conditions-aware agent reports; replaced wholesale
	// each report (the latest report is the truth). Observability only — not custody, not allocation.
	Conditions []NodeCondition
	// Telemetry is the agent's extensible metrics map from the last /telemetry heartbeat (the
	// framework's extension slot — e.g. wireguard_peers, the per-peer link detail). Opaque JSON the
	// controller persists + serves verbatim for the panel to interpret by key; nil until a
	// metrics-emitting agent heartbeats. Replaced wholesale each heartbeat. Observability only — never
	// custody, never key material (the agent emits no keys/allowed-ips).
	Telemetry  map[string]json.RawMessage
	LastSeen   time.Time
	EnrolledAt time.Time
	// RekeyRequested is set by the operator's fleet-wide key-rotation request
	// (POST /rekey-all) and cleared when the agent re-registers its new WireGuard
	// PUBLIC key (POST /rekey). It is a flag the agent observes via /config; it
	// carries no key material. Like every other Node field it is persisted by both
	// Store impls (it rides along on the whole-Node UpsertNode write).
	RekeyRequested bool
}

// NodeCondition is one stored agent condition with a SERVER-stamped ObservedAt. It embeds the
// reported runtimecontract.Condition verbatim (type/status/reason/message/since) and adds the controller's
// authoritative receipt time, so the panel orders/ages conditions by a clock the node cannot spoof
// (the wire Since is advisory only). Persisted by both Store impls on the whole-Node write.
type NodeCondition struct {
	runtimecontract.Condition
	// ObservedAt is the controller wall-clock time SetAppliedGeneration recorded this condition.
	ObservedAt time.Time `json:"observed_at"`
}

// maxStoredConditions is a defense-in-depth ceiling on the conditions stampConditions will allocate,
// independent of the HTTP-boundary check (the /report + /telemetry handlers reject a slice larger than
// 32). It is generous (2x the handler cap) so it never truncates a legitimate report, but guarantees no
// caller — present or future — can drive an unbounded allocation here.
const maxStoredConditions = 64

// stampConditions wraps each reported runtimecontract.Condition with the controller's authoritative
// ObservedAt. A nil/empty report clears the stored set (the latest report is the truth: an agent
// that no longer reports a condition has it removed). The wire Since is carried through unchanged
// (advisory); ObservedAt is the server clock. Shared by both Store impls so the server-stamp logic
// is not duplicated.
func stampConditions(conditions []runtimecontract.Condition, observedAt time.Time) []NodeCondition {
	if len(conditions) == 0 {
		return nil
	}
	if len(conditions) > maxStoredConditions {
		conditions = conditions[:maxStoredConditions]
	}
	out := make([]NodeCondition, len(conditions))
	for i, c := range conditions {
		out[i] = NodeCondition{Condition: c, ObservedAt: observedAt}
	}
	return out
}

// TopologyRecord is the operator's stored topology for a tenant. The JSON is
// public-keys-only (it must not carry WireGuard private keys); Version increments
// on each PutTopology.
type TopologyRecord struct {
	Version   int64
	JSON      []byte
	UpdatedAt time.Time
}

// TopologyHistoryLimit is the number of topology versions every Store retains
// (D7): each PutTopology appends a version and prunes the oldest beyond this
// bound, so a bad overwrite (empty-canvas deploy, botched import) is recoverable
// from the API without filesystem backups. Stage write-backs (persistAllocations)
// also count as versions.
const TopologyHistoryLimit = 10

// TopologyVersionInfo is the cheap list view of one retained topology version —
// metadata only, no JSON payload (fetch a payload via GetTopologyVersion).
type TopologyVersionInfo struct {
	Version   int64
	UpdatedAt time.Time
	Bytes     int
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
	// EventID is an optional stable idempotency identity. Empty legacy/general entries retain the
	// historical audit canonical encoding; durable keystone transitions set a random EventID so a
	// retry can distinguish "append committed but returned an error" from "append never happened".
	EventID  string `json:",omitempty"`
	PrevHash string
	Hash     string
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

// PendingKeystoneTransition is the durable write-ahead marker for one audited keystone pin or
// rotation. Expected is nil for a first pin; Next is the credential the CAS intends to install;
// Audit is the exact pre-chain entry, including a stable EventID and timestamp, that must appear
// exactly once iff Next becomes current. It contains public credential material only.
type PendingKeystoneTransition struct {
	Expected *OperatorCredential `json:"expected,omitempty"`
	Next     OperatorCredential  `json:"next"`
	Audit    AuditEntry          `json:"audit"`
}

// StoredTrustList is the operator-signed membership trust-list at rest. TrustListJSON
// is the canonical bytes the operator signed (trustlist.Canonical of the built
// trust-list); SignatureJSON is the json.Marshal of the trustlist.SignedTrustList;
// Epoch is the monotonic membership epoch those bytes were signed at. The agent /config
// response appends both byte fields alongside the promoted bundle so nodes verify membership
// against their pinned credential before adopting the bundle.
type StoredTrustList struct {
	TrustListJSON      []byte `json:"trustlist_json"`
	SignatureJSON      []byte `json:"signature_json"`
	Epoch              int64  `json:"epoch"`
	PromotedGeneration int64  `json:"promoted_generation,omitempty"`
}

// StagedSet is the complete candidate a compiler publishes before promotion. Generation is the
// provisional generation (current+1), Bundles is the exact set of changed nodes to flip, and
// TrustList is the optional keystone manifest binding the full ready fleet (including unchanged
// nodes). ReplaceStagedSet persists the component records first and a durable seal last; a crash or
// error before that final write leaves the candidate unpromotable.
type StagedSet struct {
	Generation int64
	Bundles    []SignedBundle
	TrustList  *StoredTrustList
}

// ServedConfig is the ATOMIC snapshot of what /config serves one node: its current promoted Bundle,
// whether the keystone is ON (KeystoneOn), and — when ON and something has been promoted under it —
// the served signed trust-list (TrustList, valid iff HasTrustList). Reading these together under a
// single store lock (GetServedConfig) guarantees that for any IN-PROCESS reader the (bundle,
// trust-list) pair is always from one promoted generation, so a concurrent PromoteStaged can never
// make a node fetch a torn (old-bundle, new-manifest) pair that would spuriously fail the agent's
// bundle-digest binding.
//
// The single-lock guarantee is in-process only. Across a FileStore PROCESS crash mid-PromoteStaged
// (current bundles and served_trustlist.json are separate atomic renames) a (new-bundle,
// old-served-manifest) pair can transiently exist on disk; that is FAIL-CLOSED — the agent's
// offline bundle-digest binding refuses the mismatch and keeps last-good — and SELF-REPAIRING (a
// re-run of PromoteStaged rewrites the served slot). See FileStore.PromoteStaged. (Generation-
// tagging the served slot to force crash-atomicity would be WRONG here: a node not re-staged in a
// later promote keeps an older-generation bundle while the tenant-wide manifest advances, yet the
// manifest still binds that node's unchanged digest, so the bundle generation and the served
// manifest's promote generation legitimately differ.)
type ServedConfig struct {
	Bundle       SignedBundle
	KeystoneOn   bool
	TrustList    StoredTrustList
	HasTrustList bool
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

// TOTPState is the pair of account fields that forms the TOTP configuration and replay state. It is
// used by CompareAndSetTOTPState so TOTP management can update those fields without writing a stale
// whole Operator (which could otherwise undo a concurrent login-passkey change).
type TOTPState struct {
	Secret       string
	LastUsedStep int64
}

// AssertionChallenge is a single-use, short-TTL random nonce issued for passkey login
// (plan-5.2) or browser WebAuthn enrollment proof. The plaintext challenge (base64url)
// is returned to the browser to feed navigator.credentials.get; only ChallengeHash
// (hex SHA-256 of that base64url string) is stored — the same hash-not-plaintext
// discipline as enrollment and session tokens. It is the RANDOM-challenge analogue of
// the keystone's content-bound manifest hash: its presence in the store proves the
// controller issued it, and single-use consumption is the anti-replay.
//
// Single-use is enforced by DELETION on consume (not a ConsumedAt flag). Creation purges
// expired records, while browser enrollment uses ReplaceAssertionChallengeForSubject so only one
// live challenge exists for an actor+purpose. Normal /login 2FA, passwordless begin, and disable
// re-auth challenges carry the raw username in Subject and remain interchangeable for that
// account. Browser credential enrollment instead places a synthesized purpose+actor value in
// Subject, so a login-credential proof cannot be consumed by keystone enrollment (or by a
// different actor).
type AssertionChallenge struct {
	ChallengeHash string `json:"challenge_hash"`
	// Subject retains the historical "operator" JSON key so existing FileStore records load
	// without migration. It is a username for login, or purpose+actor for enrollment.
	Subject   string    `json:"operator"`
	ExpiresAt time.Time `json:"expires_at"`
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
	// Mimic GitHub-.deb catalog (plan-3): when set, a generated install.sh for a node with a
	// transport="tcp" link falls back to a SHA-256-pinned .deb from GitHub on distros that do
	// not package mimic (Debian 12 / Ubuntu 24.04). All NON-SECRET — a release version tag, the
	// release base URL, and per-"<codename>-<arch>" (e.g. "bookworm-amd64") asset names + SHA-256
	// hashes the installer verifies before dpkg. The pins are emitted into the controller-signed
	// artifacts.json. Empty ⇒ distro-only mimic, no GitHub fallback, no artifacts.json (D4).
	MimicVersion     string                       `json:"mimic_version,omitempty"`
	MimicReleaseBase string                       `json:"mimic_release_base,omitempty"`
	MimicDebs        map[string]model.MimicDebPin `json:"mimic_debs,omitempty"`
	// MimicFallbackDefault is the FLEET-WIDE mimic→UDP fallback policy a transport=="tcp" link
	// inherits when its edge leaves mimic_fallback empty. "" / "udp" / "none". NON-SECRET. The
	// shipped default is "none" (fail-closed, D1) — preserving mimic's censorship-evasion guarantee
	// unless the operator opts in. omitempty: a legacy settings.json with no field loads as "".
	MimicFallbackDefault string `json:"mimic_fallback_default,omitempty"`
	// Signed agent self-update (plan-9, canary-then-fleet D2). All NON-SECRET. The agent
	// release base URL is the EXISTING AgentReleaseBaseURL above (reused as the self-update
	// download base — no duplicate field). The pins below are emitted into the per-node,
	// controller-signed artifacts.json agent block and the agent verifies a fetched binary
	// against them before exec.
	//
	// TargetAgentVersion is the version the rollout drives toward. EMPTY ⇒ no agent block is
	// emitted for any node ⇒ no self-update happens (the safety contract). MinAgentVersion is
	// the floor below which a node must self-update BEFORE applying a bundle (a bundle/wire
	// break); empty ⇒ no forced update. AgentBins maps "linux-<arch>" (e.g. "linux-amd64") to
	// the release asset + SHA-256 the agent verifies.
	TargetAgentVersion string                    `json:"target_agent_version,omitempty"`
	MinAgentVersion    string                    `json:"min_agent_version,omitempty"`
	AgentBins          map[string]model.Artifact `json:"agent_bins,omitempty"`
	// Canary rollout (D2): during the canary phase only AgentCanaryNodeIDs receive the agent
	// block (and thus self-update). AgentRolloutFleetWide is the operator's "promote canary →
	// fleet" action: when true, EVERY enrolled node receives the agent block. Together they
	// stage the rollout — a bad target is caught on the canary subset before it reaches the
	// fleet.
	AgentCanaryNodeIDs    []string `json:"agent_canary_node_ids,omitempty"`
	AgentRolloutFleetWide bool     `json:"agent_rollout_fleet_wide,omitempty"`
	// Translucency is the operator panel's vibrancy preference (panel-appshell P5). It is
	// a PANEL APPEARANCE setting served via GET/POST /settings; it is deliberately NOT
	// baked into the agent bootstrap script (it has no bearing on a node). A POINTER so a
	// legacy settings.json written before this field existed (nil) is distinguishable from
	// an explicit false: WithDefaults() fills nil with true, preserving the default-on
	// appearance after an upgrade instead of silently reading false.
	Translucency *bool `json:"translucency,omitempty"`
	// TelemetryHistoryCap is the per-node hard cap on retained resource-history samples (plan-2), a
	// POINTER so a legacy record predating the field (nil) is distinguishable from an explicit 0
	// (disable history): nil ⇒ DefaultTelemetryHistoryCap, an explicit value (incl. 0) is honored. See
	// EffectiveHistoryCap. NON-SECRET (a retention count); not baked into the bootstrap script.
	TelemetryHistoryCap *int `json:"telemetry_history_cap,omitempty"`
}

// EffectiveHistoryCap resolves the per-node telemetry-history sample cap: a nil field (legacy / unset)
// means the default; an explicit value (including 0 = history disabled) is honored verbatim.
func (cs ControllerSettings) EffectiveHistoryCap() int {
	if cs.TelemetryHistoryCap == nil {
		return DefaultTelemetryHistoryCap
	}
	return *cs.TelemetryHistoryCap
}

// SigningAnchor is the per-tenant pinned bundle-signing PUBLIC key. It is NON-SECRET (a public key)
// and persists two facts a controller redeploy must not silently lose: THAT this fleet's bundles
// are signed, and WHICH key signs them. With it stored, a redeploy that drops or swaps
// YAOG_BUNDLE_SIGNING_KEY is DETECTED at stage time (CodeSigningKeyMissing / CodeSigningKeyMismatch)
// instead of silently shipping unsigned or differently-signed bundles. The matching PRIVATE key
// stays off-host (the env-referenced PEM), never persisted here. It is pinned trust-on-first-use on
// the first signed stage and re-pinned only via the explicit YAOG_BUNDLE_SIGNING_KEY_ROTATE hatch.
type SigningAnchor struct {
	PubKeyPEM string `json:"pub_key_pem"`
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
	// GetNode returns the node with the live telemetry overlay merged, or ErrNotFound. This
	// is the READ path for fleet views (conditions/metrics/last-seen/agent-version are shown
	// live). A read-modify-write CUSTODY caller that will UpsertNode the result MUST use
	// GetNodeRecord instead, so the volatile overlay is never baked into the durable record.
	GetNode(ctx context.Context, t TenantID, nodeID string) (Node, error)
	// GetNodeRecord returns the DURABLE node registry record WITHOUT the volatile telemetry
	// overlay, or ErrNotFound. It is the read a custody read-modify-write MUST use before
	// UpsertNode: a heartbeat's live conditions/metrics — which GetNode merges for fleet
	// views — would otherwise be persisted into the durable record (a possibly-dead node
	// would bake in stale "healthy" telemetry). GetNode stays the overlay-merged read.
	GetNodeRecord(ctx context.Context, t TenantID, nodeID string) (Node, error)
	// ListNodes returns all nodes for the tenant (stable order by NodeID).
	ListNodes(ctx context.Context, t TenantID) ([]Node, error)
	// SetAppliedGeneration records what an agent reported applying (the applied
	// generation, the manifest checksum, the free-form health string, the agent's
	// reported build version — "" from a legacy agent leaves the stored version unchanged —
	// and the structured conditions set, server-stamped with observedAt; a nil/empty
	// conditions slice clears the stored set).
	SetAppliedGeneration(ctx context.Context, t TenantID, nodeID string, gen int64, checksum, health, agentVersion string, conditions []runtimecontract.Condition, observedAt time.Time) error
	// RecordTelemetry records a LIVE health heartbeat (beta9-smoke-hardening plan-1): it writes ONLY
	// the node's structured conditions (server-stamped with observedAt; nil/empty clears the set), the
	// extensible metrics map (replaced wholesale; nil clears it), its last-seen time, and — when
	// non-empty — its reported agent build version. It is a strict subset of SetAppliedGeneration that
	// DELIBERATELY does NOT touch AppliedGeneration / LastChecksum / LastHealth / DesiredGeneration:
	// telemetry is observability, kept strictly separate from deploy custody, so a heartbeat can never
	// advance or regress a node's applied generation. Returns ErrNotFound if the node does not exist.
	RecordTelemetry(ctx context.Context, t TenantID, nodeID string, conditions []runtimecontract.Condition, metrics map[string]json.RawMessage, agentVersion string, observedAt time.Time) error
	// TouchLastSeen records that the agent for nodeID checked in at the given time.
	TouchLastSeen(ctx context.Context, t TenantID, nodeID string, at time.Time) error
	// QueryTelemetryHistory returns the node's retained resource-history samples within [from, to]
	// (inclusive), sorted by time (plan-2). Bounded by the operator's configured per-node cap; an empty
	// result when history is disabled (cap 0) or the node has no samples. Observability only — the
	// samples carry no endpoint/IP/key material. Distinct from RecordTelemetry (the live overlay): this
	// is the durable, bounded backing for the node-detail CPU/RAM charts.
	QueryTelemetryHistory(ctx context.Context, t TenantID, nodeID string, from, to time.Time) ([]ResourceSample, error)

	// --- Topology (public-keys-only) ---

	// PutTopology stores a new topology version (public-keys-only JSON) and returns
	// the stored record with its assigned Version. The version is also retained in
	// the bounded history (TopologyHistoryLimit; oldest pruned).
	PutTopology(ctx context.Context, t TenantID, json []byte) (TopologyRecord, error)
	// GetTopology returns the current topology, or ErrNotFound.
	GetTopology(ctx context.Context, t TenantID) (TopologyRecord, error)
	// ListTopologyVersions returns the retained versions, newest first
	// (≤ TopologyHistoryLimit entries; empty slice before the first PutTopology).
	// Only COMMITTED versions appear: a crash orphan (retained ahead of the
	// current record) is invisible, the current record is always present, and a
	// corrupt retained entry is skipped rather than failing the list — this is
	// the operator's recovery surface and must stay serviceable.
	ListTopologyVersions(ctx context.Context, t TenantID) ([]TopologyVersionInfo, error)
	// GetTopologyVersion returns one retained version, or ErrNotFound (unknown,
	// already pruned, or never committed). The current record's version is always
	// servable.
	GetTopologyVersion(ctx context.Context, t TenantID, version int64) (TopologyRecord, error)

	// --- Bundles + generation ---

	// StageBundle stores a node's bundle as the staged (not-yet-current) version.
	// Staging replaces any prior staged bundle for that node.
	StageBundle(ctx context.Context, t TenantID, b SignedBundle) error
	// ReplaceStagedSet publishes one exact candidate set. It invalidates the prior durable seal
	// before mutating any component record, replaces/prunes the staged bundles and optional
	// keystone manifest, then writes the seal LAST. The returned purged IDs are stable-ordered.
	// An empty set clears every active staged record and seal. This is the production staging
	// primitive; StageBundle remains for focused/legacy store callers.
	ReplaceStagedSet(ctx context.Context, t TenantID, set StagedSet) (purged []string, err error)
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
	// PurgeEnrollmentTokensForNode deletes every enrollment token scoped to nodeID
	// within the tenant (consumed or not), returning the count removed. Called on
	// revoke so a still-outstanding token cannot resurrect a revoked node. Absent
	// tokens are not an error (returns 0, nil).
	PurgeEnrollmentTokensForNode(ctx context.Context, t TenantID, nodeID string) (int, error)

	// --- WebAuthn assertion challenges (login + enrollment proof) ---

	// CreateAssertionChallenge stores a single-use, subject-scoped, TTL assertion challenge
	// (by its hash). It issues a random nonce for passkey login (the password+passkey
	// 2FA leg, passwordless begin, or disable re-auth). Expired records are purged on every
	// create so abandoned prompts do not accumulate indefinitely.
	CreateAssertionChallenge(ctx context.Context, t TenantID, challenge AssertionChallenge, now time.Time) error
	// ReplaceAssertionChallengeForSubject stores an assertion challenge after atomically deleting
	// every prior challenge for the same subject (and every expired challenge). Browser WebAuthn
	// enrollment uses this bounded form so repeated/cancelled attempts leave at most one live
	// record per actor+purpose; ordinary login retains concurrent-challenge behavior.
	ReplaceAssertionChallengeForSubject(ctx context.Context, t TenantID, challenge AssertionChallenge, now time.Time) error
	// ConsumeAssertionChallenge atomically validates and burns an assertion challenge by DELETING
	// it: it returns ErrChallengeInvalid if no challenge matches challengeHash, if its
	// Subject != subject, or if it is expired (relative to now); otherwise it deletes
	// the record and returns nil. Single-use is enforced atomically by the delete under
	// the store lock, so a captured assertion cannot be replayed (the record is gone) and
	// two concurrent logins cannot both consume the same challenge. An expired record
	// encountered here is also deleted (lazy GC); a wrong-subject record is left intact
	// (it may be another subject's valid challenge — not the caller's to burn).
	ConsumeAssertionChallenge(ctx context.Context, t TenantID, challengeHash, subject string, now time.Time) error

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

	// CompareAndSetOperatorCredential conditionally pins/replaces the keystone under one store
	// lock. expected==nil requires that no credential is pinned; otherwise the current record
	// must exactly equal *expected. A mismatch returns ErrOperatorCredentialChanged without
	// mutation. This is the write primitive for the handler's read/classify/write transition.
	CompareAndSetOperatorCredential(ctx context.Context, t TenantID, expected *OperatorCredential, next OperatorCredential) error
	// GetOperatorCredential returns the tenant's pinned operator credential, or
	// ErrNotFound when none is pinned (keystone OFF — behave as today).
	GetOperatorCredential(ctx context.Context, t TenantID) (OperatorCredential, error)
	// CreatePendingKeystoneTransition durably creates the keystone transition write-ahead marker.
	// It never overwrites a different unresolved event; an identical EventID/content retry is
	// idempotent. CompareAndSetKeystoneCredential writes it before the credential CAS.
	CreatePendingKeystoneTransition(ctx context.Context, t TenantID, pending PendingKeystoneTransition) error
	// GetPendingKeystoneTransition returns the durable marker, or ErrNotFound.
	GetPendingKeystoneTransition(ctx context.Context, t TenantID) (PendingKeystoneTransition, error)
	// DeletePendingKeystoneTransition removes the marker after reconciliation only when its EventID
	// matches. An already-absent marker is an idempotent success; a different event fails closed.
	DeletePendingKeystoneTransition(ctx context.Context, t TenantID, eventID string) error
	// PutSignedTrustList stores (replacing any prior) the STAGED membership trust-list — the
	// to-be-signed manifest CompileAndStage builds and the operator signs off-host. It is NOT what
	// /config serves; staging it must never disturb the live fleet (the served slot below).
	PutSignedTrustList(ctx context.Context, t TenantID, s StoredTrustList) error
	// GetCurrentSignedTrustList returns the tenant's visible staged trust-list: normally the
	// to-be-signed / just-signed manifest of the pending generation, and after promote/unchanged
	// cleanup the last manifest behind an explicitly non-promotable historical seal. PromoteStaged
	// copies only a non-historical exact candidate into the SERVED slot. (The name is historical;
	// this is never the agent-served slot — that is GetServedTrustList.)
	GetCurrentSignedTrustList(ctx context.Context, t TenantID) (StoredTrustList, error)
	// GetLastStagedTrustList returns the newest manifest ever fully written by a stage, whether or
	// not it is still active. CompileAndStage uses this internal-history view only to preserve the
	// monotonic keystone epoch across an abandoned/cleared stage. It is never served to agents and
	// never authorizes promotion.
	GetLastStagedTrustList(ctx context.Context, t TenantID) (StoredTrustList, error)
	// GetServedTrustList returns the tenant's SERVED (last-promoted) signed membership trust-list —
	// the one /config hands to nodes — or ErrNotFound when nothing has been promoted under a
	// keystone yet. It is updated ONLY by PromoteStaged (copied from the staged slot once its
	// signature has verified), so STAGING a new deploy never disturbs what the live fleet is served.
	GetServedTrustList(ctx context.Context, t TenantID) (StoredTrustList, error)
	// GetServedConfig is the ATOMIC snapshot /config serves a node: its current promoted bundle,
	// whether the keystone is ON, and (when ON) the served signed trust-list — all read under a
	// single lock so a concurrent PromoteStaged can never expose a torn (old-bundle, new-manifest)
	// pair. Returns ErrNotFound when the node has no current bundle.
	GetServedConfig(ctx context.Context, t TenantID, nodeID string) (ServedConfig, error)

	// --- Operators + sessions (operator login, plan-5.2) ---

	// PutOperator creates or replaces an operator account (matched by Username). It is
	// the persistence step behind `yaog-server create-operator`. The plaintext password
	// is never passed here — Operator carries only the argon2id PHC hash.
	PutOperator(ctx context.Context, t TenantID, op Operator) error
	// CompareAndSetLoginCredential updates only the operator's login-credential field (plus
	// UpdatedAt) under one store lock. The current value must exactly match expected (nil-aware),
	// otherwise ErrLoginCredentialChanged is returned without mutation. This avoids a long browser
	// ceremony writing back a stale whole Operator and clobbering a concurrent password/TOTP change.
	CompareAndSetLoginCredential(ctx context.Context, t TenantID, username string, expected, next *LoginCredential, now time.Time) error
	// CompareAndSetTOTPState updates only the operator's TOTP secret/replay watermark (plus UpdatedAt)
	// under one store lock. A mismatch returns ErrTOTPStateChanged without mutation. It preserves the
	// password and login credential even when either changed during the TOTP ceremony.
	CompareAndSetTOTPState(ctx context.Context, t TenantID, username string, expected, next TOTPState, now time.Time) error
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
	// GetSigningAnchor returns the tenant's pinned bundle-signing public key, or ErrNotFound when
	// none is pinned (a never-signed fleet, or before the first signed stage). PutSigningAnchor
	// pins/replaces it. See SigningAnchor.
	GetSigningAnchor(ctx context.Context, t TenantID) (SigningAnchor, error)
	PutSigningAnchor(ctx context.Context, t TenantID, a SigningAnchor) error
}
