package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/artifacts"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/renderer"
)

func main() {
	inputFile := flag.String("input", "", "输入拓扑 JSON 文件路径")
	outputDir := flag.String("output", "output", "输出产物目录")
	flag.Parse()

	if *inputFile == "" {
		fmt.Fprintln(os.Stderr, "错误: 必须指定输入文件 (-input)")
		flag.Usage()
		os.Exit(1)
	}

	// 读取拓扑文件
	data, err := os.ReadFile(*inputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取文件失败: %v\n", err)
		os.Exit(1)
	}

	var topo model.Topology
	if err := json.Unmarshal(data, &topo); err != nil {
		fmt.Fprintf(os.Stderr, "解析 JSON 失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("项目: %s (%s)\n", topo.Project.Name, topo.Project.ID)
	fmt.Printf("节点数: %d, 边数: %d, 网络域数: %d\n",
		len(topo.Nodes), len(topo.Edges), len(topo.Domains))

	// 生成假密钥（Phase 0 阶段用假密钥，后续使用 wg genkey）
	keys := generateFakeKeys(&topo)

	// 编译
	c := compiler.NewCompiler()
	result, err := c.Compile(&topo, keys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "编译失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("IP 分配完成:\n")
	for _, node := range result.Topology.Nodes {
		fmt.Printf("  %s -> %s\n", node.Name, node.OverlayIP)
	}

	// 渲染 WireGuard 配置
	wgConfigs, err := renderer.RenderAllWireGuardConfigs(result.Topology, result.PeerMap, keys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "渲染 WireGuard 配置失败: %v\n", err)
		os.Exit(1)
	}
	result.WireGuardConfigs = wgConfigs

	// 渲染 Babel 配置
	babelConfigs, err := renderer.RenderAllBabelConfigs(result.Topology, result.PeerMap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "渲染 Babel 配置失败: %v\n", err)
		os.Exit(1)
	}
	result.BabelConfigs = babelConfigs

	// 渲染 sysctl 配置
	sysctlConfigs, err := renderer.RenderAllSysctlConfigs(result.Topology)
	if err != nil {
		fmt.Fprintf(os.Stderr, "渲染 sysctl 配置失败: %v\n", err)
		os.Exit(1)
	}
	result.SysctlConfigs = sysctlConfigs

	// 渲染安装脚本
	for _, node := range result.Topology.Nodes {
		_, hasBabel := result.BabelConfigs[node.ID]
		script, err := renderer.RenderInstallScript(&node, hasBabel)
		if err != nil {
			fmt.Fprintf(os.Stderr, "渲染安装脚本失败: %v\n", err)
			os.Exit(1)
		}
		result.InstallScripts[node.ID] = script
	}

	// 导出产物
	exportResult, err := artifacts.Export(result, *outputDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "导出产物失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n产物已导出到: %s\n", exportResult.OutputDir)
	for _, nodeName := range exportResult.Nodes {
		fmt.Printf("  📦 %s/\n", nodeName)
	}
	fmt.Printf("\n编译完成! checksum: %s\n", result.Manifest.Checksum)
}

func generateFakeKeys(topo *model.Topology) map[string]compiler.KeyPair {
	keys := make(map[string]compiler.KeyPair)
	for _, node := range topo.Nodes {
		keys[node.ID] = compiler.KeyPair{
			PrivateKey: fmt.Sprintf("FAKE_PRIVKEY_%s", node.ID),
			PublicKey:  fmt.Sprintf("FAKE_PUBKEY_%s", node.ID),
		}
	}
	return keys
}
