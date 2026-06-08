package artifacts

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
)

// NodeArtifact 节点产物
type NodeArtifact struct {
	NodeID        string
	NodeName      string
	WireGuardConf string
	BabelConf     string
	SysctlConf    string
	InstallScript string
}

// ExportResult 导出结果
type ExportResult struct {
	OutputDir string
	Nodes     []string
}

// Export 导出所有节点的配置产物
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

	// Signing is opt-in via bundlesig.EnvSigningKey. Load the key once up front
	// (through the shared loader so the env-var name and PEM handling stay in one
	// place, identical to the install-script renderer and the self-extracting
	// installer) so a malformed key fails the whole export early — before any node
	// dir is touched — rather than mid-loop. When the env var is unset/empty,
	// signing is nil and the export remains hash-only: byte-for-byte today's output.
	signing, err := bundlesig.LoadSigningFromEnv()
	if err != nil {
		return nil, err
	}
	signEnabled := signing != nil

	// 按节点导出
	for _, node := range result.Topology.Nodes {
		// Validate node name to prevent path traversal
		if err := validateSafeName(node.Name); err != nil {
			return nil, fmt.Errorf("节点名称不安全，跳过导出: %w", err)
		}
		nodeDir := filepath.Join(outputDir, node.Name)
		isClient := node.Role == "client"

		// 创建目录
		dirs := []string{
			filepath.Join(nodeDir, "wireguard"),
			filepath.Join(nodeDir, "sysctl"),
		}
		if !isClient {
			dirs = append(dirs, filepath.Join(nodeDir, "babel"))
		}
		for _, dir := range dirs {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("创建 %s 目录失败: %w", dir, err)
			}
		}

		// 写入 per-peer WireGuard 配置
		// WireGuardConfigs 的 key 格式为 "nodeID:interfaceName"
		var wgFiles []string
		for configKey, wgConf := range result.WireGuardConfigs {
			// 解析 key
			parts := strings.SplitN(configKey, ":", 2)
			if len(parts) != 2 || parts[0] != node.ID {
				continue
			}
			ifaceName := parts[1]
			confFileName := ifaceName + ".conf"
			path := filepath.Join(nodeDir, "wireguard", confFileName)
			if err := os.WriteFile(path, []byte(wgConf), 0600); err != nil {
				return nil, fmt.Errorf("写入 WireGuard 配置失败: %w", err)
			}
			wgFiles = append(wgFiles, "wireguard/"+confFileName)
		}

		// 写入 Babel 配置
		if babelConf, ok := result.BabelConfigs[node.ID]; ok {
			path := filepath.Join(nodeDir, "babel", "babeld.conf")
			if err := os.WriteFile(path, []byte(babelConf), 0644); err != nil {
				return nil, fmt.Errorf("写入 Babel 配置失败: %w", err)
			}
		}

		// 写入 sysctl 配置
		if sysctlConf, ok := result.SysctlConfigs[node.ID]; ok {
			path := filepath.Join(nodeDir, "sysctl", "99-overlay.conf")
			if err := os.WriteFile(path, []byte(sysctlConf), 0644); err != nil {
				return nil, fmt.Errorf("写入 sysctl 配置失败: %w", err)
			}
		}

		// 写入安装脚本
		if script, ok := result.InstallScripts[node.ID]; ok {
			path := filepath.Join(nodeDir, "install.sh")
			if err := os.WriteFile(path, []byte(script), 0755); err != nil {
				return nil, fmt.Errorf("写入安装脚本失败: %w", err)
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

		canonical := bundlesig.Canonicalize(bundleFiles)
		checksumsPath := filepath.Join(nodeDir, "checksums.sha256")
		if err := os.WriteFile(checksumsPath, canonical, 0644); err != nil {
			return nil, fmt.Errorf("写入 checksums.sha256 失败: %w", err)
		}

		// 构建文件列表
		var allFiles []string
		allFiles = append(allFiles, wgFiles...)
		if !isClient {
			allFiles = append(allFiles, "babel/babeld.conf")
		}
		allFiles = append(allFiles, "sysctl/99-overlay.conf", "install.sh")

		// When signing is enabled, sign the canonical checksums and write the
		// detached signature (base64) plus the verifying public key (PKIX PEM)
		// into each node dir. The signature covers the exact bytes written to
		// checksums.sha256 above. Both files are listed in the manifest but are
		// NOT part of the canonical/checksummed set (they are the authenticity
		// layer over it, not members of it). The public key embedded into
		// install.sh is the script renderer's responsibility (it reads the same
		// env var at render time); here we only ship the openssl-consumable PEM.
		if signEnabled {
			sig := bundlesig.Sign(canonical, signing.Priv)
			sigB64 := base64.StdEncoding.EncodeToString(sig)
			sigPath := filepath.Join(nodeDir, "bundle.sig")
			if err := os.WriteFile(sigPath, []byte(sigB64+"\n"), 0644); err != nil {
				return nil, fmt.Errorf("写入 bundle.sig 失败: %w", err)
			}
			pubPath := filepath.Join(nodeDir, "signing-pubkey.pem")
			if err := os.WriteFile(pubPath, signing.PubKeyPEM, 0644); err != nil {
				return nil, fmt.Errorf("写入 signing-pubkey.pem 失败: %w", err)
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
			return nil, fmt.Errorf("生成 manifest 失败: %w", err)
		}
		path := filepath.Join(nodeDir, "manifest.json")
		if err := os.WriteFile(path, manifestJSON, 0644); err != nil {
			return nil, fmt.Errorf("写入 manifest 失败: %w", err)
		}

		// 写入 README
		//
		// D76: README 的 Architecture 行此前硬编码为 "per-peer WireGuard interfaces"，
		// 即使是 client bundle（单接口 wg0）也照写，与同目录 manifest.json 的 architecture
		// 字段自相矛盾。改为复用上面 manifest 用的同一个 architecture 值，保持二者一致。
		readme := fmt.Sprintf("Node: %s\nOverlay IP: %s\nRole: %s\nArchitecture: %s\n\nUsage:\n  1. Copy this directory to the target host\n  2. Run: sudo bash install.sh\n",
			node.Name, node.OverlayIP, node.Role, architecture)
		readmePath := filepath.Join(nodeDir, "README.txt")
		if err := os.WriteFile(readmePath, []byte(readme), 0644); err != nil {
			return nil, fmt.Errorf("写入 README 失败: %w", err)
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
			return nil, fmt.Errorf("写入部署脚本 %s 失败: %w", name, err)
		}
	}

	return exportResult, nil
}

// validateSafeName checks that a name is safe to use as a directory or file name
// component, rejecting names that could cause path traversal or other issues.
func validateSafeName(name string) error {
	if name == "" {
		return fmt.Errorf("名称不能为空")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("名称不合法: %q", name)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("名称不能包含路径分隔符: %q", name)
	}
	if filepath.IsAbs(name) {
		return fmt.Errorf("名称不能为绝对路径: %q", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("名称不能包含 '..': %q", name)
	}
	return nil
}
