package artifacts

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// Export writes the rendered configuration artifacts for every node to outputDir.
//
// The exported bundle's checksums.sha256 (and, when signing is on, bundle.sig) cover
// ONLY the rendered artifacts — every per-peer wireguard/<iface>.conf, babel/babeld.conf
// (non-client only), sysctl/99-overlay.conf, and install.sh. The keystone trust-list
// files (trustlist.json / trustlist.sig) are deliberately NOT exported here: the
// off-host-signed manifest binds each node's checksums.sha256 DIGEST, so those files
// cannot live inside the very checksum set they bind. The controller appends them to the
// SERVED file map at /config time instead (plan-5.1 CORRECTION, 2026-06-08).
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

		// Create directories.
		dirs := []string{
			filepath.Join(nodeDir, "wireguard"),
			filepath.Join(nodeDir, "sysctl"),
		}
		if !isClient {
			dirs = append(dirs, filepath.Join(nodeDir, "babel"))
		}
		for _, dir := range dirs {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("create dir %s: %w", dir, err))
			}
		}

		// Write the per-peer WireGuard configs.
		// WireGuardConfigs keys have the format "nodeID:interfaceName".
		var wgFiles []string
		for configKey, wgConf := range result.WireGuardConfigs {
			// Parse the key.
			parts := strings.SplitN(configKey, ":", 2)
			if len(parts) != 2 || parts[0] != node.ID {
				continue
			}
			ifaceName := parts[1]
			confFileName := ifaceName + ".conf"
			path := filepath.Join(nodeDir, "wireguard", confFileName)
			if err := os.WriteFile(path, []byte(wgConf), 0600); err != nil {
				return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("write wireguard config: %w", err))
			}
			wgFiles = append(wgFiles, "wireguard/"+confFileName)
		}

		// Write the Babel config.
		if babelConf, ok := result.BabelConfigs[node.ID]; ok {
			path := filepath.Join(nodeDir, "babel", "babeld.conf")
			if err := os.WriteFile(path, []byte(babelConf), 0644); err != nil {
				return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("write babel config: %w", err))
			}
		}

		// Write the sysctl config.
		if sysctlConf, ok := result.SysctlConfigs[node.ID]; ok {
			path := filepath.Join(nodeDir, "sysctl", "99-overlay.conf")
			if err := os.WriteFile(path, []byte(sysctlConf), 0644); err != nil {
				return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("write sysctl config: %w", err))
			}
		}

		// Write the install script.
		if script, ok := result.InstallScripts[node.ID]; ok {
			path := filepath.Join(nodeDir, "install.sh")
			if err := os.WriteFile(path, []byte(script), 0755); err != nil {
				return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("write install script: %w", err))
			}
		}

		// Write artifacts.json (populated by render.All only when a mimic/agent catalog is
		// configured). It is a signed bundleFiles member that carries mimic's GitHub-.deb pin
		// (asset+sha256); the install script reads the pin only after passing the integrity
		// check. No catalog configured => empty content => the whole file is omitted, keeping
		// the air-gap bundle byte-for-byte unchanged (D4).
		artifactsJSON, hasArtifacts := result.ArtifactsJSON[node.ID]
		hasArtifacts = hasArtifacts && artifactsJSON != ""
		if hasArtifacts {
			path := filepath.Join(nodeDir, "artifacts.json")
			if err := os.WriteFile(path, []byte(artifactsJSON), 0644); err != nil {
				return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("write artifacts.json: %w", err))
			}
		}

		// Build the canonical bundle file set as a path->content map and let
		// bundlesig.Canonicalize emit the checksums.sha256 content. This replaces
		// the previous ad-hoc, append-ordered checksum writing: the output is now
		// SORTED by path and deterministic across runs. sha256sum -c is order
		// insensitive, so sorting is safe and is precisely the determinism fix.
		//
		// The set must match the rest of the bundle exactly: every per-peer
		// wireguard/<iface>.conf, babel/babeld.conf (non-client only), sysctl/
		// 99-overlay.conf, and install.sh — written above before this point so the
		// hashes describe the same bytes that landed on disk. install.sh is the
		// root-executed trust anchor and was historically the only artifact not
		// covered by checksums.sha256 (audit item D24). manifest.json is still
		// deliberately excluded: it carries compile-time timestamps (compiled_at,
		// etc.) and is out of integrity-check scope (see docs/spec/security/security.md).
		// bundle.sig and signing-pubkey.pem (when signing is enabled) are also
		// excluded by construction: bundle.sig signs this very content and the
		// pubkey is the verification anchor, so neither can self-reference.
		bundleFiles := make(map[string]string)
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
		// artifacts.json joins the checksummed set so its pins inherit the bundle's Ed25519
		// signature + keystone digest binding — no new trust primitive. Omitted when absent (D4).
		if hasArtifacts {
			bundleFiles["artifacts.json"] = artifactsJSON
		}

		canonical := bundlesig.Canonicalize(bundleFiles)
		checksumsPath := filepath.Join(nodeDir, "checksums.sha256")
		if err := os.WriteFile(checksumsPath, canonical, 0644); err != nil {
			return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("write checksums.sha256: %w", err))
		}

		// Build the file list.
		var allFiles []string
		allFiles = append(allFiles, wgFiles...)
		if !isClient {
			allFiles = append(allFiles, "babel/babeld.conf")
		}
		allFiles = append(allFiles, "sysctl/99-overlay.conf", "install.sh")
		if hasArtifacts {
			allFiles = append(allFiles, "artifacts.json")
		}

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
