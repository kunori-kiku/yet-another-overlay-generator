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
// This skeleton phase (plan-3 Phase 1) wraps — it does not yet de-impurify. It calls the
// existing render.GenerateKeys / compiler.Compile / render.All path verbatim and only
// re-shapes the resulting *compiler.CompileResult into a CompileArtifacts, so the output
// is byte-identical to the legacy callers. The keygen seam (req.Keygen), the explicit
// clock (req.CompiledAt), and the injected signer (req.SigningKey) are wired into the
// pipeline in plan-3 Phase 2 and Phase 4; until then the signer is read from the
// environment exactly as artifacts.Export does today (LoadConfigSignerFromEnv).
func Compile(req CompileRequest) (CompileArtifacts, error) {
	// Work on a local copy of the topology so the caller's value is not mutated by the
	// pipeline's write-backs (GenerateKeys, IP allocation, and the pin write-back all
	// mutate the topology in place). The returned CompileArtifacts.Topology is the
	// compiled (written-back) topology.
	topo := req.Topology

	keys, err := render.GenerateKeys(&topo, req.Custody)
	if err != nil {
		return CompileArtifacts{}, err
	}

	c := compiler.NewCompiler()
	if req.Reserved != nil {
		c = c.WithReserved(req.Reserved)
	}
	result, err := c.Compile(context.Background(), &topo, keys)
	if err != nil {
		return CompileArtifacts{}, err
	}

	if err := render.All(result, keys, req.Fetch); err != nil {
		return CompileArtifacts{}, err
	}

	return reshape(result)
}

// reshape turns a fully-rendered *compiler.CompileResult into the canonical
// CompileArtifacts. The per-node Files map, the per-node checksums, and the optional
// detached signatures are built to mirror artifacts.Export's on-disk bundle exactly
// (export.go) so the in-memory contract is byte-consistent with the real exporter.
func reshape(result *compiler.CompileResult) (CompileArtifacts, error) {
	// Signing is opt-in via bundlesig.EnvSigningKey, resolved once up front so a
	// malformed key fails the whole compile early — identical to artifacts.Export.
	// (plan-3 Phase 4 replaces this env read with the injected req.SigningKey.)
	signer, err := bundlesig.LoadConfigSignerFromEnv()
	if err != nil {
		return CompileArtifacts{}, err
	}
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
