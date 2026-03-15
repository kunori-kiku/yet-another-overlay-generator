package artifacts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
)

// NodeArtifact 单节点产物
type NodeArtifact struct {
	NodeID         string
	NodeName       string
	WireGuardConf  string
	BabelConf      string
	SysctlConf     string
	InstallScript  string
}

// ExportResult 导出结果
type ExportResult struct {
	OutputDir string
	Nodes     []string
}

// Export 将编译结果导出到目录
func Export(result *compiler.CompileResult, outputDir string) (*ExportResult, error) {
	exportResult := &ExportResult{
		OutputDir: outputDir,
	}

	// 为每个节点创建目录并写入产物
	for _, node := range result.Topology.Nodes {
		nodeDir := filepath.Join(outputDir, node.Name)

		// 创建子目录
		dirs := []string{
			filepath.Join(nodeDir, "wireguard"),
			filepath.Join(nodeDir, "babel"),
			filepath.Join(nodeDir, "sysctl"),
		}
		for _, dir := range dirs {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("创建目录 %s 失败: %w", dir, err)
			}
		}

		// 写入 WireGuard 配置
		if wgConf, ok := result.WireGuardConfigs[node.ID]; ok {
			path := filepath.Join(nodeDir, "wireguard", "wg0.conf")
			if err := os.WriteFile(path, []byte(wgConf), 0600); err != nil {
				return nil, fmt.Errorf("写入 WireGuard 配置失败: %w", err)
			}
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

		// 写入 manifest.json
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
			"files": []string{
				"wireguard/wg0.conf",
				"babel/babeld.conf",
				"sysctl/99-overlay.conf",
				"install.sh",
			},
		}
		manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("生成 manifest 失败: %w", err)
		}
		path := filepath.Join(nodeDir, "manifest.json")
		if err := os.WriteFile(path, manifestJSON, 0644); err != nil {
			return nil, fmt.Errorf("写入 manifest 失败: %w", err)
		}

		// 写入 README.txt
		readme := fmt.Sprintf("Node: %s\nOverlay IP: %s\nRole: %s\n\nUsage:\n  1. Copy this directory to the target host\n  2. Run: sudo bash install.sh\n",
			node.Name, node.OverlayIP, node.Role)
		readmePath := filepath.Join(nodeDir, "README.txt")
		if err := os.WriteFile(readmePath, []byte(readme), 0644); err != nil {
			return nil, fmt.Errorf("写入 README 失败: %w", err)
		}

		exportResult.Nodes = append(exportResult.Nodes, node.Name)
	}

	return exportResult, nil
}
