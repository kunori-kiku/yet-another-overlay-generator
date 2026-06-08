// Package trustlist is the security keystone of YAOG's off-host trust model.
//
// A trust list is a small, user-authored document that names the members of a
// tenant overlay (node IDs paired with their WireGuard public keys) plus a
// monotonic epoch. The user signs the trust list OFF-HOST — ideally with a
// hardware authenticator (a WebAuthn/FIDO2 passkey) so the signing key never
// lives on any node — and nodes verify the signature OFFLINE against a pinned
// credential they were provisioned with out of band. A node that cannot verify
// the trust list against its pinned anchor refuses to act on it (fail-closed).
//
// This package is STDLIB-ONLY and imports internal/bundlesig one way (for the
// Ed25519 detached-signature primitives and the PKIX PEM helpers). bundlesig
// must never import trustlist.
//
// The verifier (Verify in verify.go) is the single function nodes embed; its
// correctness is paramount. It dispatches on the PINNED credential's algorithm
// (never the attacker-supplied artifact's), guards against algorithm confusion,
// and binds the WebAuthn assertion to the exact trust-list content.
package trustlist

import (
	"crypto/ecdsa"
	"crypto/ed25519"
)

// Member is one entry in a trust list: a node identity, the WireGuard public
// key that identity is authorized to present, and a digest that binds the
// node's deployed BUNDLE. Field tags pin the on-the-wire JSON names;
// canonicalization (canonical.go) sorts members by NodeID and rejects
// duplicates so the signed bytes are deterministic.
//
// BundleSHA256 is the lowercase-hex SHA-256 of that node's checksums.sha256
// bytes. checksums.sha256 covers install.sh AND every config in the bundle, so
// binding this digest into the OFF-HOST-signed membership makes the off-host
// signature cover what RUNS on the node, not merely the membership list: a
// breached controller that tampers with install.sh changes checksums.sha256,
// which changes this digest, which it cannot re-sign without the off-host key —
// so the node rejects the bundle (see the agent's VerifyMembership). It is the
// keystone's install.sh-coverage fix (plan-5.1 CORRECTION, 2026-06-08).
type Member struct {
	NodeID       string `json:"node_id"`
	WGPublicKey  string `json:"wg_public_key"`
	BundleSHA256 string `json:"bundle_sha256"`
}

// TrustList is the user-authored, signable document. The canonical JSON
// encoding of this struct (see Canonical) is BOTH the trustlist.json artifact
// distributed to nodes AND the exact byte payload that is signed and verified.
//
// There is deliberately NO CreatedAt / timestamp field: it would stamp a
// non-deterministic time.Now() into the canonical bytes, so the same membership
// would sign to DIFFERENT bytes across calls and break the GET-sign-POST
// round-trip. Freshness/identity comes from Epoch (monotonic anti-rollback) and
// the per-member BundleSHA256 digests instead.
type TrustList struct {
	SchemaVersion int      `json:"schema_version"`
	Tenant        string   `json:"tenant"`
	Epoch         int64    `json:"epoch"`
	Members       []Member `json:"members"`
}

// Alg names a signing/verification algorithm. The verifier dispatches on the
// PINNED credential's Alg and rejects any artifact whose Alg disagrees, which is
// the algorithm-confusion guard. Only the values below are recognized; anything
// else (e.g. an RS256 WebAuthn assertion) is rejected explicitly.
type Alg string

const (
	// AlgEd25519 is a raw detached Ed25519 signature over Canonical(tl),
	// produced by the on-host software signer (dev/CI only).
	AlgEd25519 Alg = "ed25519"
	// AlgWebAuthnES256 is a WebAuthn assertion whose credential is an ES256
	// (ECDSA P-256 + SHA-256) key.
	AlgWebAuthnES256 Alg = "webauthn-es256"
	// AlgWebAuthnEdDSA is a WebAuthn assertion whose credential is an Ed25519
	// key.
	AlgWebAuthnEdDSA Alg = "webauthn-eddsa"
)

// SignedTrustList is the detached signature artifact that travels alongside
// trustlist.json. It carries everything a verifier needs given a pinned
// credential.
//
// PublicKey is AUDIT/CONVENIENCE ONLY — a human-readable record of the key the
// signer believes it used. The verifier NEVER trusts this field; it always
// checks the signature against the PINNED credential provisioned out of band.
// Likewise CredentialID is informational for matching/audit.
//
// AuthenticatorData and ClientDataJSON are only populated for the WebAuthn
// algorithms and carry the FIDO2 assertion's CBOR-free components (base64url).
type SignedTrustList struct {
	Alg          Alg    `json:"alg"`
	CredentialID string `json:"credential_id"`
	PublicKey    string `json:"public_key"`
	Signature    string `json:"signature"`

	AuthenticatorData string `json:"authenticator_data,omitempty"`
	ClientDataJSON    string `json:"client_data_json,omitempty"`
}

// PinnedCredential is the out-of-band trust anchor a node is provisioned with.
// It is the ONLY material the verifier trusts. Exactly one of Ed25519Pub /
// ES256Pub is populated, matching Alg. RPID and Origin are the WebAuthn
// relying-party binding values; Origin is advisory on a node (see verify).
type PinnedCredential struct {
	Alg          Alg
	CredentialID string
	Ed25519Pub   ed25519.PublicKey
	ES256Pub     *ecdsa.PublicKey
	RPID         string
	Origin       string
}

// Signer produces a SignedTrustList over a TrustList. KeyID identifies the
// signing key for audit/credential-matching.
type Signer interface {
	Sign(tl TrustList) (SignedTrustList, error)
	KeyID() string
}

// Verifier verifies a SignedTrustList against a TrustList and a pinned
// credential, returning nil only when the artifact is a valid signature over
// Canonical(tl) by the pinned credential (and, for WebAuthn, the assertion
// binds to this exact trust list).
type Verifier interface {
	Verify(tl TrustList, art SignedTrustList, pin PinnedCredential) error
}
