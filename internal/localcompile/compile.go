package localcompile

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/artifacts"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
)

// Compile runs the local compile path behind a single façade and returns the canonical
// artifacts-out result. It is the seam the TypeScript reimplementation (plan-4) and the
// conformance harness (plan-5) target.
//
// As of plan-3 Phase 4 the façade is a PURE function: every non-deterministic and
// environment-coupled input is taken from the request, never read from the process —
// the keygen seam (req.Keygen, nil ⇒ the default wgtypesKeygen), the compile clock
// (req.CompiledAt, injected via compiler.CompileAt), the bundle signer (req.SigningKey,
// injected via render.AllWith; nil ⇒ unsigned), the install-time fetch settings
// (req.Fetch), and the controller subgraph's reserved allocations (req.Reserved). There
// is no env read, no time.Now, no filesystem access, and no global state, so an identical
// request yields a byte-identical result (proven by the run-twice-assert-equal golden
// sub-test in plan-3 Phase 5).
func Compile(req CompileRequest) (CompileArtifacts, error) {
	result, err := CompileResult(req)
	if err != nil {
		return CompileArtifacts{}, err
	}
	return ArtifactsFromResult(result, req.SigningKey)
}

// CompileResult runs the local compile pipeline and returns the raw, fully-rendered
// *compiler.CompileResult — the SINGLE place the GenerateKeys → CompileAt → AllWith
// sequence lives (plan-3 Phase 6). Compile wraps it and re-shapes the result into the
// canonical CompileArtifacts; the live callers that still consume the
// *compiler.CompileResult shape directly — the air-gap HTTP handlers (CompileResponse /
// the deploy-script teardown that needs result.PeerMap), artifacts.Export's disk write,
// and the controller subgraph compile (preview + stage) — call this entry point instead
// of re-running the sequence themselves, so the façade is the single compile authority
// without forcing those callers to re-shape into CompileArtifacts.
//
// It is pure for the same reasons Compile is: every impurity (keygen, clock, signer,
// fetch, reserved) comes from req, never from the process, and it compiles under
// context.Background() (no caller deadline). The request-bearing live callers use
// CompileResultCtx instead so a client disconnect cancels the allocator scan.
func CompileResult(req CompileRequest) (*compiler.CompileResult, error) {
	return CompileResultCtx(context.Background(), req)
}

// CompileResultCtx is CompileResult with an explicit context for the live Go callers. ctx
// bounds the IP-allocation pass: the allocator polls ctx.Err() per candidate and aborts a
// long scan on cancellation (plan-8 S1), so the air-gap HTTP handlers pass r.Context() and
// the controller subgraph compile passes its request ctx — a client disconnect stops the
// scan rather than running it to the budget cap. ctx affects NEITHER the allocated values
// NOR the rendered bytes, so it is deliberately not a member of the frozen CompileRequest:
// the TS-mirrored contract stays context-free, and the pure CompileResult(req)/Compile(req)
// entry points pass context.Background(). cmd/compiler likewise has no request ctx.
func CompileResultCtx(ctx context.Context, req CompileRequest) (*compiler.CompileResult, error) {
	// Compile on a copy of the topology's Node/Edge slices so the façade never mutates the
	// caller's input: render.GenerateKeysWith writes the derived WireGuard keys onto nodes in
	// place, and we keep that write-back confined to our copy. (The compiler already allocates
	// IPs and writes pins onto its own fresh copies — compiler.go CompileAt — so only the key
	// write-back would otherwise alias the caller.) The canonical written-back topology, with
	// keys + allocated pins/IPs, is the returned result.Topology.
	topo := req.Topology
	topo.Nodes = append([]model.Node(nil), req.Topology.Nodes...)
	topo.Edges = append([]model.Edge(nil), req.Topology.Edges...)

	// nil Keygen ⇒ the default wgtypesKeygen, keeping production byte-identical to the
	// pre-seam pipeline. render consumes its own (structurally-identical) Keygen
	// interface, which both localcompile keygens satisfy.
	kg := req.Keygen
	if kg == nil {
		kg = wgtypesKeygen{}
	}
	keys, err := render.GenerateKeysWith(&topo, req.Custody, kg)
	if err != nil {
		return nil, err
	}

	c := compiler.NewCompiler()
	if req.Reserved != nil {
		c = c.WithReserved(req.Reserved)
	}
	// Thread the fleet-wide mimic-fallback default (plan-4). Setting it unconditionally is safe: "" ⇒
	// resolveMimicFallback floors to "none" everywhere ⇒ byte-identical to the pre-change pipeline.
	c = c.WithMimicFallbackDefault(req.Fetch.MimicFallbackDefault)
	// CompileAt injects the explicit clock (req.CompiledAt) instead of the compiler's
	// internal time.Now(); ctx bounds the allocator scan (cancellable on the live paths,
	// context.Background() on the pure entry points + the CLI).
	result, err := c.CompileAt(ctx, &topo, keys, req.CompiledAt)
	if err != nil {
		return nil, err
	}

	// AllWith injects the bundle signer (req.SigningKey) instead of reading it from the
	// environment; a nil interface is the byte-identical no-signing path.
	if err := render.AllWith(result, keys, req.Fetch, req.SigningKey); err != nil {
		return nil, err
	}

	return result, nil
}

// ArtifactsFromResult turns a fully-rendered *compiler.CompileResult into the canonical
// CompileArtifacts: the per-node Files map (the checksummed bundle byte set), the per-node
// checksums, and the optional detached signatures. It is the contract-level reshape Compile
// applies to expose the frozen artifacts-out shape (and the surface the golden corpus pins).
//
// The per-node bundle file set is built by the SAME artifacts.BundleFiles helper the on-disk
// exporter uses, so the in-memory CompileArtifacts and the on-disk bundle are single-sourced
// by a shared call — not merely pinned byte-equal by the corpus. (localcompile importing
// artifacts is cycle-free: artifacts is a sink package — apierr/bundlesig/compiler only — so
// it imports neither render nor this package. The reverse, artifacts importing localcompile,
// is what would cycle, since render's tests depend on artifacts.) Over that set bundlesig.
// Canonicalize emits the checksums; the detached signatures cover the canonical bytes.
//
// signer is the bundlesig.ConfigSigner interface (Compile passes req.SigningKey): a nil
// interface means "unsigned" — Signatures stays empty and SigningPubPEM nil, the byte-
// identical no-signing path. This function reads no environment, clock, or filesystem, so it
// preserves Compile's purity.
func ArtifactsFromResult(result *compiler.CompileResult, signer bundlesig.ConfigSigner) (CompileArtifacts, error) {
	signEnabled := signer != nil

	out := CompileArtifacts{
		Topology:   result.Topology,
		Files:      make(map[string]map[string]string),
		Deploy:     make(map[string]string),
		Checksums:  make(map[string]string),
		Signatures: make(map[string]string),
		Warnings:   result.Warnings,
		Manifest:   result.Manifest,
	}

	for _, node := range result.Topology.Nodes {
		// The per-node checksummed bundle file set — one source of truth (artifacts.BundleFiles)
		// for the relpath keys and the set membership (incl. the artifacts.json D4 guard), shared
		// with the on-disk exporter so the two can never drift.
		bundleFiles := artifacts.BundleFiles(result, node.ID)
		out.Files[node.ID] = bundleFiles

		// The canonical checksums.sha256 content over this node's bundle (sorted by
		// path, "%x  %s\n" lines).
		canonical := bundlesig.Canonicalize(bundleFiles)
		out.Checksums[node.ID] = string(canonical)

		// When signing is on, the detached signature covers the exact canonical bytes. The
		// in-memory Signatures value is the BARE base64; the on-disk bundle.sig the exporter
		// writes is that same base64 plus a trailing newline (a file-representation detail — the
		// signed/digest-bound bytes are identical).
		if signEnabled {
			sig, err := signer.Sign(canonical)
			if err != nil {
				return CompileArtifacts{}, fmt.Errorf("sign bundle for node %s: %w", node.ID, err)
			}
			out.Signatures[node.ID] = base64.StdEncoding.EncodeToString(sig)
		}
	}

	if signEnabled {
		out.SigningPubPEM = signer.PublicKeyPEM()
	}

	// Project-level deploy scripts (deploy-all.sh / deploy-all.ps1).
	for name, script := range result.DeployScripts {
		out.Deploy[name] = script
	}

	return out, nil
}
