package artifacts

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
func Export(result *compiler.CompileResult, outputDir string) (*ExportResult, error) {
	exportResult := &ExportResult{
		OutputDir: outputDir,
	}

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

		// 生成 checksums
		var checksumLines []string
		for configKey, wgConf := range result.WireGuardConfigs {
			parts := strings.SplitN(configKey, ":", 2)
			if len(parts) != 2 || parts[0] != node.ID {
				continue
			}
			confFileName := parts[1] + ".conf"
			checksumLines = append(checksumLines, fmt.Sprintf("%x  wireguard/%s", sha256.Sum256([]byte(wgConf)), confFileName))
		}
		if babelConf, ok := result.BabelConfigs[node.ID]; ok {
			checksumLines = append(checksumLines, fmt.Sprintf("%x  babel/babeld.conf", sha256.Sum256([]byte(babelConf))))
		}
		if sysctlConf, ok := result.SysctlConfigs[node.ID]; ok {
			checksumLines = append(checksumLines, fmt.Sprintf("%x  sysctl/99-overlay.conf", sha256.Sum256([]byte(sysctlConf))))
		}

		checksumsPath := filepath.Join(nodeDir, "checksums.sha256")
		if err := os.WriteFile(checksumsPath, []byte(strings.Join(checksumLines, "\n")), 0644); err != nil {
			return nil, fmt.Errorf("写入 checksums.sha256 失败: %w", err)
		}

		// 构建文件列表
		var allFiles []string
		allFiles = append(allFiles, wgFiles...)
		if !isClient {
			allFiles = append(allFiles, "babel/babeld.conf")
		}
		allFiles = append(allFiles, "sysctl/99-overlay.conf", "install.sh")

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
		readme := fmt.Sprintf("Node: %s\nOverlay IP: %s\nRole: %s\nArchitecture: per-peer WireGuard interfaces\n\nUsage:\n  1. Copy this directory to the target host\n  2. Run: sudo bash install.sh\n",
			node.Name, node.OverlayIP, node.Role)
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
