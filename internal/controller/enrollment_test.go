package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"testing"
	"time"
)

// newCSR generates a fresh Ed25519 mTLS keypair and a self-signed PKCS#10 CSR with
// the given CommonName, returning the DER-encoded CSR. The CSR's self-signature
// (made with the freshly generated private key) is the proof-of-possession the
// controller verifies — WireGuard's Curve25519 keys cannot sign, so the mTLS key
// is what proves possession. The private key is discarded: the test only needs the
// CSR bytes, and the controller never sees a private key.
func newCSR(t *testing.T, commonName string) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: commonName},
	}, priv)
	if err != nil {
		t.Fatalf("CreateCertificateRequest: %v", err)
	}
	return der
}

// verifiesToCA reports whether certPEM chains to the CA whose pool PEM is caPoolPEM,
// presented as a client certificate (ExtKeyUsageClientAuth). It returns a non-nil
// error describing the first failure so callers can assert on rejection too.
func verifiesToCA(certPEM, caPoolPEM []byte, at time.Time) error {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return errors.New("no PEM block in client cert")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPoolPEM) {
		return errors.New("CA pool PEM did not parse")
	}
	_, err = cert.Verify(x509.VerifyOptions{
		Roots:       pool,
		CurrentTime: at,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	return err
}

// TestDevCAIssueAndVerify covers the dev controller-CA: a CSR whose CN is
// "<tenant>:<nodeID>" yields a client cert that x509-verifies against the CA pool
// with ExtKeyUsageClientAuth; a CN mismatch is refused; a CSR whose self-signature
// has been corrupted (a flipped DER byte) is refused (proof-of-possession fails).
func TestDevCAIssueAndVerify(t *testing.T) {
	const tnt = TenantID("ca-tenant")
	now := time.Now()
	ca, err := NewDevCA(tnt, now, time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("NewDevCA: %v", err)
	}

	t.Run("issue-and-verify", func(t *testing.T) {
		csr := newCSR(t, string(tnt)+":node-1")
		certPEM, fp, err := ca.IssueClientCert(csr, "node-1", now)
		if err != nil {
			t.Fatalf("IssueClientCert: %v", err)
		}
		if fp == "" {
			t.Fatalf("IssueClientCert returned empty fingerprint")
		}
		if err := verifiesToCA(certPEM, ca.CACertPEM(), now); err != nil {
			t.Fatalf("issued cert does not verify to CA: %v", err)
		}
	})

	t.Run("cn-mismatch-rejected", func(t *testing.T) {
		// CN names a different node than the nodeID passed to IssueClientCert.
		csr := newCSR(t, string(tnt)+":someone-else")
		if _, _, err := ca.IssueClientCert(csr, "node-1", now); err == nil {
			t.Fatalf("IssueClientCert(CN mismatch): err = nil, want non-nil")
		}
	})

	t.Run("corrupted-signature-rejected", func(t *testing.T) {
		csr := newCSR(t, string(tnt)+":node-1")
		// Flip a byte near the end of the DER, which lands in the signature region,
		// so the CSR self-signature no longer validates (PoP must fail).
		corrupt := make([]byte, len(csr))
		copy(corrupt, csr)
		corrupt[len(corrupt)-1] ^= 0xFF
		if _, _, err := ca.IssueClientCert(corrupt, "node-1", now); err == nil {
			t.Fatalf("IssueClientCert(corrupted sig): err = nil, want non-nil")
		}
	})
}

// TestEnrollHappyPath covers the full enrollment ceremony against MemStore: an
// operator-created token authorizes the node, Enroll burns it, issues a per-node
// mTLS cert, and records the node as approved with its WG public key + cert
// fingerprint, writing an "enroll" audit entry. The returned cert verifies to the
// CA pool.
func TestEnrollHappyPath(t *testing.T) {
	const tnt = TenantID("enroll-tenant")
	ctx := context.Background()
	store := NewMemStore()
	now := time.Now()
	ca, err := NewDevCA(tnt, now, time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("NewDevCA: %v", err)
	}

	plaintext, tok := NewEnrollmentToken("node-1", time.Hour, now)
	if plaintext == "" {
		t.Fatalf("NewEnrollmentToken returned empty plaintext")
	}
	if tok.NodeID != "node-1" {
		t.Fatalf("token NodeID = %q, want node-1", tok.NodeID)
	}
	if tok.TokenHash == "" || tok.TokenHash == plaintext {
		t.Fatalf("token hash must be set and != plaintext (got hash=%q plaintext=%q)", tok.TokenHash, plaintext)
	}
	if err := store.CreateEnrollmentToken(ctx, tnt, tok); err != nil {
		t.Fatalf("CreateEnrollmentToken: %v", err)
	}

	csr := newCSR(t, string(tnt)+":node-1")
	res, err := Enroll(ctx, store, ca, tnt, EnrollRequest{
		Token:       plaintext,
		NodeID:      "node-1",
		CSRDER:      csr,
		WGPublicKey: "wg-pub-node-1",
	}, now)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if res.Fingerprint == "" {
		t.Fatalf("EnrollResult.Fingerprint is empty")
	}
	if err := verifiesToCA(res.ClientCertPEM, ca.CACertPEM(), now); err != nil {
		t.Fatalf("enrolled cert does not verify to CA: %v", err)
	}

	node, err := store.GetNode(ctx, tnt, "node-1")
	if err != nil {
		t.Fatalf("GetNode after enroll: %v", err)
	}
	if node.Status != NodeApproved {
		t.Fatalf("node Status = %q, want %q", node.Status, NodeApproved)
	}
	if node.WGPublicKey != "wg-pub-node-1" {
		t.Fatalf("node WGPublicKey = %q, want wg-pub-node-1", node.WGPublicKey)
	}
	if node.MTLSCertFP != res.Fingerprint {
		t.Fatalf("node MTLSCertFP = %q, want %q (the issued cert FP)", node.MTLSCertFP, res.Fingerprint)
	}

	entries, err := store.ListAudit(ctx, tnt)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	var sawEnroll bool
	for _, e := range entries {
		if e.Action == "enroll" && e.NodeID == "node-1" {
			sawEnroll = true
		}
	}
	if !sawEnroll {
		t.Fatalf("no enroll audit entry for node-1 found in %+v", entries)
	}
}

// requireNotApproved asserts that nodeID is NOT recorded as approved for the
// tenant: either it has no registry record (ErrNotFound — the expected outcome,
// since the ceremony's UpsertNode(approved) is its last write and a failure aborts
// before it) or, defensively, a record whose Status is anything but NodeApproved.
func requireNotApproved(t *testing.T, store Store, tnt TenantID, nodeID string) {
	t.Helper()
	node, err := store.GetNode(context.Background(), tnt, nodeID)
	if errors.Is(err, ErrNotFound) {
		return
	}
	if err != nil {
		t.Fatalf("GetNode(%s): %v", nodeID, err)
	}
	if node.Status == NodeApproved {
		t.Fatalf("node %s is approved after a failed enroll, want not approved", nodeID)
	}
}

// TestEnrollFailures covers the refusal paths: an unknown token leaves the node
// unapproved; re-using an already-burned token returns ErrTokenConsumed; and a CSR
// whose CN does not match "<tenant>:<nodeID>" is refused. After each refusal the
// node must NOT be recorded as approved.
func TestEnrollFailures(t *testing.T) {
	const tnt = TenantID("enroll-fail-tenant")
	ctx := context.Background()
	now := time.Now()
	ca, err := NewDevCA(tnt, now, time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("NewDevCA: %v", err)
	}

	t.Run("unknown-token", func(t *testing.T) {
		store := NewMemStore()
		// No CreateEnrollmentToken: the store has never heard of this token, so the
		// atomic consume that opens the ceremony fails and the node is never created.
		csr := newCSR(t, string(tnt)+":node-1")
		_, err := Enroll(ctx, store, ca, tnt, EnrollRequest{
			NodeID:      "node-1",
			CSRDER:      csr,
			WGPublicKey: "wg-pub-node-1",
		}, now)
		if err == nil {
			t.Fatalf("Enroll(unknown token): err = nil, want non-nil")
		}
		requireNotApproved(t, store, tnt, "node-1")
	})

	t.Run("burned-token-cannot-re-enroll", func(t *testing.T) {
		store := NewMemStore()
		plaintext, tok := NewEnrollmentToken("node-1", time.Hour, now)
		if err := store.CreateEnrollmentToken(ctx, tnt, tok); err != nil {
			t.Fatalf("CreateEnrollmentToken: %v", err)
		}
		req := EnrollRequest{Token: plaintext, NodeID: "node-1", CSRDER: newCSR(t, string(tnt)+":node-1"), WGPublicKey: "wg-pub-node-1"}
		if _, err := Enroll(ctx, store, ca, tnt, req, now); err != nil {
			t.Fatalf("Enroll(first): %v", err)
		}
		// Second enroll with the same (now-burned) token -> ErrTokenConsumed.
		req2 := EnrollRequest{Token: plaintext, NodeID: "node-1", CSRDER: newCSR(t, string(tnt)+":node-1"), WGPublicKey: "wg-pub-node-1"}
		_, err := Enroll(ctx, store, ca, tnt, req2, now)
		if !errors.Is(err, ErrTokenConsumed) {
			t.Fatalf("Enroll(burned token): err = %v, want ErrTokenConsumed", err)
		}
	})

	t.Run("cn-mismatch", func(t *testing.T) {
		store := NewMemStore()
		plaintext, tok := NewEnrollmentToken("node-1", time.Hour, now)
		if err := store.CreateEnrollmentToken(ctx, tnt, tok); err != nil {
			t.Fatalf("CreateEnrollmentToken: %v", err)
		}
		// CSR CN names a different node than EnrollRequest.NodeID.
		csr := newCSR(t, string(tnt)+":wrong-node")
		_, err := Enroll(ctx, store, ca, tnt, EnrollRequest{
			Token:       plaintext,
			NodeID:      "node-1",
			CSRDER:      csr,
			WGPublicKey: "wg-pub-node-1",
		}, now)
		if err == nil {
			t.Fatalf("Enroll(CN mismatch): err = nil, want non-nil")
		}
		requireNotApproved(t, store, tnt, "node-1")
	})
}
