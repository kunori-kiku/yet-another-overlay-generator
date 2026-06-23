package api

// wire_controller.go is the controller HTTP wire contract: the *JSON
// request/response struct types that define the on-the-wire shape of every
// controller endpoint (the agent and operator namespaces). Collecting them in
// one file makes the surface that frontend/src/types/controller.ts mirrors
// surveyable in a single place. These types are pure DTOs — they carry JSON
// tags and no behavior; the handlers that read and write them live in the
// sibling handler_*.go files.

import (
	"encoding/json"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

// --- JSON request/response types ---

// enrollRequestJSON is the wire form of an enrollment request: the single-use
// enrollment token, the claimed node id, and the node's WireGuard PUBLIC key
// (never a private key).
type enrollRequestJSON struct {
	Token       string `json:"enrollment_token"`
	NodeID      string `json:"node_id"`
	WGPublicKey string `json:"wg_public_key"`
}

// enrollResponseJSON is the wire form of a successful enrollment: the node's issued
// per-node bearer token (returned ONCE, never stored in plaintext) and its node id.
type enrollResponseJSON struct {
	ApiToken string `json:"api_token"`
	NodeID   string `json:"node_id"`
}

// configResponseJSON is the wire form of a node's current bundle: the generation
// plus the bundle files keyed by bundle-relative path, each value base64.
type configResponseJSON struct {
	Generation int64             `json:"generation"`
	Files      map[string]string `json:"files"`
	// RekeyRequested signals the agent that the operator has requested a fleet-wide
	// key rotation: on the next fetch the agent regenerates its WireGuard key,
	// re-registers the new PUBLIC key via POST /rekey, and waits for the operator's
	// redeploy rather than applying this (now stale) bundle.
	RekeyRequested bool `json:"rekey_requested"`
}

// pollResponseJSON is the wire form of a /poll hit: the generation that is now
// current (strictly greater than the caller's ?after=). A timeout returns 204 with
// no body instead.
type pollResponseJSON struct {
	Generation int64 `json:"generation"`
}

// reportRequestJSON is the wire form of an agent apply report.
type reportRequestJSON struct {
	AppliedGeneration int64  `json:"applied_generation"`
	Checksum          string `json:"checksum"`
	Health            string `json:"health"`
	// AgentVersion is the reporting agent's build version (omitempty; "" from a legacy agent).
	AgentVersion string `json:"agent_version,omitempty"`
	// Conditions is the structured feedback set (plan-1); omitempty — absent from a legacy agent.
	Conditions []model.Condition `json:"conditions,omitempty"`
}

// telemetryRequestJSON is the POST /telemetry body (beta9-smoke-hardening plan-1): a LIVE health
// heartbeat. It carries conditions + an extensible metrics map but NO applied_generation/checksum —
// telemetry is observability, kept strictly separate from deploy custody, so a heartbeat updates only
// the node's conditions + last_seen and can never advance/regress its applied generation. Metrics is
// the framework's extension slot (a future probe writes named values); accepted-but-not-yet-persisted.
type telemetryRequestJSON struct {
	Conditions   []model.Condition          `json:"conditions,omitempty"`
	Metrics      map[string]json.RawMessage `json:"metrics,omitempty"`
	AgentVersion string                     `json:"agent_version,omitempty"`
}

// stageResponseJSON is the wire form of a stage result.
type stageResponseJSON struct {
	Staged            []string `json:"staged"`
	SkippedUnenrolled []string `json:"skipped_unenrolled"`
	Generation        int64    `json:"generation"`
}

// generationResponseJSON is the wire form of a promote result.
type generationResponseJSON struct {
	Generation int64 `json:"generation"`
}

// topologyVersionJSON is the wire form of one retained topology version's
// metadata (no payload — GET /topology?version=N serves the bytes).
type topologyVersionJSON struct {
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
	Bytes     int       `json:"bytes"`
}

// topologyVersionsResponseJSON is the wire form of the version list, newest
// first, plus the server's retention bound (so the panel can label the list).
type topologyVersionsResponseJSON struct {
	Versions []topologyVersionJSON `json:"versions"`
	Limit    int                   `json:"limit"`
}

// conditionJSON is the operator-facing view of one structured Node Condition (plan-1's
// model.Condition + the controller's server stamp). It is the curated channel that replaces panel
// string-matching of the free-form last_health line: status drives the badge color, reason is the
// closed CamelCase code, message is the single length-capped human line (NEVER a raw stderr dump).
// observed_at is the controller's receive stamp (server truth, not agent-clock). All plain data; no
// key material. Pure DTO — the projection from controller.NodeCondition lives in handler_deploy.go.
type conditionJSON struct {
	Type       string    `json:"type"`
	Status     string    `json:"status"`
	Reason     string    `json:"reason"`
	Message    string    `json:"message,omitempty"`
	Since      string    `json:"since,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

// nodeJSON is the operator-facing view of one registry node. It deliberately
// exposes NO key material (neither the WG public key bytes nor the API token hash):
// only a boolean that a public key is on file. The operator panel lists fleet state
// without ever seeing secrets.
type nodeJSON struct {
	NodeID            string `json:"node_id"`
	Status            string `json:"status"`
	HasWGPublicKey    bool   `json:"has_wg_public_key"`
	DesiredGeneration int64  `json:"desired_generation"`
	AppliedGeneration int64  `json:"applied_generation"`
	LastChecksum      string `json:"last_checksum"`
	LastHealth        string `json:"last_health"`
	// AgentVersion is the build version the node last reported ("" until the first report from a
	// version-aware agent; the panel renders absent/empty as "unknown").
	AgentVersion string    `json:"agent_version,omitempty"`
	LastSeen     time.Time `json:"last_seen"`
	EnrolledAt   time.Time `json:"enrolled_at"`
	// RekeyRequested is true while the node is pending a key rotation (the operator
	// requested one and the agent has not yet re-registered its new public key). The
	// panel renders a "rekeying" badge from this flag. No key material is exposed.
	RekeyRequested bool `json:"rekey_requested"`
	// InRollout is true when this node is in the agent self-update rollout set — the canary
	// subset, or the whole fleet once promoted (AgentRolloutNodeIDs). It is server-computed
	// so the panel never re-derives canary membership client-side; the per-node update-status
	// chip combines it with the reported AgentVersion vs the configured target. Always present
	// (false when no rollout is configured); the target itself is read from /settings, not echoed
	// per node.
	InRollout bool `json:"in_rollout"`
	// Conditions is the node's structured feedback channel (plan-1/2). omitempty: a legacy agent /
	// a node that has reported none round-trips with the field absent, and the panel renders no
	// strip — byte-identical served JSON to the pre-conditions shape. Curated + length-capped at
	// ingest (handler_agent); this view re-serializes only (see mapConditions in handler_deploy.go).
	Conditions []conditionJSON `json:"conditions,omitempty"`
}

// revokeRequestJSON is the operator's request to revoke (evict) a node: the target
// node id. Revocation flips the node to NodeRevoked and clears its API token so the
// node's bearer credential stops resolving immediately.
type revokeRequestJSON struct {
	NodeID string `json:"node_id"`
}

// revokeResponseJSON confirms a revoke: the node id and a revoked flag (always true
// on success, so a caller can branch without re-reading the registry).
type revokeResponseJSON struct {
	NodeID  string `json:"node_id"`
	Revoked bool   `json:"revoked"`
}

// clearRekeyResponseJSON confirms a clear-rekey: the node id and whether a pending rekey flag
// was actually cleared (false = idempotent no-op, the node had no pending rekey).
type clearRekeyResponseJSON struct {
	NodeID  string `json:"node_id"`
	Cleared bool   `json:"cleared"`
}

// auditEntryJSON is the operator-facing wire form of one audit entry. It is an
// explicit snake_case DTO (controller.AuditEntry has no json tags, so it would
// otherwise serialize as PascalCase) that exposes only the operator-relevant fields —
// the chain internals (PrevHash/Hash) are NOT leaked; their integrity is conveyed by
// auditResponseJSON.Verified.
type auditEntryJSON struct {
	Seq       int64     `json:"seq"`
	Timestamp time.Time `json:"timestamp"`
	Actor     string    `json:"actor"`
	Action    string    `json:"action"`
	NodeID    string    `json:"node_id"`
}

// auditResponseJSON is the operator-facing view of the audit chain: the entries in
// Seq order plus whether the hash chain verifies intact.
type auditResponseJSON struct {
	Entries  []auditEntryJSON `json:"entries"`
	Verified bool             `json:"verified"`
}

// enrollmentTokenRequestJSON is the operator's request to mint a single-use
// enrollment token for a node, with a TTL in seconds.
type enrollmentTokenRequestJSON struct {
	NodeID     string `json:"node_id"`
	TTLSeconds int64  `json:"ttl_seconds"`
}

// enrollmentTokenResponseJSON returns the freshly minted plaintext enrollment
// token ONCE. The controller stores only its hash, so this is the only chance to
// capture the plaintext.
type enrollmentTokenResponseJSON struct {
	Token string `json:"token"`
	// Warning is a non-blocking advisory (plan-6): set when the node-id has no
	// matching node in the stored design, so the operator learns the token will mint
	// fine but the node will be SKIPPED at stage until it is added to the design.
	// Empty when the node-id is present (or no design is stored yet).
	Warning string `json:"warning,omitempty"`
}

// rekeyAllResponseJSON is the operator-facing result of a fleet-wide key-rotation
// request: the count of APPROVED nodes flagged for rotation.
type rekeyAllResponseJSON struct {
	Requested int `json:"requested"`
}

// rekeyRequestJSON is the agent's re-registration of its rotated WireGuard PUBLIC
// key (never a private key). The node is the bearer token's node, never the body.
type rekeyRequestJSON struct {
	WGPublicKey string `json:"wg_public_key"`
}

// rekeyResponseJSON confirms an agent rekey re-registration.
type rekeyResponseJSON struct {
	OK bool `json:"ok"`
}

// operatorCredentialRequestJSON is the operator's request to pin the off-host signing
// credential (the keystone trust anchor). public_key_pem is the PKIX ("PUBLIC KEY")
// PEM; alg selects how it is parsed (ed25519 / webauthn-es256 / webauthn-eddsa);
// rpid/origin are the WebAuthn relying-party binding values (empty for raw Ed25519).
type operatorCredentialRequestJSON struct {
	Alg          string `json:"alg"`
	CredentialID string `json:"credential_id"`
	PublicKeyPEM string `json:"public_key_pem"`
	RPID         string `json:"rpid"`
	Origin       string `json:"origin"`
	// Rotate acknowledges that this pin REPLACES a different already-pinned credential, which
	// strands every enrolled node until each is re-provisioned out of band AND a fresh deploy is
	// signed under the new key. Without it, a changed credential is refused (the anti-footgun
	// analogue of YAOG_BUNDLE_SIGNING_KEY_ROTATE). Ignored on a first pin or an idempotent re-pin.
	Rotate bool `json:"rotate"`
}

// operatorCredentialPinResultJSON is the POST /operator-credential result: ok always true on
// success; rotated true only when this pin REPLACED a different credential; unchanged true on an
// idempotent re-pin of the same credential; redeploy_required true when (after a rotation) the
// served fleet is still signed under the old key and needs a fresh signed deploy.
type operatorCredentialPinResultJSON struct {
	OK               bool `json:"ok"`
	Rotated          bool `json:"rotated"`
	Unchanged        bool `json:"unchanged,omitempty"`
	RedeployRequired bool `json:"redeploy_required,omitempty"`
}

// operatorCredentialStatusJSON is the GET /operator-credential body: the SERVER-authoritative
// keystone status the panel reflects (so a browser-local cache is never the source of the
// "enrolled" display). It carries ONLY non-secret public identifiers — never the PEM body, never
// any private key. redeploy_required signals a rotated-but-not-redeployed fleet.
type operatorCredentialStatusJSON struct {
	Pinned           bool   `json:"pinned"`
	Alg              string `json:"alg,omitempty"`
	CredentialID     string `json:"credential_id,omitempty"`
	RPID             string `json:"rpid,omitempty"`
	Origin           string `json:"origin,omitempty"`
	Fingerprint      string `json:"fingerprint,omitempty"`
	RedeployRequired bool   `json:"redeploy_required,omitempty"`
}

// trustListResponseJSON returns the canonical bytes the operator must sign
// (base64-encoded) plus the membership epoch those bytes carry. The panel signs
// challenge = SHA256(decoded trustlist_json).
type trustListResponseJSON struct {
	TrustListJSON string `json:"trustlist_json"`
	Epoch         int64  `json:"epoch"`
}

// trustListSignatureRequestJSON is the operator's submission of a signed trust-list:
// the base64 of the canonical bytes the operator actually signed (substitution guard)
// plus the trustlist.SignedTrustList detached-signature artifact.
type trustListSignatureRequestJSON struct {
	TrustListJSON string                    `json:"trustlist_json"`
	Signed        trustlist.SignedTrustList `json:"signed"`
}

// compilePreviewResponseJSON is the read-only compile-preview wire shape. It promotes the
// same fields as the air-gap CompileResponse (so the panel reuses CompilePreview/EdgeEditor
// verbatim) and adds skipped_unenrolled — the node IDs present in the topology but dropped
// from the render because they are not yet enrolled. The embedded *CompileResponse is nil
// when nothing is enrolled, so its fields are absent and only skipped_unenrolled is sent.
type compilePreviewResponseJSON struct {
	*CompileResponse
	SkippedUnenrolled []string `json:"skipped_unenrolled"`
}
