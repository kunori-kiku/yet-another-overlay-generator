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
	inputFile := flag.String("input", "", " JSON ")
	outputDir := flag.String("output", "output", "")
	flag.Parse()

	if *inputFile == "" {
		fmt.Fprintln(os.Stderr, ":  (-input)")
		flag.Usage()
		os.Exit(1)
	}

	// 
	data, err := os.ReadFile(*inputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, ": %v\n", err)
		os.Exit(1)
	}

	var topo model.Topology
	if err := json.Unmarshal(data, &topo); err != nil {
		fmt.Fprintf(os.Stderr, " JSON : %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("项目: %s (%s)\n", topo.Project.Name, topo.Project.ID)
	fmt.Printf("节点数: %d, 边数: %d, 网络域数: %d\n",
		len(topo.Nodes), len(topo.Edges), len(topo.Domains))

	// （Phase 0 ， wg genkey）
	keys := generateFakeKeys(&topo)

	// 编译拓扑
	c := compiler.NewCompiler()
	result, err := c.Compile(&topo, keys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "编译失败: %v\n", err)
		os.Exit(1)
	}

	// 编译成功后仍需向用户展示的非致命告警（NAT 不可达、无 endpoint 的边、孤立节点等）。
	// 这些告警与 API /api/compile 返回的 warnings 同源，确保 CLI 与 API 行为一致。
	if len(result.Warnings) > 0 {
		fmt.Fprintf(os.Stderr, "\n编译告警 (%d 条):\n", len(result.Warnings))
		for _, w := range result.Warnings {
			fmt.Fprintf(os.Stderr, "  [%s] %s: %s\n", w.Level, w.Field, w.Message)
		}
		fmt.Fprintln(os.Stderr)
	}

	fmt.Printf("IP 分配结果:\n")
	for _, node := range result.Topology.Nodes {
		fmt.Printf("  %s -> %s\n", node.Name, node.OverlayIP)
	}

	//  WireGuard 
	wgConfigs, err := renderer.RenderAllWireGuardConfigs(result.Topology, result.PeerMap, keys)
	if err != nil {
		fmt.Fprintf(os.Stderr, " WireGuard : %v\n", err)
		os.Exit(1)
	}
	result.WireGuardConfigs = wgConfigs

	//  Babel 
	babelConfigs, err := renderer.RenderAllBabelConfigs(result.Topology, result.PeerMap)
	if err != nil {
		fmt.Fprintf(os.Stderr, " Babel : %v\n", err)
		os.Exit(1)
	}
	result.BabelConfigs = babelConfigs

	//  sysctl 
	sysctlConfigs, err := renderer.RenderAllSysctlConfigs(result.Topology)
	if err != nil {
		fmt.Fprintf(os.Stderr, " sysctl : %v\n", err)
		os.Exit(1)
	}
	result.SysctlConfigs = sysctlConfigs

	// 
	for _, node := range result.Topology.Nodes {
		_, hasBabel := result.BabelConfigs[node.ID]
		transitCIDRs := renderer.NodeTransitCIDRs(result.Topology, &node)
		script, err := renderer.RenderInstallScript(&node, result.PeerMap[node.ID], hasBabel, transitCIDRs...)
		if err != nil {
			fmt.Fprintf(os.Stderr, ": %v\n", err)
			os.Exit(1)
		}
		result.InstallScripts[node.ID] = script
	}

	// 
	exportResult, err := artifacts.Export(result, *outputDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, ": %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n: %s\n", exportResult.OutputDir)
	for _, nodeName := range exportResult.Nodes {
		fmt.Printf("  📦 %s/\n", nodeName)
	}
	fmt.Printf("\n! checksum: %s\n", result.Manifest.Checksum)
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
