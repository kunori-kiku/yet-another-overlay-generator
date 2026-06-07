package controller

// enrollment.go implements the node-enrollment crypto and ceremony for the
// controller panel (plan-4.2). It turns a single-use, node-scoped enrollment
// token plus a node-generated mTLS CSR into an issued client certificate, and
// records the node's WireGuard PUBLIC key in the registry.
//
// Two cryptographic facts shape this file:
//
//   - WireGuard keys are Curve25519 (Diffie-Hellman only) and CANNOT produce a
//     signature. They therefore cannot serve as a proof-of-possession primitive.
//     The PoP in this ceremony is over the node's mTLS keypair: the node signs
//     its own CSR, and CheckSignature on that CSR proves the node holds the
//     corresponding private key. The WireGuard public key is registered as-is,
//     trusted only insofar as it arrives on the already-PoP'd enrollment call.
//
//   - The controller CA is EPHEMERAL (DevCA): its private key is generated in
//     memory at startup and never persisted. This deliberately bounds the breach
//     surface — there is no on-disk CA key to steal — at the cost that a
//     controller restart invalidates issued certs, so nodes must re-enroll. A
//     persisted/HSM-backed CA is a documented future swap; the issuance shape
//     here does not change.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// enrollTokenBytes is the number of crypto/rand bytes behind a plaintext
// enrollment token. 32 bytes (256 bits) of entropy makes the token unguessable;
// it is base64url-encoded (no padding) for transport and hashed for storage.
const enrollTokenBytes = 32

// serialBits bounds the random certificate serial number. RFC 5280 requires a
// positive serial no longer than 20 octets (160 bits); we use 128 bits of
// crypto/rand, which is comfortably unique and within the limit.
const serialBits = 128

// HashToken returns the hex-encoded SHA-256 of a plaintext enrollment token.
// This is the ONLY representation of a token the controller ever stores: the
// Store keeps TokenHash, never the plaintext, so a store/DB read cannot recover
// a usable token. The Enroll path hashes the presented plaintext through this
// same function before handing it to Store.ConsumeEnrollmentToken, so the lookup
// is hash-vs-hash.
func HashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// NewEnrollmentToken mints a fresh single-use token for nodeID.
//
// It returns the plaintext (to be handed to the node OUT-OF-BAND — e.g. copied
// into the agent's config) and the EnrollmentToken record (to be persisted by
// the operator via Store.CreateEnrollmentToken). The plaintext is never stored:
// only tok.TokenHash (hex SHA-256) lives in the Store. The caller is responsible
// for persisting tok and delivering plaintext; this function performs no I/O.
//
// It panics if the system CSPRNG fails. crypto/rand.Read is backed by the kernel
// getrandom(2) and does not fail in practice; a failure means the platform's
// entropy source is unavailable, in which case minting a security token is
// impossible and there is no safe value to return — failing loud is correct, and
// it keeps the signature panic-or-succeed for the callers and tests built against
// this two-value contract.
func NewEnrollmentToken(nodeID string, ttl time.Duration, now time.Time) (plaintext string, tok EnrollmentToken) {
	raw := make([]byte, enrollTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("controller: system CSPRNG failed generating enrollment token: %v", err))
	}
	plaintext = base64.RawURLEncoding.EncodeToString(raw)
	tok = EnrollmentToken{
		TokenHash:  HashToken(plaintext),
		NodeID:     nodeID,
		ExpiresAt:  now.Add(ttl),
		ConsumedAt: nil,
	}
	return plaintext, tok
}

// DevCA is an ephemeral, self-signed Ed25519 controller certificate authority.
// Its private key is held only in memory and is never written anywhere: a
// controller restart discards it, after which previously issued client certs no
// longer chain and nodes re-enroll. This bounds the blast radius of a controller
// compromise (no CA key at rest) at the cost of cert durability — an intentional
// dev/single-tenant tradeoff. A future persisted or HSM-backed CA is a drop-in
// replacement for this type.
type DevCA struct {
	tenant TenantID
	// caCert is the parsed self-signed CA certificate (the issuer for client certs).
	caCert *x509.Certificate
	// caCertDER is the raw DER of caCert, retained so CACertPEM never re-marshals.
	caCertDER []byte
	// caPriv is the ephemeral CA signing key. It NEVER leaves this process and is
	// never persisted (see the type doc).
	caPriv ed25519.PrivateKey
	// clientCertTTL is how long each issued client cert is valid from its NotBefore.
	clientCertTTL time.Duration
}

// randomSerial returns a positive, cryptographically random certificate serial
// number of serialBits bits. A unique unpredictable serial avoids serial
// collisions and does not leak an issuance counter.
func randomSerial() (*big.Int, error) {
	// rand.Int draws a uniform value in [0, max); we only need positivity and
	// uniqueness, not a fixed bit length, so max = 2^serialBits suffices.
	limit := new(big.Int).Lsh(big.NewInt(1), serialBits)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, err
	}
	// A zero serial is technically valid but unconventional; bump it to 1 so the
	// serial is always strictly positive.
	if serial.Sign() == 0 {
		serial = big.NewInt(1)
	}
	return serial, nil
}

// NewDevCA generates a fresh ephemeral Ed25519 CA for the tenant, valid from now
// for caTTL, and configured to issue client certs valid for clientCertTTL. The
// generated CA private key is retained in the returned *DevCA and never persisted.
func NewDevCA(tenant TenantID, now time.Time, caTTL, clientCertTTL time.Duration) (*DevCA, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("controller: generating CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, fmt.Errorf("controller: generating CA serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "yaog-controller-ca:" + string(tenant),
		},
		NotBefore:             now,
		NotAfter:              now.Add(caTTL),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	// Self-signed: parent == template, public/private both the CA's own keys.
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, fmt.Errorf("controller: self-signing CA cert: %w", err)
	}
	caCert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("controller: parsing CA cert: %w", err)
	}
	return &DevCA{
		tenant:        tenant,
		caCert:        caCert,
		caCertDER:     der,
		caPriv:        priv,
		clientCertTTL: clientCertTTL,
	}, nil
}

// CACertPEM returns the CA certificate as a PEM ("CERTIFICATE") block. It is the
// trust anchor handed back in the enroll response and pinned by the agent, and
// it is the ClientCAs material for plan-4.3's mTLS server.
func (c *DevCA) CACertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.caCertDER})
}

// IssueClientCert validates a node's CSR and issues a client certificate for it.
//
// The CSR's self-signature is the proof-of-possession of the node's mTLS private
// key: CheckSignature confirms the requester holds the key behind csr.PublicKey.
// (This is why PoP is over the mTLS keypair and not the WireGuard key, which is
// DH-only and cannot sign.) The CSR's Common Name MUST equal
// "<tenant>:<nodeID>"; this binds the issued identity to the enrolling node and
// is re-asserted as the issued cert's Subject CN, so a node cannot obtain a cert
// for a name other than the one it is enrolling under.
//
// On success it returns the client cert as a PEM ("CERTIFICATE") block and the
// cert's fingerprint = hex(SHA-256(certDER)), which is recorded as the node's
// MTLSCertFP in the registry.
func (c *DevCA) IssueClientCert(csrDER []byte, nodeID string, now time.Time) (certPEM []byte, fingerprint string, err error) {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, "", fmt.Errorf("controller: parsing CSR: %w", err)
	}
	// Proof-of-possession: the CSR is self-signed by the node's mTLS private key.
	if err := csr.CheckSignature(); err != nil {
		return nil, "", fmt.Errorf("controller: CSR signature invalid (proof-of-possession failed): %w", err)
	}
	// Bind the issued identity to the enrolling node: CN must be "<tenant>:<nodeID>".
	wantCN := string(c.tenant) + ":" + nodeID
	if csr.Subject.CommonName != wantCN {
		return nil, "", fmt.Errorf("controller: CSR CommonName %q does not match required %q", csr.Subject.CommonName, wantCN)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, "", fmt.Errorf("controller: generating client cert serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: wantCN,
		},
		NotBefore:             now,
		NotAfter:              now.Add(c.clientCertTTL),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	// Issuer is the CA; the certified public key is the CSR's; signed by caPriv.
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.caCert, csr.PublicKey, c.caPriv)
	if err != nil {
		return nil, "", fmt.Errorf("controller: issuing client cert: %w", err)
	}
	sum := sha256.Sum256(der)
	fingerprint = hex.EncodeToString(sum[:])
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return certPEM, fingerprint, nil
}

// EnrollRequest is the node's enrollment payload: the plaintext enrollment
// token, the claimed NodeID, the DER-encoded mTLS CSR (self-signed, carrying the
// PoP), and the node's WireGuard PUBLIC key (registered as-is; never a private key).
type EnrollRequest struct {
	Token       string
	NodeID      string
	CSRDER      []byte
	WGPublicKey string
}

// EnrollResult is returned to a successfully enrolled node: its issued client
// cert (PEM), the CA cert (PEM) to pin as the trust anchor, and the issued cert's
// SHA-256 fingerprint (also stored as the node's MTLSCertFP).
type EnrollResult struct {
	ClientCertPEM []byte
	CACertPEM     []byte
	Fingerprint   string
}

// Enroll runs the full enrollment ceremony for one node:
//
//  1. Atomically BURN the enrollment token (ConsumeEnrollmentToken): it validates
//     the token (hash, node scope, expiry) and marks it consumed under the store
//     lock. Single-use is enforced here, so two concurrent enrollments with the
//     same token cannot both pass this step.
//  2. Issue the client cert from the CSR (proof-of-possession + CN binding).
//  3. Register the node (WG PUBLIC key + mTLS fingerprint) as NodeApproved.
//  4. Append an audit entry for the enrollment.
//
// IMPORTANT — single-use ordering: the token is burned in step 1, BEFORE the
// cert is issued in step 2. If a later step fails (e.g. a malformed CSR), the
// token is NOT un-burned: the same token cannot be retried. This is deliberate.
// Single-use is the safety property we are protecting; making the burn
// best-effort-reversible would reopen the replay window. To retry after a
// post-burn failure, the operator issues a fresh token. The burn-first ordering
// trades a small operator inconvenience for a hard single-use guarantee.
func Enroll(ctx context.Context, store Store, ca *DevCA, t TenantID, req EnrollRequest, now time.Time) (EnrollResult, error) {
	// (a) Atomically validate-and-burn the token. On any token error (invalid,
	// expired, or already consumed) we return immediately without touching the CA
	// or registry — an unauthorized caller learns nothing and changes nothing.
	if err := store.ConsumeEnrollmentToken(ctx, t, HashToken(req.Token), req.NodeID, now); err != nil {
		return EnrollResult{}, err
	}

	// (b) Issue the client cert: this checks the CSR PoP and the CN binding. A
	// failure here leaves the token burned (see the ordering note above).
	certPEM, fingerprint, err := ca.IssueClientCert(req.CSRDER, req.NodeID, now)
	if err != nil {
		return EnrollResult{}, err
	}

	// (c) Register the node with its WireGuard PUBLIC key (as-is) and the issued
	// cert fingerprint, marked approved and stamped with the enrollment time.
	node := Node{
		NodeID:      req.NodeID,
		WGPublicKey: req.WGPublicKey,
		MTLSCertFP:  fingerprint,
		Status:      NodeApproved,
		EnrolledAt:  now,
	}
	if err := store.UpsertNode(ctx, t, node); err != nil {
		return EnrollResult{}, fmt.Errorf("controller: registering enrolled node: %w", err)
	}

	// (d) Audit the enrollment. The actor is the agent itself (the enroll call is
	// authenticated by the burned token, not by an operator session).
	if _, err := store.AppendAudit(ctx, t, AuditEntry{
		Timestamp: now,
		Actor:     "agent:" + req.NodeID,
		Action:    "enroll",
		NodeID:    req.NodeID,
	}); err != nil {
		return EnrollResult{}, fmt.Errorf("controller: appending enroll audit: %w", err)
	}

	return EnrollResult{
		ClientCertPEM: certPEM,
		CACertPEM:     ca.CACertPEM(),
		Fingerprint:   fingerprint,
	}, nil
}
