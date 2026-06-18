package conformance

import (
	"errors"
	"sort"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/linkid"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/localcompile"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/normalize"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/validator"
)

// FixedCompiledAt is the explicit compile clock the oracle injects into every fixture's
// CompileRequest. It feeds only manifest.json's compiled_at — which is OUT of the
// conformance byte set — but pinning it keeps the request fully deterministic so the
// golden + self-comparison tests compare like with like. It matches plan-3's
// contract-golden clock so the two harnesses agree on the same instant.
var FixedCompiledAt = time.Date(2026, time.June, 18, 0, 0, 0, 0, time.UTC)

// Fixture is the conformance view of an on-disk corpus fixture. It is built from the SAME
// shape plan-3's loader (internal/localcompile/contract_golden_test.go) reads, so the two
// harnesses consume ONE corpus directory (no duplication): a topology plus the request knobs
// that, with the default Keygen + a fixed clock, make Compile fully deterministic. Custody is
// resolved from the fixture's "airgap" | "agentheld" string; Signer is the throwaway test
// bundle signer when the fixture opts in (nil — the byte-identical no-signing path —
// otherwise). The harness loader (golden_test.go) populates this from disk.
type Fixture struct {
	Name     string
	Custody  render.KeyCustody
	Signer   bundlesig.ConfigSigner
	Topology model.Topology
}

// BuildManifest runs a fixture through BOTH verdict channels and projects the result into a
// canonical Manifest. It is the Go oracle: the authoritative bytes the TS port (plan-4)
// must match.
//
//   - The VALIDATOR channel runs validator.ValidateSchema + validator.ValidateSemantic
//     DIRECTLY on the fixture topology (exactly as /api/validate does) and collects the
//     sorted Code set from BOTH errors[] and warnings[] across both passes. This channel is
//     populated for every fixture independent of whether the compile succeeds.
//   - The rest runs the localcompile façade (CompileResult + ArtifactsFromResult — the same
//     bytes localcompile.Compile produces, but exposing the PeerMap) with a nil Keygen (the
//     default wgtypesKeygen, byte-identical to plan-3's ecdhKeygen golden by the proven X25519
//     equivalence), the fixed clock, and the fixture's custody/signer. On SUCCESS the apierr
//     channel is empty and the topology/allocations/files/checksums projections are populated.
//     On FAILURE the returned error is unwrapped via errors.As to *apierr.Error and its Code
//     becomes the sole apierr-channel entry; the success-only projections stay nil.
//   - healed_edges is computed for EVERY fixture from normalize.HealCollidingPins over a
//     COPY of the fixture's INPUT topology (the heal mutates in place; the copy keeps the
//     compile path's topology untouched), so the step-D TS heal canary has its pin whether
//     or not the fixture compiles.
func BuildManifest(fx Fixture) (Manifest, error) {
	m := Manifest{
		Fixture:     fx.Name,
		Verdict:     Verdict{Validator: validatorCodes(fx.Topology), Apierr: []string{}},
		HealedEdges: healedEdges(fx.Topology),
	}

	req := localcompile.CompileRequest{
		Topology:   fx.Topology,
		Custody:    fx.Custody,
		SigningKey: fx.Signer,
		Fetch:      render.FetchSettings{},
		CompiledAt: FixedCompiledAt,
	}

	// CompileResult drives the SAME façade path Compile does, but returns the raw
	// *compiler.CompileResult so the oracle can read the PeerMap (the derived per-peer
	// allocation the manifest keys by LinkKey|dir). ArtifactsFromResult then reshapes that
	// result into the canonical byte set EXACTLY as Compile would — single-sourcing the
	// files/checksums with the on-disk exporter — so calling the two steps explicitly is
	// byte-identical to Compile, it just also exposes the PeerMap. On a compile failure the
	// error is coded at the source; unwrap it onto the apierr channel.
	result, err := localcompile.CompileResult(req)
	if err != nil {
		// A compile failure rides the apierr channel: unwrap to the coded error and take its
		// Code. Every compile error in the pipeline is coded at the source (apierr.go), so a
		// bare/unwrappable error is a harness bug worth surfacing loudly rather than masking.
		var coded *apierr.Error
		if errors.As(err, &coded) {
			m.Verdict.Apierr = []string{string(coded.Code())}
			return m, nil
		}
		return Manifest{}, err
	}

	art, err := localcompile.ArtifactsFromResult(result, req.SigningKey)
	if err != nil {
		return Manifest{}, err
	}

	// Success: project the topology + allocations + files + checksums. The verdict's apierr
	// channel stays empty (compile succeeded); the validator channel keeps whatever warnings
	// the validator emitted for this (green) topology.
	m.Topology = art.Topology
	m.Allocations = allocationsFrom(result)
	m.Files = art.Files
	m.Checksums = art.Checksums
	return m, nil
}

// validatorCodes runs the schema + semantic passes directly (the /api/validate channel) and
// returns the sorted, deduplicated set of finding Codes across BOTH errors and warnings of
// BOTH passes. ValidateSchema/ValidateSemantic take a *model.Topology and never mutate it in
// a way the compile path depends on, so a fresh copy keeps the channels independent.
func validatorCodes(topo model.Topology) []string {
	t := copyTopology(topo)
	schema := validator.ValidateSchema(&t)
	semantic := validator.ValidateSemantic(&t)

	var codes []string
	collect := func(findings []validator.ValidationError) {
		for _, f := range findings {
			codes = append(codes, f.Code)
		}
	}
	collect(schema.Errors)
	collect(schema.Warnings)
	collect(semantic.Errors)
	collect(semantic.Warnings)
	return sortedSet(codes)
}

// allocationsFrom projects a successful compile's write-backs into the keyed Allocations
// set: the per-node overlay IPs from the compiled topology, and the per-peer derived
// resources from result.PeerMap. The peer set is keyed by a stable link identity + the
// owning node ID, NEVER by the PeerMap append position (which is edge-array order — not a
// contract surface).
//
// Link identity: a node pair carrying ONE link (the folded primary class) keys by the bare
// linkid.PinKey of the pair; a pair carrying parallel links (a primary plus one or more
// backups) keys by "<pinKey>#<interfaceName>" — the per-link interface name is distinct per
// primary/backup and is itself byte-stable, so this disambiguates parallel links without
// depending on slice order. Combined with the owning node ID ("|<owner>") this yields a
// stable, order-free key for each directed link end.
func allocationsFrom(result *compiler.CompileResult) *Allocations {
	out := &Allocations{
		NodeOverlayIPs: map[string]string{},
		Peers:          map[string]PeerAllocation{},
	}
	if result.Topology == nil {
		return out
	}
	for _, n := range result.Topology.Nodes {
		out.NodeOverlayIPs[n.ID] = n.OverlayIP
	}

	// Count enabled non-backup-vs-backup edges per pair so a parallel pair is recognized and
	// disambiguated by interface name; a single-link pair keys by the bare pinKey.
	pairLinkCount := map[string]int{}
	{
		seen := map[string]struct{}{}
		for i := range result.Topology.Edges {
			e := &result.Topology.Edges[i]
			if !e.IsEnabled {
				continue
			}
			lk := linkid.LinkKey(e)
			if _, ok := seen[lk]; ok {
				continue
			}
			seen[lk] = struct{}{}
			pairLinkCount[linkid.PinKey(e.FromNodeID, e.ToNodeID)]++
		}
	}

	for ownerID, peers := range result.PeerMap {
		for _, p := range peers {
			pair := linkid.PinKey(ownerID, p.NodeID)
			linkKey := pair
			if pairLinkCount[pair] > 1 {
				linkKey = pair + "#" + p.InterfaceName
			}
			out.Peers[linkKey+"|"+ownerID] = PeerAllocation{
				RemoteNodeID:    p.NodeID,
				PublicKey:       p.PublicKey,
				OverlayIP:       p.OverlayIP,
				InterfaceName:   p.InterfaceName,
				ListenPort:      p.ListenPort,
				LocalTransitIP:  p.LocalTransitIP,
				RemoteTransitIP: p.RemoteTransitIP,
				LocalLinkLocal:  p.LocalLinkLocal,
				RemoteLinkLocal: p.RemoteLinkLocal,
			}
		}
	}
	return out
}

// healedEdges runs normalize.HealCollidingPins over a COPY of the input topology and returns
// the resulting edges projected to {ID + the seven pin fields}, sorted by ID. The heal
// mutates in place, so the copy keeps the oracle's compile path untouched; the projection is
// the surface the TS heal canary (step D) byte-compares.
func healedEdges(topo model.Topology) []HealedEdge {
	t := copyTopology(topo)
	normalize.HealCollidingPins(&t)

	out := make([]HealedEdge, 0, len(t.Edges))
	for i := range t.Edges {
		e := &t.Edges[i]
		out = append(out, HealedEdge{
			ID:                  e.ID,
			CompiledPort:        e.CompiledPort,
			PinnedFromPort:      e.PinnedFromPort,
			PinnedToPort:        e.PinnedToPort,
			PinnedFromTransitIP: e.PinnedFromTransitIP,
			PinnedToTransitIP:   e.PinnedToTransitIP,
			PinnedFromLinkLocal: e.PinnedFromLinkLocal,
			PinnedToLinkLocal:   e.PinnedToLinkLocal,
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

// copyTopology returns a deep-enough copy for the harness's needs: the Node and Edge slices
// are duplicated so an in-place mutation (HealCollidingPins writing onto edges, the validator
// or compiler touching nodes) never aliases another channel's view. The element structs are
// value types with no shared pointer fields the harness mutates, so a shallow per-slice copy
// is sufficient.
func copyTopology(topo model.Topology) model.Topology {
	out := topo
	out.Nodes = append([]model.Node(nil), topo.Nodes...)
	out.Edges = append([]model.Edge(nil), topo.Edges...)
	out.Domains = append([]model.Domain(nil), topo.Domains...)
	return out
}
