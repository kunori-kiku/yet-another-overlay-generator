// Package localcompile is the single clean Go façade over the local compile path —
// schema/semantic validation, IP allocation, capability inference, peer derivation,
// the renderers, render.All, and the artifacts byte set that artifacts.Export writes
// to disk. It exposes a stable, documented, reproducible input→output contract
// (CompileRequest → CompileArtifacts) so that every non-deterministic and
// environment-coupled input is lifted into an explicit parameter: the keygen
// seam, the bundle-signing key, the install-time
// fetch settings, the compile clock, and the controller subgraph's reserved
// allocations.
//
// The contract is the substrate the in-browser Go/WASM engine and the WASM
// conformance gate consume; its canonical schema lives in
// docs/spec/compiler/io-contract.md. Both native Go and Go/WASM call this same
// implementation; intentional contract changes are reviewed by updating its
// conformance goldens.
package localcompile

import (
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/validator"
)

// Keygen is the WireGuard key-derivation seam — the one input lifted out of the
// pipeline that decouples key derivation from wgtypes/wgctrl (the browser/WASM blocker).
// It is part of the frozen contract: the WASM engine shares this seam, and the WASM
// conformance gate asserts public-key DERIVATION only (never newly generated
// private-key material). The default implementation uses wgtypes; the stdlib
// crypto/ecdh implementation is the cross-platform reference.
type Keygen interface {
	// DerivePublic returns the base64 public key for a base64 X25519 private key. It
	// covers the AgentHeld pub-from-private derivation and the air-gap case-a/case-c
	// public derivation.
	DerivePublic(privB64 string) (pubB64 string, err error)
	// Generate returns a fresh (privB64, pubB64) X25519 key pair (air-gap case-c).
	Generate() (privB64, pubB64 string, err error)
	// ParseAndNormalize round-trips a private key to its canonical base64 form. It MUST
	// reproduce wgtypes' privateKey.String() canonicalization byte-for-byte (the air-gap
	// case-a re-write persists this back onto the node), not merely validate.
	ParseAndNormalize(privB64 string) (canonicalPrivB64 string, err error)
}

// CompileRequest is the canonical topology-in side of the frozen contract: a topology
// plus every keygen, signer, fetch, clock, custody, and reserved-allocation input.
// Keeping those inputs explicit makes Compile pure and reproducible; the golden
// suite proves that running an identical request twice returns identical bytes.
type CompileRequest struct {
	// Topology is the only required input.
	Topology model.Topology

	// Custody selects how WireGuard key material is treated: AirGap (the local/CLI path —
	// private keys round-trip through the topology JSON) or AgentHeld (the controller
	// path — zero-knowledge custody, only public keys persist;
	// see docs/spec/controller/key-custody.md).
	Custody render.KeyCustody

	// Keygen is the WireGuard key-derivation seam; a nil value means the default
	// wgtypesKeygen. Compile passes it to render.GenerateKeysWith.
	Keygen Keygen

	// SigningKey is the optional tier-1 bundle signer. It is the bundlesig.ConfigSigner
	// INTERFACE (not a pointer): a nil interface means "unsigned" — the byte-identical
	// no-signing path. The interface (rather than a *bundlesig.Signing pointer) avoids
	// Go's typed-nil gotcha, so a plain `SigningKey == nil` test is safe. Compile passes
	// it through render.AllWith; the façade never reads a signing key from the environment.
	SigningKey bundlesig.ConfigSigner

	// Fetch is the typed channel of install-time fetch pins (mimic GitHub-.deb fallback,
	// agent self-update catalog). Its ZERO value means "no catalog configured", which
	// MUST leave install.sh and the signed bundle byte-identical (the air-gap
	// byte-identity HIGH principle). It replaces the in-pipeline FetchSettingsFromEnv read.
	Fetch render.FetchSettings

	// CompiledAt is the explicit compile clock, replacing the compiler's internal
	// time.Now(). It feeds only manifest.json's compiled_at, which is OUT of the
	// conformance byte set (display-only). Compile passes it to compiler.CompileAt.
	CompiledAt time.Time

	// Reserved carries the allocation resources (ports / transit IPs / link-locals)
	// occupied by edges outside a controller subgraph, so a subgraph compile lets
	// gap-fill allocate around them and avoids cross-subgraph pin collisions. It is set
	// only on the controller subgraph path; nil (the default) means a full compile,
	// behavior unchanged.
	Reserved *compiler.ReservedAllocations
}

// CompileArtifacts is the canonical artifacts-out side of the frozen contract: the
// rendered byte output for every node, the project-level deploy scripts, the per-node
// checksums and (when signing is on) detached signatures, plus the compile manifest.
//
// Its shape mirrors what artifacts.Export writes to disk (export.go), so the
// in-memory contract and the on-disk bundle are byte-consistent. The disk write is
// presentation; the canonical member set is shared through artifacts.BundleFiles.
type CompileArtifacts struct {
	// Topology is the compiled topology with the allocator's write-backs applied: the
	// six pinned_* edge fields + CompiledPort (model.Edge), and OverlayIP + RouterID
	// (model.Node).
	//
	// RouterID is observed and re-emitted by the contract; its derivation remains the
	// compiler's responsibility.
	Topology *model.Topology

	// Files is the per-node bundle file set: nodeID -> relpath -> content. The relpath
	// keys mirror the canonical bundleFiles map artifacts.BundleFiles builds (the single source both
	// artifacts.Export's on-disk writes and its checksums.sha256 derive from):
	//
	//	wireguard/<iface>.conf   (one per per-peer interface; the client role's single wg0
	//	                          is wireguard/wg0.conf)
	//	babel/babeld.conf        (non-client nodes only)
	//	sysctl/99-overlay.conf
	//	install.sh
	//	README.txt
	//	telemetry.json          (AgentHeld ICMP/TCP-only policy, mutually exclusive with successor)
	//	telemetry-policy.json   (AgentHeld URL/device successor policy, mutually exclusive with v1)
	//	artifacts.json           (only when a mimic/agent catalog is configured; omitted
	//	                          otherwise so a non-catalog bundle stays byte-identical, D4)
	//
	// This is exactly the checksummed set — the bytes Checksums and Signatures cover.
	Files map[string]map[string]string

	// Deploy holds the project-level deploy-all.sh / deploy-all.ps1 files written to
	// the export root. They are operational complete-bundle SSH helpers for AirGap
	// custody and fail-closed enrolled-agent / kit-apply guidance for AgentHeld custody.
	Deploy map[string]string

	// Checksums maps nodeID -> the canonical checksums.sha256 content, produced by
	// bundlesig.Canonicalize over that node's Files (sorted by path, "%x  %s\n" lines).
	Checksums map[string]string

	// Signatures maps nodeID -> the detached bundle.sig content (base64 of the Ed25519
	// signature over the node's canonical checksums). It is present only when signing is
	// on (SigningKey != nil); empty otherwise.
	Signatures map[string]string

	// SigningPubPEM is the PKIX ("PUBLIC KEY") PEM of the verifying key, identical for
	// every node bundle. It is present iff signing is on; nil otherwise.
	SigningPubPEM []byte

	// Warnings carries the non-fatal schema/semantic findings so callers can surface
	// "dumb link" issues after a green compile.
	Warnings []validator.ValidationError

	// Manifest carries the compile summary, including CompiledAt and Checksum — both OUT
	// of the conformance byte set (compiled_at is a timestamp; checksum is a
	// display-only sha256(fmt.Sprintf("%v", topo)) with no cross-language counterpart).
	// The manifest checksum is metadata, not the hash verified by checksums.sha256.
	// Both fields are masked in the golden corpus and excluded from byte assertions.
	Manifest compiler.CompileManifest
}
