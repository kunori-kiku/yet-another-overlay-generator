package localcompile

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
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
// fetch, reserved) comes from req, never from the process.
func CompileResult(req CompileRequest) (*compiler.CompileResult, error) {
	// Work on a local copy of the topology so the caller's value is not mutated by the
	// pipeline's write-backs (GenerateKeys, IP allocation, and the pin write-back all
	// mutate the topology in place). The returned result.Topology is the compiled
	// (written-back) topology.
	topo := req.Topology

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
	// CompileAt injects the explicit clock (req.CompiledAt) instead of the compiler's
	// internal time.Now(); context.Background() because the façade does not carry a
	// request context — the local compile path is not bounded by an HTTP deadline.
	result, err := c.CompileAt(context.Background(), &topo, keys, req.CompiledAt)
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
// It builds exactly the same bundle file set + bundlesig.Canonicalize checksums + detached
// signatures that artifacts.Export lays out on disk. The two are SINGLE-SOURCED by the frozen
// contract corpus (the localcompile golden test + the lossless-wrapper test pin them byte-
// equal), not by a shared call: artifacts.Export builds the set straight from the result
// because importing this package there would form a test-only import cycle (artifacts is a
// transitive test dependency of render, which localcompile imports). The corpus is what keeps
// the in-memory CompileArtifacts and the on-disk bundle from ever drifting.
//
// signer is the bundlesig.ConfigSigner interface (Compile passes req.SigningKey): a nil
// interface means "unsigned" — Signatures stays empty and SigningPubPEM nil, the byte-
// identical no-signing path. This function reads no environment, clock, or filesystem, so it
// preserves Compile's purity.
func ArtifactsFromResult(result *compiler.CompileResult, signer bundlesig.ConfigSigner) (CompileArtifacts, error) {
	signEnabled := signer != nil

	artifacts := CompileArtifacts{
		Topology:   result.Topology,
		Files:      make(map[string]map[string]string),
		Deploy:     make(map[string]string),
		Checksums:  make(map[string]string),
		Signatures: make(map[string]string),
		Warnings:   result.Warnings,
		Manifest:   result.Manifest,
	}

	for _, node := range result.Topology.Nodes {
		// The per-node bundle file set: this is the exact checksummed set
		// artifacts.Export builds (export.go:155-175). The relpath keys are the
		// contract's per-node shape.
		bundleFiles := make(map[string]string)

		// Per-peer WireGuard configs. WireGuardConfigs keys have the format
		// "nodeID:interfaceName"; the client role's single wg0 is keyed "nodeID:wg0".
		for configKey, wgConf := range result.WireGuardConfigs {
			parts := strings.SplitN(configKey, ":", 2)
			if len(parts) != 2 || parts[0] != node.ID {
				continue
			}
			bundleFiles["wireguard/"+parts[1]+".conf"] = wgConf
		}
		if babelConf, ok := result.BabelConfigs[node.ID]; ok {
			bundleFiles["babel/babeld.conf"] = babelConf
		}
		if sysctlConf, ok := result.SysctlConfigs[node.ID]; ok {
			bundleFiles["sysctl/99-overlay.conf"] = sysctlConf
		}
		if script, ok := result.InstallScripts[node.ID]; ok {
			bundleFiles["install.sh"] = script
		}
		// artifacts.json joins the set only when a catalog produced non-empty content,
		// so a non-catalog bundle stays byte-identical (D4) — mirroring export's
		// hasArtifacts guard.
		if artifactsJSON, ok := result.ArtifactsJSON[node.ID]; ok && artifactsJSON != "" {
			bundleFiles["artifacts.json"] = artifactsJSON
		}

		artifacts.Files[node.ID] = bundleFiles

		// The canonical checksums.sha256 content over this node's bundle (sorted by
		// path, "%x  %s\n" lines).
		canonical := bundlesig.Canonicalize(bundleFiles)
		artifacts.Checksums[node.ID] = string(canonical)

		// When signing is on, the detached signature covers the exact canonical bytes.
		if signEnabled {
			sig, err := signer.Sign(canonical)
			if err != nil {
				return CompileArtifacts{}, fmt.Errorf("sign bundle for node %s: %w", node.ID, err)
			}
			artifacts.Signatures[node.ID] = base64.StdEncoding.EncodeToString(sig)
		}
	}

	if signEnabled {
		artifacts.SigningPubPEM = signer.PublicKeyPEM()
	}

	// Project-level deploy scripts (deploy-all.sh / deploy-all.ps1).
	for name, script := range result.DeployScripts {
		artifacts.Deploy[name] = script
	}

	return artifacts, nil
}
