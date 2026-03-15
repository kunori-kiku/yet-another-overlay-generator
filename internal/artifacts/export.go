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

// NodeArtifact 
type NodeArtifact struct {
	NodeID        string
	NodeName      string
	WireGuardConf string
	BabelConf     string
	SysctlConf    string
	InstallScript string
}

// ExportResult 
type ExportResult struct {
	OutputDir string
	Nodes     []string
}

// Export 
func Export(result *compiler.CompileResult, outputDir string) (*ExportResult, error) {
	exportResult := &ExportResult{
		OutputDir: outputDir,
	}

	// 
	for _, node := range result.Topology.Nodes {
		nodeDir := filepath.Join(outputDir, node.Name)

		// 
		dirs := []string{
			filepath.Join(nodeDir, "wireguard"),
			filepath.Join(nodeDir, "babel"),
			filepath.Join(nodeDir, "sysctl"),
		}
		for _, dir := range dirs {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf(" %s : %w", dir, err)
			}
		}

		//  WireGuard 
		if wgConf, ok := result.WireGuardConfigs[node.ID]; ok {
			path := filepath.Join(nodeDir, "wireguard", "wg0.conf")
			if err := os.WriteFile(path, []byte(wgConf), 0600); err != nil {
				return nil, fmt.Errorf(" WireGuard : %w", err)
			}
		}

		//  Babel 
		if babelConf, ok := result.BabelConfigs[node.ID]; ok {
			path := filepath.Join(nodeDir, "babel", "babeld.conf")
			if err := os.WriteFile(path, []byte(babelConf), 0644); err != nil {
				return nil, fmt.Errorf(" Babel : %w", err)
			}
		}

		//  sysctl 
		if sysctlConf, ok := result.SysctlConfigs[node.ID]; ok {
			path := filepath.Join(nodeDir, "sysctl", "99-overlay.conf")
			if err := os.WriteFile(path, []byte(sysctlConf), 0644); err != nil {
				return nil, fmt.Errorf(" sysctl : %w", err)
			}
		}

		// 
		if script, ok := result.InstallScripts[node.ID]; ok {
			path := filepath.Join(nodeDir, "install.sh")
			if err := os.WriteFile(path, []byte(script), 0755); err != nil {
				return nil, fmt.Errorf(": %w", err)
			}
		}

		//  manifest.json
		var checksumLines []string
		if wgConf, ok := result.WireGuardConfigs[node.ID]; ok {
			checksumLines = append(checksumLines, fmt.Sprintf("%x  wireguard/wg0.conf", sha256.Sum256([]byte(wgConf))))
		}
		if babelConf, ok := result.BabelConfigs[node.ID]; ok {
			checksumLines = append(checksumLines, fmt.Sprintf("%x  babel/babeld.conf", sha256.Sum256([]byte(babelConf))))
		}
		if sysctlConf, ok := result.SysctlConfigs[node.ID]; ok {
			checksumLines = append(checksumLines, fmt.Sprintf("%x  sysctl/99-overlay.conf", sha256.Sum256([]byte(sysctlConf))))
		}

		//  checksums.sha256
		checksumsPath := filepath.Join(nodeDir, "checksums.sha256")
		if err := os.WriteFile(checksumsPath, []byte(strings.Join(checksumLines, "\n")), 0644); err != nil {
			return nil, fmt.Errorf(" checksums.sha256 : %w", err)
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
			"files": []string{
				"wireguard/wg0.conf",
				"babel/babeld.conf",
				"sysctl/99-overlay.conf",
				"install.sh",
			},
		}
		manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return nil, fmt.Errorf(" manifest : %w", err)
		}
		path := filepath.Join(nodeDir, "manifest.json")
		if err := os.WriteFile(path, manifestJSON, 0644); err != nil {
			return nil, fmt.Errorf(" manifest : %w", err)
		}

		//  README.txt
		readme := fmt.Sprintf("Node: %s\nOverlay IP: %s\nRole: %s\n\nUsage:\n  1. Copy this directory to the target host\n  2. Run: sudo bash install.sh\n",
			node.Name, node.OverlayIP, node.Role)
		readmePath := filepath.Join(nodeDir, "README.txt")
		if err := os.WriteFile(readmePath, []byte(readme), 0644); err != nil {
			return nil, fmt.Errorf(" README : %w", err)
		}

		exportResult.Nodes = append(exportResult.Nodes, node.Name)
	}

	return exportResult, nil
}
