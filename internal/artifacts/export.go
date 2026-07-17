package artifacts

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/naming"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
)

// ExportResult reports the outcome of an export run.
type ExportResult struct {
	OutputDir string
	Nodes     []string
	// CleanupWarnings report post-commit housekeeping that did not change whether the new exact
	// tree was published. In particular, a prior-tree backup that could not be removed must not be
	// reported as an export rollback: the stage->output rename already committed successfully.
	CleanupWarnings []string
}

// BundleFiles builds a node's canonical, checksummed bundle file set as a path->content map:
// every per-peer wireguard/<iface>.conf (WireGuardConfigs is keyed "nodeID:interfaceName"; a
// client's single wg0 is "nodeID:wg0"), babel/babeld.conf (present for non-client nodes),
// sysctl/99-overlay.conf, install.sh, README.txt, artifacts.json only when a catalog produced
// non-empty content, and at most one optional AgentHeld telemetry policy member. An empty catalog
// omits artifacts.json so the offline bundle stays byte-identical; AirGap omits telemetry policy.
//
// This is the SINGLE source for that set. Within Export the same map drives every view of
// the bundle: the files WRITTEN to disk, the checksums.sha256 that COVER them, the
// manifest.json "files" that LIST them, and (when signing is on) the bundle.sig that SIGNS
// them all derive from these keys — so a member can never be written-but-unlisted (shipped
// UNSIGNED/UNCHECKSUMMED) nor listed-but-unwritten (fails sha256sum -c on the node).
// localcompile.ArtifactsFromResult reshapes the identical set in memory for the frozen
// contract — it too calls here, so the on-disk bundle and the in-memory CompileArtifacts can
// never drift. bundle.sig, signing-pubkey.pem and manifest.json are NOT members (they are the
// authenticity/metadata layer over this set, not part of the checksummed bytes). README.txt is a
// member: its custody-critical apply instructions must be checksum/signature/keystone-bound too.
func BundleFiles(result *compiler.CompileResult, nodeID string) (map[string]string, error) {
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
	telemetryJSON := result.TelemetryPolicyJSON[nodeID]
	successorJSON := result.TelemetrySuccessorPolicyJSON[nodeID]
	if telemetryJSON != "" && successorJSON != "" {
		return nil, fmt.Errorf("node %s contains both %s and %s", nodeID, probepolicy.FileName, probepolicy.SuccessorFileName)
	}
	if result.AgentHeld && telemetryJSON != "" {
		bundleFiles[probepolicy.FileName] = telemetryJSON
	}
	if result.AgentHeld && successorJSON != "" {
		bundleFiles[probepolicy.SuccessorFileName] = successorJSON
	}
	for i := range result.Topology.Nodes {
		if result.Topology.Nodes[i].ID == nodeID {
			bundleFiles["README.txt"] = bundleREADME(result, &result.Topology.Nodes[i])
			break
		}
	}
	return bundleFiles, nil
}

func bundleREADME(result *compiler.CompileResult, node *model.Node) string {
	architecture := "per-peer-interface"
	if node.Role == "client" {
		architecture = "single-interface"
	}
	usage := "  1. Copy this directory to the target host\n  2. Run: sudo bash install.sh\n"
	if result.AgentHeld {
		usage = fmt.Sprintf(`  1. Do NOT run the downloaded install.sh directly.
  2. Provision the operator public credential through a separate trusted channel.
  3. Apply with the command matching that credential:
     Ed25519: sudo yaog-agent kit apply --bundle . --node-id %s --operator-cred <trusted-public-key.pem> --operator-cred-alg ed25519
     WebAuthn: sudo yaog-agent kit apply --bundle . --node-id %s --operator-cred <trusted-public-key.pem> --operator-cred-alg <webauthn-es256|webauthn-eddsa> --operator-rpid <rp-id> --operator-origin <origin>
     (RP ID is required for WebAuthn; origin preserves the enrollment binding.)
  4. Legacy fleets that have never enabled a keystone may instead acknowledge that absence explicitly:
     sudo yaog-agent kit apply --bundle . --node-id %s --dangerously-allow-no-keystone
     Never use that acknowledgement to bypass a configured or previously verified keystone.
`, node.ID, node.ID, node.ID)
	}
	return fmt.Sprintf("Node: %s\nOverlay IP: %s\nRole: %s\nArchitecture: %s\n\nUsage:\n%s",
		node.Name, node.OverlayIP, node.Role, architecture, usage)
}

// bundleFileMode derives a bundle member's file mode from its slash-separated relative path.
// It is the ONE place a member's mode is defined, so the mode a file is WRITTEN with can never
// drift from the member set: install.sh is the root-executed trust anchor (0o755);
// wireguard/<iface>.conf carries a private key (0o600); every other member —
// babel/babeld.conf, sysctl/99-overlay.conf, artifacts.json, and telemetry policy — is readable config
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

// writeFileAtomic replaces path with a complete file whose mode is exact even when an older,
// more-permissive file already exists. os.WriteFile's permission argument applies only on create;
// using it directly could leave a re-exported WireGuard private-key file world-readable.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".yaog-export-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	closed = true
	return os.Rename(tmpPath, path)
}

// Export writes the rendered configuration artifacts for every node to outputDir.
//
// Export is the DISK-WRITE TAIL of the local compile pipeline (plan-3 Phase 6). The
// compile authority — the GenerateKeys → Compile → render.All sequence — now lives solely
// behind the localcompile façade: the standalone CLI, controller, and browser/WASM
// surfaces route their compile through it and hand disk-writing callers the
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
	// Signing is opt-in via bundlesig.EnvSigningKey. Resolve the ConfigSigner once
	// up front (through the shared seam so the env-var name and PEM handling stay in
	// one place) so a malformed key fails the whole export early — before any node
	// dir is touched — rather than mid-loop. When the env var is unset/empty, the
	// signer is nil and the export remains hash-only: byte-for-byte today's output.
	// A future KMS/HSM backend swaps in here with no change to the loop below.
	//
	// This wrapper retains the env read for callers that have not already resolved a
	// signer. Production paths that render an install script first use ExportWithSigner
	// and pass the same immutable signer snapshot to both phases.
	signer, err := bundlesig.LoadConfigSignerFromEnv()
	if err != nil {
		return nil, err
	}
	return ExportWithSigner(result, outputDir, signer)
}

// ExportWithSigner is the explicit-signer disk-write tail. Callers that already resolved a
// signer for rendering use this entry point so install.sh's embedded verification key and the
// emitted bundle.sig/signing-pubkey.pem come from the exact same in-memory key snapshot. A nil
// signer preserves the historical hash-only output. Export remains the environment-loading shim.
func ExportWithSigner(result *compiler.CompileResult, outputDir string, signer bundlesig.ConfigSigner) (*ExportResult, error) {
	if result == nil || result.Topology == nil {
		return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("nil compile result or topology"))
	}
	cleanOutput := filepath.Clean(strings.TrimSpace(outputDir))
	if strings.TrimSpace(outputDir) == "" || cleanOutput == "." || cleanOutput == string(filepath.Separator) {
		return nil, apierr.New(apierr.CodeExportUnsafeName).With("name", outputDir).
			Wrap(fmt.Errorf("output directory must name a dedicated export tree"))
	}
	if info, err := os.Lstat(cleanOutput); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, apierr.New(apierr.CodeExportUnsafeName).With("name", outputDir).
				Wrap(fmt.Errorf("output path must be a real directory, not a symlink or special file"))
		}
	} else if !os.IsNotExist(err) {
		return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("inspect output directory: %w", err))
	}

	parent := filepath.Dir(cleanOutput)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("create output parent: %w", err))
	}
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(cleanOutput)+".stage-*")
	if err != nil {
		return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("create export staging tree: %w", err))
	}
	stageOwned := true
	defer func() {
		if stageOwned {
			_ = os.RemoveAll(stage)
		}
	}()

	exportResult, err := exportInto(result, stage, signer)
	if err != nil {
		return nil, err
	}
	cleanupWarning, err := replaceExportTree(stage, cleanOutput)
	if err != nil {
		return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(err)
	}
	stageOwned = false
	if cleanupWarning != "" {
		exportResult.CleanupWarnings = append(exportResult.CleanupWarnings, cleanupWarning)
	}
	exportResult.OutputDir = outputDir
	return exportResult, nil
}

// exportInto renders a complete export into a fresh private directory. Its caller
// publishes the finished tree as one replacement, so validation/signing/I/O failures
// cannot partially mutate the operator's prior export.
func exportInto(result *compiler.CompileResult, outputDir string, signer bundlesig.ConfigSigner) (*ExportResult, error) {
	exportResult := &ExportResult{
		OutputDir: outputDir,
	}

	signEnabled := signer != nil
	portableNodeIDs := make(map[string]string, len(result.Topology.Nodes))
	for name := range result.DeployScripts {
		if name != "deploy-all.sh" && name != "deploy-all.ps1" {
			return nil, apierr.New(apierr.CodeExportUnsafeName).With("name", name).
				Wrap(fmt.Errorf("unexpected project-level deploy helper"))
		}
	}

	// Export per node.
	for _, node := range result.Topology.Nodes {
		// Node ID is the one canonical bundle-directory key across the CLI exporter,
		// browser/WASM ZIP, controller stage reader, and deploy scripts. Keeping one
		// namespace prevents an ID from selecting another node's name-keyed directory.
		if err := validateSafeName(node.ID); err != nil {
			return nil, apierr.New(apierr.CodeExportUnsafeName).With("name", node.ID).Wrap(err)
		}
		portableKey := naming.PortableNodeIDKey(node.ID)
		if first, exists := portableNodeIDs[portableKey]; exists {
			return nil, apierr.New(apierr.CodeExportUnsafeName).With("name", node.ID).
				Wrap(fmt.Errorf("node ID collides with %q on a case-insensitive export filesystem", first))
		}
		portableNodeIDs[portableKey] = node.ID
		nodeDir := filepath.Join(outputDir, node.ID)
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
		bundleFiles, err := BundleFiles(result, node.ID)
		if err != nil {
			return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(err)
		}
		if err := validateBundleMemberPaths(bundleFiles); err != nil {
			return nil, apierr.New(apierr.CodeExportUnsafeName).With("name", node.ID).Wrap(err)
		}
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
			if err := writeFileAtomic(abs, []byte(bundleFiles[rel]), bundleFileMode(rel)); err != nil {
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
		// sysctl/99-overlay.conf, install.sh, README, and optional artifact/telemetry policy. install.sh is the root-executed trust anchor
		// and was historically the only artifact not covered by checksums.sha256 (audit item
		// D24). manifest.json is still deliberately excluded: it carries compile-time
		// timestamps (compiled_at, etc.) and is out of integrity-check scope (see
		// docs/spec/security/security.md). bundle.sig and signing-pubkey.pem (when signing is
		// enabled) are also excluded by construction: bundle.sig signs this very content and
		// the pubkey is the verification anchor, so neither can self-reference. Optional artifacts
		// and telemetry policy join the set so their authority inherits the same signature and
		// keystone digest binding; both remain omitted when absent.
		canonical := bundlesig.Canonicalize(bundleFiles)
		checksumsPath := filepath.Join(nodeDir, "checksums.sha256")
		if err := writeFileAtomic(checksumsPath, canonical, 0644); err != nil {
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
			if err := writeFileAtomic(sigPath, []byte(sigB64+"\n"), 0644); err != nil {
				return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("write bundle.sig: %w", err))
			}
			pubPath := filepath.Join(nodeDir, "signing-pubkey.pem")
			if err := writeFileAtomic(pubPath, signer.PublicKeyPEM(), 0644); err != nil {
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
		if err := writeFileAtomic(path, manifestJSON, 0644); err != nil {
			return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("write manifest: %w", err))
		}

		exportResult.Nodes = append(exportResult.Nodes, node.ID)
	}

	// Write project-level deploy scripts to the root of the export directory
	for name, script := range result.DeployScripts {
		path := filepath.Join(outputDir, name)
		perm := os.FileMode(0644)
		if strings.HasSuffix(name, ".sh") {
			perm = 0755
		}
		if err := writeFileAtomic(path, []byte(script), perm); err != nil {
			return nil, apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("write deploy script %s: %w", name, err))
		}
	}

	return exportResult, nil
}

// replaceExportTree swaps a complete staged export into place. An existing real
// directory is first moved to a private sibling backup so a failed publication can
// restore it; stale files, removed nodes, and obsolete signing sidecars therefore
// cannot survive a successful re-export. Symlink destinations were rejected before
// rendering and are checked again here to narrow the race window.
var removeExportBackup = os.RemoveAll

func replaceExportTree(stage, outputDir string) (cleanupWarning string, err error) {
	parent := filepath.Dir(outputDir)
	base := filepath.Base(outputDir)
	var backup string
	if info, err := os.Lstat(outputDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("publish export: output path is no longer a real directory")
		}
		reserved, err := os.MkdirTemp(parent, "."+base+".backup-*")
		if err != nil {
			return "", fmt.Errorf("reserve export backup: %w", err)
		}
		if err := os.Remove(reserved); err != nil {
			return "", fmt.Errorf("prepare export backup: %w", err)
		}
		backup = reserved
		if err := os.Rename(outputDir, backup); err != nil {
			return "", fmt.Errorf("move prior export aside: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect output before publish: %w", err)
	}

	if err := os.Rename(stage, outputDir); err != nil {
		if backup != "" {
			if restoreErr := os.Rename(backup, outputDir); restoreErr != nil {
				return "", fmt.Errorf("publish new export: %w (also failed to restore prior export: %v)", err, restoreErr)
			}
		}
		return "", fmt.Errorf("publish new export: %w", err)
	}
	if backup != "" {
		// The rename above is the publication commit point. Cleanup cannot truthfully turn that
		// committed result into an ordinary export error—the caller would be told its old tree
		// remained live when the new one is already visible. Surface a non-fatal warning instead.
		// Restrict the leftover backup root before attempting removal because an older AirGap tree
		// can contain private WireGuard material (its files are already 0600).
		_ = os.Chmod(backup, 0700)
		if err := removeExportBackup(backup); err != nil {
			return fmt.Sprintf("new export committed, but prior backup %s could not be removed: %v", backup, err), nil
		}
	}
	return "", nil
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
	if !naming.ValidPortableNodeID(name) {
		return fmt.Errorf("node ID is not a portable bundle-directory key: %q", name)
	}
	return nil
}

func validateBundleMemberPaths(files map[string]string) error {
	caseFolded := make(map[string]string, len(files))
	for rel := range files {
		if rel == "" || path.IsAbs(rel) || path.Clean(rel) != rel || strings.ContainsAny(rel, "\\:\r\n\x00") {
			return fmt.Errorf("bundle member %q is not a canonical portable relative path", rel)
		}
		key := strings.ToLower(rel)
		if first, exists := caseFolded[key]; exists {
			return fmt.Errorf("bundle members %q and %q collide on a case-insensitive filesystem", first, rel)
		}
		caseFolded[key] = rel
	}
	return nil
}
