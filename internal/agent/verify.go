package agent

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"sort"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

// VerifyResult records what verification established about a bundle. It is
// surfaced in the agent's status report so an operator can see whether the
// applied bundle was signature-verified or only hash-verified.
type VerifyResult struct {
	// Signed is true when a bundle.sig + signing-pubkey were present and the
	// signature verified against the pinned key.
	Signed bool
	// FileCount is the number of files whose SHA-256 was checked.
	FileCount int
}

// parseChecksums parses checksums.sha256 (bundlesig.Canonicalize / sha256sum
// format: "<64-hex>  <path>\n", two spaces) into a path -> lowercase-hex map. It
// is intentionally strict: a malformed line, a non-64-char hex field, or a
// duplicate path is an error, because this file is the integrity authority and a
// parse ambiguity must never silently weaken a check.
func parseChecksums(data []byte) (map[string]string, error) {
	out := make(map[string]string)
	for i, rawLine := range strings.Split(string(data), "\n") {
		line := rawLine
		if line == "" {
			continue
		}
		// sha256sum binary-mode separator is exactly two spaces between the hex
		// digest and the path. Split on the first occurrence; the path may itself
		// contain spaces, so do not split further.
		idx := strings.Index(line, "  ")
		if idx < 0 {
			return nil, fmt.Errorf("checksums line %d: missing two-space separator: %q", i+1, rawLine)
		}
		hexSum := line[:idx]
		path := line[idx+2:]
		if len(hexSum) != 64 {
			return nil, fmt.Errorf("checksums line %d: digest is %d hex chars, want 64", i+1, len(hexSum))
		}
		if _, err := hex.DecodeString(hexSum); err != nil {
			return nil, fmt.Errorf("checksums line %d: digest is not hex: %w", i+1, err)
		}
		if path == "" {
			return nil, fmt.Errorf("checksums line %d: empty path", i+1)
		}
		if _, dup := out[path]; dup {
			return nil, fmt.Errorf("checksums line %d: duplicate path %q", i+1, path)
		}
		out[path] = strings.ToLower(hexSum)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("checksums.sha256 lists no files")
	}
	return out, nil
}

// parsePinnedPublicKey decodes a PKIX ("PUBLIC KEY") PEM block into an Ed25519
// public key, the same way bundlesig.MarshalPublicKeyPEM produces it and openssl
// consumes it. It is used both for the operator-pinned key and for the bundle's
// own signing-pubkey.pem.
func parsePinnedPublicKey(pemBytes []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("agent: no PEM block in public key")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("agent: parse PKIX public key: %w", err)
	}
	pub, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("agent: pinned key is %T, want ed25519.PublicKey", key)
	}
	return pub, nil
}

// VerifyBundle is the Go-side, fail-closed integrity gate run BEFORE install.sh.
// It is defense in depth: install.sh re-verifies as root, but verifying here
// means a tampered or rolled-back bundle never reaches a root-executed script.
//
// pinnedPubPEM is the operator-configured trust anchor (--pubkey), or nil when no
// key is pinned. Policy:
//
//   - A signature (bundle.sig + signing-pubkey.pem) present in the bundle is
//     always verified, against the pinned key when one is configured, otherwise
//     against the bundle's own signing-pubkey.pem (trust-on-first-supply — the
//     pin is what makes it strong, so configuring --pubkey is recommended).
//   - When a key IS pinned but the bundle carries NO signature, verification
//     fails closed: a pinned operator demands authenticity.
//   - When no key is pinned and no signature is present, the bundle is treated as
//     unsigned and only per-file hashes are checked (back-compatible).
//   - Regardless, EVERY file listed in checksums.sha256 must be present in files
//     and match its recorded SHA-256, and the signature (when checked) must be
//     valid over the EXACT bytes of checksums.sha256.
//
// Any mismatch returns an error; the caller must refuse to apply.
func VerifyBundle(files map[string][]byte, pinnedPubPEM []byte) (*VerifyResult, error) {
	checksums, ok := files["checksums.sha256"]
	if !ok {
		return nil, fmt.Errorf("agent: bundle has no checksums.sha256")
	}
	listed, err := parseChecksums(checksums)
	if err != nil {
		return nil, err
	}

	sigB64, hasSig := files["bundle.sig"]
	bundlePubPEM, hasPub := files["signing-pubkey.pem"]
	signaturePresent := hasSig && hasPub
	pinned := len(pinnedPubPEM) > 0

	res := &VerifyResult{}

	switch {
	case signaturePresent:
		// Decode the detached signature: base64 of the raw 64-byte Ed25519 sig.
		sigRaw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB64)))
		if err != nil {
			return nil, fmt.Errorf("agent: decode bundle.sig: %w", err)
		}
		// Choose the verification key: pinned key wins; otherwise the bundle's own.
		var pubPEM []byte
		if pinned {
			pubPEM = pinnedPubPEM
		} else {
			pubPEM = bundlePubPEM
		}
		pub, err := parsePinnedPublicKey(pubPEM)
		if err != nil {
			return nil, err
		}
		// Verify over the EXACT bytes of checksums.sha256 (the canonical content).
		if !bundlesig.Verify(checksums, sigRaw, pub) {
			return nil, fmt.Errorf("agent: bundle signature verification failed")
		}
		res.Signed = true
	case pinned:
		// A key is pinned but the bundle is unsigned: fail closed.
		return nil, fmt.Errorf("agent: signing key pinned but bundle has no signature (bundle.sig/signing-pubkey.pem missing); refusing")
	default:
		// No pin, no signature: unsigned bundle, hash-only verification.
	}

	// Per-file SHA-256: every listed file must be present and match.
	for path, wantHex := range listed {
		content, ok := files[path]
		if !ok {
			return nil, fmt.Errorf("agent: checksums lists %q but it is missing from the bundle", path)
		}
		gotSum := sha256.Sum256(content)
		gotHex := hex.EncodeToString(gotSum[:])
		if gotHex != wantHex {
			return nil, fmt.Errorf("agent: checksum mismatch for %q: got %s, want %s", path, gotHex, wantHex)
		}
		res.FileCount++
	}

	// install.sh is the root-executed trust anchor and must always be present and
	// covered by checksums (the export path lists it). Guard explicitly so a
	// checksums file that somehow omits it cannot let an unverified script run.
	if _, ok := files["install.sh"]; !ok {
		return nil, fmt.Errorf("agent: bundle has no install.sh")
	}
	if _, ok := listed["install.sh"]; !ok {
		return nil, fmt.Errorf("agent: checksums.sha256 does not cover install.sh")
	}

	// artifacts.json (plan-9): OPTIONAL (omitted in air-gap / no-catalog deploys), but when
	// PRESENT it carries the self-update + mimic pins the root install.sh and the agent
	// self-update read, so it MUST be covered by checksums.sha256 — i.e. part of the signed
	// set. The per-file loop above only verifies LISTED files, so without this guard an
	// attacker who can ADD a file to the bundle (but not re-sign checksums) could smuggle in
	// an UNLISTED artifacts.json carrying a malicious agent-binary/.deb pin that passes
	// VerifyBundle and is then trusted. Mirror the install.sh coverage guard, conditional on
	// presence. (A LISTED-but-absent artifacts.json is already caught by the per-file loop.)
	if _, ok := files["artifacts.json"]; ok {
		if _, covered := listed["artifacts.json"]; !covered {
			return nil, fmt.Errorf("agent: artifacts.json present but not covered by checksums.sha256; refusing")
		}
	}

	return res, nil
}

// MembershipConfig is the keystone (off-host trust-list) configuration
// VerifyMembership needs: the pinned operator credential the node was provisioned
// with out of band, and the node's own identity (to assert it is a signed member).
//
// When OperatorCredPEM is empty, keystone is OFF and VerifyMembership is a no-op:
// the agent behaves exactly as before (no trust-list) — the opt-in contract.
type MembershipConfig struct {
	// NodeID is this agent's configured identity; it MUST appear in the signed
	// trust-list's members or VerifyMembership fails closed.
	NodeID string
	// OperatorCredPEM is the pinned operator credential's public key (PKIX PEM), or
	// empty when keystone is OFF. It is the ONLY trust anchor VerifyMembership uses —
	// never any key shipped inside the (controller-produced) bundle.
	OperatorCredPEM []byte
	// OperatorCredAlg names the pinned credential's algorithm ("ed25519",
	// "webauthn-es256", or "webauthn-eddsa"). Dispatch is on THIS value, not the
	// attacker-influenced artifact's, closing the algorithm-confusion door.
	OperatorCredAlg string
	// OperatorRPID / OperatorOrigin are the WebAuthn relying-party binding values for
	// the WebAuthn algorithms (ignored for raw ed25519). Origin is advisory on a node.
	OperatorRPID   string
	OperatorOrigin string
}

// VerifyMembership is the keystone gate (plan-5.1c, install.sh-coverage CORRECTION
// 2026-06-08): it proves the bundle's membership AND the bundle's exact contents were
// authorized by the OFF-HOST operator credential, so a breached controller can forge
// neither membership nor what RUNS on the node. It runs AFTER VerifyBundle (tier-1
// integrity) and BEFORE apply. It is fail-closed: any missing field, parse error,
// signature failure, bundle-digest mismatch, non-member node, or epoch rollback returns
// an error and the caller must refuse to apply (keep-last-good).
//
// When cfg.OperatorCredPEM is empty, keystone is OFF and this is a no-op (opt-in):
// the agent applies exactly as it did before the trust-list existed.
//
// When keystone is ON it enforces, in order:
//
//   - REQUIRE files["trustlist.json"], files["trustlist.sig"], and
//     files["checksums.sha256"] (fail-closed if any is absent — a pinned operator
//     demands a signed membership AND the bundle's integrity manifest to bind it to).
//   - Parse the TrustList (from trustlist.json) and the SignedTrustList (from
//     trustlist.sig).
//   - Assert trustlist.Canonical(manifest) byte-equals files["trustlist.json"] (the
//     Verify CALLER CONTRACT): the agent acts on EXACTLY the bytes the operator
//     signed, never an attacker's re-encoding carrying extra/duplicate fields.
//   - trustlist.Verify(manifest, signed, pin) against the PINNED credential.
//   - BUNDLE-DIGEST BINDING (the install.sh-coverage fix): compute
//     gotDigest = hex(sha256(files["checksums.sha256"])) and assert it equals THIS
//     node's (cfg.NodeID) member.BundleSHA256 in the signed manifest. checksums.sha256
//     covers install.sh + every config, so a controller that tampers with install.sh
//     changes this digest — which it cannot re-sign without the off-host key — and the
//     node rejects the bundle. This is what binds the OFF-HOST signature to what RUNS,
//     closing the prior bypass where install.sh was controller-controlled and unsigned.
//   - Assert cfg.NodeID is a member (this node is in the signed fleet).
//   - Assert every WG public key in the bundle's wireguard/*.conf [Peer] blocks is
//     some signed member's WGPublicKey (cheap defense in depth — the bundle-digest
//     binding above already covers the configs, but this keeps a clear, early signal).
//   - Assert manifest.Epoch >= prevEpoch (anti-rollback against State.MembershipEpoch).
//
// On success it returns the verified trust-list's Epoch so the caller can persist it
// (advancing the anti-rollback floor) after a successful apply.
func VerifyMembership(files map[string][]byte, cfg MembershipConfig, prevEpoch int64) (epoch int64, err error) {
	// Keystone OFF (opt-in): no operator credential pinned -> behave as today.
	if len(cfg.OperatorCredPEM) == 0 {
		return 0, nil
	}

	// Fail closed when a pinned operator's bundle lacks the signed membership or the
	// integrity manifest the off-host signature binds to: the keystone is mandatory
	// once an operator credential is configured.
	tlJSON, ok := files["trustlist.json"]
	if !ok {
		return 0, fmt.Errorf("agent: operator credential pinned but bundle has no trustlist.json; refusing")
	}
	sigJSON, ok := files["trustlist.sig"]
	if !ok {
		return 0, fmt.Errorf("agent: operator credential pinned but bundle has no trustlist.sig; refusing")
	}
	// checksums.sha256 is the bundle's integrity manifest (it covers install.sh + every
	// config). Its digest is what the off-host manifest binds per member, so it MUST be
	// present — an absent one would otherwise let the bundle-digest check be skipped.
	checksums, ok := files["checksums.sha256"]
	if !ok {
		return 0, fmt.Errorf("agent: operator credential pinned but bundle has no checksums.sha256; refusing")
	}

	// Parse the distributed trust-list manifest and its detached signature artifact.
	var tl trustlist.TrustList
	if err := json.Unmarshal(tlJSON, &tl); err != nil {
		return 0, fmt.Errorf("agent: parse trustlist.json: %w", err)
	}
	var signed trustlist.SignedTrustList
	if err := json.Unmarshal(sigJSON, &signed); err != nil {
		return 0, fmt.Errorf("agent: parse trustlist.sig: %w", err)
	}

	// CALLER CONTRACT (verify.go): the signed payload is Canonical(tl), so a node that
	// ACTS on the membership must assert Canonical(parsed) byte-equals the received
	// file (then it never trusts bytes the user did not sign). The controller ships
	// trustlist.json AS the canonical bytes, so this must hold exactly.
	canonical, err := trustlist.Canonical(tl)
	if err != nil {
		return 0, fmt.Errorf("agent: canonicalize trustlist: %w", err)
	}
	if !bytes.Equal(canonical, tlJSON) {
		return 0, fmt.Errorf("agent: trustlist.json is not its own canonical form; refusing (the signed bytes must be the distributed bytes)")
	}

	// Build the pinned credential from the OUT-OF-BAND material only. Dispatch is on
	// the pinned Alg (never the artifact's) — the algorithm-confusion guard.
	pin, err := pinnedCredential(cfg)
	if err != nil {
		return 0, err
	}

	// Offline signature check against the pinned anchor. Fail-closed on any error.
	if err := trustlist.Verify(tl, signed, pin); err != nil {
		return 0, fmt.Errorf("agent: trust-list signature verification failed: %w", err)
	}

	// This node must itself be a signed member, AND its signed member entry carries the
	// digest that binds THIS bundle. Index by node id so we can read this node's
	// BundleSHA256; index pubkeys for the cheap peer defense below.
	members := make(map[string]string, len(tl.Members)) // wg pubkey -> node id (for diagnostics)
	var self *trustlist.Member
	for i := range tl.Members {
		m := &tl.Members[i]
		if m.NodeID == cfg.NodeID {
			self = m
		}
		if m.WGPublicKey != "" {
			members[m.WGPublicKey] = m.NodeID
		}
	}
	if self == nil {
		return 0, fmt.Errorf("agent: node %q is not a member of the signed trust-list; refusing", cfg.NodeID)
	}

	// BUNDLE-DIGEST BINDING (install.sh-coverage, plan-5.1 CORRECTION): the off-host
	// signature binds this node's bundle digest = hex(sha256(checksums.sha256)), and
	// checksums.sha256 covers install.sh + every config. So a breached controller that
	// rewrites install.sh (e.g. to splice a rogue `wg set ... peer ...`) must rebuild
	// checksums.sha256 to keep tier-1 VerifyBundle passing — which changes this digest,
	// which it cannot re-sign without the off-host key. The signed member's digest then
	// no longer matches and we refuse. This is THE check that makes the off-host
	// signature cover what RUNS, not merely the membership list.
	sum := sha256.Sum256(checksums)
	gotDigest := hex.EncodeToString(sum[:])
	if self.BundleSHA256 == "" {
		return 0, fmt.Errorf("agent: signed trust-list member %q carries no bundle_sha256; refusing (the off-host signature must bind the bundle)", cfg.NodeID)
	}
	if !strings.EqualFold(gotDigest, self.BundleSHA256) {
		return 0, fmt.Errorf("agent: bundle digest %s does not match the off-host-signed digest %s for node %q; refusing (install.sh or a config was tampered)", gotDigest, strings.ToLower(self.BundleSHA256), cfg.NodeID)
	}

	// Every WireGuard peer the bundle would configure should be a signed member. The
	// bundle-digest binding above already covers the conf bytes cryptographically; this
	// is a cheap, early, human-legible signal kept as defense in depth.
	peerKeys, err := collectBundlePeerKeys(files)
	if err != nil {
		return 0, err
	}
	for _, pk := range peerKeys {
		if _, ok := members[pk]; !ok {
			return 0, fmt.Errorf("agent: bundle peer public key %q is not a signed trust-list member; refusing", pk)
		}
	}

	// Anti-rollback: refuse a trust-list strictly older than the last applied epoch. An
	// equal epoch is allowed (idempotent re-apply of the same membership generation).
	if tl.Epoch < prevEpoch {
		return 0, fmt.Errorf("agent: trust-list epoch %d is older than last applied %d; refusing (membership rollback)", tl.Epoch, prevEpoch)
	}

	return tl.Epoch, nil
}

// pinnedCredential builds a trustlist.PinnedCredential from the out-of-band operator
// material in cfg, parsing the public key by the PINNED algorithm. It is the single
// place the node turns its provisioned anchor into the verifier's trust input; an
// unknown/unsupported algorithm is rejected here so dispatch can never fall through.
func pinnedCredential(cfg MembershipConfig) (trustlist.PinnedCredential, error) {
	pin := trustlist.PinnedCredential{
		Alg:    trustlist.Alg(cfg.OperatorCredAlg),
		RPID:   cfg.OperatorRPID,
		Origin: cfg.OperatorOrigin,
	}
	switch pin.Alg {
	case trustlist.AlgEd25519, trustlist.AlgWebAuthnEdDSA:
		pub, err := trustlist.ParseEd25519PinPEM(cfg.OperatorCredPEM)
		if err != nil {
			return trustlist.PinnedCredential{}, fmt.Errorf("agent: parse pinned operator credential: %w", err)
		}
		pin.Ed25519Pub = pub
	case trustlist.AlgWebAuthnES256:
		pub, err := trustlist.ParseES256Pin(cfg.OperatorCredPEM)
		if err != nil {
			return trustlist.PinnedCredential{}, fmt.Errorf("agent: parse pinned operator credential: %w", err)
		}
		pin.ES256Pub = pub
	default:
		return trustlist.PinnedCredential{}, fmt.Errorf("agent: unsupported operator credential alg %q", cfg.OperatorCredAlg)
	}
	return pin, nil
}

// collectBundlePeerKeys extracts every [Peer] PublicKey value from the bundle's
// wireguard/*.conf files. It mirrors the renderer's exact line shape
// ("PublicKey = <base64>", see internal/renderer/wireguard.go) but parses
// defensively: it walks [Section] headers, only reads a "PublicKey" line while inside
// a [Peer] block (so an [Interface] PrivateKey/PublicKey can never be mistaken for a
// peer), and tolerates arbitrary surrounding whitespace. The result is sorted +
// de-duplicated for a deterministic membership check.
func collectBundlePeerKeys(files map[string][]byte) ([]string, error) {
	seen := make(map[string]struct{})
	for rel, content := range files {
		if !strings.HasPrefix(rel, "wireguard/") || !strings.HasSuffix(rel, ".conf") {
			continue
		}
		inPeer := false
		for _, raw := range strings.Split(string(content), "\n") {
			line := strings.TrimSpace(raw)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			// Section headers ([Interface] / [Peer]) switch the parse context. A WG
			// conf may carry multiple [Peer] blocks (one per peer); each is scanned.
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				inPeer = strings.EqualFold(line, "[Peer]")
				continue
			}
			if !inPeer {
				continue
			}
			key, val, ok := splitConfKV(line)
			if !ok || !strings.EqualFold(key, "PublicKey") {
				continue
			}
			if val == "" {
				return nil, fmt.Errorf("agent: %s: empty [Peer] PublicKey", rel)
			}
			seen[val] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// splitConfKV splits a "Key = Value" WireGuard config line on the first '=',
// trimming whitespace around both sides. It returns ok=false when there is no '='.
func splitConfKV(line string) (key, val string, ok bool) {
	idx := strings.Index(line, "=")
	if idx < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+1:]), true
}
