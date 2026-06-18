// Package conformance is the load-bearing Go↔TS drift-control apparatus for the
// local-compile pipeline (program plan-5 / milestone 1.5). It is test-only support
// code in the spirit of internal/regression: it changes ZERO production pipeline
// behavior and exists solely to freeze the compiler's byte output as the
// authoritative oracle the TypeScript reimplementation (plan-4) must match.
//
// The unit of the harness is the canonical Manifest: one deterministic document per
// fixture, serialized identically across runs, machines, and languages, so the Go
// oracle and the future TS producer can be byte-compared with a first-divergence
// report. The manifest is the contract — see docs/spec/compiler/conformance-manifest-schema.md
// (this plan) and docs/spec/compiler/io-contract.md (plan-3, the upstream IN/OUT
// byte-exclusion set this package MIRRORS, it does not re-author).
//
// Design invariants the serializer enforces (so neither side is free to drift):
//   - The verdict has TWO independent channels (validator + apierr); each is a SORTED
//     code SET, never an ordered list, so reordered findings never red the harness and
//     a dropped/added code always does.
//   - Peers are a SET keyed by linkid.LinkKey + "|" + direction, never a slice index:
//     the compiler's PeerMap is appended in edge-array order (peers.go), which is not a
//     contract surface, so indexing it would pin an accident.
//   - Files are per-file by their relpath key (e.g. wireguard/<iface>.conf), never a
//     concatenated blob: Go map iteration is non-deterministic, so only a keyed,
//     sorted projection is comparable.
//
// Excluded from the byte set (mirroring plan-3's io-contract.md, NOT redefined here):
// manifest.json's compiled_at (a timestamp), compiler.computeChecksum (the display-only
// sha256(fmt.Sprintf("%v", topo)) with no TS counterpart), and the self-extracting
// tar.gz wrapper bytes (the harness compares bundle CONTENTS + the per-node checksums
// only). The signing private key NEVER appears — zero-knowledge custody (principle P2):
// fixtures pin fixed per-node PRIVATE keys as INPUT and the harness asserts only the
// derived PUBLIC material that surfaces inside the rendered files.
package conformance

import (
	"bytes"
	"encoding/json"
	"sort"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// Verdict is the two-channel compile/validate outcome. The two channels are
// COMPILE-TIME distinct in the product (a validator finding rides a 200
// ValidateResponse and is a different Go type from the apierr HTTP envelope —
// validator/code.go's design lock), and the manifest keeps them distinct so the
// harness can tell a validation failure from a transport/compile-resource failure.
//
//   - Validator carries the sorted set of validator.Code strings collected by running
//     validator.ValidateSchema + ValidateSemantic DIRECTLY (exactly as /api/validate
//     does), over BOTH errors[] and warnings[]. It is populated for every fixture —
//     a green fixture has whatever warnings the validator emits; a validator-FAIL
//     fixture additionally has its error code(s) here.
//   - Apierr carries the sorted set of apierr.Code strings from the compile error
//     envelope. It is EMPTY on a successful compile (Compile returned nil). On a
//     compile failure it holds the single coded error (e.g. compile_transit_pool_exhausted).
//
// Both fields are always non-nil slices (emitted as [] rather than null) so the JSON
// shape is stable whether or not codes are present.
type Verdict struct {
	Validator []string `json:"validator"`
	Apierr    []string `json:"apierr"`
}

// Allocations is the load-bearing allocator write-back projection: the per-node overlay
// IP map and the per-peer derived resources, keyed so the projection is order-free.
type Allocations struct {
	// NodeOverlayIPs maps nodeID -> the allocated overlay IP (model.Node.OverlayIP).
	NodeOverlayIPs map[string]string `json:"node_overlay_ips"`

	// Peers maps "<linkid.LinkKey>|<dir>" -> the peer's derived allocation, where dir is
	// the local node ID of the PeerInfo's owner. This makes the peer set keyed by stable
	// link identity + direction, never by the edge-array append position in PeerMap.
	Peers map[string]PeerAllocation `json:"peers"`
}

// PeerAllocation is the conformance projection of one compiler.PeerInfo: the allocated,
// byte-stable resources for one directed link end. It DROPS the echoed-input fields
// (NodeName, AllowedIPs, Endpoint, keepalive, the cosmetic role-derived flags) and keeps
// the values the allocator assigns — the surface incremental-stability (P1) protects.
type PeerAllocation struct {
	RemoteNodeID    string `json:"remote_node_id"`
	PublicKey       string `json:"public_key"`
	OverlayIP       string `json:"overlay_ip"`
	InterfaceName   string `json:"interface_name"`
	ListenPort      int    `json:"listen_port"`
	LocalTransitIP  string `json:"local_transit_ip"`
	RemoteTransitIP string `json:"remote_transit_ip"`
	LocalLinkLocal  string `json:"local_link_local"`
	RemoteLinkLocal string `json:"remote_link_local"`
}

// HealedEdge is one edge of the topology AFTER normalize.HealCollidingPins has run over a
// copy of the fixture's input topology. It carries the edge identity plus the seven pin
// fields the heal strips/keeps, so step D's TS heal canary can byte-compare the FE
// healCollidingPins against the Go heal over the shared corpus. Edges are emitted SORTED
// by ID so map/slice iteration order never reds the comparison.
type HealedEdge struct {
	ID                  string `json:"id"`
	CompiledPort        int    `json:"compiled_port"`
	PinnedFromPort      int    `json:"pinned_from_port"`
	PinnedToPort        int    `json:"pinned_to_port"`
	PinnedFromTransitIP string `json:"pinned_from_transit_ip"`
	PinnedToTransitIP   string `json:"pinned_to_transit_ip"`
	PinnedFromLinkLocal string `json:"pinned_from_link_local"`
	PinnedToLinkLocal   string `json:"pinned_to_link_local"`
}

// Manifest is the one canonical document per fixture. Every field is a deterministic
// projection of the compile result; the whole struct round-trips through Marshal (below)
// to canonical JSON (sorted keys, LF, no trailing whitespace) so the bytes are identical
// across runs, machines, and the Go/TS language boundary.
type Manifest struct {
	// Fixture is the corpus name (loadFixtures' name), the document's primary key.
	Fixture string `json:"fixture"`

	// Verdict is the two-channel validate/compile outcome (see Verdict).
	Verdict Verdict `json:"verdict"`

	// Topology is the post-write-back compiled topology (allocated overlay IPs, the six
	// pinned_* edge fields + CompiledPort, derived router-ids/keys) on a SUCCESSFUL
	// compile; nil on a fail fixture (no compiled topology exists). It is the model the TS
	// port must reproduce field-for-field — a TS Node that drops router_id reds here.
	Topology *model.Topology `json:"topology"`

	// Allocations is the keyed allocator write-back projection; nil on a fail fixture.
	Allocations *Allocations `json:"allocations"`

	// Files is the per-node bundle byte set: nodeID -> relpath -> verbatim content; nil on
	// a fail fixture. These are exactly the checksummed bytes (Checksums covers them).
	Files map[string]map[string]string `json:"files"`

	// Checksums maps nodeID -> bundlesig.Canonicalize output verbatim (the canonical
	// checksums.sha256 content); nil on a fail fixture.
	Checksums map[string]string `json:"checksums"`

	// HealedEdges is the corpus-input topology after normalize.HealCollidingPins, sorted
	// by edge ID. It is computed for EVERY fixture (independent of the compile verdict)
	// because the TS heal canary (step D) pins this layer regardless of whether the full
	// TS pipeline can compile the fixture yet.
	HealedEdges []HealedEdge `json:"healed_edges"`
}

// sortedSet returns the unique, lexicographically-sorted set of in. A nil/empty input
// yields a non-nil empty slice so the JSON renders as [] (a stable shape), never null.
func sortedSet(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Marshal renders the manifest as canonical JSON: keys sorted (encoding/json sorts map
// keys, and the struct field order is fixed and intentionally alphabetical-by-meaning),
// two-space indentation for human-diffable goldens, LF newlines, and NO trailing
// whitespace. encoding/json's deterministic map-key sort is what makes the per-node Files
// map, the keyed Peers set, and the Checksums map byte-stable regardless of Go map
// iteration order. The output ends in exactly one trailing LF so the committed goldens are
// git-clean and editor-stable.
//
// The serializer asserts no trailing whitespace on any line (json.MarshalIndent already
// emits none, but the harness's "no trailing whitespace" invariant is load-bearing, so it
// is verified rather than assumed — a stray space would otherwise drift silently between
// languages).
func Marshal(m Manifest) ([]byte, error) {
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	// json.MarshalIndent never produces trailing whitespace; the verification below makes
	// that property part of the contract so a future change can't regress it unnoticed.
	for _, line := range bytes.Split(out, []byte("\n")) {
		if n := len(line); n > 0 && (line[n-1] == ' ' || line[n-1] == '\t') {
			return nil, errTrailingWhitespace
		}
	}
	return append(out, '\n'), nil
}

// errTrailingWhitespace is returned by Marshal if the encoder ever emits a line with
// trailing whitespace (it does not today; this is the load-bearing backstop).
var errTrailingWhitespace = &marshalError{"conformance: canonical manifest line has trailing whitespace"}

type marshalError struct{ msg string }

func (e *marshalError) Error() string { return e.msg }
