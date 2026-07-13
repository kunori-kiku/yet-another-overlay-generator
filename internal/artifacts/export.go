package artifacts

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
)

// NodeArtifact holds the rendered configuration artifacts for a single node.
type NodeArtifact struct {
	NodeID        string
	NodeName      string
	WireGuardConf string
	BabelConf     string
	SysctlConf    string
	InstallScript string
}

// ExportResult reports the outcome of an export run.
type ExportResult struct {
	OutputDir string
	Nodes     []string
}

// BundleFiles builds a node's canonical, checksummed bundle file set as a path->content map:
// every per-peer wireguard/<iface>.conf (WireGuardConfigs is keyed "nodeID:interfaceName"; a
// client's single wg0 is "nodeID:wg0"), babel/babeld.conf (present for non-client nodes),
// sysctl/99-overlay.conf, install.sh, and artifacts.json only when a catalog produced
// non-empty content (the D4 guard — an empty catalog omits the file so the air-gap bundle
// stays byte-identical).
//
// This is the SINGLE source for that set. Within Export the same map drives every view of
// the bundle: the files WRITTEN to disk, the checksums.sha256 that COVER them, the
// manifest.json "files" that LIST them, and (when signing is on) the bundle.sig that SIGNS
// them all derive from these keys — so a member can never be written-but-unlisted (shipped
// UNSIGNED/UNCHECKSUMMED) nor listed-but-unwritten (fails sha256sum -c on the node).
// localcompile.ArtifactsFromResult reshapes the identical set in memory for the frozen
// contract — it too calls here, so the on-disk bundle and the in-memory CompileArtifacts can
// never drift. bundle.sig, signing-pubkey.pem and manifest.json are NOT members (they are the
// authenticity/metadata layer over this set, not part of the checksummed bytes).
func BundleFiles(result *compiler.CompileResult, nodeID string) map[string]string {
	bundleFiles := make(map[string]string)
	for configKey, wgConf := range result.WireGuardConfigs {
		parts := strings.SplitN(configKey, ":", 2)
		if len(parts) != 2 || parts[0] != nodeID {
			continue
		}
		bundleFiles["wireguard/"+parts[1]+".conf"] = wgConf
	}
	if babelConf, ok := result.BabelConfigs[nodeID]; ok {
		bundleFiles["babel/babeld.conf"] = babelConf
	}
	if sysctlConf, ok := result.SysctlConfigs[nodeID]; ok {
		bundleFiles["sysctl/99-overlay.conf"] = sysctlConf
	}
	if script, ok := result.InstallScripts[nodeID]; ok {
		bundleFiles["install.sh"] = script
	}
	if artifactsJSON, ok := result.ArtifactsJSON[nodeID]; ok && artifactsJSON != "" {
		bundleFiles["artifacts.json"] = artifactsJSON
	}
	return bundleFiles
}

// bundleFileMode derives a bundle member's file mode from its slash-separated relative path.
// It is the ONE place a member's mode is defined, so the mode a file is WRITTEN with can never
// drift from the member set: install.sh is the root-executed trust anchor (0o755);
// wireguard/<iface>.conf carries a private key (0o600); every other member —
// babel/babeld.conf, sysctl/99-overlay.conf, artifacts.json — is world-readable config
// (0o644). This reproduces exactly the per-file modes the pre-single-source write-loop used.
func bundleFileMode(rel string) os.FileMode {
	switch {
	case rel == "install.sh":
		return 0o755
	case strings.HasPrefix(rel, "wireguard/"):
		return 0o600
	default:
		return 0o644
	}
}

// Export writes the rendered configuration artifacts for every node to outputDir.
//
// Export is the DISK-WRITE TAIL of the local compile pipeline (plan-3 Phase 6). The
// compile authority — the GenerateKeys → Compile → render.All sequence — now lives solely
// behind the localcompile façade: all three live callers (the air-gap CLI/API and the
// controller subgraph compile) route their compile through it and hand Export the
// resulting *compiler.CompileResult. Export's job is purely presentation: lay the rendered
// bytes out on disk under the per-node directory shape (it owns no compile or render
// step). It keeps its *compiler.CompileResult signature so those callers stay call-
// compatible, and its output is byte-for-byte identical to the pre-façade exporter.
//
// The per-node bundle set is built ONCE by BundleFiles and drives every downstream view
// within Export: the files WRITTEN to disk, the checksums.sha256 that cover them, the
// manifest.json "files" that list them, and (when signing is on) the bundle.sig that signs
// them — so a member is never written-but-unlisted (shipped unsigned) nor listed-but-unwritten
// (fails sha256sum -c). BundleFiles is also the SAME helper the contract's
// localcompile.ArtifactsFromResult calls to re-shape the set in memory — one source of truth,
// so the on-disk bundle and the in-memory CompileArtifacts can never drift (the localcompile
// golden corpus + lossless-wrapper test pin the result on top of that). The
// shared helper lives here rather than in the façade because artifacts is a sink package
// (apierr/bundlesig/compiler only): localcompile imports it freely, whereas the reverse would
// cycle (render's tests depend on this package, and localcompile depends on render).
//
// The exported bundle's checksums.sha256 (and, when signing is on, bundle.sig) cover
// ONLY those rendered artifacts. The keystone trust-list files (trustlist.json /
// trustlist.sig) are deliberately NOT exported here: the off-host-signed manifest binds
// each node's checksums.sha256 DIGEST, so those files cannot live inside the very checksum
// set they bind. The controller appends them to the SERVED file map at /config time
// instead (plan-5.1 CORRECTION, 2026-06-08).
func Export(result *compiler.CompileResult, outputDir string) (*ExportResult, error) {
	exportResult := &ExportResult{
		OutputDir: outputDir,
	}

	// Signing is opt-in via bundlesig.EnvSigningKey. Resolve the ConfigSigner once
	// up front (through the shared seam so the env-var name and PEM handling stay in
	// one place, identical to the install-script renderer and the self-extracting
	// installer) so a malformed key fails the whole export early — before any node
	// dir is touched — rather than mid-loop. When the env var is unset/empty, the
	// signer is nil and the export remains hash-only: byte-for-byte today's output.
	// A future KMS/HSM backend swaps in here with no change to the loop below.
	//
	// This env read is the one impurity Export retains on purpose: the controller's
	// CompileAndStage relies on Export signing at the export boundary (the Phase-0 env
	// path), and the air-gap CLI/API resolves the same signer when building its
	// CompileRequest — so the bundle-signing key has a single resolution seam.
	signer, err := bundlesig.LoadConfigSignerFromEnv()
	if err != nil {
		return nil, err
	}
	signEnabled := signer != nil

	// Export per node.
	for _, node := range result.Topology.Nodes {
		// Validate node name to prevent path traversal
		if err := validateSafeName(node.Name); err != nil {
			return nil, apierr.New(apierr.CodeExportUnsafeName).With("name", node.Name).Wrap(err)
		}
		nodeDir := filepath.Join(outputDir, node.Name)
		isClient := node.Role == "client"

		// Write the node's bundle. BundleFiles is the SINGLE source of the member set, so
		// the files WRITTEN here are EXACTLY the set checksummed below, listed in
		// manifest.json, and (when signing is on) signed — a member can never be
		// written-but-unlisted (shipped unsigned) nor listed-but-unwritten (fails
		// sha256sum -c). Each member's mode derives from its slash path via bundleFileMode
		// (wireguard/* 0600, install.sh 0755, else 0644) and its parent dir is created on
		// demand. Members are written in sorted order so the run is deterministic; that same
		// sorted key list is reused verbatim as manifest.json's "files" below (replacing the
		// old wg-map-ordered — non-reproducible — list).
		bundleFiles := BundleFiles(result, node.ID)
		members := make([]string, 0, len(bundleFiles))
		for rel := range bundleFiles {
			members = append(members, rel)
		}
		sort.Strings(members)
		for _, rel := range members {
			abs := filepath.Join(nodeDir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
				return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("create dir %s: %w", filepath.Dir(abs), err))
			}
			if err := os.WriteFile(abs, []byte(bundleFiles[rel]), bundleFileMode(rel)); err != nil {
				return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("write bundle file %s: %w", rel, err))
			}
		}

		// bundlesig.Canonicalize emits the checksums.sha256 content from the SAME bundleFiles
		// map just written to disk. The output is SORTED by path and deterministic across
		// runs; sha256sum -c is order insensitive, so sorting is safe. BundleFiles is the
		// single source for this set (shared with the contract's
		// localcompile.ArtifactsFromResult), so the on-disk checksums.sha256 and the in-memory
		// CompileArtifacts.Checksums never diverge.
		//
		// The set matches the rest of the bundle exactly — it IS the set written just above:
		// every per-peer wireguard/<iface>.conf, babel/babeld.conf (non-client only),
		// sysctl/99-overlay.conf, and install.sh. install.sh is the root-executed trust anchor
		// and was historically the only artifact not covered by checksums.sha256 (audit item
		// D24). manifest.json is still deliberately excluded: it carries compile-time
		// timestamps (compiled_at, etc.) and is out of integrity-check scope (see
		// docs/spec/security/security.md). bundle.sig and signing-pubkey.pem (when signing is
		// enabled) are also excluded by construction: bundle.sig signs this very content and
		// the pubkey is the verification anchor, so neither can self-reference. (artifacts.json
		// joins the set so its pins inherit the bundle's Ed25519 signature + keystone digest
		// binding — no new trust primitive; omitted when absent, D4.)
		canonical := bundlesig.Canonicalize(bundleFiles)
		checksumsPath := filepath.Join(nodeDir, "checksums.sha256")
		if err := os.WriteFile(checksumsPath, canonical, 0644); err != nil {
			return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("write checksums.sha256: %w", err))
		}

		// The manifest's file list IS the member set (sorted): LISTED == WRITTEN ==
		// checksummed. The signing block below appends bundle.sig/signing-pubkey.pem — the
		// authenticity layer over this set, not members of it.
		allFiles := append([]string(nil), members...)

		// When signing is enabled, sign the canonical checksums and write the
		// detached signature (base64) plus the verifying public key (PKIX PEM)
		// into each node dir. The signature covers the exact bytes written to
		// checksums.sha256 above. Both files are listed in the manifest but are
		// NOT part of the canonical/checksummed set (they are the authenticity
		// layer over it, not members of it). The public key embedded into
		// install.sh is the script renderer's responsibility (it reads the same
		// env var at render time); here we only ship the openssl-consumable PEM.
		if signEnabled {
			sig, err := signer.Sign(canonical)
			if err != nil {
				return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("sign bundle: %w", err))
			}
			sigB64 := base64.StdEncoding.EncodeToString(sig)
			sigPath := filepath.Join(nodeDir, "bundle.sig")
			if err := os.WriteFile(sigPath, []byte(sigB64+"\n"), 0644); err != nil {
				return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("write bundle.sig: %w", err))
			}
			pubPath := filepath.Join(nodeDir, "signing-pubkey.pem")
			if err := os.WriteFile(pubPath, signer.PublicKeyPEM(), 0644); err != nil {
				return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("write signing-pubkey.pem: %w", err))
			}
			allFiles = append(allFiles, "bundle.sig", "signing-pubkey.pem")
		}

		architecture := "per-peer-interface"
		if isClient {
			architecture = "single-interface"
		}

		manifest := map[string]interface{}{
			"node_id":      node.ID,
			"node_name":    node.Name,
			"overlay_ip":   node.OverlayIP,
			"role":         node.Role,
			"domain_id":    node.DomainID,
			"project_id":   result.Manifest.ProjectID,
			"project_name": result.Manifest.ProjectName,
			"version":      result.Manifest.Version,
			"compiled_at":  result.Manifest.CompiledAt.Format("2006-01-02T15:04:05Z"),
			"checksum":     result.Manifest.Checksum,
			"architecture": architecture,
			"files":        allFiles,
		}
		manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("marshal manifest: %w", err))
		}
		path := filepath.Join(nodeDir, "manifest.json")
		if err := os.WriteFile(path, manifestJSON, 0644); err != nil {
			return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("write manifest: %w", err))
		}

		// Write the README.
		//
		// D76: the README's Architecture line was previously hardcoded to "per-peer WireGuard
		// interfaces", written even for a client bundle (single wg0 interface), contradicting
		// the architecture field of the manifest.json in the same directory. Reuse the same
		// architecture value the manifest uses above so the two stay consistent.
		readme := fmt.Sprintf("Node: %s\nOverlay IP: %s\nRole: %s\nArchitecture: %s\n\nUsage:\n  1. Copy this directory to the target host\n  2. Run: sudo bash install.sh\n",
			node.Name, node.OverlayIP, node.Role, architecture)
		readmePath := filepath.Join(nodeDir, "README.txt")
		if err := os.WriteFile(readmePath, []byte(readme), 0644); err != nil {
			return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("write README: %w", err))
		}

		exportResult.Nodes = append(exportResult.Nodes, node.Name)
	}

	// Write project-level deploy scripts to the root of the export directory
	for name, script := range result.DeployScripts {
		path := filepath.Join(outputDir, name)
		perm := os.FileMode(0644)
		if strings.HasSuffix(name, ".sh") {
			perm = 0755
		}
		if err := os.WriteFile(path, []byte(script), perm); err != nil {
			return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("write deploy script %s: %w", name, err))
		}
	}

	return exportResult, nil
}

// validateSafeName checks that a name is safe to use as a directory or file name
// component, rejecting names that could cause path traversal or other issues.
func validateSafeName(name string) error {
	if name == "" {
		return fmt.Errorf("name must not be empty")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("invalid name: %q", name)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("name must not contain a path separator: %q", name)
	}
	if filepath.IsAbs(name) {
		return fmt.Errorf("name must not be an absolute path: %q", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("name must not contain '..': %q", name)
	}
	return nil
}
