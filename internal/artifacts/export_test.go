package artifacts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

func TestExport_BasicStructure(t *testing.T) {
	// 构建一个简单的编译结果
	result := &compiler.CompileResult{
		Topology: &model.Topology{
			Project: model.Project{ID: "test-001", Name: "Test", Version: "0.1.0"},
			Domains: []model.Domain{{ID: "d1", Name: "test", CIDR: "10.10.0.0/24", RoutingMode: "babel"}},
			Nodes: []model.Node{
				{ID: "n1", Name: "alpha", OverlayIP: "10.10.0.1", Role: "router", DomainID: "d1"},
				{ID: "n2", Name: "beta", OverlayIP: "10.10.0.2", Role: "peer", DomainID: "d1"},
			},
		},
		PeerMap: map[string][]compiler.PeerInfo{
			"n1": {{NodeID: "n2", NodeName: "beta"}},
			"n2": {{NodeID: "n1", NodeName: "alpha"}},
		},
		WireGuardConfigs: map[string]string{
			"n1": "[Interface]\nPrivateKey = test\n",
			"n2": "[Interface]\nPrivateKey = test\n",
		},
		BabelConfigs: map[string]string{
			"n1": "router-id alpha\n",
			"n2": "router-id beta\n",
		},
		SysctlConfigs: map[string]string{
			"n1": "net.ipv4.ip_forward = 1\n",
			"n2": "net.ipv4.conf.all.rp_filter = 2\n",
		},
		InstallScripts: map[string]string{
			"n1": "#!/bin/bash\necho install alpha\n",
			"n2": "#!/bin/bash\necho install beta\n",
		},
		Manifest: compiler.CompileManifest{
			ProjectID:   "test-001",
			ProjectName: "Test",
			Version:     "0.1.0",
			CompiledAt:  time.Now(),
			NodeCount:   2,
			Checksum:    "abc123",
		},
	}

	// 导出到临时目录
	outputDir := t.TempDir()
	exportResult, err := Export(result, outputDir)
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}

	// 检查导出了 2 个节点
	if len(exportResult.Nodes) != 2 {
		t.Errorf("期望导出 2 个节点, 得到 %d", len(exportResult.Nodes))
	}

	// 检查每个节点的文件结构
	for _, nodeName := range []string{"alpha", "beta"} {
		nodeDir := filepath.Join(outputDir, nodeName)

		expectedFiles := []string{
			"wireguard/wg0.conf",
			"babel/babeld.conf",
			"sysctl/99-overlay.conf",
			"install.sh",
			"manifest.json",
			"README.txt",
		}

		for _, f := range expectedFiles {
			path := filepath.Join(nodeDir, f)
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Errorf("节点 %s 缺少文件: %s", nodeName, f)
			}
		}
	}
}

func TestExport_WireGuardPermissions(t *testing.T) {
	result := minimalCompileResult()
	outputDir := t.TempDir()

	_, err := Export(result, outputDir)
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}

	// WireGuard 配置文件应为 0600
	wgPath := filepath.Join(outputDir, "alpha", "wireguard", "wg0.conf")
	info, err := os.Stat(wgPath)
	if err != nil {
		t.Fatalf("获取文件信息失败: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("WireGuard 配置文件权限期望 0600, 得到 %o", perm)
	}
}

func TestExport_InstallScriptExecutable(t *testing.T) {
	result := minimalCompileResult()
	outputDir := t.TempDir()

	_, err := Export(result, outputDir)
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}

	// 安装脚本应可执行
	scriptPath := filepath.Join(outputDir, "alpha", "install.sh")
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("获取文件信息失败: %v", err)
	}

	perm := info.Mode().Perm()
	if perm&0100 == 0 {
		t.Errorf("安装脚本应可执行, 当前权限: %o", perm)
	}
}

func TestExport_ManifestContent(t *testing.T) {
	result := minimalCompileResult()
	outputDir := t.TempDir()

	_, err := Export(result, outputDir)
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}

	// 读取 manifest
	manifestPath := filepath.Join(outputDir, "alpha", "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("读取 manifest 失败: %v", err)
	}

	var manifest map[string]interface{}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("解析 manifest 失败: %v", err)
	}

	if manifest["node_id"] != "n1" {
		t.Errorf("manifest node_id 期望 n1, 得到 %v", manifest["node_id"])
	}
	if manifest["overlay_ip"] != "10.10.0.1" {
		t.Errorf("manifest overlay_ip 期望 10.10.0.1, 得到 %v", manifest["overlay_ip"])
	}
	if manifest["project_id"] != "test-001" {
		t.Errorf("manifest project_id 期望 test-001, 得到 %v", manifest["project_id"])
	}
}

func minimalCompileResult() *compiler.CompileResult {
	return &compiler.CompileResult{
		Topology: &model.Topology{
			Project: model.Project{ID: "test-001", Name: "Test", Version: "0.1.0"},
			Nodes: []model.Node{
				{ID: "n1", Name: "alpha", OverlayIP: "10.10.0.1", Role: "router", DomainID: "d1"},
			},
		},
		PeerMap:          map[string][]compiler.PeerInfo{"n1": {}},
		WireGuardConfigs: map[string]string{"n1": "[Interface]\nPrivateKey = test\n"},
		BabelConfigs:     map[string]string{"n1": "router-id alpha\n"},
		SysctlConfigs:    map[string]string{"n1": "net.ipv4.ip_forward = 1\n"},
		InstallScripts:   map[string]string{"n1": "#!/bin/bash\necho install\n"},
		Manifest: compiler.CompileManifest{
			ProjectID:   "test-001",
			ProjectName: "Test",
			Version:     "0.1.0",
			CompiledAt:  time.Now(),
			NodeCount:   1,
			Checksum:    "abc123",
		},
	}
}
