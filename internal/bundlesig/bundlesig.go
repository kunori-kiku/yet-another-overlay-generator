// Package bundlesig is the single authority for canonical bundle
// serialization and Ed25519 signing of per-node install bundles. It is a
// leaf package depending only on the Go standard library, shared by the
// export path and (in later phases) the controller and the node agent.
//
// The contract it enforces: a signature is always produced over the
// canonical checksums byte string emitted by Canonicalize, and NEVER over
// compiler.go's computeChecksum (which uses a non-canonical fmt.Sprintf("%v")
// digest that is unsafe to sign). Canonicalize is deterministic and
// order-independent, so the same set of bundle files always yields the same
// bytes regardless of map iteration order, and that byte string is exactly
// the content written to the bundle's checksums.sha256 file (sha256sum -c
// format, sorted by path).
package bundlesig

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// EnvSigningKey is the environment variable that opts a bundle export into
// Ed25519 signing. When set it must point to a filesystem path holding an
// Ed25519 private key in PKCS#8 PEM form (e.g. the output of
// `openssl genpkey -algorithm ed25519`). When unset or empty, exports stay
// hash-only — today's back-compatible default. This is the single source of
// truth for the variable name; the export path, the install-script renderer,
// and the self-extracting installer all read it through the ConfigSigner
// constructor LoadConfigSignerFromEnv (which delegates to LoadSigningFromEnv).
const EnvSigningKey = "YAOG_BUNDLE_SIGNING_KEY"

// Signing carries loaded Ed25519 signing material: the private key used to sign
// the canonical bundle digest (or the self-extracting payload) and the PKIX
// public-key PEM that is pinned into install.sh / the wrapper for verification.
// It is produced by LoadSigningFromEnv. A nil *Signing means signing is off.
type Signing struct {
	// Priv signs the canonical checksums (export) or the tar.gz payload (wrapper).
	Priv ed25519.PrivateKey
	// PubKeyPEM is the verifying public key as a PKIX ("PUBLIC KEY") PEM block,
	// identical for every node bundle. It is both shipped as signing-pubkey.pem
	// and embedded into install.sh as the pinned trust anchor.
	PubKeyPEM []byte
}

// LoadSigningFromEnv reads EnvSigningKey and returns the loaded *Signing.
//
// It returns (nil, nil) when the variable is unset or empty: signing is off and
// bundles stay hash-only exactly as before (opt-in). A non-empty-but-unreadable
// or unparsable key path is a configuration error returned to the caller, so the
// export fails closed rather than silently shipping unsigned bundles. This is the
// one server/export-side entry point for signing configuration; the node agent
// never calls it — it verifies against the pinned public key using Verify.
func LoadSigningFromEnv() (*Signing, error) {
	keyPath := strings.TrimSpace(os.Getenv(EnvSigningKey))
	if keyPath == "" {
		return nil, nil
	}
	pemBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("bundlesig: read signing key (%s=%s): %w", EnvSigningKey, keyPath, err)
	}
	priv, err := LoadPrivateKeyPEM(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("bundlesig: parse signing key (%s=%s): %w", EnvSigningKey, keyPath, err)
	}
	return &Signing{
		Priv:      priv,
		PubKeyPEM: MarshalPublicKeyPEM(priv.Public().(ed25519.PublicKey)),
	}, nil
}

// ConfigSigner is the tier-1 config-bundle signing seam: it produces a detached
// Ed25519 signature over a message (the canonical checksums bytes for the export
// path, or the self-extracting tar.gz payload for the installer wrapper) and
// exposes the PKIX public-key PEM pinned into install.sh / the wrapper as the
// verification anchor.
//
// The default implementation is *Signing — an in-process Ed25519 key loaded from
// EnvSigningKey. A future host-isolated backend (HashiCorp Vault / OpenBao
// transit, GCP Cloud KMS, a YubiHSM) plugs in by implementing this SAME interface
// in its own package, with no change to the export / render / installer call
// sites: only the constructor (LoadConfigSignerFromEnv, or a caller that injects a
// ConfigSigner) changes. The interface lives here — not in a package that pulls a
// KMS SDK — so bundlesig keeps its stdlib-only contract; the heavy KMS clients
// live in their own packages that import bundlesig for this seam.
//
// Sign returns an error because a networked backend can fail mid-call; the
// in-process Ed25519 signer never does (its error is always nil). When a
// networked backend is actually introduced, Sign should also take a
// context.Context for cancellation/deadline; it is omitted now to keep the
// in-process default and its call sites minimal (this is an internal interface
// with no external consumers, so widening it later is a contained change).
type ConfigSigner interface {
	// Sign returns the raw 64-byte Ed25519 signature over message.
	Sign(message []byte) (sig []byte, err error)
	// PublicKeyPEM returns the PKIX ("PUBLIC KEY") PEM of the verifying key,
	// identical for every node bundle.
	PublicKeyPEM() []byte
}

// Sign implements ConfigSigner using the in-process Ed25519 private key. The
// error is always nil — ed25519.Sign cannot fail for a valid key — but the
// signature matches the interface so networked backends can report failures.
func (s *Signing) Sign(message []byte) ([]byte, error) {
	return Sign(message, s.Priv), nil
}

// PublicKeyPEM implements ConfigSigner, returning the pinned PKIX public-key PEM.
func (s *Signing) PublicKeyPEM() []byte {
	return s.PubKeyPEM
}

// Compile-time assertion that the in-process signer satisfies the seam.
var _ ConfigSigner = (*Signing)(nil)

// LoadConfigSignerFromEnv returns the configured tier-1 ConfigSigner, or
// (nil, nil) when signing is not configured (opt-in; bundles stay hash-only,
// byte-for-byte today's output). Today the only backend is the in-process
// Ed25519 signer loaded from EnvSigningKey; future KMS/HSM backends are selected
// here (or injected by the caller) without touching the export / render /
// installer call sites.
//
// It returns an explicit nil INTERFACE (never a non-nil interface wrapping a nil
// *Signing) when signing is off, so callers can compare the result against nil
// directly without tripping Go's typed-nil gotcha.
func LoadConfigSignerFromEnv() (ConfigSigner, error) {
	signing, err := LoadSigningFromEnv()
	if err != nil {
		return nil, err
	}
	if signing == nil {
		return nil, nil
	}
	return signing, nil
}

// Canonicalize produces the canonical checksums byte string for a bundle.
//
// For every (path, content) pair in files it computes the SHA-256 of the
// content and emits one line in sha256sum format:
//
//	<64-hex-lowercase-sha256><two spaces><path>\n
//
// Lines are sorted by path in byte order, separated and terminated by a
// single LF (no CR). The result is the exact content of the bundle's
// checksums.sha256 file, suitable both for `sha256sum -c` and as the input
// to Sign/Verify. The output is deterministic and independent of the map's
// iteration order.
func Canonicalize(files map[string]string) []byte {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	// Sort by raw byte order. Go string comparison is byte-wise, matching
	// the ordering a deterministic on-disk checksums file needs.
	sort.Strings(paths)

	var b strings.Builder
	for _, path := range paths {
		sum := sha256.Sum256([]byte(files[path]))
		// "%x" lowercases hex; two spaces are the sha256sum binary-mode
		// separator that `sha256sum -c` expects.
		fmt.Fprintf(&b, "%x  %s\n", sum, path)
	}
	return []byte(b.String())
}

// Sign returns the raw 64-byte Ed25519 signature over the canonical bytes.
// The input is expected to be the output of Canonicalize.
func Sign(canonical []byte, priv ed25519.PrivateKey) []byte {
	return ed25519.Sign(priv, canonical)
}

// Verify reports whether sig is a valid Ed25519 signature over canonical for
// pub. It returns false (rather than panicking) on malformed inputs such as a
// wrong-length signature or public key.
func Verify(canonical, sig []byte, pub ed25519.PublicKey) bool {
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, canonical, sig)
}

// LoadPrivateKeyPEM parses an Ed25519 private key from a PKCS#8 PEM block, the
// format produced by `openssl genpkey -algorithm ed25519`. It returns an error
// if the PEM is malformed or does not contain an Ed25519 key.
func LoadPrivateKeyPEM(data []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("bundlesig: no PEM block found in private key data")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("bundlesig: parse PKCS#8 private key: %w", err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("bundlesig: private key is %T, want ed25519.PrivateKey", key)
	}
	return priv, nil
}

// MarshalPublicKeyPEM encodes an Ed25519 public key as a PKIX ("PUBLIC KEY")
// PEM block. The output is consumable by `openssl pkeyutl -verify -pubin` for
// install.sh signature verification.
func MarshalPublicKeyPEM(pub ed25519.PublicKey) []byte {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		// MarshalPKIXPublicKey only fails on an unsupported key type; a
		// valid ed25519.PublicKey is always supported, so this is
		// unreachable for correct callers.
		panic(fmt.Sprintf("bundlesig: marshal PKIX public key: %v", err))
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}
